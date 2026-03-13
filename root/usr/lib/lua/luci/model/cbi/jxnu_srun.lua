local fs = require "nixio.fs"
local sys = require "luci.sys"
local util = require "luci.util"
local jsonc = require "luci.jsonc"

local CONFIG_FILE = "/usr/lib/jxnu_srun/config.json"
local STATE_FILE = "/var/run/jxnu_srun/state.json"

-- 全局标量字段名
local GLOBAL_SCALAR_KEYS = {
    "enabled", "quiet_hours_enabled", "quiet_start", "quiet_end",
    "force_logout_in_quiet", "failover_enabled", "backoff_enable",
    "backoff_max_retries", "backoff_initial_duration", "backoff_max_duration",
    "retry_cooldown_seconds", "retry_max_cooldown_seconds",
    "switch_ready_timeout_seconds", "manual_terminal_check_max_attempts",
    "manual_terminal_check_interval_seconds", "hotspot_failback_enabled",
    "connectivity_check_mode",
    "backoff_exponent_factor", "backoff_inter_const_factor",
    "backoff_outer_const_factor", "interval", "developer_mode",
    "sta_iface", "n", "type", "enc",
}
-- 指针字段名
local POINTER_KEYS = {
    "active_campus_id", "default_campus_id",
    "active_hotspot_id", "default_hotspot_id",
}
-- 列表字段名
local LIST_KEYS = { "campus_accounts", "hotspot_profiles" }
-- 标量默认值
local SCALAR_DEFAULTS = {
    enabled = "0", quiet_hours_enabled = "1",
    quiet_start = "00:00", quiet_end = "06:00",
    force_logout_in_quiet = "1", failover_enabled = "1",
    backoff_enable = "1", backoff_max_retries = "0",
    backoff_initial_duration = "10", backoff_max_duration = "600",
    retry_cooldown_seconds = "10", retry_max_cooldown_seconds = "600",
    switch_ready_timeout_seconds = "12",
    manual_terminal_check_max_attempts = "5",
    manual_terminal_check_interval_seconds = "2",
    hotspot_failback_enabled = "1",
    connectivity_check_mode = "internet",
    backoff_exponent_factor = "1.5", backoff_inter_const_factor = "0",
    backoff_outer_const_factor = "0", interval = "60",
    developer_mode = "0", sta_iface = "",
    n = "200", ["type"] = "1", enc = "srun_bx1",
}
-- 旧版字段（用于迁移检测）
local LEGACY_CAMPUS_KEYS = {
    "user_id", "operator", "password", "base_url", "ac_id",
    "campus_ssid", "campus_encryption", "campus_key",
}

local function ensure_json_file()
    local dir = CONFIG_FILE:match("^(.+)/[^/]+$")
    if dir and not fs.access(dir) then
        fs.mkdirr(dir)
    end
    if not fs.access(CONFIG_FILE) then
        fs.writefile(CONFIG_FILE, "{}\n")
    end
end

local function is_legacy_config(parsed)
    if type(parsed.campus_accounts) == "table" then return false end
    for _, k in ipairs(LEGACY_CAMPUS_KEYS) do
        if parsed[k] ~= nil then return true end
    end
    return false
end

local function migrate_legacy_config(parsed)
    local migrated = {}
    for _, key in ipairs(GLOBAL_SCALAR_KEYS) do
        migrated[key] = parsed[key] ~= nil and tostring(parsed[key]) or (SCALAR_DEFAULTS[key] or "")
    end
    local uid = tostring(parsed.user_id or ""):match("^%s*(.-)%s*$")
    local op = tostring(parsed.operator or "cucc"):match("^%s*(.-)%s*$"):lower()
    local ca = {
        id = "campus-1", label = "",
        base_url = tostring(parsed.base_url or "http://172.17.1.2"):match("^%s*(.-)%s*$"),
        ac_id = tostring(parsed.ac_id or "1"):match("^%s*(.-)%s*$"),
        user_id = uid, password = tostring(parsed.password or ""):match("^%s*(.-)%s*$"),
        operator = op,
        ssid = tostring(parsed.campus_ssid or "jxnu_stu"):match("^%s*(.-)%s*$"),
        bssid = tostring(parsed.campus_bssid or ""):match("^%s*(.-)%s*$"),
    }
    ca.label = (uid ~= "" and op ~= "" and op ~= "xn") and (uid .. "@" .. op) or (uid ~= "" and uid or "未命名账号")
    migrated.campus_accounts = uid ~= "" and { ca } or {}
    migrated.active_campus_id = uid ~= "" and "campus-1" or ""
    migrated.default_campus_id = migrated.active_campus_id

    local hssid = tostring(parsed.hotspot_ssid or ""):match("^%s*(.-)%s*$")
    local hp = {
        id = "hotspot-1", label = hssid ~= "" and hssid or "未命名热点",
        ssid = hssid,
        encryption = tostring(parsed.hotspot_encryption or "psk2"):match("^%s*(.-)%s*$"):lower(),
        key = tostring(parsed.hotspot_key or ""):match("^%s*(.-)%s*$"),
        radio = tostring(parsed.hotspot_radio or ""):match("^%s*(.-)%s*$"),
    }
    migrated.hotspot_profiles = hssid ~= "" and { hp } or {}
    migrated.active_hotspot_id = hssid ~= "" and "hotspot-1" or ""
    migrated.default_hotspot_id = migrated.active_hotspot_id
    return migrated
