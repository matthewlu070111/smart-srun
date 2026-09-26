#!/usr/bin/env python3
"""Build Go packages with pinned OpenWrt SDK inputs (Linux, Python 3.12+).

Each target owns a separate workspace. Output is a build record, not release
acceptance: installation, emulation and hardware evidence must be added later.
APK signing accepts an existing maintainer key; it never creates a release key.
"""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

try:
    import fcntl
except ImportError:  # Helpers can be tested on Windows; the SDK runs on Linux.
    fcntl = None


REPO = Path(__file__).resolve().parents[1]
PACKAGES = ("smart-srun", "luci-app-smart-srun", "luci-app-smart-srun-bundle")
SOURCE_PATHS = (
    "Makefile", "core/go.mod", "core/go.sum", "core/cmd", "core/internal",
    "root/etc/init.d/smart_srun", "root/lib/upgrade/keep.d/smart-srun",
    "root/etc/init.d/smart_srun_update",
    "root/usr/share/smart-srun/third-party-licenses.txt",
    "root/usr/lib/lua/luci/controller/smart_srun.lua",
    "root/usr/lib/lua/luci/model/cbi/smart_srun.lua",
    "root/usr/lib/lua/luci/smart_srun",
    "root/www/luci-static/resources/smart_srun.js", "doc/school-presets.json",
)
FEED_URLS = {
    "base": "https://github.com/openwrt/openwrt.git",
    "packages": "https://github.com/openwrt/packages.git",
    "luci": "https://github.com/openwrt/luci.git",
}
CORE_FILES = {
    "usr/bin/srunnet", "etc/init.d/smart_srun", "lib/upgrade/keep.d/smart-srun",
    "etc/init.d/smart_srun_update",
    "usr/share/smart-srun/school-presets.json", "usr/share/smart-srun/third-party-licenses.txt",
}
LUCI_FILES = {
    "usr/lib/lua/luci/controller/smart_srun.lua", "usr/lib/lua/luci/model/cbi/smart_srun.lua",
    "usr/lib/lua/luci/smart_srun/rpc.lua", "usr/lib/lua/luci/smart_srun/schema.lua",
    "usr/lib/lua/luci/smart_srun/bridge.lua", "www/luci-static/resources/smart_srun.js",
}


def validate_payload(package, files, limit):
    expected = set()
    if package in ("smart-srun", "luci-app-smart-srun-bundle"):
        expected |= CORE_FILES
    if package in ("luci-app-smart-srun", "luci-app-smart-srun-bundle"):
        expected |= LUCI_FILES
    metadata = {f"lib/apk/packages/{package}.{suffix}" for suffix in ("list", "conffiles", "conffiles_static")}
    metadata.add("lib/upgrade/keep.d/" + package)
    if package not in PACKAGES or not expected <= files.keys() or files.keys() - expected - metadata:
        raise ValueError("Package payload differs from the explicit runtime file list")
    if any(not isinstance(size, int) or size < 0 for size in files.values()) or sum(files.values()) > limit:
        # The limit is reported rather than spelled out: it comes from
        # targets.json, and a literal here said "10 MiB" for as long as that was
        # true and would have gone on saying it afterwards.
        raise ValueError(f"Installed payload exceeds the {limit} byte budget or has an invalid size")


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def command(args, cwd=None, log=None, env=None):
    result = subprocess.run([str(arg) for arg in args], cwd=cwd, env=env,
                            stdout=log or subprocess.PIPE, stderr=subprocess.STDOUT)
    if result.returncode:
        detail = result.stdout.decode(errors="replace")[-4000:] if result.stdout else "See build.log"
        raise RuntimeError(f"{args[0]} failed ({result.returncode}): {detail}")
    return result.stdout.decode() if result.stdout is not None else ""


def package_version(display, manager):
    match = re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:rc([1-9]\d*))?", display)
    if not match or manager not in ("opkg", "apk"):
        raise ValueError("Expected X.Y.Z or X.Y.ZrcN and an opkg/apk target")
    base = ".".join(match.group(index) for index in (1, 2, 3))
    return base + (("~rc" if manager == "opkg" else "_rc") + match[4] if match[4] else "")


def load_target(path, target_id):
    catalog = json.loads(path.read_text())
    found = [target for target in catalog["targets"] if target["id"] == target_id]
    if catalog.get("schema_version") != 1 or len(found) != 1:
        raise ValueError("Target must occur exactly once in targets.json schema 1")
    target = found[0]
    if not re.fullmatch(r"[a-zA-Z0-9_.-]+", target_id):
        raise ValueError("Unsafe target id")
    for value in [target["sdk_sha256"], catalog["go_source_sha256"]]:
        if not re.fullmatch(r"[0-9a-f]{64}", value):
            raise ValueError("Missing immutable download digest")
    for value in [catalog["golang_framework_commit"], *target["feeds"].values()]:
        if not re.fullmatch(r"[0-9a-f]{40}", value):
            raise ValueError("Feed must use a full commit")
    if not target["sdk_url"].startswith("https://downloads.openwrt.org/releases/"):
        raise ValueError("SDK must come from the official immutable release directory")
    return catalog, target


