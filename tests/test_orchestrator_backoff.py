import os
import sys
import unittest

from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import orchestrator


class RetryInterruptionTests(unittest.TestCase):
    def setUp(self):
        self.cfg = {
            "enabled": "1",
            "backoff_enable": "1",
            "backoff_max_retries": "0",
        }

    def test_retry_loop_stops_before_sleep_when_action_is_pending(self):
        runtime_cfg = dict(self.cfg)

        with (
            mock.patch.object(
                orchestrator.srun_auth,
                "run_once_safe",
                side_effect=[(False, "login failed"), (True, "should not run")],
            ) as run_once,
            mock.patch.object(orchestrator, "load_config", return_value=runtime_cfg),
            mock.patch.object(orchestrator, "backoff_enabled", return_value=True),
            mock.patch.object(orchestrator, "in_quiet_window", return_value=False),
            mock.patch.object(
                orchestrator, "calc_backoff_delay_seconds", return_value=30
            ),
            mock.patch.object(
                orchestrator,
                "load_json_file",
                create=True,
                return_value={"action": "manual_logout"},
            ),
            mock.patch.object(
                orchestrator, "ACTION_FILE", "/tmp/action.json", create=True
            ),
            mock.patch.object(orchestrator, "log"),
            mock.patch.object(
                orchestrator.time,
                "sleep",
                side_effect=AssertionError("should not sleep with queued action"),
            ),
        ):
            ok, message = orchestrator.run_once_with_retry(dict(self.cfg))

        self.assertFalse(ok)
        self.assertIn("待处理操作", message)
        self.assertEqual(run_once.call_count, 1)

    def test_retry_wait_is_interruptible_when_action_appears_mid_sleep(self):
        runtime_cfg = dict(self.cfg)
        pending_payloads = iter(
            [{}, {}, {"action": "switch_hotspot"}, {"action": "switch_hotspot"}]
        )
        sleep_calls = []

        def fake_load_json_file(_path):
            return dict(next(pending_payloads))

        def fake_sleep(seconds):
            sleep_calls.append(seconds)

        with (
            mock.patch.object(
                orchestrator.srun_auth,
                "run_once_safe",
                side_effect=[(False, "login failed"), (True, "should not run")],
            ) as run_once,
            mock.patch.object(orchestrator, "load_config", return_value=runtime_cfg),
            mock.patch.object(orchestrator, "backoff_enabled", return_value=True),
            mock.patch.object(orchestrator, "in_quiet_window", return_value=False),
            mock.patch.object(
                orchestrator, "calc_backoff_delay_seconds", return_value=5
            ),
            mock.patch.object(
                orchestrator,
                "load_json_file",
                create=True,
                side_effect=fake_load_json_file,
            ),
            mock.patch.object(
                orchestrator, "ACTION_FILE", "/tmp/action.json", create=True
            ),
            mock.patch.object(orchestrator, "log"),
            mock.patch.object(orchestrator.time, "sleep", side_effect=fake_sleep),
        ):
            ok, message = orchestrator.run_once_with_retry(dict(self.cfg))

        self.assertFalse(ok)
        self.assertIn("待处理操作", message)
        self.assertEqual(run_once.call_count, 1)
        self.assertGreaterEqual(len(sleep_calls), 1)
        self.assertTrue(all(call <= 2.0 for call in sleep_calls), sleep_calls)

    def test_retry_wait_rechecks_action_after_short_final_sleep_chunk(self):
        runtime_cfg = dict(self.cfg)
        pending_payloads = iter(
            [{}, {"action": "manual_login"}, {"action": "manual_login"}]
        )
        sleep_calls = []

        def fake_load_json_file(_path):
            return dict(next(pending_payloads))

        def fake_sleep(seconds):
            sleep_calls.append(seconds)

        with (
            mock.patch.object(
                orchestrator.srun_auth,
                "run_once_safe",
                side_effect=[(False, "login failed"), (True, "should not run")],
            ) as run_once,
            mock.patch.object(orchestrator, "load_config", return_value=runtime_cfg),
            mock.patch.object(orchestrator, "backoff_enabled", return_value=True),
            mock.patch.object(orchestrator, "in_quiet_window", return_value=False),
            mock.patch.object(
                orchestrator, "calc_backoff_delay_seconds", return_value=1
            ),
            mock.patch.object(
                orchestrator,
                "load_json_file",
                create=True,
                side_effect=fake_load_json_file,
            ),
            mock.patch.object(
                orchestrator, "ACTION_FILE", "/tmp/action.json", create=True
            ),
            mock.patch.object(orchestrator, "log"),
            mock.patch.object(orchestrator.time, "sleep", side_effect=fake_sleep),
        ):
            ok, message = orchestrator.run_once_with_retry(dict(self.cfg))

        self.assertFalse(ok)
        self.assertIn("待处理操作", message)
        self.assertEqual(run_once.call_count, 1)
        self.assertEqual(sleep_calls, [1])


if __name__ == "__main__":
    unittest.main()
