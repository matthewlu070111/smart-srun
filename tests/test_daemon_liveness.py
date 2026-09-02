import fcntl
import io
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest import mock

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import daemon


class DaemonLivenessTests(unittest.TestCase):
    """state['daemon_running'] is only ever written True.

    An abnormal exit (SIGKILL / OOM / power loss) leaves it set, so status
    kept reporting a dead daemon as running. Liveness now comes from the
    flock that run_daemon() holds, which the kernel drops on death.
    """

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.lock_path = os.path.join(self._tmp.name, "daemon.lock")

    def _patch_lock(self):
        return mock.patch.object(daemon, "DAEMON_LOCK_FILE", self.lock_path)

    def test_missing_lock_file_reports_not_alive(self):
        with self._patch_lock():
            self.assertFalse(daemon.daemon_is_alive())

    def test_stale_unlocked_lock_file_reports_not_alive(self):
        with open(self.lock_path, "w", encoding="utf-8") as handle:
            handle.write("999999")

        with self._patch_lock():
            self.assertFalse(daemon.daemon_is_alive())

    def test_held_lock_reports_alive(self):
        with open(self.lock_path, "a+", encoding="utf-8") as holder:
            fcntl.flock(holder, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self._patch_lock():
                self.assertTrue(daemon.daemon_is_alive())

    def test_released_lock_reports_not_alive_again(self):
        with open(self.lock_path, "a+", encoding="utf-8") as holder:
            fcntl.flock(holder, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self._patch_lock():
                self.assertTrue(daemon.daemon_is_alive())
                fcntl.flock(holder, fcntl.LOCK_UN)
                self.assertFalse(daemon.daemon_is_alive())

    def test_status_reports_stopped_when_state_flag_is_stale(self):
        stale_state = {
            "daemon_running": True,
            "connectivity": "互联网可达",
            "connectivity_level": "online",
            "current_ip": "10.0.0.2",
            "current_ssid": "campus",
            "mode_label": "校园网模式",
            "campus_account_label": "someone",
        }
        cfg = {"enabled": "1", "interval": "60"}

        with self._patch_lock(), \
                mock.patch("config.load_runtime_state", return_value=stale_state), \
                mock.patch.object(
                    daemon.orchestrator, "run_status", return_value=(True, "在线")
                ):
            buffer = io.StringIO()
            with redirect_stdout(buffer):
                daemon._show_status(cfg)

        output = buffer.getvalue()
        self.assertIn("守护:   已停止", output)
        self.assertNotIn("守护:   运行中", output)


if __name__ == "__main__":
    unittest.main()
