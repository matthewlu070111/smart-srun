"""Focused checks for release version mapping and failed signing/config input."""

import importlib.util
import hashlib
import json
from pathlib import Path
import re
import tempfile
import tarfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("build_go_sdk", ROOT / "scripts/build_go_sdk.py")
build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)


class GoSDKBuildTests(unittest.TestCase):
    def test_sdk_filter_discards_only_builder_host_links(self):
        filter_member = build.sdk_archive_filter("sdk")
        with tempfile.TemporaryDirectory() as temporary:
            for tool in ("gcc", "python3", "git", "xxd"):
                member = tarfile.TarInfo("sdk/staging_dir/host/bin/" + tool)
                member.type = tarfile.SYMTYPE
                member.linkname = "/builder/original/tool"
                self.assertIsNone(filter_member(member, temporary))
            member = tarfile.TarInfo("sdk/toolchain/lib64")
            member.type = tarfile.SYMTYPE
            member.linkname = "../lib64"
            self.assertIsNotNone(filter_member(member, temporary))
            member.linkname = "/outside"
            with self.assertRaises(tarfile.FilterError):
                filter_member(member, temporary)

    def test_sdk_filter_preserves_path_and_link_escape_checks(self):
        filter_member = build.sdk_archive_filter("sdk")
        with tempfile.TemporaryDirectory() as temporary:
            for name in ("../outside", "/sdk/absolute", "other/file", "sdk/../outside"):
                with self.subTest(name=name), self.assertRaises(ValueError):
                    filter_member(tarfile.TarInfo(name), temporary)
            for kind, target in ((tarfile.SYMTYPE, "../../../outside"),
                                 (tarfile.LNKTYPE, "../outside"),
                                 (tarfile.LNKTYPE, "/outside")):
                member = tarfile.TarInfo("sdk/bin/link")
                member.type, member.linkname = kind, target
                with self.subTest(kind=kind, target=target), self.assertRaises(tarfile.FilterError):
                    filter_member(member, temporary)

    def test_native_rc_substitution_preserves_common_input_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo = root / "repo"
            (repo / "core").mkdir(parents=True)
            (repo / "Makefile").write_bytes(b"PKG_VERSION:=0.0.0\r\nOTHER:=unchanged\r\n")
            (repo / "core/go.mod").write_text("module test\n")
            with patch.object(build, "SOURCE_PATHS", ("Makefile", "core/go.mod")):
                ipk, first = build.copy_source(repo, root / "ipk", "2.0.0~rc9")
                apk, second = build.copy_source(repo, root / "apk", "2.0.0_rc9")
            self.assertEqual(first, second)
            self.assertNotEqual(ipk["Makefile"], apk["Makefile"])
            self.assertEqual(ipk["core/go.mod"], first["core/go.mod"])
            for name, digest in apk.items():
                self.assertEqual(hashlib.sha256((root / "apk" / name).read_bytes()).hexdigest(), digest)

    def test_payload_rejects_missing_binary_extra_config_and_legacy_runtime(self):
        files = dict.fromkeys(build.CORE_FILES | build.LUCI_FILES, 100)
        build.validate_payload("luci-app-smart-srun-bundle", files, 10 * 1024**2)
        for unwanted in ("etc/smart-srun/config.json", "usr/lib/smart_srun/client.py", "tests/secrets.json"):
            with self.subTest(unwanted=unwanted), self.assertRaises(ValueError):
                build.validate_payload("luci-app-smart-srun-bundle", {**files, unwanted: 1}, 10 * 1024**2)
        del files["usr/bin/srunnet"]
        with self.assertRaises(ValueError):
            build.validate_payload("luci-app-smart-srun-bundle", files, 10 * 1024**2)

    def test_payload_budget_has_one_source(self):
        """targets.json and the device constant must agree, and nothing may fork them.

        D80 raised the budget from 10 to 16 MiB and found the old number written
        out in five places. A build-side limit that drifts above the device's
        produces packages the router refuses to install; one that drifts below
        refuses a package the router would have accepted. Either way the failure
        names the build, not the stale literal, so this pins them together.
        """
        limit = json.loads((ROOT / "targets.json").read_text(encoding="utf-8"))["development_payload_limit_bytes"]
        self.assertEqual(limit, 16 * 1024**2)

        source = (ROOT / "core/internal/update/manifest.go").read_text(encoding="utf-8")
        shift = re.search(r"MaxPayloadBytes\s*=\s*(\d+)\s*<<\s*(\d+)", source)
        self.assertIsNotNone(shift, "MaxPayloadBytes is no longer a shift literal")
        self.assertEqual(int(shift.group(1)) << int(shift.group(2)), limit)

        # Every enforcement point must read the budget by name. Checking for the
        # key rather than banning its value keeps this honest about the
        # neighbouring constants: hot_update_go.MAX_PACKAGE is the compressed
        # asset cap and currently happens to be 16 MiB too, which a value-based
        # check would report as drift it is not.
        for name in ("scripts/verify_go_sdk.py", "scripts/hot_update_go.py",
                     "scripts/build_go_sdk.py", "scripts/build-go-dev.sh"):
            text = (ROOT / name).read_text(encoding="utf-8")
            self.assertIn("development_payload_limit_bytes", text,
                          f"{name} does not read the budget from targets.json")
            # The superseded value must survive nowhere: unlike 16 MiB it is not
            # shared with any other limit, so any occurrence is a stale copy.
            self.assertNotRegex(text, r"10485760|\b10 ?\* ?1024\*\*2",
                                f"{name} still carries the old 10 MiB budget")

    def test_rc_and_stable_native_versions(self):
        self.assertEqual(build.package_version("2.0.0rc10", "opkg"), "2.0.0~rc10")
        self.assertEqual(build.package_version("2.0.0rc10", "apk"), "2.0.0_rc10")
        self.assertEqual(build.package_version("2.0.0", "apk"), "2.0.0")
        for invalid in ("2.0.0rc0", "2.0.0rc01", "v2.0.0", "2.0.0\n", "2.0.0;touch x", "02.0.0"):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                build.package_version(invalid, "opkg")

    def test_catalog_has_no_ambiguous_target_or_unpinned_commit(self):
        catalog = json.loads((ROOT / "targets.json").read_text())
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "targets.json"
            target = catalog["targets"][0]
            catalog["targets"].append(target)
            path.write_text(json.dumps(catalog))
            with self.assertRaises(ValueError):
                build.load_target(path, target["id"])
            catalog["targets"].pop()
            target["feeds"]["packages"] = "master"
            path.write_text(json.dumps(catalog))
            with self.assertRaises(ValueError):
                build.load_target(path, target["id"])

    def test_cached_archive_is_verified_before_reuse(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "sdk.tar.zst"
            path.write_bytes(b"corrupted")
            with patch.object(build.urllib.request, "urlopen") as network:
                with self.assertRaises(ValueError):
                    build.download("https://downloads.openwrt.org/test", path, "0" * 64)
                network.assert_not_called()

    def test_signer_zero_exit_does_not_hide_reported_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            key = Path(temporary) / "public.pem"
            key.write_text("test public input")
            with patch.object(build, "command", return_value="UNTRUSTED signature\n"):
                with self.assertRaisesRegex(RuntimeError, "signer reported"):
                    build.sign_apk("apk", "test.apk", "private.pem", key)

    def test_signing_requires_separate_trusted_verification(self):
        calls = []

        def command(args):
            calls.append(args)
            if "verify" in args:
                raise RuntimeError("untrusted signature")
            return ""

        with tempfile.TemporaryDirectory() as temporary:
            key = Path(temporary) / "public.pem"
            key.write_text("test public input")
            with patch.object(build, "command", side_effect=command):
                with self.assertRaisesRegex(RuntimeError, "untrusted signature"):
                    build.sign_apk("apk", "test.apk", "private.pem", key)
        self.assertEqual(len(calls), 2)
        self.assertIn("--allow-untrusted", calls[0])
        self.assertNotIn("--allow-untrusted", calls[1])


if __name__ == "__main__":
    unittest.main()
