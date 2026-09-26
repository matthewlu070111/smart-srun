// Run the shipped event handlers and inspect the requests they actually send.
const assert = require('assert');
const fs = require('fs');
const vm = require('vm');
const source = fs.readFileSync(process.argv[2], 'utf8');

for (const tokenInForm of [true, false]) {
  const nodes = {};
  for (const name of ['manual-login', 'manual-logout', 'manual-result',
    'switch-hotspot', 'switch-campus', 'force-close', 'switch-result',
    'log-box', 'log-pre', 'log-start', 'log-stop', 'log-clear', 'log-download']) {
    nodes['smart-srun-' + name] = {
      style: {}, textContent: '', handlers: {},
      addEventListener(name, callback) { this.handlers[name] = callback; }
    };
  }
  const sent = [];
  let init;
  function XHR() {}
  XHR.prototype.open = function(method, url) { this.method = method; this.url = url; };
  XHR.prototype.setRequestHeader = function() {};
  XHR.prototype.send = function(body) { sent.push({method: this.method, url: this.url, body}); };
  function FormData() { this.fields = {}; }
  FormData.prototype.append = function(key, value) { this.fields[key] = value; };
  const token = 'token with + & characters';
  const L = {env: {token}};
  const context = {
    window: {L, setInterval() { return 1; }}, L,
    document: {readyState: 'loading', hidden: false,
      getElementById(id) { return nodes[id] || null; },
      querySelector() { return tokenInForm ? {value: token} : null; },
      addEventListener(event, callback) { if (event === 'DOMContentLoaded') init = callback; }
    },
    XMLHttpRequest: XHR, FormData, confirm() { return true; },
    setInterval() { return 1; }, Date, JSON
  };
  vm.runInNewContext(source, context);
  init();
  for (const name of ['manual-login', 'manual-logout', 'switch-hotspot', 'switch-campus', 'force-close', 'log-clear']) {
    nodes['smart-srun-' + name].handlers.click({preventDefault() {}});
  }
  for (const kind of ['campus', 'hotspot']) {
    context.window.smartSetDefault(kind, 'fixture');
    context.window.smartDelete(kind, 'fixture');
  }
  const writes = sent.filter(request => request.method === 'POST');
  assert.strictEqual(writes.length, 10);
  for (const request of writes) {
    const actual = typeof request.body === 'string'
      ? new URLSearchParams(request.body).get('token') : request.body.fields.token;
    assert.strictEqual(actual, token, request.url);
  }
}
console.log('PASS real LuCI mutation handlers send the form or environment CSRF token');
