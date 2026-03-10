local fs = require "nixio.fs"
local sys = require "luci.sys"
local util = require "luci.util"
local jsonc = require "luci.jsonc"

local CONFIG_FILE = "/usr/lib/jxnu_srun/config.json"
local DEFAULTS_FILE = "/usr/lib/jxnu_srun/defaults.json"

local function load_defaults()
    local raw = fs.readfile(DEFAULTS_FILE)
    if raw then
        local parsed = jsonc.parse(raw)
        if type(parsed) == "table" then
            local out = {}
            for k, v in pairs(parsed) do
                out[k] = tostring(v)
            end
            return out
        end
    end
    return {
        enabled = "0", user_id = "", operator = "cucc", password = "",
        quiet_hours_enabled = "1", quiet_start = "00:00", quiet_end = "06:00",
        force_logout_in_quiet = "1", developer_mode = "0", failover_enabled = "1",
        sta_iface = "", campus_ssid = "jxnu_stu", campus_encryption = "none",
        campus_key = "", hotspot_ssid = "", hotspot_encryption = "psk2",
        hotspot_key = "", hotspot_radio = "", backoff_enable = "1", backoff_max_retries = "0",
        backoff_initial_duration = "10", backoff_max_duration = "600",
        backoff_exponent_factor = "1.5", backoff_inter_const_factor = "0",
        backoff_outer_const_factor = "0", base_url = "http://172.17.1.2",
        ac_id = "1", n = "200", type = "1", enc = "srun_bx1", interval = "180",
    }
end

local DEFAULTS = load_defaults()

local function ensure_json_file()
    local dir = CONFIG_FILE:match("^(.+)/[^/]+$")
    if dir and not fs.access(dir) then
        fs.mkdirr(dir)
    end
    if not fs.access(CONFIG_FILE) then
        fs.writefile(CONFIG_FILE, "{}\n")
    end
end

local function load_cfg()
    ensure_json_file()
    local cfg = {}
    local raw = fs.readfile(CONFIG_FILE) or "{}"
    local parsed = jsonc.parse(raw)
    if type(parsed) == "table" then
        for k, v in pairs(parsed) do
            cfg[k] = tostring(v)
        end
    end

    for k, v in pairs(DEFAULTS) do
        if cfg[k] == nil or cfg[k] == "" then
            cfg[k] = v
        end
    end

    return cfg
end

local function save_cfg(cfg)
    local out = {}
    for k, v in pairs(DEFAULTS) do
        out[k] = tostring(cfg[k] or v)
    end
    ensure_json_file()
    fs.writefile(CONFIG_FILE, (jsonc.stringify(out) or "{}") .. "\n")
end

local function has_cmd(name)
    return util.trim(sys.exec("command -v " .. name .. " 2>/dev/null") or "") ~= ""
end

local HAS_TIMEOUT = has_cmd("timeout")

local function find_python()
    local py = util.trim(sys.exec("command -v python3 2>/dev/null") or "")
    if py ~= "" then
        return py
    end
    py = util.trim(sys.exec("command -v python3.11 2>/dev/null") or "")
    if py ~= "" then
        return py
    end
    return ""
end

local function run_client(args, stderr_to_stdout)
    local py = find_python()
    if py == "" then
        return "", "未找到 Python3，请先安装。"
    end

    local cmd = py .. " -B /usr/lib/jxnu_srun/client.py " .. (args or "")
    if HAS_TIMEOUT then
        cmd = "timeout 12 " .. cmd
    end

    if stderr_to_stdout then
        cmd = cmd .. " 2>&1"
    else
        cmd = cmd .. " 2>/dev/null"
    end

    return util.trim(sys.exec(cmd) or ""), nil
end

local function last_nonempty_line(text)
    local last = ""
    for line in tostring(text or ""):gmatch("[^\n]+") do
        local v = util.trim(line)
        if v ~= "" then
            last = v
        end
    end
    return last
end

