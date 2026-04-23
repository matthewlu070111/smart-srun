module("luci.controller.smart_srun", package.seeall)

local http = require "luci.http"
local jsonc = require "luci.jsonc"
local sys = require "luci.sys"
local util = require "luci.util"
local fs = require "nixio.fs"
local schema = require "luci.smart_srun.schema"

local STATE_FILE = "/var/run/smart_srun/state.json"
local ACTION_FILE = "/var/run/smart_srun/action.json"
local LOG_FILE = "/var/log/smart_srun.log"
local restore_manual_guarded_enabled
local ACTION_STALE_SECONDS = 20
local LOG_TAIL_SOURCE_LINES = 2000

local NETWORK_EVENTS = {
    bind_ip_resolved = true,
    http_fetch = true,
    http_fetch_result = true,
    connectivity_probe_begin = true,
    connectivity_probe_result = true,
    dns_probe_failed = true,
    srun_challenge = true,
    srun_challenge_result = true,
    srun_login_submit = true,
    srun_login_response = true,
    srun_online_query = true,
    srun_online_result = true,
    ip_wait_progress = true,
    ip_wait_result = true,
    wifi_reload = true,
    sta_section_disabled = true,
    uci_wireless_update = true,
}

local SYSLOG_TAGS = {
    netifd = true,
    wpa_supplicant = true,
    hostapd = true,
    udhcpc = true,
}

local SYSLOG_KEYWORDS = {
    "authentication",
    "disconnected",
    "dhcp",
    "no lease",
}

local SYSLOG_LEVELS = {
    err = "ERROR",
    crit = "ERROR",
    alert = "ERROR",
    emerg = "ERROR",
    warn = "WARN",
    warning = "WARN",
    debug = "DEBUG",
}

local MONTH_MAP = {
    Jan = 1,
    Feb = 2,
    Mar = 3,
    Apr = 4,
    May = 5,
    Jun = 6,
    Jul = 7,
    Aug = 8,
    Sep = 9,
    Oct = 10,
    Nov = 11,
    Dec = 12,
}

function index()
    entry({"admin", "services", "smart_srun"}, cbi("smart_srun"), _("SMART SRun"), 80).dependent = true
    entry({"admin", "services", "smart_srun", "status"}, call("action_status")).leaf = true
    entry({"admin", "services", "smart_srun", "enqueue"}, call("action_enqueue")).leaf = true
    entry({"admin", "services", "smart_srun", "log_tail"}, call("action_log_tail")).leaf = true
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

local function remove_file(path)
    if fs.access(path) then
        fs.remove(path)
    end
end

