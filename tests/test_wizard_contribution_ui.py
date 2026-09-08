"""Verify the save receipt and the allowlisted, user-submitted preset draft."""

from pathlib import Path
import shutil
import subprocess
import unittest


JS_FILE = Path(__file__).resolve().parents[1] / "root/www/luci-static/resources/smart_srun.js"


class WizardContributionTests(unittest.TestCase):
    def test_saved_receipt_and_draft_exclude_credentials(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        script = r"""
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const source = fs.readFileSync(process.argv[1], 'utf8');
let posts = 0, reloads = 0, renderCount = 0, response, callback;
const context = {findSchoolPreset: () => ({name:'Example Campus',short_name:'example'}),
  location: {reload() {reloads++;}}, L: {hideModal() {}},
  document: {getElementById() {return null;}}};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('  var WIZ_STEPS'), source.indexOf('  function initAll()')), context);
context.wizRender = () => {renderCount++;};
context.wizNormalizeAddress = () => true;
context.wizPost = (action, payload, done) => {assert.strictEqual(action, 'enqueue'); posts++; callback=done;};
context.wizError = error => {context.wiz.busy=false; context.wiz.error=error;};
function setup() {
  context.wizReset();
  Object.assign(context.wiz, {step:4,school:'example',accessMode:'wifi',ssid:'CampusWiFi',
    acId:'7',baseUrl:'https://url-user:url-password@portal.example.test/login?token=secret-token#session',
    userId:'private-account',password:'private-password',wifiKey:'private-wifi-password',
    current_ip:'192.0.2.199',bssid:'02:00:00:00:00:99',selectedSuffix:'realm.example',
    wifiJob:'private-job',passwordVerified:true,
    portalOps:[{suffix:'realm.example',label:'Student'},{suffix:'',label:'Guest'},
      {suffix:'realm.example',label:'Duplicate'},{suffix:'??',label:'Unknown'}],
    ops:[{suffix:'realm.example',label:'Student'},{suffix:'',label:'Guest'}]});
}
setup(); context.wizSave();
assert.strictEqual(posts,1); context.wizSave(); assert.strictEqual(posts,1,'double save while pending');
assert.ok(!context.wiz.saveComplete,'cannot show success while pending');
context.wiz.busy=false; callback(null, {ok:true,saved:true});
assert.strictEqual(context.wiz.saveComplete,true);
assert.strictEqual(reloads,0,'must keep the receipt open');
assert.strictEqual(context.wiz.wifiJob,'');
assert.strictEqual(context.wiz.userId,''); assert.strictEqual(context.wiz.password,'');
assert.strictEqual(context.wiz.wifiKey,'');
assert.strictEqual(context.wiz.contribution.defaults.base_url,'https://portal.example.test');
assert.deepStrictEqual(Array.from(context.wiz.contribution.operators, op=>op.suffix),['realm.example','']);
assert.strictEqual(context.wiz.contribution.status,'draft','saving is not proof of school-wide compatibility');
let body = context.wizContributionBody();
for(const secret of ['private-account','private-password','private-wifi-password','private-job','secret-token',
  'url-user','url-password','192.0.2.199','02:00:00:00:00:99']) assert.ok(!body.includes(secret),secret+' leaked');
assert.ok(body.includes('已通过插件验证')); assert.ok(body.includes('尚未确认'));
context.wiz.contributionOnline=true; body=context.wizContributionBody(); assert.ok(body.includes('是（用户确认）'));
context.wizSave(); assert.strictEqual(posts,1,'cannot save a second account from receipt');
context.wizClose(); assert.strictEqual(reloads,1); assert.strictEqual(context.wiz,null);
setup(); context.wizSave(); context.wiz.busy=false; callback(new Error('Request failed'), {});
assert.ok(!context.wiz.saveComplete); assert.strictEqual(context.wiz.password,'private-password');
assert.ok(!context.wiz.saved,'failed save must remain retryable');
setup(); context.wizSave(); context.wiz.busy=false; callback(null, {ok:false,saved:true,message:'Post-save failure'});
assert.ok(!context.wiz.saveComplete,'partial failure must not claim success');
assert.strictEqual(context.wiz.saved,true); assert.strictEqual(context.wiz.wifiJob,'');
const before = posts; context.wizSave(); assert.strictEqual(posts,before,'partial saved result must not duplicate account');
setup(); context.wiz.portalOps=[]; context.wiz.ops=[]; context.wiz.selectedSuffix='special.school';
context.wiz.contribution=context.wizContributionData();
assert.strictEqual(context.wiz.contribution.operators[0].suffix,'special.school');
context.wiz.selectedSuffix=''; context.wiz.accessMode='wired';
context.wiz.contribution=context.wizContributionData();
assert.strictEqual(context.wiz.contribution.operators[0].suffix,'');
assert.ok(!('ssid' in context.wiz.contribution.defaults));
"""
        result = subprocess.run([node, "-e", script, str(JS_FILE)], stdin=subprocess.DEVNULL,
                                capture_output=True, text=True, encoding="utf-8")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
