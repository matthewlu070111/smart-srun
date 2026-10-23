local repo = ... or "."
local harness = dofile(repo .. "/tests/lua/controller_harness.lua")(repo)
local http = require "luci.http"
local dispatcher = require "luci.dispatcher"
local util = require "luci.util"
dispatcher.build_url = function() return "/cgi-bin/luci/admin/services/smart_srun" end
http.redirect = function(url) harness.redirect = url end
util.pcdata = function(value)
    return tostring(value or ""):gsub("&", "&amp;"):gsub("<", "&lt;"):gsub('"', "&quot;")
end

local stored = {
    revision = 17, school = "saved-school", enabled = true,
    selection = {active_campus_id = "c1"},
    campus_accounts = {{id = "c1", label = "saved-campus", user_id = "student", access_mode = "wired"}},
    hotspot_profiles = {{id = "h1", label = "saved-hotspot", ssid = "saved-ssid"}},
    school_extra = {saved = "value"},
}
local available = true
harness.responses["config.get"] = function()
    if available then return stored end
    return nil, {code = "Unavailable", message = "RPC <unavailable>"}
end
harness.responses["schema.get"] = {global = {
    {path = "school", default = "default"}, {path = "enabled", default = false},
}}
harness.responses["status.get"] = {service = "running", enabled = true}
harness.responses["config.apply"] = function(params)
    if params.expected_revision ~= stored.revision then
        return nil, {code = "Conflict", message = "配置已被修改"}
    end
    for key, value in pairs(params.settings) do stored[key] = value end
    stored.revision = stored.revision + 1
    return {config_revision = stored.revision}
end

local function new_form(kind)
    local form = {kind = kind, fields = {}, children = {}, save = true, uci = {}}
    function form.uci:get() return true end
    function form.uci:set() end
    function form:section()
        local section = {}
        function section:tab() end
        function section:option(_, name)
            local option = {option = name}
            function option:value() end
            function option:depends() end
            form.fields[name] = option
            return option
        end
        function section:taboption(_, class, name) return self:option(class, name) end
        form.children[#form.children + 1] = section
        return section
    end
    function form:parse() end
    return form
end

local Map = setmetatable({}, {__call = function() return new_form("map") end})
function Map.parse(self)
    harness.parse_calls = harness.parse_calls + 1
    if harness.before_parse then harness.before_parse() end
    if harness.env.REQUEST_METHOD ~= "POST" then return 0 end
    for _ = 1, harness.form["cbi.apply"] and 2 or 1 do
        for key, option in pairs(self.fields) do
            local value = harness.form["cbid.smart_srun.main." .. key]
            if value ~= nil and option.write then option:write("main", value) end
        end
    end
    if harness.invalid_parse then self.save = false end
    return 1
end

local function request(method, form)
    harness.reset()
    harness.redirect, harness.parse_calls = nil, 0
    harness.env = {REQUEST_METHOD = method or "GET"}
    harness.form = form or {}
    local env = setmetatable({Map = Map, SimpleForm = function() return new_form("error") end,
        SimpleSection = {}, NamedSection = {}, DummyValue = {}, Flag = {}, ListValue = {}, Value = {}}, {__index = _G})
    local chunk = assert(loadfile(repo .. "/root/usr/lib/lua/luci/model/cbi/smart_srun.lua"))
    setfenv(chunk, env)
    local page = chunk()
    page:parse()
    return page
end

local function rejected(page)
    assert(page.kind == "error", "untrusted configuration must render an independent error form")
    assert(page.embedded == true, "the CBI route already provides the outer form")
    assert(page.submit == false and page.reset == false)
    assert(not page.fields.enabled and not page.fields._tables and not page.fields._setup_wizard)
    assert(harness.parse_calls == 0 and not harness.last_call("config.apply"))
    local html = page.fields._config_error:cfgvalue()
    assert(html:find('href="/cgi-bin/luci/admin/services/smart_srun"', 1, true))
    assert(not html:find('name="expected_revision"', 1, true))
end

available = false
local failed = request()
rejected(failed)
assert(failed.fields._config_error:cfgvalue():find("&lt;unavailable>", 1, true))
for _, call in ipairs(harness.calls) do assert(not call.started, "page reads must not start the service") end

available = true
rejected(request("POST", {["cbi.submit"] = "1", ["cbid.smart_srun.main.enabled"] = "0"}))
assert(stored.enabled and stored.school == "saved-school")
for _, revision in ipairs({"16", "18", "", "-1", "1.5", "1e1", "9007199254740992"}) do
    rejected(request("POST", {["cbi.submit"] = "1", expected_revision = revision,
        ["cbid.smart_srun.main.school"] = "default"}))
end
local recovered = request()
assert(recovered.fields.school:cfgvalue() == "saved-school" and recovered.fields.enabled:cfgvalue() == "1")
assert(recovered.fields._tables:cfgvalue():find("saved-campus", 1, true))
local html = recovered.fields._overview_status:cfgvalue()
assert(html:find('id="smart-srun-config-revision"', 1, true))
assert(html:find('name="expected_revision" value="17"', 1, true))

harness.invalid_parse = true
request("POST", {["cbi.submit"] = "1", expected_revision = "17", ["cbid.smart_srun.main.enabled"] = "0"})
assert(not harness.last_call("config.apply") and not harness.redirect and stored.enabled)
harness.invalid_parse = nil

request("POST", {["cbi.submit"] = "1", ["cbi.apply"] = "1", expected_revision = "17",
    ["cbid.smart_srun.main.enabled"] = "0"})
assert(harness.last_call("config.apply").params.expected_revision == 17)
assert(stored.enabled == false and stored.revision == 18)
local writes = 0
for _, call in ipairs(harness.calls) do if call.method == "config.apply" then writes = writes + 1 end end
assert(writes == 1, "CBI Apply reparsing must not issue a second write")
assert(harness.redirect == dispatcher.build_url(), "successful saves must reload the complete configuration")
local next_page = request()
assert(next_page.fields._overview_status:cfgvalue():find('name="expected_revision" value="18"', 1, true))

harness.before_parse = function() stored.revision = 19 end
local raced = request("POST", {["cbi.submit"] = "1", expected_revision = "18",
    ["cbid.smart_srun.main.enabled"] = "1"})
harness.before_parse = nil
assert(harness.last_call("config.apply").params.expected_revision == 18)
assert(stored.enabled == false and not harness.redirect)
assert(raced.message:find("配置未保存", 1, true))
assert(raced.fields._overview_status:cfgvalue():find('name="expected_revision" value="18"', 1, true))

for _, revision in ipairs({false, "invalid", -1, 1.5, 9007199254740992}) do
    stored.revision = revision or nil
    rejected(request())
end

stored.revision = 0
request("POST", {["cbi.submit"] = "1", expected_revision = "0", ["cbid.smart_srun.main.enabled"] = "1"})
assert(harness.last_call("config.apply").params.expected_revision == 0 and stored.enabled == true)
assert(harness.redirect)

available = false
local status, err = harness.bridge.status()
assert(status == nil and err.code == "Unavailable", "config read failure must propagate through status")
print("CBI unavailable/recovered/stale/racing/initial configuration checks passed")
