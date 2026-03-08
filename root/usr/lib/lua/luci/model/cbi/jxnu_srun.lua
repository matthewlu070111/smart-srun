local sys = require "luci.sys"
local util = require "luci.util"

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

local function validate_non_negative_number(v, field_label)
    local value = util.trim(v or "")
    local num = tonumber(value)
    if not num or num < 0 then
        return nil, (field_label or "该字段") .. "必须是大于等于 0 的数字"
    end
    return tostring(num)
end

local function form_or_uci(section, option, default_value)
    local form_key = "cbid.jxnu_srun." .. tostring(section or "") .. "." .. tostring(option or "")
    local from_form = m:formvalue(form_key)
    if from_form ~= nil then
        return util.trim(from_form)
    end

    local from_uci = m.uci:get("jxnu_srun", section, option)
    if from_uci ~= nil and tostring(from_uci) ~= "" then
        return util.trim(from_uci)
    end

    return util.trim(default_value or "")
end

local function html_escape(text)
    local value = tostring(text or "")
    if util.pcdata then
        return util.pcdata(value)
    end
    value = value:gsub("&", "&amp;")
    value = value:gsub("<", "&lt;")
    value = value:gsub(">", "&gt;")
    value = value:gsub('"', "&quot;")
    return value
end

local quiet_start_now = util.trim(sys.exec("uci -q get jxnu_srun.main.quiet_start 2>/dev/null") or "")
if quiet_start_now == "" then
    quiet_start_now = "00:00"
end
local quiet_end_now = util.trim(sys.exec("uci -q get jxnu_srun.main.quiet_end 2>/dev/null") or "")
if quiet_end_now == "" then
    quiet_end_now = "06:00"
end
local quiet_desc = string.format("当前下线/上线时间：%s / %s", quiet_start_now, quiet_end_now)

m = Map("jxnu_srun", "师大校园网", "江西师范大学校园网认证配置")

s = m:section(NamedSection, "main", "main", "配置")
s.addremove = false
s.anonymous = true

local developer_mode_now = util.trim(sys.exec("uci -q get jxnu_srun.main.developer_mode 2>/dev/null") or "0")
s:tab("basic", "基础设置")
s:tab("advanced", "进阶设置")
if developer_mode_now == "1" then
    s:tab("developer", "开发者调试")
end
s:tab("log", "日志")

status = s:taboption("basic", DummyValue, "_status", "当前在线状态")
function status.cfgvalue()
    local out, err = run_client("--status", false)
    if err then
        return err
    end
    if out == "" then
        return "未知"
    end
    return out
end

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
enabled.rmempty = false

user_id = s:taboption("basic", Value, "user_id", "学工号")
user_id.datatype = "uinteger"
user_id.rmempty = false

operator = s:taboption("basic", ListValue, "operator", "运营商")
operator:value("cmcc", "中国移动 (cmcc)")
operator:value("ctcc", "中国电信 (ctcc)")
operator:value("cucc", "中国联通 (cucc)")
operator:value("xn", "校内网 (xn)")
operator.default = "cucc"
operator.rmempty = false

password = s:taboption("basic", Value, "password", "密码")
password.password = true
password.rmempty = false

failover_enabled = s:taboption("basic", Flag, "failover_enabled", "夜间模式自动切换热点SSID")
failover_enabled.rmempty = false
failover_enabled.default = "1"

hotspot_ssid = s:taboption("basic", Value, "hotspot_ssid", "热点SSID", "手动输入要切换的热点 SSID")
hotspot_ssid.placeholder = "例如: iPhone_15"
hotspot_ssid.rmempty = true
hotspot_ssid:depends("failover_enabled", "1")
function hotspot_ssid.validate(self, value, section)
    local enabled_now = form_or_uci(section, "failover_enabled", "0")
    local v = util.trim(value or "")
    if enabled_now == "1" and v == "" then
        return nil, "启用夜间模式切换时必须填写热点SSID"
    end
    return v
end

