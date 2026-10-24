const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const source = fs.readFileSync(path.join(__dirname, '../../root/www/luci-static/resources/smart_srun.js'), 'utf8');
const code = source.slice(source.indexOf('  var campusData'), source.indexOf('  function initAll()'));

function fixture(revision = '17') {
  const nodes = {}, requests = [], alerts = [], timers = [], listeners = {};
  let reloads = 0, closed = 0;
  for (const id of ['label', 'user_id', 'operator_suffix', 'access_mode', 'wired_iface', 'auth_enabled',
    'password', 'base_url', 'ac_id', 'login-n', 'login-type', 'login-enc', 'info-prefix',
    'double-stack', 'login-os', 'login-name', 'ssid', 'bssid', 'ap_selection', 'radio', 'encryption', 'key']) {
    nodes['jm-' + id] = {value: 'saved-' + id};
  }
  nodes['jm-access_mode'].value = 'wired';
  if (revision !== null) nodes['smart-srun-config-revision'] = {value: revision};
  function XHR() { requests.push(this); }
  XHR.prototype.open = function(method, url) { this.method = method; this.url = url; };
  XHR.prototype.setRequestHeader = function() {};
  XHR.prototype.send = function(body) { this.body = body; };
  function FormData() { this.fields = {}; }
  FormData.prototype.append = function(key, value) { this.fields[key] = String(value); };
  const L = {hideModal() { closed++; }, showModal() {}};
  const context = {
    window: {L, setInterval(fn) { timers.push(fn); return timers.length; }, clearInterval() {}}, L,
    document: {hidden: false, getElementById(id) { return nodes[id] || null; },
      querySelector() { return {value: 'csrf-token'}; },
      addEventListener(event, callback) { (listeners[event] ||= []).push(callback); },
      body: {contains() { return true; }, classList: {contains() { return true; }}}
    },
    XMLHttpRequest: XHR, FormData, location: {reload() { reloads++; }},
    alert(message) { alerts.push(message); }, confirm() { return true; },
    E(tag, attrs, text) { return {style: {}, textContent: text || '', appendChild() {}, addEventListener() {}}; }
  };
  vm.createContext(context);
  vm.runInContext(code, context);
  return {context, nodes, requests, alerts, timers, listeners,
    get reloads() { return reloads; }, get closed() { return closed; },
    reply(text, status = 200) {
      const request = requests.at(-1);
      Object.assign(request, {status, responseText: text, readyState: 4});
      (request.onload || request.onreadystatechange)();
    },
    modal(kind = 'campus', id = 'saved-id') {
      context.modalType = kind; context.modalEditId = id;
      context.window.smartModalSave();
    },
    wizard() {
      context.wizReset();
      context.wizRender = () => {};
      context.wizNormalizeAddress = () => true;
      Object.assign(context.wiz, {root: {}, userId: 'saved-user', password: 'saved-password',
        selectedSuffix: '', baseUrl: 'https://portal.example.test', accessMode: 'wired', wiredIface: 'wan'});
      context.wizSave();
    }
  };
}

const failures = [];
function check(name, callback) {
  try { callback(); console.log('PASS ' + name); }
  catch (error) { failures.push(name + ': ' + error.stack); }
}
const mutations = [
  ['default campus', f => f.context.window.smartSetDefault('campus', 'saved-id')],
  ['default hotspot', f => f.context.window.smartSetDefault('hotspot', 'saved-id')],
  ['delete campus', f => f.context.window.smartDelete('campus', 'saved-id')],
  ['delete hotspot', f => f.context.window.smartDelete('hotspot', 'saved-id')],
  ['edit campus', f => f.modal()], ['add campus', f => f.modal('campus', '')],
  ['edit hotspot', f => f.modal('hotspot')], ['add hotspot', f => f.modal('hotspot', '')],
  ['wizard save', f => f.wizard()]
];

check('every configuration mutation carries the page revision, including zero', () => {
  for (const revision of ['0', '17']) for (const [name, mutate] of mutations) {
    const f = fixture(revision); mutate(f);
    assert.strictEqual(f.requests.length, 1, name);
    const body = f.requests[0].body;
    const sent = typeof body === 'string' ? new URLSearchParams(body).get('expected_revision') : body.fields.expected_revision;
    assert.strictEqual(sent, revision, name);
  }
});