local function validate_hhmm(v)
    local value = util.trim(v or "")
    local h, m = value:match("^(%d%d?):(%d%d)$")
    if not h then
        return nil
    end

    local hour = tonumber(h)
    local minute = tonumber(m)
    if not hour or not minute then
        return nil
    end
    if hour < 0 or hour > 23 or minute < 0 or minute > 59 then
        return nil
    end

    return string.format("%02d:%02d", hour, minute)
end

local function validate_non_negative_number(v)
    local value = util.trim(v or "")
    local num = tonumber(value)
    if not num or num < 0 then
        return nil
    end
    return tostring(num)
end

local function load_radio_choices()
    local out = {}
    local seen = {}
    local raw = sys.exec("uci show wireless 2>/dev/null") or ""
    for line in raw:gmatch("[^\n]+") do
        local radio, opt, val = line:match("^wireless%.(radio%d+)%.([%w_]+)=(.+)$")
        if radio and (opt == "band" or opt == "hwmode") then
            local entry = out[radio] or { label = radio }
            val = util.trim(val or "")
            val = val:gsub("^['\"]", ""):gsub("['\"]$", "")
            if opt == "band" then
                if val == "2g" then
                    entry.label = radio .. " (2.4GHz)"
                elseif val == "5g" then
                    entry.label = radio .. " (5GHz)"
                elseif val == "6g" then
                    entry.label = radio .. " (6GHz)"
                else
                    entry.label = radio .. " (" .. val .. ")"
                end
            elseif not seen[radio] then
                if val:find("a", 1, true) then
                    entry.label = radio .. " (5GHz)"
                else
                    entry.label = radio .. " (2.4GHz)"
                end
            end
            out[radio] = entry
            seen[radio] = true
        end
    end
    return out
end

local RADIO_CHOICES = load_radio_choices()

local cfg = load_cfg()
local changed = false

local function set_value(key, value)
    local v = tostring(value or "")
    if cfg[key] ~= v then
        cfg[key] = v
        changed = true
    end
end

local function bind_flag(opt, key)
    opt.rmempty = false
    function opt.cfgvalue()
        return cfg[key] == "1" and "1" or "0"
    end
    function opt.write(self, section, value)
        set_value(key, (value == "1") and "1" or "0")
    end
    function opt.remove(self, section)
        set_value(key, "0")
    end
end

local function bind_text(opt, key, normalize_fn)
    opt.rmempty = true
    function opt.cfgvalue()
        return cfg[key] or ""
    end
    function opt.write(self, section, value)
        local raw = util.trim(value or "")
        if normalize_fn then
            local normalized = normalize_fn(raw)
            if normalized == nil then
                return
            end
            set_value(key, normalized)
            return
        end
        set_value(key, raw)
    end
end

local quiet_desc = string.format("当前下线/上线时间：%s / %s", cfg.quiet_start or "00:00", cfg.quiet_end or "06:00")

m = Map("jxnu_srun", "师大校园网", "江西师范大学校园网认证配置（JSON后端）")
if not m.uci:get("jxnu_srun", "main") then
    m.uci:section("jxnu_srun", "main", "main")
    m.uci:save("jxnu_srun")
    m.uci:commit("jxnu_srun")
end

