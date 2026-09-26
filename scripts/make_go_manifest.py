#!/usr/bin/env python3
"""Create immutable Go release metadata from measured SDK build records.

Additional validation is accepted only as a report keyed by the package's
actual SHA256. A successful build never implies installation or campus testing.
Dirty builds require an explicit internal-test option and are not publishable.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

REPOSITORY = "matthewlu070111/smart-srun"
KINDS = {"smart-srun": "core", "luci-app-smart-srun": "luci", "luci-app-smart-srun-bundle": "bundle"}
CHECKS = ("build", "elf", "emulated_core", "openwrt_install", "hardware_core", "campus_auth")


def firmware_compatibility(evidence):
    # Forks may keep opkg after upstream moved to APK. Keep the build's SDK
    # identity intact and accept extra families only from exact-byte native
    # installation reports, never from a firmware label or CPU/model guess.
    reports = evidence.get("firmware_compatibility", {})
    if not isinstance(reports, dict):
        raise ValueError("Invalid firmware compatibility evidence")
    result = {}
    for digest, families in reports.items():
        if not isinstance(digest, str) or not re.fullmatch("[0-9a-f]{64}", digest) or not isinstance(families, dict) or not 0 < len(families) <= 16:
            raise ValueError("Invalid firmware compatibility evidence")
        for family, report in families.items():
            if not isinstance(family, str) or not re.fullmatch(r"\d{2}\.\d{2}", family) or report != {"openwrt_install": True} or type(report.get("openwrt_install")) is not bool:
                raise ValueError("Extra firmware families require successful native installation evidence")
        result[digest] = set(families)
    return result


def release_filename(package, native_version, architecture, sdk, fmt):
    # Native APK filenames omit architecture. Both formats also omit the SDK,
    # so never flatten their original names into a multi-target Release.
    # GitHub rewrites '~' during asset upload. Keep the native package version
    # intact in its metadata, but use the upload-safe spelling in every URL,
    # checksum entry and split archive member from the beginning.
    filename_version = native_version.replace("~", ".")
    separator = "_" if fmt == "ipk" else "-"
    name = f"{package}{separator}{filename_version}_{architecture}_openwrt-{sdk}.{fmt}"
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,199}", name):
        raise ValueError("Release filename is not safe for GitHub asset upload")
    return name


def apk_contents(path, apk, keys, version):
    """Compare signed APK content without treating randomized signatures as payload."""
    if not apk or not keys:
        raise ValueError("Non-identical LuCI APKs require --apk and --keys for native content verification")
    subprocess.run([str(apk), "--keys-dir", str(keys), "verify", str(path)],
                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, check=True, timeout=30)
    # Native metadata includes file hashes, attributes, dependencies and scripts.
    # Keep it bounded and verify the full archive first; matching sizes or source
    # inputs alone do not prove two generated packages have the same contents.
    with tempfile.TemporaryFile() as output:
        subprocess.run([str(apk), "adbdump", "--format", "json", str(path)],
                       stdout=output, stderr=subprocess.PIPE, check=True, timeout=30)
        output.seek(0)
        data = output.read((256 << 10) + 1)
    if len(data) > 256 << 10:
        raise ValueError("Native LuCI APK metadata exceeds the comparison limit")
    document = json.loads(data)
    if not isinstance(document, dict) or not isinstance(document.get("info"), dict) or not document.get("paths"):
        raise ValueError("Native LuCI APK metadata is incomplete")
    info = document["info"]
    if (info.get("name"), info.get("arch"), info.get("version")) != ("luci-app-smart-srun", "noarch", version):
        raise ValueError("Native LuCI APK identity differs from its build record")
    return document


def generate(records, evidence=None, internal=False, *, package_sources=None, apk=None, keys=None):
    evidence = evidence or {"schema_version": 1, "assets": {}}
    if evidence.get("schema_version") != 1 or not isinstance(evidence.get("assets"), dict):
        raise ValueError("Invalid validation evidence")
    compatibility = firmware_compatibility(evidence)
    manifest = None
    source_files = None
    selections, names, sources, device_selections = {}, {}, {}, set()
    for path in records:
        path = Path(path)
        record = json.loads(path.read_text(encoding="utf-8"))
        version = record["display_version"]
        if not re.fullmatch(r"(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:rc[1-9]\d*)?", version):
            raise ValueError("Invalid display version")
        if int(version.split(".")[0]) < 2 or not re.fullmatch("[0-9a-f]{40}", record["source_commit"]):
            raise ValueError("Not a Go release source")
        if record.get("source_dirty") is not False and not internal:
            raise ValueError("Dirty source is allowed only for explicit internal tests")
        # Native RC spelling changes Makefile (~rc for opkg, _rc for APK).
        # Compare the measured inputs before that one deliberate substitution,
        # while retaining the SDK's exact staged file hashes in the build record.
        files = record.get("source_template_files")
        if not isinstance(files, dict) or not 2 <= len(files) <= 4096 or not {"Makefile", "core/go.mod"} <= files.keys():
            raise ValueError("Missing measured source file identities")
        for name, digest in files.items():
            if not isinstance(name, str) or name.startswith("/") or "\\" in name or ":" in name or any(part in ("", ".", "..") for part in name.split("/")) or not isinstance(digest, str) or not re.fullmatch("[0-9a-f]{64}", digest):
                raise ValueError("Invalid measured source file identity")
        staged = record.get("source_files")
        if not isinstance(staged, dict) or staged.keys() != files.keys() or any(
                not isinstance(digest, str) or not re.fullmatch("[0-9a-f]{64}", digest)
                or (name != "Makefile" and files[name] != digest) for name, digest in staged.items()):
            raise ValueError("Unexpected SDK source substitution")
        if source_files is None:
            source_files = files
        elif source_files != files:
            raise ValueError("SDK builds do not contain the same source files")
        if manifest is None:
            manifest = dict(schema_version=1, release=version, channel="rc" if "rc" in version else "stable",
                            source_commit=record["source_commit"], assets=[])
        if manifest["release"] != version or manifest["source_commit"] != record["source_commit"]:
            raise ValueError("Build records do not share a release and source commit")
        target = record["target"]
        if target.get("goos") != "linux" or not isinstance(target.get("goarch"), str) or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", target["goarch"]) or not re.fullmatch(r"[a-z0-9_-]+/[a-z0-9_-]+", target.get("target", "")) or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", target.get("openwrt_arch", "")) or target["openwrt_arch"] in ("all", "noarch"):
            raise ValueError("Invalid native SDK target identity")
        manager, fmt = target["package_manager"], target["format"]
        if (manager, fmt) not in (("opkg", "ipk"), ("apk", "apk")):
            raise ValueError("Inconsistent package format")
        sdk = target["sdk_release"]
        if not re.fullmatch(r"\d{2}\.\d{2}\.\d+", sdk):
            raise ValueError("Unpinned SDK release")
        family = sdk.rsplit(".", 1)[0]
        for artifact in record["artifacts"]:
            name = artifact["file"]
            if Path(name).name != name or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.~+-]{0,199}", name):
                raise ValueError("Unsafe artifact filename")
            file = path.parent / name
            if not file.is_file() or file.is_symlink():
                raise ValueError("Missing regular artifact")
            with file.open("rb") as stream:
                digest = hashlib.file_digest(stream, "sha256").hexdigest()
            size = file.stat().st_size
            if digest != artifact["sha256"] or size != artifact["bytes"] or not 0 < size <= 16 * 1024**2:
                raise ValueError("Artifact bytes or checksum changed after build")
            installed = artifact["installed_payload_bytes"]
            if type(installed) is not int or not isinstance(artifact["files"], dict) or any(type(size) is not int or size < 0 for size in artifact["files"].values()) or sum(artifact["files"].values()) != installed or not 0 < installed <= 10 * 1024**2:
                raise ValueError("Invalid measured installed payload")
            kind = KINDS[artifact["name"]]
            separator = "_" if fmt == "ipk" else "-"
            if not name.startswith(artifact["name"] + separator) or not name.endswith("." + fmt):
                raise ValueError("Artifact filename does not match package identity")
            architecture = artifact["architecture"]
            expected = ("all" if manager == "opkg" else "noarch") if kind == "luci" else target["openwrt_arch"]
            if architecture != expected:
                raise ValueError("Package architecture does not match its target/kind")
            native = version.replace("rc", "~rc" if manager == "opkg" else "_rc")
            if not re.fullmatch(re.escape(native) + r"-r[1-9]\d{0,8}", artifact["package_version"]):
                raise ValueError("Unexpected native package version")
            expected_name = (f"{artifact['name']}_{artifact['package_version']}_{architecture}.ipk" if fmt == "ipk"
                             else f"{artifact['name']}-{artifact['package_version']}.apk")
            if name != expected_name:
                raise ValueError("Filename does not identify the exact native package")
            if manager == "apk" and not re.fullmatch("[0-9a-f]{64}", artifact.get("signature_spki_sha256") or ""):
                raise ValueError("APK must have verified signing evidence")
            if artifact.get("validation", {}).get("build") is not True:
                raise ValueError("Build did not pass")
            checked = evidence["assets"].get(digest, {})
            if any(key not in CHECKS or type(value) is not bool for key, value in checked.items()):
                raise ValueError("Invalid validation levels")
            validation = {key: checked.get(key, False) for key in CHECKS}
            validation["build"] = True
            identifier = f"{kind}-{manager}-{architecture}-{family}"
            export_name = release_filename(artifact["name"], artifact["package_version"], architecture, sdk, fmt)
            if len(export_name) > 200:
                raise ValueError("Release artifact filename exceeds the consumer limit")
            asset = dict(id=identifier, kind=kind, package_manager=manager, format=fmt,
                         openwrt_arch=architecture, package_version=artifact["package_version"],
                         sdk_release=sdk, target=target["target"], goos=target["goos"], goarch=target["goarch"],
                         url=f"https://github.com/{REPOSITORY}/releases/download/{version}/{export_name}",
                         sha256=digest, bytes=size, installed_bytes=installed,
                         firmware_compat=sorted({family} | compatibility.get(digest, set())), validation=validation)
            if identifier in selections:
                previous = selections[identifier]
                if kind != "luci" or any(previous[key] != asset[key] for key in
                                        ("package_version", "installed_bytes", "validation", "sdk_release", "firmware_compat")):
                    raise ValueError("Ambiguous packages for the same device selection")
                if previous["sha256"] != digest:
                    previous_name = previous["url"].rsplit("/", 1)[1]
                    native_version = asset["package_version"]
                    if fmt != "apk" or apk_contents(sources[previous_name], apk, keys, native_version) != apk_contents(file, apk, keys, native_version):
                        raise ValueError("Ambiguous packages for the same device selection")
                # ECDSA signatures differ even for identical noarch payloads.
                # Keep the FIRST verified archive's exact bytes/hash/evidence;
                # never transfer acceptance from the discarded signature variant.
                continue  # identical architecture-neutral LuCI from another SDK target
            compatible = {(kind, manager, architecture, item) for item in asset["firmware_compat"]}
            if device_selections & compatible:
                raise ValueError("Ambiguous packages for overlapping firmware compatibility")
            device_selections.update(compatible)
            if export_name in names:
                raise ValueError("Duplicate release asset filename")
            selections[identifier], names[export_name] = asset, digest
            sources[export_name] = file
            manifest["assets"].append(asset)
    if manifest is None or not manifest["assets"]:
        raise ValueError("No artifacts")
    if package_sources is not None:
        package_sources.update(sources)
    return manifest, "".join(f"{digest}  {name}\n" for name, digest in sorted(names.items()))


def export_release(records, output, evidence=None, internal=False, *, apk=None, keys=None):
    sources = {}
    manifest, sums = generate(records, evidence, internal, package_sources=sources, apk=apk, keys=keys)
    output = Path(output)
    if output.exists() or output.is_symlink():
        raise ValueError("Refusing to replace an existing release directory")
    output.parent.mkdir(parents=True, exist_ok=True)
    document = json.dumps(manifest, ensure_ascii=False, indent=2) + "\n"
    manifest_hash = hashlib.sha256(document.encode()).hexdigest()
    with tempfile.TemporaryDirectory(prefix=".smart-srun-release-", dir=output.parent) as temporary:
        stage = Path(temporary)
        for asset in manifest["assets"]:
            name = asset["url"].rsplit("/", 1)[1]
            destination = stage / name
            shutil.copyfile(sources[name], destination)
            with destination.open("rb") as stream:
                digest = hashlib.file_digest(stream, "sha256").hexdigest()
            if digest != asset["sha256"] or destination.stat().st_size != asset["bytes"]:
                raise ValueError("Artifact changed while staging the release")
        (stage / "SHA256SUMS").write_text(sums + f"{manifest_hash}  release-manifest.json\n", encoding="utf-8", newline="\n")
        (stage / "release-manifest.json").write_text(document, encoding="utf-8", newline="\n")
        # Reserve the immutable destination exclusively after all byte checks.
        # Install the manifest last; an interrupted export has no usable index
        # and is kept for inspection rather than silently overwritten.
        output.mkdir()
        for name in [*sorted(sources), "SHA256SUMS", "release-manifest.json"]:
            (stage / name).rename(output / name)
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("records", nargs="+", type=Path)
    parser.add_argument("--evidence", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--internal-test", action="store_true")
    parser.add_argument("--apk", type=Path, help="Native apk-tools for comparing independently signed noarch packages")
    parser.add_argument("--keys", type=Path, help="Trusted public keys for native APK comparison")
    args = parser.parse_args()
    evidence = json.loads(args.evidence.read_text()) if args.evidence else None
    manifest = export_release(args.records, args.output, evidence, args.internal_test, apk=args.apk, keys=args.keys)
    print(f"{manifest['release']}: {len(manifest['assets'])} measured assets -> {args.output}")


if __name__ == "__main__":
    main()
