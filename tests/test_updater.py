import hashlib
import os
import sys
import tempfile
import unittest
import zipfile
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import updater


class UpdaterTests(unittest.TestCase):
    def test_build_update_plan_selects_matching_bundle_asset(self):
        release = {
            "tag_name": "v1.3.4",
            "assets": [
                {
                    "name": "luci-app-smart-srun-bundle_1.3.4-r1_all.ipk",
                    "browser_download_url": "https://example.invalid/bundle.ipk",
                    "digest": "sha256:abc",
                }
            ],
        }
        with (
            mock.patch.object(
                updater.version_info,
                "detect_installed_package_name",
                return_value="luci-app-smart-srun-bundle",
            ),
            mock.patch.object(
                updater.version_info,
                "get_display_version",
                return_value="v1.3.3-r1",
            ),
            mock.patch.object(updater, "package_manager", return_value="opkg"),
        ):
            plan = updater.build_update_plan(release)

        self.assertEqual(plan["install_mode"], "bundle")
        self.assertEqual(plan["package_format"], "ipk")
        self.assertTrue(plan["update_available"])
        self.assertEqual(plan["asset_name"], "luci-app-smart-srun-bundle_1.3.4-r1_all.ipk")
        self.assertEqual(plan["download_url"], "https://example.invalid/bundle.ipk")

    def test_build_update_plan_uses_downloads_branch_for_split_packages(self):
        release = {"tag_name": "v1.3.4", "assets": []}
        with (
            mock.patch.object(
                updater.version_info,
                "detect_installed_package_name",
                return_value="luci-app-smart-srun",
            ),
            mock.patch.object(
                updater.version_info,
                "get_display_version",
                return_value="v1.3.3-r1",
            ),
            mock.patch.object(updater, "package_manager", return_value="opkg"),
        ):
            plan = updater.build_update_plan(release)

        self.assertEqual(plan["install_mode"], "split")
        self.assertEqual(plan["download_kind"], "split_zip")
        self.assertEqual(
            plan["download_urls"],
            [
                "https://raw.githubusercontent.com/matthewlu070111/"
                "smart-srun/downloads/1.3.4/smart-srun-split-packages-1.3.4.zip"
            ],
        )

    def test_split_zip_extraction_rejects_path_traversal(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = os.path.join(tmp, "bad.zip")
            with zipfile.ZipFile(zip_path, "w") as archive:
                archive.writestr("../bad.ipk", "bad")

            with self.assertRaisesRegex(RuntimeError, "unsafe split zip member"):
                updater._extract_split_zip(zip_path, os.path.join(tmp, "out"), "ipk", "split")

    def test_split_zip_extraction_selects_core_and_luci_packages(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = os.path.join(tmp, "split.zip")
            with zipfile.ZipFile(zip_path, "w") as archive:
                archive.writestr("smart-srun_1.3.4-r1_all.ipk", "core")
                archive.writestr("luci-app-smart-srun_1.3.4-r1_all.ipk", "luci")
                archive.writestr("luci-app-smart-srun-bundle_1.3.4-r1_all.ipk", "bundle")
            out_dir = os.path.join(tmp, "out")
            os.mkdir(out_dir)

            selected = updater._extract_split_zip(zip_path, out_dir, "ipk", "split")

        self.assertEqual(len(selected), 2)
        self.assertTrue(any(os.path.basename(path).startswith("smart-srun_") for path in selected))
        self.assertTrue(
            any(os.path.basename(path).startswith("luci-app-smart-srun_") for path in selected)
        )
        self.assertFalse(any("bundle" in os.path.basename(path) for path in selected))


class SplitZipDigestTests(unittest.TestCase):
    def _write_zip(self, tmp):
        zip_path = os.path.join(tmp, "split.zip")
        with zipfile.ZipFile(zip_path, "w") as archive:
            archive.writestr("smart-srun_1.3.4_all.ipk", "core")
        return zip_path

    def test_verify_split_zip_accepts_matching_sidecar(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = self._write_zip(tmp)
            with open(zip_path, "rb") as handle:
                digest = hashlib.sha256(handle.read()).hexdigest()
            sidecar = "%s  split.zip\n" % digest
            with mock.patch.object(updater, "_fetch_text", return_value=sidecar):
                # 不抛异常即视为通过校验。
                updater._verify_split_zip(zip_path, "https://example.invalid/split.zip")

    def test_verify_split_zip_rejects_mismatched_sidecar(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = self._write_zip(tmp)
            sidecar = "%s  split.zip\n" % ("0" * 64)
            with mock.patch.object(updater, "_fetch_text", return_value=sidecar):
                with self.assertRaisesRegex(RuntimeError, "sha256 digest mismatch"):
                    updater._verify_split_zip(
                        zip_path, "https://example.invalid/split.zip"
                    )

    def test_verify_split_zip_skips_when_sidecar_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            zip_path = self._write_zip(tmp)

            def _raise(*_args, **_kwargs):
                raise RuntimeError("404")

            with (
                mock.patch.object(updater, "_fetch_text", side_effect=_raise),
                mock.patch.object(updater, "_append_log"),
            ):
                # 旧版本无旁注文件，应静默跳过而非报错。
                updater._verify_split_zip(zip_path, "https://example.invalid/split.zip")

    def test_parse_sha256_extracts_hex_digest(self):
        self.assertEqual(updater._parse_sha256("a" * 64 + "  file.zip"), "a" * 64)
        self.assertEqual(updater._parse_sha256("not-a-hash file"), "")


class UpdateFinishHandoffTests(unittest.TestCase):
    def test_write_finish_worker_uses_tmp_script_not_client_py(self):
        with tempfile.TemporaryDirectory() as tmp:
            with (
                mock.patch.object(updater, "WORK_DIR", tmp),
                mock.patch.object(updater, "STATUS_FILE", os.path.join(tmp, "status.json")),
                mock.patch.object(updater, "LOG_FILE", os.path.join(tmp, "update.log")),
                mock.patch.object(updater, "LOCK_FILE", os.path.join(tmp, "update.lock")),
            ):
                script, plan = updater._write_finish_worker(
                    ["/tmp/pkg.ipk"],
                    "opkg",
                    {"latest_tag": "v1.3.5-b2", "package_format": "ipk"},
                )
                self.assertTrue(script.startswith(tmp))
                self.assertTrue(script.endswith("finish_update.py"))
                self.assertNotIn("client.py", script)
                self.assertTrue(os.path.isfile(script))
                self.assertTrue(os.path.isfile(plan))
                with open(plan, "r", encoding="utf-8") as handle:
                    payload = handle.read()
                self.assertIn("opkg", payload)
                self.assertIn("/tmp/pkg.ipk", payload)
                with open(script, "r", encoding="utf-8") as handle:
                    body = handle.read()
                self.assertIn("opkg", body)
                self.assertIn("apk", body)
                self.assertIn("smart_srun", body)

    def test_run_update_hands_off_install_and_keeps_lock_for_worker(self):
        with tempfile.TemporaryDirectory() as tmp:
            status_file = os.path.join(tmp, "status.json")
            lock_file = os.path.join(tmp, "update.lock")
            log_file = os.path.join(tmp, "update.log")
            work_dir = os.path.join(tmp, "work")
            os.makedirs(work_dir)

            plan = {
                "ok": True,
                "update_available": True,
                "package_manager": "opkg",
                "package_format": "ipk",
                "install_mode": "bundle",
                "package_name": "luci-app-smart-srun-bundle",
                "current_version": "v1.3.4",
                "latest_tag": "v1.3.5-b2",
                "latest_version": "1.3.5-b2",
                "download_kind": "release_asset",
                "asset_name": "bundle.ipk",
                "download_url": "https://example.invalid/bundle.ipk",
                "asset_digest": "",
            }

            class FakeProc:
                pid = 4242

            def fake_spawn(script_path, plan_path):
                updater._write_lock_pid(FakeProc.pid)
                return FakeProc()

            with (
                mock.patch.object(updater, "WORK_DIR", work_dir),
                mock.patch.object(updater, "STATUS_FILE", status_file),
                mock.patch.object(updater, "LOG_FILE", log_file),
                mock.patch.object(updater, "LOCK_FILE", lock_file),
                mock.patch.object(updater, "check_update", return_value=plan),
                mock.patch.object(updater, "_download_url"),
                mock.patch.object(updater, "_verify_digest", return_value=True),
                mock.patch.object(updater, "_run_command"),
                mock.patch.object(
                    updater, "_spawn_finish_worker", side_effect=fake_spawn
                ) as spawn,
                mock.patch.object(updater, "log"),
            ):
                result = updater.run_update()

            self.assertTrue(result.get("running"))
            self.assertEqual(result.get("phase"), "installing")
            spawn.assert_called_once()
            # Parent handed off: lock remains and points at worker pid.
            self.assertTrue(os.path.exists(lock_file))
            with open(lock_file, "r", encoding="ascii") as handle:
                self.assertEqual(handle.read().strip(), "4242")

    def test_finish_worker_script_completes_status_for_opkg_and_apk(self):
        """Execute generated worker with a fake package manager (both formats)."""
        for manager, package_name in (
            ("opkg", "bundle.ipk"),
            ("apk", "bundle.apk"),
        ):
            with self.subTest(manager=manager):
                with tempfile.TemporaryDirectory() as tmp:
                    status_file = os.path.join(tmp, "status.json")
                    lock_file = os.path.join(tmp, "update.lock")
                    log_file = os.path.join(tmp, "update.log")
                    work_dir = os.path.join(tmp, "work")
                    os.makedirs(work_dir)
                    package_path = os.path.join(tmp, package_name)
                    with open(package_path, "wb") as handle:
                        handle.write(b"pkg")

                    with (
                        mock.patch.object(updater, "WORK_DIR", work_dir),
                        mock.patch.object(updater, "STATUS_FILE", status_file),
                        mock.patch.object(updater, "LOG_FILE", log_file),
                        mock.patch.object(updater, "LOCK_FILE", lock_file),
                    ):
                        script, plan = updater._write_finish_worker(
                            [package_path],
                            manager,
                            {
                                "latest_tag": "v1.3.5-b2",
                                "package_format": "apk"
                                if manager == "apk"
                                else "ipk",
                            },
                        )

                    def fake_popen(args, **kwargs):
                        class Proc:
                            returncode = 0

                            def communicate(self, timeout=None):
                                return ("ok\n", None)

                            def kill(self):
                                return None

                        fake_popen.calls.append(list(args))
                        return Proc()

                    fake_popen.calls = []

                    with open(script, "r", encoding="utf-8") as handle:
                        code = compile(handle.read(), script, "exec")
                    ns = {"__name__": "not_main"}
                    exec(code, ns)
                    with mock.patch.object(
                        ns["subprocess"], "Popen", side_effect=fake_popen
                    ):
                        with mock.patch.object(
                            ns["os"].path, "exists", return_value=True
                        ):
                            rc = ns["main"](["finish_update.py", plan])
                    self.assertEqual(rc, 0)
                    with open(status_file, "r", encoding="utf-8") as handle:
                        status = handle.read()
                    self.assertIn('"phase": "complete"', status)
                    self.assertIn('"running": false', status)
                    self.assertFalse(os.path.exists(lock_file))
                    self.assertGreaterEqual(len(fake_popen.calls), 1)
                    self.assertEqual(fake_popen.calls[0][0], manager)


class InitScriptUpdateSafetyTests(unittest.TestCase):
    def test_init_script_only_stops_daemon_not_all_client_py(self):
        init_path = os.path.join(
            REPO_ROOT, "root", "etc", "init.d", "smart_srun"
        )
        with open(init_path, "r", encoding="utf-8") as handle:
            text = handle.read()
        self.assertIn("is_daemon_cmdline", text)
        self.assertIn('*"$PROG"\\ daemon', text)
        # Must not use the old broad matcher that killed update workers.
        self.assertNotIn('*"$PROG"*)', text)


class LockWriteAtomicityTests(unittest.TestCase):
    def test_write_lock_pid_uses_atomic_replace(self):
        with tempfile.TemporaryDirectory() as tmp:
            lock_file = os.path.join(tmp, "update.lock")
            with (
                mock.patch.object(updater, "LOCK_FILE", lock_file),
            ):
                updater._write_lock_pid(13579)
            self.assertFalse(os.path.exists(lock_file + ".tmp"))
            with open(lock_file, "r", encoding="ascii") as handle:
                self.assertEqual(handle.read().strip(), "13579")
            # Second write still atomic and replaces content.
            with mock.patch.object(updater, "LOCK_FILE", lock_file):
                updater._write_lock_pid(24680)
            with open(lock_file, "r", encoding="ascii") as handle:
                self.assertEqual(handle.read().strip(), "24680")

    def test_finish_worker_claims_lock_via_replace(self):
        with tempfile.TemporaryDirectory() as tmp:
            work_dir = os.path.join(tmp, "work")
            os.makedirs(work_dir)
            lock_file = os.path.join(tmp, "update.lock")
            status_file = os.path.join(tmp, "status.json")
            log_file = os.path.join(tmp, "update.log")
            with (
                mock.patch.object(updater, "WORK_DIR", work_dir),
                mock.patch.object(updater, "STATUS_FILE", status_file),
                mock.patch.object(updater, "LOG_FILE", log_file),
                mock.patch.object(updater, "LOCK_FILE", lock_file),
            ):
                script, _plan = updater._write_finish_worker(
                    [os.path.join(tmp, "pkg.ipk")], "opkg", {}
                )
            with open(script, "r", encoding="utf-8") as handle:
                body = handle.read()
            self.assertIn("os.replace(tmp_lock, lock_file)", body)
            self.assertNotIn(
                'with open(lock_file, "w", encoding="ascii") as handle:',
                body,
            )


if __name__ == "__main__":
    unittest.main()