local function collect_client_pids()
    local pids = {}
    local proc = fs.dir("/proc")
    if not proc then
        return pids
    end

    for entry in proc do
        if tostring(entry):match("^%d+$") then
            local cmdline = fs.readfile("/proc/" .. entry .. "/cmdline") or ""
            cmdline = cmdline:gsub("%z", " ")
            if cmdline:find("/usr/lib/smart_srun/client.py", 1, true) then
                pids[#pids + 1] = tostring(entry)
            end
        end
    end
    return pids
end

local function force_stop_client_processes()
    local pids = collect_client_pids()
    for _, pid in ipairs(pids) do
        sys.call("kill -TERM " .. pid .. " >/dev/null 2>&1")
    end
    for _, pid in ipairs(collect_client_pids()) do
        sys.call("kill -KILL " .. pid .. " >/dev/null 2>&1")
    end
    return pids
end

local function handle_force_stop()
    sys.call("/etc/init.d/smart_srun stop >/dev/null 2>&1")
    local killed = force_stop_client_processes()
    remove_file(ACTION_FILE)

    local state = read_json_file(STATE_FILE)
    restore_manual_guarded_enabled(state)
    state.message = "已强制关闭插件并停止服务"
    state.pending_action = ""
    state.last_action = "force_stop"
    state.last_action_ts = os.time()
    state.action_result = "forced"
    state.action_started_at = 0
    state.daemon_running = false
    write_json_file(STATE_FILE, state)

    return true, string.format("已强制关闭插件并停止服务（结束 %d 个进程）", #killed)
end

local function current_pending_runtime_action()
    local action = read_json_file(ACTION_FILE)
    local queued = tostring(action.action or "")
    if queued ~= "" then
        local requested_at = tonumber(action.requested_at) or 0
        if requested_at > 0 and (os.time() - requested_at) >= ACTION_STALE_SECONDS then
            remove_file(ACTION_FILE)
        else
            return queued
        end
    end

    local state = read_json_file(STATE_FILE)
    if tostring(state.action_result or "") == "pending" then
        local daemon_running = state.daemon_running and true or false
        local started_at = tonumber(state.action_started_at) or tonumber(state.last_action_ts) or 0
        if daemon_running or (started_at > 0 and (os.time() - started_at) < 15) then
            return tostring(state.pending_action or state.last_action or "")
        end
    end
    return ""
end

function action_status()
    local data = read_json_file(STATE_FILE)
    local action = read_json_file(ACTION_FILE)
    local text = tostring(data.message or "未知")
    local pending = current_pending_runtime_action()
    local last_log = util.trim(sys.exec("tail -n 1 " .. LOG_FILE .. " 2>/dev/null") or "")
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
        current_bssid = tostring(data.current_bssid or ""),
        current_ip = tostring(data.current_ip or ""),
        current_iface = tostring(data.current_iface or ""),
        campus_account_label = tostring(data.campus_account_label or ""),
        online_account_label = tostring(data.online_account_label or ""),
        hotspot_profile_label = tostring(data.hotspot_profile_label or ""),
        campus_ssid = tostring(data.campus_ssid or ""),
        campus_bssid = tostring(data.campus_bssid or ""),
        connectivity = tostring(data.connectivity or ""),
        connectivity_level = tostring(data.connectivity_level or "offline"),
        last_action = tostring(data.last_action or ""),
        action_result = tostring(data.action_result or ""),
        last_action_ts = tonumber(data.last_action_ts) or 0,
        action_started_at = tonumber(data.action_started_at) or 0,
        last_log = last_log,
        updated_at = tonumber(data.updated_at) or 0,
        ts = os.time(),
    }))
end

-- 表格 CRUD 需要的配置读写
local CONFIG_FILE = "/usr/lib/smart_srun/config.json"

local GLOBAL_SCALAR_KEYS_SET = schema.global_scalar_key_set()
local POINTER_KEYS_LIST = schema.POINTER_KEYS
local LIST_KEYS_LIST = schema.LIST_KEYS

local function load_config_json_unlocked()
    local raw = fs.readfile(CONFIG_FILE) or "{}"
    local parsed = jsonc.parse(raw)
    return type(parsed) == "table" and parsed or {}
end

local function write_config_json_unlocked(data)
    local tmp = CONFIG_FILE .. ".tmp"
    fs.writefile(tmp, (jsonc.stringify(data) or "{}") .. "\n")
    os.rename(tmp, CONFIG_FILE)
end

local function load_config_json()
    return schema.with_file_lock(CONFIG_FILE, function()
        return load_config_json_unlocked()
    end)
end

local function save_config_json(data)
    schema.with_file_lock(CONFIG_FILE, function()
        write_config_json_unlocked(data)
    end)
end

local function update_config_json(mutator)
    return schema.with_file_lock(CONFIG_FILE, function()
        local cfg = load_config_json_unlocked()
        local should_save, result = mutator(cfg)
        local payload = type(result) == "table" and result or cfg
        if should_save then
            write_config_json_unlocked(payload)
        end
        return payload, should_save
    end)
end

restore_manual_guarded_enabled = function(state)
    if type(state) ~= "table" or not state.manual_service_guard_active then
        return false
    end

    local previous_enabled = tostring(state.manual_service_enabled_before or "")
    if previous_enabled == "" then
        previous_enabled = "1"
    end

    update_config_json(function(cfg)
        cfg.enabled = previous_enabled
        return true, cfg
    end)
    state.manual_service_guard_active = false
    state.manual_service_enabled_before = ""
    return true
