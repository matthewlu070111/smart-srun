-- The page's half of the local control protocol.
--
-- Spec 02 sends every LuCI business request to the Go daemon over its Unix
-- socket, and spec 03 fixes the framing: one UTF-8 JSON object per line, one
-- request and one response per connection. This file is the only place in the
-- interface that knows any of that, so the controller and the CBI page never
-- assemble a frame, a deadline or an error envelope themselves.
--
-- Nothing here builds a shell command out of user input. The single external
-- program this module may run is the fixed lifecycle helper, with a fixed
-- argument list and no data from the request in it.

local nixio = require "nixio"
-- writeall and readall are not nixio methods: nixio.util adds them to the
-- socket metatable. This module used to rely on something else having loaded
-- it -- the controller and CBI page pull in nixio.fs, which does -- so it
-- worked in the page and failed with "attempt to call method 'writeall'" from
-- any caller that did not happen to load nixio.fs first.
require "nixio.util"
local jsonc = require "luci.jsonc"

local M = {}

M.SOCKET_PATH = "/var/run/smart-srun/control.sock"
M.HELPER = "/usr/bin/srunnet"
M.RPC_VERSION = 1

-- A response is bounded by the same limit the daemon writes under, so a peer
-- that never sends a newline cannot make uhttpd buffer without end.
local MAX_RESPONSE_BYTES = 1024 * 1024
local READ_CHUNK = 8192
-- Seconds. Well inside uhttpd's script timeout: a page that hangs for a minute
-- is indistinguishable from a broken one, and every method reachable from here
-- answers from memory.
local IO_TIMEOUT = 8
-- The helper waits up to five seconds for the socket and then gives up, so this
-- only has to be longer than that, not open-ended.
local HELPER_TIMEOUT = 12

-- Two values the protocol needs and a Lua table cannot hold: an explicit JSON
-- null, which is how an account clears its double_stack override, and an empty
-- JSON object, which an empty Lua table encodes as an array instead.
--
-- They travel as strings built from a control character, because that is what
-- luci.jsonc escapes as  -- a form field cannot contain one, so no value
-- a user types can be mistaken for a sentinel, and a request that somehow did
-- carry one would be refused by the daemon's strict decoder rather than saved
-- as something else.
M.NULL = "\1null\1"
M.EMPTY_OBJECT = "\1{}\1"

local SENTINELS = {
    { '"\\u0001null\\u0001"', "null" },
    { '"\\u0001{}\\u0001"', "{}" },
}

local request_counter = 0

local function fail(code, message)
    return nil, { code = code, message = message, retryable = false }
end

local function next_request_id()
    request_counter = request_counter + 1
    return string.format("luci-%d-%d", nixio.getpid(), request_counter)
end

-- connect returns a socket already carrying its deadlines.
--
-- The timeouts are set before the connect, not after, so a socket that exists
-- but is never accepted cannot hold the request open either.
local function connect()
    local sock = nixio.socket("unix", "stream")
    if not sock then
        return fail("Internal", "无法创建本地套接字")
    end
    -- Old nixio builds without SO_RCVTIMEO would raise here rather than return;
    -- pcall keeps that from turning into a 500 page, and the daemon answering
    -- from memory is what keeps the remaining risk to a blocked read.
    pcall(function()
        sock:setopt("socket", "sndtimeo", IO_TIMEOUT)
        sock:setopt("socket", "rcvtimeo", IO_TIMEOUT)
    end)

    local ok = sock:connect(M.SOCKET_PATH)
    if not ok then
        sock:close()
        -- Not reaching the socket is the ordinary "the service is not running"
        -- case and gets the code the rest of the program uses for it, so the
        -- page can offer to start it instead of showing a transport fault.
        return nil, {
            code = "ServiceStopped",
            message = "认证服务未在运行",
            retryable = true,
        }
    end
    return sock
end

local function write_frame(sock, line)
    local payload = line .. "\n"
    local sent = sock:writeall(payload)
    if sent ~= #payload then
        return fail("ServiceStopped", "请求未能完整发送给认证服务")
    end
    return true
end

