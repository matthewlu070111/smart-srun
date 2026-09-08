"""Exercise operator selection across the shipped wizard's two account steps."""

from pathlib import Path
import shutil
import subprocess
import unittest


JS_FILE = Path(__file__).resolve().parents[1] / "root/www/luci-static/resources/smart_srun.js"


class WizardOperatorUiTests(unittest.TestCase):
    def test_account_step_can_choose_discovered_suffix_without_losing_credentials(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        script = r"""
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const source = fs.readFileSync(process.argv[1], 'utf8');
let root;
function find(node, predicate) {
  if (predicate(node)) return node;
  for (const child of node.children) { const result = find(child, predicate); if (result) return result; }
  return null;
}
function element(tag) {
  if (tag === 'a') return {set href(value) {
    const url = new URL(value);
    for (const key of ['protocol','host','hostname','pathname','search']) this[key] = url[key];
  }};
  return {tagName: tag.toUpperCase(), children: [], textContent: '', value: '', style: {},
    appendChild(child) { this.children.push(child); child.parentNode = this; return child; },
    removeChild(child) { this.children.splice(this.children.indexOf(child), 1); },
    setAttribute() {}, focus() {},
    querySelector(selector) { return find(this, n => n.id === selector.slice(1)); }
  };
}
const context = {document: {
  createElement: element, createTextNode: text => Object.assign(element('text'), {textContent:text}),
  getElementById: id => find(root, n => n.id === id)
}, findSchoolPreset: () => null};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('  var WIZ_STEPS'), source.indexOf('  function initAll()')), context);
const discover = context.wizDiscoverOperators;
context.wizRender = () => {
  root = element('div'); context.wiz.root = root;
  if (context.wiz.error) { const error = element('div'); error.id = 'wiz-error'; root.appendChild(error); }
  if (context.wiz.step === 2) context.wizStepOperator(root);
  if (context.wiz.step === 3) context.wizStepAccount(root);
};
context.wizDiscoverOperators = () => { throw new Error('Account selection must not issue a discovery request'); };
const byId = id => context.document.getElementById(id);
const click = text => find(root, n => n.tagName === 'BUTTON' && n.textContent === text).onclick();
const input = (id, value) => { const field = byId(id); field.value = value; field.oninput(); };
const choose = value => { const field = byId('wiz-account-operator'); field.value = value; field.onchange(); };
context.wizReset();
Object.assign(context.wiz, {step:2, ops:[
  {suffix:'cmcc', label:'移动', src:'认证页自动发现'},
  {suffix:'ctcc', label:'电信', src:'认证页自动发现'},
  {suffix:'custom.realm', label:'自定义运营商', src:'认证页自动发现'}
]});
context.wizRender();
click('下一步');
assert.strictEqual(context.wiz.step, 3);
assert.strictEqual(byId('wiz-account-operator').value, '');
assert.strictEqual(context.wiz.selectedSuffix, null, 'discovery must not guess a personal operator');
assert.deepStrictEqual(byId('wiz-account-operator').children.map(n => n.textContent),
  ['请选择账号类型', '移动（@cmcc）', '电信（@ctcc）', '自定义运营商（@custom.realm）', '不加后缀']);
input('wiz-user', 'fixture-user'); input('wiz-pass', 'fixture-password');
click('下一步');
assert.strictEqual(context.wiz.step, 3, 'pending operator must block confirmation');
assert.ok(byId('wiz-error'));
choose('1');
assert.strictEqual(context.wiz.selectedSuffix, 'ctcc');
assert.strictEqual(byId('wiz-login-preview').textContent, 'fixture-user@ctcc');
assert.strictEqual(byId('wiz-error'), null);
assert.strictEqual(byId('wiz-user').value, 'fixture-user');
assert.strictEqual(byId('wiz-pass').value, 'fixture-password');
context.wiz.opConfirmed = true; context.wiz.passwordVerified = true; context.wiz.opLog = 'old result';
context.wizRender();
assert.ok(byId('wiz-account-result'));
assert.strictEqual(byId('wiz-pass').type, 'password');
find(root, n => n.tagName === 'BUTTON' && n.title === '显示密码').onclick();
assert.strictEqual(byId('wiz-pass').type, 'text');
assert.strictEqual(byId('wiz-pass').value, 'fixture-password');
assert.strictEqual(context.wiz.password, 'fixture-password');
assert.strictEqual(context.wiz.opConfirmed, true, 'visibility is not a credential change');
assert.ok(byId('wiz-account-result'));
find(root, n => n.tagName === 'BUTTON' && n.title === '隐藏密码').onclick();
assert.strictEqual(byId('wiz-pass').type, 'password');
choose('2');
assert.strictEqual(context.wiz.selectedSuffix, 'custom.realm');
assert.strictEqual(byId('wiz-login-preview').textContent, 'fixture-user@custom.realm');
assert.strictEqual(context.wiz.opConfirmed, false);
assert.strictEqual(context.wiz.passwordVerified, false);
assert.strictEqual(byId('wiz-account-result'), null);
context.wizDiscoverOperators = () => {};
click('上一步');
assert.strictEqual(find(root, n => n.tagName === 'INPUT' && n.value === 'custom.realm').checked, true);
const mobile = find(root, n => n.tagName === 'INPUT' && n.value === 'cmcc'); mobile.onchange();
click('下一步');
assert.strictEqual(byId('wiz-account-operator').value, '0', 'step three selection must carry forward');
assert.strictEqual(byId('wiz-pass').value, 'fixture-password');
choose('3');
assert.strictEqual(context.wiz.selectedSuffix, '', 'no suffix must not mean pending');
assert.ok(context.wiz.ops.some(op => op.suffix === ''));
input('wiz-user', 'fixture-user@custom.realm');
assert.strictEqual(byId('wiz-login-preview').textContent, 'fixture-user@custom.realm');
click('下一步'); assert.strictEqual(context.wiz.step, 4);
context.wizGo(3);
assert.strictEqual(byId('wiz-account-operator').children.length, 5, 'no duplicate empty suffix');
choose('');
assert.strictEqual(context.wiz.selectedSuffix, null);
assert.strictEqual(byId('wiz-login-preview').textContent, '待选择认证后缀');
click('下一步'); assert.strictEqual(context.wiz.step, 3);

// A one-option preset must not gain unrelated carrier choices.
const singlePreset = {operators:[{suffix:'Students.Campus-2.test',label:'学生用户'}]};
context.wizReset();
context.findSchoolPreset = () => singlePreset;
context.wiz.step = 2; context.wizRender();
assert.strictEqual(context.wiz.ops.length, 1);
assert.strictEqual(context.wiz.selectedSuffix, 'Students.Campus-2.test');
assert.strictEqual(context.wiz.ops[0].label, '学生用户');
assert.strictEqual(context.wiz.opConfirmed, false, 'one option is not proof of login');
click('下一步');
assert.deepStrictEqual(byId('wiz-account-operator').children.map(n => n.textContent),
  ['请选择账号类型', '学生用户（@Students.Campus-2.test）', '不加后缀']);

// An unsupported page without a preset must not invent even an empty suffix.
context.findSchoolPreset = () => ({operators:[{suffix:'??',label:'未确认'},{label:'缺失后缀'}]});
context.wizReset(); context.wiz.step = 2; context.wizRender();
assert.strictEqual(context.wiz.ops.length, 0);
assert.strictEqual(context.wiz.selectedSuffix, null);
click('下一步'); input('wiz-user', 'fixture-user'); input('wiz-pass', 'fixture-password');
context.wizPost = () => { throw new Error('No known suffix must not attempt login'); };
context.wizRunOperatorProbe(false);
assert.strictEqual(context.wiz.error, '请选择认证后缀后验证登录。');

// Refreshing the same portal keeps manual entries; a different portal clears them.
context.findSchoolPreset = () => singlePreset;
context.wizReset();
Object.assign(context.wiz, {step:2,baseUrl:'http://portal.example.test',acId:'1',accessMode:'wired',wiredIface:'wan'});
let response = {ok:true,operators:[{suffix:'staff.Research-2.edu',label:'研究人员'}]};
context.wizPost = (path, payload, done) => { context.wiz.busy = ''; done(null, response); };
context.wizDiscoverOperators = discover;
discover(false);
assert.strictEqual(context.wiz.ops.length, 1, 'portal evidence must replace a different preset');
assert.strictEqual(context.wiz.selectedSuffix, 'staff.Research-2.edu');
byId('wiz-op-add').value = 'Lab_42'; click('添加并选择');
discover(true);
assert.strictEqual(context.wiz.selectedSuffix, 'Lab_42');
assert.strictEqual(context.wiz.ops.length, 2);
context.wiz.baseUrl = 'http://different.example.test';
response = {ok:true,operators:[{suffix:'',label:'访客'}]}; discover(false);
assert.strictEqual(context.wiz.ops.length, 1);
assert.strictEqual(context.wiz.selectedSuffix, '');
assert.strictEqual(context.wiz.ops[0].label, '访客');

// Explicit realm values have no reserved school-specific alias.
for (const suffix of ['xn', '42', 'Lab_Mixed.Case']) {
  byId('wiz-op-add').value = suffix; click('添加并选择');
  click('下一步');
  input('wiz-user', 'fixture-user');
  assert.strictEqual(byId('wiz-login-preview').textContent, 'fixture-user@' + suffix);
  click('上一步');
}

// The receipt renders and retains the actual school name, not a wizard key.
context.wiz.contribution = {name:'Example Campus'};
context.wiz.saveComplete = true;
root = element('div'); context.wizStepSaved(root);
assert.strictEqual(byId('wiz-contribution-name').value, 'Example Campus');
assert.strictEqual(byId('wiz-contribution-name').type, 'text');
assert.strictEqual(byId('wiz-contribution-name').placeholder, '学校及校区名称');
byId('wiz-contribution-name').value = 'Edited Campus'; byId('wiz-contribution-name').oninput();
root = element('div'); context.wizStepSaved(root);
assert.strictEqual(byId('wiz-contribution-name').value, 'Edited Campus');

// Discovery must keep custom portal paths after the login origin is normalized.
context.wizReset();
Object.assign(context.wiz, {step:2,baseUrl:'https://portal.example.test:8443/custom/login?ac_id=42',accessMode:'wired',wiredIface:'wan'});
assert.strictEqual(context.wizNormalizeAddress(), true);
assert.strictEqual(context.wiz.baseUrl, 'https://portal.example.test:8443');
assert.strictEqual(context.wiz.acId, '42');
assert.strictEqual(context.wizNormalizeAddress(), true);
context.wizPost = (path, payload, done) => {
  assert.strictEqual(payload.base_url, 'https://portal.example.test:8443/custom/login?ac_id=42');
  context.wiz.busy = ''; done(null, {ok:true,operators:[{suffix:'42',label:'访客'}]});
};
discover(false);
assert.strictEqual(context.wiz.selectedSuffix, '42');
context.wiz.baseUrl = 'https://different.example.test';
assert.strictEqual(context.wizNormalizeAddress(), true);
assert.strictEqual(context.wiz.portalUrl, '', 'never reuse a different gateway page');
"""
        result = subprocess.run([node, "-e", script, str(JS_FILE)],
                                stdin=subprocess.DEVNULL, capture_output=True, text=True, encoding="utf-8", timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