end

local function next_id(items, prefix)
    local max_num = 0
    if type(items) == "table" then
        for _, item in ipairs(items) do
            local ns = tostring(item.id or ""):match("^" .. prefix .. "%-(%d+)$")
            if ns then local n = tonumber(ns); if n and n > max_num then max_num = n end end
        end
    end
    return prefix .. "-" .. (max_num + 1)
end

local function load_cfg()
    ensure_json_file()
    local raw = fs.readfile(CONFIG_FILE) or "{}"
    local parsed = jsonc.parse(raw)
    if type(parsed) ~= "table" then parsed = {} end
    if is_legacy_config(parsed) then
        parsed = migrate_legacy_config(parsed)
        local j = jsonc.stringify(parsed)
        if j then fs.writefile(CONFIG_FILE, j .. "\n") end
    end
    local cfg = {}
    for _, key in ipairs(GLOBAL_SCALAR_KEYS) do
        cfg[key] = parsed[key] ~= nil and tostring(parsed[key]) or (SCALAR_DEFAULTS[key] or "")
    end
    for _, key in ipairs(POINTER_KEYS) do
        cfg[key] = tostring(parsed[key] or "")
    end
    for _, key in ipairs(LIST_KEYS) do
        cfg[key] = type(parsed[key]) == "table" and parsed[key] or {}
    end
    return cfg
end

local function save_cfg(cfg)
    local out = {}
    for _, key in ipairs(GLOBAL_SCALAR_KEYS) do
        out[key] = tostring(cfg[key] or SCALAR_DEFAULTS[key] or "")
    end
    for _, key in ipairs(POINTER_KEYS) do
        out[key] = tostring(cfg[key] or "")
    end
    for _, key in ipairs(LIST_KEYS) do
        out[key] = type(cfg[key]) == "table" and cfg[key] or {}
    end
    ensure_json_file()
    fs.writefile(CONFIG_FILE, (jsonc.stringify(out) or "{}") .. "\n")
end

local function load_state()
    local raw = fs.readfile(STATE_FILE) or "{}"
    local parsed = jsonc.parse(raw)
    return type(parsed) == "table" and parsed or {}
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
<div id="jxnu-srun-overview" style="margin:4px 0 18px 0;border-left:4px solid #c62828;background:rgba(128,128,128,.08);padding:14px 16px;border-radius:0 6px 6px 0;box-shadow:none;">
  <div id="jxnu-srun-overview-title" style="font-size:18px;font-weight:700;color:#1f2937;margin-bottom:8px;">状态读取中</div>
  <div id="jxnu-srun-overview-meta" style="font-size:13px;color:#374151;display:flex;gap:14px;flex-wrap:wrap;line-height:1.6;">
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
    online: { border: '#2e7d32', bg: 'rgba(46,125,50,.10)', title: '#166534', meta: '#166534' },
    portal: { border: '#ef6c00', bg: 'rgba(239,108,0,.10)', title: '#b45309', meta: '#92400e' },
    limited: { border: '#c62828', bg: 'rgba(198,40,40,.10)', title: '#b91c1c', meta: '#991b1b' },
    offline: { border: '#6b7280', bg: 'rgba(107,114,128,.10)', title: '#374151', meta: '#4b5563' }
  };

  function applyTone(level) {
    var tone = palette[level] || palette.offline;
    root.style.borderLeftColor = tone.border;
    root.style.background = tone.bg;
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
        var status = (typeof data.status === 'string' && data.status !== '') ? data.status : '未知';
        var ssid = (typeof data.current_ssid === 'string' && data.current_ssid !== '') ? data.current_ssid : '未连接';
        var mode = (typeof data.mode_label === 'string' && data.mode_label !== '') ? data.mode_label : '未知模式';
        var conn = (typeof data.connectivity === 'string' && data.connectivity !== '') ? data.connectivity : '未知';
        var iface = (typeof data.current_iface === 'string' && data.current_iface !== '') ? data.current_iface : '--';
        var ip = (typeof data.current_ip === 'string' && data.current_ip !== '') ? data.current_ip : '--';
        var pending = (typeof data.pending_action === 'string' && data.pending_action !== '') ? ('；待执行动作: ' + data.pending_action) : '';
        var campusLabel = (typeof data.online_account_label === 'string' && data.online_account_label !== '') ? data.online_account_label : ((typeof data.campus_account_label === 'string' && data.campus_account_label !== '') ? data.campus_account_label : '--');
        var hotspotLabel = (typeof data.hotspot_profile_label === 'string' && data.hotspot_profile_label !== '') ? data.hotspot_profile_label : '--';

        applyTone(level);
        title.textContent = status + pending;
        var metaHtml = '<span>WiFi: ' + ssid + '</span><span>模式: ' + mode + '</span><span>连通性: ' + conn + '</span><span>接口/IP: ' + iface + ' / ' + ip + '</span>';
        if (mode === '热点模式') {
          metaHtml += '<span>热点: ' + hotspotLabel + '</span>';
        } else {
          metaHtml += '<span>账号: ' + campusLabel + '</span>';
        }
        meta.innerHTML = metaHtml;
      } catch (e) {
        applyTone('offline');
        title.textContent = '状态读取失败';
        meta.innerHTML = '<span>WiFi: --</span><span>模式: --</span><span>连通性: --</span>';
      }
    };
    xhr.send(null);
  }

  refreshOverview();
  setInterval(refreshOverview, 1200);
})();
</script>
]]
end

