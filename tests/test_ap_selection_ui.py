"""Execute AP policy editing and status rendering through shipped LuCI code."""

import json
from pathlib import Path
import shutil
import subprocess
import unittest


ROOT = Path(__file__).resolve().parents[1]
JS = ROOT / "root/www/luci-static/resources/smart_srun.js"
LUA = ROOT / "root/usr/lib/lua/luci"
BSSID = "02:11:22:33:44:55"


class APSelectionUITests(unittest.TestCase):
    def run_js(self, scenario):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        script = r"""
const fs = require('fs'), vm = require('vm');
const scenario = JSON.parse(process.argv[1]);
let source = fs.readFileSync(process.argv[2], 'utf8');
const end = source.lastIndexOf('})();');
source = source.slice(0, end) + `
window.apTest = { normalize: normalizeApSelection, update: updateCampusAccessModeUI,
  overview: initOverview, edit: function() { modalType = 'campus'; modalEditId = 'one'; } };
` + source.slice(end);
const nodes = {}, sent = [], alerts = [];
['label', 'user_id', 'operator_suffix', 'access_mode', 'wired_iface', 'auth_enabled',
 'password', 'base_url', 'ac_id', 'login-n', 'login-type', 'login-enc', 'info-prefix',
 'double-stack', 'login-os', 'login-name', 'ssid', 'bssid', 'radio', 'ap_selection'].forEach(key => {
  nodes['jm-' + key] = {value: '', style: {}};
});
['wired-iface', 'auth-enabled', 'ssid', 'bssid', 'radio', 'ap-selection'].forEach(key => {
  nodes['jm-' + key + '-row'] = {style: {}};
});
['smart-srun-overview', 'smart-srun-overview-title', 'smart-srun-overview-meta'].forEach(key => {
  nodes[key] = {style: {}};
});
nodes['jm-access_mode'].value = scenario.mode || 'wifi';
nodes['jm-ap_selection'].value = scenario.policy || 'auto';
nodes['jm-bssid'].value = scenario.bssid || '';
function FormData() { this.fields = {}; }
FormData.prototype.append = function(key, value) { this.fields[key] = value; };
function XHR() {}
XHR.prototype.open = function(method, url) { this.url = url; this.method = method; };
XHR.prototype.send = function(body) {
  if (this.method === 'POST') { sent.push(body.fields); return; }
  this.readyState = 4; this.status = 200;
  this.responseText = JSON.stringify(scenario.status || {});
  this.onreadystatechange();
};
const context = { window: {setInterval() {}}, document: {
  readyState: 'loading', addEventListener() {},
  getElementById(id) { return nodes[id] || null; }
}, XMLHttpRequest: XHR, FormData, alert(value) { alerts.push(value); }, Date, JSON };
vm.runInNewContext(source, context);
context.window.apTest.edit();
context.window.apTest.update();
context.window.smartModalSave();
context.window.apTest.overview();
console.log(JSON.stringify({ sent, alerts, bssidDisabled: nodes['jm-bssid'].disabled,
  policyDisabled: nodes['jm-ap_selection'].disabled,
  normalizations: [context.window.apTest.normalize(null, scenario.bssid),
    context.window.apTest.normalize('auto', scenario.bssid)],
  overview: nodes['smart-srun-overview-meta'].innerHTML }));
"""
        output = subprocess.run(
            [node, "-e", script, json.dumps(scenario), str(JS)], check=True,
            stdin=subprocess.DEVNULL, capture_output=True, text=True, encoding="utf-8", timeout=15,
        )
        return json.loads(output.stdout)

    def test_auto_and_strongest_disable_fixed_field_but_preserve_remembered_value(self):
        for policy in ("auto", "strongest"):
            with self.subTest(policy=policy):
                output = self.run_js({"policy": policy, "bssid": BSSID})
                self.assertTrue(output["bssidDisabled"])
                self.assertFalse(output["policyDisabled"])
                self.assertEqual(output["sent"][0]["ap_selection"], policy)
                self.assertEqual(output["sent"][0]["bssid"], BSSID)
                self.assertEqual(output["normalizations"], ["fixed", "auto"])

    def test_fixed_invalid_does_not_send_or_lock_save_and_wired_ignores_it(self):
        for address in ("", "bad", "ff:ff:ff:ff:ff:ff", "01:11:22:33:44:55"):
            with self.subTest(address=address):
                output = self.run_js({"policy": "fixed", "bssid": address})
                self.assertFalse(output["bssidDisabled"])
                self.assertEqual(output["sent"], [])
                self.assertIn("固定 BSSID", output["alerts"][0])
        valid = self.run_js({"policy": "fixed", "bssid": BSSID})
        self.assertEqual(valid["alerts"], [])
        self.assertEqual(valid["sent"][0]["bssid"], BSSID)
        wired = self.run_js({"mode": "wired", "policy": "fixed"})
        self.assertTrue(wired["bssidDisabled"])
        self.assertTrue(wired["policyDisabled"])
        self.assertEqual(len(wired["sent"]), 1)

    def test_status_uses_observed_values_escapes_reason_and_keeps_unknown_signal(self):
        output = self.run_js({"status": {
            "current_bssid": BSSID, "current_wireless_ifname": "phy0-sta0",
            "current_signal": -47, "current_channel": 36,
            "ap_selection_policy": "strongest", "ap_selection_reason": "<untrusted>",
        }})
        self.assertIn("-47 dBm", output["overview"])
        self.assertIn("实际 AP: " + BSSID, output["overview"])
        self.assertIn("信道: 36", output["overview"])
        self.assertIn("&lt;untrusted&gt;", output["overview"])
        missing = self.run_js({"status": {"campus_bssid": BSSID, "current_signal": None}})
        self.assertIn("实际 AP: 未知", missing["overview"])
        self.assertIn("信号: 未知", missing["overview"])
        self.assertNotIn(BSSID, missing["overview"])
        self.assertNotIn("0 dBm", missing["overview"])

    def test_wired_hides_ap_fields_and_hotspot_only_shows_observations(self):
        wired = self.run_js({"status": {"current_campus_access_mode": "wired"}})
        self.assertNotIn("实际 AP:", wired["overview"])
        self.assertNotIn("信号:", wired["overview"])
        hotspot = self.run_js({"status": {"mode": "hotspot", "current_bssid": BSSID, "current_signal": -55}})
        self.assertIn("实际 AP: " + BSSID, hotspot["overview"])
        self.assertIn("-55 dBm", hotspot["overview"])
        self.assertNotIn("AP 选择:", hotspot["overview"])
        self.assertNotIn("选择说明:", hotspot["overview"])

    def test_advanced_fields_and_ssr_use_policy_and_observed_state(self):
        source = JS.read_text(encoding="utf-8")
        advanced = source.split('<summary>进阶设置</summary>', 1)[1].split("'</details>'", 1)[0]
        for field in ("jm-ap_selection", "jm-radio", "jm-bssid"):
            self.assertIn('id="' + field + '"', advanced)
        cbi = (LUA / "model/cbi/smart_srun.lua").read_text(encoding="utf-8")
        overview = cbi.split("function overview_status.cfgvalue()", 1)[1].split("\nend", 1)[0]
        for field in ("current_bssid", "current_signal", "current_channel", "current_wireless_ifname"):
            self.assertIn("state." + field, overview)
        self.assertNotIn("campus_bssid", overview)
        self.assertIn('ap_selection == "fixed"', cbi)

    def test_real_lua_schema_and_controller_reject_invalid_fixed_and_persist_policy(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        script = r"""
local files, encoded, form = {}, {}, {}
local sequence, output = 0, nil
local function stringify(value)
    sequence = sequence + 1
    local key = "json" .. sequence
    encoded[key] = value
    return key
end
local function parse(value) return encoded[tostring(value or ""):match("^(json%d+)")] or {} end
local config_path = "/usr/lib/smart_srun/config.json"
local state_path = "/var/run/smart_srun/state.json"
files[config_path] = stringify({campus_accounts={{id="one", bssid="02:11:22:33:44:55", custom=7}}})
files[state_path] = stringify({current_bssid="02:11:22:33:44:55", current_signal=-47,
    current_channel=36, current_wireless_ifname="phy0-sta0", ap_selection_policy="strongest",
    ap_selection_reason="fixture"})
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
    access=function() return false end, mkdirr=function() return true end,
    remove=function(path) files[path] = nil; return true end,
    dir=function() return function() return nil end end
} end
package.preload["nixio"] = function() return {
    open_flags=function() return 0 end,
    open=function() return {lock=function() return true end, close=function() end} end
} end
os.rename = function(from, target) files[target] = files[from]; files[from] = nil; return true end
package.preload["luci.smart_srun.schema"] = function() return dofile(SCHEMA_PATH) end
local schema = require "luci.smart_srun.schema"
assert(schema.normalize_ap_selection(nil, "02:11:22:33:44:55") == "fixed")
assert(schema.normalize_ap_selection("auto", "02:11:22:33:44:55") == "auto")
dofile(CONTROLLER_PATH)
local controller = package.loaded["luci.controller.smart_srun"]
for _, address in ipairs({"", "invalid", "01:11:22:33:44:55", "ff:ff:ff:ff:ff:ff"}) do
    local before = files[config_path]
    form = {action="edit_campus", id="one", access_mode="wifi", ap_selection="fixed", bssid=address}
    controller.action_enqueue()
    assert(output.ok == false)
    assert(files[config_path] == before)
end
for _, policy in ipairs({"auto", "strongest", "fixed"}) do
    form = {action="edit_campus", id="one", access_mode="wifi", ap_selection=policy, bssid="02:11:22:33:44:55"}
    controller.action_enqueue()
    local account = parse(files[config_path]).campus_accounts[1]
    assert(output.ok == true)
    assert(account.ap_selection == policy and account.custom == 7)
    assert(account.bssid == "02:11:22:33:44:55")
end
form.access_mode = "wired"; form.bssid = ""; form.ap_selection = "fixed"
controller.action_enqueue()
assert(output.ok == true)
assert(parse(files[config_path]).campus_accounts[1].ap_selection == "")
controller.action_status()
assert(output.current_signal == -47 and output.current_channel == 36)
assert(output.current_wireless_ifname == "phy0-sta0" and output.ap_selection_reason == "fixture")
files[state_path] = stringify({campus_bssid="02:11:22:33:44:55"})
controller.action_status()
assert(output.current_bssid == "" and output.current_signal == nil)
for _, event in ipairs({"ap_selection", "ap_association"}) do
    local line = controller.friendly_line('[2026-06-01 22:00:00] INFO ' .. event .. ' | fixture')
    assert(not line:find(event, 1, true), line)
end
""".replace("SCHEMA_PATH", json.dumps((LUA / "smart_srun/schema.lua").as_posix())).replace(
            "CONTROLLER_PATH", json.dumps((LUA / "controller/smart_srun.lua").as_posix())
        )
        subprocess.run([lua, "-e", script], check=True, stdin=subprocess.DEVNULL,
                       capture_output=True, text=True, encoding="utf-8", timeout=15)

    def test_account_table_only_displays_remembered_bssid_for_fixed_policy(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        script = r"""
local fs = {readfile=function() return nil end}
local jsonc = {parse=function() return nil end, stringify=function() return "[]" end}
package.preload["nixio.fs"] = function() return fs end
package.preload["luci.jsonc"] = function() return jsonc end
package.preload["nixio"] = function() return {} end
local schema = dofile(SCHEMA_PATH)
local util = {pcdata=function(value) return tostring(value or "") end,
    trim=function(value) return tostring(value or ""):match("^%s*(.-)%s*$") end}
local function load_state() return {current_mode="campus", current_campus_access_mode="wifi",
    current_ssid="Campus", current_bssid="02:66:77:88:99:aa", online_account_label="student"} end
local handle = assert(io.open(CBI_PATH, "rb"))
local source = handle:read("*a"):gsub("\r\n", "\n")
handle:close()
local body = assert(source:match("function tables_html%.cfgvalue%(%)\n(.-)\nend"))
local render = assert(loadstring("return function() " .. body .. " end"))()
local context = setmetatable({cfg={}, RADIO_CHOICES={}, school_presets={}, USER_PRESETS_FILE="",
    schema=schema, util=util, fs=fs, jsonc=jsonc, load_state=load_state}, {__index=_G})
setfenv(render, context)
for _, case in ipairs({
    {policy="auto"}, {policy="strongest"}, {policy="fixed", fixed=true},
    {fixed=true}, {policy="fixed", access="wired"}
}) do
    context.cfg = {campus_accounts={{id="one", user_id="student", ssid="Campus",
        bssid="02:11:22:33:44:55", ap_selection=case.policy, access_mode=case.access or "wifi"}}}
    local rows = render():match("<tbody>(.-)</tbody>")
    local label = case.access == "wired" and string.char(226, 128, 148)
        or schema.ap_selection_label(case.policy or "fixed")
    assert(rows:find(label, 1, true), rows)
    assert((rows:find("02:11:22:33:44:55", 1, true) ~= nil) == (case.fixed == true), rows)
end
""".replace("SCHEMA_PATH", json.dumps((LUA / "smart_srun/schema.lua").as_posix())).replace(
            "CBI_PATH", json.dumps((LUA / "model/cbi/smart_srun.lua").as_posix())
        )
        subprocess.run([lua, "-e", script], check=True, stdin=subprocess.DEVNULL,
                       capture_output=True, text=True, encoding="utf-8", timeout=15)

    def test_asset_url_tracks_js_mtime_and_size_with_unavailable_stat_fallback(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        script = r"""
local handle = assert(io.open(CBI_PATH, "rb"))
local source = handle:read("*a"):gsub("\r\n", "\n")
handle:close()
local body = assert(source:match("local function render_js_asset_tag%(%)\n(.-)\nend"))
local render = assert(loadstring("return function() " .. body .. " end"))()
local path = "/luci-static/resources/smart_srun.js"
local current
local fs = {stat=function(actual) assert(actual == "/www" .. path); return current end}
local context = setmetatable({JS_ASSET_PATH=path, fs=fs,
    util={pcdata=function(value) return value end}}, {__index=_G})
setfenv(render, context)
for _, case in ipairs({
    {stat={mtime=0, size=72217}, query="?v=0-72217"},
    {stat={mtime=0, size=72218}, query="?v=0-72218"},
    {stat={mtime=123, size=72218}, query="?v=123-72218"},
    {query=""}, {stat={mtime=0}, query=""}, {stat={mtime="invalid", size=10}, query=""}
}) do
    current = case.stat
    assert(render() == '<script src="' .. path .. case.query .. '"></script>')
end
fs.stat = nil
assert(render() == '<script src="' .. path .. '"></script>')
""".replace("CBI_PATH", json.dumps((LUA / "model/cbi/smart_srun.lua").as_posix()))
        subprocess.run([lua, "-e", script], check=True, stdin=subprocess.DEVNULL,
                       capture_output=True, text=True, encoding="utf-8", timeout=15)


if __name__ == "__main__":
    unittest.main()
