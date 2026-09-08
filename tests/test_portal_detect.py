import contextlib
import os
import sys
import unittest
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import network  # noqa: E402  (依赖上方 sys.path 注入，与其余测试文件一致)
import portal_detect  # noqa: E402
from _portal_urls import (  # noqa: E402
    PORTAL_ACID4_THEME_URL,
    PORTAL_ACID9_PAGE_PATH,
    PORTAL_ACID9_PAGE_URL,
    PORTAL_HTTPS_ORIGIN,
    PORTAL_ORIGIN,
)


class PortalDetectTests(unittest.TestCase):
    def test_detect_acid_reads_full_portal_url_before_normalizing_base_url(self):
        payload = portal_detect.detect_acid(PORTAL_ACID4_THEME_URL)

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "4")
        self.assertEqual(payload["base_url"], PORTAL_ORIGIN)
        self.assertEqual(payload["source"], "input_url")

    def test_detect_acid_follows_redirect_location(self):
        def fake_fetch(url, timeout):
            self.assertEqual(url, PORTAL_ORIGIN)
            return 302, {"Location": PORTAL_ACID9_PAGE_PATH}, ""

        with mock.patch.object(portal_detect, "_fetch_once", side_effect=fake_fetch):
            payload = portal_detect.detect_acid(PORTAL_ORIGIN)

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "9")
        self.assertEqual(payload["source"], "redirect_url")
        self.assertEqual(payload["detected_url"], PORTAL_ACID9_PAGE_URL)

    def test_detect_acid_reads_hidden_input_from_html(self):
        html = '<input type="hidden" name="ac_id" value="17">'
        with mock.patch.object(portal_detect, "_fetch_once", return_value=(200, {}, html)):
            payload = portal_detect.detect_acid(PORTAL_ORIGIN)

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "17")
        self.assertEqual(payload["source"], "html")

    def test_https_error_message_mentions_python_openssl(self):
        message = network.humanize_http_errors(
            PORTAL_HTTPS_ORIGIN, [RuntimeError("ssl module unavailable")]
        )

        self.assertIn("HTTPS", message)
        self.assertIn("python3-openssl", message)


class PortalDetectIdnaFallbackTests(unittest.TestCase):
    """python3-light ships urllib but usually omits the idna codec.

    Hostname encoding then raises LookupError before any network IO, which
    used to surface as 'unknown encoding: idna' instead of probing AC_ID.
    """

    def test_stdlib_probe_reports_unusable_when_urllib_missing(self):
        with mock.patch.object(portal_detect, "HAVE_URLLIB", False):
            self.assertFalse(portal_detect._stdlib_http_is_usable())

        with mock.patch.object(portal_detect, "urllib_request", None):
            self.assertFalse(portal_detect._stdlib_http_is_usable())

    def test_stdlib_probe_reports_usable_when_idna_codec_present(self):
        self.assertTrue("example.com".encode("idna"))
        self.assertTrue(portal_detect._stdlib_http_is_usable())

    def test_fetch_once_falls_back_to_system_client_without_idna(self):
        with mock.patch.object(
            portal_detect, "_stdlib_http_is_usable", return_value=False
        ), mock.patch.object(
            portal_detect, "http_get", return_value='<input name="ac_id" value="12">'
        ) as fake_get:
            status, headers, body = portal_detect._fetch_once(PORTAL_ORIGIN, 5)

        fake_get.assert_called_once_with(PORTAL_ORIGIN, timeout=5)
        self.assertEqual(status, 200)
        self.assertEqual(headers, {})
        self.assertIn("ac_id", body)

    def test_detect_acid_succeeds_via_system_client_without_idna(self):
        with mock.patch.object(
            portal_detect, "_stdlib_http_is_usable", return_value=False
        ), mock.patch.object(
            portal_detect, "http_get", return_value='<input name="ac_id" value="12">'
        ):
            payload = portal_detect.detect_acid(PORTAL_ORIGIN)

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "12")
        self.assertEqual(payload["source"], "html")
        self.assertNotIn("idna", payload["message"])

    def test_late_lookup_error_falls_back_instead_of_raising(self):
        class _Opener:
            def open(self, *args, **kwargs):
                raise LookupError("unknown encoding: idna")

        with mock.patch.object(
            portal_detect, "_stdlib_http_is_usable", return_value=True
        ), mock.patch.object(
            portal_detect.urllib_request, "build_opener", return_value=_Opener()
        ), mock.patch.object(
            portal_detect, "http_get", return_value='<input name="ac_id" value="7">'
        ) as fake_get:
            status, headers, body = portal_detect._fetch_once(PORTAL_ORIGIN, 5)

        fake_get.assert_called_once_with(PORTAL_ORIGIN, timeout=5)
        self.assertEqual(status, 200)
        self.assertEqual(headers, {})
        self.assertIn("ac_id", body)


