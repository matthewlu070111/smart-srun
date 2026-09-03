"""登录/在线判定语义测试。

覆盖两类现场常见语义：
1. E2620 "You are already online" 必须按成功处理——srun 会话按 IP 记，
   路由器重启后旧会话仍在，把它当失败会让守护带着退避无限重试。
2. 部分学校 rad_user_info 返回带后缀的完整账号（user@stu.example.test），
   在线判定与账号匹配都要按去后缀主体比对。

测试账号、门户与客户端 IP 均为占位符，不对应真实账号或校园网环境。
"""

import os
import sys
import unittest
from unittest import mock

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")
SCHOOLS_ROOT = os.path.join(MODULE_ROOT, "schools")
THIS_DIR = os.path.dirname(os.path.abspath(__file__))

for path in (THIS_DIR, MODULE_ROOT, SCHOOLS_ROOT):
    if path not in sys.path:
        sys.path.insert(0, path)

from _portal_urls import CLIENT_IP, PORTAL_IPV4_ORIGIN  # noqa: E402
import orchestrator  # noqa: E402
import srun_auth  # noqa: E402
from schools._base import SchoolProfile  # noqa: E402

# Synthetic fixtures — not real campus accounts.
FAKE_USER_ID = "student001"
FAKE_USER_SUFFIX = "stu.example.test"
FAKE_USERNAME = FAKE_USER_ID + "@" + FAKE_USER_SUFFIX
FAKE_USERNAME_CMCC = FAKE_USER_ID + "@cmcc"
FAKE_OTHER_USERNAME = "otheruser@" + FAKE_USER_SUFFIX


class StubRuntime:
    def build_urls(self, base_url):
        return {
            "init_url": base_url,
            "get_challenge_api": base_url + "/cgi-bin/get_challenge",
            "srun_portal_api": base_url + "/cgi-bin/srun_portal",
            "rad_user_info_api": base_url + "/cgi-bin/rad_user_info",
            "rad_user_dm_api": base_url + "/cgi-bin/rad_user_dm",
        }

    def do_complex_work(self, cfg, ip, token):
        return "info", "hmd5", "chksum"


def _login_app_ctx():
    return {
        "cfg": {
            "username": FAKE_USERNAME,
            "password": "secret",
            "base_url": PORTAL_IPV4_ORIGIN,
        },
        "runtime": StubRuntime(),
    }


class AlreadyOnlineLoginTests(unittest.TestCase):
    def _run_login(self, login_results, online_status=None, online_error=None):
        query = mock.Mock()
        if online_error is not None:
            query.side_effect = online_error
        else:
            query.return_value = online_status
        self.logout_mock = mock.Mock(return_value=(True, "ok"))
        with (
            mock.patch.object(srun_auth, "resolve_bind_ip", return_value=None),
            mock.patch.object(srun_auth, "init_getip", return_value=CLIENT_IP),
            mock.patch.object(
                srun_auth, "get_token", return_value=("token", CLIENT_IP)
            ),
            mock.patch.object(srun_auth, "login", side_effect=login_results),
            mock.patch.object(srun_auth, "default_query_online_status", query),
            mock.patch.object(srun_auth, "default_logout_once", self.logout_mock),
        ):
            return srun_auth.default_login_once(_login_app_ctx())

    def test_already_online_confirmed_by_status_is_success(self):
        ok, message = self._run_login(
            [(False, "E2620: You are already online.")],
            online_status=(True, "在线"),
        )
        self.assertTrue(ok)
        self.assertEqual("已在线", message)
        self.logout_mock.assert_not_called()

    def test_already_online_with_status_check_error_trusts_gateway(self):
        ok, message = self._run_login(
            [(False, "E2620: You are already online.")],
            online_error=OSError("transient network error"),
        )
        self.assertTrue(ok)
        self.assertEqual("已在线", message)

    def test_stale_session_kicked_and_relogin_succeeds(self):
        # 会话挂在旧 IP 上：本 IP 未在线 → unbind 登出踢旧会话 → 重登成功
        ok, message = self._run_login(
            [(False, "E2620: You are already online."), (True, "ok")],
            online_status=(False, "not_online_error"),
        )
        self.assertTrue(ok)
        self.assertIn("旧会话", message)
        self.logout_mock.assert_called_once()

    def test_stale_session_relogin_still_rejected_reports_failure(self):
        ok, message = self._run_login(
            [
                (False, "E2620: You are already online."),
                (False, "E2620: You are already online."),
            ],
            online_status=(False, "not_online_error"),
        )
        self.assertFalse(ok)
        self.assertIn("登录失败", message)
        self.logout_mock.assert_called_once()

    def test_other_login_errors_remain_failures(self):
        ok, message = self._run_login([(False, "E2531: User not found.")])
        self.assertFalse(ok)
        self.assertIn("登录失败", message)
        self.logout_mock.assert_not_called()


