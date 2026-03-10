module("luci.controller.jxnu_srun", package.seeall)

local http = require "luci.http"
local jsonc = require "luci.jsonc"
local sys = require "luci.sys"
local fs = require "nixio.fs"

local STATE_FILE = "/var/run/jxnu_srun/state.json"
local ACTION_FILE = "/var/run/jxnu_srun/action.json"

function index()
    entry({"admin", "services", "jxnu_srun"}, cbi("jxnu_srun"), _("JXNU SRun"), 80).dependent = true
    entry({"admin", "services", "jxnu_srun", "status"}, call("action_status")).leaf = true
    entry({"admin", "services", "jxnu_srun", "enqueue"}, call("action_enqueue")).leaf = true
    entry({"admin", "services", "jxnu_srun", "log_tail"}, call("action_log_tail")).leaf = true
end

local function read_json_file(path)
    local raw = fs.readfile(path)
    if not raw or raw == "" then
        return {}
    end

    local parsed = jsonc.parse(raw)
    if type(parsed) ~= "table" then
        return {}
    end
    return parsed
end

local function write_json_file(path, payload)
    local dir = path:match("^(.+)/[^/]+$")
    if dir and not fs.access(dir) then
        fs.mkdirr(dir)
    end
    fs.writefile(path, (jsonc.stringify(payload) or "{}") .. "\n")
end

function action_status()
    local data = read_json_file(STATE_FILE)
    local action = read_json_file(ACTION_FILE)
    local text = tostring(data.message or "未知")
    local pending = tostring(action.action or data.pending_action or "")
    local enabled = true
    if data.enabled == false or tostring(data.enabled or "") == "0" then
        enabled = false
    end

    http.prepare_content("application/json")
    http.write(jsonc.stringify({
        status = text,
        enabled = enabled,
        mode = tostring(data.current_mode or ""),
        mode_label = tostring(data.mode_label or ""),
        in_quiet = data.in_quiet and true or false,
        pending_action = pending,
        current_ssid = tostring(data.current_ssid or ""),
        current_ip = tostring(data.current_ip or ""),
        current_iface = tostring(data.current_iface or ""),
        connectivity = tostring(data.connectivity or ""),
        connectivity_level = tostring(data.connectivity_level or "offline"),
        updated_at = tonumber(data.updated_at) or 0,
        ts = os.time(),
    }))
end

function action_enqueue()
    local action = tostring(http.formvalue("action") or "")
    local allowed = {
        switch_hotspot = true,
        switch_campus = true,
    }
    local ok = allowed[action] == true
    local message = "不支持的动作"

    if ok then
        write_json_file(ACTION_FILE, {
            action = action,
            requested_at = os.time(),
        })
        sys.call("(sleep 1; /etc/init.d/jxnu_srun restart >/dev/null 2>&1) >/dev/null 2>&1 &")
        message = action == "switch_hotspot" and "已提交切到热点请求" or "已提交切回校园网请求"
    end

    http.prepare_content("application/json")
    http.write(jsonc.stringify({
        ok = ok,
        message = message,
        action = action,
        ts = os.time(),
    }))
end

function action_log_tail()
    local lines = tonumber(http.formvalue("lines")) or 400
    if lines < 10 then
        lines = 10
    elseif lines > 1000 then
        lines = 1000
    end

    local text = sys.exec("tail -n " .. lines .. " /var/log/jxnu_srun.log 2>/dev/null") or ""
    if text == "" then
        text = "No logs yet."
    end

    http.prepare_content("application/json")
    http.write(jsonc.stringify({
        log = text,
        ts = os.time(),
    }))
end
