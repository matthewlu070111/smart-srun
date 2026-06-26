import hashlib
import os
import sys
import tempfile
import unittest
import zipfile
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import updater


class UpdaterTests(unittest.TestCase):
    def test_build_update_plan_selects_matching_bundle_asset(self):
        release = {
            "tag_name": "v1.3.4",
            "assets": [
                {
                    "name": "luci-app-smart-srun-bundle_1.3.4-r1_all.ipk",
                    "browser_download_url": "https://example.invalid/bundle.ipk",
                    "digest": "sha256:abc",
                }
            ],
        }
        with (
            mock.patch.object(
                updater.version_info,
                "detect_installed_package_name",
                return_value="luci-app-smart-srun-bundle",
            ),
            mock.patch.object(
                updater.version_info,
                "get_display_version",
                return_value="v1.3.3-r1",
            ),
            mock.patch.object(updater, "package_manager", return_value="opkg"),
        ):
            plan = updater.build_update_plan(release)

        self.assertEqual(plan["install_mode"], "bundle")
        self.assertEqual(plan["package_format"], "ipk")
        self.assertTrue(plan["update_available"])
        self.assertEqual(plan["asset_name"], "luci-app-smart-srun-bundle_1.3.4-r1_all.ipk")
        self.assertEqual(plan["download_url"], "https://example.invalid/bundle.ipk")

    def test_build_update_plan_uses_downloads_branch_for_split_packages(self):
        release = {"tag_name": "v1.3.4", "assets": []}
        with (
            mock.patch.object(
                updater.version_info,
                "detect_installed_package_name",
                return_value="luci-app-smart-srun",
            ),
            mock.patch.object(
                updater.version_info,
                "get_display_version",
                return_value="v1.3.3-r1",
            ),
            mock.patch.object(updater, "package_manager", return_value="opkg"),
        ):
            plan = updater.build_update_plan(release)

        self.assertEqual(plan["install_mode"], "split")
        self.assertEqual(plan["download_kind"], "split_zip")
        self.assertEqual(
            plan["download_urls"],
            [
                "https://raw.githubusercontent.com/matthewlu070111/"
                "smart-srun/downloads/1.3.4/smart-srun-split-packages-1.3.4.zip"
            ],
        )

    def test_split_zip_extraction_rejects_path_traversal(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = os.path.join(tmp, "bad.zip")
            with zipfile.ZipFile(zip_path, "w") as archive:
                archive.writestr("../bad.ipk", "bad")

            with self.assertRaisesRegex(RuntimeError, "unsafe split zip member"):
                updater._extract_split_zip(zip_path, os.path.join(tmp, "out"), "ipk", "split")

    def test_split_zip_extraction_selects_core_and_luci_packages(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = os.path.join(tmp, "split.zip")
            with zipfile.ZipFile(zip_path, "w") as archive:
                archive.writestr("smart-srun_1.3.4-r1_all.ipk", "core")
                archive.writestr("luci-app-smart-srun_1.3.4-r1_all.ipk", "luci")
                archive.writestr("luci-app-smart-srun-bundle_1.3.4-r1_all.ipk", "bundle")
            out_dir = os.path.join(tmp, "out")
            os.mkdir(out_dir)

            selected = updater._extract_split_zip(zip_path, out_dir, "ipk", "split")

        self.assertEqual(len(selected), 2)
        self.assertTrue(any(os.path.basename(path).startswith("smart-srun_") for path in selected))
        self.assertTrue(
            any(os.path.basename(path).startswith("luci-app-smart-srun_") for path in selected)
        )
        self.assertFalse(any("bundle" in os.path.basename(path) for path in selected))


class SplitZipDigestTests(unittest.TestCase):
    def _write_zip(self, tmp):
        zip_path = os.path.join(tmp, "split.zip")
        with zipfile.ZipFile(zip_path, "w") as archive:
            archive.writestr("smart-srun_1.3.4_all.ipk", "core")
        return zip_path

    def test_verify_split_zip_accepts_matching_sidecar(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = self._write_zip(tmp)
            digest = hashlib.sha256(open(zip_path, "rb").read()).hexdigest()
            sidecar = "%s  split.zip\n" % digest
            with mock.patch.object(updater, "_fetch_text", return_value=sidecar):
                # 不抛异常即视为通过校验。
                updater._verify_split_zip(zip_path, "https://example.invalid/split.zip")

    def test_verify_split_zip_rejects_mismatched_sidecar(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = self._write_zip(tmp)
            sidecar = "%s  split.zip\n" % ("0" * 64)
            with mock.patch.object(updater, "_fetch_text", return_value=sidecar):
                with self.assertRaisesRegex(RuntimeError, "sha256 digest mismatch"):
                    updater._verify_split_zip(
                        zip_path, "https://example.invalid/split.zip"
                    )

    def test_verify_split_zip_skips_when_sidecar_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = self._write_zip(tmp)

            def _raise(*_args, **_kwargs):
                raise RuntimeError("404")

            with (
                mock.patch.object(updater, "_fetch_text", side_effect=_raise),
                mock.patch.object(updater, "_append_log"),
            ):
                # 旧版本无旁注文件，应静默跳过而非报错。
                updater._verify_split_zip(zip_path, "https://example.invalid/split.zip")

    def test_parse_sha256_extracts_hex_digest(self):
        self.assertEqual(updater._parse_sha256("a" * 64 + "  file.zip"), "a" * 64)
        self.assertEqual(updater._parse_sha256("not-a-hash file"), "")


if __name__ == "__main__":
    unittest.main()
