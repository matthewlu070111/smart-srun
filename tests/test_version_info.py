import os
import sys
import unittest
from pathlib import Path


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
        self.assertEqual("v1.3.0", version_info.normalize_version_string("1.3.0-1"))
        self.assertEqual(
            "v1.3.0", version_info.normalize_version_string("v1.3.0-r2")
        )
        self.assertEqual(
            "v1.3.0-b1",
            version_info.normalize_version_string("1.3.0-beta.1-1"),
        )
        self.assertEqual(
            "v1.3.0-b1",
            version_info.normalize_version_string("1.3.0_beta.1-r1"),
        )
        self.assertEqual("v0.0.0", version_info.normalize_version_string(""))

    def test_luci_display_text_uses_cn_labels(self):
        bundle_status = "Package: luci-app-smart-srun-bundle\nVersion: 1.3.0-1\n\n"
        split_status = "Package: luci-app-smart-srun\nVersion: 1.3.0-1\n\n"

        self.assertEqual(
            "Bundle 版 v1.3.0",
            version_info.get_luci_display_text(status_text=bundle_status),
        )
        self.assertEqual(
            "标准版 v1.3.0",
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
            "Bundle 版 v1.3.0",
            version_info.get_luci_display_text(status_text=apk_status),
        )

    def test_parses_apk_version_with_r_release_suffix(self):
        self.assertEqual(
            "v1.3.0", version_info.normalize_version_string("1.3.0-r5")
        )

    def test_luci_displays_prereleases_and_uses_backend_update_decision(self):
        root = Path(WORKTREE_ROOT)
        schema = (root / "root/usr/lib/lua/luci/smart_srun/schema.lua").read_text(
            encoding="utf-8"
        )
        js = (root / "root/www/luci-static/resources/smart_srun.js").read_text(
            encoding="utf-8"
        )

        self.assertIn("^v?([0-9][%w%._%-]*)%-r?%d+$", schema)
        self.assertIn('version = version:gsub("_", "-")', schema)
        self.assertIn("fetchJson(UPDATE_CHECK_URL, function(err, data)", js)
        self.assertIn("if (err || !data || !data.ok || !data.update_available) return;", js)
        self.assertIn("data.latest_tag || data.latest_version", js)


if __name__ == "__main__":
    unittest.main()
