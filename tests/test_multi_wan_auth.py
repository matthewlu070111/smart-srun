import os
import sys
import unittest
from unittest import mock


THIS_DIR = os.path.dirname(os.path.abspath(__file__))
WORKTREE_ROOT = os.path.dirname(THIS_DIR)
MODULE_DIR = os.path.join(WORKTREE_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_DIR not in sys.path:
    sys.path.insert(0, MODULE_DIR)


from _portal_urls import BIND_IP, CLIENT_IP, PORTAL_ORIGIN, WIRED_BIND_IP
import config
import daemon


class _FakeRuntime(object):
    def build_urls(self, base_url):
        return {"init_url": base_url + "/srun_portal_pc"}


def _account(account_id, iface, user_id, suffix="carrier", enabled="1"):
    return {
        "id": account_id,
        "label": account_id,
        "access_mode": "wired",
        "wired_iface": iface,
        "auth_enabled": enabled,
        "base_url": PORTAL_ORIGIN,
        "ac_id": "1",
        "user_id": user_id,
        "password": "secret-" + account_id,
        "operator_suffix": suffix,
    }


def _cfg(accounts, enabled="1"):
    return {
        "multi_wan_enabled": enabled,
        "school": "default",
        "active_campus_id": accounts[0]["id"] if accounts else "",
        "default_campus_id": accounts[0]["id"] if accounts else "",
        "campus_accounts": accounts,
        "hotspot_profiles": [],
        "n": "200",
        "type": "1",
        "enc": "srun_bx1",
        "backoff_enable": "1",
        "retry_cooldown_seconds": "10",
        "retry_max_cooldown_seconds": "60",
    }


class MultiWanConfigTests(unittest.TestCase):
    def test_each_enabled_wired_account_resolves_its_own_credentials_and_interface(self):
        accounts = [
            _account("campus-1", "wan", "1001", "carrier-a"),
            _account("campus-2", "wan.v2", "1002", "carrier-b"),
            _account("campus-3", "wan.v3", "1003", "carrier-c"),
            _account(
                "campus-off", "wan.v4", "1004", "carrier-d", enabled="0"
            ),
        ]
        cfg = _cfg(accounts)

        resolved = config.get_managed_wired_account_configs(cfg)

        self.assertEqual(["wan", "wan.v2", "wan.v3"], [c["wired_iface"] for c in resolved])
        self.assertEqual(
            ["1001@carrier-a", "1002@carrier-b", "1003@carrier-c"],
            [c["username"] for c in resolved],
        )
        self.assertEqual(
            ["secret-campus-1", "secret-campus-2", "secret-campus-3"],
            [c["password"] for c in resolved],
        )
        self.assertTrue(all(c.get("_multi_wan_strict_bind") == "1" for c in resolved))
        self.assertEqual("campus-1", cfg["active_campus_id"])

    def test_global_switch_keeps_legacy_mode_until_explicitly_enabled(self):
        cfg = _cfg([_account("campus-1", "wan", "1001")], enabled="0")
        self.assertEqual([], config.get_managed_wired_account_configs(cfg))


class MultiWanDaemonTests(unittest.TestCase):
    def setUp(self):
        self.accounts = [
            _account("campus-1", "wan", "1001", "carrier-a"),
            _account("campus-2", "wan.v2", "1002", "carrier-b"),
            _account("campus-3", "wan.v3", "1003", "carrier-c"),
        ]
        self.cfg = _cfg(self.accounts)
        self.ips = {
            "wan": CLIENT_IP,
            "wan.v2": BIND_IP,
            "wan.v3": WIRED_BIND_IP,
        }

    def test_daemon_maintains_three_bound_sessions_independently(self):
        queried = []
        logged_in = []

        def query(_app_ctx, expected_username=None, bind_ip=None):
            queried.append((expected_username, bind_ip))
            if bind_ip == self.ips["wan"]:
                return True, expected_username, "在线"
            return False, "", "not_online_error"

        def login(account_cfg):
            logged_in.append((account_cfg["username"], account_cfg["wired_iface"]))
            return True, "登录成功"

        state = daemon._make_daemon_state()
        with (
            mock.patch.object(
                daemon.school_runtime,
                "build_app_context",
                return_value={"runtime": _FakeRuntime()},
            ),
            mock.patch.object(
                daemon,
                "resolve_bind_ip",
                side_effect=lambda _url, account_cfg: self.ips[account_cfg["wired_iface"]],
            ),
            mock.patch.object(daemon.srun_auth, "query_online_identity", side_effect=query),
            mock.patch.object(daemon.srun_auth, "run_once_safe", side_effect=login),
        ):
            ids, message, sleep = daemon._maintain_managed_wired_accounts(
                self.cfg, state, 60, now=1000
            )

        self.assertEqual({"campus-1", "campus-2", "campus-3"}, ids)
        self.assertEqual(
            [
                ("1001@carrier-a", CLIENT_IP),
                ("1002@carrier-b", BIND_IP),
                ("1003@carrier-c", WIRED_BIND_IP),
            ],
            queried,
        )
        self.assertEqual(
            [
                ("1002@carrier-b", "wan.v2"),
                ("1003@carrier-c", "wan.v3"),
            ],
            logged_in,
        )
        self.assertEqual(3, sum(1 for s in state["wired_auth_sessions"].values() if s["online"]))
        self.assertIn("3/3", message)
        self.assertEqual(60, sleep)
        self.assertNotIn("secret-", repr(state["wired_auth_sessions"]))

    def test_failed_line_has_its_own_retry_deadline(self):
        account_cfg = config.get_managed_wired_account_configs(
            _cfg([self.accounts[1]])
        )[0]
        state = daemon._make_daemon_state()

        with (
            mock.patch.object(
                daemon.school_runtime,
                "build_app_context",
                return_value={"runtime": _FakeRuntime()},
            ),
            mock.patch.object(daemon, "resolve_bind_ip", return_value=BIND_IP),
            mock.patch.object(
                daemon.srun_auth,
                "query_online_identity",
                return_value=(False, "", "not_online_error"),
            ),
            mock.patch.object(
                daemon.srun_auth, "run_once_safe", return_value=(False, "登录失败")
            ) as login,
        ):
            first = daemon._maintain_one_wired_account(account_cfg, {}, now=1000)
            second = daemon._maintain_one_wired_account(account_cfg, first, now=1001)

        self.assertEqual("error", first["status"])
        self.assertEqual(1010, first["next_retry_at"])
        self.assertEqual("retry_wait", second["status"])
        # Only the first call logs in; the second one is still inside backoff.
        self.assertEqual(1, login.call_count)

    def test_global_switch_without_selected_wired_account_stays_idle(self):
        cfg = config.resolve_active_items(
            _cfg([_account("campus-1", "wan", "1001", enabled="0")])
        )
        cfg["failover_enabled"] = "1"
        state = daemon._make_daemon_state()

        with (
            mock.patch.object(
                daemon,
                "ensure_expected_profile",
                side_effect=AssertionError("failover must not own an unchecked row"),
            ),
            mock.patch.object(
                daemon.srun_auth,
                "run_once_safe",
                side_effect=AssertionError("unchecked row must not be authenticated"),
            ),
        ):
            message, sleep = daemon._daemon_tick_active(cfg, state, 60)

        self.assertIn("没有勾选", message)
        self.assertEqual({}, state["wired_auth_sessions"])
        self.assertFalse(state["was_online"])
        self.assertEqual(60, sleep)

    def test_multi_wan_maintenance_runs_before_active_iface_failover_check(self):
        cfg = config.resolve_active_items(_cfg(self.accounts))
        cfg["failover_enabled"] = "1"
        state = daemon._make_daemon_state()
        events = []

        def maintain(*_args, **_kwargs):
            events.append("maintain")
            return {"campus-1", "campus-2", "campus-3"}, "多 WAN 已维护", 60

        def ensure(*_args, **_kwargs):
            events.append("failover")
            return False, "活跃接口无 IPv4", 0

        with (
            mock.patch.object(
                daemon, "_maintain_managed_wired_accounts", side_effect=maintain
            ) as maintain_accounts,
            mock.patch.object(
                daemon, "ensure_expected_profile", side_effect=ensure
            ) as ensure_profile,
        ):
            message, sleep = daemon._daemon_tick_active(cfg, state, 60)

        self.assertEqual(["maintain", "failover"], events)
        maintain_accounts.assert_called_once()
        ensure_profile.assert_called_once()
        self.assertIn("多 WAN 已维护", message)
        self.assertEqual(30, sleep)


class MultiWanLuciSourceTests(unittest.TestCase):
    def test_luci_exposes_global_and_per_account_controls(self):
        with open(
            os.path.join(MODULE_DIR, "defaults.json"), encoding="utf-8"
        ) as handle:
            defaults_source = handle.read()
        with open(
            os.path.join(
                WORKTREE_ROOT, "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
            ),
            encoding="utf-8",
        ) as handle:
            model_source = handle.read()
        with open(
            os.path.join(
                WORKTREE_ROOT, "root", "www", "luci-static", "resources", "smart_srun.js"
            ),
            encoding="utf-8",
        ) as handle:
            js_source = handle.read()

        self.assertIn('"multi_wan_enabled": "0"', defaults_source)
        self.assertIn('Flag, "multi_wan_enabled", "多 WAN 并行认证"', model_source)
        self.assertIn(
            "if is_managed and next(wired_session) ~= nil then", model_source
        )
        self.assertIn('id="jm-auth_enabled"', js_source)
        self.assertIn("fd.append('auth_enabled'", js_source)


if __name__ == "__main__":
    unittest.main()
