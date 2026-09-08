import sys
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "root/usr/lib/smart_srun"))

import portal_detect as portal
import srun_auth
import network
from schools._base import SchoolProfile


PAGE = '''<select id="domain">
<option value="" disabled>请选择运营商</option>
<option value="@cmcc">移动校园网</option>
<option value="@ctcc">电信校园网</option>
<option value="@cucc" selected>联通校园网</option>
<option value="@Campus.Research.test">研究网络</option></select>'''


def test_portal_options_are_evidence_and_selected_is_not_a_personal_result():
    parser = portal._OperatorOptions()
    parser.feed(PAGE)
    assert parser.operators == [
        {"suffix": "cmcc", "label": "移动校园网"}, {"suffix": "ctcc", "label": "电信校园网"},
        {"suffix": "cucc", "label": "联通校园网"}, {"suffix": "Campus.Research.test", "label": "研究网络"},
    ]
    assert not any("confirmed" in option or "selected" in option for option in parser.operators)


def test_nonstandard_suffix_empty_suffix_and_escaped_label_survive():
    parser = portal._OperatorOptions()
    parser.feed('<select name="domain"><option value="1 - @stu.example.edu">学生 &amp; 教职工</option>'
                '<option value="">校园网</option><option value="@stu.example.edu">重复</option></select>')
    assert parser.operators == [{"suffix": "stu.example.edu", "label": "学生 & 教职工"}, {"suffix": "", "label": "校园网"}]


def test_does_not_infer_suffix_from_email_script_numeric_service_or_placeholder():
    parser = portal._OperatorOptions()
    parser.feed('Contact admin@example.edu<script>var x="@example";</script>'
                '<select id="service"><option value="1">中国移动</option></select>'
                '<select id="domain"><option value="??">移动</option>'
                '<option value="0">请选择</option><option value="@bad@realm">坏值</option></select>')
    assert parser.operators == []


def test_follows_meta_redirect_even_after_acid_is_found_and_retains_binding():
    binding = {"bind_ip": "192.0.2.3", "bind_device": "eth1", "strict": True}
    with mock.patch.object(portal, "_detect_binding", return_value=(binding, "campus")), \
            mock.patch.object(portal, "_fetch_once", side_effect=[
                (200, {}, '<meta http-equiv="refresh" content="0;url=/srun_portal_pc?ac_id=1&amp;theme=pro">'),
                (200, {}, PAGE),
            ]) as fetch:
        result = portal.discover_operators("http://portal.example.test", "1", access_mode="wired", iface="campus")
    assert result["ok"]
    assert len(result["operators"]) == 4
    assert fetch.call_args_list[1].args[0] == "http://portal.example.test/srun_portal_pc?ac_id=1&theme=pro"
    assert all(call.kwargs == binding for call in fetch.call_args_list)


def test_cross_host_and_action_redirects_are_not_requested():
    for target in ("http://other.example.test/", "/cgi-bin/srun_portal?action=logout", "/logout"):
        with mock.patch.object(portal, "_detect_binding", return_value=({}, "")), \
                mock.patch.object(portal, "_fetch_once", return_value=(302, {"Location": target}, "")) as fetch:
            result = portal.discover_operators("http://portal.example.test")
        assert not result["ok"]
        assert fetch.call_count == 1


def test_binding_failure_never_falls_back_to_default_route():
    with mock.patch.object(portal, "_detect_binding", side_effect=RuntimeError("接口未就绪")), \
            mock.patch.object(portal, "_fetch_once") as fetch:
        result = portal.discover_operators("http://portal.example.test", access_mode="wired", iface="campus")
    assert not result["ok"]
    assert "接口未就绪" in result["message"]
    fetch.assert_not_called()


def test_online_identity_includes_the_separate_domain_without_doubling_it():
    cases = [("student", "cmcc", "student@cmcc"), ("student@cmcc", "cmcc", "student@cmcc"),
             ("student", "@cmcc", "student@cmcc"), ("student", "", "student"),
             ("student", "invalid@realm", "student")]
    for name, domain, expected in cases:
        with mock.patch.object(srun_auth, "http_get", return_value="fixture"), \
                mock.patch.object(srun_auth, "_parse_auth_response", return_value={
                    "error": "ok", "user_name": name, "domain": domain}):
            online, reported, _ = srun_auth.query_online_identity(SchoolProfile(), "http://portal.example.test/info", "student")
        assert online and reported == expected