s = m:section(NamedSection, "main", "main", "配置")
s.addremove = false
s.anonymous = true
s:tab("basic", "基础设置")
s:tab("advanced", "进阶设置")
s:tab("log", "日志")

manual_login = s:taboption("basic", DummyValue, "_manual_login", "手动登录")
manual_login.rawhtml = true
function manual_login.cfgvalue()
    return [[
<div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
  <button id="jxnu-srun-manual-login" type="button" class="cbi-button cbi-button-apply">立即登录</button>
  <button id="jxnu-srun-manual-logout" type="button" class="cbi-button cbi-button-reset">立即登出</button>
  <span id="jxnu-srun-manual-result" style="color:#666;"></span>
</div>
<script type="text/javascript">
(function() {
  var login = document.getElementById('jxnu-srun-manual-login');
  var logout = document.getElementById('jxnu-srun-manual-logout');
  var result = document.getElementById('jxnu-srun-manual-result');
  if (!login || !logout || !result || window.__jxnuSrunManualInit) return;
  window.__jxnuSrunManualInit = true;

  function fetchJson(url, callback) {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      if (xhr.status !== 200) {
        callback(new Error('http_' + xhr.status));
        return;
      }
      try {
        callback(null, JSON.parse(xhr.responseText || '{}'));
      } catch (e) {
        callback(e);
      }
    };
    xhr.send(null);
  }

  window.jxnuFetchJson = fetchJson;

  function openBlockingFeedback(action, requestedAt) {
    var logBox = E('pre', {
      'style': 'max-height:18rem;overflow:auto;margin:0;padding:.75rem;border:1px solid rgba(127,127,127,.28);background:rgba(127,127,127,.08);white-space:pre-wrap;word-break:break-word;'
    }, '等待后端反馈...');
    var titles = {
      manual_login: '正在登录',
      manual_logout: '正在登出',
      switch_hotspot: '正在切到热点',
      switch_campus: '正在切回校园网'
    };
    var tips = {
      manual_login: '正在执行登录流程，请勿关闭页面。',
      manual_logout: '正在执行登出流程，请稍候。',
      switch_hotspot: '正在切换到热点网络，请稍候。',
      switch_campus: '正在切换回校园网，请稍候。'
    };
    var tip = E('p', { 'style': 'margin:.5rem 0 1rem 0;' }, tips[action] || '正在执行网络动作，请稍候。');
    var footer = E('div', { 'class': 'right' });
    var closed = false;
    var timer = null;
    var forceShown = false;

    function showForceStopButton() {
      if (closed || forceShown) return;
      forceShown = true;
      footer.appendChild(E('button', {
        'class': 'btn cbi-button cbi-button-remove',
        'click': function(ev) {
          ev.preventDefault();
          this.disabled = true;
          result.textContent = '正在强制停止...';
          var xhr = new XMLHttpRequest();
          xhr.open('POST', '/cgi-bin/luci/admin/services/jxnu_srun/enqueue', true);
          xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
          xhr.onreadystatechange = function() {
            if (xhr.readyState !== 4) return;
            var text = '已触发强制停止';
            if (xhr.status === 200) {
              try {
                var data = JSON.parse(xhr.responseText || '{}');
                if (typeof data.message === 'string' && data.message !== '')
                  text = data.message;
              } catch (e) {}
            }
            unlock(text, false);
          };
          xhr.send('action=' + encodeURIComponent('force_stop'));
        }
      }, '强制停止'));
    }

    function unlock(text, success) {
      if (closed) return;
      closed = true;
      if (timer) window.clearInterval(timer);
      while (footer.firstChild) footer.removeChild(footer.firstChild);
      footer.appendChild(E('button', {
        'class': 'btn cbi-button',
        'click': function() { L.hideModal(); }
      }, '关闭返回'));
      if (text) result.textContent = text + (success ? ' 🎉' : ' ⚠');
      window.setTimeout(function() { window.location.reload(); }, 1200);
    }

    function checkTerminal(statusData) {
      if (!statusData) return false;
      if (statusData.last_action !== action) return false;
      if ((statusData.last_action_ts || 0) < requestedAt) return false;
      if (statusData.action_result === 'forced') {
        unlock(statusData.status || '已强制停止', false);
        return true;
      }
      if (statusData.action_result === 'error') {
        unlock(statusData.status || '执行失败', false);
        return true;
      }

      if (action === 'manual_login') {
        var ssidOk = !!statusData.current_ssid && statusData.current_ssid === statusData.campus_ssid;
        var bssidOk = !statusData.campus_bssid || statusData.current_bssid === statusData.campus_bssid;
        var onlineOk = statusData.connectivity_level === 'online';
        if (statusData.action_result === 'ok' && ssidOk && bssidOk && onlineOk) {
          unlock(statusData.status || '登录成功', true);
          return true;
        }
      }

      if (action === 'manual_logout') {
        if (statusData.action_result === 'ok' && statusData.connectivity_level !== 'online') {
          unlock(statusData.status || '登出成功', true);
          return true;
        }
      }

      if (action === 'switch_hotspot') {
        if (statusData.action_result === 'ok') {
          unlock(statusData.status || '已切到热点', true);
          return true;
        }
      }

      if (action === 'switch_campus') {
        if (statusData.action_result === 'ok') {
          unlock(statusData.status || '已切回校园网', true);
          return true;
        }
      }

      return false;
    }

    function poll() {
      if (!closed && requestedAt > 0 && ((Date.now() / 1000) - requestedAt) >= 10) {
        showForceStopButton();
      }

      fetchJson('/cgi-bin/luci/admin/services/jxnu_srun/log_tail?lines=200&since=' + encodeURIComponent(requestedAt) + '&_=' + Date.now(), function(err, logData) {
        if (!err && logData && typeof logData.log === 'string') {
          logBox.textContent = logData.log;
          logBox.scrollTop = logBox.scrollHeight;
        }
      });

      fetchJson('/cgi-bin/luci/admin/services/jxnu_srun/status?_=' + Date.now(), function(err, statusData) {
        if (err) return;
        checkTerminal(statusData);
      });
    }

    L.showModal(titles[action] || '正在执行动作', [ tip, logBox, footer ], 'cbi-modal');
    timer = window.setInterval(poll, 1000);
    poll();
  }

  window.jxnuOpenBlockingFeedback = openBlockingFeedback;

  function submit(action) {
    result.textContent = '正在提交...';
    login.disabled = true;
    logout.disabled = true;

    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/jxnu_srun/enqueue', true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      login.disabled = false;
      logout.disabled = false;
      if (xhr.status !== 200) {
        result.textContent = '提交失败';
        return;
      }
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
        result.textContent = message;
        if (data.ok) {
          openBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
        }
      } catch (e) {
        result.textContent = '提交失败';
      }
    };
    xhr.send('action=' + encodeURIComponent(action));
  }

  login.addEventListener('click', function() { submit('manual_login'); });
  logout.addEventListener('click', function() { submit('manual_logout'); });
})();
</script>
]]
end

