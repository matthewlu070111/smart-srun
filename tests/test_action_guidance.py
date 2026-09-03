import json
import os
import sys
import tempfile
import time
import unittest
from unittest import mock

MODULE_ROOT = os.path.join(
    os.path.dirname(os.path.dirname(__file__)), "root", "usr", "lib", "smart_srun"
)
if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)

# Runtime modules use bare imports after the local source path is installed.
from _portal_urls import CLIENT_IP, PORTAL_ORIGIN  # noqa: E402
import config  # noqa: E402
import daemon  # noqa: E402


class ActionGuidanceTests(unittest.TestCase):
    def dispatch(self, cfg, result=(False, "网关未返回认证数据"), prior=None):
        with tempfile.TemporaryDirectory() as temp:
            state_file = os.path.join(temp, "state.json")
            with mock.patch.object(config, "STATE_FILE", state_file):
                config.save_runtime_state(prior or {})
                with (
                    mock.patch.object(
                        daemon,
                        "pop_runtime_action",
                        return_value={
                            "action": "manual_login",
                            "requested_at": int(time.time()),
                        },
                    ),
                    mock.patch.object(daemon, "mark_inflight_action"),
                    mock.patch.object(daemon, "clear_inflight_action"),
                    mock.patch.object(
                        daemon.school_runtime,
                        "dispatch_runtime_action",
                        return_value=result,
                    ),
                    mock.patch.object(
                        daemon,
                        "build_runtime_snapshot",
                        return_value={"connectivity_level": "online"},
                    ),
                ):
                    state = daemon._make_daemon_state()
                    daemon.handle_runtime_action(
                        cfg, state, runtime=object(), app_ctx={"cfg": cfg}
                    )
                config.save_runtime_status("后续守护概况", state)
                return config.load_runtime_state()

    def test_terminal_message_survives_later_tick(self):
        state = self.dispatch({"base_url": PORTAL_ORIGIN})
        self.assertEqual("后续守护概况", state["message"])
        self.assertIn("网关未返回认证数据", state["last_action_message"])
        self.assertEqual(PORTAL_ORIGIN, state["last_action_portal_url"])
        self.assertEqual("error", state["action_result"])

    def test_success_clears_old_portal_guidance(self):
        state = self.dispatch(
            {"base_url": PORTAL_ORIGIN},
            (True, "登录成功"),
            {
                "last_action_portal_url": PORTAL_ORIGIN,
                "last_action_message": "旧失败",
            },
        )
        self.assertEqual("", state["last_action_portal_url"])
        self.assertEqual("登录成功", state["last_action_message"])

    def test_unsafe_portal_url_is_not_exposed(self):
        for url in (
            "javascript:alert(1)",
            "http://name:secret@example.test",
            "http://example.test\\evil",
            "http://bad host",
        ):
            with self.subTest(url=url):
                state = self.dispatch({"base_url": url})
                self.assertEqual("", state["last_action_portal_url"])

    def test_only_fresh_same_account_portal_evidence_is_used(self):
        cfg = {
            "base_url": PORTAL_ORIGIN,
            "school": "default",
            "active_campus_id": "a",
            "username": "student",
            "campus_access_mode": "wired",
            "wired_iface": "wan",
        }
        prior = {
            "connectivity_cache_key": json.dumps(
                [
                    "a",
                    "student",
                    "wan",
                    "eth1",
                    CLIENT_IP,
                    PORTAL_ORIGIN,
                    "default",
                    "",
                    False,
                ]
            ),
            "connectivity_checked_at": int(time.time()),
            "current_ip": CLIENT_IP,
            "current_iface": "wan",
            "connectivity": "认证网关可达",
            "connectivity_level": "portal",
        }
        state = self.dispatch(cfg, prior=prior)
        self.assertIn("认证网关可达", state["last_action_message"])
        cfg["active_campus_id"] = "b"
        state = self.dispatch(cfg, prior=prior)
        self.assertNotIn("认证网关可达", state["last_action_message"])


if __name__ == "__main__":
    unittest.main()
