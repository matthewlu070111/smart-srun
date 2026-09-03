"""Exercise the LuCI action result and portal link without a router/browser."""

import json
import shutil
import subprocess
import unittest
from pathlib import Path

from _portal_urls import PORTAL_IPV4_ORIGIN, PORTAL_ORIGIN


REPO_ROOT = Path(__file__).resolve().parents[1]
JS_FILE = REPO_ROOT / "root/www/luci-static/resources/smart_srun.js"
CONTROLLER_FILE = REPO_ROOT / "root/usr/lib/lua/luci/controller/smart_srun.lua"
CBI_FILE = REPO_ROOT / "root/usr/lib/lua/luci/model/cbi/smart_srun.lua"


class PortalLuciTests(unittest.TestCase):
    def _run_ui(self, status):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        # Run the shipped script. A small DOM/XHR surface lets the real modal
        # consume the same status payload as the controller sends to browsers.
        script = r"""
const fs = require('fs');
const vm = require('vm');
const payload = JSON.parse(process.argv[1]);
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
XHR.prototype.send = function() {
  this.readyState = 4;
  this.status = 200;
  this.responseText = JSON.stringify(
    this.url.indexOf('/status?') >= 0 ? payload : {empty: true}
  );
  this.onreadystatechange();
};
const context = {
  window: { setInterval() { return 1; }, clearInterval() { cleared += 1; } },
  document: { readyState: 'loading', addEventListener() {},
    getElementById(id) { return nodes[id] || null; },
    createElement(tag) { return element(tag); }, createTextNode(text) { return {textContent: text}; }
  },
  E: element, XMLHttpRequest: XHR,
  L: { showModal(title, contents) { modal = contents; } },
  Date, JSON
};
vm.runInNewContext(source, context);
context.window.smartOpenBlockingFeedback('manual_login', 100);
console.log(JSON.stringify({result: nodes['smart-srun-manual-result'].textContent,
  tip: modal[0].textContent, portal: modal[2].children,
  resultPortal: nodes['smart-srun-manual-portal'].children, urls, cleared}));
"""
        result = subprocess.run(
            [node, "-e", script, json.dumps(status), str(JS_FILE)],
            stdin=subprocess.DEVNULL,
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
        )
        return json.loads(result.stdout)

    def _status(self, **extra):
        status = {
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

    def test_stale_action_does_not_finish_current_modal(self):
        rendered = self._run_ui(self._status(last_action_ts=99))
        self.assertEqual(0, rendered["cleared"])
        self.assertEqual([], rendered["portal"])
        self.assertIn("正在执行", rendered["tip"])

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

    def test_controller_persists_and_exposes_action_specific_feedback(self):
        text = CONTROLLER_FILE.read_text(encoding="utf-8")
        for field in ("last_action_message", "last_action_portal_url"):
            self.assertIn(f'{field} = tostring(data.{field} or "")', text)
            self.assertIn(f'state.{field} = ""', text)
        self.assertIn(
            'id="smart-srun-manual-portal"', CBI_FILE.read_text(encoding="utf-8")
        )

    def test_controller_status_enqueue_and_friendly_logs_execute(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        script = r"""
local files, encoded, form = {}, {}, {}
local portal_url = PORTAL_URL
local sequence, output = 0, nil
local function stringify(value)
    sequence = sequence + 1
    local key = "json" .. sequence
    encoded[key] = value
    return key
end
local function parse(value)
    return encoded[tostring(value or ""):match("^(json%d+)")] or {}
end
local state_path = "/var/run/smart_srun/state.json"
files[state_path] = stringify({
    last_action="manual_login", action_result="error", message="later tick",
    last_action_message="original failure", last_action_portal_url=portal_url
})
package.preload["luci.http"] = function() return {
    formvalue=function(key) return form[key] end, prepare_content=function() end,
    write=function(value) output = parse(value) end
} end
package.preload["luci.jsonc"] = function() return {parse=parse, stringify=stringify} end
package.preload["luci.sys"] = function() return {exec=function() return "" end, call=function() return 0 end} end
package.preload["luci.util"] = function() return {
    trim=function(value) return tostring(value or ""):match("^%s*(.-)%s*$") end
} end
package.preload["nixio.fs"] = function() return {
    readfile=function(path) return files[path] end,
    writefile=function(path, value) files[path] = value; return true end,
    rename=function(from, target) files[target] = files[from]; files[from] = nil; return true end,
    access=function() return false end, mkdirr=function() return true end,
    remove=function(path) files[path] = nil; return true end,
    dir=function() return function() return nil end end
} end
package.preload["luci.smart_srun.schema"] = function() return {
    POINTER_KEYS={}, LIST_KEYS={}, global_scalar_key_set=function() return {} end,
    with_file_lock=function(_, callback) return callback() end
} end
dofile(CONTROLLER_PATH)
local controller = package.loaded["luci.controller.smart_srun"]
controller.action_status()
assert(output.status == "later tick")
assert(output.last_action_message == "original failure")
assert(output.last_action_portal_url == portal_url)
form.action = "manual_login"
controller.action_enqueue()
local saved = parse(files[state_path])
assert(saved.last_action_message == "")
assert(saved.last_action_portal_url == "")
assert(saved.action_result == "pending")
local config_path = "/usr/lib/smart_srun/config.json"
files[config_path] = stringify({campus_accounts={{
    id="campus1", network_interface="wan.old", custom_field="preserved"
}}})
form = {
    action="edit_campus", id="campus1", access_mode="wired", wired_iface="  ",
    network_interface="  wan.test  ", auth_enabled="1"
}
controller.action_enqueue()
local account = parse(files[config_path]).campus_accounts[1]
assert(account.wired_iface == "wan.test")
assert(account.network_interface == nil)
assert(account.custom_field == "preserved")
assert(account.auth_enabled == "1")
form.wired_iface = "  wan.canonical  "
controller.action_enqueue()
assert(parse(files[config_path]).campus_accounts[1].wired_iface == "wan.canonical")
for _, code in ipairs({
    "no_response_data_error", "not_online_error", "portal_intercept_error",
    "auth_html_response_error", "auth_response_parse_error"
}) do
    local line = controller.friendly_line('[2026-06-01 22:00:00] WARN srun_login_response error_code=' .. code)
    assert(not line:find(code, 1, true), line)
end
local multi = controller.friendly_line(
    '[2026-06-01 22:00:00] WARN multi_wan_session account_id=campus1 wired_iface=wan.test | offline'
)
assert(not multi:find("multi_wan_session", 1, true), multi)
assert(multi:find("wan.test", 1, true), multi)
""".replace("CONTROLLER_PATH", json.dumps(CONTROLLER_FILE.as_posix())).replace(
            "PORTAL_URL", json.dumps(PORTAL_ORIGIN)
        )
        subprocess.run(
            [lua, "-e", script],
            stdin=subprocess.DEVNULL,
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
        )

    def test_luci_account_editor_reads_legacy_interface_but_saves_canonical(self):
        controller = CONTROLLER_FILE.read_text(encoding="utf-8")
        js = JS_FILE.read_text(encoding="utf-8")
        cbi = CBI_FILE.read_text(encoding="utf-8")
        self.assertIn('util.trim(fv("network_interface"))', controller)
        self.assertIn("merged.network_interface = nil", controller)
        self.assertIn("String(item.network_interface || '').replace", js)
        self.assertIn("fd.append('wired_iface'", js)
        self.assertNotIn("fd.append('network_interface'", js)
        self.assertIn('util.trim(tostring(a.network_interface or ""))', cbi)


if __name__ == "__main__":
    unittest.main()
