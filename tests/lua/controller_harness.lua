-- Loads the real LuCI controller against a recording stand-in for the daemon.
--
-- The controller's job is now translation: form values in, one RPC out, the
-- daemon's answer back to the browser. A harness that stubs the socket rather
-- than the filesystem is what makes that testable -- and it fails loudly if the
-- page ever goes back to writing a file, because nothing here provides one.
--
-- Usage:
--   local harness = dofile("tests/lua/controller_harness.lua")(repo_root)
--   harness.responses["config.get"] = { revision = 3, ... }
--   harness.form = { action = "manual_login" }
--   harness.controller.action_enqueue()
--   assert(harness.calls[1].method == "status.get")

return function(repo_root)
    local harness = {
        form = {},
        output = nil,
        calls = {},
        responses = {},
        writes = {},
        commands = {},
        helpers = {},
        now = 1000,
        security_allowed = true,
    }

    local function encode(value)
        -- The controller hands one table to the response writer; keeping the
        -- table itself is more useful to a test than a string of it.
        return value
    end

    package.preload["luci.http"] = function()
        return {
            formvalue = function(key) return harness.form[key] end,
            getenv = function(key) return harness.env and harness.env[key] end,
            prepare_content = function() end,
            header = function(key, value) harness.headers = harness.headers or {}; harness.headers[key] = value end,
            write = function(value) harness.output = value end,
        }
    end
    package.preload["luci.jsonc"] = function()
        return {
            parse = function(text) return harness.parsed and harness.parsed[text] or nil end,
            stringify = encode,
        }
    end
    package.preload["luci.dispatcher"] = function()
        return { context = { authsession = "browser-session" },
            test_post_security = function() return harness.security_allowed end }
    end
    package.preload["luci.sys"] = function()
        return {
            exec = function(command)
                harness.commands[#harness.commands + 1] = command
                return ""
            end,
            call = function(command)
                harness.commands[#harness.commands + 1] = command
                return 0
            end,
        }
    end
    package.preload["luci.util"] = function()
        return {
            trim = function(value) return tostring(value or ""):match("^%s*(.-)%s*$") end,
            pcdata = function(value) return tostring(value or "") end,
            shellquote = function(value) return "'" .. tostring(value or "") .. "'" end,
        }
    end
    package.preload["nixio.fs"] = function()
        return {
            readfile = function() return nil end,
            writefile = function(path, value)
                harness.writes[#harness.writes + 1] = { path = path, value = value }
                return true
            end,
            access = function() return false end,
            mkdirr = function() return true end,
            remove = function() return true end,
            unlink = function() return true end,
            rename = function() return true end,
            stat = function() return nil end,
            dir = function() return function() return nil end end,
        }
    end
    package.preload["nixio"] = function()
        return {
            getpid = function() return 4242 end,
            open_flags = function() return 0 end,
            open = function() return { lock = function() return true end, close = function() end } end,
        }
    end

    -- The stand-in daemon. Unset methods answer "not implemented" rather than
    -- an empty success, so a call the test did not plan for cannot pass as one.
    package.preload["luci.smart_srun.rpc"] = function()
        local rpc = { NULL = "\1null\1", EMPTY_OBJECT = "\1{}\1" }

        local function record(method, params, started)
            harness.calls[#harness.calls + 1] =
                { method = method, params = params, started = started or false }
            local canned = harness.responses[method]
            if type(canned) == "function" then
                return canned(params)
            end
            if canned == nil then
                return nil, { code = "Internal", message = "本次测试没有为 " .. method .. " 准备响应" }
            end
            if canned.error then
                return nil, canned.error
            end
            return canned
        end

        function rpc.call(method, params) return record(method, params, false) end
        function rpc.call_started(method, params) return record(method, params, true) end
        function rpc.stop_service()
            harness.helpers[#harness.helpers + 1] = "service stop"
            if harness.stop_failure then
                return nil, harness.stop_failure
            end
            return true
        end
        function rpc.ensure_running()
            harness.helpers[#harness.helpers + 1] = "service ensure-running"
            return true
        end
        function rpc.service_stopped(err)
            return type(err) == "table" and err.code == "ServiceStopped"
        end
        function rpc.message(err, fallback)
            if type(err) == "table" and tostring(err.message or "") ~= "" then
                return err.message
            end
            return fallback or "操作失败"
        end
        return rpc
    end

    package.path = repo_root .. "/root/usr/lib/lua/?.lua;" .. package.path
    dofile(repo_root .. "/root/usr/lib/lua/luci/controller/smart_srun.lua")
    harness.controller = package.loaded["luci.controller.smart_srun"]
    harness.rpc = require "luci.smart_srun.rpc"
    harness.bridge = require "luci.smart_srun.bridge"

    -- A fixed clock, so an age computed from two timestamps is a stated number
    -- rather than whatever the machine running the test happens to say.
    local real_time = os.time
    os.time = function(parts)
        if parts then return real_time(parts) end
        return harness.now
    end

    function harness.last_call(method)
        for index = #harness.calls, 1, -1 do
            if harness.calls[index].method == method then
                return harness.calls[index]
            end
        end
        return nil
    end

    function harness.reset()
        harness.calls = {}
        harness.output = nil
        harness.writes = {}
        harness.helpers = {}
    end

    return harness
end
