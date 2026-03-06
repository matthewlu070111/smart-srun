local sys = require "luci.sys"
local util = require "luci.util"

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
        return "", "未找到 Python3，请先安装 python3-light。"
    end

    local cmd = py .. " -B /usr/lib/jxnu_srun/client.py " .. (args or "")
    if stderr_to_stdout then
        cmd = cmd .. " 2>&1"
    else
        cmd = cmd .. " 2>/dev/null"
    end
    return util.trim(sys.exec(cmd) or ""), nil
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

local function strip_quotes(v)
    local value = util.trim(v or "")
    if #value >= 2 then
        local first = value:sub(1, 1)
        local last = value:sub(-1)
        if (first == "'" and last == "'") or (first == '"' and last == '"') then
            value = value:sub(2, -2)
        end
    end
    return value
end

local function list_sta_ssids()
    local out = sys.exec("uci show wireless 2>/dev/null") or ""
    local sections = {}

    for line in out:gmatch("[^\n]+") do
        local sec, opt, val = line:match("^wireless%.([^.]+)%.([^.=]+)=(.+)$")
        if sec and opt and val then
            sections[sec] = sections[sec] or {}
            sections[sec][opt] = strip_quotes(val)
        end
    end

    local set = {}
    for _, item in pairs(sections) do
        local ssid = util.trim(item.ssid or "")
        local mode = util.trim((item.mode or "")):lower()
        if ssid ~= "" and (mode == "" or mode == "sta") then
            set[ssid] = true
        end
    end

    local ret = {}
    for ssid, _ in pairs(set) do
        ret[#ret + 1] = ssid
    end
    table.sort(ret)
    return ret
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
s:tab("basic", "基础设置")
s:tab("advanced", "进阶设置")
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
    if out == "" then
        out = "已触发登录"
    end
    m.message = "手动登录结果: " .. out
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

failover_enabled = s:taboption("basic", Flag, "failover_enabled", "自动切换SSID（下线时段/断网）")
failover_enabled.rmempty = false
failover_enabled.default = "1"

local ssids = list_sta_ssids()

campus_ssid = s:taboption("basic", ListValue, "campus_ssid", "校园网SSID")
campus_ssid.rmempty = false
for _, name in ipairs(ssids) do
    campus_ssid:value(name, name)
end
if #ssids == 0 then
    campus_ssid:value("", "未找到 STA 模式 SSID")
end
campus_ssid:depends("failover_enabled", "1")

hotspot_ssid = s:taboption("basic", ListValue, "hotspot_ssid", "热点SSID")
hotspot_ssid.rmempty = false
for _, name in ipairs(ssids) do
    hotspot_ssid:value(name, name)
end
if #ssids == 0 then
    hotspot_ssid:value("", "未找到 STA 模式 SSID")
end
hotspot_ssid:depends("failover_enabled", "1")

quiet_hours_enabled = s:taboption("advanced", Flag, "quiet_hours_enabled", "按时段自动上下线", quiet_desc)
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

connectivity_check_host = s:taboption("advanced", Value, "connectivity_check_host", "连通性检测地址", "支持 IP 或域名")
connectivity_check_host.default = "8.8.8.8"
connectivity_check_host.rmempty = false
connectivity_check_host:depends("failover_enabled", "1")

interval = s:taboption("advanced", Value, "interval", "检测间隔（秒）")
interval.datatype = "uinteger"
interval.default = "60"
interval.rmempty = false

log_text = s:taboption("log", DummyValue, "_log_text", "运行日志（最近 200 行）")
log_text.rawhtml = true
function log_text.cfgvalue(self, section)
    local t = sys.exec("tail -n 200 /var/log/jxnu_srun.log 2>/dev/null") or ""
    if t == "" then
        t = "暂无日志"
    end
    return '<pre style="white-space:pre-wrap;word-break:break-all;max-height:420px;overflow:auto;">' .. util.pcdata(t) .. '</pre>'
end

clear_log = s:taboption("log", Button, "_clear_log", "清空日志")
clear_log.inputstyle = "remove"
function clear_log.write()
    sys.exec(": > /var/log/jxnu_srun.log")
    m.message = "日志已清空"
end

return m