def download(url, destination, sha256):
    if destination.exists():
        if digest(destination) != sha256:
            raise ValueError("Cached SDK digest mismatch")
        return
    temporary = destination.with_suffix(".part")
    with urllib.request.urlopen(url, timeout=60) as response, temporary.open("wb") as out:
        total = 0
        while chunk := response.read(1024 * 1024):
            total += len(chunk)
            if total > 2 * 1024**3:
                raise ValueError("SDK download exceeds 2 GiB")
            out.write(chunk)
    if digest(temporary) != sha256:
        raise ValueError("Downloaded SDK digest mismatch")
    temporary.replace(destination)


def sdk_archive_filter(root_name):
    def filter_member(member, destination):
        path = PurePosixPath(member.name)
        if path.is_absolute() or ".." in path.parts or not path.parts or path.parts[0] != root_name:
            raise ValueError("SDK archive member escapes its expected root")
        # Official SDKs carry links to their original builder's host tools.
        # Do not extract these external links. OpenWrt's prerequisite checks
        # recreate them using tools on this host before any feed scan/build.
        if (member.issym() and PurePosixPath(member.linkname).is_absolute()
                and len(path.parts) == 5 and path.parts[1:4] == ("staging_dir", "host", "bin")):
            return None
        return tarfile.data_filter(member, destination)
    return filter_member


def prepare_sdk(work, catalog, target, log):
    sdk = work / "sdk"
    marker = work / "inputs.json"
    inputs = {"target": target, "go_version": catalog["go_version"],
              "go_source_sha256": catalog["go_source_sha256"],
              "golang_framework_commit": catalog["golang_framework_commit"]}
    if marker.exists():
        if json.loads(marker.read_text()) != inputs:
            raise ValueError("Workspace has different pinned inputs; use another work directory")
        return sdk
    if sdk.exists():
        raise ValueError("Incomplete SDK workspace; inspect it and use a fresh work directory")
    archive = work / Path(target["sdk_url"]).name
    download(target["sdk_url"], archive, target["sdk_sha256"])
    with tempfile.TemporaryDirectory(prefix="extract-", dir=work) as temporary:
        process = subprocess.Popen(["zstd", "-dc", str(archive)], stdout=subprocess.PIPE)
        try:
            with tarfile.open(fileobj=process.stdout, mode="r|") as tar:
                tar.extractall(temporary, filter=sdk_archive_filter(archive.name.removesuffix(".tar.zst")))
            process.stdout.close()
            if process.wait() != 0:
                raise ValueError("SDK decompression failed")
        finally:
            process.stdout.close()
            if process.poll() is None:
                process.terminate()
            process.wait()
        directories = list(Path(temporary).iterdir())
        if len(directories) != 1 or not (directories[0] / "include/package.mk").is_file():
            raise ValueError("Unexpected SDK archive layout")
        directories[0].rename(sdk)
    # The archive's successful check belongs to its builder, not this host.
    (sdk / "staging_dir/host/.prereq-build").unlink(missing_ok=True)
    lines = []
    for name, url in FEED_URLS.items():
        option = " --root=package" if name == "base" and target.get("base_feed_subdir") == "package" else ""
        lines.append(f"src-git{option} {name} {url}^{target['feeds'][name]}")
    (sdk / "feeds.conf").write_text("\n".join(lines) + "\n")
    command(["./scripts/feeds", "update", *FEED_URLS], sdk, log)
    overlay = work / "golang-framework"
    command(["git", "init", overlay], log=log)
    command(["git", "fetch", "--depth=1", FEED_URLS["packages"], catalog["golang_framework_commit"]], overlay, log)
    command(["git", "checkout", "--detach", "FETCH_HEAD"], overlay, log)
    go_makefile = overlay / f"lang/golang/golang{'.'.join(catalog['go_version'].split('.')[:2])}/Makefile"
    if catalog["go_source_sha256"] not in go_makefile.read_text():
        raise ValueError("Go framework source hash does not match targets.json")
    destination = sdk / "feeds/packages/lang/golang"
    # This tree was created above from a verified SDK and pinned feeds, and is
    # owned by this build. Never accept a caller-supplied path for deletion.
    if not destination.resolve().is_relative_to(work):
        raise ValueError("Go overlay escapes the build workspace")
    shutil.rmtree(destination)
    shutil.copytree(overlay / "lang/golang", destination)
    command(["./scripts/feeds", "update", "-i", "packages"], sdk, log)
    go_package = "golang" + ".".join(catalog["go_version"].split(".")[:2])
    command(["./scripts/feeds", "install", go_package, "luci-base", "luci-compat",
             "ca-bundle", "uci", "ubus", "procd", "rpcd", "rpcd-mod-iwinfo"], sdk, log)
    marker.write_text(json.dumps(inputs, indent=2) + "\n")
    return sdk


