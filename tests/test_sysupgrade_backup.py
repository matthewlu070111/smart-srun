"""Keep runtime-created user settings across OpenWrt keep-config upgrades."""

import re
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
KEEP_PATH = "/lib/upgrade/keep.d/smart-srun"
CONFIG_PATH = "/etc/smart-srun/config.json"
USER_PRESETS_PATH = "/etc/smart-srun/user-presets.json"


def read_source(relative_path):
    return (REPO_ROOT / relative_path).read_text(encoding="utf-8")


class SysupgradeBackupTests(unittest.TestCase):
    def test_backup_covers_shared_user_config_without_programs_or_runtime_state(self):
        entries = [
            line.strip()
            for line in read_source("root" + KEEP_PATH).splitlines()
            if line.strip() and not line.lstrip().startswith("#")
        ]
        self.assertEqual(entries, [CONFIG_PATH, USER_PRESETS_PATH])

        for relative_path in (
            "root/usr/lib/lua/luci/controller/smart_srun.lua",
            "root/usr/lib/lua/luci/model/cbi/smart_srun.lua",
        ):
            self.assertNotIn(CONFIG_PATH, read_source(relative_path))
            self.assertNotIn(USER_PRESETS_PATH, read_source(relative_path))
        # The package must not ship defaults over restored user data.
        for path in (CONFIG_PATH, USER_PRESETS_PATH):
            self.assertFalse((REPO_ROOT / ("root" + path)).exists())

    def test_both_runtime_packages_install_the_backup_rule_as_data(self):
        makefile = read_source("Makefile")
        for package in ("smart-srun", "luci-app-smart-srun-bundle"):
            with self.subTest(package=package):
                install = re.search(
                    r"^define Package/%s/install\n(.*?)^endef$" % package,
                    makefile, re.MULTILINE | re.DOTALL,
                )
                self.assertIsNotNone(install)
                body = " ".join(install.group(1).replace("\\\n", "").split())
                self.assertIn("$(call SmartSrun/InstallCore,$(1))", body)
                shared = re.search(r"^define SmartSrun/InstallCore\n(.*?)^endef$", makefile, re.MULTILINE | re.DOTALL)
                self.assertIsNotNone(shared)
                body = " ".join(shared.group(1).replace("\\\n", "").split())
                self.assertIn("$(1)/lib/upgrade/keep.d", body)
                self.assertIn(
                    "$(INSTALL_DATA) $(CURDIR)/root%s $(1)%s"
                    % (KEEP_PATH, KEEP_PATH), body,
                )

    def test_keep_rule_has_unix_line_endings_in_package_checkouts(self):
        self.assertNotIn(b"\r", (REPO_ROOT / ("root" + KEEP_PATH)).read_bytes())
        self.assertIn("root%s text eol=lf" % KEEP_PATH, read_source(".gitattributes"))



if __name__ == "__main__":
    unittest.main()