local function add_encryption_values(opt)
    opt:value("none", "开放网络 (none)")
    opt:value("psk", "WPA-PSK (psk)")
    opt:value("psk2", "WPA2-PSK (psk2)")
    opt:value("psk-mixed", "WPA/WPA2-PSK (psk-mixed)")
    opt:value("sae", "WPA3-SAE (sae)")
    opt:value("sae-mixed", "WPA2/WPA3-SAE (sae-mixed)")
end

hotspot_encryption = s:taboption("basic", ListValue, "hotspot_encryption", "热点加密方式")
add_encryption_values(hotspot_encryption)
hotspot_encryption.default = "psk2"
hotspot_encryption.rmempty = false
hotspot_encryption:depends("failover_enabled", "1")

hotspot_key = s:taboption("basic", Value, "hotspot_key", "热点密码")
hotspot_key.password = true
hotspot_key.rmempty = true
hotspot_key:depends("failover_enabled", "1")
function hotspot_key.validate(self, value, section)
    local enabled_now = form_or_uci(section, "failover_enabled", "0")
    local enc = form_or_uci(section, "hotspot_encryption", "none"):lower()
    local v = util.trim(value or "")
    if enabled_now == "1" and enc ~= "none" and v == "" then
        return nil, "热点加密方式非 none 时必须填写热点密码"
    end
    return v
end

quiet_hours_enabled = s:taboption("advanced", Flag, "quiet_hours_enabled", "按时段自动上/下线", quiet_desc)
quiet_hours_enabled.rmempty = false
quiet_hours_enabled.default = "1"

quiet_start = s:taboption("advanced", Value, "quiet_start", "下线时间（北京时间 HH:MM）")
quiet_start.default = "00:00"
quiet_start.rmempty = false
quiet_start:depends("quiet_hours_enabled", "1")
function quiet_start.validate(self, value, section)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end

quiet_end = s:taboption("advanced", Value, "quiet_end", "上线时间（北京时间 HH:MM）")
quiet_end.default = "06:00"
quiet_end.rmempty = false
quiet_end:depends("quiet_hours_enabled", "1")
function quiet_end.validate(self, value, section)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end

force_logout_in_quiet = s:taboption("advanced", Flag, "force_logout_in_quiet", "进入下线时段时强制下线")
force_logout_in_quiet.rmempty = false
force_logout_in_quiet.default = "1"
force_logout_in_quiet:depends("quiet_hours_enabled", "1")

developer_mode = s:taboption("advanced", Flag, "developer_mode", "开发者选项", "启用后显示“开发者调试”标签页（保存并刷新后生效）")
developer_mode.rmempty = false
developer_mode.default = "0"

backoff_enable = s:taboption("advanced", Flag, "backoff_enable", "登录失败时启用退避重试")
backoff_enable.rmempty = false
backoff_enable.default = "1"

backoff_max_retries = s:taboption("advanced", Value, "backoff_max_retries", "最大重试次数（0 为无限）")
backoff_max_retries.datatype = "uinteger"
backoff_max_retries.default = "0"
backoff_max_retries.rmempty = false
backoff_max_retries:depends("backoff_enable", "1")

backoff_initial_duration = s:taboption("advanced", Value, "backoff_initial_duration", "初始等待时长（秒）")
backoff_initial_duration.default = "10"
backoff_initial_duration.rmempty = false
backoff_initial_duration:depends("backoff_enable", "1")
function backoff_initial_duration.validate(self, value, section)
    return validate_non_negative_number(value, "初始等待时长")
end

backoff_max_duration = s:taboption("advanced", Value, "backoff_max_duration", "最大等待时长（秒）")
backoff_max_duration.default = "600"
backoff_max_duration.rmempty = false
backoff_max_duration:depends("backoff_enable", "1")
function backoff_max_duration.validate(self, value, section)
    return validate_non_negative_number(value, "最大等待时长")
end

backoff_exponent_factor = s:taboption("advanced", Value, "backoff_exponent_factor", "指数因子")
backoff_exponent_factor.default = "1.5"
backoff_exponent_factor.rmempty = false
backoff_exponent_factor:depends("backoff_enable", "1")
function backoff_exponent_factor.validate(self, value, section)
    return validate_non_negative_number(value, "指数因子")