overview = m:section(SimpleSection)
overview.anonymous = true
overview_status = overview:option(DummyValue, "_overview_status", "")
overview_status.rawhtml = true
function overview_status.cfgvalue()
    return [[
<div id="jxnu-srun-overview" style="margin:4px 0 18px 0;border-left:4px solid #c62828;background:linear-gradient(180deg,#2c1b1b 0%,#221616 100%);padding:14px 16px;border-radius:0 6px 6px 0;box-shadow:0 8px 24px rgba(0,0,0,.14);">
  <div id="jxnu-srun-overview-title" style="font-size:18px;font-weight:700;color:#ffb4b4;margin-bottom:8px;">状态读取中</div>
  <div id="jxnu-srun-overview-meta" style="font-size:13px;color:#f1d3d3;display:flex;gap:14px;flex-wrap:wrap;line-height:1.6;">
    <span>WiFi: --</span>
    <span>模式: --</span>
    <span>连通性: --</span>
  </div>
</div>
<script type="text/javascript">
(function() {
  var root = document.getElementById('jxnu-srun-overview');
  var title = document.getElementById('jxnu-srun-overview-title');
  var meta = document.getElementById('jxnu-srun-overview-meta');
  if (!root || !title || !meta || window.__jxnuSrunOverviewInit) return;
  window.__jxnuSrunOverviewInit = true;

  var palette = {
    online: { border: '#2e7d32', bg1: '#16311b', bg2: '#112616', title: '#b8f2bb', meta: '#d6f3d7' },
    portal: { border: '#ef6c00', bg1: '#352313', bg2: '#291b10', title: '#ffd08f', meta: '#f5dfbb' },
    limited: { border: '#c62828', bg1: '#2c1b1b', bg2: '#221616', title: '#ffb4b4', meta: '#f1d3d3' },
    offline: { border: '#6b7280', bg1: '#24272d', bg2: '#1d2025', title: '#d5d7dc', meta: '#c9ced6' }
  };

  function applyTone(level) {
    var tone = palette[level] || palette.offline;
    root.style.borderLeftColor = tone.border;
    root.style.background = 'linear-gradient(180deg,' + tone.bg1 + ' 0%,' + tone.bg2 + ' 100%)';
    title.style.color = tone.title;
    meta.style.color = tone.meta;
  }

  function refreshOverview() {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', '/cgi-bin/luci/admin/services/jxnu_srun/status?_=' + Date.now(), true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      if (xhr.status !== 200) {
        applyTone('offline');
        title.textContent = '状态读取失败';
        meta.innerHTML = '<span>WiFi: --</span><span>模式: --</span><span>连通性: --</span>';
        return;
      }
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        var level = (typeof data.connectivity_level === 'string' && data.connectivity_level !== '') ? data.connectivity_level : 'offline';
        if (data.enabled === false) {
          level = 'limited';
        }
        var status = (typeof data.status === 'string' && data.status !== '') ? data.status : '未知';
        var ssid = (typeof data.current_ssid === 'string' && data.current_ssid !== '') ? data.current_ssid : '未连接';
        var mode = (typeof data.mode_label === 'string' && data.mode_label !== '') ? data.mode_label : '未知模式';
        var conn = (typeof data.connectivity === 'string' && data.connectivity !== '') ? data.connectivity : '未知';
        var iface = (typeof data.current_iface === 'string' && data.current_iface !== '') ? data.current_iface : '--';
        var ip = (typeof data.current_ip === 'string' && data.current_ip !== '') ? data.current_ip : '--';
        var pending = (typeof data.pending_action === 'string' && data.pending_action !== '') ? ('；待执行动作: ' + data.pending_action) : '';

        applyTone(level);
        title.textContent = status + pending;
        meta.innerHTML = '<span>WiFi: ' + ssid + '</span><span>模式: ' + mode + '</span><span>连通性: ' + conn + '</span><span>接口/IP: ' + iface + ' / ' + ip + '</span>';
      } catch (e) {
        applyTone('offline');
        title.textContent = '状态读取失败';
        meta.innerHTML = '<span>WiFi: --</span><span>模式: --</span><span>连通性: --</span>';
      }
    };
    xhr.send(null);
  }

  refreshOverview();
  setInterval(refreshOverview, 3000);
})();
</script>
]]
end

s = m:section(NamedSection, "main", "main", "配置")
s.addremove = false
s.anonymous = true
s:tab("basic", "基础设置")
s:tab("advanced", "进阶设置")
if cfg.developer_mode == "1" then
    s:tab("developer", "开发者调试")
end
s:tab("log", "日志")

