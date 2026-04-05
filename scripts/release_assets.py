"""Helpers for preparing release assets."""

import argparse
import json
from pathlib import Path
import re
import shutil
import sys
import zipfile


_SAFE_VERSION_RE = re.compile(r"^[A-Za-z0-9._-]+$")


def _validate_version(version):
    if (
        not version
        or version.startswith("-")
        or version in (".", "..")
        or not _SAFE_VERSION_RE.match(version)
    ):
        raise ValueError("unsafe version: %s" % version)
    return version


def _split_zip_name(version):
    version = _validate_version(version)
    return "smart-srun-split-packages-%s.zip" % version


def _require_single_match(paths, label):
    if not paths:
        raise ValueError("Missing %s package" % label)
    if len(paths) != 1:
        raise ValueError("Expected exactly one %s package" % label)
    return paths[0]


def prepare_release_outputs(artifacts_dir, release_dir, split_dir, version):
    artifacts_dir = Path(artifacts_dir)
    release_dir = Path(release_dir)
    split_dir = Path(split_dir)
    split_zip_name = _split_zip_name(version)
    split_zip_path = split_dir / split_zip_name

    release_dir.mkdir(parents=True, exist_ok=True)
    split_dir.mkdir(parents=True, exist_ok=True)

    for stale_release_ipk_path in release_dir.glob("*.ipk"):
        stale_release_ipk_path.unlink()

    for stale_split_zip_path in split_dir.glob("smart-srun-split-packages-*.zip"):
        stale_split_zip_path.unlink()

    bundle_path = _require_single_match(
        sorted(artifacts_dir.glob("luci-app-smart-srun-bundle_*.ipk")),
        "luci-app-smart-srun bundle",
    )
    shutil.copy2(str(bundle_path), str(release_dir / bundle_path.name))

    split_package_paths = [
        _require_single_match(
            sorted(artifacts_dir.glob("smart-srun_*.ipk")),
            "smart-srun split",
        ),
        _require_single_match(
            sorted(artifacts_dir.glob("luci-app-smart-srun_*.ipk")),
            "luci-app-smart-srun split",
        ),
    ]

    with zipfile.ZipFile(str(split_zip_path), "w", zipfile.ZIP_DEFLATED) as archive:
        for package_path in split_package_paths:
            archive.write(str(package_path), package_path.name)

    return {
        "split_zip_name": split_zip_name,
        "split_zip_path": str(split_zip_path),
    }


def build_split_packages_url(owner, repo, version):
    return "https://raw.githubusercontent.com/%s/%s/downloads/%s/%s" % (
        owner,
        repo,
        version,
        _split_zip_name(version),
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
    prepare_parser.add_argument("artifacts_dir")
    prepare_parser.add_argument("release_dir")
    prepare_parser.add_argument("split_dir")
    prepare_parser.add_argument("version")

    render_parser = subparsers.add_parser("render-notes")
    render_parser.add_argument("template_path")
    render_parser.add_argument("output_path")
    render_parser.add_argument("replacements", nargs="*")

    split_url_parser = subparsers.add_parser("build-split-url")
    split_url_parser.add_argument("owner")
    split_url_parser.add_argument("repo")
    split_url_parser.add_argument("version")

    args = parser.parse_args(argv)

    if args.command == "prepare":
        metadata = prepare_release_outputs(
            args.artifacts_dir, args.release_dir, args.split_dir, args.version
        )
        print(json.dumps(metadata, sort_keys=True))
        return 0

    if args.command == "build-split-url":
        print(build_split_packages_url(args.owner, args.repo, args.version))
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
