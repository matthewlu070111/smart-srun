const fs = require('fs');
const vm = require('vm');
const assert = require('assert');
const source = fs.readFileSync(process.argv[2], 'utf8');
function section(start, end) {
  const from = source.indexOf(start), to = source.indexOf(end, from);
  assert(from >= 0 && to > from, 'shipped handler must exist');
  return source.slice(from, to);
}
const noticeCode = section('  function initVersionNotice()', '  window.smartFetchJson');
function notice(responses) {
  const requests = [], timers = [], opened = [];
  function node() {
    return {style: {}, children: [], textContent: '', title: '',
      setAttribute(name, value) { this[name] = value; },
      appendChild(child) { this.children.push(child); },
      addEventListener(name, handler) { this[name] = handler; }};
  }
  const nodes = Object.fromEntries(['smart-srun-version-info', 'smart-srun-version-link',
    'smart-srun-update-dot'].map(id => [id, node()]));
  const context = {window: {}, document: {getElementById: id => nodes[id], createElement: node},
    RELEASES_PAGE_URL: 'https://github.com/matthewlu070111/smart-srun/releases',
    UPDATE_CHECK_URL: '/check', UPDATE_STATUS_URL: '/status',
    fetchJson(url, callback) {
      requests.push(url);
      const next = responses.shift();
      assert(next, 'unexpected request');
      callback(next.error || null, next.data);
    }, setTimeout(fn) { timers.push(fn); }, openUpdateModal(plan) { opened.push(plan); }};
  vm.runInNewContext(noticeCode + '\ninitVersionNotice();', context);
  return {context, requests, timers, opened, link: nodes['smart-srun-version-link'],
    dot: nodes['smart-srun-update-dot'], message: nodes['smart-srun-version-info'].children[0],
    click() { let prevented = false; this.link.click({preventDefault() { prevented = true; }}); return prevented; }};
}
for (const failure of [
  {data: {ok: false, code: 'DNSFailure', message: '无法解析更新域名'}},
  {data: {ok: true, update_available: false, code: 'PackageIncompatible', message: '固件 25.12 不兼容'}},
  {error: new Error('request_timeout')}
]) {
  const fixture = notice([failure, {data: {ok: true, update_available: true, plan_id: 'safe-plan', latest_tag: '2.0.0rc3'}}]);
  assert(fixture.message.textContent.includes('点击版本号重试'));
  if (failure.data) assert(fixture.message.textContent.includes(failure.data.message));
  assert.strictEqual(fixture.dot.style.display, 'none');
  assert(fixture.click());
  assert.strictEqual(fixture.requests.length, 2);
  assert.strictEqual(fixture.dot.style.display, 'inline-block');
  assert.strictEqual(fixture.message.textContent, '');
  assert(fixture.click());
  assert.strictEqual(fixture.opened[0].plan_id, 'safe-plan');
  vm.runInNewContext('initVersionNotice();', fixture.context);
  assert.strictEqual(fixture.requests.length, 2, 'initialization must not duplicate checks');
}
const pending = notice([{data: {ok: true, running: true, job_id: 'job/1'}},
  {data: {ok: true, update_available: false, message: '没有更新的兼容版本'}}]);
assert.strictEqual(pending.requests.length, 1);
pending.timers.shift()();
assert.strictEqual(pending.requests[1], '/status?job_id=job%2F1');
assert.strictEqual(pending.message.textContent, '');
assert.strictEqual(pending.click(), false);

const requestCode = section('  function fetchJson(', '  function initConfigBackup(');
for (const method of ['fetchJson', 'startUpdate']) {
  for (const outcome of ['timeout', 'error', 'success', 'http', 'json']) {
    const requests = [], replies = [];
    function XHR() { requests.push(this); }
    XHR.prototype.open = function(method, url) { this.method = method; this.url = url; };
    XHR.prototype.setRequestHeader = function() {};
    XHR.prototype.send = function(body) { this.body = body; };
    const context = {XMLHttpRequest: XHR, UPDATE_START_URL: '/start', window: {},
      document: {querySelector() { return {value: 'csrf-token'}; }},
      callback(error, data) { replies.push({error, data}); }};
    vm.runInNewContext(requestCode + `\n${method}('plan&1', callback);`, context);
    const xhr = requests[0];
    assert.strictEqual(xhr.timeout, 30000);
    if (method === 'startUpdate') assert(xhr.body.includes('plan_id=plan%261'));
    if (outcome === 'timeout') xhr.ontimeout();
    if (outcome === 'error') xhr.onerror();
    xhr.readyState = 4;
    xhr.status = outcome === 'http' ? 503 : 200;
    xhr.responseText = outcome === 'json' ? '{invalid' : '{"ok":true}';
    xhr.onreadystatechange();
    xhr.onerror();
    xhr.ontimeout();
    assert.strictEqual(replies.length, 1, `${method}/${outcome} completes once`);
    assert.strictEqual(!!replies[0].error, outcome !== 'success');
  }
}
console.log('update UI retry, polling and XHR completion passed');