login_now = s:taboption("basic", Button, "_login_now", "立即登录")
login_now.inputstyle = "apply"
function login_now.write()
    local out, err = run_client("--once", true)
    if err then
        m.message = "手动登录结果: " .. err
        return
    end

    local line = last_nonempty_line(out)
    if line == "" then
        line = "已触发登录"
    end
    m.message = "手动登录结果: " .. line
end

enabled = s:taboption("basic", Flag, "enabled", "启用")
bind_flag(enabled, "enabled")

user_id = s:taboption("basic", Value, "user_id", "学工号")
user_id.datatype = "uinteger"
user_id.rmempty = false
bind_text(user_id, "user_id")

operator = s:taboption("basic", ListValue, "operator", "运营商")
operator:value("cmcc", "中国移动")
operator:value("ctcc", "中国电信")
operator:value("cucc", "中国联通")
operator:value("xn", "校内网")
operator.default = "cucc"
operator.rmempty = false
function operator.cfgvalue()
    return cfg.operator or "cucc"
end
function operator.write(self, section, value)
    local v = util.trim(value or ""):lower()
    if v ~= "cmcc" and v ~= "ctcc" and v ~= "cucc" and v ~= "xn" then
        v = "cucc"
    end
    set_value("operator", v)
end

password = s:taboption("basic", Value, "password", "密码")
password.password = true
password.rmempty = false
bind_text(password, "password")

failover_enabled = s:taboption("basic", Flag, "failover_enabled", "夜间模式自动切换热点SSID")
bind_flag(failover_enabled, "failover_enabled")

hotspot_ssid = s:taboption("basic", Value, "hotspot_ssid", "热点SSID", "手动输入要切换的热点 SSID")
hotspot_ssid.placeholder = "例如: iPhone_15"
function hotspot_ssid.validate(self, value)
    local enabled_now = cfg.failover_enabled == "1"
    local v = util.trim(value or "")
    if enabled_now and v == "" then
        return nil, "启用夜间模式切换时必须填写热点SSID"
    end
    return v
end
bind_text(hotspot_ssid, "hotspot_ssid")

hotspot_encryption = s:taboption("basic", ListValue, "hotspot_encryption", "热点加密方式")
hotspot_encryption:value("none", "开放网络 (none)")
hotspot_encryption:value("psk", "WPA-PSK (psk)")
hotspot_encryption:value("psk2", "WPA2-PSK (psk2)")
hotspot_encryption:value("psk-mixed", "WPA/WPA2-PSK (psk-mixed)")
hotspot_encryption:value("sae", "WPA3-SAE (sae)")
hotspot_encryption:value("sae-mixed", "WPA2/WPA3-SAE (sae-mixed)")
function hotspot_encryption.cfgvalue()
    return cfg.hotspot_encryption or "psk2"
end
function hotspot_encryption.write(self, section, value)
    set_value("hotspot_encryption", util.trim(value or "psk2"):lower())
end

hotspot_key = s:taboption("basic", Value, "hotspot_key", "热点密码")
hotspot_key.password = true
function hotspot_key.validate(self, value)
    local enc = util.trim(cfg.hotspot_encryption or "none"):lower()
    local v = util.trim(value or "")
    if cfg.failover_enabled == "1" and enc ~= "none" and v == "" then
        return nil, "热点加密方式非 none 时必须填写热点密码"
    end
    return v
end
bind_text(hotspot_key, "hotspot_key")

hotspot_radio = s:taboption("basic", ListValue, "hotspot_radio", "热点所在频段", "留空时沿用当前 STA；如果热点固定在另一个频段，请手动指定对应频段")
hotspot_radio:value("", "自动")
for radio, meta in pairs(RADIO_CHOICES) do
    hotspot_radio:value(radio, meta.label)
end
function hotspot_radio.cfgvalue()
    return cfg.hotspot_radio or ""
end
function hotspot_radio.write(self, section, value)
    local v = util.trim(value or "")
    if v ~= "" and not RADIO_CHOICES[v] then
        v = ""
    end
    set_value("hotspot_radio", v)