end

backoff_inter_const_factor = s:taboption("advanced", Value, "backoff_inter_const_factor", "内常数因子（秒）")
backoff_inter_const_factor.default = "0"
backoff_inter_const_factor.rmempty = false
backoff_inter_const_factor:depends("backoff_enable", "1")
function backoff_inter_const_factor.validate(self, value, section)
    return validate_non_negative_number(value, "内常数因子")
end

backoff_outer_const_factor = s:taboption("advanced", Value, "backoff_outer_const_factor", "外常数因子（秒）")
backoff_outer_const_factor.default = "0"
backoff_outer_const_factor.rmempty = false
backoff_outer_const_factor:depends("backoff_enable", "1")
function backoff_outer_const_factor.validate(self, value, section)
    return validate_non_negative_number(value, "外常数因子")
end

interval = s:taboption("advanced", Value, "interval", "检测间隔（秒）")
interval.datatype = "uinteger"
interval.default = "180"
interval.rmempty = false

if developer_mode_now == "1" then
    switch_hotspot_test = s:taboption("developer", Button, "_switch_hotspot_test", "测试切到热点")
    switch_hotspot_test.inputstyle = "apply"
    switch_hotspot_test.inputtitle = "执行"
    switch_hotspot_test:depends("failover_enabled", "1")
    function switch_hotspot_test.write()
        local out, err = run_client("--switch-hotspot", true)
        if err then
            m.message = "切换测试结果: " .. err
            return
        end
        local line = last_nonempty_line(out)
        if line == "" then
            line = "已触发切换到热点测试"
        end
        m.message = "切换测试结果: " .. line
    end

    switch_campus_test = s:taboption("developer", Button, "_switch_campus_test", "测试切回校园网")
    switch_campus_test.inputstyle = "apply"
    switch_campus_test.inputtitle = "执行"
    switch_campus_test:depends("failover_enabled", "1")
    function switch_campus_test.write()
        local out, err = run_client("--switch-campus", true)
        if err then
            m.message = "切换测试结果: " .. err
            return
        end
        local line = last_nonempty_line(out)
        if line == "" then
            line = "已触发切回校园网测试"
        end
        m.message = "切换测试结果: " .. line
    end
end

log_text = s:taboption("log", DummyValue, "_log_text", "运行日志（实时刷新）")
log_text.rawhtml = true
function log_text.cfgvalue(self, section)
    local t = sys.exec("tail -n 400 /var/log/jxnu_srun.log 2>/dev/null") or ""
    if t == "" then
        t = "暂无日志"
    end

    local escaped = html_escape(t)
    return [[
<div id="jxnu-srun-log-box" style="max-height:420px;overflow:auto;border:1px solid #ddd;padding:8px;background:#fff;">
  <pre id="jxnu-srun-log-pre" style="margin:0;white-space:pre-wrap;word-break:break-all;">]] .. escaped .. [[</pre>
</div>
<script type="text/javascript">
(function() {
  var box = document.getElementById('jxnu-srun-log-box');
  var pre = document.getElementById('jxnu-srun-log-pre');
  if (!box || !pre || window.__jxnuSrunLogInit) return;
  window.__jxnuSrunLogInit = true;

  function atBottom() {
    return (box.scrollHeight - box.scrollTop - box.clientHeight) < 24;
  }
  function stickBottom() {
    box.scrollTop = box.scrollHeight;
  }
  function refresh() {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', '/cgi-bin/luci/admin/services/jxnu_srun/log_tail?lines=400&_=' + Date.now(), true);
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

  stickBottom();
  setInterval(refresh, 2000);
})();
</script>
]]
end

clear_log = s:taboption("log", Button, "_clear_log", "清空日志")
clear_log.inputstyle = "remove"
function clear_log.write()
    sys.exec(": > /var/log/jxnu_srun.log")
    m.message = "日志已清空"
end

m.on_after_commit = function()
    run_client("--sync-json", false)
end

return m
