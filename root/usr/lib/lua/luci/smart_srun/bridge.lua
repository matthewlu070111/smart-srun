-- Translation between the frozen interface and the Go configuration contract.
--
-- The page keeps the baseline's flat, UCI-style field names; the daemon owns a
-- typed config v2. One of them has to convert, and it is this file, in one
-- place, using the destinations recorded in the M00 field mapping. Doing it in
-- the controller and the CBI page separately is how two halves of one form end
-- up disagreeing about what "quiet_hours_enabled" means.
--
-- Everything above `-- calls` is pure: tables in, tables out. That is
-- deliberate, so the mapping can be exercised without a socket or a daemon.

local rpc = require "luci.smart_srun.rpc"

local M = {}

-- Read the local catalogue completely. Rendering and task polling never fetch
-- remote data; presets.refresh is the separate bounded network operation.
function M.presets()
    local schools, offset, revision = {}, 0, nil
    for page_number = 1, 128 do
        local page, err = rpc.call("presets.list", { include_inactive = false, offset = offset, limit = 100 })
        if not page then return nil, err end
        if revision and revision ~= page.revision then
            return nil, { code = "Conflict", message = "预设读取期间发生编辑，请刷新页面" }
        end
        revision = page.revision
        for _, group in ipairs({ page.public or {}, page.user or {} }) do
            for _, item in ipairs(group) do schools[#schools + 1] = item end
        end
        if page.next_offset == nil then return schools end
        local next_offset = tonumber(page.next_offset)
        if not next_offset or next_offset <= offset then
            return nil, { code = "ProtocolInvalid", message = "预设分页响应无效" }
        end
        offset = next_offset
    end
    return nil, { code = "ProtocolInvalid", message = "预设超过分页上限" }
end

-- Baseline scalar -> config v2 destination, from
-- .codex/go-loop/baseline/field-mapping.json. Keys the mapping records as
-- dropped (developer_mode, the three backoff factors, the two superseded
-- durations) are absent on purpose: they have no v2 destination, and inventing
-- one here would resurrect a knob the decisions removed.
local SCALARS = {
    enabled                                = { path = { "enabled" }, kind = "bool" },
    multi_wan_enabled                      = { path = { "multi_wan_enabled" }, kind = "bool" },
    school                                 = { path = { "school" }, kind = "string" },
    -- The one string the user may legitimately clear: no STA interface named
    -- means "let the runtime pick", which is a choice, not a missing value.
    sta_iface                              = { path = { "sta_iface" }, kind = "string", empty_ok = true },
    quiet_hours_enabled                    = { path = { "quiet", "enabled" }, kind = "bool" },
    quiet_start                            = { path = { "quiet", "start" }, kind = "string" },
    quiet_end                              = { path = { "quiet", "end" }, kind = "string" },
    preset_auto_update_enabled             = { path = { "preset_updates", "enabled" }, kind = "bool" },
    preset_update_time                     = { path = { "preset_updates", "time" }, kind = "string" },
    force_logout_in_quiet                  = { path = { "quiet", "force_logout" }, kind = "bool" },
    failover_enabled                       = { path = { "failover", "enabled" }, kind = "bool" },
    hotspot_failback_enabled               = { path = { "failover", "hotspot_failback_enabled" }, kind = "bool" },
    backoff_enable                         = { path = { "retry", "enabled" }, kind = "bool" },
    backoff_max_retries                    = { path = { "retry", "max_retries" }, kind = "int" },
    retry_cooldown_seconds                 = { path = { "retry", "initial_seconds" }, kind = "number" },
    retry_max_cooldown_seconds             = { path = { "retry", "max_seconds" }, kind = "number" },
    interval                               = { path = { "checks", "interval_seconds" }, kind = "int" },
    connectivity_check_mode                = { path = { "checks", "mode" }, kind = "string" },
    switch_ready_timeout_seconds           = { path = { "checks", "switch_timeout_seconds" }, kind = "int" },
    manual_terminal_check_max_attempts     = { path = { "checks", "terminal_attempts" }, kind = "int" },
    manual_terminal_check_interval_seconds = { path = { "checks", "terminal_interval_seconds" }, kind = "int" },
    log_level                              = { path = { "log", "level" }, kind = "string" },
    n                                      = { path = { "login_defaults", "n" }, kind = "string" },
    type                                   = { path = { "login_defaults", "type" }, kind = "string" },
    enc                                    = { path = { "login_defaults", "enc" }, kind = "string" },
}

M.SCALARS = SCALARS

local POINTERS = {
    active_campus_id = "active_campus_id",
    default_campus_id = "default_campus_id",
    active_hotspot_id = "active_hotspot_id",
    default_hotspot_id = "default_hotspot_id",
}

local function trim(value)
    return tostring(value or ""):match("^%s*(.-)%s*$")
end

local function truthy(value)
    local text = trim(value):lower()
    return text == "1" or text == "true" or text == "yes" or text == "on"
end

local function read_path(root, path)
    local node = root
    for _, key in ipairs(path) do
        if type(node) ~= "table" then
            return nil
        end
        node = node[key]
    end
    return node
end

local function write_path(root, path, value)
    local node = root
    for index = 1, #path - 1 do
        local key = path[index]
        if type(node[key]) ~= "table" then
            node[key] = {}
        end
        node = node[key]
    end
    node[path[#path]] = value
end

-- to_display renders a typed v2 value in the string form the frozen page reads.
local function to_display(value, kind)
    if kind == "bool" then
        return value and "1" or "0"
    end
    if value == nil then
        return ""
    end
    -- Lua 5.1 prints an integral double without a fractional part, so a whole
    -- number of seconds reaches the form as "60" rather than "60.0".
    return tostring(value)
end

-- to_typed is the other direction, and refuses rather than guesses: a number
-- field whose text is not a number keeps its stored value instead of silently
-- becoming zero, which the daemon would accept as a valid setting.
local function to_typed(text, kind)
    if kind == "bool" then
        return truthy(text)
    end
    if kind == "int" then
        local number = tonumber(trim(text))
        if not number then return nil end
        return math.floor(number)
    end
    if kind == "number" then
        return tonumber(trim(text))
    end
    return tostring(text or "")
end

-- account_view renders one v2 account as the baseline item the table, the edit
-- dialog and the wizard all read.
--
-- Secrets are not part of it. config.get redacts them, and the page has no use
-- for a credential it never displays: the edit dialog submits an empty password
-- field, and `campus_patch` reads that as "keep the stored one".
local function account_view(account)
    local login = type(account.login) == "table" and account.login or {}
    local double_stack = ""
    if login.double_stack ~= nil then
        double_stack = login.double_stack and "1" or "0"
    end
    return {
        id = tostring(account.id or ""),
        label = tostring(account.label or ""),
        user_id = tostring(account.user_id or ""),
        password = "",
        operator = tostring(account.operator or ""),
        operator_suffix = tostring(account.operator_suffix or ""),
        access_mode = tostring(account.access_mode or "wired"),
        wired_iface = tostring(account.wired_iface or ""),
        auth_enabled = account.auth_enabled and "1" or "0",
        base_url = tostring(account.base_url or ""),
        ac_id = tostring(account.ac_id or ""),
        ssid = tostring(account.ssid or ""),
        radio = tostring(account.radio or ""),
        encryption = tostring(account.encryption or ""),
        key = "",
        ap_selection = tostring(account.ap_selection or ""),
        bssid = tostring(account.bssid or ""),
        n = tostring(login.n or ""),
        type = tostring(login.type or ""),
        enc = tostring(login.enc or ""),
        info_prefix = tostring(login.info_prefix or ""),
        double_stack = double_stack,
        login_os = tostring(login.os or ""),
        login_name = tostring(login.name or ""),
        preset_id = tostring(account.preset_id or ""),
    }
end

local function hotspot_view(hotspot)
    return {
        id = tostring(hotspot.id or ""),
        label = tostring(hotspot.label or ""),
        ssid = tostring(hotspot.ssid or ""),
        encryption = tostring(hotspot.encryption or ""),
        key = "",
        radio = tostring(hotspot.radio or ""),
    }
end

-- flatten turns one config.get result into the baseline-shaped table the page
-- has always worked with.
function M.flatten(config)
    local flat = {}
    for key, spec in pairs(SCALARS) do
        flat[key] = to_display(read_path(config, spec.path), spec.kind)
    end
    local selection = type(config.selection) == "table" and config.selection or {}
    for legacy, field in pairs(POINTERS) do
        flat[legacy] = tostring(selection[field] or "")
    end

    flat.campus_accounts = {}
    for _, account in ipairs(config.campus_accounts or {}) do
        flat.campus_accounts[#flat.campus_accounts + 1] = account_view(account)
    end
    flat.hotspot_profiles = {}
    for _, hotspot in ipairs(config.hotspot_profiles or {}) do
        flat.hotspot_profiles[#flat.hotspot_profiles + 1] = hotspot_view(hotspot)
    end
    flat.school_extra = type(config.school_extra) == "table" and config.school_extra or {}
    flat.revision = tonumber(config.revision) or 0
    return flat
end

-- settings_patch builds the config.apply body for the fields a save touched.
--
-- Only the dirty ones. A settings form that sent every field would overwrite
-- values another page, the CLI or the wizard changed while this one was open,
-- and it would do it without the user having touched the control.
function M.settings_patch(flat, dirty)
    local patch = {}
    local touched = false
    for key in pairs(dirty or {}) do
        local spec = SCALARS[key]
        if spec then
            local value = to_typed(flat[key], spec.kind)
            -- A field the form did not submit arrives here as an empty string,
            -- because that is how the CBI reports "removed". For everything
            -- except sta_iface an empty value is not a setting the daemon can
            -- hold -- a clock with no time, an enum with no choice -- and
            -- sending it would fail the whole save, including the fields the
            -- user did change. Absent stays absent instead.
            if value == "" and not spec.empty_ok then
                value = nil
            end
            if value ~= nil then
                write_path(patch, spec.path, value)
                touched = true
            end
        end
    end
    if dirty and dirty.school_extra then
        local extra = type(flat.school_extra) == "table" and flat.school_extra or {}
        -- An empty Lua table encodes as a JSON array, which is not a map. The
        -- sentinel survives encoding and becomes `{}` on the wire.
        patch.school_extra = next(extra) == nil and rpc.EMPTY_OBJECT or extra
        touched = true
    end
    if not touched then
        return nil
    end
    return patch
end

-- campus_patch turns submitted form values into a v2 account patch.
--
-- Presence is the whole point: a field the dialog did not submit is left out,
-- so it keeps its stored value. The password is the case that matters. The
-- dialog cannot show it, so an empty box means "unchanged" on an edit, and only
-- a new account stores the empty string it was given.
function M.campus_patch(form, options)
    options = options or {}
    local creating = options.creating and true or false
    local access_mode = trim(form.access_mode) == "wired" and "wired" or "wifi"

    local patch = {
        label = tostring(form.label or ""),
        user_id = tostring(form.user_id or ""),
        operator = tostring(form.operator or ""),
        operator_suffix = tostring(form.operator_suffix or ""),
        access_mode = access_mode,
        base_url = tostring(form.base_url or ""),
        ac_id = tostring(form.ac_id or ""),
        login = {
            n = tostring(form.n or ""),
            type = tostring(form.type or ""),
            enc = tostring(form.enc or ""),
            info_prefix = tostring(form.info_prefix or ""),
            os = tostring(form.login_os or ""),
            name = tostring(form.login_name or ""),
        },
    }
    if not creating then
        patch.id = tostring(form.id or "")
    end

    local double_stack = trim(form.double_stack)
    if double_stack == "" then
        -- Explicit null, which the account patch accepts on this one path: it
        -- clears the override so the strategy default applies again. Leaving the
        -- field out would silently keep the previous override instead.
        patch.login.double_stack = rpc.NULL
    else
        patch.login.double_stack = truthy(double_stack)
    end

    local password = tostring(form.password or "")
    if creating or password ~= "" then
        patch.password = password
    end

    if access_mode == "wired" then
        local iface = trim(form.wired_iface)
        if iface == "" then
            -- PR #32 called this field network_interface. Read the alias, write
            -- only wired_iface.
            iface = trim(form.network_interface)
        end
        patch.wired_iface = iface ~= "" and iface or "wan"
        patch.auth_enabled = truthy(form.auth_enabled)
        -- The wireless half is cleared rather than left behind, so a stored
        -- account never carries an effective setting for the mode it is not in.
        patch.ssid, patch.bssid, patch.radio, patch.ap_selection = "", "", "", "auto"
    else
        patch.auth_enabled = false
        patch.ssid = tostring(form.ssid or "")
        patch.radio = tostring(form.radio or "")
        patch.bssid = trim(form.bssid):lower()
        patch.ap_selection = M.normalize_ap_selection(form.ap_selection, patch.bssid)
        if options.encryption ~= nil then
            patch.encryption = tostring(options.encryption)
        end
        if options.key ~= nil then
            patch.key = tostring(options.key)
        end
        if creating and options.encryption == nil then
            -- A new wireless account needs an encryption mode; the campus dialog
            -- has never had that control, and the validator requires one.
            patch.encryption = "none"
        end
    end
    return patch
end

function M.hotspot_patch(form, options)
    options = options or {}
    local patch = {
        label = tostring(form.label or ""),
        ssid = tostring(form.ssid or ""),
        encryption = tostring(form.encryption or ""),
        radio = tostring(form.radio or ""),
    }
    if not options.creating then
        patch.id = tostring(form.id or "")
    end
    local key = tostring(form.key or "")
    if options.creating or key ~= "" then
        patch.key = key
    end
    return patch
end

-- normalize_ap_selection is the baseline rule, kept here so the controller and
-- the CBI page share one copy: an unrecognised policy means "fixed" when a
-- BSSID was given and "auto" otherwise.
function M.normalize_ap_selection(value, bssid)
    local policy = trim(value):lower()
    if policy == "auto" or policy == "strongest" or policy == "fixed" then
        return policy
    end
    return trim(bssid) ~= "" and "fixed" or "auto"
end

-- default_label reproduces the baseline's fallback naming.
function M.default_label(user_id, suffix, fallback)
    local id, tail = trim(user_id), trim(suffix)
    if id ~= "" and tail ~= "" then
        return id .. "@" .. tail
    end
    if id ~= "" then
        return id
    end
    return fallback
end

-- utc_seconds converts an RFC 3339 timestamp to a second count.
--
-- Its own civil-date arithmetic rather than os.time, which would interpret the
-- fields in the router's local zone. The absolute value is never displayed:
-- only differences between two of these are, and a wrong zone would make an
-- action look minutes old the moment it finished.
function M.utc_seconds(text)
    local year, month, day, hour, minute, second =
        tostring(text or ""):match("^(%d+)-(%d+)-(%d+)[Tt](%d+):(%d+):(%d+)")
    if not year then
        return nil
    end
    year, month, day = tonumber(year), tonumber(month), tonumber(day)
    hour, minute, second = tonumber(hour), tonumber(minute), tonumber(second)
    -- Days from the civil date, shifting the year to start in March so the leap
    -- day is the last day of it and no month-length table is needed.
    local y = year - (month <= 2 and 1 or 0)
    local era = math.floor(y / 400)
    local year_of_era = y - era * 400
    local day_of_year = math.floor((153 * (month + (month > 2 and -3 or 9)) + 2) / 5) + day - 1
    local day_of_era = year_of_era * 365 + math.floor(year_of_era / 4)
        - math.floor(year_of_era / 100) + day_of_year
    local days = era * 146097 + day_of_era - 719468
    return days * 86400 + hour * 3600 + minute * 60 + second
end

local AUTH_TEXT = {
    Unknown = "状态未知",
    Authenticating = "正在认证",
    Accepted = "已提交认证",
    VerifiedSelf = "已认证",
    VerifiedOther = "线路上是其他账号",
    Rejected = "认证被拒绝",
    Offline = "未认证",
}

local LINK_TEXT = {
    Missing = "接口不存在",
    LinkDown = "链路未就绪",
    AddressPending = "等待 IPv4 地址",
}

local CONNECTIVITY = {
    InternetReachable = { text = "互联网可达", level = "online" },
    PortalReachable = { text = "认证网关可达", level = "portal" },
    Limited = { text = "已连接但受限", level = "limited" },
    Offline = { text = "未连接", level = "offline" },
    Unknown = { text = "未探测", level = "offline" },
}

local ACTION_RESULT = {
    succeeded = "ok",
    failed = "error",
    -- A cancelled or interrupted action is what the frozen page calls "forced":
    -- the force-stop button cancels, and a stopped service interrupts. Neither
    -- is a failed attempt, and showing them as one would tell a user their
    -- password was wrong when they pressed stop.
    cancelled = "forced",
    interrupted = "forced",
}

-- Only explicit login/logout/switch actions own the frozen progress dialog.
-- Maintenance, preset refresh and wizard jobs expose their results elsewhere.
local FEEDBACK_ACTION = {
    manual_login = true, manual_logout = true, relogin = true,
    switch_campus = true, switch_hotspot = true,
}

local function account_by_id(config, id)
    for _, account in ipairs(config.campus_accounts or {}) do
        if account.id == id then
            return account
        end
    end
    return nil
end

-- portal_origin reduces a configured gateway address to a bare HTTP(S)
-- origin, or "" when it is not one.
--
-- 1.6.1's rule, kept: scheme and host (IPv6 in brackets, or letters, digits,
-- dots and hyphens) with an optional port, nothing else. No userinfo, no path,
-- no whitespace, control characters or backslashes -- the last because browsers
-- disagree about what a backslash in an authority means.
local function portal_origin(value)
    -- Schemes are case-insensitive; the answer is always written lower-case.
    local scheme, rest = trim(value):match("^(%a+)://(.*)$")
    scheme = scheme and scheme:lower()
    if scheme ~= "http" and scheme ~= "https" then return "" end
    local authority = rest:match("^([^/%?#]+)")
    if not authority or authority:find("[%s%c\\@]") then return "" end
    local host, port = authority:match("^(%[[%x:]+%])(.*)$")
    if not host then host, port = authority:match("^([%w%.%-]+)(.*)$") end
    if not host or not (port == "" or port:match("^:%d%d?%d?%d?%d?$")) then return "" end
    return scheme:lower() .. "://" .. authority
end

-- failed_login_portal answers the page's "open the school's page" link.
--
-- Only after a failed manual login, as in 1.6.1, and only from the account that
-- action was for -- not whichever account is active by the time the page asks.
-- The address is the one the user configured. A gateway's response body never
-- supplies a URL here.
local function failed_login_portal(action, config)
    if type(action) ~= "table" or tostring(action.kind or "") ~= "manual_login"
        or tostring(action.state or "") ~= "failed" then
        return ""
    end
    local account = account_by_id(config or {}, tostring(action.account_id or ""))
    return account and portal_origin(account.base_url) or ""
end

M.portal_origin = portal_origin

local function view_by_id(snapshot, id)
    for _, view in ipairs(snapshot.accounts or {}) do
        if view.account_id == id then
            return view
        end
    end
    return nil
end

-- wired_session_view rebuilds the per-account badge input the account table
-- reads, from the projection the daemon publishes for every account.
local function wired_session_view(view)
    if not view then
        return { online = false, status = "" }
    end
    local auth = tostring(view.auth or "")
    local status = ""
    if auth == "VerifiedOther" then
        status = "account_mismatch"
    elseif auth == "Rejected" then
        status = "error"
    elseif tostring(view.link or "") == "Missing" then
        status = "config_error"
    end
    return { online = auth == "VerifiedSelf", status = status }
end

-- mode_of decides campus or hotspot from what actually happened.
--
-- The last switch that succeeded, not the last one submitted: an attempt that
-- failed left the uplink where it was, and reporting the requested mode would
-- describe a network the device is not on.
local function mode_of(snapshot, account)
    local wireless = type(snapshot.wireless) == "table" and snapshot.wireless or {}
    if wireless.state == "associated" then
        if tostring(wireless.hotspot_id or "") ~= "" then return "hotspot" end
        if account and account.access_mode ~= "wired"
            and wireless.radio == account.radio and wireless.ssid == account.ssid then
            return "campus"
        end
    end
    local mode = "campus"
    for _, action in ipairs(snapshot.actions or {}) do
        if action.state == "succeeded" and action.ended_at
            and (action.kind == "switch_campus" or action.kind == "switch_hotspot") then
            -- Retained terminals are ordered by retirement, including when
            -- the router's wall clock moves backwards during a switch.
            mode = action.kind == "switch_hotspot" and "hotspot" or "campus"
        end
    end
    return mode
end

local function hotspot_status(wireless)
    if wireless.state == "disconnected" then
        return "热点未连接", CONNECTIVITY.Offline
    elseif wireless.state == "unavailable" or wireless.state == "ambiguous" then
        return "无法读取热点连接", CONNECTIVITY.Unknown
    elseif wireless.state ~= "associated" then
        return "正在读取热点状态", CONNECTIVITY.Unknown
    elseif tostring(wireless.address or "") == "" then
        return "热点等待 IP 地址", CONNECTIVITY.Unknown
    end
    local level = tostring(wireless.connectivity or "")
    if level == "InternetReachable" then
        return "热点已联网", CONNECTIVITY.InternetReachable
    elseif level == "Limited" then
        return "热点联网受限", CONNECTIVITY.Limited
    elseif level == "Offline" then
        return "热点联网检测失败", { text = "互联网探测未通过", level = "offline" }
    end
    return "热点已连接", { text = "等待联网检测", level = "offline" }
end

-- status_view is the one combined answer the page polls.
--
-- Connection details come from the daemon's bounded, passive wireless cache.
-- Configured SSIDs and BSSIDs are never used as proof of an association.
function M.status_view(snapshot, config, now)
    now = now or os.time()
    local running = tostring(snapshot.service or "") == "running"
    local selection = type(config.selection) == "table" and config.selection or {}
    local active_id = tostring(selection.active_campus_id or "")
    local account = account_by_id(config, active_id)
    local view = view_by_id(snapshot, active_id)
    local wireless = type(snapshot.wireless) == "table" and snapshot.wireless or {}
    local mode = mode_of(snapshot, account)

    local wired = account ~= nil and tostring(account.access_mode or "") == "wired"
    local mode_label = "未知模式"
    local access_mode = ""
    if mode == "hotspot" then
        mode_label = "热点模式"
    elseif account then
        mode_label = wired and "校园网模式（有线）" or "校园网模式"
        access_mode = wired and "wired" or "wifi"
    end

    local connectivity = CONNECTIVITY[tostring(view and view.connectivity or "")] or CONNECTIVITY.Unknown
    local status = "未知"
    if not running then
        status = "认证服务已停止"
        connectivity = CONNECTIVITY.Unknown
    elseif mode == "hotspot" then
        status, connectivity = hotspot_status(wireless)
    elseif not account then
        status = "尚未配置校园网账号"
    elseif not view then
        status = snapshot.enabled and "等待首次检测" or "自动认证已关闭"
    else
        status = LINK_TEXT[tostring(view.link or "")] or AUTH_TEXT[tostring(view.auth or "")] or "状态未知"
    end

    -- Keep background work visible in the overview without letting it replace
    -- a manual result or hold that action's progress dialog open.
    local pending, feedback_pending, last, last_ended = "", false, nil, nil
    for _, action in ipairs(snapshot.actions or {}) do
        local state = tostring(action.state or "")
        local feedback = FEEDBACK_ACTION[tostring(action.kind or "")]
        if state == "queued" or state == "running" then
            if pending == "" then
                pending = tostring(action.kind or "")
            end
            if feedback then feedback_pending = true end
        elseif feedback and ACTION_RESULT[state] then
            last, last_ended = action, M.utc_seconds(action.ended_at)
        end
    end
    -- A new manual request clears the old terminal message until it completes.
    if feedback_pending then last, last_ended = nil, nil end

    local written = M.utc_seconds(snapshot.written_at)
    local last_action_ts = 0
    if last_ended and written then
        -- Display only. Wall clocks can move backwards; the dialog uses the
        -- receipt ID and never uses this timestamp as proof of completion.
        last_action_ts = now - (written - last_ended)
    end

    local link_ready = view ~= nil and tostring(view.link or "") == "Ready"
    local line = mode ~= "hotspot" and type(view) == "table" and type(view.line) == "table" and view.line or {}
    -- Quiet hours suspend a service the user has switched on; the page has
    -- always shown that separately from the switch itself.
    local paused = {}
    for _, reason in ipairs(snapshot.pause or {}) do
        paused[tostring(reason)] = true
    end
    local payload = {
        status = status,
        enabled = snapshot.enabled and true or false,
        service = running and "running" or "stopped",
        mode = mode,
        current_mode = mode,
        mode_label = mode_label,
        pending_action = pending,
        current_campus_access_mode = access_mode,
        campus_account_label = account and tostring(account.label or "") or "",
        online_account_label = mode ~= "hotspot" and view and tostring(view.identity or "") or "",
        campus_ssid = account and tostring(account.ssid or "") or "",
        campus_bssid = account and tostring(account.bssid or "") or "",
        current_ssid = "",
        current_bssid = "",
        current_wireless_ifname = "",
        -- The line the last attempt actually reached the network on, not the
        -- one the account is configured for.
        current_iface = tostring(line.iface or ""),
        current_ip = tostring(line.address or ""),
        current_device = tostring(line.device or ""),
        in_quiet = paused["QuietHours"] and true or false,
        paused = next(paused) ~= nil,
        ap_selection_policy = "",
        ap_selection_reason = "",
        connectivity = connectivity.text,
        connectivity_level = connectivity.level,
        hotspot_profile_label = "",
        wired_auth_sessions = {},
        last_action = last and tostring(last.kind or "") or "",
        last_action_message = last and tostring(last.message or "") or "",
        last_action_portal_url = failed_login_portal(last, config),
        action_result = last and ACTION_RESULT[tostring(last.state or "")] or "",
        last_action_ts = last_action_ts,
        action_started_at = feedback_pending and now or 0,
        config_revision = tonumber(snapshot.config_revision) or 0,
        last_log = "",
        updated_at = now,
        ts = now,
    }
    if feedback_pending then
        payload.action_result = "pending"
    end
    -- The observed line is preferred; the configured one only stands in while
    -- the link is ready and nothing has reported a device yet, which is the
    -- first moment after a save.
    if mode ~= "hotspot" and payload.current_iface == "" and link_ready and account and wired then
        payload.current_iface = tostring(account.wired_iface or "")
    end
    if running and (mode == "hotspot" or (account and not wired)) then
        payload.current_wireless_ifname = tostring(wireless.device or "")
        payload.ap_selection_policy = mode == "hotspot" and "auto"
            or M.normalize_ap_selection(account.ap_selection, account.bssid)
        local reasons = {
            disconnected = "尚未连接无线网络",
            unavailable = "暂时无法读取无线状态",
            ambiguous = "检测到多个上联客户端，请检查无线配置",
        }
        payload.ap_selection_reason = reasons[wireless.state] or "等待无线状态检测"
        if wireless.state == "associated" then
            payload.current_ssid = tostring(wireless.ssid or "")
            payload.current_bssid = tostring(wireless.bssid or "")
            payload.current_iface = tostring(wireless.iface or "")
            payload.current_device = tostring(wireless.device or "")
            payload.current_ip = tostring(wireless.address or "")
            local signal, channel = tonumber(wireless.signal), tonumber(wireless.channel)
            payload.current_signal = signal and signal < 0 and signal >= -127 and signal or nil
            payload.current_channel = channel and channel > 0 and channel or nil
            if mode ~= "hotspot" and payload.current_ssid ~= tostring(account.ssid or "") then
                payload.ap_selection_reason = "当前无线连接与所选校园网配置不同"
            elseif payload.ap_selection_policy == "fixed" then
                payload.ap_selection_reason = payload.current_bssid == tostring(account.bssid or ""):lower()
                    and "已连接指定接入点" or "当前接入点与固定 BSSID 不同"
            elseif payload.ap_selection_policy == "strongest" then
                payload.ap_selection_reason = "连接时优先信号较强的接入点，保持当前连接"
            else
                payload.ap_selection_reason = "由无线系统选择接入点"
            end
        end
    end

    local hotspot_id = tostring(selection.active_hotspot_id or "")
    if mode == "hotspot" and tostring(wireless.hotspot_id or "") ~= "" then
        hotspot_id = tostring(wireless.hotspot_id)
    end
    for _, hotspot in ipairs(config.hotspot_profiles or {}) do
        if hotspot.id == hotspot_id then
            payload.hotspot_profile_label = tostring(hotspot.label or "")
        end
    end
    -- Both gates, as the daemon applies them: an account that opted in while
    -- the global switch is off is not managed, and saying it is would show a
    -- parallel-authentication badge for work nothing is doing.
    for _, item in ipairs(config.multi_wan_enabled and config.campus_accounts or {}) do
        if item.access_mode == "wired" and item.auth_enabled then
            payload.wired_auth_sessions[tostring(item.id or "")] =
                wired_session_view(view_by_id(snapshot, item.id))
        end
    end
    return payload
end

-- offline_view is the same shape with nothing observed in it.
--
-- A stopped service is a state, not a page error: the overview must say so in
-- its own card rather than fall back to "状态读取失败", which is what the page
-- shows when it cannot reach LuCI at all.
function M.offline_view(message, now)
    now = now or os.time()
    return {
        status = message or "认证服务未在运行",
        enabled = false,
        service = "stopped",
        mode = "",
        current_mode = "",
        mode_label = "未知模式",
        pending_action = "",
        current_campus_access_mode = "",
        campus_account_label = "",
        online_account_label = "",
        campus_ssid = "",
        campus_bssid = "",
        current_ssid = "",
        current_bssid = "",
        current_wireless_ifname = "",
        current_iface = "",
        current_ip = "",
        current_device = "",
        in_quiet = false,
        paused = false,
        ap_selection_policy = "",
        ap_selection_reason = "",
        connectivity = "未连接",
        connectivity_level = "offline",
        hotspot_profile_label = "",
        wired_auth_sessions = {},
        last_action = "",
        last_action_message = "",
        last_action_portal_url = "",
        action_result = "",
        last_action_ts = 0,
        action_started_at = 0,
        config_revision = 0,
        last_log = "",
        updated_at = now,
        ts = now,
    }
end

-- calls: everything below here talks to the daemon.

-- defaults answers the form's flat defaults from the daemon's own schema.
--
-- One source of defaults, as spec 02 requires: Go owns types, defaults, bounds
-- and choices, and the page keeps the labels. Deriving them here through the
-- same mapping the save path uses means a changed default cannot reach the
-- form as the old one, and a field nobody mapped cannot acquire a default that
-- nothing would ever apply.
-- school_extra_descriptors answers the selected strategy's private controls.
--
-- The page used to build these from parse_school_runtime_contract(""), a
-- literal empty string, so the list was always empty and every control below it
-- was unreachable whatever a strategy declared. The daemon publishes the
-- declarations through the same schema.get the defaults above come from; this
-- translates them into the shape the page's normaliser already expects.
--
-- Kind names differ on purpose: Go names them for the configuration contract
-- (number, select, multi), the page names them for its widgets (int, enum).
-- Translating here keeps both vocabularies honest. "multi" has no widget yet
-- and is passed through unmapped, which the page's supported-type filter drops.
local SCHOOL_EXTRA_KINDS = { string = "string", bool = "bool",
    number = "int", select = "enum" }

function M.school_extra_descriptors()
    local schema, err = rpc.call("schema.get", nil)
    if not schema then
        return nil, err
    end
    local out = {}
    for _, field in ipairs(schema.school_extra or {}) do
        if type(field) == "table" then
            local kind = tostring(field.kind or "string")
            local choices = {}
            for _, choice in ipairs(field.choices or {}) do
                if type(choice) == "table" and choice.value ~= nil then
                    choices[#choices + 1] = tostring(choice.value)
                end
            end
            out[#out + 1] = {
                key = tostring(field.key or ""),
                type = SCHOOL_EXTRA_KINDS[kind] or kind,
                label = tostring(field.label or ""),
                description = tostring(field.help or ""),
                choices = choices,
            }
        end
    end
    return out
end

function M.defaults()
    local schema, err = rpc.call("schema.get", nil)
    if not schema then
        return nil, err
    end
    local by_path = {}
    for legacy, spec in pairs(SCALARS) do
        by_path[table.concat(spec.path, ".")] = { key = legacy, kind = spec.kind }
    end

    local out = {}
    for _, field in ipairs(schema.global or {}) do
        local target = by_path[tostring(field.path or "")]
        -- A false or empty default is omitted on the wire. For a flag that is
        -- unambiguous; for anything else an absent default is absent, and
        -- inventing a zero would put a value in the form that nothing chose.
        if target and (field.default ~= nil or target.kind == "bool") then
            out[target.key] = to_display(field.default, target.kind)
        end
    end
    return out
end

-- config reads the stored configuration in the page's own shape.
function M.config()
    local result, err = rpc.call("config.get", nil)
    if not result then
        return nil, err
    end
    return M.flatten(result), nil, result
end

function M.snapshot()
    return rpc.call("status.get", nil)
end

-- status answers the page's poll from two cached reads.
--
-- Two, because the projection names accounts by id and the labels live in the
-- configuration. Both are answered from memory: spec 03 forbids a status read
-- that authenticates or probes, and this path must stay cheap enough for a
-- browser tab to hold open.
function M.status()
    local snapshot, err = rpc.call("status.get", nil)
    if not snapshot then
        return nil, err
    end
    local config = rpc.call("config.get", nil) or {}
    return M.status_view(snapshot, config, os.time())
end

-- A dialog reads its own receipt, not the overview's latest manual action.
-- Do not start/replay work or expose discovery results through this channel.
function M.action_feedback(action_id)
    local id = type(action_id) == "string" and action_id or ""
    local payload = { action_id = id, action_result = "error", last_action = "",
        last_action_message = "操作记录已失效，请查看当前状态", last_action_portal_url = "" }
    if #id == 0 or #id > 64 or not id:match("^[%w_-]+$") then
        payload.action_id = ""
        payload.last_action_message = "操作编号无效"
        return payload
    end
    -- session is a discovery owner guard, not a generic LuCI auth parameter.
    -- Access to this controller already requires an authenticated LuCI user.
    local action, err = rpc.call("action.get", { action_id = id })
    if not action then
        if err and err.code ~= "NotFound" then
            payload.last_action_message = rpc.message(err, payload.last_action_message)
        end
        return payload
    end
    if action.id ~= id or not FEEDBACK_ACTION[action.kind] then return payload end
    local result = ACTION_RESULT[action.state]
    if action.state == "queued" or action.state == "running" then result = "pending" end
    if not result then return payload end
    payload.last_action = action.kind
    payload.action_result = result
    payload.last_action_message = tostring(action.message or "")
    -- The configuration is read only for the one outcome that can use it, so
    -- polling a running action costs no extra call.
    if action.kind == "manual_login" and action.state == "failed" then
        payload.last_action_portal_url = failed_login_portal(action, rpc.call("config.get", nil))
    end
    return payload
end

return M
