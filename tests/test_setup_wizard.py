"""Wizard probes must describe and use the user's selected campus uplink."""

import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "root/usr/lib/smart_srun"))

import config  # noqa: E402
import network  # noqa: E402
import portal_detect as detect  # noqa: E402
import srun_auth  # noqa: E402
import wireless  # noqa: E402

BINDING = {"bind_ip": "192.0.2.12", "bind_device": "eth1.2", "strict": True, "bind_iface": "campus"}
PORTAL = "http://portal.example.test"


class WizardProbeTests(unittest.TestCase):
    def test_selected_wired_interface_is_strict_even_without_multiwan_enabled(self):
        with mock.patch.object(network, "resolve_wired_binding", return_value=("192.0.2.12", "eth1.2")) as resolve:
            binding, iface = detect._detect_binding("wired", "campus")
        self.assertEqual(binding, BINDING)
        self.assertEqual(iface, "campus")
        self.assertEqual(resolve.call_args.args[0]["wired_iface"], "campus")
        self.assertEqual(resolve.call_args.args[0]["_multi_wan_strict_bind"], "1")

    def test_missing_selected_interface_does_not_probe_default_route(self):
        with mock.patch.object(network, "resolve_wired_binding", side_effect=RuntimeError("接口未获取 IPv4")), \
                mock.patch.object(detect, "probe_captive_portal") as probe:
            result = detect.detect_environment(access_mode="wired", iface="campus")
        self.assertTrue(result["binding_error"])
        self.assertEqual(result["state"], "down")
        probe.assert_not_called()

    def test_wireless_selection_must_match_ssid_and_network(self):
        data = {"sta1": {"mode": "sta", "network": "wwan", "ssid": "Campus"},
                "sta2": {"mode": "sta", "network": "wwan2", "ssid": "Phone"}}
        with mock.patch.object(wireless, "parse_wireless_iface_data", return_value=data), \
                mock.patch.object(detect, "resolve_http_binding", return_value=BINDING) as resolve:
            self.assertEqual(detect._detect_binding("wifi", "", "Campus")[1], "wwan")
            self.assertEqual(resolve.call_args.args[1]["_probe_iface"], "wwan")
            with self.assertRaisesRegex(RuntimeError, "无线客户端"):
                detect._detect_binding("wifi", "wwan2", "Campus")
            with self.assertRaisesRegex(RuntimeError, "无线客户端"):
                detect._detect_binding("wifi")

    def test_all_redirect_fetches_keep_the_same_binding(self):
        responses = [(302, {"Location": "/login"}, ""), (200, {}, '<input name="ac_id" value="12">')]
        with mock.patch.object(detect, "_fetch_once", side_effect=responses) as fetch:
            acid, _, url = detect._probe_url(PORTAL, **BINDING)
        self.assertEqual(acid, "12")
        self.assertEqual(url, PORTAL + "/login")
        self.assertEqual([c.kwargs for c in fetch.call_args_list], [BINDING, BINDING])

    def test_bound_fetch_keeps_response_headers_for_portal_redirects(self):
        with mock.patch.object(detect, "_http_get_via_stdlib", return_value=("", 302, {"Location": "/login"})) as fetch:
            result = detect._fetch_once(PORTAL, 5, **BINDING)
        self.assertEqual(result, (302, {"Location": "/login"}, ""))
        fetch.assert_called_once_with(PORTAL, 5, return_headers=True, **BINDING)

    def test_bound_fetch_never_falls_back_to_unbound_system_client(self):
        with mock.patch.object(detect, "HAVE_URLLIB", False), mock.patch.object(detect, "http_get") as fallback:
            with self.assertRaises(RuntimeError):
                detect._fetch_once(PORTAL, 5, **BINDING)
        fallback.assert_not_called()

    def test_online_uplink_still_discovers_portal_from_known_candidates(self):
        probe = {"state": "online", "status_code": 204, "checked_url": "http://check.test/", "message": "online"}
        with mock.patch.object(detect, "_detect_binding", return_value=(BINDING, "campus")), \
                mock.patch.object(detect, "probe_captive_portal", return_value=probe) as captive, \
                mock.patch.object(detect, "_environment_candidates", return_value=[(PORTAL, "学校预设")]), \
                mock.patch.object(detect, "_probe_url", return_value=("12", "html", PORTAL + "/login")) as fetch:
            result = detect.detect_environment(access_mode="wired", iface="campus", school="example")
        captive.assert_called_once_with(timeout=5, **BINDING)
        fetch.assert_called_once_with(PORTAL, timeout=5, **BINDING)
        self.assertEqual(result["state"], "online")
        self.assertTrue(result["ok"])
        self.assertEqual(result["base_url"], PORTAL)
        self.assertEqual(result["acid"], "12")

    def test_candidates_never_borrow_other_wired_account_or_default_gateway(self):
        cfg = {"campus_accounts": [
            {"access_mode": "wired", "wired_iface": "other", "base_url": "http://wrong.test"},
            {"access_mode": "wired", "wired_iface": "campus", "base_url": PORTAL},
        ]}
        with mock.patch.object(config, "load_config", return_value=cfg), \
                mock.patch.object(detect, "run_cmd", return_value=(True, '{"route":[{"target":"0.0.0.0","nexthop":"192.0.2.1"}]}')) as run:
            candidates = detect._environment_candidates("", "", "wired", "campus", "")
        self.assertEqual([u for u, _ in candidates], [PORTAL, "http://192.0.2.1"])
        self.assertEqual(run.call_args.args[0], ["ubus", "call", "network.interface.campus", "status"])

    def test_manual_full_path_is_fetched_before_origin_normalization(self):
        with mock.patch.object(detect, "_detect_binding", return_value=(BINDING, "campus")), \
                mock.patch.object(detect, "_probe_url", return_value=("12", "html", PORTAL + "/custom")) as fetch:
            result = detect.detect_acid(PORTAL + "/custom", access_mode="wired", iface="campus")
        fetch.assert_called_once_with(PORTAL + "/custom", timeout=5, **BINDING)
        self.assertEqual(result["base_url"], PORTAL)

    def test_schemeless_address_retains_path_during_probe(self):
        with mock.patch.object(detect, "_probe_url", return_value=("12", "html", PORTAL + "/custom")) as fetch:
            result = detect.detect_acid("portal.example.test/custom")
        fetch.assert_called_once_with(PORTAL + "/custom", timeout=5)
        self.assertEqual(result["base_url"], PORTAL)

    def test_cli_passes_selected_connection_to_environment_probe(self):
        import cli

        parser, _sub = cli._build_parser()
        args = parser.parse_args(['detect', 'env', '--access-mode', 'wired', '--iface', 'campus', '--school', 'example'])
        self.assertEqual((args.access_mode, args.iface, args.school), ('wired', 'campus', 'example'))

    def test_operator_uses_selected_interface_and_new_account_login_shape(self):
        cfg = {"ac_id": "99", "n": "999", "_legacy_login_shape": {"n": "200", "type": "1", "enc": "srun_bx1"}}
        with mock.patch.object(config, "load_config", return_value=cfg), \
                mock.patch.object(detect, "_detect_binding", return_value=(BINDING, "campus")), \
                mock.patch.object(srun_auth, "probe_online_identity", return_value=(False, "", "offline")), \
                mock.patch.object(detect.time, "sleep"), \
                mock.patch.object(srun_auth, "probe_login_once", return_value=(True, "成功")) as login:
            result = detect.detect_operator(PORTAL, "12", "student", "test-password", [""],
                                            access_mode="wired", iface="campus", login_shape={"n": "201"})
        self.assertTrue(result["confirmed"])
        self.assertEqual(result["suffix"], "")
        used = login.call_args.args[0]
        self.assertEqual(used["_probe_iface"], "campus")
        self.assertEqual(used["_multi_wan_strict_bind"], "1")
        self.assertEqual(used["n"], "201")
        self.assertEqual(used["ac_id"], "12")

    def test_operator_missing_binding_never_sends_credentials(self):
        with mock.patch.object(config, "load_config", return_value={}), \
                mock.patch.object(detect, "_detect_binding", side_effect=RuntimeError("接口未就绪")), \
                mock.patch.object(srun_auth, "probe_login_once") as login:
            result = detect.detect_operator(PORTAL, "12", "student", "test", [""], access_mode="wired", iface="campus")
        self.assertFalse(result["confirmed"])
        login.assert_not_called()

    def test_server_caps_attempts_even_when_client_requests_more(self):
        with mock.patch.object(config, "load_config", return_value={}), \
                mock.patch.object(srun_auth, "probe_online_identity", return_value=(False, "", "offline")), \
                mock.patch.object(detect.time, "sleep"), \
                mock.patch.object(srun_auth, "probe_login_once", return_value=(False, "E2531")) as login:
            detect.detect_operator(PORTAL, "12", "student", "test", list("abcdefghij"), max_attempts=100)
        self.assertEqual(login.call_count, 5)


class WizardSourceContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = (ROOT / "root/www/luci-static/resources/smart_srun.js").read_text(encoding="utf-8")
        cls.wizard = source[source.index("  var WIZ_STEPS"):source.index("  function initAll()")]
        cls.controller = (ROOT / "root/usr/lib/lua/luci/controller/smart_srun.lua").read_text(encoding="utf-8")

    def test_notice_and_form_classes_do_not_inherit_theme_floating_notifications(self):
        self.assertNotIn("'alert-message", self.wizard)
        self.assertNotIn("'cbi-value", self.wizard)
        self.assertIn("'smart-wizard-note smart-wizard-note-' + kind", self.wizard)
        self.assertIn("flex-wrap:nowrap!important", self.wizard)
        self.assertIn(".smart-wizard-content{min-height:0;overflow:auto", self.wizard)

    def test_initial_page_waits_for_explicit_connection_selection(self):
        self.assertIn("accessMode: ''", self.wizard)
        self.assertIn("['wired', '有线", self.wizard)
        self.assertIn("['wifi', '无线", self.wizard)
        self.assertNotIn("wizSyncFinal", self.wizard)

    def test_empty_operator_suffix_survives_transport(self):
        self.assertIn("JSON.stringify(suffixes)", self.wizard)
        self.assertIn('jsonc.parse(fv("candidates"))', self.controller)
        self.assertNotIn('gmatch("[^,]+")', self.controller)
        self.assertIn("operator_suffix: wiz.selectedSuffix", self.wizard)

    def test_operator_discovery_and_credentials_are_separate_steps(self):
        self.assertIn("'认证后缀', '账号信息', '确认保存'", self.wizard)
        operator = self.wizard.split("function wizStepOperator(body)", 1)[1].split("function wizSelectedOperator", 1)[0]
        self.assertNotIn("wiz-user", operator)
        self.assertNotIn("wiz-pass", operator)
        self.assertNotIn("wizRunOperatorProbe", operator)
        self.assertNotIn("验证结果", operator)
        self.assertIn("wizGo(3)", operator)
        account = self.wizard.split("function wizStepAccount(body)", 1)[1].split("function wizRunOperatorProbe", 1)[0]
        self.assertIn("wiz-user", account)
        self.assertIn("wiz-pass", account)
        self.assertIn("wizGo(4)", account)
        self.assertIn("wizStepOperator, wizStepAccount, wizStepDone", self.wizard)

    def test_read_only_identity_does_not_send_typed_password(self):
        probe = self.wizard.split("function wizRunOperatorProbe(readOnly)", 1)[1].split("function wizSummary", 1)[0]
        self.assertIn("payload.password = readOnly ? '' : wiz.password", probe)
        self.assertIn("if (!readOnly && !wiz.password)", probe)

    def test_closed_or_replaced_modal_cannot_be_reopened_by_old_requests(self):
        self.assertIn("wiz !== owner || owner.xhr !== xhr", self.wizard)
        self.assertIn("document.body.contains(owner.root)", self.wizard)
        self.assertIn("old.xhr.abort()", self.wizard)
        self.assertIn("xhr.ontimeout", self.wizard)
        self.assertIn("data.ok !== true", self.wizard)

    def test_credentials_use_exclusive_owner_only_file_and_post(self):
        action = self.controller.split("function action_detect_operator()", 1)[1].split("\nend", 1)[0]
        self.assertIn('http.getenv("REQUEST_METHOD") ~= "POST"', action)
        self.assertIn('nixio.open_flags("wronly", "creat", "excl"), "600"', action)
        self.assertIn('nixio.getpid()', action)
        self.assertIn('fs.unlink(path)', action)


if __name__ == "__main__":
    unittest.main()
