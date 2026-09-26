"""The LuCI bridge's mapping between the frozen form and config v2.

The interface keeps the baseline's flat field names while the daemon owns a
typed configuration, so a wrong destination here is a setting that silently
does not take effect -- or, in the password case, a credential a routine edit
would wipe. The assertions live in Lua next door and run under the same
interpreter version LuCI uses; this wrapper is what puts them in the suite.
"""

import shutil
import subprocess
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
LUA_ROOT = REPO_ROOT / "root" / "usr" / "lib" / "lua"
PROBE_DIR = Path(__file__).resolve().parent / "lua"


class LuciRpcBridgeTests(unittest.TestCase):
    def test_native_version_display(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        result = subprocess.run([lua, str(PROBE_DIR / "version_probe.lua"), str(REPO_ROOT)],
                                cwd=REPO_ROOT, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)

    def test_discovery_and_refresh_controller(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        result = subprocess.run(
            [lua, str(PROBE_DIR / "controller_discovery.lua"), str(REPO_ROOT)],
            cwd=str(REPO_ROOT), capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)

    def test_acid_job_client(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        result = subprocess.run(
            [node, str(PROBE_DIR.parent / "js" / "acid_job_test.js")],
            cwd=str(REPO_ROOT), capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)

    def test_bridge_mapping_probe_passes(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")

        probe = PROBE_DIR / "bridge_probe.lua"
        self.assertTrue(probe.is_file(), probe)
        result = subprocess.run(
            [lua, "-e", "require 'stub_env'; dofile([[%s]])" % probe.as_posix()],
            cwd=str(REPO_ROOT),
            env={
                "LUA_PATH": ";".join(
                    [
                        "%s/?.lua" % LUA_ROOT.as_posix(),
                        "%s/?.lua" % PROBE_DIR.as_posix(),
                        "",
                    ]
                ),
                "PATH": "/usr/bin:/bin",
            },
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)


if __name__ == "__main__":
    unittest.main()
