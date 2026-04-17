import os
import sys
import unittest


THIS_DIR = os.path.dirname(os.path.abspath(__file__))
WORKTREE_ROOT = os.path.dirname(THIS_DIR)
MODULE_DIR = os.path.join(WORKTREE_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_DIR not in sys.path:
    sys.path.insert(0, MODULE_DIR)


import version_info


class VersionInfoTests(unittest.TestCase):
    def test_detect_installed_package_prefers_bundle_then_luci_then_cli(self):
        status_text = (
            "Package: smart-srun\nVersion: 1.3.0-1\n\n"
            "Package: luci-app-smart-srun\nVersion: 1.3.0-1\n\n"
        )
        self.assertEqual(
            "luci-app-smart-srun",
            version_info.detect_installed_package_name(status_text),
        )

        bundle_status = (
            status_text + "Package: luci-app-smart-srun-bundle\nVersion: 1.3.0-1\n\n"
        )
        self.assertEqual(
            "luci-app-smart-srun-bundle",
            version_info.detect_installed_package_name(bundle_status),
        )

    def test_normalize_version_formats_makefile_and_opkg_versions(self):
        self.assertEqual("v1.3.0-r1", version_info.normalize_version_string("1.3.0-1"))
        self.assertEqual(
            "v1.3.0-r2", version_info.normalize_version_string("v1.3.0-r2")
        )
        self.assertEqual("v0.0.0-r1", version_info.normalize_version_string(""))

    def test_luci_display_text_uses_cn_labels(self):
        bundle_status = "Package: luci-app-smart-srun-bundle\nVersion: 1.3.0-1\n\n"
        split_status = "Package: luci-app-smart-srun\nVersion: 1.3.0-1\n\n"

        self.assertEqual(
            "Bundle 版 v1.3.0-r1",
            version_info.get_luci_display_text(status_text=bundle_status),
        )
        self.assertEqual(
            "标准版 v1.3.0-r1",
            version_info.get_luci_display_text(status_text=split_status),
        )

    def test_parses_apk_db_format_single_letter_keys(self):
        """OpenWrt 24.10+ uses /lib/apk/db/installed with P:/V: keys."""
        apk_status = (
            "C:Q1xxxxxxxx=\n"
            "P:luci-app-smart-srun-bundle\n"
            "V:1.3.0-r1\n"
            "A:all\n"
            "\n"
            "P:some-other-pkg\n"
            "V:2.0-r0\n"
            "\n"
        )
        self.assertEqual(
            "luci-app-smart-srun-bundle",
            version_info.detect_installed_package_name(apk_status),
        )
        self.assertEqual(
            "Bundle 版 v1.3.0-r1",
            version_info.get_luci_display_text(status_text=apk_status),
        )

    def test_parses_apk_version_with_r_release_suffix(self):
        self.assertEqual(
            "v1.3.0-r5", version_info.normalize_version_string("1.3.0-r5")
        )


if __name__ == "__main__":
    unittest.main()
