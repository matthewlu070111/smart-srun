// Run with node tests/js/acid_job_test.js. Exercise the shipped task client;
// the fake XHR controls network ordering without replacing its polling logic.
'use strict';
var assert = require('assert');
var fs = require('fs');
var vm = require('vm');
var path = require('path');
var source = fs.readFileSync(path.join(__dirname, '../../root/www/luci-static/resources/smart_srun.js'), 'utf8');
var helper = source.slice(source.indexOf('  function postDiscovery('), source.indexOf('  function wizPost('));

function setup(endpoint) {
  var requests = [], timers = [], completed = [];
  function XHR() { requests.push(this); }
  XHR.prototype.open = function(method, url) { this.method = method; this.url = url; };
  XHR.prototype.setRequestHeader = function() {};
  XHR.prototype.send = function(body) { this.body = new URLSearchParams(body); };
  XHR.prototype.abort = function() { this.aborted = true; };
  var sandbox = {XMLHttpRequest: XHR, document: {querySelector: function() { return {value: 'csrf-token'}; }}, window: {},
    setTimeout: function(fn) { timers.push(fn); return timers.length; }, clearTimeout: function(id) { timers[id - 1] = null; }};
  vm.createContext(sandbox);
  vm.runInContext(helper, sandbox);
  var handle = sandbox.postDiscovery(endpoint || 'detect_acid', {base_url: 'http://portal.invalid/path', access_mode: 'wired', iface: 'wan'},
    function(err, data) { completed.push({err: err, data: data}); });
  return {requests: requests, timers: timers, completed: completed, handle: handle,
    reply: function(data) { var req = requests[requests.length - 1]; req.status = 200; req.responseText = JSON.stringify(data); req.onload(); },
    tick: function() { var fn = timers.filter(Boolean).pop(); assert(fn); fn(); }};
}

var s = setup();
assert.strictEqual(s.requests[0].body.get('iface'), 'wan');
assert.strictEqual(s.requests[0].body.get('token'), 'csrf-token');
assert(s.requests[0].body.get('idempotency_key'));
s.reply({ok: true, action_id: 'a1', state: 'queued'});
s.tick();
assert.strictEqual(s.requests[1].body.get('action'), 'status');
assert.strictEqual(s.requests[1].body.get('base_url'), null);
s.reply({ok: true, id: 'a1', state: 'running'});
s.tick();
s.reply({ok: true, id: 'a1', state: 'succeeded', result: {ok: true, ac_id: '007'}});
assert.strictEqual(s.completed.length, 1);
assert.strictEqual(s.completed[0].data.ac_id, '007');
s.handle.abort();
assert.strictEqual(s.requests.length, 3);

s = setup();
s.reply({ok: true, action_id: 'a2', state: 'succeeded', duplicate: true});
assert.strictEqual(s.completed.length, 0, 'a completed receipt is not the result');
s.tick();
s.reply({ok: true, id: 'a2', state: 'succeeded', result: {ok: true, ac_id: '8'}});
assert.strictEqual(s.completed[0].data.ac_id, '8');

s = setup();
s.reply({ok: true, action_id: 'a3', state: 'running'});
s.tick();
s.handle.abort();
assert(s.requests[1].aborted);
assert.strictEqual(s.requests[2].body.get('action'), 'cancel');
assert.strictEqual(s.requests[2].body.get('action_id'), 'a3');
assert.strictEqual(s.requests[2].body.get('token'), 'csrf-token');
s.reply({ok: true, id: 'a3', state: 'cancelled'});
assert.strictEqual(s.completed.length, 0, 'closed wizard must ignore late replies');

s = setup();
s.reply({ok: true, action_id: 'a4', state: 'queued'});
s.tick();
s.reply({ok: true, id: 'a4', state: 'failed', message: 'line changed'});
assert.strictEqual(s.completed[0].err.message, 'line changed');
s = setup('detect_env');
assert(s.requests[0].url.endsWith('/detect_env'));
s.reply({ok: true, action_id: 'env1', state: 'queued'});
s.tick();
assert(s.requests[1].url.endsWith('/detect_env'));
s.reply({ok: true, id: 'env1', state: 'succeeded', result: {ok: false, state: 'online', message: 'no portal'}});
assert.strictEqual(s.completed[0].err, null);
assert.strictEqual(s.completed[0].data.state, 'online', 'online without a portal is a valid discovery result');
var connection = {wiz: {accessMode: 'wired', wiredIface: 'wan', wifiIface: 'wwan', ssid: 'preset-campus'}};
vm.createContext(connection);
vm.runInContext(source.slice(source.indexOf('  function wizConnection('), source.indexOf('  function wizLoginPreview(')), connection);
assert.strictEqual(connection.wizConnection().ssid, '', 'a preset Wi-Fi name must not invalidate wired discovery');
connection.wiz.accessMode = 'wifi';
assert.strictEqual(connection.wizConnection().ssid, 'preset-campus');
assert.strictEqual(connection.wizConnection().iface, 'wwan');
console.log('PASS discovery task client: submit, poll, terminal result, completed retry, cancel, failure, environment');

// Execute the shipped wizard handler: reading the online account must not send
// a password even if the user already typed it in the credentials step.
var drafts = [];
var wizard = {wiz: {userId: 'student', password: 'draft-secret', selectedSuffix: '', ops: [], shape: {}, baseUrl: 'http://portal.invalid', acId: '9'},
  WIZ_SHAPE_KEYS: [], wizConnection: function() { return {access_mode: 'wired', iface: 'wan'}; },
  wizInvalidate: function() {}, wizRender: function() {}, wizError: function(message) { throw Error(message); },
  wizPost: function(endpoint, payload) { drafts.push({endpoint: endpoint, payload: payload}); }};
vm.createContext(wizard);
vm.runInContext(source.slice(source.indexOf('  function wizRunOperatorProbe('), source.indexOf('  function wizSummary(')), wizard);
wizard.wizRunOperatorProbe(true);
assert.strictEqual(drafts[0].payload.read_only, '1');
assert.strictEqual(drafts[0].payload.password, undefined);
assert.strictEqual(drafts[0].payload.candidates, undefined);
wizard.wizRunOperatorProbe(false);
assert.strictEqual(drafts[1].payload.read_only, '0');
assert.strictEqual(drafts[1].payload.password, 'draft-secret');
assert.strictEqual(drafts[1].payload.candidates, '[""]', 'explicit empty suffix is a real candidate');