def copy_source(repo, destination, version):
    if destination.exists():
        shutil.rmtree(destination)
    destination.mkdir(parents=True)
    files, templates = {}, {}
    for relative in SOURCE_PATHS:
        source = repo / relative
        entries = sorted(source.rglob("*")) if source.is_dir() else [source]
        for entry in entries:
            if entry.is_symlink():
                raise ValueError("Source symlinks are not accepted")
            if not entry.is_file():
                continue
            name = entry.relative_to(repo).as_posix()
            data = entry.read_bytes().replace(b"\r\n", b"\n")
            templates[name] = hashlib.sha256(data).hexdigest()
            if name == "Makefile":
                if data.count(b"PKG_VERSION:=0.0.0\n") != 1:
                    raise ValueError("Missing Makefile version placeholder")
                data = data.replace(b"PKG_VERSION:=0.0.0\n", f"PKG_VERSION:={version}\n".encode())
            output = destination / name
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_bytes(data)
            files[name] = hashlib.sha256(data).hexdigest()
    return files, templates


def sign_apk(apk, path, private_key, public_key):
    with tempfile.TemporaryDirectory(prefix="smart-srun-verify-") as temporary:
        keys = Path(temporary)
        shutil.copyfile(public_key, keys / "smart-srun.pem")
        # adbsign 3.0.5 returns zero even when refusing unsigned input. The
        # input-only exception is confined to signing our SDK output; verify
        # and package installation always require a trusted signature.
        output = command([apk, "adbsign", "--allow-untrusted", "--reset-signatures",
                          "--sign-key", private_key, path])
        if output.strip():
            raise RuntimeError("APK signer reported an error: " + output)
        command([apk, "--keys-dir", keys, "verify", path])
    der = subprocess.check_output(["openssl", "pkey", "-pubin", "-in", str(public_key), "-outform", "DER"])
    return hashlib.sha256(der).hexdigest()


def package_info(path, apk):
    if path.suffix == ".apk":
        data = json.loads(command([apk, "adbdump", "--format", "json", path]))
        info = data["info"]
        files = {str(Path(directory.get("name", "")) / entry["name"]): entry["size"]
                 for directory in data["paths"] for entry in directory.get("files", [])}
        return info["name"], info["version"], info["arch"], files
    with tarfile.open(path) as outer:
        with tarfile.open(fileobj=io.BytesIO(outer.extractfile("./control.tar.gz").read())) as control:
            text = control.extractfile("./control").read().decode()
            fields = dict(line.split(": ", 1) for line in text.splitlines() if ": " in line)
        with tarfile.open(fileobj=io.BytesIO(outer.extractfile("./data.tar.gz").read())) as data:
            files = {member.name.removeprefix("./"): member.size for member in data if member.isfile()}
    return fields["Package"], fields["Version"], fields["Architecture"], files


