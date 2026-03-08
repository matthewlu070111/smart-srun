module("luci.controller.jxnu_srun", package.seeall)

local http = require "luci.http"
local jsonc = require "luci.jsonc"
local sys = require "luci.sys"

function index()
    entry({"admin", "services", "jxnu_srun"}, cbi("jxnu_srun"), _("JXNU SRun"), 80).dependent = true
    entry({"admin", "services", "jxnu_srun", "log_tail"}, call("action_log_tail")).leaf = true
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