end

quiet_hours_enabled = s:taboption("advanced", Flag, "quiet_hours_enabled", "按时段自动上/下线", quiet_desc)
bind_flag(quiet_hours_enabled, "quiet_hours_enabled")

quiet_start = s:taboption("advanced", Value, "quiet_start", "下线时间（北京时间 HH:MM）")
quiet_start.default = "00:00"
function quiet_start.validate(self, value)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end
bind_text(quiet_start, "quiet_start", validate_hhmm)

quiet_end = s:taboption("advanced", Value, "quiet_end", "上线时间（北京时间 HH:MM）")
quiet_end.default = "06:00"
function quiet_end.validate(self, value)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end
bind_text(quiet_end, "quiet_end", validate_hhmm)

force_logout_in_quiet = s:taboption("advanced", Flag, "force_logout_in_quiet", "进入下线时段时强制下线")
bind_flag(force_logout_in_quiet, "force_logout_in_quiet")

developer_mode = s:taboption("advanced", Flag, "developer_mode", "开发者选项")
bind_flag(developer_mode, "developer_mode")

backoff_enable = s:taboption("advanced", Flag, "backoff_enable", "登录失败时启用退避重试")
bind_flag(backoff_enable, "backoff_enable")

backoff_max_retries = s:taboption("advanced", Value, "backoff_max_retries", "最大重试次数（0 为无限）")
backoff_max_retries.datatype = "uinteger"
bind_text(backoff_max_retries, "backoff_max_retries")

backoff_initial_duration = s:taboption("advanced", Value, "backoff_initial_duration", "初始等待时长（秒）")
function backoff_initial_duration.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "初始等待时长必须是大于等于 0 的数字"
end
bind_text(backoff_initial_duration, "backoff_initial_duration", validate_non_negative_number)

backoff_max_duration = s:taboption("advanced", Value, "backoff_max_duration", "最大等待时长（秒）")
function backoff_max_duration.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "最大等待时长必须是大于等于 0 的数字"
end
bind_text(backoff_max_duration, "backoff_max_duration", validate_non_negative_number)

backoff_exponent_factor = s:taboption("advanced", Value, "backoff_exponent_factor", "指数因子")
function backoff_exponent_factor.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "指数因子必须是大于等于 0 的数字"
end
bind_text(backoff_exponent_factor, "backoff_exponent_factor", validate_non_negative_number)

backoff_inter_const_factor = s:taboption("advanced", Value, "backoff_inter_const_factor", "内常数因子（秒）")
function backoff_inter_const_factor.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "内常数因子必须是大于等于 0 的数字"
end
bind_text(backoff_inter_const_factor, "backoff_inter_const_factor", validate_non_negative_number)

backoff_outer_const_factor = s:taboption("advanced", Value, "backoff_outer_const_factor", "外常数因子（秒）")
function backoff_outer_const_factor.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "外常数因子必须是大于等于 0 的数字"
end
bind_text(backoff_outer_const_factor, "backoff_outer_const_factor", validate_non_negative_number)

interval = s:taboption("advanced", Value, "interval", "检测间隔（秒）")
interval.datatype = "uinteger"
bind_text(interval, "interval")

