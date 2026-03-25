import os
import sys
import unittest
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import orchestrator
import school_runtime
import snapshot
import srun_auth


def unexpected_low_level(name):
    def _raise(*args, **kwargs):
        raise AssertionError("low-level path used: %s" % name)

    return _raise


class FakeRuntime(object):
    def __init__(self):
        self.calls = []

    def build_urls(self, base_url):
        return {
            "init_url": base_url,
            "get_challenge_api": base_url + "/cgi-bin/get_challenge",
            "srun_portal_api": base_url + "/cgi-bin/srun_portal",
            "rad_user_info_api": base_url + "/cgi-bin/rad_user_info",
            "rad_user_dm_api": base_url + "/cgi-bin/rad_user_dm",
        }

    def login_once(self, app_ctx):
        self.calls.append(("login_once", app_ctx["cfg"].get("username")))
        return True, "runtime-login"

    def logout_once(self, app_ctx, override_user_id=None, bind_ip=None):
        self.calls.append(("logout_once", override_user_id, bind_ip))
        return True, "runtime-logout"

    def query_online_identity(self, app_ctx, expected_username=None, bind_ip=None):
        self.calls.append(("query_online_identity", expected_username, bind_ip))
        return True, "runtime-user", "runtime-online"

    def query_online_status(self, app_ctx, expected_username=None, bind_ip=None):
        self.calls.append(("query_online_status", expected_username, bind_ip))
        return True, "runtime-status"

    def status(self, app_ctx):
        self.calls.append(("status", app_ctx["cfg"].get("username")))
        return True, "runtime-status"

    def quiet_logout(self, app_ctx):
        self.calls.append(("quiet_logout", app_ctx["cfg"].get("username")))
        return True, "runtime-quiet-logout"