end

local function find_index_by_id(items, target_id)
    if type(items) ~= "table" then return nil end
    for i, item in ipairs(items) do
        if type(item) == "table" and tostring(item.id or "") == target_id then
            return i
        end
    end
    return nil
end

local function next_id(items, prefix)
    local max_num = 0
    if type(items) == "table" then
        for _, item in ipairs(items) do
            local ns = tostring(item.id or ""):match("^" .. prefix .. "%-(%d+)$")
            if ns then
                local n = tonumber(ns)
                if n and n > max_num then max_num = n end
            end
        end
    end
    return prefix .. "-" .. (max_num + 1)
end

local function fv(name)
    return tostring(http.formvalue(name) or ""):match("^%s*(.-)%s*$")
end

function action_enqueue()
    local action = fv("action")

    -- 原有的 daemon action 处理
    local daemon_actions = {
        switch_hotspot = "已提交切到热点请求，并已停用自动守护服务",
        switch_campus = "已提交切回校园网请求，并已停用自动守护服务",
        manual_login = "已提交手动登录请求",
        manual_logout = "已提交手动登出请求",
    }
    if action == "force_stop" then
        local ok_force, message_force = handle_force_stop()
        http.prepare_content("application/json")
        http.write(jsonc.stringify({ ok = ok_force, message = message_force, action = action, ts = os.time() }))
        return
    end

    if daemon_actions[action] then
        local pending = current_pending_runtime_action()
        if pending ~= "" then
            http.prepare_content("application/json")
            http.write(jsonc.stringify({
                ok = false,
                message = "已有动作正在执行: " .. pending .. "，请等待完成后再试",
                action = action,
                pending_action = pending,
                ts = os.time(),
            }))
            return
        end

        local requested_at = os.time()
        local state = read_json_file(STATE_FILE)
        if action == "switch_hotspot" or action == "switch_campus" then
            update_config_json(function(cfg)
                cfg.enabled = "0"
                return true, cfg
            end)
            state.enabled = false
        end
        write_json_file(ACTION_FILE, {
            action = action,
            requested_at = requested_at,
        })
        state.message = daemon_actions[action]
        state.pending_action = action
        state.last_action = action
        state.last_action_ts = requested_at
        state.action_result = "pending"
        state.action_started_at = requested_at
        state.updated_at = requested_at
        write_json_file(STATE_FILE, state)
        sys.call("(/etc/init.d/smart_srun restart >/dev/null 2>&1) >/dev/null 2>&1 &")
        http.prepare_content("application/json")
        http.write(jsonc.stringify({ ok = true, message = daemon_actions[action], requested_at = requested_at }))
        return
    end

    -- 表格 CRUD 操作
    local ok = false
    local message = "不支持的动作"
    local need_restart = false

    update_config_json(function(cfg)
        if type(cfg.campus_accounts) ~= "table" then cfg.campus_accounts = {} end
        if type(cfg.hotspot_profiles) ~= "table" then cfg.hotspot_profiles = {} end

        if action == "add_campus" or action == "edit_campus" then
            local id = fv("id")
            local item = {
                label = fv("label"), user_id = fv("user_id"),
                operator = fv("operator"), operator_suffix = fv("operator_suffix"),
                password = fv("password"),
                access_mode = fv("access_mode"),
                base_url = fv("base_url"), ac_id = fv("ac_id"),
                ssid = fv("ssid"), bssid = fv("bssid"), radio = fv("radio"),
            }
            if item.access_mode ~= "wired" then
                item.access_mode = "wifi"
            end
            if item.access_mode == "wired" then
                item.ssid = ""
                item.bssid = ""
                item.radio = ""
            end
            if item.label == "" then
                local suffix = item.operator_suffix or ""
                local op = item.operator or ""
                if suffix ~= "" and item.user_id ~= "" then
                    item.label = item.user_id .. "@" .. suffix
                elseif item.user_id ~= "" and op ~= "" and op ~= "xn" then
                    item.label = item.user_id .. "@" .. op
                elseif item.user_id ~= "" then
                    item.label = item.user_id
                else
                    item.label = "未命名账号"
                end
            end
            if action == "edit_campus" and id ~= "" then
                local idx = find_index_by_id(cfg.campus_accounts, id)
                if idx then
                    item.id = id
                    cfg.campus_accounts[idx] = item
                    ok = true; message = "已更新"; need_restart = true
                else
                    ok = false; message = "未找到 ID: " .. id
                end
            else
                item.id = next_id(cfg.campus_accounts, "campus")
                cfg.campus_accounts[#cfg.campus_accounts + 1] = item
                if #cfg.campus_accounts == 1 then
                    cfg.active_campus_id = item.id
                    cfg.default_campus_id = item.id
                end
                ok = true; message = "已添加"; need_restart = true
            end
            return ok, cfg
        end

        if action == "add_hotspot" or action == "edit_hotspot" then
            local id = fv("id")
            local item = {
                label = fv("label"), ssid = fv("ssid"),
                encryption = fv("encryption"), key = fv("key"),
                radio = fv("radio"),
            }
            if item.label == "" then
                item.label = item.ssid ~= "" and item.ssid or "未命名热点"
            end
            if action == "edit_hotspot" and id ~= "" then
                local idx = find_index_by_id(cfg.hotspot_profiles, id)
                if idx then
                    item.id = id
                    cfg.hotspot_profiles[idx] = item
                    ok = true; message = "已更新"; need_restart = true
                else
                    ok = false; message = "未找到 ID: " .. id
                end
            else
                item.id = next_id(cfg.hotspot_profiles, "hotspot")
                cfg.hotspot_profiles[#cfg.hotspot_profiles + 1] = item
                if #cfg.hotspot_profiles == 1 then
                    cfg.active_hotspot_id = item.id
                    cfg.default_hotspot_id = item.id
                end
                ok = true; message = "已添加"; need_restart = true
            end
            return ok, cfg
        end

        if action == "delete_campus" then
            local id = fv("id")
            local idx = find_index_by_id(cfg.campus_accounts, id)
            if idx then
                table.remove(cfg.campus_accounts, idx)
                if tostring(cfg.active_campus_id or "") == id then
                    cfg.active_campus_id = #cfg.campus_accounts > 0 and cfg.campus_accounts[1].id or ""
                end
                if tostring(cfg.default_campus_id or "") == id then
                    cfg.default_campus_id = cfg.active_campus_id
                end
                ok = true; message = "已删除"; need_restart = true
            else
                ok = false; message = "未找到"
            end
            return ok, cfg
        end

        if action == "delete_hotspot" then
            local id = fv("id")
            local idx = find_index_by_id(cfg.hotspot_profiles, id)
            if idx then
                table.remove(cfg.hotspot_profiles, idx)
                if tostring(cfg.active_hotspot_id or "") == id then
                    cfg.active_hotspot_id = #cfg.hotspot_profiles > 0 and cfg.hotspot_profiles[1].id or ""
                end
                if tostring(cfg.default_hotspot_id or "") == id then
                    cfg.default_hotspot_id = cfg.active_hotspot_id
                end
                ok = true; message = "已删除"; need_restart = true
            else
                ok = false; message = "未找到"
            end
            return ok, cfg
        end

        if action == "set_default_campus" then
            local id = fv("id")
            if find_index_by_id(cfg.campus_accounts, id) then
                cfg.default_campus_id = id
                ok = true; message = "已设为默认账号，手动登录后生效"
            else
                ok = false; message = "未找到"
            end
            return ok, cfg
        end

        if action == "set_default_hotspot" then
            local id = fv("id")
            if find_index_by_id(cfg.hotspot_profiles, id) then
                cfg.default_hotspot_id = id
                ok = true; message = "已设为默认热点，不会立即切换；如与当前连接不同，将显示为待生效"
            else
                ok = false; message = "未找到"
            end
            return ok, cfg
        end

        return false, cfg
    end)

    if ok then
        if need_restart then
            sys.call("(sleep 1; /etc/init.d/smart_srun restart >/dev/null 2>&1) >/dev/null 2>&1 &")
        end
    end

    http.prepare_content("application/json")
    http.write(jsonc.stringify({ ok = ok, message = message, action = action, ts = os.time() }))
end

-- Structured log translation table (event -> Chinese)
local event_zh = {
    login_success       = "登录成功",
    login_failed        = "登录失败",
    retry_scheduled     = "即将重试",
    retry_success       = "重试成功",
    retry_failed        = "重试失败",
    retry_stopped       = "停止重试",
    disconnect_detected = "检测到断线",
    status_check_error  = "状态检测异常",
    logout_request      = "正在登出",
    logout_success      = "登出成功",
    logout_failed       = "登出失败",
    logout_verify_failed = "登出校验失败",
    manual_login_start  = "开始手动登录",
    manual_login_success = "手动登录成功",
    manual_login_failed = "手动登录失败",
    manual_preclean     = "登录预清理",
    manual_preclean_done = "预清理完成",
    action_result       = "操作结果",
    action_started      = "开始执行动作",
    action_unknown      = "未知操作",
    switch_progress     = "切换进度",
    quiet_enter         = "进入夜间停用",
    quiet_exit          = "退出夜间停用",
    daemon_tick         = "状态更新",
    daemon_start        = "守护进程启动",
    daemon_stop         = "守护进程停止",
    switch_campus_done  = "已切换到校园网",
    switch_campus_no_ip = "校园网切换未获取IP",
    switch_hotspot_done = "已切换到热点",
    switch_hotspot_no_ip = "热点未获取IP",
    hotspot_failback    = "热点回退",
    config_migrated     = "配置已迁移",
    config_legacy_fix   = "修复遗留状态",
    config_default_applied = "应用默认配置",
    config_action_queued = "操作已入队",
    config_action_consumed = "操作已出队",
    config_loaded       = "配置已加载",
    status_query        = "状态查询",
    -- 重试与生命周期
    retry_cycle_start   = "进入重试循环",
    retry_cycle_end     = "重试循环结束",
    tick_begin          = "守护进程心跳",
    -- 网络底层
    bind_ip_resolved    = "绑定 IP 已解析",
    http_fetch          = "发起 HTTP 请求",
    http_fetch_result   = "HTTP 请求结果",
    connectivity_probe_begin = "开始连通性探测",
    connectivity_probe_result = "连通性探测结果",
    dns_probe_failed    = "DNS 解析失败",
    -- 无线模块
    wifi_reload         = "重载无线配置",
    sta_section_disabled = "已禁用 STA 配置段",
    ip_wait_progress    = "等待 IPv4 中",
    ip_wait_result      = "IPv4 等待结果",
    runtime_mode_detect = "检测运行模式",
    profile_rebuild     = "重建无线配置",
    uci_wireless_update = "更新 UCI 无线配置",
    switch_evaluate     = "评估切换决策",
    -- SRun 认证
    srun_challenge      = "请求 SRun 挑战码",
    srun_challenge_result = "SRun 挑战码结果",
    srun_login_submit   = "提交 SRun 登录",
    srun_login_response = "SRun 登录响应",
    srun_online_query   = "查询 SRun 在线状态",
    srun_online_result  = "SRun 在线状态结果",
    -- 学校运行时
    runtime_resolved    = "学校运行时已加载",
    runtime_hook        = "学校运行时钩子",
    runtime_dispatch    = "学校运行时派发",
}

-- Error reason translation (reuses server-side mapping)
local reason_zh = {
    username_or_password_error = "用户名或密码错误",
    ip_already_online_error    = "IP已在线",
    challenge_expire_error     = "挑战码已过期",
    sign_error                 = "签名错误",
    radius_error               = "RADIUS认证失败",
    login_error                = "认证失败",
}

-- Parse structured log line: "[ts] LEVEL EVENT k=v ... | msg"
local function parse_structured(line)
    local ts, rest = line:match("^(%[.-%]) (.+)$")
    if not rest then return nil end
    local level, event = rest:match("^(%u+) ([%w_]+)")
    if not level or not event then return nil end
    return ts, level, event, rest
end

-- Extract a key=value pair from structured log rest string.
-- Supports both unquoted (key=val) and quoted (key="val with spaces").
local function extract_kv(rest, key)
    local quoted = rest:match(key .. '="(.-)"')
    if quoted then return quoted end
    return rest:match(key .. "=(%S+)")
end

local function extract_structured_suffix(rest, level, event)
    local prefix = tostring(level or "") .. " " .. tostring(event or "")
    local text = tostring(rest or "")
    if text:sub(1, #prefix) ~= prefix then
        return ""
    end
    return util.trim(text:sub(#prefix + 1))
end

local switch_stage_zh = {
    applying = "应用无线配置",
    wait_ip  = "等待获取 IPv4",
    probe    = "检测连通性",
}

-- Translate a structured log line to user-friendly Chinese
function friendly_line(line)
    local ts, level, event, rest = parse_structured(line)
    if not ts then return line end
    local zh = event_zh[event]

    local parts = { ts, " " }
    if level == "ERROR" then
        parts[#parts + 1] = "[错误] "
    elseif level == "WARN" then
        parts[#parts + 1] = "[警告] "
    elseif level == "DEBUG" then
        parts[#parts + 1] = "[调试] "
    elseif level == "INFO" then
        parts[#parts + 1] = "[信息] "
    else
        parts[#parts + 1] = "[信息] "
    end
    parts[#parts + 1] = zh or event

    if not zh then
        local suffix = extract_structured_suffix(rest, level, event)
        if suffix ~= "" then
            parts[#parts + 1] = " " .. suffix
        end
        return table.concat(parts)
    end

    if event == "switch_progress" then
        local stage = extract_kv(rest, "stage")
        local stage_zh = stage and switch_stage_zh[stage]
        if stage_zh then parts[#parts + 1] = " · " .. stage_zh end
        local target = extract_kv(rest, "target")
        if target then parts[#parts + 1] = "（" .. target .. "）" end
        return table.concat(parts)
    end

    if event == "action_started" then
        local action = extract_kv(rest, "action")
        if action then parts[#parts + 1] = "：" .. action end
        return table.concat(parts)
    end

    local account = extract_kv(rest, "account")
    if account then parts[#parts + 1] = " [" .. account .. "]" end

    local reason = extract_kv(rest, "reason")
    if reason then
        local rzh = reason_zh[reason]
        parts[#parts + 1] = ": " .. (rzh or reason)
    end

    local attempt = rest:match("attempt=(%d+)")
    if attempt then parts[#parts + 1] = " (第" .. attempt .. "次)" end

    local detail = rest:match("|%s*(.+)$")
    if detail and not reason and not account then
        parts[#parts + 1] = ": " .. detail
    end

    return table.concat(parts)
end

function friendly_log_text(text)
    if not text or text == "" then
        return text or ""
    end

    local translated = {}
    for line in tostring(text):gmatch("[^\n]+") do
        translated[#translated + 1] = friendly_line(line)
    end
    return table.concat(translated, "\n")
end

local function structured_unix_ts(line)
    local y, m, d, hh, mm, ss = line:match("^%[(%d+)%-(%d+)%-(%d+) (%d+):(%d+):(%d+)%]")
    if not y then
        return nil
    end
    return os.time({
        year = tonumber(y),
        month = tonumber(m),
        day = tonumber(d),
        hour = tonumber(hh),
        min = tonumber(mm),
        sec = tonumber(ss),
    })
end

local function read_plugin_log_text(lines)
    return sys.exec("tail -n " .. lines .. " " .. LOG_FILE .. " 2>/dev/null") or ""
end

local function read_plugin_full_log_text()
    return fs.readfile(LOG_FILE) or ""
end

local function filter_text_since(text, since)
    if since <= 0 or text == "" then
        return text
    end

    local kept = {}
    for line in text:gmatch("[^\n]+") do
        local ts = structured_unix_ts(line)
        if ts and ts >= since then
            kept[#kept + 1] = line
        end
    end
    return table.concat(kept, "\n")
end

local function read_system_log_text(lines)
    local ok, text = pcall(sys.exec, "logread -l " .. lines .. " 2>/dev/null")
    if ok and text and text ~= "" then
        return text
    end
    ok, text = pcall(sys.exec, "logread 2>/dev/null | tail -n " .. lines)
    if ok and text then
        return text
    end
    return ""
end

local function parse_syslog_timestamp(line)
    local now = os.date("*t")
    local month_name, day, hh, mm, ss, year = line:match(
        "^%a+ (%a+) +(%d+) (%d+):(%d+):(%d+) (%d%d%d%d)%s+"
    )
    if not month_name then
        month_name, day, hh, mm, ss = line:match(
            "^%a+ (%a+) +(%d+) (%d+):(%d+):(%d+)%s+"
        )
        year = tostring(now.year)
    end
    if not month_name then
        return os.time(), true
    end

    local month = MONTH_MAP[month_name]
    if not month then
        return os.time(), true
    end

    local ts = os.time({
        year = tonumber(year),
        month = month,
        day = tonumber(day),
        hour = tonumber(hh),
        min = tonumber(mm),
        sec = tonumber(ss),
    })
    if not ts then
        return os.time(), true
    end
    return ts, false
end

local function parse_syslog_payload(line)
    local facility, severity, raw_tag, message = line:match(
        "^.- ([%w_%-]+)%.([%w_%-]+) ([^:]+):%s*(.*)$"
    )
    if not facility or not severity or not raw_tag then
        return nil
    end

    local tag = tostring(raw_tag):match("^([%w_%-]+)")
    if not tag or not SYSLOG_TAGS[tag] then
        return nil
    end

    return facility, severity, tag, message or ""
end

local function syslog_matches_context(line, state)
    local line_lower = tostring(line or ""):lower()
    local matched_context = false
    local values = {
        util.trim(tostring((state or {}).current_ssid or "")),
        util.trim(tostring((state or {}).current_bssid or "")),
        util.trim(tostring((state or {}).current_iface or "")),
    }

    for _, value in ipairs(values) do
        if value ~= "" then
            matched_context = true
            if line_lower:find(value:lower(), 1, true) then
                return true
            end
        end
    end

    if matched_context then
        return false
    end

    for _, keyword in ipairs(SYSLOG_KEYWORDS) do
        if line_lower:find(keyword, 1, true) then
            return true
        end
    end
    return false
end

local function extract_syslog_iface(message, state)
    local iface = tostring(message or ""):match("^([%w_.%-]+):")
    if iface and iface ~= "" then
        return iface
    end

    iface = tostring(message or ""):match("Interface '([%w_.%-]+)'")
    if iface and iface ~= "" then
        return iface
    end

    local current_iface = util.trim(tostring((state or {}).current_iface or ""))
    if current_iface ~= "" and tostring(message or ""):find(current_iface, 1, true) then
        return current_iface
    end

    return nil
end

local function build_system_log_entry(line, state, order)
    local _, severity, tag, message = parse_syslog_payload(line)
    if not tag or not syslog_matches_context(line, state) then
        return nil
    end

    local ts, unparsed = parse_syslog_timestamp(line)
    local parts = {
        "[",
        os.date("%Y-%m-%d %H:%M:%S", ts),
        "] ",
        SYSLOG_LEVELS[tostring(severity or ""):lower()] or "INFO",
        " syslog_",
        tag,
    }
    local iface = extract_syslog_iface(message, state)
    if iface and iface ~= "" then
        parts[#parts + 1] = " iface=" .. iface
    end
    parts[#parts + 1] = " source=system"
    if unparsed then
        parts[#parts + 1] = " unparsed=1"
    end
    parts[#parts + 1] = " | " .. tostring(message or "")

    return {
        line = table.concat(parts),
        ts = ts,
        source_priority = 1,
        order = order,
    }
end

local function trim_entries(entries, lines)
    if #entries <= lines then
        return entries
    end
    local trimmed = {}
    for idx = #entries - lines + 1, #entries do
        trimmed[#trimmed + 1] = entries[idx]
    end
    return trimmed
end

local function resolve_network_source_lines(lines, download_mode)
    if download_mode then
        return LOG_TAIL_SOURCE_LINES
    end

    local requested = tonumber(lines) or 0
    if requested < 10 then
        requested = 10
    end

    local source_lines = requested * 4
    if source_lines < 200 then
        source_lines = 200
    elseif source_lines > LOG_TAIL_SOURCE_LINES then
        source_lines = LOG_TAIL_SOURCE_LINES
    end
    return source_lines
end

local function build_network_log_text(lines, since, source_lines)
    local entries = {}
    local order_counter = 0
    local plugin_text = read_plugin_log_text(source_lines)
    for line in plugin_text:gmatch("[^\n]+") do
        local _, _, event = parse_structured(line)
        if event and NETWORK_EVENTS[event] then
            order_counter = order_counter + 1
            entries[#entries + 1] = {
                line = line,
                ts = structured_unix_ts(line) or os.time(),
                source_priority = 0,
                order = order_counter,
            }
        end
    end

    local state = read_json_file(STATE_FILE)
    local system_text = read_system_log_text(source_lines)
    for line in system_text:gmatch("[^\n]+") do
        order_counter = order_counter + 1
        local entry = build_system_log_entry(line, state, order_counter)
        if entry then
            entries[#entries + 1] = entry
        end
    end

    table.sort(entries, function(left, right)
        if left.ts ~= right.ts then
            return left.ts < right.ts
        end
        if left.source_priority ~= right.source_priority then
            return left.source_priority < right.source_priority
        end
        return (left.order or 0) < (right.order or 0)
    end)

    if since > 0 then
        local kept = {}
        for _, entry in ipairs(entries) do
            if (entry.ts or 0) >= since then
                kept[#kept + 1] = entry
            end
        end
        entries = kept
    end

    entries = trim_entries(entries, lines)

    local output = {}
    for _, entry in ipairs(entries) do
        output[#output + 1] = entry.line
    end
    return table.concat(output, "\n")
end

local function build_log_text(channel, lines, since, download_mode)
    if download_mode then
        if channel == "network" then
            return build_network_log_text(
                LOG_TAIL_SOURCE_LINES,
                since,
                resolve_network_source_lines(lines, download_mode)
            )
        end
        return read_plugin_full_log_text()
    end

    if channel == "network" then
        return build_network_log_text(
            lines,
            since,
            resolve_network_source_lines(lines, download_mode)
        )
    end
    return filter_text_since(read_plugin_log_text(lines), since)
end

function action_log_tail()
    local since = tonumber(http.formvalue("since")) or 0
    local lines = tonumber(http.formvalue("lines")) or 1000
    local fmt = http.formvalue("format") or "raw"
    local channel = http.formvalue("channel") or "plugin"
    local download_mode = tostring(http.formvalue("download") or "") == "1"
    channel = channel == "network" and "network" or "plugin"
    if not download_mode then
        if lines < 10 then
            lines = 10
        elseif lines > 1000 then
            lines = 1000
        end
    end

    local text = build_log_text(channel, lines, since, download_mode)

    if fmt == "friendly" and text ~= "" then
        text = friendly_log_text(text)
    end

    local empty = (text == "")
    if empty then
        text = "No logs yet."
    end

    http.prepare_content("application/json")
    http.write(jsonc.stringify({
        log = text,
        empty = empty,
        channel = channel,
        ts = os.time(),
    }))
end
