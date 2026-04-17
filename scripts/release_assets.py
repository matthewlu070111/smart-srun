"""Helpers for preparing release assets."""

import argparse
import json
from pathlib import Path
import re
import shutil
import sys
import zipfile


_SAFE_VERSION_RE = re.compile(r"^[A-Za-z0-9._-]+$")
_SUPPORTED_FORMATS = ("ipk", "apk")


def _validate_version(version):
    if (
        not version
        or version.startswith("-")
        or version in (".", "..")
        or not _SAFE_VERSION_RE.match(version)
    ):
        raise ValueError("unsafe version: %s" % version)
    return version


def _validate_format(package_format):
    if package_format not in _SUPPORTED_FORMATS:
        raise ValueError(
            "unsupported package_format %r (expected one of %s)"
            % (package_format, ", ".join(_SUPPORTED_FORMATS))
        )
    return package_format


def _split_zip_name(version, package_format="ipk"):
    _validate_version(version)
    _validate_format(package_format)
    if package_format == "apk":
        return "smart-srun-split-packages-%s-apk.zip" % version
    return "smart-srun-split-packages-%s.zip" % version


def _require_single_match(paths, label):
    if not paths:
        raise ValueError("Missing %s package" % label)
    if len(paths) != 1:
        raise ValueError("Expected exactly one %s package" % label)
    return paths[0]


def _collect_packages(artifacts_dir, package_format):
    """Return (bundle_path, [core_path, luci_split_path]) for the given format."""
    if package_format == "ipk":
        bundle_path = _require_single_match(
            sorted(artifacts_dir.glob("luci-app-smart-srun-bundle_*.ipk")),
            "luci-app-smart-srun bundle",
        )
        core_path = _require_single_match(
            sorted(artifacts_dir.glob("smart-srun_*.ipk")),
            "smart-srun split",
        )
        luci_path = _require_single_match(
            sorted(artifacts_dir.glob("luci-app-smart-srun_*.ipk")),
            "luci-app-smart-srun split",
        )
        return bundle_path, [core_path, luci_path]

    bundle_path = _require_single_match(
        sorted(artifacts_dir.glob("luci-app-smart-srun-bundle-*.apk")),
        "luci-app-smart-srun bundle",
    )
    core_path = _require_single_match(
        sorted(artifacts_dir.glob("smart-srun-*.apk")),
        "smart-srun split",
    )
    # `luci-app-smart-srun-*.apk` also matches the bundle — filter it out.
    luci_candidates = sorted(
        path
        for path in artifacts_dir.glob("luci-app-smart-srun-*.apk")
        if not path.name.startswith("luci-app-smart-srun-bundle-")
    )
    luci_path = _require_single_match(luci_candidates, "luci-app-smart-srun split")
    return bundle_path, [core_path, luci_path]


def prepare_release_outputs(
    artifacts_dir, release_dir, split_dir, version, package_format="ipk"
):
    artifacts_dir = Path(artifacts_dir)
    release_dir = Path(release_dir)
    split_dir = Path(split_dir)
    _validate_format(package_format)
    extension = package_format
    split_zip_name = _split_zip_name(version, package_format)
    split_zip_path = split_dir / split_zip_name

    release_dir.mkdir(parents=True, exist_ok=True)
    split_dir.mkdir(parents=True, exist_ok=True)

    for stale_release_path in release_dir.glob("*." + extension):
        stale_release_path.unlink()

    stale_zip_glob = (
        "smart-srun-split-packages-*-apk.zip"
        if package_format == "apk"
        else "smart-srun-split-packages-*.zip"
    )
    for stale_split_zip_path in split_dir.glob(stale_zip_glob):
        # For ipk we must avoid clobbering apk zips that happen to live alongside.
        if package_format == "ipk" and stale_split_zip_path.name.endswith("-apk.zip"):
            continue
        stale_split_zip_path.unlink()

    bundle_path, split_package_paths = _collect_packages(artifacts_dir, package_format)
    shutil.copy2(str(bundle_path), str(release_dir / bundle_path.name))

    with zipfile.ZipFile(str(split_zip_path), "w", zipfile.ZIP_DEFLATED) as archive:
        for package_path in split_package_paths:
            archive.write(str(package_path), package_path.name)

    return {
        "split_zip_name": split_zip_name,
        "split_zip_path": str(split_zip_path),
        "package_format": package_format,
    }