-- read_frame reads exactly one newline-terminated response.
--
-- A short read is not a frame and neither is an unterminated one: the loop
-- keeps reading until the newline arrives, and end-of-stream with bytes in hand
-- is reported as a truncated response rather than parsed as a complete one.
local function read_frame(sock)
    local parts, total = {}, 0
    while true do
        local chunk = sock:read(READ_CHUNK)
        if chunk == nil or chunk == "" then
            if total == 0 then
                return fail("ServiceStopped", "认证服务没有返回响应")
            end
            return fail("ProtocolInvalid", "认证服务的响应在换行之前结束")
        end
        total = total + #chunk
        if total > MAX_RESPONSE_BYTES then
            return fail("ProtocolInvalid", "认证服务的响应超过大小上限")
        end
        parts[#parts + 1] = chunk
        local joined = table.concat(parts)
        local newline = joined:find("\n", 1, true)
        if newline then
            return joined:sub(1, newline - 1)
        end
        parts = { joined }
    end
end

-- call makes one request and returns its result.
--
-- Two returns, never one: `result, nil` or `nil, err`. A caller that has to
-- inspect a field of the result to find out whether the call failed eventually
-- forgets to, which is how a failed save reports success.
function M.call(method, params)
    local request = {
        rpc_version = M.RPC_VERSION,
        request_id = next_request_id(),
        method = method,
    }
    if params ~= nil then
        request.params = params
    end
    local line = jsonc.stringify(request)
    if type(line) ~= "string" then
        return fail("Internal", "无法编码请求参数")
    end
    for _, sentinel in ipairs(SENTINELS) do
        line = line:gsub(sentinel[1], sentinel[2])
    end

    local sock, err = connect()
    if not sock then
        return nil, err
    end

    local ok, write_err = write_frame(sock, line)
    if not ok then
        sock:close()
        return nil, write_err
    end

    local response_line, read_err = read_frame(sock)
    sock:close()
    if not response_line then
        return nil, read_err
    end

    local response = jsonc.parse(response_line)
    if type(response) ~= "table" then
        return fail("ProtocolInvalid", "认证服务返回的不是合法 JSON")
    end
    if tonumber(response.rpc_version) ~= M.RPC_VERSION then
        return fail("ProtocolInvalid", "认证服务的协议版本与本页面不一致")
    end
    -- A mismatched id means the answer belongs to another call. One request per
    -- connection makes that impossible on a healthy socket, which is exactly
    -- why seeing it means the payload must not be trusted. A failure with no id
    -- at all is the documented exception: some refusals happen before the
    -- request has been read, so there is nothing to echo.
    if response.request_id ~= request.request_id
        and not (tostring(response.request_id or "") == "" and not response.ok) then
        return fail("ProtocolInvalid", "认证服务的响应与请求不匹配")
    end

    if not response.ok then
        local payload = type(response.error) == "table" and response.error or {}
        return nil, {
            code = tostring(payload.code or "Internal"),
            message = tostring(payload.message or "认证服务内部错误"),
            field = type(payload.details) == "table"
                and tostring(payload.details.field or "") or "",
            retryable = payload.retryable and true or false,
        }
    end
    -- Methods that answer with nothing come back as JSON null. An empty table
    -- keeps every caller's `result.x` lookup from erroring on a nil index.
    if type(response.result) ~= "table" then
        return {}
    end
    return response.result
end

function M.service_stopped(err)
    return type(err) == "table" and err.code == "ServiceStopped"
end

function M.message(err, fallback)
    if type(err) == "table" and tostring(err.message or "") ~= "" then
        return err.message
    end
    return fallback or "操作失败"
end

-- run_helper runs the fixed lifecycle helper with an argument list.
--
-- fork and exec rather than a shell: spec 02 requires the page to invoke one
-- fixed program with safe arguments, and there is no shell here to interpret an
-- account name, a URL or a password even if a future caller tried to pass one.
-- The arguments are checked against a whitelist anyway, because "no shell" and
-- "no arbitrary subcommand" are two different guarantees.
local ALLOWED_HELPER_ARGS = {
    ["service ensure-running"] = true,
    ["service stop"] = true,
    ["service status"] = true,
}

local function run_helper(argv)
    local joined = table.concat(argv, " ")
    local update_start = #argv == 4 and argv[1] == "update" and argv[2] == "run"
        and #argv[3] == 64 and argv[3]:match("^[0-9a-f]+$") and argv[4] == "--background"
    if not ALLOWED_HELPER_ARGS[joined] and not update_start then
        return fail("InvalidArgument", "不允许的服务命令")
    end

    local pid = nixio.fork()
    if not pid then
        return fail("Internal", "无法启动服务助手")
    end
    if pid == 0 then
        -- Child. Its output belongs in the log the daemon keeps, not in the
        -- JSON body this request is about to write.
        local null = nixio.open("/dev/null", "r+")
        if null then
            nixio.dup(null, nixio.stdin)
            nixio.dup(null, nixio.stdout)
            nixio.dup(null, nixio.stderr)
        end
        nixio.exec(M.HELPER, unpack(argv))
        -- exec only returns when it failed.
        os.exit(127)
    end

    -- A helper that never exits must not hold the page open for longer than the
    -- helper's own budget, so the wait is polled rather than blocking.
    local waited = 0
    while waited < HELPER_TIMEOUT * 10 do
        local done, _, status = nixio.waitpid(pid, "nohang")
        if done == pid then
            if status == 0 then
                return true
            end
            -- Not `return ok or fail(...)`: `or` would keep only the first of
            -- fail's two values and the caller would lose the message.
            return fail("ServiceStopped", "服务助手失败（退出码 " .. tostring(status) .. "）")
        end
        nixio.nanosleep(0, 100000000)
        waited = waited + 1
    end
    return fail("DeadlineExceeded", "等待服务助手超时")
end

-- ensure_running starts the service for an explicit user action.
--
-- Spec 02 allows exactly this: a deliberate save or manual action on a page
-- whose service is stopped may start it and then submit its own request. It is
-- not called from status polling, because a page left open in a tab must not
-- keep restarting a service the user stopped.
function M.ensure_running()
    return run_helper({ "service", "ensure-running" })
end

function M.stop_service()
    return run_helper({ "service", "stop" })
end

function M.start_update(plan_id)
    if type(plan_id) ~= "string" or #plan_id ~= 64 or not plan_id:match("^[0-9a-f]+$") then
        return fail("InvalidArgument", "更新计划无效，请重新检查更新")
    end
    return run_helper({ "update", "run", plan_id, "--background" })
end

-- call_started is the mutating path: make sure the service is there, then make
-- the call. Reads deliberately do not use it.
function M.call_started(method, params)
    local ok, err = M.ensure_running()
    if not ok then
        return nil, err
    end
    return M.call(method, params)
end

return M
