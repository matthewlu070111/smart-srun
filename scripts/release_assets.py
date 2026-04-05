"""Helpers for preparing release assets."""

import argparse
import json
from pathlib import Path
import shutil
import sys
import zipfile


def _split_zip_name(version):
    return "smart-srun-split-packages-%s.zip" % version


def prepare_release_outputs(artifacts_dir, release_dir, split_dir, version):
    artifacts_dir = Path(artifacts_dir)
    release_dir = Path(release_dir)
    split_dir = Path(split_dir)
    split_zip_name = _split_zip_name(version)
    split_zip_path = split_dir / split_zip_name

    release_dir.mkdir(parents=True, exist_ok=True)
    split_dir.mkdir(parents=True, exist_ok=True)

    for stale_bundle_path in release_dir.glob("luci-app-smart-srun-bundle_*.ipk"):
        stale_bundle_path.unlink()

    for stale_split_zip_path in split_dir.glob("smart-srun-split-packages-*.zip"):
        stale_split_zip_path.unlink()

    for bundle_path in sorted(artifacts_dir.glob("luci-app-smart-srun-bundle_*.ipk")):
        shutil.copy2(str(bundle_path), str(release_dir / bundle_path.name))

    core_package_paths = sorted(artifacts_dir.glob("smart-srun_*.ipk"))
    luci_package_paths = sorted(artifacts_dir.glob("luci-app-smart-srun_*.ipk"))

    if not core_package_paths:
        raise ValueError("Missing smart-srun split package")
    if not luci_package_paths:
        raise ValueError("Missing luci-app-smart-srun split package")

    split_package_paths = [core_package_paths[0], luci_package_paths[0]]

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

    replacements = dict(item.split("=", 1) for item in args.replacements)
    write_release_notes(args.template_path, args.output_path, replacements)
    return 0


if __name__ == "__main__":
    sys.exit(main())
