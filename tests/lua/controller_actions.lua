-- Status, manual actions and account editing through the real controller.
--
-- Every one of these used to be a file the page wrote. What is asserted now is
-- the request that leaves the page and the answer that reaches the browser: the
-- account the action names, the revision it carries, the credential it does not
-- send, and the refusal it does not swallow.
local repo = ... or "."
local harness = dofile(repo .. "/tests/lua/controller_harness.lua")(repo)
local controller = harness.controller
local PORTAL_URL = "http://portal.example.invalid/srun_portal_pc"

local CONFIG = {
    revision = 11,
    selection = { active_campus_id = "c1", default_campus_id = "c2",
                  active_hotspot_id = "h1", default_hotspot_id = "h1" },
    campus_accounts = {
        { id = "c1", label = "宿舍", user_id = "student", access_mode = "wired",
          wired_iface = "wan", auth_enabled = false, login = {} },
        { id = "c2", label = "教学楼", user_id = "student", access_mode = "wifi",
          ssid = "Campus", login = {} },
    },
    hotspot_profiles = { { id = "h1", label = "手机热点", ssid = "iPhone" } },
}

local RUNNING = {
    service = "running", enabled = true, config_revision = 11,
    written_at = "2026-09-18T12:00:05Z",
    accounts = { { account_id = "c1", link = "Ready", auth = "Rejected",
                   connectivity = "PortalReachable", identity = "" } },
    actions = { { id = "a1", kind = "manual_login", state = "failed",
                  message = "用户名或密码错误", ended_at = "2026-09-18T12:00:00Z" } },
}

harness.responses["config.get"] = CONFIG
harness.responses["status.get"] = RUNNING

-- The explanation of the last action survives routine status polling, which is
-- the failure the baseline hit: a daemon tick replaced it seconds after it
-- appeared, while the user was still reading it.
controller.action_status()
local status = harness.output
assert(status.last_action == "manual_login", status.last_action)
assert(status.action_result == "error", status.action_result)
assert(status.last_action_message == "用户名或密码错误", status.last_action_message)
assert(status.last_action_ts == harness.now - 5, status.last_action_ts)
assert(status.connectivity == "认证网关可达", status.connectivity)
assert(status.connectivity_level == "portal", status.connectivity_level)
assert(status.status == "认证被拒绝", status.status)
-- Portal evidence is not in the daemon's projection yet (batch B): the field is
-- published empty rather than filled with a guess.
assert(status.last_action_portal_url == "", status.last_action_portal_url)
assert(status.campus_account_label == "宿舍", status.campus_account_label)
assert(status.hotspot_profile_label == "手机热点", status.hotspot_profile_label)
-- A read must never start the service.
for _, call in ipairs(harness.calls) do
    assert(call.started == false, call.method .. " must not start the service")
end

-- A stopped service is reported as a state, not as a page error.
harness.reset()
harness.responses["status.get"] = { error = { code = "ServiceStopped", message = "认证服务未在运行" } }
controller.action_status()
assert(harness.output.status == "认证服务未在运行", harness.output.status)
assert(harness.output.service == "stopped", harness.output.service)
assert(harness.output.connectivity_level == "offline")
harness.responses["status.get"] = RUNNING

-- Overview history is in completion order; wall-clock corrections must not
-- select an older result or uplink. An ID query remains independent of both.
local rolled = { service = "running", actions = {
    {id = "old", kind = "switch_hotspot", state = "succeeded", ended_at = "2026-09-18T12:00:00Z"},
    {id = "new", kind = "switch_campus", state = "succeeded", ended_at = "2026-09-18T11:00:00Z"},
} }
local rolled_view = harness.bridge.status_view(rolled, CONFIG)
assert(rolled_view.last_action == "switch_campus" and rolled_view.mode == "campus")

