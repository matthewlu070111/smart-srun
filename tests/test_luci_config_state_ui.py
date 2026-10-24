from pathlib import Path
import shutil
import subprocess
import unittest


class LuciConfigStateTests(unittest.TestCase):
    def test_cbi_configuration_read_and_save_states(self):
        self.run_lua_probe("cbi_config_state.lua")

    def test_controller_preserves_browser_revisions(self):
        self.run_lua_probe("controller_config_revision.lua")

    def run_lua_probe(self, name):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        root = Path(__file__).resolve().parents[1]
        result = subprocess.run([lua, str(root / "tests/lua" / name), str(root)],
                                capture_output=True, text=True, encoding="utf-8", timeout=20)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_config_revisions_response_validation_and_status_polling(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        probe = Path(__file__).resolve().parent / "js/luci_config_state_test.js"
        result = subprocess.run([node, str(probe)], capture_output=True, text=True,
                                encoding="utf-8", timeout=20)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
