local repo = ... or "."
local h = dofile(repo .. "/tests/lua/controller_harness.lua")(repo)

local cases = {
    { action = "add_campus", method = "campus.upsert" },
    { action = "edit_campus", method = "campus.upsert" },
    { action = "add_hotspot", method = "hotspot.upsert" },
    { action = "edit_hotspot", method = "hotspot.upsert" },
    { action = "delete_campus", method = "campus.remove" },
    { action = "delete_hotspot", method = "hotspot.remove" },
    { action = "set_default_campus", method = "campus.set_default" },
    { action = "set_default_hotspot", method = "hotspot.set_default" },
    { action = "add_campus", method = "setup_wifi.account", setup_job = string.rep("a", 32) },
}

for _, case in ipairs(cases) do
    local revision = 11
    local writes = 0
    h.responses["config.get"] = { revision = revision }
    h.responses[case.method] = function(params)
        if params.expected_revision ~= revision then
            return nil, { code = "Conflict", message = "配置已变化，请刷新页面后再保存" }
        end
        writes = writes + 1
        revision = revision + 1
        return { config_revision = revision }
    end

    h.reset()
    h.form = { action = case.action, id = "saved-id", expected_revision = "10",
        setup_job = case.setup_job }
    h.controller.action_enqueue()
    assert(h.output.ok == false, case.action .. " accepted a stale browser revision")
    assert(writes == 0, case.action .. " overwrote a newer configuration")
    assert(h.last_call(case.method).params.expected_revision == 10)
    assert(h.last_call("config.get") == nil, case.action .. " replaced the browser revision")
    assert(#h.calls == 1, case.action .. " retried a rejected write")

    for _, invalid in ipairs({ false, "", "-1", "1.5", "1e1", "0x10", " 11", "11 ",
        "9007199254740992", {}, "NaN", "Infinity" }) do
        h.reset()
        h.form.expected_revision = invalid
        if invalid == false then h.form.expected_revision = nil end
        h.controller.action_enqueue()
        assert(h.output.ok == false, case.action .. " accepted an invalid revision")
        assert(#h.calls == 0 and #h.helpers == 0, case.action .. " made an RPC with an invalid revision")
    end

    h.responses["config.get"] = { error = { code = "Unavailable" } }
    for _, valid in ipairs({ "0", "11", "9007199254740991" }) do
        h.reset()
        revision = tonumber(valid)
        h.form.expected_revision = valid
        h.controller.action_enqueue()
        assert(h.output.ok == true, case.action .. ": " .. tostring(h.output.message))
        assert(h.last_call(case.method).params.expected_revision == tonumber(valid))
        assert(#h.calls == 1 and h.last_call("config.get") == nil)
    end

    if not case.setup_job then
        h.reset()
        revision = 11
        h.form.expected_revision = "11"
        h.controller.action_enqueue()
        assert(h.output.ok == true)
        local prior_writes = writes
        h.reset()
        h.controller.action_enqueue()
        assert(h.output.ok == false and writes == prior_writes, case.action .. " accepted a duplicate old edit")
        assert(#h.calls == 1 and h.last_call(case.method).params.expected_revision == 11)
    end
end

print("controller config revision probe: ok")