for _, state in ipairs({ "queued", "running", "succeeded", "failed", "cancelled", "interrupted" }) do
    harness.reset()
    harness.form = { action_id = "instance-a1" }
    harness.responses["action.get"] = {id = "instance-a1", kind = "manual_login", state = state,
        account_id = "c1", message = "this request", ended_at = "2026-09-18T11:00:00Z",
        result = { secret = "private" }}
    harness.responses["config.get"] = CONFIG
    controller.action_status()
    -- Polling costs one read. The one outcome that can offer the school's page
    -- (a failed manual login) costs one more, of the already-redacted
    -- configuration, and never starts the service.
    if state == "failed" then
        assert(#harness.calls == 2 and #harness.helpers == 0, #harness.calls)
        assert(harness.calls[2].method == "config.get" and not harness.calls[2].started)
    else
        assert(#harness.calls == 1 and #harness.helpers == 0, #harness.calls)
    end
    local read = harness.calls[1]
    assert(read.method == "action.get" and not read.started)
    assert(read.params.action_id == "instance-a1" and read.params.session == nil)
    assert(harness.output.action_id == "instance-a1" and harness.output.last_action_message == "this request")
    local expected = ({queued = "pending", running = "pending", succeeded = "ok", failed = "error",
        cancelled = "forced", interrupted = "forced"})[state]
    assert(harness.output.action_result == expected)
    assert(harness.output.result == nil)
end

for _, response in ipairs({
    {id = "instance-a2", kind = "manual_login", state = "succeeded", message = "someone else"},
    {id = "instance-a1", kind = "detect_operators", state = "succeeded", result = { secret = "private" }},
    {id = "instance-a1", kind = "manual_login", state = "unknown"},
    {error = {code = "NotFound"}}, {error = {code = "ServiceStopped", message = "认证服务未在运行"}},
}) do
    harness.reset()
    harness.responses["action.get"] = response
    controller.action_status()
    assert(harness.output.action_id == "instance-a1" and harness.output.action_result == "error")
    assert(harness.output.last_action == "" and harness.output.result == nil)
    assert(#harness.calls == 1 and not harness.calls[1].started and #harness.helpers == 0)
end
for _, invalid in ipairs({ "", string.rep("a", 65), "a/1", "a 1", {} }) do
    harness.reset()
    harness.form = { action_id = invalid }
    controller.action_status()
    assert(harness.output.action_result == "error" and #harness.calls == 0)
end
harness.form = {}

-- A manual action names the active account and carries the revision it read.
-- Rejected method/token checks must run before any read that starts a service,
-- submission, config write, log clear or lifecycle helper.
for _, action in ipairs({ "manual_login", "manual_logout", "switch_campus", "switch_hotspot",
    "force_stop", "add_campus", "edit_campus", "delete_campus", "set_default_campus",
    "add_hotspot", "edit_hotspot", "delete_hotspot", "set_default_hotspot" }) do
    harness.reset()
    harness.security_allowed = false
    harness.form = { action = action }
    controller.action_enqueue()
    assert(#harness.calls == 0 and #harness.helpers == 0, action .. " bypassed security rejection")
end
for _, handler in ipairs({controller.action_log_clear, controller.action_user_presets_set}) do
    harness.reset()
    harness.env = { REQUEST_METHOD = "POST" }
    harness.form = { data = "presets" }
    harness.parsed = { presets = { presets = {}, operators = {} } }
    handler()
    assert(#harness.calls == 0 and #harness.helpers == 0, "write endpoint bypassed security rejection")
end
harness.security_allowed = true

harness.reset()
harness.responses["action.submit"] = { action_id = "a2", state = "queued" }
harness.form = { action = "manual_login" }
controller.action_enqueue()
local submit = assert(harness.last_call("action.submit"))
assert(submit.params.kind == "manual_login", submit.params.kind)
assert(submit.params.account_id == "c1", submit.params.account_id)
assert(submit.params.expected_revision == 11, submit.params.expected_revision)
assert(submit.params.idempotency_key:find("luci-manual_login-", 1, true) == 1,
    submit.params.idempotency_key)
assert(harness.output.ok == true, harness.output.message)
assert(harness.output.requested_at == harness.now, harness.output.requested_at)
assert(harness.output.message == "已提交手动登录请求", harness.output.message)
assert(#harness.writes == 0, "no file may be written")
assert(#harness.commands == 0, "no shell command may run")

-- Two clicks in the same second are one action, not two logins.
local first = submit.params.idempotency_key
harness.reset()
controller.action_enqueue()
assert(harness.last_call("action.submit").params.idempotency_key == first)

-- Switching back to campus uses the default account; switching to a hotspot
-- names the hotspot and no account at all.
harness.reset()
harness.form = { action = "switch_campus" }
controller.action_enqueue()
assert(harness.last_call("action.submit").params.account_id == "c2")

harness.reset()
harness.form = { action = "switch_hotspot" }
controller.action_enqueue()
local hotspot = harness.last_call("action.submit").params
assert(hotspot.hotspot_id == "h1", tostring(hotspot.hotspot_id))
assert(hotspot.account_id == nil, "a hotspot switch does not name a campus account")

-- Background work is lower priority in the Go coordinator. A status preflight
-- must not prevent the user's request from ever reaching that scheduler.
for _, background in ipairs({ "maintain", "presets_refresh", "forced_logout", "quiet_hotspot", "quiet_campus" }) do
    for _, state in ipairs({ "queued", "running" }) do
        for _, manual in ipairs({ "manual_login", "manual_logout", "switch_campus", "switch_hotspot" }) do
            harness.reset()
            harness.responses["status.get"] = { actions = { { kind = background, state = state } } }
            harness.form = { action = manual }
            controller.action_enqueue()
            assert(harness.output.ok == true, background .. ": " .. tostring(harness.output.message))
            assert(harness.last_call("action.submit").params.kind == manual)
        end
    end
end

-- An action already in flight is refused with the frozen wording, and nothing
-- is submitted behind it.
harness.reset()
harness.responses["status.get"] = {
    service = "running", enabled = true, written_at = "2026-09-18T12:00:05Z", accounts = {},
    actions = { { id = "a9", kind = "switch_campus", state = "running" } },
}
harness.form = { action = "manual_logout" }
controller.action_enqueue()
assert(harness.output.ok == false, "a second action must be refused")
assert(harness.output.message:find("已有动作正在执行", 1, true), harness.output.message)
assert(harness.output.pending_action == "switch_campus", harness.output.pending_action)
assert(harness.last_call("action.submit") == nil, "nothing may be submitted")

-- A wizard or an unknown kind remains a conflicting operation, including when
-- it follows background work in the snapshot. Only known background kinds skip
-- the preflight; the daemon can still reject a full/frozen queue itself.
for _, kind in ipairs({ "manual_login", "relogin", "detect_verify", "wifi_setup_start", "future_action" }) do
    harness.reset()
    harness.responses["status.get"] = { actions = {
        { kind = "maintain", state = "running" },
        { kind = kind, state = "queued" },
    } }
    controller.action_enqueue()
    assert(harness.output.ok == false)
    assert(harness.output.pending_action == kind)
    assert(harness.last_call("action.submit") == nil)
end
harness.reset()
harness.responses["status.get"] = { actions = { { kind = "maintain", state = "running" } } }
harness.responses["action.submit"] = { error = { code = "Busy", message = "队列已满" } }
controller.action_enqueue()
assert(harness.output.ok == false and harness.output.message == "队列已满")
harness.responses["action.submit"] = { action_id = "a2", state = "queued" }
harness.responses["status.get"] = RUNNING

-- Force stop is the fixed lifecycle helper. No init script, no process hunt,
-- and the user's automatic-authentication switch is not touched.
harness.reset()
harness.form = { action = "force_stop" }
controller.action_enqueue()
assert(harness.helpers[1] == "service stop", tostring(harness.helpers[1]))
assert(harness.output.ok == true, harness.output.message)
assert(harness.output.message == "已强制关闭插件并停止服务", harness.output.message)
assert(harness.last_call("config.apply") == nil, "force stop must not write configuration")
assert(#harness.commands == 0, "force stop must not run a shell command")

harness.reset()
harness.stop_failure = { code = "Internal", message = "停不下来" }
controller.action_enqueue()
assert(harness.output.ok == false and harness.output.message == "停不下来", harness.output.message)
harness.stop_failure = nil

-- Editing an account: the legacy alias is read, only the canonical field is
-- written, and an empty password box keeps the stored credential.
harness.reset()
harness.responses["campus.upsert"] = { config_revision = 12, id = "c1" }
harness.form = {
    action = "edit_campus", id = "c1", access_mode = "wired", wired_iface = "  ",
    network_interface = "  wan.test  ", auth_enabled = "1", password = "",
    user_id = "student", operator_suffix = "telecom", double_stack = "",
}
controller.action_enqueue()
local account = harness.last_call("campus.upsert").params.account
assert(account.wired_iface == "wan.test", account.wired_iface)
assert(account.network_interface == nil, "the alias must not be written back")
assert(account.password == nil, "an empty password box must not clear the credential")
assert(account.auth_enabled == true)
assert(account.login.double_stack == harness.rpc.NULL, "an empty override clears it explicitly")
assert(account.label == "student@telecom", account.label)
assert(harness.output.message == "已更新", harness.output.message)

-- A new account submits what it was given, including an empty password, and
-- lets the daemon choose the identifier.
harness.reset()
harness.responses["campus.upsert"] = { config_revision = 13, id = "c3" }
harness.form = { action = "add_campus", access_mode = "wired", wired_iface = "wan",
                 user_id = "fresh", password = "", operator_suffix = "" }
controller.action_enqueue()
local created = harness.last_call("campus.upsert").params.account
assert(created.id == nil, "a new account must not choose its own id")
assert(created.password == "", "a new account stores what the form gave it")
assert(created.label == "fresh", created.label)
assert(harness.output.message == "已添加", harness.output.message)

-- A base URL typed as a bare host still reaches the daemon as a URL.
harness.reset()
harness.form = { action = "add_campus", access_mode = "wired", wired_iface = "wan",
                 user_id = "fresh", base_url = "10.0.0.55/srun_portal" }
controller.action_enqueue()
assert(harness.last_call("campus.upsert").params.account.base_url == "http://10.0.0.55",
    harness.last_call("campus.upsert").params.account.base_url)

-- Deleting and defaulting are their own operations, each with the revision.
for _, case in ipairs({
    { action = "delete_campus", method = "campus.remove", message = "已删除" },
    { action = "delete_hotspot", method = "hotspot.remove", message = "已删除" },
    { action = "set_default_campus", method = "campus.set_default", message = "已设为当前默认账号" },
    { action = "set_default_hotspot", method = "hotspot.set_default", message = "已设为当前默认热点" },
}) do
    harness.reset()
    harness.responses[case.method] = { config_revision = 14 }
    harness.form = { action = case.action, id = "c1" }
    controller.action_enqueue()
    local call = assert(harness.last_call(case.method), case.method .. " was not called")
    assert(call.params.id == "c1" and call.params.expected_revision == 11)
    assert(harness.output.message == case.message, harness.output.message)
end

-- A configuration conflict is reported, not hidden behind a success.
harness.reset()
harness.responses["campus.remove"] = {
    error = { code = "Conflict", message = "配置已变化，请重新读取后再保存" },
}
harness.form = { action = "delete_campus", id = "c1" }
controller.action_enqueue()
assert(harness.output.ok == false, "a conflict must not report success")
assert(harness.output.message == "配置已变化，请重新读取后再保存", harness.output.message)

-- A temporary Wi-Fi account goes through the owned wizard transaction. A
-- missing/rejected job must not fall back to an ordinary account save.
package.preload["luci.dispatcher"] = function()
    return { context = { authsession = "browser-session" }, test_post_security = function() return true end }
end
harness.reset()
harness.responses["setup_wifi.account"] = { error = { code = "NotFound", message = "没有当前会话的无线任务" } }
harness.form = { action = "add_campus", setup_job = string.rep("a", 32), access_mode = "wifi" }
controller.action_enqueue()
assert(harness.output.ok == false, "a wizard save must not pretend to work")
assert(harness.last_call("campus.upsert") == nil)

-- Log translation is unchanged: raw gateway codes never reach the user.
for _, code in ipairs({ "no_response_data_error", "not_online_error", "portal_intercept_error",
                        "auth_html_response_error", "auth_response_parse_error" }) do
    local line = controller.friendly_line(
        "[2026-06-01 22:00:00] WARN srun_login_response error_code=" .. code)
    assert(not line:find(code, 1, true), line)
end
local multi = controller.friendly_line(
    "[2026-06-01 22:00:00] WARN multi_wan_session account_id=c1 wired_iface=wan.test | offline")
assert(not multi:find("multi_wan_session", 1, true), multi)
assert(multi:find("wan.test", 1, true), multi)
assert(PORTAL_URL ~= nil)

print("controller actions probe: ok")
