local fs = require "nixio.fs"
local sys = require "luci.sys"
local util = require "luci.util"
local jsonc = require "luci.jsonc"
local schema = require "luci.smart_srun.schema"

local CONFIG_FILE = "/usr/lib/smart_srun/config.json"
local STATE_FILE = "/var/run/smart_srun/state.json"
local JS_ASSET_PATH = "/luci-static/resources/smart_srun.js"
local GLOBAL_SCALAR_KEYS = schema.GLOBAL_SCALAR_KEYS
local POINTER_KEYS = schema.POINTER_KEYS
local LIST_KEYS = schema.LIST_KEYS
local SCHOOL_EXTRA_KEY = schema.SCHOOL_EXTRA_KEY
local SUPPORTED_SCHOOL_EXTRA_TYPES = {
    string = true,
    bool = true,
    int = true,
    enum = true,
}
local cfg
local changed = false
local dirty_scalar_keys = {}
local school_extra_dirty = false
local SCALAR_DEFAULTS = schema.SCALAR_DEFAULTS
-- 旧版字段（用于迁移检测）
local LEGACY_CAMPUS_KEYS = {
    "user_id", "operator", "password", "base_url", "ac_id",
    "campus_ssid", "campus_encryption", "campus_key",
}

local function ensure_json_file()
    local dir = CONFIG_FILE:match("^(.+)/[^/]+$")
    if dir and not fs.access(dir) then
        fs.mkdirr(dir)
    end
    if not fs.access(CONFIG_FILE) then
        fs.writefile(CONFIG_FILE, "{}\n")
    end
end

local function write_config_json_atomic(data)
    schema.with_file_lock(CONFIG_FILE, function()
        ensure_json_file()
        local tmp = CONFIG_FILE .. ".tmp"
        fs.writefile(tmp, (jsonc.stringify(data) or "{}") .. "\n")
        os.rename(tmp, CONFIG_FILE)
    end)
end

local function render_js_asset_tag()
    return '<script src="' .. util.pcdata(JS_ASSET_PATH) .. '"></script>'
end

local function is_legacy_config(parsed)
    if type(parsed.campus_accounts) == "table" then return false end
    for _, k in ipairs(LEGACY_CAMPUS_KEYS) do
        if parsed[k] ~= nil then return true end
    end
    return false
end

local function migrate_legacy_config(parsed)
    local migrated = {}
    for _, key in ipairs(GLOBAL_SCALAR_KEYS) do
        migrated[key] = parsed[key] ~= nil and tostring(parsed[key]) or (SCALAR_DEFAULTS[key] or "")
    end
    local uid = tostring(parsed.user_id or ""):match("^%s*(.-)%s*$")
    local op = tostring(parsed.operator or "cucc"):match("^%s*(.-)%s*$"):lower()
    local ca = {
        id = "campus-1", label = "",
        base_url = tostring(parsed.base_url or "http://172.17.1.2"):match("^%s*(.-)%s*$"),
        ac_id = tostring(parsed.ac_id or "1"):match("^%s*(.-)%s*$"),
        user_id = uid, password = tostring(parsed.password or ""):match("^%s*(.-)%s*$"),
        operator = op, operator_suffix = "",
        ssid = tostring(parsed.campus_ssid or "jxnu_stu"):match("^%s*(.-)%s*$"),
        bssid = tostring(parsed.campus_bssid or ""):match("^%s*(.-)%s*$"),
    }
    ca.label = (uid ~= "" and op ~= "" and op ~= "xn") and (uid .. "@" .. op) or (uid ~= "" and uid or "未命名账号")
    migrated.campus_accounts = uid ~= "" and { ca } or {}
    migrated.active_campus_id = uid ~= "" and "campus-1" or ""
    migrated.default_campus_id = migrated.active_campus_id

    local hssid = tostring(parsed.hotspot_ssid or ""):match("^%s*(.-)%s*$")
    local hp = {
        id = "hotspot-1", label = hssid ~= "" and hssid or "未命名热点",
        ssid = hssid,
        encryption = tostring(parsed.hotspot_encryption or "psk2"):match("^%s*(.-)%s*$"):lower(),
        key = tostring(parsed.hotspot_key or ""):match("^%s*(.-)%s*$"),
        radio = tostring(parsed.hotspot_radio or ""):match("^%s*(.-)%s*$"),
    }
    migrated.hotspot_profiles = hssid ~= "" and { hp } or {}
    migrated.active_hotspot_id = hssid ~= "" and "hotspot-1" or ""
    migrated.default_hotspot_id = migrated.active_hotspot_id
    return migrated
end

local function next_id(items, prefix)
    local max_num = 0
    if type(items) == "table" then
        for _, item in ipairs(items) do
            local ns = tostring(item.id or ""):match("^" .. prefix .. "%-(%d+)$")
            if ns then local n = tonumber(ns); if n and n > max_num then max_num = n end end
        end
    end
    return prefix .. "-" .. (max_num + 1)
