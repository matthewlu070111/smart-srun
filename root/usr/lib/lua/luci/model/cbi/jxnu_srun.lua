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

m = Map("jxnu_srun", "师大校园网", "江西师范大学校园网认证配置")

s = m:section(NamedSection, "main", "main", "基本设置")
s.addremove = false
s.anonymous = true

status = s:option(DummyValue, "_status", "当前在线状态")
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

login_now = s:option(Button, "_login_now", "立即登录")
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

enabled = s:option(Flag, "enabled", "启用")
enabled.rmempty = false

user_id = s:option(Value, "user_id", "学工号")
user_id.datatype = "uinteger"
user_id.rmempty = false

operator = s:option(ListValue, "operator", "运营商")
operator:value("cmcc", "中国移动 (cmcc)")
operator:value("ctcc", "中国电信 (ctcc)")
operator:value("cucc", "中国联通 (cucc)")
operator:value("xn", "校内网 (xn)")
operator.default = "cucc"
operator.rmempty = false

password = s:option(Value, "password", "密码")
password.password = true
password.rmempty = false

quiet_hours_enabled = s:option(Flag, "quiet_hours_enabled", "夜间停用 (北京时间 00:00-06:00)")
quiet_hours_enabled.rmempty = false
quiet_hours_enabled.default = "1"

force_logout_in_quiet = s:option(Flag, "force_logout_in_quiet", "进入夜间停用时强制下线 (便于使用手机热点)")
force_logout_in_quiet.rmempty = false
force_logout_in_quiet.default = "1"

interval = s:option(Value, "interval", "检测间隔(秒)")
interval.datatype = "uinteger"
interval.default = "300"
interval.rmempty = false

return m
