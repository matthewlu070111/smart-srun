"""Exercise implicit HTTP dependencies absent from OpenWrt python3-light.

The official packages feed separates unicodedata into python3-codecs and _ssl
into python3-openssl on both release SDK branches (openwrt-23.05/25.12):
lang/python/python3/files/python3-package-{codecs,openssl}.mk.
Use a fresh interpreter so an earlier test cannot hide a missing module by
preloading IDNA or HTTPS support into sys.modules.
"""

import re
import subprocess
import sys
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
MODULE_ROOT = REPO_ROOT / "root" / "usr" / "lib" / "smart_srun"
OPTIONAL_MODULES = {
    "python3-codecs": "unicodedata",
    "python3-openssl": "_ssl",
    "python3-urllib": "urllib",
}
HTTP_SMOKE = r'''
import sys

blocked = set(sys.argv[2:])

class OpenWrtModules:
    def find_spec(self, fullname, path=None, target=None):
        if fullname.split(".")[0] in blocked:
            raise ModuleNotFoundError("OpenWrt package missing: " + fullname)

sys.meta_path.insert(0, OpenWrtModules())
sys.path.insert(0, sys.argv[1])

import cli
import network
import school_presets
import updater
import portal_detect
import wifi_setup

assert network.HAVE_URLLIB, "Python HTTP client unavailable"
assert "portal.example".encode("idna") == b"portal.example"
assert updater._stdlib_http_is_usable(), "Updater HTTP client unavailable"
assert school_presets.urlrequest is not None
assert hasattr(network.http_client, "HTTPSConnection"), "HTTPS unavailable"
import ssl
context = ssl.create_default_context()
assert context.check_hostname
assert context.verify_mode == ssl.CERT_REQUIRED
'''


def smoke_runtime(packages):
    blocked = [module for package, module in OPTIONAL_MODULES.items()
               if package not in packages]
    return subprocess.run(
        [sys.executable, "-S", "-c", HTTP_SMOKE, str(MODULE_ROOT)] + blocked,
        stdin=subprocess.DEVNULL,
        capture_output=True,
        text=True,
        timeout=20,
    )


class RuntimeDependencyTests(unittest.TestCase):
    def test_declared_dependencies_supply_idna_and_verified_https(self):
        makefile = (REPO_ROOT / "Makefile").read_text(encoding="utf-8")
        match = re.search(r"^RUNTIME_DEPENDS:=(.*)$", makefile, re.MULTILINE)
        self.assertIsNotNone(match)
        packages = {name.lstrip("+") for name in match.group(1).split()}
        result = smoke_runtime(packages)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_smoke_exposes_missing_implicit_http_dependencies(self):
        packages = set(OPTIONAL_MODULES)
        for missing in ("python3-codecs", "python3-openssl"):
            with self.subTest(package=missing):
                result = smoke_runtime(packages - {missing})
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(
                    "unknown encoding: idna" if missing == "python3-codecs" else "HTTPS unavailable",
                    result.stderr,
                )


if __name__ == "__main__":
    unittest.main()