end

local function load_cfg()
    ensure_json_file()
    local raw = fs.readfile(CONFIG_FILE) or "{}"
    local parsed = jsonc.parse(raw)
    if type(parsed) ~= "table" then parsed = {} end
    if is_legacy_config(parsed) then
        parsed = migrate_legacy_config(parsed)
        write_config_json_atomic(parsed)
    end
    local cfg = {}
    for _, key in ipairs(GLOBAL_SCALAR_KEYS) do
        cfg[key] = parsed[key] ~= nil and tostring(parsed[key]) or (SCALAR_DEFAULTS[key] or "")
    end
    for _, key in ipairs(POINTER_KEYS) do
        cfg[key] = tostring(parsed[key] or "")
    end
    for _, key in ipairs(LIST_KEYS) do
        cfg[key] = type(parsed[key]) == "table" and parsed[key] or {}
    end
    cfg[SCHOOL_EXTRA_KEY] = type(parsed[SCHOOL_EXTRA_KEY]) == "table" and parsed[SCHOOL_EXTRA_KEY] or {}
    return cfg
end

local function save_cfg(cfg)
    schema.with_file_lock(CONFIG_FILE, function()
        ensure_json_file()
        local latest = jsonc.parse(fs.readfile(CONFIG_FILE) or "{}")
        if type(latest) ~= "table" then
            latest = {}
        end

        local out = {}
        for _, key in ipairs(GLOBAL_SCALAR_KEYS) do
            out[key] = dirty_scalar_keys[key]
                and tostring(cfg[key] or SCALAR_DEFAULTS[key] or "")
                or tostring(latest[key] or SCALAR_DEFAULTS[key] or "")
        end
        for _, key in ipairs(POINTER_KEYS) do
            out[key] = tostring(latest[key] or "")
        end
        for _, key in ipairs(LIST_KEYS) do
            out[key] = type(latest[key]) == "table" and latest[key] or {}
        end
        out[SCHOOL_EXTRA_KEY] = school_extra_dirty
            and (type(cfg[SCHOOL_EXTRA_KEY]) == "table" and cfg[SCHOOL_EXTRA_KEY] or {})
            or (type(latest[SCHOOL_EXTRA_KEY]) == "table" and latest[SCHOOL_EXTRA_KEY] or {})

        local tmp = CONFIG_FILE .. ".tmp"
        fs.writefile(tmp, (jsonc.stringify(out) or "{}") .. "\n")
        os.rename(tmp, CONFIG_FILE)
    end)
end

local function load_state()
    local raw = fs.readfile(STATE_FILE) or "{}"
    local parsed = jsonc.parse(raw)
    return type(parsed) == "table" and parsed or {}
end

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

    local cmd = py .. " -B /usr/lib/smart_srun/client.py " .. (args or "")
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

local function validate_non_negative_number(v)
    local value = util.trim(v or "")
    local num = tonumber(value)
    if not num or num < 0 then
        return nil
    end
    return tostring(num)
end

local function load_radio_choices()
    local out = {}
    local seen = {}
    local raw = sys.exec("uci show wireless 2>/dev/null") or ""
    for line in raw:gmatch("[^\n]+") do
        local radio, opt, val = line:match("^wireless%.(radio%d+)%.([%w_]+)=(.+)$")
        if radio and (opt == "band" or opt == "hwmode") then
            local entry = out[radio] or { label = radio }
            val = util.trim(val or "")
            val = val:gsub("^['\"]", ""):gsub("['\"]$", "")
            if opt == "band" then
                if val == "2g" then
                    entry.label = radio .. " (2.4GHz)"
                elseif val == "5g" then
                    entry.label = radio .. " (5GHz)"
                elseif val == "6g" then
                    entry.label = radio .. " (6GHz)"
                else
                    entry.label = radio .. " (" .. val .. ")"
                end
            elseif not seen[radio] then
                if val:find("a", 1, true) then
                    entry.label = radio .. " (5GHz)"
                else
                    entry.label = radio .. " (2.4GHz)"
                end
            end
            out[radio] = entry
            seen[radio] = true
        end
    end
    return out
end

local RADIO_CHOICES = load_radio_choices()

local function is_github_username(value)
    local username = tostring(value or "")
    if #username < 2 or #username > 39 then
        return false
    end
    if username:find("%-%-", 1, true) then
        return false
    end
    return username:match("^@[A-Za-z0-9][A-Za-z0-9%-]*[A-Za-z0-9]$") ~= nil
        or username:match("^@[A-Za-z0-9]$") ~= nil
end