def build(args):
    if fcntl is None:
        raise RuntimeError("OpenWrt SDK builds require Linux")
    catalog, target = load_target(args.targets, args.target)
    version = package_version(args.version, target["package_manager"])
    work = (args.work_dir.resolve() / target["id"])
    if any(char.isspace() for char in str(work)):
        raise ValueError("OpenWrt SDK work path must not contain whitespace")
    work.mkdir(parents=True, exist_ok=True)
    with (work / ".lock").open("w") as lock, (work / "build.log").open("a") as log:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        artifacts = work / "artifacts" / args.version
        if artifacts.exists():
            raise ValueError("Artifact directory already exists; never overwrite recorded artifacts")
        sdk = prepare_sdk(work, catalog, target, log)
        for name, commit in target["feeds"].items():
            if command(["git", "rev-parse", "HEAD"], sdk / "feeds" / name).strip() != commit:
                raise ValueError("Feed checkout changed: " + name)
        files, templates = copy_source(REPO, sdk / "package/luci-app-smart-srun", version)
        source_commit = command(["git", "rev-parse", "HEAD"], REPO).strip()
        dirty = bool(command(["git", "status", "--porcelain"], REPO).strip())
        epoch = command(["git", "show", "-s", "--format=%ct", "HEAD"], REPO).strip()
        bootstrap = args.bootstrap.resolve()
        if not (bootstrap / "bin/go").is_file():
            raise ValueError("An external Go bootstrap toolchain is required")
        if not re.fullmatch(r"[/a-zA-Z0-9_.-]+", str(bootstrap)):
            raise ValueError("Unsafe bootstrap path")
        board, subtarget = target["target"].split("/")
        config = [f"CONFIG_TARGET_{board}=y", f"CONFIG_TARGET_{board}_{subtarget}=y",
                  "CONFIG_DEVEL=y", "# CONFIG_ALL is not set", "# CONFIG_ALL_KMODS is not set",
                  "# CONFIG_ALL_NONSHARED is not set", "# CONFIG_GOLANG_BUILD_BOOTSTRAP is not set",
                  f'CONFIG_GOLANG_EXTERNAL_BOOTSTRAP_ROOT="{bootstrap}"']
        config += [f"CONFIG_PACKAGE_{package}=m" for package in PACKAGES]
        (sdk / ".config").write_text("\n".join(config) + "\n")
        output = command(["make", "defconfig"], sdk)
        log.write(output)
        log.flush()
        if "recursive dependency detected" in output or re.search(r"(?:^|\n).*:error:", output):
            raise ValueError("SDK configuration failed even though make returned zero")
        env = dict(os.environ, SOURCE_DATE_EPOCH=epoch, GOTOOLCHAIN="local")
        command(["make", "package/luci-app-smart-srun/clean", "V=s"], sdk, log, env)
        command(["make", "package/luci-app-smart-srun/compile", f"-j{args.jobs}", "V=s",
                 "SMARTSRUN_DISPLAY_VERSION=" + args.version, "SOURCE_DATE_EPOCH=" + epoch], sdk, log, env)
        go_root = sdk / "staging_dir/hostpkg/lib" / ("go-" + ".".join(catalog["go_version"].split(".")[:2]))
        go_actual = command([go_root / "bin/go", "version"], env=env).strip()
        if f"go{catalog['go_version']} " not in go_actual:
            raise ValueError("SDK used a different Go compiler: " + go_actual)
        # A failed collection has no completed record. Keep its temporary
        # output for diagnosis, but reserve the immutable version name only
        # after every package passed validation.
        artifacts.parent.mkdir(parents=True, exist_ok=True)
        stage = Path(tempfile.mkdtemp(prefix=".collect-", dir=artifacts.parent))
        apk = sdk / "staging_dir/host/bin/apk"
        result = []
        for package in PACKAGES:
            pattern = f"{package}_{version}-*.ipk" if target["format"] == "ipk" else f"{package}-{version}-*.apk"
            candidates = list((sdk / "bin").rglob(pattern))
            if len(candidates) != 1:
                raise ValueError("Expected exactly one SDK artifact for " + package)
            path = stage / candidates[0].name
            shutil.copyfile(candidates[0], path)
            fingerprint = None
            if path.suffix == ".apk" and args.sign_key:
                fingerprint = sign_apk(apk, path, args.sign_key, args.public_key)
            name, native_version, arch, payload = package_info(path, apk)
            expected_arch = ("all" if target["format"] == "ipk" else "noarch") if package == "luci-app-smart-srun" else target["openwrt_arch"]
            if name != package or arch != expected_arch:
                raise ValueError("Package name or architecture mismatch")
            validate_payload(package, payload, catalog["development_payload_limit_bytes"])
            result.append(dict(file=path.name, name=name, package_version=native_version,
                               architecture=arch, bytes=path.stat().st_size, sha256=digest(path),
                               installed_payload_bytes=sum(payload.values()), files=payload,
                               signature_spki_sha256=fingerprint, validation={"build": True}))
        record = dict(schema_version=1, display_version=args.version, source_commit=source_commit,
                      source_dirty=dirty, source_files=files, source_template_files=templates,
                      source_date_epoch=int(epoch),
                      target=target, go_version=catalog["go_version"], go_compiler=go_actual,
                      golang_framework_commit=catalog["golang_framework_commit"], artifacts=result)
        (stage / "build-record.json").write_text(json.dumps(record, indent=2) + "\n")
        (stage / "SHA256SUMS").write_text("".join(f"{asset['sha256']}  {asset['file']}\n" for asset in result))
        stage.rename(artifacts)
        print(artifacts)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--targets", type=Path, default=REPO / "targets.json")
    parser.add_argument("--target", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--work-dir", type=Path, required=True)
    parser.add_argument("--bootstrap", type=Path, required=True, help="Existing Go toolchain root")
    parser.add_argument("--jobs", type=int, choices=range(1, 33), default=2)
    parser.add_argument("--sign-key", type=Path)
    parser.add_argument("--public-key", type=Path)
    args = parser.parse_args()
    if bool(args.sign_key) != bool(args.public_key):
        parser.error("APK signing requires both --sign-key and --public-key")
    build(args)


if __name__ == "__main__":
    main()
