#!/usr/bin/env python3
"""Deploy measured Go SDK packages through the device's independent worker.

Python and OpenSSH run on the development host only. SSH configuration provides
authentication and host-key trust; this tool never starts network authentication.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
from urllib.parse import urlsplit

MAX_MANIFEST = 256 * 1024
MAX_PACKAGE = 16 * 1024**2
# The installed payload budget, read from the file that owns it so the uploader
# and the device agree. The device enforces update.MaxPayloadBytes; a stale copy
# here would refuse a package the router would have accepted.
MAX_PAYLOAD = json.loads(
    (Path(__file__).resolve().parents[1] / "targets.json").read_text(encoding="utf-8")
)["development_payload_limit_bytes"]
NAMES = {"core": "smart-srun", "luci": "luci-app-smart-srun", "bundle": "luci-app-smart-srun-bundle"}
STATUS_COMMAND = "if [ -x /var/run/smart-srun/update-worker ]; then /var/run/smart-srun/update-worker update status; else /usr/bin/srunnet update status; fi"


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Duplicate JSON field: " + key)
        result[key] = value
    return result


def load_manifest(path, directory):
    path = Path(path)
    if path.stat().st_size > MAX_MANIFEST:
        raise ValueError("Manifest exceeds size limit")
    data = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    if data.get("schema_version") != 1 or not isinstance(data.get("assets"), list) or not 0 < len(data["assets"]) <= 256:
        raise ValueError("Invalid release manifest")
    root = Path(directory).resolve()
    inputs, ids = [], set()
    for asset in data["assets"]:
        identifier, fmt = asset["id"], asset["format"]
        if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", identifier) or identifier in ids:
            raise ValueError("Invalid or duplicate asset ID")
        ids.add(identifier)
        if (asset["package_manager"], fmt) not in (("opkg", "ipk"), ("apk", "apk")) or asset["kind"] not in NAMES:
            raise ValueError("Invalid package kind or format")
        url = urlsplit(asset["url"])
        prefix = "/matthewlu070111/smart-srun/releases/download/" + data["release"] + "/"
        filename = url.path.removeprefix(prefix)
        if url.scheme != "https" or url.netloc != "github.com" or url.query or url.fragment or not url.path.startswith(prefix):
            raise ValueError("Package URL is not an official release asset")
        if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.~+-]{0,199}", filename) or not filename.endswith("." + fmt):
            raise ValueError("Unsafe package filename")
        # Manifests may cover several targets; verify the packages that are
        # actually present, then require every selected package before upload.
        file = root / filename
        if file.exists() or file.is_symlink():
            verify_local(file, asset)
        inputs.append((asset, file))
    return data, inputs


def verify_local(file, asset):
    if file.is_symlink() or not file.is_file() or file.stat().st_size != asset["bytes"] or not 0 < asset["bytes"] <= MAX_PACKAGE:
        raise ValueError("Missing, oversized or changed package: " + file.name)
    with file.open("rb") as stream:
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    if digest != asset["sha256"]:
        raise ValueError("Package SHA256 mismatch: " + file.name)


def select(inputs, inventory, recovery=False):
    packages = inventory["packages"]
    names = set(packages)
    modes = {frozenset([NAMES["core"]]): ["core"], frozenset([NAMES["core"], NAMES["luci"]]): ["core", "luci"],
             frozenset([NAMES["bundle"]]): ["bundle"]}
    if frozenset(names) not in modes or len(set(packages.values())) != 1:
        raise ValueError("Installed package set is incomplete or inconsistent")
    chosen = []
    for kind in modes[frozenset(names)]:
        arch = inventory["architecture"] if kind != "luci" else ("all" if inventory["package_manager"] == "opkg" else "noarch")
        matches = [(a, p) for a, p in inputs if a["kind"] == kind and a["package_manager"] == inventory["package_manager"]
                   and a["openwrt_arch"] == arch and inventory["firmware_family"] in a["firmware_compat"]
                   and a["validation"].get("build") is True and (kind == "luci" or a["validation"].get("elf") is True)]
        if len(matches) != 1:
            raise ValueError("No unique compatible validated package for " + kind)
        asset, path = matches[0]
        if recovery and asset["package_version"] != packages[NAMES[kind]]:
            raise ValueError("Recovery package is not the exact installed version")
        verify_local(path, asset)
        chosen.append((asset, path))
    if len({a["package_version"] for a, _ in chosen}) != 1 or sum(a["installed_bytes"] for a, _ in chosen) > MAX_PAYLOAD:
        raise ValueError(f"Split version mismatch or installed payload exceeds {MAX_PAYLOAD} bytes")
    return chosen


class SSH:
    def __init__(self, args):
        if not args.host or args.host.startswith("-") or re.search(r"[\s\x00-\x1f]", args.host):
            raise ValueError("Provide a valid SSH host or configured alias with --host")
        self.argv = ["ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15"]
        if args.ssh_config:
            self.argv += ["-F", str(Path(args.ssh_config).resolve())]
        if args.port:
            self.argv += ["-p", str(args.port)]
        self.argv.append(args.host)

    def run(self, command, data=None):
        result = subprocess.run(self.argv + [command], input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)
        if result.returncode:
            # Fixed commands never read configuration or account credentials.
            raise RuntimeError("Remote operation failed: " + result.stderr.decode("utf-8", "replace")[-2000:])
        if len(result.stdout) > 256 * 1024:
            raise RuntimeError("Remote response exceeds size limit")
        return result.stdout

    def json(self, command, data=None):
        return json.loads(self.run(command, data), object_pairs_hook=unique_object)

    def upload(self, folder, asset, path):
        verify_local(path, asset)
        directory = "/var/run/smart-srun/update-inbox/" + folder
        destination = directory + "/" + asset["id"] + "." + asset["format"]
        # Private, exclusive staging prevents a stale symlink being followed.
        # Only this exact staging file is cleaned up on exit, including errors.
        command = "umask 077; mkdir -p " + directory + "; chmod 0700 /var/run/smart-srun/update-inbox " + directory + "; "
        command += "stage=$(mktemp " + directory + "/.upload-XXXXXX) || exit 1; trap 'rm -f -- \"$stage\"' EXIT; "
        command += 'cat > "$stage" && chmod 0600 "$stage" && mv -f -- "$stage" ' + shlex.quote(destination)
        self.run(command, path.read_bytes())
        digest = self.run("sha256sum " + shlex.quote(destination)).split()[0].decode("ascii")
        if digest != asset["sha256"]:
            raise ValueError("Uploaded package checksum mismatch")


def deploy(args, ssh, manifests):
    new, old = manifests
    inventory = ssh.json("/usr/bin/srunnet update inventory")
    status = ssh.json(STATUS_COMMAND)
    if status.get("running") or status.get("phase") == "recovery_required":
        raise RuntimeError("Resolve the existing update task before deploying")
    if old[0]["release"] != inventory["display_version"]:
        raise ValueError("Recovery manifest does not match installed version")
    chosen = [("recovery", select(old[1], inventory, True)), ("new", select(new[1], inventory))]
    print(json.dumps({"inventory": inventory, "target": new[0]["release"], "packages": [p.name for _, files in chosen for _, p in files]}, ensure_ascii=False))
    if args.probe:
        return 0
    for folder, files in chosen:
        for asset, path in files:
            ssh.upload(folder, asset, path)
    payload = json.dumps({"manifest": new[0], "recovery": old[0]}, separators=(",", ":")).encode()
    # The device independently selects, validates metadata/signatures and hashes.
    plan = ssh.json("/usr/bin/srunnet update prepare-local", payload)
    if plan.get("ok") is not True or not re.fullmatch("[0-9a-f]{64}", plan.get("plan_id", "")):
        raise RuntimeError("Device did not return a verified update plan")
    if args.prepare:
        print(json.dumps(plan, ensure_ascii=False))
        return 0
    task = ssh.json("/usr/bin/srunnet update run " + plan["plan_id"] + " --background")
    if not task.get("running") or not re.fullmatch("[0-9a-f]{32}", task.get("job_id", "")):
        raise RuntimeError("Worker did not acknowledge the installation")
    print(json.dumps(task, ensure_ascii=False), flush=True)
    if args.background:
        return 0
    deadline, previous = time.monotonic() + args.wait, None
    while time.monotonic() < deadline:
        status = ssh.json(STATUS_COMMAND)
        if status.get("job_id") != task["job_id"]:
            raise RuntimeError("Update task identity changed")
        if status.get("phase") != previous or not status.get("running"):
            print(json.dumps(status, ensure_ascii=False), flush=True)
            previous = status.get("phase")
        if not status.get("running"):
            return 0 if status.get("ok") is True and status.get("phase") == "completed" else 1
        time.sleep(1)
    raise TimeoutError("Stopped waiting; the independent worker continues. Read srunnet update status before taking further action.")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default=os.environ.get("SMARTSRUN_ROUTER_HOST", ""), help="OpenSSH alias or user@host")
    parser.add_argument("--port", type=int)
    parser.add_argument("--ssh-config", type=Path)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--assets", type=Path, required=True)
    parser.add_argument("--recovery-manifest", type=Path, required=True)
    parser.add_argument("--recovery-assets", type=Path, required=True)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true", help="Verify local bytes without SSH or device changes")
    mode.add_argument("--probe", action="store_true", help="Read inventory and select packages without device changes")
    mode.add_argument("--prepare", action="store_true", help="Upload and verify packages without installing")
    mode.add_argument("--background", action="store_true")
    parser.add_argument("--wait", type=int, default=900, help="Wait budget; timeout never kills the installer")
    args = parser.parse_args(argv)
    try:
        if not 1 <= args.wait <= 86400 or (args.port is not None and not 1 <= args.port <= 65535):
            raise ValueError("Invalid wait budget or SSH port")
        manifests = [load_manifest(args.manifest, args.assets), load_manifest(args.recovery_manifest, args.recovery_assets)]
        if args.dry_run:
            present = [p for _, files in manifests for _, p in files if p.is_file()]
            if not present:
                raise ValueError("No local packages to verify")
            print(json.dumps({"local_files_verified": [str(p) for p in present], "device_checked": False}, ensure_ascii=False))
            return 0
        return deploy(args, SSH(args), manifests)
    except (OSError, ValueError, KeyError, TypeError, RuntimeError, subprocess.SubprocessError) as exc:
        print(str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
