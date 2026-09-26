#!/usr/bin/env python3
"""Inspect measured SDK packages and optionally execute their version command.

Produces SHA256-keyed ELF evidence, not installation, core or campus acceptance.
Requires Linux binutils and Go; APK inputs additionally require apk and trusted
public keys. Explicit --qemu/--execute runs package code only when requested.
"""
import argparse
import io
import json
from pathlib import Path
import re
import struct
import subprocess
import tarfile
import tempfile

import build_go_sdk as build


ELF_TARGETS = {
    "amd64": (2, 1, 62), "arm64": (2, 1, 183), "arm": (1, 1, 40),
    "mips": (1, 2, 8), "mipsle": (1, 1, 8),
}

ROOT = Path(__file__).resolve().parents[1]


def payload_limit():
    """The installed payload budget, from the one file that owns it.

    This verifier previously carried its own copy of the number. Two copies of a
    limit is one copy too many: raising it in targets.json while this file kept
    the old value would fail the release with a message pointing at the build,
    not at the stale literal here.
    """
    limit = json.loads((ROOT / "targets.json").read_text(encoding="utf-8"))["development_payload_limit_bytes"]
    if not isinstance(limit, int) or limit <= 0:
        raise ValueError("Invalid development payload limit")
    return limit


def inspect_header(data, goarch):
    if len(data) < 64 or data[:4] != b"\x7fELF" or data[5] not in (1, 2):
        raise ValueError("Invalid ELF header")
    kind, machine = struct.unpack(("<" if data[5] == 1 else ">") + "HH", data[16:20])
    if kind != 2 or (data[4], data[5], machine) != ELF_TARGETS.get(goarch):
        raise ValueError("ELF class, endian, machine or executable type differs from target")


def run(args):
    return subprocess.check_output([str(arg) for arg in args], text=True, stderr=subprocess.STDOUT, timeout=60)


def inspect_binary(binary, target, go, execute, qemu, version):
    with binary.open("rb") as stream:
        inspect_header(stream.read(64), target["goarch"])
    program_headers = run(["readelf", "--program-headers", "--wide", binary])
    if re.search(r"^\s*(INTERP|DYNAMIC)\s", program_headers, re.MULTILINE):
        raise ValueError("ELF requires a dynamic loader or dynamic section")
    info = run([go, "version", "-m", binary])
    settings = {}
    for line in info.splitlines():
        fields = line.strip().split("\t", 1)
        if len(fields) == 2 and fields[0] == "build" and "=" in fields[1]:
            key, value = fields[1].split("=", 1)
            # go version quotes values containing whitespace; OpenWrt appends
            # a trailing space when combining its package/compiler flags.
            settings[key] = json.loads(value) if value.startswith('"') else value
    expected = {"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": target["goarch"]}
    if target["goarch"] == "amd64":
        expected["GOAMD64"] = "v1"
        expected["-gcflags"] = "github.com/matthewlu070111/smart-srun/core/...=-l"
    if target["goarch"] == "arm64":
        expected["GOARM64"] = "v8.0"
    if target["goarch"] == "arm" and settings.get("GOARM") not in ("7", "7,hardfloat"):
        raise ValueError("ARMv7 package requires the SDK's ARMv7 hardware-float baseline")
    if target["goarch"] in ("mips", "mipsle"):
        expected.update(GOMIPS="softfloat", **{"-gcflags": "all=-l"})
    for key, value in expected.items():
        actual = settings.get(key)
        if key == "-gcflags" and isinstance(actual, str):
            actual = actual.strip()
        if actual != value:
            raise ValueError("Unexpected Go build setting: " + key)
    if target["goarch"] not in ("mips", "mipsle", "amd64") and settings.get("-gcflags"):
        raise ValueError("Build unexpectedly changes inlining")
    execution = None
    if execute or qemu:
        execution = run(([qemu] if qemu else []) + [binary, "version"]).strip()
        if execution != f"srunnet {version} (config schema v2)":
            raise ValueError("Executed version differs from the build record")
    return {"go_build_info": info, "program_headers": program_headers,
            "version_output": execution, "emulator": str(qemu) if qemu else None}