check('missing or invalid page revisions block every configuration mutation', () => {
  for (const revision of [null, '', '-1', '1.5', '1e2', ' 17 ', '01', 'NaN', '9007199254740992']) for (const [name, mutate] of mutations) {
    const f = fixture(revision); mutate(f);
    assert.strictEqual(f.requests.length, 0, name + '/' + revision);
    assert(f.alerts.length || f.context.wiz.error, 'explain why the save was blocked');
  }
});

check('only explicit success closes an editor or refreshes a mutation page', () => {
  for (const response of ['', '{invalid', '{}', 'null', '[]', '{"ok":false}', '{"ok":1}', '{"ok":"true"}']) {
    for (const [name, mutate] of mutations) {
      const f = fixture(); mutate(f); f.reply(response);
      assert.strictEqual(f.reloads, 0, name + '/' + response);
      assert.strictEqual(f.closed, 0, name + '/' + response);
      assert.notStrictEqual(f.context.window.__smartModalSaving, true, name);
      assert.strictEqual(f.nodes['jm-password'].value, 'saved-password');
      if (name === 'wizard save') assert.strictEqual(f.context.wiz.password, 'saved-password');
      else assert(f.alerts.length, name + ' must report failure');
    }
  }
  for (const [name, mutate] of mutations.filter(([name]) => name !== 'wizard save')) {
    const f = fixture(); mutate(f); f.reply('{"ok":true}');
    assert.strictEqual(f.reloads, 1, name);
  }
  const f = fixture(); f.modal(); f.reply('{"ok":false,"message":"配置已变化，请刷新"}', 409);
  assert.strictEqual(f.reloads, 0);
  assert.strictEqual(f.alerts[0], '配置已变化，请刷新');
  assert.strictEqual(f.nodes['jm-password'].value, 'saved-password');
});

check('slow status requests never overlap, including visibility events and failures', () => {
  const f = fixture('17');
  for (const suffix of ['', '-title', '-meta', '-details-content', '-pending']) {
    f.nodes['smart-srun-overview' + suffix] = {style: {}, textContent: '', innerHTML: ''};
  }
  f.context.initOverview();
  function tick() { f.timers.forEach(fn => fn()); }
  function visible() { for (const event of ['visibilitychange', 'webkitvisibilitychange']) f.listeners[event].forEach(fn => fn()); }
  assert.strictEqual(f.requests.length, 1);
  tick(); tick(); visible();
  assert.strictEqual(f.requests.length, 1, 'pending request must block timer and visibility refreshes');
  f.reply('{"ok":false,"message":"配置读取失败"}');
  assert.strictEqual(f.nodes['smart-srun-overview-title'].textContent, '状态读取失败');
  tick(); assert.strictEqual(f.requests.length, 2);
  f.requests.at(-1).ontimeout();
  visible(); assert.strictEqual(f.requests.length, 3, 'timeouts must release the request guard');
  tick(); assert.strictEqual(f.requests.length, 3);
  f.reply('{"ok":true,"status":"在线","revision":99}');
  assert.strictEqual(f.nodes['smart-srun-config-revision'].value, '17', 'status cannot rebase stale form contents');
  assert.strictEqual(f.nodes['jm-password'].value, 'saved-password');
  f.context.document.hidden = true; tick();
  assert.strictEqual(f.requests.length, 3);
  f.context.document.hidden = false; visible();
  assert.strictEqual(f.requests.length, 4);
  f.reply('null');
  assert.strictEqual(f.nodes['smart-srun-overview-title'].textContent, '状态读取失败');
  tick(); assert.strictEqual(f.requests.length, 5);
});

check('action feedback waits for both pending reads and stops polling after completion', () => {
  const f = fixture();
  f.context.window.smartOpenBlockingFeedback('manual_login', 1, 'action-1');
  function tick() { f.timers.forEach(fn => fn()); }
  assert.strictEqual(f.requests.length, 2);
  tick(); tick();
  assert.strictEqual(f.requests.length, 2);
  f.requests[0].onerror(); tick();
  assert.strictEqual(f.requests.length, 2, 'status is still pending');
  f.reply('{"action_id":"action-1","action_result":"pending"}');
  tick(); assert.strictEqual(f.requests.length, 4);
  f.reply('{"action_id":"action-1","action_result":"ok"}');
  f.requests[2].onerror(); tick();
  assert.strictEqual(f.requests.length, 4, 'terminal feedback must never start new reads');
});

assert.deepStrictEqual(failures, []);
