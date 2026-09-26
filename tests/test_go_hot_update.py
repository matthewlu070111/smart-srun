"""Host deployment must select exact recovery bytes and never invoke Python remotely."""
import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("go_hot_update_test", ROOT / "scripts/hot_update_go.py")
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class DeployTests(unittest.TestCase):
    def fixture(self, directory, version="2.0.0rc2", manager="opkg", kind="bundle"):
        fmt = "ipk" if manager == "opkg" else "apk"
        filename = "fixture-" + version + "-" + kind + "." + fmt
        path = Path(directory) / filename
        path.write_bytes(version.encode())
        asset = dict(id=version + "-" + kind, format=fmt, kind=kind, package_manager=manager,
                     openwrt_arch="x86_64", firmware_compat=["24.10"], package_version=version + "-r1",
                     url="https://github.com/matthewlu070111/smart-srun/releases/download/" + version + "/" + filename,
                     bytes=path.stat().st_size, sha256=hashlib.sha256(path.read_bytes()).hexdigest(), installed_bytes=100,
                     validation=dict(build=True, elf=True))
        manifest = dict(schema_version=1, release=version, assets=[asset])
        manifest_path = Path(directory) / (version + ".json")
        manifest_path.write_text(json.dumps(manifest))
        return manifest_path, asset, path

    def inventory(self):
        return dict(display_version="2.0.0rc1", packages={"luci-app-smart-srun-bundle": "2.0.0rc1-r1"},
                    architecture="x86_64", package_manager="opkg", firmware_family="24.10")

    def test_dry_run_checks_bytes_without_ssh_or_password(self):
        with tempfile.TemporaryDirectory() as directory:
            new, _, path = self.fixture(directory)
            old, _, _ = self.fixture(directory, "2.0.0rc1")
            args = ["--dry-run", "--manifest", str(new), "--assets", directory,
                    "--recovery-manifest", str(old), "--recovery-assets", directory]
            with mock.patch.object(deploy, "SSH", side_effect=AssertionError("must not connect")), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(deploy.main(args), 0)
                path.write_bytes(b"tampered")
                with contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(deploy.main(args), 1)

    def test_exact_recovery_and_ambiguous_selection_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            _, asset, path = self.fixture(directory)
            inventory = self.inventory()
            with self.assertRaises(ValueError):
                deploy.select([(asset, path)], inventory, recovery=True)
            with self.assertRaises(ValueError):
                deploy.select([(asset, path), (asset, path)], inventory)
            inventory["packages"]["smart-srun"] = "2.0.0rc1-r1"
            with self.assertRaises(ValueError):
                deploy.select([(asset, path)], inventory)

    def test_probe_does_not_upload_or_start_and_worker_poll_uses_copy(self):
        with tempfile.TemporaryDirectory() as directory:
            new, _, _ = self.fixture(directory)
            old, _, _ = self.fixture(directory, "2.0.0rc1")
            manifests = [deploy.load_manifest(new, directory), deploy.load_manifest(old, directory)]
            ssh = mock.Mock()
            ssh.json.side_effect = [self.inventory(), dict(running=False, phase="idle")]
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(deploy.deploy(SimpleNamespace(probe=True), ssh, manifests), 0)
            ssh.upload.assert_not_called()
            self.assertEqual(ssh.json.call_args_list, [mock.call("/usr/bin/srunnet update inventory"), mock.call(deploy.STATUS_COMMAND)])
            ssh.reset_mock()
            ssh.json.side_effect = [self.inventory(), dict(running=False, phase="idle"), dict(ok=True, plan_id="a" * 64),
                                    dict(running=True, job_id="b" * 32), dict(running=False, ok=True, phase="completed", job_id="b" * 32)]
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(deploy.deploy(SimpleNamespace(probe=False, prepare=False, background=False, wait=2), ssh, manifests), 0)
            self.assertEqual(ssh.upload.call_count, 2)
            self.assertEqual(ssh.json.call_args, mock.call(deploy.STATUS_COMMAND))
            for call in ssh.json.call_args_list:
                self.assertNotIn("python", call.args[0])
                self.assertNotIn("allow-untrusted", call.args[0])

    def test_unsafe_remote_asset_path_and_duplicate_manifest_fields_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, _, _ = self.fixture(directory)
            value = json.loads(manifest.read_text())
            value["assets"][0]["id"] = "../../etc/config/network"
            manifest.write_text(json.dumps(value))
            with self.assertRaises(ValueError):
                deploy.load_manifest(manifest, directory)
            manifest.write_text('{"schema_version":1,"schema_version":1,"assets":[]}')
            with self.assertRaises(ValueError):
                deploy.load_manifest(manifest, directory)

    def test_public_entrypoint_in_go_tree_cannot_run_legacy_uploader(self):
        spec = importlib.util.spec_from_file_location("hot_update_dispatch_test", ROOT / "scripts/hot_update.py")
        entry = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(entry)
        self.assertFalse(hasattr(entry, "legacy_main"))
        self.assertEqual(entry.main.__module__, "hot_update_go")
        with contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(SystemExit) as raised:
                entry.main(["--help"])
        self.assertEqual(raised.exception.code, 0)


if __name__ == "__main__":
    unittest.main()