class StaleSessionRebuildEscalationTests(unittest.TestCase):
    """重试循环的 E2620 升级：unbind 踢不掉 AC 上的残留会话（硬重启不发
    deauth）时，重建校园连接（同手动登录预清理）后再试。"""

    def _run_cycle(self, attempt_results, rebuild_result=(True, "")):
        cfg = {
            "enabled": "1",
            "backoff_enabled": "1",
            "backoff_max_retries": "0",
            "username": FAKE_USERNAME,
        }
        rebuild = mock.Mock(return_value=rebuild_result)
        with (
            mock.patch.object(
                orchestrator.srun_auth, "run_once_safe", side_effect=attempt_results
            ) as attempts,
            mock.patch.object(orchestrator, "load_config", return_value=dict(cfg)),
            mock.patch.object(orchestrator, "backoff_enabled", return_value=True),
            mock.patch.object(orchestrator, "in_quiet_window", return_value=False),
            mock.patch.object(
                orchestrator, "_pending_runtime_action", return_value=""
            ),
            mock.patch.object(orchestrator, "campus_uses_wired", return_value=False),
            mock.patch.object(
                orchestrator, "clean_slate_for_manual_login", rebuild
            ),
            mock.patch.object(orchestrator, "calc_backoff_delay_seconds", return_value=0),
        ):
            ok, message = orchestrator.run_once_with_retry(dict(cfg))
        return ok, message, rebuild, attempts

    def test_already_online_failure_triggers_rebuild_then_success(self):
        ok, message, rebuild, attempts = self._run_cycle(
            [
                (False, "登录失败: E2620: You are already online."),
                (True, "登录成功"),
            ]
        )
        self.assertTrue(ok)
        self.assertIn("重建", message)
        rebuild.assert_called_once()
        self.assertEqual(2, attempts.call_count)

    def test_rebuild_only_attempted_once_per_cycle(self):
        # 重建后依旧 E2620 → 回到普通退避重试，不再反复重建；
        # 第三次尝试成功结束循环。
        ok, _message, rebuild, attempts = self._run_cycle(
            [
                (False, "登录失败: E2620: You are already online."),
                (False, "登录失败: E2620: You are already online."),
                (True, "登录成功"),
            ]
        )
        self.assertTrue(ok)
        rebuild.assert_called_once()
        self.assertEqual(3, attempts.call_count)

    def test_non_e2620_failures_do_not_trigger_rebuild(self):
        ok, _message, rebuild, attempts = self._run_cycle(
            [
                (False, "登录失败: E2531: User not found."),
                (True, "登录成功"),
            ]
        )
        self.assertTrue(ok)
        rebuild.assert_not_called()
        self.assertEqual(2, attempts.call_count)


class OnlineIdentitySuffixTests(unittest.TestCase):
    def setUp(self):
        self.profile = SchoolProfile()

    def test_suffixed_online_name_matches_bare_expected(self):
        online, name, message = self.profile.parse_online_status(
            {"error": "ok", "user_name": FAKE_USERNAME},
            FAKE_USERNAME,
        )
        self.assertTrue(online)
        self.assertEqual(FAKE_USERNAME, name)
        self.assertEqual("在线", message)

    def test_bare_online_name_still_matches(self):
        online, _name, message = self.profile.parse_online_status(
            {"error": "ok", "user_name": FAKE_USER_ID},
            FAKE_USERNAME_CMCC,
        )
        self.assertTrue(online)
        self.assertEqual("在线", message)

    def test_different_account_reports_online_with_label(self):
        online, name, message = self.profile.parse_online_status(
            {"error": "ok", "user_name": FAKE_OTHER_USERNAME},
            FAKE_USER_ID,
        )
        self.assertTrue(online)
        self.assertEqual(FAKE_OTHER_USERNAME, name)
        self.assertIn("在线账号", message)


if __name__ == "__main__":
    unittest.main()