class SchoolRuntimeDispatchTests(unittest.TestCase):
    def setUp(self):
        self.cfg = {
            "school": "custom",
            "base_url": "https://portal.example.edu",
            "username": "20230001@cmcc",
            "password": "secret",
            "ac_id": "1",
            "enc": "srun_bx1",
            "n": "200",
            "type": "1",
            "campus_ssid": "campus-net",
            "hotspot_ssid": "phone-hotspot",
            "campus_bssid": "",
            "campus_access_mode": "wifi",
            "campus_account_label": "Campus",
            "hotspot_profile_label": "Hotspot",
            "force_logout_in_quiet": "1",
        }
        self.runtime = FakeRuntime()
        self.app_ctx = {
            "cfg": self.cfg,
            "runtime": self.runtime,
            "core_api": {},
            "runtime_api_version": school_runtime.RUNTIME_API_VERSION,
            "school_metadata": {"short_name": "custom"},
        }

    def bind_app_context(self):
        return mock.patch.multiple(
            srun_auth,
            build_app_context=mock.DEFAULT,
            create=True,
        )

    def test_login_once_uses_runtime_override(self):
        with (
            mock.patch.object(
                srun_auth, "build_app_context", return_value=self.app_ctx, create=True
            ),
            mock.patch.object(
                srun_auth, "init_getip", side_effect=unexpected_low_level("init_getip")
            ),
            mock.patch.object(
                srun_auth, "get_token", side_effect=unexpected_low_level("get_token")
            ),
            mock.patch.object(
                srun_auth, "login", side_effect=unexpected_low_level("login")
            ),
        ):
            ok, message = srun_auth.run_once(self.cfg)

        self.assertEqual((ok, message), (True, "runtime-login"))
        self.assertIn(("login_once", self.cfg["username"]), self.runtime.calls)

    def test_run_once_safe_preserves_readable_error_when_runtime_build_fails(self):
        with mock.patch.object(
            srun_auth,
            "build_app_context",
            side_effect=RuntimeError("runtime init exploded"),
            create=True,
        ):
            ok, message = srun_auth.run_once_safe(dict(self.cfg))

        self.assertFalse(ok)
        self.assertEqual(message, "错误: runtime init exploded")

    def test_partial_runtime_inherits_default_boundary_methods(self):
        class PartialRuntime(object):
            def __init__(self):
                self.calls = []

            def build_urls(self, base_url):
                return {
                    "init_url": base_url,
                    "get_challenge_api": base_url + "/cgi-bin/get_challenge",
                    "srun_portal_api": base_url + "/cgi-bin/srun_portal",
                    "rad_user_info_api": base_url + "/cgi-bin/rad_user_info",
                    "rad_user_dm_api": base_url + "/cgi-bin/rad_user_dm",
                }

            def query_online_status(
                self, app_ctx, expected_username=None, bind_ip=None
            ):
                self.calls.append(("query_online_status", expected_username, bind_ip))
                return True, "partial-runtime-status"

        runtime = school_runtime._finalize_runtime(
            PartialRuntime(),
            {"short_name": "partial", "name": "Partial", "description": ""},
            "build_runtime",
            "partial_runtime.py",
        )
        app_ctx = {
            "cfg": dict(self.cfg),
            "runtime": runtime,
            "core_api": school_runtime.build_core_api(),
            "runtime_api_version": school_runtime.RUNTIME_API_VERSION,
            "school_metadata": {"short_name": "partial"},
        }

        online, message = runtime.status(app_ctx)

        self.assertEqual((online, message), (True, "partial-runtime-status"))
        self.assertIn(
            ("query_online_status", self.cfg["username"], None), runtime.calls
        )

    def test_logout_once_uses_runtime_override(self):
        with (
            mock.patch.object(
                srun_auth, "build_app_context", return_value=self.app_ctx, create=True
            ),
            mock.patch.object(
                srun_auth, "init_getip", side_effect=unexpected_low_level("init_getip")
            ),
            mock.patch.object(
                srun_auth, "logout", side_effect=unexpected_low_level("logout")
            ),
        ):
            ok, message = srun_auth.run_logout_once(
                self.cfg, override_user_id="20230001"
            )

        self.assertEqual((ok, message), (True, "runtime-logout"))
        self.assertIn(("logout_once", "20230001", None), self.runtime.calls)

    def test_online_query_uses_runtime_override(self):
        with mock.patch.object(
            srun_auth, "http_get", side_effect=unexpected_low_level("http_get")
        ):
            online, user_id, message = srun_auth.query_online_identity(self.app_ctx)

        self.assertEqual(
            (online, user_id, message), (True, "runtime-user", "runtime-online")
        )
        self.assertIn(
            ("query_online_identity", self.cfg["username"], None), self.runtime.calls
        )

    def test_status_lookup_uses_runtime_override(self):
        with (
            mock.patch.object(
                orchestrator,
                "build_app_context",
                return_value=self.app_ctx,
                create=True,
            ),
            mock.patch.object(orchestrator, "in_quiet_window", return_value=False),
            mock.patch.object(
                orchestrator.srun_auth,
                "query_online_status",
                side_effect=unexpected_low_level("query_online_status"),
            ),
        ):
            online, message = orchestrator.run_status(dict(self.cfg))

        self.assertEqual((online, message), (True, "runtime-status"))
        self.assertIn(("status", self.cfg["username"]), self.runtime.calls)

    def test_quiet_window_logout_uses_runtime_override(self):
        with (
            mock.patch.object(
                orchestrator,
                "build_app_context",
                return_value=self.app_ctx,
                create=True,
            ),
            mock.patch.object(
                orchestrator.srun_auth,
                "logout",
                side_effect=unexpected_low_level("logout"),
            ),
            mock.patch.object(
                orchestrator.srun_auth,
                "wait_for_logout_status",
                side_effect=unexpected_low_level("wait_for_logout_status"),
            ),
        ):
            ok, message = orchestrator.run_quiet_logout(dict(self.cfg))

        self.assertEqual((ok, message), (True, "runtime-quiet-logout"))
        self.assertIn(("quiet_logout", self.cfg["username"]), self.runtime.calls)

    def test_quiet_window_logout_followup_status_uses_runtime_dispatch(self):
        class FollowupRuntime(FakeRuntime):
            def logout_once(self, app_ctx, override_user_id=None, bind_ip=None):
                self.calls.append(("logout_once", override_user_id, bind_ip))
                return True, "runtime-logout"

            def query_online_status(
                self, app_ctx, expected_username=None, bind_ip=None
            ):
                self.calls.append(("query_online_status", expected_username, bind_ip))
                return False, "离线"

            def build_online_query_params(self):
                raise AssertionError("low-level path used: build_online_query_params")

        runtime = FollowupRuntime()
        app_ctx = dict(self.app_ctx)
        app_ctx["runtime"] = runtime

        ok, message = orchestrator.default_run_quiet_logout(app_ctx)

        self.assertEqual((ok, message), (True, "夜间停用下线成功"))
        self.assertIn(("logout_once", None, None), runtime.calls)
        self.assertIn(
            ("query_online_status", self.cfg["username"], None), runtime.calls
        )

    def test_snapshot_uses_runtime_online_identity_override(self):
        with (
            mock.patch.object(
                snapshot, "build_app_context", return_value=self.app_ctx, create=True
            ),
            mock.patch.object(snapshot, "parse_wireless_iface_data", return_value={}),
            mock.patch.object(snapshot, "get_runtime_sta_section", return_value="sta0"),
            mock.patch.object(
                snapshot,
                "get_sta_profile_from_section",
                return_value={
                    "ssid": self.cfg["campus_ssid"],
                    "bssid": "AA:BB:CC:DD:EE:FF",
                },
            ),
            mock.patch.object(
                snapshot, "get_network_interface_from_sta_section", return_value="wlan0"
            ),
            mock.patch.object(
                snapshot, "get_ipv4_from_network_interface", return_value="10.0.0.8"
            ),
            mock.patch.object(snapshot, "load_runtime_state", return_value={}),
            mock.patch.object(snapshot, "campus_uses_wired", return_value=False),
            mock.patch.object(
                snapshot, "test_internet_connectivity", return_value=(True, "ok")
            ),
            mock.patch.object(
                snapshot, "test_portal_reachability", return_value=(False, "offline")
            ),
            mock.patch.object(
                snapshot.srun_auth,
                "query_online_identity",
                side_effect=unexpected_low_level("query_online_identity"),
            ),
        ):
            data = snapshot.build_runtime_snapshot(dict(self.cfg), state={})

        self.assertEqual(data["online_account_label"], "runtime-user")
        self.assertEqual(data["current_ssid"], self.cfg["campus_ssid"])
        self.assertIn(
            ("query_online_identity", self.cfg["username"], None), self.runtime.calls
        )


if __name__ == "__main__":
    unittest.main()
