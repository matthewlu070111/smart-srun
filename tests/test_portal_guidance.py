"""强制门户下的登录失败必须给得出下一步（issue #29）。

浙工大的报告里，手动登录先报 no_response_data_error，界面最终却显示
not_online_error。后者只是复述"你没登上"，把唯一能定位问题的错误码盖掉了，
用户也不知道要先去浏览器过网页认证。

认证失败与连通性证据分开：保留认证原因，不将任意解析失败归因于门户。
低层保留错误码，公开登录结果提供中文，challenge 和 login 都覆盖网页响应。

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
    def test_offline_recheck_keeps_the_gateway_reason_and_localizes_it(self):
        """#29: not_online_error must not overwrite no_response_data_error."""
        ok, message = _run_login(
            [(False, "no_response_data_error")],
            online_status=(False, "not_online_error"),
        )
        self.assertFalse(ok)
        self.assertEqual("登录失败: " + config.localize_error("no_response_data_error"), message)
        self.assertNotIn("not_online", message)
        self.assertNotIn("no_response_data_error", message)

    def test_failed_recheck_does_not_replace_the_original_reason(self):
        with mock.patch.object(srun_auth, "default_query_online_status", side_effect=OSError("query failed")):
            # Keep the query exception visible to the implementation rather than
            # masking it behind _run_login's offline response fixture.
            with (
                mock.patch.object(srun_auth, "resolve_bind_ip", return_value=None),
                mock.patch.object(srun_auth, "init_getip", return_value=CLIENT_IP),
                mock.patch.object(srun_auth, "get_token", return_value=("token", CLIENT_IP)),
                mock.patch.object(srun_auth, "login", return_value=(False, "no_response_data_error")),
            ):
                ok, message = srun_auth.default_login_once(_login_app_ctx())
        self.assertFalse(ok)
        self.assertEqual("登录失败: " + config.localize_error("no_response_data_error"), message)
        self.assertNotIn("query failed", message)

    def test_recheck_that_finds_the_session_still_reports_online(self):
        ok, message = _run_login(
            [(False, "no_response_data_error")],
            online_status=(True, "在线"),
        )
        self.assertTrue(ok)
        self.assertEqual("已在线", message)