class DetectEnvironmentTests(unittest.TestCase):
    """出口自检：用户填不出认证地址，就反过来问出口有没有被门户拦下来。"""

    def _captive(self, **overrides):
        payload = {"state": "portal", "checked_url": "http://probe.example.test/generate_204",
                   "status_code": 302, "location": PORTAL_ACID9_PAGE_URL,
                   "message": "已捕获认证页跳转地址"}
        payload.update(overrides)
        return mock.patch.object(portal_detect, "probe_captive_portal", return_value=payload)

    def test_hijack_yields_both_base_url_and_acid_without_any_user_input(self):
        with self._captive():
            result = portal_detect.detect_environment()
        self.assertTrue(result["ok"])
        self.assertEqual(result["state"], "portal")
        self.assertEqual(result["base_url"], PORTAL_ORIGIN)
        self.assertEqual(result["acid"], "9")
        self.assertEqual(result["portal_url"], PORTAL_ACID9_PAGE_URL)

    def test_relative_location_is_resolved_against_the_probed_url(self):
        with self._captive(location=PORTAL_ACID9_PAGE_PATH,
                           checked_url=PORTAL_ORIGIN + "/generate_204"):
            result = portal_detect.detect_environment()
        self.assertEqual(result["base_url"], PORTAL_ORIGIN)
        self.assertEqual(result["acid"], "9")

    def test_intercepted_without_location_still_scrapes_the_landing_page(self):
        # 200 + HTML 跳转的门户：裸 socket 读不到 Location，但正文里有。
        with self._captive(location="", status_code=200), mock.patch.object(
            portal_detect, "_probe_url",
            return_value=("9", "html", PORTAL_ACID9_PAGE_URL),
        ) as probe:
            result = portal_detect.detect_environment()
        probe.assert_called_once()
        self.assertEqual(result["base_url"], PORTAL_ORIGIN)
        self.assertEqual(result["acid"], "9")

    def test_captured_address_survives_a_failed_acid_scrape(self):
        with self._captive(), mock.patch.object(
            portal_detect, "_probe_url", side_effect=RuntimeError("认证页无响应")
        ):
            result = portal_detect.detect_environment()
        self.assertTrue(result["ok"])
        self.assertEqual(result["base_url"], PORTAL_ORIGIN)
        self.assertEqual(result["acid"], "")
        self.assertIn("认证页无响应", result["message"])

    def test_online_and_down_report_no_address_to_offer(self):
        for state in ("online", "down"):
            with self.subTest(state=state):
                with self._captive(state=state, location="", status_code=204):
                    result = portal_detect.detect_environment()
                self.assertFalse(result["ok"])
                self.assertEqual(result["state"], state)
                self.assertEqual(result["base_url"], "")

    def test_empty_base_url_probes_the_environment_instead_of_refusing(self):
        # 旧行为是直接回"请先填写认证地址"，把这个功能要解决的问题当成前置条件。
        with self._captive():
            payload = portal_detect.detect_acid("")
        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "9")
        self.assertEqual(payload["base_url"], PORTAL_ORIGIN)
        self.assertTrue(payload["source"].startswith("environment_"))

    def test_empty_base_url_while_online_explains_why_nothing_was_found(self):
        with self._captive(state="online", location="", status_code=204):
            payload = portal_detect.detect_acid("")
        self.assertFalse(payload["ok"])
        self.assertEqual(payload["base_url"], "")
        self.assertIn("填写登录页地址", payload["message"])
        self.assertNotIn("没有认证页", payload["message"])

    def test_reality_url_alone_derives_the_gateway_from_the_redirect_chain(self):
        with mock.patch.object(
            portal_detect, "_probe_url",
            return_value=("9", "redirect_url", PORTAL_ACID9_PAGE_URL),
        ):
            payload = portal_detect.detect_acid("", reality_url=PORTAL_ORIGIN + "/x")
        self.assertTrue(payload["ok"])
        self.assertEqual(payload["base_url"], PORTAL_ORIGIN)


