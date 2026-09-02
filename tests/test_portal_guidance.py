"""强制门户下的登录失败必须给得出下一步（issue #29）。

浙工大的报告里，手动登录先报 no_response_data_error，界面最终却显示
not_online_error。后者只是复述"你没登上"，把唯一能定位问题的错误码盖掉了，
用户也不知道要先去浏览器过网页认证。

这里锁三件事：
1. no_response_data_error 之后的在线复核如果说"没在线"，返回的仍是原始错误码；
2. 网关回的不是 JSONP（真被门户劫成 HTML）时，报 portal_intercept_error，
   而不是把一条 JSON 解析异常抛给上层；
3. 这些错误码都能本地化成可执行的中文，不再漏成英文原文。

账号与门户地址均为占位符。
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

from _portal_urls import CLIENT_IP, PORTAL_IPV4_ORIGIN, PORTAL_LOGIN_URL  # noqa: E402
import config  # noqa: E402
import srun_auth  # noqa: E402

FAKE_USERNAME = "student001"


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


class StubProfile:
    def build_login_params(self, cfg, ip, i_value, hmd5, chksum):
        return {"username": cfg.get("username", "")}

    def parse_login_response(self, data):
        error = str(data.get("error", "")).lower()
        return error == "ok", str(data.get("error") or "unknown response")


def _login_app_ctx():
    return {
        "cfg": {
            "username": FAKE_USERNAME,
            "password": "secret",
            "base_url": PORTAL_IPV4_ORIGIN,
        },
        "runtime": StubRuntime(),
    }


def _run_login(login_results, online_status):
    with (
        mock.patch.object(srun_auth, "resolve_bind_ip", return_value=None),
        mock.patch.object(srun_auth, "init_getip", return_value=CLIENT_IP),
        mock.patch.object(srun_auth, "get_token", return_value=("token", CLIENT_IP)),
        mock.patch.object(srun_auth, "login", side_effect=login_results),
        mock.patch.object(
            srun_auth, "default_query_online_status", return_value=online_status
        ),
        mock.patch.object(srun_auth, "default_logout_once", mock.Mock()),
    ):
        return srun_auth.default_login_once(_login_app_ctx())


class NoResponseDataGuidanceTests(unittest.TestCase):
    def test_offline_recheck_keeps_the_gateway_error_code(self):
        """#29: not_online_error must not overwrite no_response_data_error."""
        ok, message = _run_login(
            [(False, "no_response_data_error")],
            online_status=(False, "not_online_error"),
        )
        self.assertFalse(ok)
        self.assertEqual("no_response_data_error", message)
        self.assertNotIn("not_online", message)

    def test_recheck_that_finds_the_session_still_reports_online(self):
        ok, message = _run_login(
            [(False, "no_response_data_error")],
            online_status=(True, "在线"),
        )
        self.assertTrue(ok)
        self.assertEqual("已在线", message)


class PortalInterceptTests(unittest.TestCase):
    def _login_against_body(self, body):
        with mock.patch.object(srun_auth, "http_get", return_value=body):
            return srun_auth.login(
                StubProfile(),
                PORTAL_LOGIN_URL,
                {"username": FAKE_USERNAME},
                CLIENT_IP,
                "info",
                "hmd5",
                "chksum",
            )

    def test_html_login_page_is_reported_as_portal_intercept(self):
        ok, message = self._login_against_body(
            "<html><head><title>Web Authentication</title></head><body></body></html>"
        )
        self.assertFalse(ok)
        self.assertEqual("portal_intercept_error", message)

    def test_empty_body_is_reported_as_portal_intercept(self):
        ok, message = self._login_against_body("")
        self.assertFalse(ok)
        self.assertEqual("portal_intercept_error", message)

    def test_missing_body_does_not_raise(self):
        ok, message = self._login_against_body(None)
        self.assertFalse(ok)
        self.assertEqual("portal_intercept_error", message)

    def test_real_jsonp_still_parses(self):
        ok, message = self._login_against_body('cb({"error":"ok"})')
        self.assertTrue(ok)
        self.assertEqual("ok", message)


class ErrorLocalizationTests(unittest.TestCase):
    def test_portal_intercept_tells_the_user_to_use_a_browser(self):
        text = config.localize_error("portal_intercept_error")
        self.assertIn("浏览器", text)
        self.assertIn("网页认证", text)
        self.assertNotIn("portal_intercept_error", text)

    def test_not_online_error_is_no_longer_raw_english(self):
        text = config.localize_error("not_online_error")
        self.assertNotIn("not_online_error", text)
        self.assertIn("未在线", text)

    def test_no_response_data_no_longer_claims_the_user_may_be_online(self):
        text = config.localize_error("no_response_data_error")
        self.assertNotIn("可能已在线", text)
        self.assertIn("ac_id", text.lower())


if __name__ == "__main__":
    unittest.main()