enabled = s:taboption("basic", Flag, "enabled", "启用")
enabled.description = "仅控制后台自动登录守护服务（自动检测、自动重连、按时段自动上下线/切网）。手动登录和手动登出始终可用，不受此开关影响。"
bind_flag(enabled, "enabled")

quiet_desc = "当前下线/上线时间：" .. tostring(cfg.quiet_start or "00:00") .. " / " .. tostring(cfg.quiet_end or "06:00")

quiet_hours_enabled = s:taboption("basic", Flag, "quiet_hours_enabled", "按时段自动上/下线", quiet_desc)
bind_flag(quiet_hours_enabled, "quiet_hours_enabled")

quiet_start = s:taboption("basic", Value, "quiet_start", "下线时间（北京时间 HH:MM）")
quiet_start.default = "00:00"
function quiet_start.validate(self, value)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end
bind_text(quiet_start, "quiet_start", validate_hhmm)

quiet_end = s:taboption("basic", Value, "quiet_end", "上线时间（北京时间 HH:MM）")
quiet_end.default = "06:00"
function quiet_end.validate(self, value)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end
bind_text(quiet_end, "quiet_end", validate_hhmm)

force_logout_in_quiet = s:taboption("basic", Flag, "force_logout_in_quiet", "进入下线时段时强制下线")
bind_flag(force_logout_in_quiet, "force_logout_in_quiet")

failover_enabled = s:taboption("basic", Flag, "failover_enabled", "登出时自动切换热点上网")
bind_flag(failover_enabled, "failover_enabled")