class OperatorProbeTests(unittest.TestCase):
    """运营商后缀探测：会真的向网关发起登录，所以限次与分类必须钉死。"""

    @contextlib.contextmanager
    def _auth(self, login):
        import config as config_mod
        import srun_auth

        with mock.patch.object(srun_auth, "probe_login_once", side_effect=login), \
                mock.patch.object(config_mod, "load_config", return_value={"ac_id": "1"}), \
                mock.patch.object(srun_auth, "probe_online_identity", return_value=(False, "", "offline")), \
                mock.patch.object(portal_detect.time, "sleep"):
            yield

    def test_operator_probe_stops_at_the_first_suffix_the_gateway_accepts(self):
        calls = []

        def fake_login(cfg):
            calls.append(cfg["username"])
            if cfg["operator_suffix"] == "cmcc":
                return True, "登录成功"
            return False, "登录失败: E2531: User not found."

        with self._auth(fake_login):
            result = portal_detect.detect_operator(
                PORTAL_ORIGIN, "4", "2021001", "secret", ["", "cmcc", "ctcc"]
            )

        self.assertTrue(result["ok"])
        self.assertTrue(result["confirmed"])
        self.assertEqual(result["suffix"], "cmcc")
        self.assertEqual(calls, ["2021001", "2021001@cmcc"])
        self.assertEqual([a["outcome"] for a in result["attempts"]], ["miss", "hit"])

    def test_wrong_password_stops_without_claiming_the_suffix_is_correct(self):
        # A generic credential error cannot distinguish an unknown account.
        def fake_login(cfg):
            if cfg["operator_suffix"] == "stu":
                return False, "登录失败: 用户名或密码错误。"
            return False, "登录失败: E2531: User not found."

        with self._auth(fake_login):
            result = portal_detect.detect_operator(
                PORTAL_ORIGIN, "4", "2021001", "wrong", ["", "stu", "cmcc"]
            )

        self.assertFalse(result["ok"])
        self.assertFalse(result["confirmed"])
        self.assertEqual(result["suffix"], "")
        self.assertEqual(len(result["attempts"]), 2)

    def test_already_online_does_not_confirm_the_attempted_suffix(self):
        with self._auth(lambda cfg: (False, "登录失败: E2620: You are already online.")):
            result = portal_detect.detect_operator(
                PORTAL_ORIGIN, "4", "2021001", "secret", ["cmcc"]
            )
        self.assertFalse(result["ok"])
        self.assertFalse(result["confirmed"])
        self.assertEqual(result["attempts"][0]["outcome"], "online")

    def test_probe_never_builds_a_username_from_the_unverified_sentinel(self):
        seen = []
        with self._auth(lambda cfg: (seen.append(cfg["username"]), (False, "E2531"))[1]):
            portal_detect.detect_operator(
                PORTAL_ORIGIN, "4", "2021001", "secret", ["??", ""]
            )
        # Unverified values are skipped; the explicitly supplied empty suffix remains.
        self.assertEqual(seen, ["2021001"])

    def test_missing_or_unverified_candidates_never_guess_plain_account(self):
        for candidates in ([], ["??", None]):
            with self.subTest(candidates=candidates), self._auth(lambda cfg: self.fail("未选择后缀不得登录")):
                result = portal_detect.detect_operator(PORTAL_ORIGIN, "4", "student", "secret", candidates)
            self.assertFalse(result["confirmed"])
            self.assertEqual(result["attempts"], [])
            self.assertIn("未提供认证后缀", result["message"])

    def test_unknown_school_can_identify_nonstandard_online_suffix_without_candidates(self):
        import srun_auth
        with self._auth(lambda cfg: self.fail("不应提交登录")), \
                mock.patch.object(srun_auth, "probe_online_identity", return_value=(True, "student@Students.Campus.test", "在线")):
            result = portal_detect.detect_operator(PORTAL_ORIGIN, "1", "student", "", [])
        self.assertTrue(result["confirmed"])
        self.assertEqual(result["suffix"], "Students.Campus.test")
        self.assertFalse(result["password_verified"])

    def test_attempt_budget_caps_how_many_logins_are_ever_tried(self):
        seen = []
        with self._auth(lambda cfg: (seen.append(cfg["username"]), (False, "E2531"))[1]):
            result = portal_detect.detect_operator(
                PORTAL_ORIGIN, "4", "2021001", "secret",
                ["a", "b", "c", "d", "e", "f"], max_attempts=3,
            )
        self.assertEqual(len(seen), 3)
        self.assertFalse(result["confirmed"])

    def test_unclassifiable_gateway_reply_halts_instead_of_burning_attempts(self):
        seen = []
        with self._auth(lambda cfg: (seen.append(cfg["username"]), (False, "网关返回未知响应。"))[1]):
            result = portal_detect.detect_operator(
                PORTAL_ORIGIN, "4", "2021001", "secret", ["a", "b", "c"]
            )
        self.assertEqual(len(seen), 1)
        self.assertFalse(result["confirmed"])
        self.assertEqual(result["attempts"][0]["outcome"], "other")

    def test_missing_credentials_or_address_never_touches_the_gateway(self):
        cases = (
            (PORTAL_ORIGIN, "", "secret"),
            (PORTAL_ORIGIN, "2021001", ""),
            ("", "2021001", "secret"),
        )
        for base, user, password in cases:
            with self.subTest(base=base, user=user):
                with self._auth(lambda cfg: self.fail("不该发起登录")):
                    result = portal_detect.detect_operator(base, "4", user, password, ["cmcc"])
                self.assertFalse(result["ok"])
                self.assertEqual(result["attempts"], [])

    def test_full_online_suffix_is_confirmed_without_logging_in(self):
        import srun_auth
        with self._auth(lambda cfg: self.fail("在线账号不应再登录")), \
                mock.patch.object(srun_auth, "probe_online_identity", return_value=(True, "student@cmcc", "在线")):
            result = portal_detect.detect_operator(PORTAL_ORIGIN, "1", "student", "", ["ctcc"])
        self.assertTrue(result["confirmed"])
        self.assertEqual(result["suffix"], "cmcc")
        self.assertFalse(result["password_verified"])
        self.assertEqual(result["attempts"], [])

    def test_bare_online_account_cannot_prove_empty_suffix(self):
        import srun_auth
        for reported in ("student", "different@cmcc", "student@@cmcc"):
            with self.subTest(reported=reported), self._auth(lambda cfg: self.fail("不应干扰在线会话")), \
                    mock.patch.object(srun_auth, "probe_online_identity", return_value=(True, reported, "在线")):
                result = portal_detect.detect_operator(PORTAL_ORIGIN, "1", "student", "secret", [""])
            self.assertFalse(result["confirmed"])
            self.assertEqual(result["attempts"], [])

    def test_rate_limit_stops_with_specific_reason(self):
        with self._auth(lambda cfg: (False, "E2532: authentication too frequent")):
            result = portal_detect.detect_operator(PORTAL_ORIGIN, "1", "student", "secret", ["a", "b"])
        self.assertEqual(len(result["attempts"]), 1)
        self.assertEqual(result["attempts"][0]["outcome"], "limited")
        self.assertIn("频率", result["message"])

    def test_online_reply_overrides_daemon_style_success_boolean(self):
        self.assertEqual(portal_detect.classify_login_attempt(True, "E2620 already online"), "online")
        self.assertEqual(portal_detect.classify_login_attempt(True, "已在线"), "online")

    def test_online_race_cannot_confirm_plain_suffix_from_bare_name(self):
        import srun_auth
        with self._auth(lambda cfg: (False, "E2620 already online")), \
                mock.patch.object(srun_auth, "probe_online_identity", side_effect=[(False, "", "离线"), (True, "student", "在线")]):
            result = portal_detect.detect_operator(PORTAL_ORIGIN, "1", "student", "secret", [""])
        self.assertFalse(result["confirmed"])
        self.assertEqual(result["attempts"][0]["outcome"], "online")

    def test_probe_login_never_runs_recovery_or_logout(self):
        import srun_auth
        runtime = mock.Mock()
        runtime.build_urls.return_value = {key: PORTAL_ORIGIN for key in ("init_url", "get_challenge_api", "srun_portal_api")}
        runtime.do_complex_work.return_value = ("info", "md5", "checksum")
        cfg = {"username": "student@cmcc", "base_url": PORTAL_ORIGIN}
        with mock.patch.object(srun_auth, "ensure_app_context", return_value={"cfg": cfg, "runtime": runtime}), \
                mock.patch.object(srun_auth, "_resolve_auth_binding", return_value={}), \
                mock.patch.object(srun_auth, "init_getip", return_value="192.0.2.1"), \
                mock.patch.object(srun_auth, "get_token", return_value=("token", "192.0.2.1")), \
                mock.patch.object(srun_auth, "login", return_value=(False, "E2620 already online")) as login, \
                mock.patch.object(srun_auth, "default_logout_once") as logout:
            self.assertEqual(srun_auth.probe_login_once(cfg), (False, "E2620 already online"))
        self.assertEqual(login.call_count, 1)
        logout.assert_not_called()


if __name__ == "__main__":
    unittest.main()
