import importlib
import io
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

    def test_build_split_packages_url_uses_downloads_branch_raw_url(self):
        release_assets = load_release_assets_module(self)

        self.assertEqual(
            release_assets.build_split_packages_url("example", "smart-srun", "v1.2.3"),
            "https://raw.githubusercontent.com/example/smart-srun/downloads/v1.2.3/smart-srun-split-packages-v1.2.3.zip",
        )

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

            stdout = sys.stdout
            sys.stdout = io.StringIO()
            try:
                exit_code = release_assets.main(
                    [
                        "prepare",
                        str(artifacts_dir),
                        str(release_dir),
                        str(split_dir),
                        "v1.2.3",
                    ]
                )
                output = sys.stdout.getvalue()
            finally:
                sys.stdout = stdout

            self.assertEqual(exit_code, 0)
            self.assertIn(
                '"split_zip_name": "smart-srun-split-packages-v1.2.3.zip"', output
            )
            self.assertEqual(
                sorted(path.name for path in release_dir.iterdir()),
                ["luci-app-smart-srun-bundle_1.2.3_all.ipk"],
            )

    def test_main_prints_split_packages_url(self):
        release_assets = load_release_assets_module(self)

        stdout = sys.stdout
        sys.stdout = io.StringIO()
        try:
            exit_code = release_assets.main(
                ["build-split-url", "example", "smart-srun", "v1.2.3"]
            )
            output = sys.stdout.getvalue()
        finally:
            sys.stdout = stdout

        self.assertEqual(exit_code, 0)
        self.assertEqual(
            output.strip(),
            "https://raw.githubusercontent.com/example/smart-srun/downloads/v1.2.3/smart-srun-split-packages-v1.2.3.zip",
        )


if __name__ == "__main__":
    unittest.main()
