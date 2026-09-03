"""Run the shipped LuCI preset functions against a small DOM/XHR surface."""

import json
from pathlib import Path
import shutil
import subprocess
import unittest

from _portal_urls import PORTAL_ORIGIN


REPO_ROOT = Path(__file__).resolve().parents[1]
JS_FILE = REPO_ROOT / "root/www/luci-static/resources/smart_srun.js"


class PresetLuciTests(unittest.TestCase):
    def _run_ui(self, initial, custom=None, lookups=None, refreshes=None):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        scenario = {
            "initial": initial,
            "custom": custom or [],
            "lookups": lookups or [],
            "refreshes": refreshes or [],
        }
        # Expose the real closure functions only inside this test VM. Keeping
        # readJson/fetchJson intact exercises both textarea and HTTP handling.
        script = r"""
const fs = require('fs');
const vm = require('vm');
const scenario = JSON.parse(process.argv[1]);
let source = fs.readFileSync(process.argv[2], 'utf8');
const end = source.lastIndexOf('})();');
if (end < 0) throw new Error('Missing LuCI script closure');
source = source.slice(0, end) + `
  window.presetTestApi = {
    list: schoolPresetList,
    find: findSchoolPreset,
    refresh: refreshSchoolPresets,
    initCustom: initUserPresetStore
  };
` + source.slice(end);
function textarea(value) {
  const text = JSON.stringify(value);
  return {value: text, textContent: text};
}
const nodes = {
  'smart-school-preset-data': textarea(scenario.initial),
  'smart-user-preset-data': textarea({presets: scenario.custom, operators: []})
};
const pending = [];
const urls = [];
function XHR() {}
XHR.prototype.open = function(method, url, async) {
  this.url = url;
  this.async = async;
  urls.push(url);
};
XHR.prototype.send = function() { pending.push(this); };
const context = {
  window: {},
  document: {
    readyState: 'loading', addEventListener() {},
    getElementById(id) { return nodes[id] || null; }
  },
  XMLHttpRequest: XHR,
  Date, JSON
};
vm.runInNewContext(source, context);
const api = context.window.presetTestApi;
api.initCustom();
function snapshot() {
  const found = {};
  scenario.lookups.forEach(id => { found[id] = api.find(id); });
  return {
    schools: api.list(), found,
    value: nodes['smart-school-preset-data'].value,
    textContent: nodes['smart-school-preset-data'].textContent
  };
}
const states = [snapshot()];
const beforeResponses = [];
scenario.refreshes.forEach(payload => {
  // Allow another page refresh cycle while retaining the previous DOM value.
  context.window.__smartPresetsRefresh = false;
  api.refresh();
  beforeResponses.push(snapshot());
  if (pending.length !== 1) throw new Error('Expected one asynchronous request');
  const xhr = pending.shift();
  if (!xhr.async) throw new Error('Preset request must be asynchronous');
  xhr.readyState = 4;
  xhr.status = 200;
  xhr.responseText = JSON.stringify(payload);
  xhr.onreadystatechange();
  states.push(snapshot());
});
console.log(JSON.stringify({states, beforeResponses, urls}));
"""
        result = subprocess.run(
            [node, "-e", script, json.dumps(scenario), str(JS_FILE)],
            stdin=subprocess.DEVNULL,
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=15,
        )
        return json.loads(result.stdout)

    def test_public_presets_require_active_status(self):
        active = {"short_name": "active-school", "status": "active"}
        rendered = self._run_ui([
            active,
            {"short_name": "draft-school", "status": "draft"},
            {"short_name": "retired-school", "status": "deprecated"},
            {"short_name": "unclassified-school"},
        ])
        self.assertEqual(rendered["states"][0]["schools"], [active])

    def test_custom_preset_without_status_remains_findable(self):
        active = {"short_name": "active-school", "status": "active"}
        custom = {
            "short_name": "custom-123", "name": "My campus", "custom": True,
            "defaults": {"base_url": PORTAL_ORIGIN},
        }
        rendered = self._run_ui(
            [active, {"short_name": "draft-school", "status": "draft"}],
            custom=[custom],
            lookups=["active-school", "draft-school", "custom-123", "__none__", "missing"],
        )
        found = rendered["states"][0]["found"]
        self.assertEqual(found["active-school"], active)
        self.assertEqual(found["custom-123"], custom)
        self.assertIsNone(found["draft-school"])
        self.assertIsNone(found["__none__"])
        self.assertIsNone(found["missing"])

    def test_async_empty_refresh_clears_public_presets_and_keeps_custom(self):
        initial = {"short_name": "old-school", "status": "active"}
        refreshed = {"short_name": "new-school", "status": "active"}
        custom = {"short_name": "custom-123", "name": "My campus", "custom": True}
        rendered = self._run_ui(
            [initial],
            custom=[custom],
            lookups=["old-school", "new-school", "custom-123"],
            refreshes=[{"ok": True, "schools": [refreshed]}, {"ok": True, "schools": []}],
        )
        before = rendered["beforeResponses"]
        states = rendered["states"]
        self.assertEqual(before[0]["schools"], [initial])
        self.assertEqual(states[1]["schools"], [refreshed])
        self.assertIsNone(states[1]["found"]["old-school"])
        self.assertEqual(before[1]["schools"], [refreshed])
        self.assertEqual(states[2]["schools"], [])
        self.assertEqual(states[2]["value"], "[]")
        self.assertEqual(states[2]["textContent"], "[]")
        self.assertIsNone(states[2]["found"]["new-school"])
        self.assertEqual(states[2]["found"]["custom-123"], custom)
        self.assertEqual(len(rendered["urls"]), 2)
        self.assertTrue(all("/presets_refresh?" in url for url in rendered["urls"]))


if __name__ == "__main__":
    unittest.main()