def verify(record_path, go, apk=None, keys=None, execute=False, qemu=None):
    record_path = Path(record_path)
    record = json.loads(record_path.read_text(encoding="utf-8"))
    target = record["target"]
    if target["goarch"] not in ELF_TARGETS:
        raise ValueError("Unsupported ELF inspection target")
    if target["format"] == "apk" and (not apk or not keys):
        raise ValueError("APK verification requires apk and trusted public keys")
    limit = payload_limit()
    evidence, inspections = {"schema_version": 1, "assets": {}}, []
    for asset in record["artifacts"]:
        name = asset["file"]
        if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.~+-]{0,199}", name) or not name.endswith("." + target["format"]):
            raise ValueError("Unsafe package filename")
        package = record_path.parent / name
        if package.is_symlink() or not package.is_file() or package.stat().st_size != asset["bytes"] or build.digest(package) != asset["sha256"]:
            raise ValueError("Package bytes differ from the build record")
        if target["format"] == "apk":
            run([apk, "--keys-dir", keys, "verify", package])
        actual_name, actual_version, arch, files = build.package_info(package, apk)
        neutral = "all" if target["format"] == "ipk" else "noarch"
        expected_arch = neutral if actual_name == "luci-app-smart-srun" else target["openwrt_arch"]
        if (actual_name != asset["name"] or actual_version != asset["package_version"]
                or arch != expected_arch or arch != asset["architecture"] or files != asset["files"]
                or sum(files.values()) != asset["installed_payload_bytes"]):
            raise ValueError("Native package identity or measured payload changed")
        build.validate_payload(actual_name, files, limit)
        detail = {"file": name, "sha256": asset["sha256"], "architecture": arch,
                  "installed_payload_bytes": sum(files.values())}
        evidence["assets"][asset["sha256"]] = {}
        if actual_name != "luci-app-smart-srun":
            with tempfile.TemporaryDirectory(prefix="smart-srun-elf-") as temporary:
                temp = Path(temporary)
                if target["format"] == "apk":
                    run([apk, "--keys-dir", keys, "extract", "--no-chown", "--destination", temp, package])
                    binary = temp / "usr/bin/srunnet"
                else:
                    binary = temp / "srunnet"
                    with tarfile.open(package) as outer:
                        with tarfile.open(fileobj=io.BytesIO(outer.extractfile("./data.tar.gz").read())) as data:
                            candidates = [entry for entry in data if entry.name.removeprefix("./") == "usr/bin/srunnet"]
                            if len(candidates) != 1 or not candidates[0].isfile() or candidates[0].size > limit:
                                raise ValueError("Missing or unsafe packaged ELF")
                            binary.write_bytes(data.extractfile(candidates[0]).read())
                    binary.chmod(0o755)
                if binary.is_symlink() or not binary.resolve().is_relative_to(temp):
                    raise ValueError("Packaged ELF escapes its temporary directory")
                detail.update(inspect_binary(binary, target, go, execute, qemu, record["display_version"]))
                evidence["assets"][asset["sha256"]] = {"elf": True}
        inspections.append(detail)
    return evidence, {"display_version": record["display_version"], "target": target,
                      "scope": "ELF/payload and optional version command only; no core/install/hardware/campus acceptance",
                      "artifacts": inspections}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("record", type=Path)
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--apk", type=Path)
    parser.add_argument("--keys", type=Path)
    execution = parser.add_mutually_exclusive_group()
    execution.add_argument("--execute", action="store_true")
    execution.add_argument("--qemu", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    evidence, details = verify(args.record, args.go, args.apk, args.keys, args.execute, args.qemu)
    args.output.mkdir(parents=True, exist_ok=False)
    for name, value in (("validation.json", evidence), ("inspection.json", details)):
        (args.output / name).write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    print(f"Verified {len(details['artifacts'])} packages -> {args.output}")


if __name__ == "__main__":
    main()
