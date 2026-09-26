"""Installed Go runtime dependencies are independent of host Python tooling."""
import re
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]

class RuntimeDependencyTests(unittest.TestCase):
    def test_go_package_declares_device_tools_and_no_python(self):
        makefile = (REPO_ROOT / "Makefile").read_text(encoding="utf-8")
        match = re.search(r"^RUNTIME_DEPENDS:=(.*)$", makefile, re.MULTILINE)
        self.assertIsNotNone(match)
        packages = {name.lstrip("+") for name in match.group(1).split()}
        self.assertFalse(any(name.startswith("python") for name in packages))
        self.assertTrue({"ca-bundle", "uci", "ubus", "procd", "rpcd-mod-iwinfo"} <= packages)
