import contextlib
import importlib
import io
import json
import sys
import tempfile
import unittest
import zipfile

from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]

if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))


def load_release_assets_module(test_case):
    try:
        return importlib.import_module("scripts.release_assets")
    except ImportError:
        test_case.fail("scripts.release_assets module missing")


class ReleaseAssetsTests(unittest.TestCase):
    def test_prepare_release_outputs_rejects_missing_bundle_package(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )

            with self.assertRaisesRegex(ValueError, "bundle"):
                release_assets.prepare_release_outputs(
                    artifacts_dir, release_dir, split_dir, "v1.2.3"
                )

    def test_prepare_release_outputs_rejects_missing_split_package(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )

            with self.assertRaisesRegex(ValueError, "luci-app-smart-srun"):
                release_assets.prepare_release_outputs(
                    artifacts_dir, release_dir, split_dir, "v1.2.3"
                )

    def test_prepare_release_outputs_keeps_bundle_separate_and_zips_split_packages(
        self,
    ):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            bundle_name = "luci-app-smart-srun-bundle_1.2.3_all.ipk"
            core_name = "smart-srun_1.2.3_all.ipk"
            luci_name = "luci-app-smart-srun_1.2.3_all.ipk"
            extra_name = "unrelated-package_1.2.3_all.ipk"

            for name in [bundle_name, core_name, luci_name, extra_name]:
                (artifacts_dir / name).write_text(name, encoding="utf-8")

            metadata = release_assets.prepare_release_outputs(
                artifacts_dir, release_dir, split_dir, "v1.2.3"
            )

            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()), [bundle_name]
            )
            self.assertEqual(
                metadata["split_zip_name"],
                "smart-srun-split-packages-v1.2.3.zip",
            )
            self.assertEqual(
                metadata["split_zip_path"],
                str(split_dir / "smart-srun-split-packages-v1.2.3.zip"),
            )

            with zipfile.ZipFile(metadata["split_zip_path"]) as archive:
                self.assertEqual(
                    sorted(archive.namelist()), sorted([core_name, luci_name])
                )

    def test_prepare_release_outputs_replaces_existing_release_bundles(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()
            release_dir.mkdir()

            stale_bundle = release_dir / "luci-app-smart-srun-bundle_0.9.0_all.ipk"
            stale_bundle.write_text("stale", encoding="utf-8")

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )

            release_assets.prepare_release_outputs(
                artifacts_dir, release_dir, split_dir, "v1.2.3"
            )

            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()),
                ["luci-app-smart-srun-bundle_1.2.3_all.ipk"],
            )

    def test_prepare_release_outputs_removes_stale_non_bundle_ipks(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()
            release_dir.mkdir()

            stale_split_ipk = release_dir / "smart-srun_0.9.0_all.ipk"
            stale_split_ipk.write_text("stale", encoding="utf-8")

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )

            release_assets.prepare_release_outputs(
                artifacts_dir, release_dir, split_dir, "v1.2.3"
            )

            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()),
                ["luci-app-smart-srun-bundle_1.2.3_all.ipk"],
            )

    def test_prepare_release_outputs_removes_stale_split_zip_files(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()
            split_dir.mkdir()

            stale_split_zip = split_dir / "smart-srun-split-packages-v0.9.0.zip"
            stale_split_zip.write_text("stale", encoding="utf-8")

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )

            release_assets.prepare_release_outputs(
                artifacts_dir, release_dir, split_dir, "v1.2.3"
            )

            self.assertEqual(
                sorted(path.name for path in split_dir.iterdir()),
                ["smart-srun-split-packages-v1.2.3.zip"],
            )

    def test_prepare_release_outputs_rejects_ambiguous_bundle_matches(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle1", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.4_all.ipk").write_text(
                "bundle2", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )

            with self.assertRaisesRegex(ValueError, "Expected exactly one"):
                release_assets.prepare_release_outputs(
                    artifacts_dir, release_dir, split_dir, "v1.2.3"
                )

    def test_build_split_packages_url_uses_downloads_branch_raw_url(self):
        release_assets = load_release_assets_module(self)

        self.assertEqual(
            release_assets.build_split_packages_url("example", "smart-srun", "v1.2.3"),
            "https://raw.githubusercontent.com/example/smart-srun/downloads/v1.2.3/smart-srun-split-packages-v1.2.3.zip",
        )

    def test_build_split_packages_url_rejects_unsafe_version(self):
        release_assets = load_release_assets_module(self)

        with self.assertRaisesRegex(ValueError, "unsafe"):
            release_assets.build_split_packages_url(
                "example", "smart-srun", "../v1.2.3"
            )

        with self.assertRaisesRegex(ValueError, "unsafe"):
            release_assets.build_split_packages_url("example", "smart-srun", "..")

        with self.assertRaisesRegex(ValueError, "unsafe"):
            release_assets.build_split_packages_url("example", "smart-srun", "-v1.2.3")

    def test_render_release_notes_template_replaces_placeholders(self):
        release_assets = load_release_assets_module(self)

        rendered = release_assets.render_release_notes_template(
            "Version ${VERSION} uses ${OPENWRT_VERSION} from ${COMPARE_REF}",
            {
                "VERSION": "v1.2.3",
                "OPENWRT_VERSION": "24.10.0",
                "COMPARE_REF": "v1.2.2...v1.2.3",
            },
        )

        self.assertEqual(
            rendered,
            "Version v1.2.3 uses 24.10.0 from v1.2.2...v1.2.3",
        )

    def test_write_release_notes_renders_template_file_to_output_file(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            template_path = temp_path / "release-template.md"
            output_path = temp_path / "release-notes.md"

            template_path.write_text(
                "Version ${VERSION} uses ${OPENWRT_VERSION}", encoding="utf-8"
            )

            release_assets.write_release_notes(
                template_path,
                output_path,
                {
                    "VERSION": "v1.2.3",
                    "OPENWRT_VERSION": "24.10.0",
                },
            )

            self.assertEqual(
                output_path.read_text(encoding="utf-8"),
                "Version v1.2.3 uses 24.10.0",
            )

    def test_main_renders_release_notes_from_template_files(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            template_path = temp_path / "release-template.md"
            output_path = temp_path / "release-notes.md"

            template_path.write_text(
                "Compare ${COMPARE_REF} via ${SPLIT_PACKAGES_URL}", encoding="utf-8"
            )

            exit_code = release_assets.main(
                [
                    "render-notes",
                    str(template_path),
                    str(output_path),
                    "COMPARE_REF=main...v1.2.3",
                    "SPLIT_PACKAGES_URL=https://example.invalid/download.zip",
                ]
            )

            self.assertEqual(exit_code, 0)
            self.assertEqual(
                output_path.read_text(encoding="utf-8"),
                "Compare main...v1.2.3 via https://example.invalid/download.zip",
            )

    def test_main_rejects_invalid_replacement_argument(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            template_path = temp_path / "release-template.md"
            output_path = temp_path / "release-notes.md"

            template_path.write_text("Version ${VERSION}", encoding="utf-8")

            stderr = io.StringIO()
            with (
                self.assertRaises(SystemExit) as ctx,
                contextlib.redirect_stderr(stderr),
            ):
                release_assets.main(
                    [
                        "render-notes",
                        str(template_path),
                        str(output_path),
                        "INVALID_REPLACEMENT",
                    ]
                )

            self.assertEqual(ctx.exception.code, 2)
            self.assertIn("expected KEY=VALUE format", stderr.getvalue())

    def test_main_prepares_release_outputs_and_prints_metadata(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release-assets"
            split_dir = temp_path / "split-downloads"
            artifacts_dir.mkdir()

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )

            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                exit_code = release_assets.main(
                    [
                        "prepare",
                        str(artifacts_dir),
                        str(release_dir),
                        str(split_dir),
                        "v1.2.3",
                    ]
                )
            output = stdout.getvalue()

            self.assertEqual(exit_code, 0)
            self.assertIn(
                '"split_zip_name": "smart-srun-split-packages-v1.2.3.zip"', output
            )
            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()),
                ["luci-app-smart-srun-bundle_1.2.3_all.ipk"],
            )

    def test_main_prepare_metadata_can_drive_followup_commands(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release-assets"
            split_dir = temp_path / "split-downloads"
            template_path = temp_path / "release-template.md"
            output_path = temp_path / "release-notes.md"
            artifacts_dir.mkdir()

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )
            template_path.write_text("Download ${SPLIT_PACKAGES_URL}", encoding="utf-8")

            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                exit_code = release_assets.main(
                    [
                        "prepare",
                        str(artifacts_dir),
                        str(release_dir),
                        str(split_dir),
                        "v1.2.3",
                    ]
                )

            metadata = json.loads(stdout.getvalue())

            self.assertEqual(exit_code, 0)
            self.assertEqual(
                metadata["split_zip_name"],
                "smart-srun-split-packages-v1.2.3.zip",
            )

            exit_code = release_assets.main(
                [
                    "render-notes",
                    str(template_path),
                    str(output_path),
                    "SPLIT_PACKAGES_URL="
                    + release_assets.build_split_packages_url(
                        "example", "smart-srun", "v1.2.3"
                    ),
                ]
            )

            self.assertEqual(exit_code, 0)
            self.assertIn(metadata["split_zip_name"], output_path.read_text("utf-8"))

    def test_main_prints_split_packages_url(self):
        release_assets = load_release_assets_module(self)

        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            exit_code = release_assets.main(
                ["build-split-url", "example", "smart-srun", "v1.2.3"]
            )
        output = stdout.getvalue()

        self.assertEqual(exit_code, 0)
        self.assertEqual(
            output.strip(),
            "https://raw.githubusercontent.com/example/smart-srun/downloads/v1.2.3/smart-srun-split-packages-v1.2.3.zip",
        )


class ReleaseAssetsApkTests(unittest.TestCase):
    """apk SDK produces files like smart-srun-1.2.3-r1.apk (no arch suffix)."""

    def test_prepare_release_outputs_keeps_bundle_separate_for_apk_format(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            bundle_name = "luci-app-smart-srun-bundle-1.2.3-r1.apk"
            core_name = "smart-srun-1.2.3-r1.apk"
            luci_name = "luci-app-smart-srun-1.2.3-r1.apk"
            extra_name = "unrelated-package-1.2.3-r1.apk"

            for name in [bundle_name, core_name, luci_name, extra_name]:
                (artifacts_dir / name).write_text(name, encoding="utf-8")

            metadata = release_assets.prepare_release_outputs(
                artifacts_dir,
                release_dir,
                split_dir,
                "v1.2.3",
                package_format="apk",
            )

            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()), [bundle_name]
            )
            self.assertEqual(
                metadata["split_zip_name"],
                "smart-srun-split-packages-v1.2.3-apk.zip",
            )
            with zipfile.ZipFile(metadata["split_zip_path"]) as archive:
                self.assertEqual(
                    sorted(archive.namelist()), sorted([core_name, luci_name])
                )

    def test_prepare_release_outputs_luci_apk_glob_excludes_bundle(self):
        """luci-app-smart-srun-*.apk would also match bundle-*.apk; must exclude it."""
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            (artifacts_dir / "luci-app-smart-srun-bundle-1.2.3-r1.apk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun-1.2.3-r1.apk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun-1.2.3-r1.apk").write_text(
                "luci", encoding="utf-8"
            )

            metadata = release_assets.prepare_release_outputs(
                artifacts_dir,
                release_dir,
                split_dir,
                "v1.2.3",
                package_format="apk",
            )

            with zipfile.ZipFile(metadata["split_zip_path"]) as archive:
                names = sorted(archive.namelist())
            self.assertIn("luci-app-smart-srun-1.2.3-r1.apk", names)
            self.assertNotIn("luci-app-smart-srun-bundle-1.2.3-r1.apk", names)

    def test_prepare_release_outputs_rejects_unknown_format(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            artifacts_dir.mkdir()

            with self.assertRaisesRegex(ValueError, "package_format"):
                release_assets.prepare_release_outputs(
                    artifacts_dir,
                    temp_path / "release",
                    temp_path / "split",
                    "v1.2.3",
                    package_format="deb",
                )

    def test_main_prepare_accepts_format_flag(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            artifacts_dir.mkdir()

            (artifacts_dir / "luci-app-smart-srun-bundle-1.2.3-r1.apk").write_text(
                "bundle", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun-1.2.3-r1.apk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun-1.2.3-r1.apk").write_text(
                "luci", encoding="utf-8"
            )

            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                exit_code = release_assets.main(
                    [
                        "prepare",
                        "--format",
                        "apk",
                        str(artifacts_dir),
                        str(temp_path / "release"),
                        str(temp_path / "split"),
                        "v1.2.3",
                    ]
                )
            metadata = json.loads(stdout.getvalue())

            self.assertEqual(exit_code, 0)
            self.assertEqual(
                metadata["split_zip_name"],
                "smart-srun-split-packages-v1.2.3-apk.zip",
            )

    def test_build_split_url_honors_apk_format(self):
        release_assets = load_release_assets_module(self)

        self.assertEqual(
            release_assets.build_split_packages_url(
                "example", "smart-srun", "v1.2.3", package_format="apk"
            ),
            "https://raw.githubusercontent.com/example/smart-srun/downloads/v1.2.3/smart-srun-split-packages-v1.2.3-apk.zip",
        )


class ReleaseAssetsUnifiedTests(unittest.TestCase):
    """Unified pre-release: both ipk + apk bundles as assets; one combined split zip."""

    def _populate_both_formats(self, artifacts_dir):
        ipk_names = [
            "luci-app-smart-srun-bundle_1.2.3_all.ipk",
            "smart-srun_1.2.3_all.ipk",
            "luci-app-smart-srun_1.2.3_all.ipk",
        ]
        apk_names = [
            "luci-app-smart-srun-bundle-1.2.3-r1.apk",
            "smart-srun-1.2.3-r1.apk",
            "luci-app-smart-srun-1.2.3-r1.apk",
        ]
        for name in ipk_names + apk_names:
            (artifacts_dir / name).write_text(name, encoding="utf-8")
        return ipk_names, apk_names

    def test_unified_prepare_puts_both_bundles_in_release_dir(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            self._populate_both_formats(artifacts_dir)

            metadata = release_assets.prepare_unified_release_outputs(
                artifacts_dir, release_dir, split_dir, "v1.2.3"
            )

            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()),
                sorted([
                    "luci-app-smart-srun-bundle_1.2.3_all.ipk",
                    "luci-app-smart-srun-bundle-1.2.3-r1.apk",
                ]),
            )
            self.assertEqual(
                metadata["split_zip_name"],
                "smart-srun-split-packages-v1.2.3.zip",
            )

    def test_unified_prepare_zip_contains_all_four_split_packages(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()

            self._populate_both_formats(artifacts_dir)

            metadata = release_assets.prepare_unified_release_outputs(
                artifacts_dir, release_dir, split_dir, "v1.2.3"
            )

            with zipfile.ZipFile(metadata["split_zip_path"]) as archive:
                names = sorted(archive.namelist())

            self.assertEqual(
                names,
                sorted([
                    "smart-srun_1.2.3_all.ipk",
                    "luci-app-smart-srun_1.2.3_all.ipk",
                    "smart-srun-1.2.3-r1.apk",
                    "luci-app-smart-srun-1.2.3-r1.apk",
                ]),
            )

    def test_unified_prepare_requires_both_formats(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            artifacts_dir.mkdir()

            (artifacts_dir / "luci-app-smart-srun-bundle_1.2.3_all.ipk").write_text(
                "bundle-ipk", encoding="utf-8"
            )
            (artifacts_dir / "smart-srun_1.2.3_all.ipk").write_text(
                "core", encoding="utf-8"
            )
            (artifacts_dir / "luci-app-smart-srun_1.2.3_all.ipk").write_text(
                "luci", encoding="utf-8"
            )

            with self.assertRaisesRegex(ValueError, "bundle"):
                release_assets.prepare_unified_release_outputs(
                    artifacts_dir,
                    temp_path / "release",
                    temp_path / "split",
                    "v1.2.3",
                )

    def test_unified_prepare_clears_stale_bundles_and_zips(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            release_dir = temp_path / "release"
            split_dir = temp_path / "split"
            artifacts_dir.mkdir()
            release_dir.mkdir()
            split_dir.mkdir()

            (release_dir / "luci-app-smart-srun-bundle_0.9.0_all.ipk").write_text(
                "stale-ipk", encoding="utf-8"
            )
            (release_dir / "luci-app-smart-srun-bundle-0.9.0-r1.apk").write_text(
                "stale-apk", encoding="utf-8"
            )
            (split_dir / "smart-srun-split-packages-v0.9.0.zip").write_text(
                "stale-zip", encoding="utf-8"
            )
            (split_dir / "smart-srun-split-packages-v0.9.0-apk.zip").write_text(
                "stale-apk-zip", encoding="utf-8"
            )

            self._populate_both_formats(artifacts_dir)

            release_assets.prepare_unified_release_outputs(
                artifacts_dir, release_dir, split_dir, "v1.2.3"
            )

            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()),
                sorted([
                    "luci-app-smart-srun-bundle_1.2.3_all.ipk",
                    "luci-app-smart-srun-bundle-1.2.3-r1.apk",
                ]),
            )
            self.assertEqual(
                [path.name for path in split_dir.iterdir()],
                ["smart-srun-split-packages-v1.2.3.zip"],
            )

    def test_main_prepare_unified_subcommand(self):
        release_assets = load_release_assets_module(self)

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            artifacts_dir = temp_path / "artifacts"
            artifacts_dir.mkdir()
            self._populate_both_formats(artifacts_dir)

            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                exit_code = release_assets.main(
                    [
                        "prepare-unified",
                        str(artifacts_dir),
                        str(temp_path / "release"),
                        str(temp_path / "split"),
                        "v1.2.3",
                    ]
                )
            metadata = json.loads(stdout.getvalue())

            self.assertEqual(exit_code, 0)
            self.assertEqual(
                metadata["split_zip_name"], "smart-srun-split-packages-v1.2.3.zip"
            )


if __name__ == "__main__":
    unittest.main()
