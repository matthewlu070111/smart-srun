"""Exercise the LuCI action result and portal link without a router/browser."""

import json
import shutil
import subprocess
import unittest
from pathlib import Path

from _portal_urls import PORTAL_IPV4_ORIGIN


REPO_ROOT = Path(__file__).resolve().parents[1]
JS_FILE = REPO_ROOT / "root/www/luci-static/resources/smart_srun.js"
CONTROLLER_FILE = REPO_ROOT / "root/usr/lib/lua/luci/controller/smart_srun.lua"
CBI_FILE = REPO_ROOT / "root/usr/lib/lua/luci/model/cbi/smart_srun.lua"


class PortalLuciTests(unittest.TestCase):
    def test_mutation_handlers_send_csrf_token(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        subprocess.run(
            [node, str(REPO_ROOT / "tests/js/luci_action_transport_test.js"), str(JS_FILE)],
            check=True, capture_output=True, text=True, encoding="utf-8", timeout=30,
        )

    def _run_ui(self, status, receipt="instance-a1", force_response=None):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        # Run the shipped script. A small DOM/XHR surface lets the real modal
        # consume the same status payload as the controller sends to browsers.
        script = r"""
const fs = require('fs');
const vm = require('vm');
const payload = JSON.parse(process.argv[1]);
const forceResponse = JSON.parse(process.argv[4]);
const source = fs.readFileSync(process.argv[2], 'utf8');
function element(tag, attrs, text) {
  const el = {
    tag, children: [], style: {}, textContent: text || '',
    appendChild(child) { this.children.push(child); return child; },
    addEventListener() {}, setAttribute(key, value) { this[key] = value; }
  };
  if (tag === 'a') Object.defineProperty(el, 'href', {
    set(value) {
      this.url = value;
      try {
        const url = new URL(value);
        this.protocol = url.protocol;
        this.host = url.host;
      } catch (_) {}
    },
    get() { return this.url; }
  });
  Object.assign(el, attrs || {});
  return el;
}
const nodes = {
  'smart-srun-manual-result': element('span'),
  'smart-srun-manual-portal': element('div')
};
const urls = [];
let modal = null;
let cleared = 0;
function XHR() {}
XHR.prototype.open = function(method, url) { this.url = url; urls.push(url); };
XHR.prototype.setRequestHeader = function() {};
XHR.prototype.send = function() {
  this.readyState = 4;
  const force = this.url.indexOf('/enqueue') >= 0;
  this.status = force ? forceResponse.status : 200;
  this.responseText = force ? forceResponse.body : JSON.stringify(
    this.url.indexOf('/status?') >= 0 ? payload : {empty: true}
  );
  this.onreadystatechange();
};
const context = {
  window: { setInterval() { return 1; }, clearInterval() { cleared += 1; } },
  document: { readyState: 'loading', addEventListener() {},
    querySelector() { return {value: 'csrf-token'}; },
    getElementById(id) { return nodes[id] || null; },
    createElement(tag) { return element(tag); }, createTextNode(text) { return {textContent: text}; }
  },
  E: element, XMLHttpRequest: XHR,
  L: { showModal(title, contents) { modal = contents; } },
  Date, JSON
};
vm.runInNewContext(source, context);
context.window.smartOpenBlockingFeedback('manual_login', 100, JSON.parse(process.argv[3]));
if (forceResponse) modal[3].children[1].click({preventDefault() {}});
console.log(JSON.stringify({result: nodes['smart-srun-manual-result'].textContent,
  tip: modal[0].textContent, portal: modal[2].children,
  resultPortal: nodes['smart-srun-manual-portal'].children, urls, cleared,
  progressDisabled: !!modal[3].children[0].disabled, forceDisabled: !!modal[3].children[1].disabled}));
"""
        result = subprocess.run(
            [node, "-e", script, json.dumps(status), str(JS_FILE), json.dumps(receipt), json.dumps(force_response)],
            stdin=subprocess.DEVNULL,
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
        )
        return json.loads(result.stdout)

    def _status(self, **extra):
        status = {
            "action_id": "instance-a1",
            "last_action": "manual_login",
            "last_action_ts": 101,
            "action_result": "error",
            "status": "后续守护状态",
            "last_action_message": "认证网关可达，但尚未联网，可尝试网页登录。",
            "last_action_portal_url": PORTAL_IPV4_ORIGIN,
        }
        status.update(extra)
        return status

    def test_terminal_uses_stable_result_and_explicit_safe_link(self):
        status = self._status()
        rendered = self._run_ui(status)
        self.assertEqual(status["last_action_message"], rendered["tip"])
        self.assertIn(status["last_action_message"], rendered["result"])
        self.assertNotIn(status["status"], rendered["result"])
        for key in ("portal", "resultPortal"):
            link = rendered[key][0]
            self.assertEqual(PORTAL_IPV4_ORIGIN, link["url"])
            self.assertEqual("_blank", link["target"])
            self.assertEqual("noopener noreferrer", link["rel"])
        self.assertEqual(1, rendered["cleared"])
        self.assertTrue(
            any(
                "log_tail?lines=200&format=friendly&since=100" in url
                for url in rendered["urls"]
            )
        )

    def test_old_status_payload_remains_compatible(self):
        rendered = self._run_ui(
            self._status(last_action_message="", last_action_portal_url="")
        )
        self.assertEqual("后续守护状态", rendered["tip"])
        self.assertEqual([], rendered["portal"])

    def test_another_actions_result_does_not_finish_current_modal(self):
        rendered = self._run_ui(self._status(action_id="instance-a2"))
        self.assertEqual(0, rendered["cleared"])
        self.assertEqual([], rendered["portal"])
        self.assertIn("正在执行", rendered["tip"])

    def test_clock_rollback_cannot_hide_own_terminal_result(self):
        rendered = self._run_ui(self._status(last_action_ts=-3600))
        self.assertEqual(1, rendered["cleared"])
        self.assertIn("尚未联网", rendered["tip"])
        self.assertTrue(any("status?action_id=instance-a1&" in url for url in rendered["urls"]))

    def test_lost_history_or_stopped_service_is_a_closable_error(self):
        rendered = self._run_ui(self._status(last_action="", last_action_message="操作记录已失效"))
        self.assertEqual("操作记录已失效", rendered["tip"])
        self.assertEqual(1, rendered["cleared"])

    def test_missing_receipt_does_not_poll_global_state(self):
        rendered = self._run_ui(self._status(), receipt=None)
        self.assertIn("无法读取操作回执", rendered["tip"])
        self.assertEqual([], rendered["urls"])

    def test_rejected_force_stop_does_not_claim_stopped_or_end_polling(self):
        for response in (
            {"status": 403, "body": "csrf rejected"},
            {"status": 0, "body": ""},
            {"status": 200, "body": "invalid json"},
            {"status": 200, "body": json.dumps({"ok": False, "message": "停止失败"})},
        ):
            with self.subTest(response=response):
                rendered = self._run_ui(self._status(action_result="pending"), force_response=response)
                self.assertIn("失败", rendered["tip"])
                self.assertEqual(0, rendered["cleared"])
                self.assertTrue(rendered["progressDisabled"])
                self.assertFalse(rendered["forceDisabled"])

    def test_confirmed_force_stop_unlocks_the_dialog(self):
        rendered = self._run_ui(self._status(action_result="pending"), force_response={
            "status": 200, "body": json.dumps({"ok": True, "message": "已停止"})})
        self.assertEqual("已停止", rendered["tip"])
        self.assertEqual(1, rendered["cleared"])
        self.assertFalse(rendered["progressDisabled"])
        self.assertTrue(rendered["forceDisabled"])

    def test_unsafe_portal_urls_are_never_rendered(self):
        for url in (
            "javascript:alert(1)",
            "data:text/html,hello",
            "//example.test",
            "https://example.test\n/",
            "https://example.test\\other",
        ):
            with self.subTest(url=url):
                rendered = self._run_ui(self._status(last_action_portal_url=url))
                self.assertEqual([], rendered["portal"])
                self.assertEqual([], rendered["resultPortal"])

    def test_success_cannot_reuse_stale_portal_guidance(self):
        rendered = self._run_ui(
            self._status(action_result="ok", last_action_message="登录成功")
        )
        self.assertEqual("登录成功", rendered["tip"])
        self.assertEqual([], rendered["portal"])

    def test_status_projection_publishes_action_specific_feedback(self):
        """The result of the last action is part of the status answer.

        It is built from the daemon's action record now, not copied out of a
        state file the page also wrote. Clearing it when a new action starts is
        the daemon's job (observe.Store.ActionStarted), which is why the page
        no longer has a line that blanks it. (M00 ledger:
        replaced_with_stronger_test -> T39;T24.)
        """
        bridge = (REPO_ROOT / "root/usr/lib/lua/luci/smart_srun/bridge.lua").read_text(
            encoding="utf-8"
        )
        for field in ("last_action", "last_action_message", "action_result",
                      "last_action_portal_url", "last_action_ts"):
            self.assertIn(f"{field} =", bridge)
        self.assertIn("ACTION_RESULT", bridge)
        self.assertIn(
            'id="smart-srun-manual-portal"', CBI_FILE.read_text(encoding="utf-8")
        )

    def test_controller_status_enqueue_and_friendly_logs_execute(self):
        """Drive the shipped controller against a recording stand-in daemon.

        The old version of this test stubbed the filesystem, because the page
        used to answer from files it also wrote. It now stubs the control
        protocol instead, which is where the work went: what is asserted is the
        request that leaves the page, the credential that does not, and the
        refusal that reaches the browser unchanged. (M00 ledger:
        ported_behavior -> T39;T24.)
        """
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        result = subprocess.run(
            [lua, (REPO_ROOT / "tests/lua/controller_actions.lua").as_posix(),
             REPO_ROOT.as_posix()],
            cwd=str(REPO_ROOT),
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)

    def test_luci_account_editor_reads_legacy_interface_but_saves_canonical(self):
        controller = CONTROLLER_FILE.read_text(encoding="utf-8")
        bridge = (REPO_ROOT / "root/usr/lib/lua/luci/smart_srun/bridge.lua").read_text(
            encoding="utf-8"
        )
        js = JS_FILE.read_text(encoding="utf-8")
        cbi = CBI_FILE.read_text(encoding="utf-8")
        # The alias is read from the form and resolved in one place; the patch
        # that goes to the daemon carries only the canonical field, so nothing
        # has to remember to delete the old one afterwards.
        self.assertIn('network_interface = fv("network_interface")', controller)
        self.assertIn("trim(form.network_interface)", bridge)
        self.assertIn("patch.wired_iface", bridge)
        self.assertNotIn("patch.network_interface", bridge)
        self.assertIn("String(item.network_interface || '').replace", js)
        self.assertIn("fd.append('wired_iface'", js)
        self.assertNotIn("fd.append('network_interface'", js)
        self.assertIn('util.trim(tostring(a.network_interface or ""))', cbi)

    def test_empty_auth_address_probes_the_environment_instead_of_refusing(self):
        # 认证地址是小白最填不出来的一项，不能拿它当探测的前置条件。空地址时
        # 前端要改问 detect_env，由路由器自己从强制门户跳转里捞出地址。
        controller = CONTROLLER_FILE.read_text(encoding="utf-8")
        js = JS_FILE.read_text(encoding="utf-8")

        self.assertIn('"detect_env"}, call("action_detect_env")', controller)
        self.assertIn("function action_detect_env()", controller)
        self.assertIn('discovery_job("detect.environment")', controller)

        self.assertIn("var path = baseUrl ? 'detect_acid' : 'detect_env';", js)
        self.assertNotIn("请先填写认证地址", js)


if __name__ == "__main__":
    unittest.main()
