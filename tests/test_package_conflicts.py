"""Evaluate package metadata without downloading an OpenWrt SDK.

The SDK captures the build dependency graph inside BuildPackage, then expands
Package/<name>/DEPENDS when writing the package.  Keep that boundary in this
fixture so APK conflicts cannot accidentally become Kconfig dependencies.
Actual SDK builds additionally validate the generated APK/IPK metadata.
"""

import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
PACKAGES = ("smart-srun", "luci-app-smart-srun", "luci-app-smart-srun-bundle")
BUILD_DEPENDS = {
    "smart-srun": {"+python3-light", "+python3-urllib"},
    "luci-app-smart-srun": {"+smart-srun"},
    "luci-app-smart-srun-bundle": {"+python3-light", "+python3-urllib"},
}
CONFLICTS = {
    "smart-srun": set(),
    "luci-app-smart-srun": {"luci-app-smart-srun-bundle"},
    "luci-app-smart-srun-bundle": {"smart-srun", "luci-app-smart-srun"},
}


@unittest.skipUnless(shutil.which("make"), "GNU make is required")
class PackageConflictTests(unittest.TestCase):
    def evaluate_metadata(self, use_apk):
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir)
            (path / "rules.mk").write_text("INCLUDE_DIR := .\n", encoding="utf-8")
            (path / "package.mk").write_text(
                "define BuildPackage\n"
                "$(eval DEPENDS :=)\n"
                "$(eval CONFLICTS :=)\n"
                "$(eval $(call Package/$(1)))\n"
                "$(eval BuildDepends/$(1) := $(DEPENDS))\n"
                "$(eval Conflicts/$(1) := $(CONFLICTS))\n"
                "$(eval Package/$(1)/DEPENDS := $(subst +,,$(DEPENDS)))\n"
                "endef\n",
                encoding="utf-8",
            )
            makefile = (REPO_ROOT / "Makefile").read_text(encoding="utf-8")
            for package in PACKAGES:
                for field, variable in (
                    ("build", "BuildDepends/" + package),
                    ("conflicts", "Conflicts/" + package),
                    ("runtime", "Package/" + package + "/DEPENDS"),
                ):
                    makefile += "\n$(info contract:%s:%s:$(%s))" % (
                        package,
                        field,
                        variable,
                    )
            makefile += "\n.PHONY: contract\ncontract:\n"
            (path / "Makefile").write_text(makefile, encoding="utf-8")
            completed = subprocess.run(
                [
                    shutil.which("make"),
                    "--no-print-directory",
                    "-s",
                    "TOPDIR=.",
                    "CONFIG_USE_APK=" + ("y" if use_apk else ""),
                    "contract",
                ],
                cwd=path,
                stdin=subprocess.DEVNULL,
                text=True,
                capture_output=True,
                check=True,
            )
        metadata = {}
        for line in completed.stdout.splitlines():
            if line.startswith("contract:"):
                _, package, field, value = line.split(":", 3)
                metadata.setdefault(package, {})[field] = set(
                    filter(None, re.split(r"[,\s]+", value.strip()))
                )
        self.assertEqual(set(metadata), set(PACKAGES))
        return metadata

    def test_ipk_keeps_conflicts_separate_from_runtime_dependencies(self):
        metadata = self.evaluate_metadata(False)
        for package in PACKAGES:
            with self.subTest(package=package):
                self.assertEqual(metadata[package]["build"], BUILD_DEPENDS[package])
                self.assertEqual(metadata[package]["conflicts"], CONFLICTS[package])
                self.assertEqual(
                    metadata[package]["runtime"],
                    {dep.lstrip("+") for dep in BUILD_DEPENDS[package]},
                )

    def test_apk_excludes_bundle_without_changing_the_split_build_graph(self):
        metadata = self.evaluate_metadata(True)
        for package in PACKAGES:
            with self.subTest(package=package):
                self.assertEqual(metadata[package]["build"], BUILD_DEPENDS[package])
                self.assertEqual(metadata[package]["conflicts"], CONFLICTS[package])
                self.assertEqual(
                    metadata[package]["runtime"],
                    {dep.lstrip("+") for dep in BUILD_DEPENDS[package]}
                    | {"!" + conflict for conflict in CONFLICTS[package]},
                )


if __name__ == "__main__":
    unittest.main()
