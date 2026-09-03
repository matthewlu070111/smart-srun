import os
import sys
import unittest
from unittest import mock

MODULE_ROOT = os.path.join(
    os.path.dirname(os.path.dirname(__file__)), "root", "usr", "lib", "smart_srun"
)
if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)

# Runtime modules use bare imports after the local source path is installed.
from _portal_urls import PORTAL_ORIGIN  # noqa: E402
import config  # noqa: E402
import daemon  # noqa: E402


def quiet_config(managed=(False,)):
    accounts = [
        {
            "id": "campus-%d" % index,
            "label": "Campus %d" % index,
            "access_mode": "wired",
            "wired_iface": "wan%d" % index,
            "auth_enabled": "1" if enabled else "0",
            "user_id": "student%d" % index,
            "password": "test-only",
            "base_url": PORTAL_ORIGIN,
        }
        for index, enabled in enumerate(managed)
    ]
    return config.resolve_active_items(
        {
            "school": "default",
            "multi_wan_enabled": "1",
            "force_logout_in_quiet": "1",
            "failover_enabled": "0",
            "campus_accounts": accounts,
            "active_campus_id": "campus-0",
            "default_campus_id": "campus-0",
            "hotspot_profiles": [],
        }
    )


class QuietMultiWanTests(unittest.TestCase):
    def tick(self, cfg, state, logout):
        with (
            mock.patch.object(daemon, "detect_runtime_mode", return_value="campus"),
            mock.patch.object(
                daemon.orchestrator, "run_quiet_logout", side_effect=logout
            ),
            mock.patch.object(
                daemon.orchestrator, "quiet_connection_state", return_value="已下线"
            ),
        ):
            return daemon._daemon_tick_quiet(cfg, state, 60)

    def test_empty_managed_set_still_logs_out_active_account(self):
        calls = []
        state = daemon._make_daemon_state()
        self.tick(
            quiet_config(),
            state,
            lambda cfg: (calls.append(cfg["campus_account_id"]) is None, "已下线"),
        )
        self.assertEqual(["campus-0"], calls)
        self.assertTrue(state["quiet_logout_done"])

    def test_active_account_is_not_logged_out_twice(self):
        calls = []
        state = daemon._make_daemon_state()
        self.tick(
            quiet_config((True, True)),
            state,
            lambda cfg: (calls.append(cfg["campus_account_id"]) is None, "已下线"),
        )
        self.assertEqual(["campus-0", "campus-1"], calls)

    def test_only_failed_accounts_are_retried_in_same_quiet_window(self):
        calls = []
        cfg = quiet_config((True, True))
        state = daemon._make_daemon_state()

        def logout(account):
            account_id = account["campus_account_id"]
            calls.append(account_id)
            return account_id == "campus-0" or calls.count(account_id) > 1, "result"

        self.tick(cfg, state, logout)
        self.assertFalse(state["quiet_logout_done"])
        self.tick(cfg, state, logout)
        self.assertEqual(["campus-0", "campus-1", "campus-1"], calls)
        self.assertTrue(state["quiet_logout_done"])

    def test_pause_without_forced_logout_keeps_sessions(self):
        cfg = quiet_config((False, True))
        cfg["force_logout_in_quiet"] = "0"
        state = daemon._make_daemon_state()
        self.tick(cfg, state, lambda _: self.fail("pause must not log out"))
        self.assertTrue(state["quiet_logout_done"])
        self.assertTrue(
            all(
                item["status"] == "paused"
                for item in state["wired_auth_sessions"].values()
            )
        )

    def test_new_quiet_window_retries_previous_successful_accounts(self):
        calls = []
        cfg = quiet_config((True,))
        state = daemon._make_daemon_state()
        def logout(account):
            calls.append(account["campus_account_id"])
            return True, "已下线"

        self.tick(cfg, state, logout)
        state["was_in_quiet"] = False
        self.tick(cfg, state, logout)
        self.assertEqual(["campus-0", "campus-0"], calls)

    def test_mixed_wifi_and_wired_only_retries_failed_line(self):
        cfg = quiet_config((False, True))
        cfg["campus_accounts"][0]["access_mode"] = "wifi"
        cfg = config.resolve_active_items(cfg)
        state = daemon._make_daemon_state()
        calls = []

        def logout(account):
            account_id = account["campus_account_id"]
            calls.append(account_id)
            if account_id == "campus-1":
                return calls.count(account_id) > 1, "wired result"
            return calls.count(account_id) == 1, "wifi result"

        self.tick(cfg, state, logout)
        self.assertFalse(state["quiet_logout_done"])
        self.tick(cfg, state, logout)
        self.assertEqual(["campus-1", "campus-0", "campus-1"], calls)
        self.assertTrue(state["quiet_logout_done"])

    def test_enabling_force_logout_during_pause_applies_new_policy(self):
        cfg = quiet_config((True,))
        cfg["force_logout_in_quiet"] = "0"
        state = daemon._make_daemon_state()
        self.tick(cfg, state, lambda _: self.fail("pause must not log out"))
        cfg["force_logout_in_quiet"] = "1"
        calls = []
        self.tick(
            cfg,
            state,
            lambda item: (calls.append(item["campus_account_id"]) is None, "已下线"),
        )
        self.assertEqual(["campus-0"], calls)


if __name__ == "__main__":
    unittest.main()
