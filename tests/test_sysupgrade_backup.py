"""Keep runtime-created user settings across OpenWrt keep-config upgrades."""

import ast
import importlib.util
import io
import re
import tempfile
import unittest
from pathlib import Path
from unittest import mock


REPO_ROOT = Path(__file__).resolve().parents[1]
KEEP_PATH = "/lib/upgrade/keep.d/smart-srun"
CONFIG_PATH = "/usr/lib/smart_srun/config.json"


def read_source(relative_path):
    return (REPO_ROOT / relative_path).read_text(encoding="utf-8")


class SysupgradeBackupTests(unittest.TestCase):
    def test_backup_covers_shared_user_config_without_programs_or_runtime_state(self):
        entries = [
            line.strip()
            for line in read_source("root" + KEEP_PATH).splitlines()
            if line.strip() and not line.lstrip().startswith("#")
        ]
        self.assertEqual(entries, [CONFIG_PATH])

        config_tree = ast.parse(read_source("root/usr/lib/smart_srun/config.py"))
        config_path = next(
            ast.literal_eval(node.value)
            for node in config_tree.body
            if isinstance(node, ast.Assign)
            and any(
                isinstance(target, ast.Name) and target.id == "JSON_CONFIG_FILE"
                for target in node.targets
            )
        )
        self.assertEqual(config_path, CONFIG_PATH)
        for relative_path in (
            "root/usr/lib/lua/luci/controller/smart_srun.lua",
            "root/usr/lib/lua/luci/model/cbi/smart_srun.lua",
        ):
            self.assertIn('local CONFIG_FILE = "%s"' % CONFIG_PATH,
                          read_source(relative_path))
        # The package must not ship a default config over restored credentials.
        self.assertFalse((REPO_ROOT / ("root" + CONFIG_PATH)).exists())

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
                self.assertIn("$(INSTALL_DIR) $(1)/lib/upgrade/keep.d", body)
                self.assertIn(
                    "$(INSTALL_DATA) $(CURDIR)/root%s $(1)%s"
                    % (KEEP_PATH, KEEP_PATH), body,
                )

    def test_keep_rule_has_unix_line_endings_in_package_checkouts(self):
        self.assertNotIn(b"\r", (REPO_ROOT / ("root" + KEEP_PATH)).read_bytes())
        self.assertIn("root%s text eol=lf" % KEEP_PATH, read_source(".gitattributes"))

    def test_hot_deploy_uploads_keep_rule_with_unix_paths_even_from_windows(self):
        spec = importlib.util.spec_from_file_location(
            "hot_update_backup_test", REPO_ROOT / "scripts/hot_update.py",
        )
        hot_update = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(hot_update)
        self.assertIn(
            {"local": "root" + KEEP_PATH, "remote": KEEP_PATH},
            hot_update.UPLOAD_TARGETS,
        )
        self.assertNotIn(KEEP_PATH, hot_update.EXECUTABLE_TARGETS)
        target = next(
            item for item in hot_update.remote_target_paths("/tmp/probe")
            if item["original_remote"] == KEEP_PATH
        )
        with tempfile.TemporaryDirectory() as temp_dir:
            source = Path(temp_dir) / target["local"]
            source.parent.mkdir(parents=True)
            source.write_bytes((CONFIG_PATH + "\r\n").encode("utf-8"))
            output = io.BytesIO()
            remote_file = mock.MagicMock()
            remote_file.__enter__.return_value = output
            sftp = mock.Mock()
            sftp.file.return_value = remote_file
            with mock.patch.object(hot_update, "REPO_ROOT", Path(temp_dir)):
                hot_update.upload_files(sftp, [target])
            sftp.file.assert_called_once_with("/tmp/probe" + KEEP_PATH, "wb")
            self.assertEqual(output.getvalue(), (CONFIG_PATH + "\n").encode("utf-8"))
            sftp.put.assert_not_called()


if __name__ == "__main__":
    unittest.main()