def test_wizard_discovers_on_enter_and_prefers_page_options_over_guesses():
    root = Path(__file__).resolve().parents[1]
    js = (root / "root/www/luci-static/resources/smart_srun.js").read_text(encoding="utf-8")
    navigation = js.split("function wizGo(step)", 1)[1].split("function wizClose", 1)[0]
    assert "if (step === 2) wizDiscoverOperators(false)" in navigation
    build = js.split("function wizBuildCandidates()", 1)[1].split("function wizDiscoverOperators", 1)[0]
    assert build.index("wiz.portalOps.forEach") < build.index("findSchoolPreset(wiz.school)")
    assert "认证页面" in build
    assert "WIZ_GENERIC_SUFFIXES" not in js


def test_single_indexed_suffix_and_nonstandard_select_id():
    parser = portal._OperatorOptions()
    parser.feed('<select id="accountType" name="domain">'
                '<option value="1-@Students.Campus.test">学生用户</option></select>')
    assert parser.operators == [{"suffix": "Students.Campus.test", "label": "学生用户"}]
    parser = portal._OperatorOptions()
    parser.feed('<select name="service"><option value="1">套餐一</option>'
                '<option value="2-@Research.example.edu">研究人员</option></select>')
    assert parser.operators == [{"suffix": "Research.example.edu", "label": "研究人员"}]


def test_arbitrary_realms_keep_case_labels_and_explicit_empty_values():
    parser = portal._OperatorOptions()
    parser.feed('<select name="realm"><option value="@Staff.Research-2.edu.cn">研究人员</option>'
                '<option value="@Lab_42">实验室</option><option value="">访客用户</option></select>')
    assert parser.operators == [{"suffix": "Staff.Research-2.edu.cn", "label": "研究人员"},
                              {"suffix": "Lab_42", "label": "实验室"},
                              {"suffix": "", "label": "访客用户"}]


def test_explicit_realms_survive_discovery_configuration_and_login():
    import config
    import school_presets

    for suffix in ("xn", "42", "Research.Example.test", "Lab_42", ""):
        parser = portal._OperatorOptions()
        parser.feed('<select name="realm"><option value="%s">账号类型</option></select>' % suffix)
        assert parser.operators == [{"suffix": suffix, "label": "账号类型"}]
        choices = school_presets._normalize_operators(parser.operators)
        cfg = config.resolve_active_items({"campus_accounts": [
            {"id": "sample", "user_id": "sample-user", "operator_suffix": choices[0]["suffix"]}
        ], "hotspot_profiles": []})
        expected = "sample-user" + ("@" + suffix if suffix else "")
        assert cfg["username"] == expected
        with mock.patch.object(config, "load_config", return_value={}), \
                mock.patch.object(srun_auth, "probe_online_identity", return_value=(False, "", "")), \
                mock.patch.object(srun_auth, "probe_login_once", return_value=(True, "成功")) as login:
            result = portal.detect_operator("http://portal.example.test", "7", "sample-user", "fixture-password", [suffix])
        assert result["confirmed"] and result["suffix"] == suffix
        assert login.call_args.args[0]["username"] == expected


def test_chinese_operator_labels_respect_gateway_encoding():
    body = '<select id="domain"><option value="@cmcc">移动校园网</option></select>'
    response = mock.Mock()
    response.read.return_value = body.encode("gb2312")
    response.status = 200
    response.getheaders.return_value = [("Content-Type", "text/html; Charset=gb2312")]
    connection = mock.Mock()
    connection.getresponse.return_value = response
    with mock.patch.object(network.http_client, "HTTPConnection", return_value=connection):
        decoded, status, _ = network._http_get_via_stdlib("http://portal.example.test", 5, None, return_headers=True)
    assert decoded == body and status == 200
    response.headers = dict(response.getheaders.return_value)
    response.getcode.return_value = 200
    with mock.patch.object(portal.urllib_request, "build_opener") as opener:
        opener.return_value.open.return_value = response
        status, _, decoded = portal._fetch_once("http://portal.example.test", 5)
    assert decoded == body and status == 200