switch_test = s:taboption("basic", DummyValue, "_switch_test", "手动切换网络")
switch_test.rawhtml = true
function switch_test.cfgvalue()
    return [[
<div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
  <button id="jxnu-srun-switch-hotspot" type="button" class="cbi-button cbi-button-apply">切到热点</button>
  <button id="jxnu-srun-switch-campus" type="button" class="cbi-button cbi-button-apply">切回校园网</button>
  <span id="jxnu-srun-switch-result" style="color:#666;"></span>
</div>
<div class="cbi-value-description">手动切网会停用自动登录服务，如需启用请再次手动开启。</div>
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
        var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
        result.textContent = message;
        if (data.ok && typeof window.jxnuOpenBlockingFeedback === 'function') {
          window.jxnuOpenBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
        }
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

-- 校园网账号表格 + 热点配置表格 + 弹窗
tables_html = s:taboption("basic", DummyValue, "_tables", "")
tables_html.rawhtml = true
function tables_html.cfgvalue()
    local campus = cfg.campus_accounts or {}
    local hotspots = cfg.hotspot_profiles or {}
    local state = load_state()
    local active_cid = cfg.active_campus_id or ""
    local default_cid = cfg.default_campus_id or ""
    local active_hid = cfg.active_hotspot_id or ""
    local default_hid = cfg.default_hotspot_id or ""
    local current_mode = tostring(state.current_mode or "")
    local online_account_label = tostring(state.online_account_label or "")
    local current_ssid = tostring(state.current_ssid or "")
    local current_bssid = tostring(state.current_bssid or "")
    local current_iface = tostring(state.current_iface or "")
    local current_campus_access_mode = tostring(state.current_campus_access_mode or "")

    local operator_labels = { cmcc = "移动", ctcc = "电信", cucc = "联通", xn = "校内网" }
    local radio_labels = { [""] = "自动" }
    local radio_options = '<option value="">自动</option>'
    for radio, meta in pairs(RADIO_CHOICES) do
        local label = tostring(meta.label or radio)
        radio_labels[radio] = label
        radio_options = radio_options .. '<option value="' .. util.pcdata(radio) .. '">' .. util.pcdata(label) .. '</option>'
    end

    -- 构建校园网账号表格行
    local campus_rows = ""
    if type(campus) == "table" then
        for _, a in ipairs(campus) do
            local aid = tostring(a.id or "")
            local campus_user = tostring(a.user_id or "")
            local campus_ssid = tostring(a.ssid or "")
            local campus_bssid = tostring(a.bssid or ""):lower()
            local access_mode = tostring(a.access_mode or "wifi")
            local ssid_display = access_mode == "wired" and "有线" or tostring(a.ssid or "jxnu_stu")
            local is_active = (aid == active_cid)
            local is_default = (aid == default_cid)
            local wifi_match = current_mode == "campus"
                and current_campus_access_mode == "wifi"
                and campus_ssid ~= ""
                and current_ssid == campus_ssid
                and ((campus_bssid == "") or (current_bssid == campus_bssid))
            local wired_match = current_mode == "campus"
                and current_campus_access_mode == "wired"
                and access_mode == "wired"
                and current_iface == "wan"
            local identity_match = campus_user ~= "" and online_account_label == campus_user
            local is_connected = false
            if access_mode == "wired" then
                is_connected = wired_match and identity_match
            else
                is_connected = wifi_match and identity_match
            end
            local badge_parts = {}
            if is_connected then
                badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#16a34a;font-weight:700;">已连接</span>'
            end
            if is_default then
                if is_active and not is_connected then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#2563eb;font-weight:700;">默认</span>'
                elseif not is_active then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#d97706;font-weight:700;">待生效</span>'
                end
            else
                badge_parts[#badge_parts + 1] = ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"jxnuSetDefault('campus','%s')\">设默认</button>"):format(util.pcdata(aid))
            end
            local badge = table.concat(badge_parts, '<br>')
            campus_rows = campus_rows .. '<tr class="tr">'
                .. '<td class="td">' .. badge .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.label or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.base_url or "http://172.17.1.2")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.ac_id or "1")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.user_id or "")) .. '</td>'
                .. '<td class="td">' .. (operator_labels[tostring(a.operator or "")] or tostring(a.operator or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(ssid_display) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.bssid or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(radio_labels[tostring(a.radio or "")] or tostring(a.radio or "自动")) .. '</td>'
                .. '<td class="td cbi-section-actions"><div class="jxnu-action-cell">'
                .. ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"jxnuEditCampus('%s')\">编辑</button>"):format(util.pcdata(aid))
                .. ("<button type=\"button\" class=\"cbi-button cbi-button-remove\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"jxnuDelete('campus','%s')\">删除</button>"):format(util.pcdata(aid))
                .. '</div></td></tr>\n'
        end
    end
    if campus_rows == "" then
        campus_rows = '<tr class="tr"><td class="td" colspan="10" style="text-align:center;color:#999;">暂无账号，请点击"新增"添加</td></tr>'
    end

    -- 构建热点配置表格行
    local hotspot_rows = ""
    if type(hotspots) == "table" then
        for _, h in ipairs(hotspots) do
            local hid = tostring(h.id or "")
            local is_active = (hid == active_hid)
            local is_default = (hid == default_hid)
            local hotspot_ssid = tostring(h.ssid or "")
            local is_connected = current_mode == "hotspot" and hotspot_ssid ~= "" and current_ssid == hotspot_ssid
            local badge_parts = {}
            if is_connected then
                badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#16a34a;font-weight:700;">已连接</span>'
            end
            if is_default then
                if is_active and not is_connected then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#2563eb;font-weight:700;">默认</span>'
                elseif not is_active then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#d97706;font-weight:700;">待生效</span>'
                end
            else
                badge_parts[#badge_parts + 1] = ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"jxnuSetDefault('hotspot','%s')\">设默认</button>"):format(util.pcdata(hid))
            end
            local badge = table.concat(badge_parts, '<br>')
            hotspot_rows = hotspot_rows .. '<tr class="tr">'
                .. '<td class="td">' .. badge .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(h.label or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(h.ssid or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(h.encryption or "psk2")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(radio_labels[tostring(h.radio or "")] or tostring(h.radio or "自动")) .. '</td>'
                .. '<td class="td cbi-section-actions"><div class="jxnu-action-cell">'
                .. ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"jxnuEditHotspot('%s')\">编辑</button>"):format(util.pcdata(hid))
                .. ("<button type=\"button\" class=\"cbi-button cbi-button-remove\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"jxnuDelete('hotspot','%s')\">删除</button>"):format(util.pcdata(hid))
                .. '</div></td></tr>\n'
        end
    end
    if hotspot_rows == "" then
        hotspot_rows = '<tr class="tr"><td class="td" colspan="6" style="text-align:center;color:#999;">暂无热点，请点击"新增"添加</td></tr>'
    end

    -- 将数据嵌入到前端
    local campus_json = jsonc.stringify(campus) or "[]"
    local hotspot_json = jsonc.stringify(hotspots) or "[]"

    return [[
<style>
.jxnu-native-box{margin:18px 0;}
.jxnu-native-box h3{margin:0 0 .75rem 0;font-weight:600;}
.jxnu-native-box .cbi-section-table .th,
.jxnu-native-box .cbi-section-table .td{vertical-align:middle;}
.jxnu-native-box .cbi-section-table .td:last-child{white-space:nowrap;}
.jxnu-native-box .cbi-section-table .btn,
.jxnu-native-box .cbi-section-table .cbi-button{vertical-align:middle;}
.jxnu-native-box .cbi-section-actions{white-space:nowrap;text-align:center;}
.jxnu-native-box .jxnu-action-cell{display:inline-flex;align-items:center;justify-content:center;gap:.5rem;width:100%;}
.jxnu-native-box .jxnu-box-actions{padding:.75rem 1rem 0 1rem;}
.jxnu-native-row{margin-bottom:.75rem;}
.jxnu-native-row label{display:block;margin-bottom:.25rem;font-weight:600;}
.jxnu-native-row input,.jxnu-native-row select{width:100%;box-sizing:border-box;}
</style>

<div class="cbi-section cbi-tblsection jxnu-native-box">
  <h3>校园网账号</h3>
  <table class="table cbi-section-table">
    <tr class="tr table-titles"><th class="th" style="width:80px;">状态</th><th class="th">标签</th><th class="th">认证地址</th><th class="th">ACID</th><th class="th">学工号</th><th class="th">运营商</th><th class="th">SSID</th><th class="th">BSSID</th><th class="th">频段</th><th class="th cbi-section-actions" style="width:120px;">操作</th></tr>
    <tbody>]] .. campus_rows .. [[</tbody>
  </table>
  <div class="jxnu-box-actions">
    <button type="button" class="cbi-button cbi-button-add" onclick="jxnuEditCampus('')">新增</button>
  </div>
</div>

<div class="cbi-section cbi-tblsection jxnu-native-box">
  <h3>热点配置</h3>
  <table class="table cbi-section-table">
    <tr class="tr table-titles"><th class="th" style="width:80px;">状态</th><th class="th">标签</th><th class="th">SSID</th><th class="th">加密方式</th><th class="th">频段</th><th class="th cbi-section-actions" style="width:120px;">操作</th></tr>
    <tbody>]] .. hotspot_rows .. [[</tbody>
  </table>
  <div class="jxnu-box-actions">
    <button type="button" class="cbi-button cbi-button-add" onclick="jxnuEditHotspot('')">新增</button>
  </div>
</div>

<script type="text/javascript">
(function() {
if (window.__jxnuTablesInit) return;
window.__jxnuTablesInit = true;

var campusData = ]] .. campus_json .. [[;
var hotspotData = ]] .. hotspot_json .. [[;
var modalType = '';
var modalEditId = '';
var modalSaveHandler = null;

function renderPasswordField(containerId, fieldId, value) {
  var container = document.getElementById(containerId);
  if (!container) return;
  L.require('ui').then(function(ui) {
    var widget = new ui.Textfield(value || '', {
      id: fieldId,
      password: true,
      optional: true
    });
    return Promise.resolve(widget.render()).then(function(node) {
      container.innerHTML = '';
      container.appendChild(node);
    });
  });
}

function getFieldValue(id) {
  var node = document.getElementById('widget.' + id) || document.getElementById(id);
  return node ? node.value : '';
}

function setRowDisabled(rowId, inputId, disabled) {
  var row = document.getElementById(rowId);
  var input = document.getElementById(inputId);
  if (!row || !input) return;
  input.disabled = !!disabled;
  row.style.opacity = disabled ? '0.55' : '1';
}

function updateCampusAccessModeUI() {
  var mode = document.getElementById('jm-access_mode');
  if (!mode) return;
  var wired = mode.value === 'wired';
  setRowDisabled('jm-ssid-row', 'jm-ssid', wired);
  setRowDisabled('jm-bssid-row', 'jm-bssid', wired);
  setRowDisabled('jm-radio-row', 'jm-radio', wired);
}

function showNativeModal(title, bodyHtml, afterOpen, onSave) {
  var body = document.createElement('div');
  body.innerHTML = bodyHtml;

  var buttonRow = document.createElement('div');
  buttonRow.className = 'right';

  var cancelBtn = document.createElement('button');
  cancelBtn.type = 'button';
  cancelBtn.className = 'btn cbi-button';
  cancelBtn.textContent = '取消';
  cancelBtn.onclick = function() { L.hideModal(); };

  var saveBtn = document.createElement('button');
  saveBtn.type = 'button';
  saveBtn.className = 'btn cbi-button cbi-button-save important';
  saveBtn.textContent = '保存';
  saveBtn.onclick = function() {
    if (typeof modalSaveHandler === 'function')
      modalSaveHandler();
  };

  buttonRow.appendChild(cancelBtn);
  buttonRow.appendChild(document.createTextNode(' '));
  buttonRow.appendChild(saveBtn);

  modalSaveHandler = onSave;
  L.showModal(title, [ body, buttonRow ], 'cbi-modal');
  if (typeof afterOpen === 'function')
    afterOpen();
}

window.jxnuSetDefault = function(kind, id) {
  var fd = new FormData();
  fd.append('action', 'set_default_' + kind);
  fd.append('id', id);
  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/cgi-bin/luci/admin/services/jxnu_srun/enqueue', true);
  xhr.onload = function() {
    var message = '已保存默认配置';
    if (xhr.status === 200) {
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        if (typeof data.message === 'string' && data.message !== '')
          message = data.message;
      } catch (e) {}
    }
    alert(message);
    location.reload();
  };
  xhr.send(fd);
};

window.jxnuDelete = function(kind, id) {
  if (!confirm('确定要删除此项吗？')) return;
  var fd = new FormData();
  fd.append('action', 'delete_' + kind);
  fd.append('id', id);
  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/cgi-bin/luci/admin/services/jxnu_srun/enqueue', true);
  xhr.onload = function() { location.reload(); };
  xhr.send(fd);
};

function findById(arr, id) {
  for (var i = 0; i < arr.length; i++) {
    if (arr[i].id === id) return arr[i];
  }
  return null;
}

window.jxnuEditCampus = function(id) {
  modalType = 'campus';
  modalEditId = id;
  var item = id ? findById(campusData, id) : {};
  var bodyHtml =
    '<div class="jxnu-native-row"><label>标签（选填）</label><input id="jm-label" value="' + (item.label || '') + '"></div>' +
    '<div class="jxnu-native-row"><label>学工号</label><input id="jm-user_id" value="' + (item.user_id || '') + '"></div>' +
    '<div class="jxnu-native-row"><label>运营商</label><select id="jm-operator"><option value="cmcc"' + (item.operator==='cmcc'?' selected':'') + '>中国移动</option><option value="ctcc"' + (item.operator==='ctcc'?' selected':'') + '>中国电信</option><option value="cucc"' + (item.operator==='cucc'?' selected':'') + '>中国联通</option><option value="xn"' + (item.operator==='xn'?' selected':'') + '>校内网</option></select></div>' +
    '<div class="jxnu-native-row"><label>接入方式</label><select id="jm-access_mode"><option value="wifi"' + (((item.access_mode || 'wifi')==='wifi')?' selected':'') + '>无线</option><option value="wired"' + ((item.access_mode==='wired')?' selected':'') + '>有线（WAN）</option></select></div>' +
    '<div class="jxnu-native-row"><label>密码</label><div id="jm-password-field"></div></div>' +
    '<div class="jxnu-native-row"><label>认证地址</label><input id="jm-base_url" value="' + (item.base_url || 'http://172.17.1.2') + '"></div>' +
    '<div class="jxnu-native-row"><label>AC_ID</label><input id="jm-ac_id" value="' + (item.ac_id || '1') + '"></div>' +
    '<div class="jxnu-native-row" id="jm-ssid-row"><label>校园网 SSID</label><input id="jm-ssid" value="' + (item.ssid || 'jxnu_stu') + '"></div>' +
    '<div class="jxnu-native-row" id="jm-bssid-row"><label>BSSID（留空则不锁定）</label><input id="jm-bssid" value="' + (item.bssid || '') + '"></div>' +
    '<div class="jxnu-native-row" id="jm-radio-row"><label>频段</label><select id="jm-radio">]] .. radio_options .. [[</select></div>';
  showNativeModal(
    id ? '编辑校园网账号' : '新增校园网账号',
    bodyHtml,
    function() {
      document.getElementById('jm-radio').value = item.radio || '';
      document.getElementById('jm-access_mode').addEventListener('change', updateCampusAccessModeUI);
      updateCampusAccessModeUI();
      renderPasswordField('jm-password-field', 'jm-password', item.password || '');
    },
    function() { jxnuModalSave(); }
  );
};

window.jxnuEditHotspot = function(id) {
  modalType = 'hotspot';
  modalEditId = id;
  var item = id ? findById(hotspotData, id) : {};
  var bodyHtml =
    '<div class="jxnu-native-row"><label>标签（选填）</label><input id="jm-label" value="' + (item.label || '') + '"></div>' +
    '<div class="jxnu-native-row"><label>SSID</label><input id="jm-ssid" value="' + (item.ssid || '') + '"></div>' +
    '<div class="jxnu-native-row"><label>加密方式</label><select id="jm-encryption"><option value="none"' + (item.encryption==='none'?' selected':'') + '>开放(none)</option><option value="psk"' + (item.encryption==='psk'?' selected':'') + '>WPA-PSK</option><option value="psk2"' + ((item.encryption==='psk2'||!item.encryption)?' selected':'') + '>WPA2-PSK</option><option value="psk-mixed"' + (item.encryption==='psk-mixed'?' selected':'') + '>WPA/WPA2</option><option value="sae"' + (item.encryption==='sae'?' selected':'') + '>WPA3-SAE</option><option value="sae-mixed"' + (item.encryption==='sae-mixed'?' selected':'') + '>WPA2/WPA3</option></select></div>' +
    '<div class="jxnu-native-row"><label>密码</label><div id="jm-key-field"></div></div>' +
    '<div class="jxnu-native-row"><label>频段</label><select id="jm-radio">]] .. radio_options .. [[</select></div>';
  showNativeModal(
    id ? '编辑热点配置' : '新增热点配置',
    bodyHtml,
    function() {
      document.getElementById('jm-encryption').value = item.encryption || 'psk2';
      document.getElementById('jm-radio').value = item.radio || '';
      renderPasswordField('jm-key-field', 'jm-key', item.key || '');
    },
    function() { jxnuModalSave(); }
  );
};

window.jxnuModalSave = function() {
  var fd = new FormData();
  fd.append('action', (modalEditId ? 'edit_' : 'add_') + modalType);
  if (modalEditId) fd.append('id', modalEditId);

  if (modalType === 'campus') {
    fd.append('label', document.getElementById('jm-label').value);
    fd.append('user_id', document.getElementById('jm-user_id').value);
    fd.append('operator', document.getElementById('jm-operator').value);
    fd.append('access_mode', document.getElementById('jm-access_mode').value);
    fd.append('password', getFieldValue('jm-password'));
    fd.append('base_url', document.getElementById('jm-base_url').value);
    fd.append('ac_id', document.getElementById('jm-ac_id').value);
    fd.append('ssid', document.getElementById('jm-ssid').value);
    fd.append('bssid', document.getElementById('jm-bssid').value);
    fd.append('radio', document.getElementById('jm-radio').value);
  } else {
    fd.append('label', document.getElementById('jm-label').value);
    fd.append('ssid', document.getElementById('jm-ssid').value);
    fd.append('encryption', document.getElementById('jm-encryption').value);
    fd.append('key', getFieldValue('jm-key'));
    fd.append('radio', document.getElementById('jm-radio').value);
  }

  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/cgi-bin/luci/admin/services/jxnu_srun/enqueue', true);
  xhr.onload = function() {
    L.hideModal();
    location.reload();
  };
  xhr.send(fd);
};
})();
</script>
]]
end

backoff_enable = s:taboption("advanced", Flag, "backoff_enable", "登录失败时启用退避重试")
bind_flag(backoff_enable, "backoff_enable")

backoff_max_retries = s:taboption("advanced", Value, "backoff_max_retries", "最大重试次数（0 为无限）")
backoff_max_retries.datatype = "uinteger"
bind_text(backoff_max_retries, "backoff_max_retries")

retry_cooldown_seconds = s:taboption("advanced", Value, "retry_cooldown_seconds", "失败后首次重试等待（秒）")
function retry_cooldown_seconds.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "首次重试等待时长必须是大于等于 0 的数字"
end
retry_cooldown_seconds.placeholder = "10"
bind_text(retry_cooldown_seconds, "retry_cooldown_seconds", validate_non_negative_number)

retry_max_cooldown_seconds = s:taboption("advanced", Value, "retry_max_cooldown_seconds", "退避等待上限（秒）")
function retry_max_cooldown_seconds.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "退避等待上限必须是大于等于 0 的数字"
end
retry_max_cooldown_seconds.placeholder = "600"
bind_text(retry_max_cooldown_seconds, "retry_max_cooldown_seconds", validate_non_negative_number)

switch_ready_timeout_seconds = s:taboption("advanced", Value, "switch_ready_timeout_seconds", "切网完成等待时间（秒）")
switch_ready_timeout_seconds.datatype = "uinteger"
switch_ready_timeout_seconds.placeholder = "12"
bind_text(switch_ready_timeout_seconds, "switch_ready_timeout_seconds")

manual_terminal_check_max_attempts = s:taboption("advanced", Value, "manual_terminal_check_max_attempts", "手动登录/登出终态最大检查次数")
function manual_terminal_check_max_attempts.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "手动终态检查次数必须是大于等于 0 的数字"
end
manual_terminal_check_max_attempts.placeholder = "5"
bind_text(manual_terminal_check_max_attempts, "manual_terminal_check_max_attempts", validate_non_negative_number)

manual_terminal_check_interval_seconds = s:taboption("advanced", Value, "manual_terminal_check_interval_seconds", "手动终态校验间隔（秒）")
manual_terminal_check_interval_seconds.datatype = "uinteger"
manual_terminal_check_interval_seconds.placeholder = "2"
bind_text(manual_terminal_check_interval_seconds, "manual_terminal_check_interval_seconds")

hotspot_failback_enabled = s:taboption("advanced", Flag, "hotspot_failback_enabled", "热点切换失败时自动回切校园网")
bind_flag(hotspot_failback_enabled, "hotspot_failback_enabled")

connectivity_check_mode = s:taboption("advanced", ListValue, "connectivity_check_mode", "在线判定方式")
connectivity_check_mode:value("internet", "互联网可达")
connectivity_check_mode:value("portal", "认证网关可达即可")
connectivity_check_mode:value("ssid", "仅关联到目标 SSID")
connectivity_check_mode.rmempty = false
bind_text(connectivity_check_mode, "connectivity_check_mode")

interval = s:taboption("advanced", Value, "interval", "检测间隔（秒）")
interval.datatype = "uinteger"
bind_text(interval, "interval")

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