if cfg.developer_mode == "1" then
    switch_test = s:taboption("developer", DummyValue, "_switch_test", "切网测试")
    switch_test.rawhtml = true
    function switch_test.cfgvalue()
        return [[
<div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
  <button id="jxnu-srun-switch-hotspot" type="button" class="cbi-button cbi-button-apply">测试切到热点</button>
  <button id="jxnu-srun-switch-campus" type="button" class="cbi-button">测试切回校园网</button>
  <span id="jxnu-srun-switch-result" style="color:#666;">点击后会异步执行</span>
</div>
<script type="text/javascript">
(function() {
  var hotspot = document.getElementById('jxnu-srun-switch-hotspot');
  var campus = document.getElementById('jxnu-srun-switch-campus');
  var result = document.getElementById('jxnu-srun-switch-result');
  if (!hotspot || !campus || !result || window.__jxnuSrunSwitchInit) return;
  window.__jxnuSrunSwitchInit = true;

  function enqueue(action) {
    result.textContent = '正在提交...';
    hotspot.disabled = true;
    campus.disabled = true;

    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/jxnu_srun/enqueue', true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      hotspot.disabled = false;
      campus.disabled = false;
      if (xhr.status !== 200) {
        result.textContent = '提交失败';
        return;
      }
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        result.textContent = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
      } catch (e) {
        result.textContent = '提交失败';
      }
    };
    xhr.send('action=' + encodeURIComponent(action));
  }

  hotspot.addEventListener('click', function() { enqueue('switch_hotspot'); });
  campus.addEventListener('click', function() { enqueue('switch_campus'); });
})();
</script>
]]
    end
end

log_text = s:taboption("log", DummyValue, "_log_text", "运行日志")
log_text.rawhtml = true
function log_text.cfgvalue(self, section)
    local t = sys.exec("tail -n 80 /var/log/jxnu_srun.log 2>/dev/null") or ""
    if t == "" then
        t = "暂无日志"
    end

    local escaped = util.pcdata and util.pcdata(t) or t
    return [[
<div style="display:flex;justify-content:flex-end;align-items:center;margin-bottom:6px;">
  <button id="jxnu-srun-refresh-toggle" type="button" class="cbi-button cbi-button-apply">刷新: 开</button>
</div>
<div id="jxnu-srun-log-box" style="max-height:420px;overflow:auto;border:1px solid #2b2b2b;padding:10px;background:#0b0f14;border-radius:4px;">
  <pre id="jxnu-srun-log-pre" style="margin:0;white-space:pre-wrap;word-break:break-all;color:#9ef19e;font-family:monospace;line-height:1.35;">]] .. escaped .. [[</pre>
</div>
<script type="text/javascript">
(function() {
  var box = document.getElementById('jxnu-srun-log-box');
  var pre = document.getElementById('jxnu-srun-log-pre');
  var toggle = document.getElementById('jxnu-srun-refresh-toggle');
  if (!box || !pre || !toggle || window.__jxnuSrunLogInit) return;
  window.__jxnuSrunLogInit = true;
  var autoRefresh = true;
  var timer = null;

  function atBottom() {
    return (box.scrollHeight - box.scrollTop - box.clientHeight) < 24;
  }
  function stickBottom() {
    box.scrollTop = box.scrollHeight;
  }
  function setToggleText() {
    toggle.textContent = autoRefresh ? '刷新: 开' : '刷新: 关';
  }
  function refresh() {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', '/cgi-bin/luci/admin/services/jxnu_srun/log_tail?lines=80&_=' + Date.now(), true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4 || xhr.status !== 200) return;
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        if (typeof data.log !== 'string') return;
        var keepBottom = atBottom();
        pre.textContent = data.log;
        if (keepBottom) stickBottom();
      } catch (e) {}
    };
    xhr.send(null);
  }
  function startLoop() {
    if (timer) clearInterval(timer);
    timer = setInterval(function() {
      if (autoRefresh) refresh();
    }, 2000);
  }
  toggle.addEventListener('click', function() {
    autoRefresh = !autoRefresh;
    setToggleText();
    if (autoRefresh) refresh();
  });

  setToggleText();
  stickBottom();
  startLoop();
})();
</script>
]]
end

function m.parse(self, ...)
    changed = false
    Map.parse(self, ...)
    if changed then
        save_cfg(cfg)
        m.uci:set("jxnu_srun", "main", "_stamp", tostring(os.time()))
        m.message = (m.message and (m.message .. "；") or "") .. "配置已保存到 JSON"
    end
end

function m.on_before_commit(self)
    sys.call("(sleep 1; /etc/init.d/jxnu_srun restart >/dev/null 2>&1) >/dev/null 2>&1 &")
end

return m