def prepare_unified_release_outputs(artifacts_dir, release_dir, split_dir, version):
    """Pack both ipk and apk outputs into a single pre-release.

    Release assets: two bundle files (one ipk, one apk).
    Split zip:       one `smart-srun-split-packages-<version>.zip` with all
                     four split packages (core+luci for each format).
    """
    artifacts_dir = Path(artifacts_dir)
    release_dir = Path(release_dir)
    split_dir = Path(split_dir)
    _validate_version(version)
    split_zip_name = _split_zip_name(version, "ipk")
    split_zip_path = split_dir / split_zip_name

    release_dir.mkdir(parents=True, exist_ok=True)
    split_dir.mkdir(parents=True, exist_ok=True)

    for stale_release_path in release_dir.glob("*.ipk"):
        stale_release_path.unlink()
    for stale_release_path in release_dir.glob("*.apk"):
        stale_release_path.unlink()
    for stale_split_zip_path in split_dir.glob("smart-srun-split-packages-*.zip"):
        stale_split_zip_path.unlink()

    ipk_bundle, ipk_splits = _collect_packages(artifacts_dir, "ipk")
    apk_bundle, apk_splits = _collect_packages(artifacts_dir, "apk")

    shutil.copy2(str(ipk_bundle), str(release_dir / ipk_bundle.name))
    shutil.copy2(str(apk_bundle), str(release_dir / apk_bundle.name))

    with zipfile.ZipFile(str(split_zip_path), "w", zipfile.ZIP_DEFLATED) as archive:
        for package_path in ipk_splits + apk_splits:
            archive.write(str(package_path), package_path.name)

    return {
        "split_zip_name": split_zip_name,
        "split_zip_path": str(split_zip_path),
        "package_format": "unified",
    }


def build_split_packages_url(owner, repo, version, package_format="ipk"):
    return "https://raw.githubusercontent.com/%s/%s/downloads/%s/%s" % (
        owner,
        repo,
        version,
        _split_zip_name(version, package_format),
    )


def render_release_notes_template(template_text, replacements):
    rendered = template_text
    for key, value in replacements.items():
        rendered = rendered.replace("${%s}" % key, value)
    return rendered


def write_release_notes(template_path, output_path, replacements):
    template_path = Path(template_path)
    output_path = Path(output_path)
    rendered = render_release_notes_template(
        template_path.read_text(encoding="utf-8"), replacements
    )
    output_path.write_text(rendered, encoding="utf-8")


def main(argv=None):
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    prepare_parser = subparsers.add_parser("prepare")
    prepare_parser.add_argument(
        "--format",
        dest="package_format",
        choices=_SUPPORTED_FORMATS,
        default="ipk",
    )
    prepare_parser.add_argument("artifacts_dir")
    prepare_parser.add_argument("release_dir")
    prepare_parser.add_argument("split_dir")
    prepare_parser.add_argument("version")

    unified_parser = subparsers.add_parser("prepare-unified")
    unified_parser.add_argument("artifacts_dir")
    unified_parser.add_argument("release_dir")
    unified_parser.add_argument("split_dir")
    unified_parser.add_argument("version")

    render_parser = subparsers.add_parser("render-notes")
    render_parser.add_argument("template_path")
    render_parser.add_argument("output_path")
    render_parser.add_argument("replacements", nargs="*")

    split_url_parser = subparsers.add_parser("build-split-url")
    split_url_parser.add_argument(
        "--format",
        dest="package_format",
        choices=_SUPPORTED_FORMATS,
        default="ipk",
    )
    split_url_parser.add_argument("owner")
    split_url_parser.add_argument("repo")
    split_url_parser.add_argument("version")

    args = parser.parse_args(argv)

    if args.command == "prepare":
        metadata = prepare_release_outputs(
            args.artifacts_dir,
            args.release_dir,
            args.split_dir,
            args.version,
            package_format=args.package_format,
        )
        print(json.dumps(metadata, sort_keys=True))
        return 0

    if args.command == "prepare-unified":
        metadata = prepare_unified_release_outputs(
            args.artifacts_dir,
            args.release_dir,
            args.split_dir,
            args.version,
        )
        print(json.dumps(metadata, sort_keys=True))
        return 0

    if args.command == "build-split-url":
        print(
            build_split_packages_url(
                args.owner,
                args.repo,
                args.version,
                package_format=args.package_format,
            )
        )
        return 0

    replacements = {}
    for item in args.replacements:
        if "=" not in item:
            parser.error("invalid replacement %r: expected KEY=VALUE format" % item)
        key, value = item.split("=", 1)
        replacements[key] = value

    write_release_notes(args.template_path, args.output_path, replacements)
    return 0


if __name__ == "__main__":
    sys.exit(main())