class AuthResponseTests(unittest.TestCase):
    def setUp(self):
        self.log_patch = mock.patch.object(srun_auth, "log")
        self.log_mock = self.log_patch.start()
        self.addCleanup(self.log_patch.stop)

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

    def test_html_login_page_is_reported_without_claiming_interception(self):
        ok, message = self._login_against_body(
            "<html><head><title>Web Authentication</title></head><body></body></html>"
        )
        self.assertFalse(ok)
        self.assertEqual("auth_html_response_error", message)

    def test_invalid_bodies_do_not_claim_portal_interception(self):
        for body in ("", None, "upstream unavailable", 'cb({"error":', "null", "[]", "true", "42"):
            with self.subTest(body=body):
                ok, message = self._login_against_body(body)
                self.assertFalse(ok)
                self.assertEqual("auth_response_parse_error", message)

    def test_real_jsonp_still_parses(self):
        ok, message = self._login_against_body('cb({"error":"ok"})')
        self.assertTrue(ok)
        self.assertEqual("ok", message)

    def test_plain_json_still_parses(self):
        self.assertEqual((True, "ok"), self._login_against_body('{"error":"ok"}'))

    def test_gateway_error_code_is_preserved_in_log(self):
        self.assertEqual((False, "no_response_data_error"), self._login_against_body('cb({"error":"no_response_data_error"})'))
        self.assertEqual("no_response_data_error", self.log_mock.call_args.kwargs["error_code"])

    def test_raw_response_is_not_copied_to_logs(self):
        secret = "response-secret-never-log"
        self._login_against_body('<html><body><input value="' + secret + '"></body></html>')
        self.assertNotIn(secret, str(self.log_mock.call_args_list))
        self.assertNotIn("body_head", str(self.log_mock.call_args_list))

    def test_challenge_html_reaches_public_result_as_chinese(self):
        app_ctx = _login_app_ctx()
        with (
            mock.patch.object(srun_auth, "resolve_bind_ip", return_value=None),
            mock.patch.object(srun_auth, "init_getip", return_value=CLIENT_IP),
            mock.patch.object(srun_auth, "http_get", return_value="<!DOCTYPE html><html><body>Sign in</body></html>"),
            mock.patch.object(srun_auth, "run_once", side_effect=lambda ctx: srun_auth.default_login_once(ctx)),
        ):
            ok, message = srun_auth.run_once_safe(app_ctx)
        self.assertFalse(ok)
        self.assertIn("认证接口返回了网页", message)
        self.assertNotIn("Expecting value", message)
        self.assertNotIn("没有到达", message)

    def test_challenge_invalid_body_has_stable_error_without_traceback(self):
        with mock.patch.object(srun_auth, "http_get", return_value="[]"):
            with self.assertRaisesRegex(ValueError, "auth_response_parse_error"):
                srun_auth.get_token(PORTAL_LOGIN_URL, FAKE_USERNAME, CLIENT_IP)

    def test_offline_status_keeps_raw_code_in_log_and_returns_chinese(self):
        profile = mock.Mock()
        profile.build_online_query_params.return_value = {}
        profile.parse_online_status.return_value = (False, "", "not_online_error")
        with mock.patch.object(srun_auth, "http_get", return_value='{"error":"not_online_error"}'):
            online, _, message = srun_auth.query_online_identity(profile, PORTAL_LOGIN_URL, FAKE_USERNAME)
        self.assertFalse(online)
        self.assertEqual(config.localize_error("not_online_error"), message)
        self.assertEqual("not_online_error", self.log_mock.call_args.kwargs["error_code"])

    def test_strict_binding_reaches_every_request_and_recheck(self):
        app_ctx = _login_app_ctx()
        app_ctx["cfg"]["_multi_wan_strict_bind"] = "1"
        app_ctx["cfg"]["wired_iface"] = "wan.test"
        runtime = app_ctx["runtime"]
        runtime.build_login_params = StubProfile().build_login_params
        runtime.parse_login_response = StubProfile().parse_login_response
        runtime.build_online_query_params = lambda: {}
        runtime.parse_online_status = lambda data, expected: (False, "", data["error"])
        binding = {
            "bind_ip": CLIENT_IP, "bind_device": "eth-test.2",
            "strict": True, "bind_iface": "wan.test",
        }
        responses = [
            "<html></html>",
            'cb({"challenge":"token","client_ip":"' + CLIENT_IP + '"})',
            'cb({"error":"no_response_data_error"})',
            'cb({"error":"not_online_error"})',
        ]
        with (
            mock.patch.object(srun_auth, "resolve_http_binding", return_value=binding) as resolver,
            mock.patch.object(srun_auth, "resolve_bind_ip", side_effect=AssertionError("must not use default route")),
            mock.patch.object(srun_auth, "http_get", side_effect=responses) as http,
        ):
            ok, message = srun_auth.default_login_once(app_ctx)
        self.assertFalse(ok)
        self.assertIn(config.localize_error("no_response_data_error"), message)
        resolver.assert_called_once()
        self.assertEqual(4, http.call_count)
        for call in http.call_args_list:
            for key, value in binding.items():
                self.assertEqual(value, call.kwargs[key])

    def test_strict_binding_failure_never_starts_authentication(self):
        app_ctx = _login_app_ctx()
        app_ctx["cfg"]["_multi_wan_strict_bind"] = "1"
        with (
            mock.patch.object(srun_auth, "resolve_http_binding", side_effect=RuntimeError("所选接口没有 IPv4")),
            mock.patch.object(srun_auth, "http_get") as http,
        ):
            with self.assertRaisesRegex(RuntimeError, "IPv4"):
                srun_auth.default_login_once(app_ctx)
        http.assert_not_called()


class ErrorLocalizationTests(unittest.TestCase):
    def test_portal_intercept_tells_the_user_to_use_a_browser(self):
        text = config.localize_error("portal_intercept_error")
        self.assertIn("浏览器", text)
        self.assertNotIn("portal_intercept_error", text)
        self.assertNotIn("没有到达", text)

    def test_not_online_error_is_no_longer_raw_english(self):
        text = config.localize_error("not_online_error")
        self.assertNotIn("not_online_error", text)
        self.assertIn("未在线", text)

    def test_no_response_data_no_longer_claims_the_user_may_be_online(self):
        text = config.localize_error("no_response_data_error")
        self.assertNotIn("可能已在线", text)
        self.assertIn("ac_id", text.lower())
        self.assertNotIn("通常", text)


if __name__ == "__main__":
    unittest.main()