local function render_school_info_html(schools, current_school)
    local cur_desc = ""
    local cur_contributors = {}
    local helper_prefix = "如果该配置无法在您的学校使用，请直接前往"
    local helper_suffix = "提交 Issue 或 PR"
    local helper_link = "https://github.com/matthewlu070111/luci-app-smart-srun"

    for _, sch in ipairs(schools or {}) do
        if sch.short_name == current_school then
            cur_desc = tostring(sch.description or "")
            if type(sch.contributors) == "table" then
                cur_contributors = sch.contributors
            end
            break
        end
    end

    local show_desc = cur_desc ~= ""
    local show_contrib = #cur_contributors > 0
    local contrib_spacing = "4px"
    local helper_spacing = "4px"
    local cur_contrib_html = {}
    local js_data = jsonc.stringify(schools or {}) or "[]"

    for _, contributor in ipairs(cur_contributors) do
        local text = tostring(contributor or "")
        if is_github_username(text) then
            cur_contrib_html[#cur_contrib_html + 1] = string.format(
                '<a href="https://github.com/%s" target="_blank" rel="noopener noreferrer">%s</a>',
                util.pcdata(text:sub(2)),
                util.pcdata(text)
            )
        else
            cur_contrib_html[#cur_contrib_html + 1] = string.format('<span>%s</span>', util.pcdata(text))
        end
    end

    return string.format([[
<div id="smart-school-info" class="cbi-value-description" style="color:#14532d;opacity:0.9;display:block;line-height:1.6;">
  <div id="smart-school-desc" style="display:%s;">
    <strong>该配置在以下学校已得到验证：</strong> <span id="smart-school-desc-text">%s</span>
  </div>
  <div id="smart-school-contrib" style="display:%s;margin-top:%s;">
    <strong>贡献者:</strong> <span id="smart-school-contrib-text">%s</span>
  </div>
  <div id="smart-school-helper" style="display:block;margin-top:%s;color:#6b7280;font-size:0.92em;">
    %s<a id="smart-school-repo-link" href="%s" target="_blank" rel="noopener noreferrer">插件仓库</a>%s
  </div>
  <textarea id="smart-school-data" style="display:none;">%s</textarea>
</div>
]],
        "block",
        util.pcdata(cur_desc),
        show_contrib and "block" or "none",
        contrib_spacing,
        table.concat(cur_contrib_html, ", "),
        (show_desc or show_contrib) and helper_spacing or "0",
        helper_prefix,
        helper_link,
        helper_suffix,
        util.pcdata(js_data))
end

local function ensure_school_extra_table()
    if type(cfg[SCHOOL_EXTRA_KEY]) ~= "table" then
        cfg[SCHOOL_EXTRA_KEY] = {}
    end
    return cfg[SCHOOL_EXTRA_KEY]
end

local function set_school_extra_value(key, value)
    local school_extra = ensure_school_extra_table()
    local normalized = tostring(value or "")
    if school_extra[key] ~= normalized then
        school_extra[key] = normalized
        changed = true
        school_extra_dirty = true
    end
end

local function remove_school_extra_value(key)
    local school_extra = ensure_school_extra_table()
    if school_extra[key] ~= nil then
        school_extra[key] = nil
        changed = true
        school_extra_dirty = true
    end
end

local function get_school_extra_value(key, default_value)
    local school_extra = ensure_school_extra_table()
    local value = school_extra[key]
    if value == nil or tostring(value) == "" then
        return tostring(default_value or "")
    end
    return tostring(value)
end

local function normalize_school_runtime_descriptor(descriptor)
    if type(descriptor) ~= "table" then
        return nil
    end

    local key = util.trim(tostring(descriptor.key or ""))
    if key == "" then
        return nil
    end

    local value_type = util.trim(tostring(descriptor.type or "string")):lower()
    local item = {
        key = key,
        type = value_type ~= "" and value_type or "string",
        label = util.trim(tostring(descriptor.label or key)),
        description = tostring(descriptor.description or ""),
        required = descriptor.required == true,
        default = descriptor.default ~= nil and tostring(descriptor.default) or "",
        choices = {},
    }

    if type(descriptor.choices) == "table" then
        for _, choice in ipairs(descriptor.choices) do
            item.choices[#item.choices + 1] = tostring(choice)
        end
    end

    if item.label == "" then
        item.label = key
    end
    return item
end

local function parse_school_runtime_contract(raw_json)
    local parsed = jsonc.parse(raw_json or "")
    if type(parsed) ~= "table" then
        parsed = {}
    end
    return parsed
end

local function bind_school_extra_flag(opt, descriptor, school_changed_ref)
    opt.rmempty = false
    function opt.cfgvalue()
        return get_school_extra_value(descriptor.key, descriptor.default) == "1" and "1" or "0"
    end
    function opt.write(self, section, value)
        if school_changed_ref() then
            return
        end
        set_school_extra_value(descriptor.key, value == "1" and "1" or "0")
    end
    function opt.remove(self, section)
        if school_changed_ref() then
            return
        end
        set_school_extra_value(descriptor.key, "0")
    end
end

local function bind_school_extra_text(opt, descriptor, school_changed_ref, normalize_fn)
    opt.rmempty = not descriptor.required
    function opt.cfgvalue()
        return get_school_extra_value(descriptor.key, descriptor.default)
    end
    function opt.write(self, section, value)
        if school_changed_ref() then
            return
        end
        local raw = util.trim(value or "")
        if raw == "" and not descriptor.required then
            remove_school_extra_value(descriptor.key)
            return
        end
        if normalize_fn then
            local normalized = normalize_fn(raw)
            if normalized == nil then
                return
            end
            set_school_extra_value(descriptor.key, normalized)
            return
        end
        set_school_extra_value(descriptor.key, raw)
    end
    function opt.remove(self, section)
        if school_changed_ref() then
            return
        end
        if descriptor.required then
            return
        end
        remove_school_extra_value(descriptor.key)
    end
end

cfg = load_cfg()
changed = false

-- 加载学校 Profile 列表
local schools_json = select(1, run_client("schools", false)) or ""
local schools = jsonc.parse(schools_json)
if type(schools) ~= "table" then schools = {} end

local school_runtime_json = select(1, run_client("schools inspect --selected", false)) or ""
local school_runtime_contract = parse_school_runtime_contract(school_runtime_json)
if type(school_runtime_contract.school_extra) == "table" then
    cfg[SCHOOL_EXTRA_KEY] = school_runtime_contract.school_extra
end
local school_runtime_descriptors = {}
local school_runtime_renderable = type(school_runtime_contract.field_descriptors) == "table"
    and type(school_runtime_contract.school_extra) == "table"

if school_runtime_renderable then
    for _, descriptor in ipairs(school_runtime_contract.field_descriptors) do
        local item = normalize_school_runtime_descriptor(descriptor)
        if item and SUPPORTED_SCHOOL_EXTRA_TYPES[item.type] then
            school_runtime_descriptors[#school_runtime_descriptors + 1] = item
        end
    end
end

local function set_value(key, value)
    local v = tostring(value or "")
    if cfg[key] ~= v then
        cfg[key] = v
        changed = true
        dirty_scalar_keys[key] = true
    end
end

local school_changed_during_parse = false

local function school_extra_write_blocked()
    return school_changed_during_parse
end

local function bind_flag(opt, key)
    opt.rmempty = false
    function opt.cfgvalue()
        return cfg[key] == "1" and "1" or "0"
    end
    function opt.write(self, section, value)
        set_value(key, (value == "1") and "1" or "0")
    end
    function opt.remove(self, section)
        set_value(key, "0")
    end
end

local function bind_text(opt, key, normalize_fn)
    opt.rmempty = true
    function opt.cfgvalue()
        return cfg[key] or ""
    end
    function opt.write(self, section, value)
        local raw = util.trim(value or "")
        if normalize_fn then
            local normalized = normalize_fn(raw)
            if normalized == nil then
                return
            end
            set_value(key, normalized)
            return
        end
        set_value(key, raw)
    end
end

local quiet_desc = string.format("当前下线/上线时间：%s / %s", cfg.quiet_start or "00:00", cfg.quiet_end or "06:00")

m = Map("smart_srun", "智慧深澜", "深澜校园网认证配置")
if not m.uci:get("smart_srun", "main") then
    m.uci:section("smart_srun", "main", "main")
    m.uci:save("smart_srun")
    m.uci:commit("smart_srun")
end

overview = m:section(SimpleSection)
overview.anonymous = true
overview_status = overview:option(DummyValue, "_overview_status", "")
overview_status.rawhtml = true
function overview_status.cfgvalue()
    return render_js_asset_tag() .. [[
<div id="smart-srun-overview" style="margin:4px 0 18px 0;border-left:4px solid #c62828;background:rgba(128,128,128,.08);padding:14px 16px;border-radius:0 6px 6px 0;box-shadow:none;">
  <div id="smart-srun-overview-title" style="font-size:18px;font-weight:700;color:#1f2937;margin-bottom:8px;">状态读取中</div>
  <div id="smart-srun-overview-meta" style="font-size:13px;color:#374151;display:flex;gap:14px;flex-wrap:wrap;line-height:1.6;">
    <span>WiFi: --</span>
    <span>模式: --</span>
    <span>连通性: --</span>
  </div>
</div>
]]
end

s = m:section(NamedSection, "main", "main", "配置")
s.addremove = false
s.anonymous = true
s:tab("basic", "基础设置")
s:tab("advanced", "进阶设置")
s:tab("log", "日志")

-- 学校配置选择器
school = s:taboption("basic", ListValue, "school", "登录配置")
if #schools == 0 then
    school:value("jxnu", "默认配置")
else
    for _, sch in ipairs(schools) do
        school:value(sch.short_name, sch.name)
    end
end
function school.cfgvalue()
    return cfg.school or "jxnu"
end
function school.write(self, section, value)
    local next_school = util.trim(value or "jxnu")
    if next_school == "" then
        next_school = "jxnu"
    end
    if next_school ~= (cfg.school or "jxnu") then
        school_changed_during_parse = true
        cfg[SCHOOL_EXTRA_KEY] = {}
        changed = true
        school_extra_dirty = true
    end
    set_value("school", next_school)
end
school.description = render_school_info_html(schools, cfg.school or "jxnu")

if school_runtime_renderable then
    for idx, descriptor in ipairs(school_runtime_descriptors) do
        local option_name = "_school_extra_" .. idx .. "_" .. descriptor.key:gsub("[^%w_]", "_")
        local label = descriptor.label
        local description = descriptor.description
        if descriptor.type == "bool" then
            local opt = s:taboption("basic", Flag, option_name, label, description)
            bind_school_extra_flag(opt, descriptor, school_extra_write_blocked)
        elseif descriptor.type == "enum" then
            local opt = s:taboption("basic", ListValue, option_name, label, description)
            for _, choice in ipairs(descriptor.choices or {}) do
                opt:value(choice, choice)
            end
            bind_school_extra_text(opt, descriptor, school_extra_write_blocked, function(raw)
                if raw == "" and not descriptor.required then
                    return ""
                end
                for _, choice in ipairs(descriptor.choices or {}) do
                    if raw == choice then
                        return raw
                    end
                end
                return nil
            end)
        elseif descriptor.type == "int" then
            local opt = s:taboption("basic", Value, option_name, label, description)
            function opt.validate(self, value)
                local raw = util.trim(value or "")
                if raw == "" and not descriptor.required then
                    return raw
                end
                if raw:match("^-?%d+$") then
                    return raw
                end
                return nil, "该字段必须是整数"
            end
            bind_school_extra_text(opt, descriptor, school_extra_write_blocked, function(raw)
                if raw == "" and not descriptor.required then
                    return ""
                end
                if raw:match("^-?%d+$") then
                    return tostring(tonumber(raw))
                end
                return nil
            end)
        else
            local opt = s:taboption("basic", Value, option_name, label, description)
            bind_school_extra_text(opt, descriptor, school_extra_write_blocked)
        end
    end
end

manual_login = s:taboption("basic", DummyValue, "_manual_login", "手动登录")
manual_login.rawhtml = true
function manual_login.cfgvalue()
    return [[
<div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
  <button id="smart-srun-manual-login" type="button" class="cbi-button cbi-button-apply">立即登录</button>
  <button id="smart-srun-manual-logout" type="button" class="cbi-button cbi-button-reset">立即登出</button>
  <span id="smart-srun-manual-result" style="color:#666;"></span>
</div>
]]
end

enabled = s:taboption("basic", Flag, "enabled", "启用")
enabled.description = "仅控制后台自动登录守护服务（自动检测、自动重连、按时段自动上下线/切网）。手动登录和手动登出始终可用，不受此开关影响。"
bind_flag(enabled, "enabled")

quiet_desc = "当前下线/上线时间：" .. tostring(cfg.quiet_start or "00:00") .. " / " .. tostring(cfg.quiet_end or "06:00")

quiet_hours_enabled = s:taboption("basic", Flag, "quiet_hours_enabled", "按时段自动上/下线", quiet_desc)
bind_flag(quiet_hours_enabled, "quiet_hours_enabled")

quiet_start = s:taboption("basic", Value, "quiet_start", "下线时间（北京时间 HH:MM）")
quiet_start.default = "00:00"
function quiet_start.validate(self, value)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end
bind_text(quiet_start, "quiet_start", validate_hhmm)

quiet_end = s:taboption("basic", Value, "quiet_end", "上线时间（北京时间 HH:MM）")
quiet_end.default = "06:00"
function quiet_end.validate(self, value)
    local t = validate_hhmm(value)
    if t then
        return t
    end
    return nil, "时间格式应为 HH:MM（24小时制）"
end
bind_text(quiet_end, "quiet_end", validate_hhmm)

force_logout_in_quiet = s:taboption("basic", Flag, "force_logout_in_quiet", "进入下线时段时强制下线")
bind_flag(force_logout_in_quiet, "force_logout_in_quiet")

failover_enabled = s:taboption("basic", Flag, "failover_enabled", "登出时自动切换热点上网")
bind_flag(failover_enabled, "failover_enabled")

switch_test = s:taboption("basic", DummyValue, "_switch_test", "手动切换网络")
switch_test.rawhtml = true
function switch_test.cfgvalue()
    return [[
<div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
  <button id="smart-srun-switch-hotspot" type="button" class="cbi-button cbi-button-apply">切到热点</button>
  <button id="smart-srun-switch-campus" type="button" class="cbi-button cbi-button-apply">切回校园网</button>
  <button id="smart-srun-force-close" type="button" class="cbi-button cbi-button-remove">强制关闭插件</button>
  <span id="smart-srun-switch-result" style="color:#666;"></span>
</div>
<div class="cbi-value-description">手动切网会停用自动登录服务，如需启用请再次手动开启。</div>
]]
end

-- 校园网账号表格 + 热点配置表格 + 弹窗
tables_html = s:taboption("basic", DummyValue, "_tables", "")
tables_html.rawhtml = true
function tables_html.cfgvalue()
    local campus = cfg.campus_accounts or {}
    local hotspots = cfg.hotspot_profiles or {}
    local state = load_state()
    local active_cid = cfg.active_campus_id or ""
    local default_cid = cfg.default_campus_id or ""
    local active_hid = cfg.active_hotspot_id or ""
    local default_hid = cfg.default_hotspot_id or ""
    local current_mode = tostring(state.current_mode or "")
    local online_account_label = tostring(state.online_account_label or "")
    local current_ssid = tostring(state.current_ssid or "")
    local current_bssid = tostring(state.current_bssid or "")
    local current_iface = tostring(state.current_iface or "")
    local current_campus_access_mode = tostring(state.current_campus_access_mode or "")

    local operator_labels = { cmcc = "移动", ctcc = "电信", cucc = "联通", xn = "校内网" }
    local radio_labels = { [""] = "自动" }
    local radio_options = '<option value="">自动</option>'
    for radio, meta in pairs(RADIO_CHOICES) do
        local label = tostring(meta.label or radio)
        radio_labels[radio] = label
        radio_options = radio_options .. '<option value="' .. util.pcdata(radio) .. '">' .. util.pcdata(label) .. '</option>'
    end

    -- 构建校园网账号表格行
    local campus_rows = ""
    if type(campus) == "table" then
        for _, a in ipairs(campus) do
            local aid = tostring(a.id or "")
            local campus_user = tostring(a.user_id or "")
            local campus_ssid = tostring(a.ssid or "")
            local campus_bssid = tostring(a.bssid or ""):lower()
            local access_mode = tostring(a.access_mode or "wifi")
            local ssid_display = access_mode == "wired" and "有线" or tostring(a.ssid or "jxnu_stu")
            local is_active = (aid == active_cid)
            local is_default = (aid == default_cid)
            local wifi_match = current_mode == "campus"
                and current_campus_access_mode == "wifi"
                and campus_ssid ~= ""
                and current_ssid == campus_ssid
                and ((campus_bssid == "") or (current_bssid == campus_bssid))
            local wired_match = current_mode == "campus"
                and current_campus_access_mode == "wired"
                and access_mode == "wired"
                and current_iface == "wan"
            local identity_match = campus_user ~= "" and online_account_label == campus_user
            local is_connected = false
            if access_mode == "wired" then
                is_connected = wired_match and identity_match
            else
                is_connected = wifi_match and identity_match
            end
            local badge_parts = {}
            if is_connected then
                badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#16a34a;font-weight:700;">已连接</span>'
            end
            if is_default then
                if is_active and not is_connected then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#2563eb;font-weight:700;">默认</span>'
                elseif not is_active then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#d97706;font-weight:700;">待生效</span>'
                end
            else
                badge_parts[#badge_parts + 1] = ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"smartSetDefault('campus','%s')\">设默认</button>"):format(util.pcdata(aid))
            end
            local badge = table.concat(badge_parts, '<br>')
            campus_rows = campus_rows .. '<tr class="tr">'
                .. '<td class="td">' .. badge .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.label or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.base_url or "http://172.17.1.2")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.ac_id or "1")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.user_id or "")) .. '</td>'
                .. '<td class="td">' .. (operator_labels[tostring(a.operator or "")] or tostring(a.operator or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.operator_suffix or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(ssid_display) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(a.bssid or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(radio_labels[tostring(a.radio or "")] or tostring(a.radio or "自动")) .. '</td>'
                .. '<td class="td cbi-section-actions"><div class="smart-action-cell">'
                .. ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"smartEditCampus('%s')\">编辑</button>"):format(util.pcdata(aid))
                .. ("<button type=\"button\" class=\"cbi-button cbi-button-remove\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"smartDelete('campus','%s')\">删除</button>"):format(util.pcdata(aid))
                .. '</div></td></tr>\n'
        end
    end
    if campus_rows == "" then
        campus_rows = '<tr class="tr"><td class="td" colspan="11" style="text-align:center;color:#999;">暂无账号，请点击"新增"添加</td></tr>'
    end

    -- 构建热点配置表格行
    local hotspot_rows = ""
    if type(hotspots) == "table" then
        for _, h in ipairs(hotspots) do
            local hid = tostring(h.id or "")
            local is_active = (hid == active_hid)
            local is_default = (hid == default_hid)
            local hotspot_ssid = tostring(h.ssid or "")
            local is_connected = current_mode == "hotspot" and hotspot_ssid ~= "" and current_ssid == hotspot_ssid
            local badge_parts = {}
            if is_connected then
                badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#16a34a;font-weight:700;">已连接</span>'
            end
            if is_default then
                if is_active and not is_connected then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#2563eb;font-weight:700;">默认</span>'
                elseif not is_active then
                    badge_parts[#badge_parts + 1] = '<span style="display:inline-block;color:#d97706;font-weight:700;">待生效</span>'
                end
            else
                badge_parts[#badge_parts + 1] = ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"smartSetDefault('hotspot','%s')\">设默认</button>"):format(util.pcdata(hid))
            end
            local badge = table.concat(badge_parts, '<br>')
            hotspot_rows = hotspot_rows .. '<tr class="tr">'
                .. '<td class="td">' .. badge .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(h.label or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(h.ssid or "")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(tostring(h.encryption or "psk2")) .. '</td>'
                .. '<td class="td">' .. util.pcdata(radio_labels[tostring(h.radio or "")] or tostring(h.radio or "自动")) .. '</td>'
                .. '<td class="td cbi-section-actions"><div class="smart-action-cell">'
                .. ("<button type=\"button\" class=\"cbi-button\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"smartEditHotspot('%s')\">编辑</button>"):format(util.pcdata(hid))
                .. ("<button type=\"button\" class=\"cbi-button cbi-button-remove\" style=\"font-size:12px;padding:1px 8px;\" onclick=\"smartDelete('hotspot','%s')\">删除</button>"):format(util.pcdata(hid))
                .. '</div></td></tr>\n'
        end
    end
    if hotspot_rows == "" then
        hotspot_rows = '<tr class="tr"><td class="td" colspan="6" style="text-align:center;color:#999;">暂无热点，请点击"新增"添加</td></tr>'
    end

    -- 将数据嵌入到前端
    local campus_json = jsonc.stringify(campus) or "[]"
    local hotspot_json = jsonc.stringify(hotspots) or "[]"

    return [[
<style>
.smart-native-box{margin:18px 0;}
.smart-native-box h3{margin:0 0 .75rem 0;font-weight:600;}
.smart-native-box .cbi-section-table .th,
.smart-native-box .cbi-section-table .td{vertical-align:middle;}
.smart-native-box .cbi-section-table .td:last-child{white-space:nowrap;}
.smart-native-box .cbi-section-table .btn,
.smart-native-box .cbi-section-table .cbi-button{vertical-align:middle;}
.smart-native-box .cbi-section-actions{white-space:nowrap;text-align:center;}
.smart-native-box .smart-action-cell{display:inline-flex;align-items:center;justify-content:center;gap:.5rem;width:100%;}
.smart-native-box .smart-box-actions{padding:.75rem 1rem 0 1rem;}
.smart-native-row{margin-bottom:.75rem;}
.smart-native-row label{display:block;margin-bottom:.25rem;font-weight:600;}
.smart-native-row input,.smart-native-row select{width:100%;box-sizing:border-box;}
</style>

<div class="cbi-section cbi-tblsection smart-native-box">
  <h3>校园网账号</h3>
  <table class="table cbi-section-table">
    <tr class="tr table-titles"><th class="th" style="width:80px;">状态</th><th class="th">标签</th><th class="th">认证地址</th><th class="th">ACID</th><th class="th">学工号</th><th class="th">运营商</th><th class="th">后缀</th><th class="th">SSID</th><th class="th">BSSID</th><th class="th">频段</th><th class="th cbi-section-actions" style="width:120px;">操作</th></tr>
    <tbody>]] .. campus_rows .. [[</tbody>
  </table>
  <div class="smart-box-actions">
    <button type="button" class="cbi-button cbi-button-add" onclick="smartEditCampus('')">新增</button>
  </div>
</div>

<div class="cbi-section cbi-tblsection smart-native-box">
  <h3>热点配置</h3>
  <table class="table cbi-section-table">
    <tr class="tr table-titles"><th class="th" style="width:80px;">状态</th><th class="th">标签</th><th class="th">SSID</th><th class="th">加密方式</th><th class="th">频段</th><th class="th cbi-section-actions" style="width:120px;">操作</th></tr>
    <tbody>]] .. hotspot_rows .. [[</tbody>
  </table>
  <div class="smart-box-actions">
    <button type="button" class="cbi-button cbi-button-add" onclick="smartEditHotspot('')">新增</button>
  </div>
</div>
<textarea id="smart-campus-data" style="display:none;">]] .. util.pcdata(campus_json) .. [[</textarea>
<textarea id="smart-hotspot-data" style="display:none;">]] .. util.pcdata(hotspot_json) .. [[</textarea>
<textarea id="smart-radio-options" style="display:none;">]] .. util.pcdata(radio_options) .. [[</textarea>
]]
end

backoff_enable = s:taboption("advanced", Flag, "backoff_enable", "登录失败时启用退避重试")
bind_flag(backoff_enable, "backoff_enable")

backoff_max_retries = s:taboption("advanced", Value, "backoff_max_retries", "最大重试次数（0 为无限）")
backoff_max_retries.datatype = "uinteger"
bind_text(backoff_max_retries, "backoff_max_retries")

retry_cooldown_seconds = s:taboption("advanced", Value, "retry_cooldown_seconds", "失败后首次重试等待（秒）")
function retry_cooldown_seconds.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "首次重试等待时长必须是大于等于 0 的数字"
end
retry_cooldown_seconds.placeholder = "10"
bind_text(retry_cooldown_seconds, "retry_cooldown_seconds", validate_non_negative_number)

retry_max_cooldown_seconds = s:taboption("advanced", Value, "retry_max_cooldown_seconds", "退避等待上限（秒）")
function retry_max_cooldown_seconds.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "退避等待上限必须是大于等于 0 的数字"
end
retry_max_cooldown_seconds.placeholder = "600"
bind_text(retry_max_cooldown_seconds, "retry_max_cooldown_seconds", validate_non_negative_number)

switch_ready_timeout_seconds = s:taboption("advanced", Value, "switch_ready_timeout_seconds", "切网完成等待时间（秒）")
switch_ready_timeout_seconds.datatype = "uinteger"
switch_ready_timeout_seconds.placeholder = "12"
bind_text(switch_ready_timeout_seconds, "switch_ready_timeout_seconds")

manual_terminal_check_max_attempts = s:taboption("advanced", Value, "manual_terminal_check_max_attempts", "手动登录/登出终态最大检查次数")
function manual_terminal_check_max_attempts.validate(self, value)
    if validate_non_negative_number(value) then
        return value
    end
    return nil, "手动终态检查次数必须是大于等于 0 的数字"
end
manual_terminal_check_max_attempts.placeholder = "5"
bind_text(manual_terminal_check_max_attempts, "manual_terminal_check_max_attempts", validate_non_negative_number)

manual_terminal_check_interval_seconds = s:taboption("advanced", Value, "manual_terminal_check_interval_seconds", "手动终态校验间隔（秒）")
manual_terminal_check_interval_seconds.datatype = "uinteger"
manual_terminal_check_interval_seconds.placeholder = "2"
bind_text(manual_terminal_check_interval_seconds, "manual_terminal_check_interval_seconds")

hotspot_failback_enabled = s:taboption("advanced", Flag, "hotspot_failback_enabled", "热点切换失败时自动回切校园网")
bind_flag(hotspot_failback_enabled, "hotspot_failback_enabled")

connectivity_check_mode = s:taboption("advanced", ListValue, "connectivity_check_mode", "在线判定方式")
connectivity_check_mode:value("internet", "互联网可达")
connectivity_check_mode:value("portal", "认证网关可达即可")
connectivity_check_mode:value("ssid", "仅关联到目标 SSID")
connectivity_check_mode.rmempty = false
bind_text(connectivity_check_mode, "connectivity_check_mode")

interval = s:taboption("advanced", Value, "interval", "检测间隔（秒）")
interval.datatype = "uinteger"
bind_text(interval, "interval")

log_text = s:taboption("log", DummyValue, "_log_text", "运行日志")
log_text.rawhtml = true
function log_text.cfgvalue(self, section)
    local t = sys.exec("tail -n 80 /var/log/smart_srun.log 2>/dev/null") or ""
    if t == "" then
        t = "暂无日志"
    end

    local escaped = util.pcdata and util.pcdata(t) or t
    return [[
<div style="display:flex;justify-content:flex-end;align-items:center;margin-bottom:6px;">
  <button id="smart-srun-refresh-toggle" type="button" class="cbi-button cbi-button-apply">刷新: 开</button>
</div>
<div id="smart-srun-log-box" style="max-height:420px;overflow:auto;border:1px solid #2b2b2b;padding:10px;background:#0b0f14;border-radius:4px;">
  <pre id="smart-srun-log-pre" style="margin:0;white-space:pre-wrap;word-break:break-all;color:#9ef19e;font-family:monospace;line-height:1.35;">]] .. escaped .. [[</pre>
</div>
]]
end

function m.parse(self, ...)
    changed = false
    dirty_scalar_keys = {}
    school_extra_dirty = false
    Map.parse(self, ...)
    if changed then
        save_cfg(cfg)
        m.uci:set("smart_srun", "main", "_stamp", tostring(os.time()))
        m.message = (m.message and (m.message .. "；") or "") .. "配置已保存到 JSON"
    end
end

function m.on_before_commit(self)
    sys.call("(sleep 1; /etc/init.d/smart_srun restart >/dev/null 2>&1) >/dev/null 2>&1 &")
end

return m
