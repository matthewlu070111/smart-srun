-- Exercises the LuCI bridge's pure mapping against the Go configuration shape.
--
-- Run by tests/test_luci_rpc_bridge.py with the repository's own Lua tree on
-- package.path and nixio/luci.jsonc stubbed: nothing here opens a socket, and
-- the functions under test are the ones that decide what a save sends.

local failures = 0

local function check(name, condition, detail)
    if not condition then
        failures = failures + 1
        io.stderr:write(string.format("FAIL %s: %s\n", name, tostring(detail or "")))
    end
end

local function equal(name, got, want)
    check(name, got == want, string.format("got %s, want %s", tostring(got), tostring(want)))
end

local bridge = require "luci.smart_srun.bridge"
local rpc = require "luci.smart_srun.rpc"

-- rpc calls sock:writeall/readall, which only exist once nixio.util has been
-- loaded. It must load that itself: in the page nixio.fs happened to do it,
-- and a caller that did not load nixio.fs first failed on its first request.
check("rpc.loads_nixio_util", package.loaded["nixio.util"] ~= nil,
    "luci.smart_srun.rpc did not require nixio.util")

-- One configuration in exactly the shape config.get answers with.
local CONFIG = {
    schema_version = 2,
    revision = 7,
    enabled = true,
    multi_wan_enabled = false,
    school = "default",
    sta_iface = "",
    login_defaults = { n = "200", type = "1", enc = "srun_bx1" },
    selection = {
        active_campus_id = "c1", default_campus_id = "c2",
        active_hotspot_id = "h1", default_hotspot_id = "h1",
    },
    quiet = { enabled = true, start = "00:00", ["end"] = "06:00", force_logout = true },
    preset_updates = { enabled = true, time = "09:00" },
    retry = { enabled = true, max_retries = 4, initial_seconds = 10, max_seconds = 60 },
    checks = {
        interval_seconds = 60, mode = "internet", switch_timeout_seconds = 30,
        terminal_attempts = 5, terminal_interval_seconds = 2,
    },
    failover = { enabled = true, hotspot_failback_enabled = true },
    log = { level = "INFO" },
    campus_accounts = {
        {
            id = "c1", label = "宿舍有线", user_id = "2021001", password = "",
            operator = "中国电信", operator_suffix = "telecom",
            access_mode = "wired", wired_iface = "wan", auth_enabled = true,
            base_url = "http://10.0.0.55", ac_id = "1",
            login = { n = "200", type = "1", info_prefix = "SRBX1", double_stack = false },
        },
        {
            id = "c2", label = "教学楼 Wi-Fi", user_id = "2021002",
            operator_suffix = "", access_mode = "wifi", ssid = "JXNU",
            encryption = "none", ap_selection = "fixed", bssid = "02:11:22:33:44:55",
            base_url = "http://10.0.0.55", ac_id = "8", login = {},
        },
    },
    hotspot_profiles = {
        { id = "h1", label = "手机热点", ssid = "iPhone", encryption = "psk2", key = "", radio = "radio0" },
    },
    school_extra = {},
}

-- flatten: the page's flat, string-valued view of a typed configuration.
local flat = bridge.flatten(CONFIG)
equal("flatten.enabled", flat.enabled, "1")
equal("flatten.multi_wan", flat.multi_wan_enabled, "0")
equal("flatten.quiet_enabled", flat.quiet_hours_enabled, "1")
equal("flatten.quiet_start", flat.quiet_start, "00:00")
equal("flatten.quiet_end", flat.quiet_end, "06:00")
equal("flatten.preset_auto_update", flat.preset_auto_update_enabled, "1")
equal("flatten.preset_time", flat.preset_update_time, "09:00")
local preset_patch = bridge.settings_patch(
    { preset_auto_update_enabled = "0", preset_update_time = "23:00" },
    { preset_auto_update_enabled = true, preset_update_time = true })
equal("patch.preset_disabled", preset_patch.preset_updates.enabled, false)
equal("patch.preset_time", preset_patch.preset_updates.time, "23:00")
equal("flatten.interval", flat.interval, "60")
equal("flatten.retry_cooldown", flat.retry_cooldown_seconds, "10")
equal("flatten.max_retries", flat.backoff_max_retries, "4")
equal("flatten.log_level", flat.log_level, "INFO")
equal("flatten.login_default_n", flat.n, "200")
equal("flatten.active_campus", flat.active_campus_id, "c1")
equal("flatten.default_campus", flat.default_campus_id, "c2")
equal("flatten.revision", flat.revision, 7)
equal("flatten.accounts", #flat.campus_accounts, 2)
equal("flatten.account_auth_enabled", flat.campus_accounts[1].auth_enabled, "1")
equal("flatten.account_double_stack", flat.campus_accounts[1].double_stack, "0")
equal("flatten.account_unset_double_stack", flat.campus_accounts[2].double_stack, "")
equal("flatten.account_mode", flat.campus_accounts[2].access_mode, "wifi")
-- A redacted configuration must stay redacted on its way to the page.
equal("flatten.no_password", flat.campus_accounts[1].password, "")
equal("flatten.no_hotspot_key", flat.hotspot_profiles[1].key, "")

-- settings_patch: only what the form touched.
local patch = bridge.settings_patch(
    { quiet_hours_enabled = "0", interval = "90", log_level = "DEBUG", enabled = "1" },
    { quiet_hours_enabled = true, interval = true })
equal("patch.quiet_enabled", patch.quiet.enabled, false)
equal("patch.interval", patch.checks.interval_seconds, 90)
check("patch.log_untouched", patch.log == nil, "log level must not travel unless edited")
check("patch.enabled_untouched", patch.enabled == nil, "enabled must not travel unless edited")
check("patch.nothing_dirty", bridge.settings_patch(flat, {}) == nil, "an untouched form sends nothing")

-- A number field that is not a number keeps its stored value rather than
-- becoming zero, which the daemon would accept as a valid setting.
local bad = bridge.settings_patch({ interval = "abc" }, { interval = true })
check("patch.rejects_non_numeric", bad == nil, "non-numeric interval must not be sent")

-- An emptied field is "not submitted", not "set to nothing": a clock with no
-- time or an enum with no choice would fail the whole save, including the
-- fields the user did change.
local cleared = bridge.settings_patch(
    { quiet_start = "", log_level = "", school = "", interval = "90" },
    { quiet_start = true, log_level = true, school = true, interval = true })
check("patch.skips_empty_clock", cleared.quiet == nil, "an empty clock must not be sent")
check("patch.skips_empty_enum", cleared.log == nil, "an empty enum must not be sent")
check("patch.skips_empty_required", cleared.school == nil, "an empty school must not be sent")
equal("patch.keeps_the_edited_field", cleared.checks.interval_seconds, 90)
-- The one field that may genuinely be empty still can be.
local sta = bridge.settings_patch({ sta_iface = "" }, { sta_iface = true })
equal("patch.allows_empty_sta_iface", sta.sta_iface, "")

-- Seconds stay fractional: the baseline parsed them with float().
local fraction = bridge.settings_patch({ retry_cooldown_seconds = "0.5" }, { retry_cooldown_seconds = true })
equal("patch.fractional_seconds", fraction.retry.initial_seconds, 0.5)

-- campus_patch: presence decides what a save overwrites.
local edit = bridge.campus_patch({
    id = "c1", label = "", user_id = "2021001", operator_suffix = "telecom",
    password = "", access_mode = "wired", wired_iface = "", network_interface = "wan.v2",
    auth_enabled = "1", base_url = "http://10.0.0.55", ac_id = "1",
    ssid = "leftover", bssid = "02:11:22:33:44:55", ap_selection = "fixed",
    n = "200", type = "1", enc = "srun_bx1", info_prefix = "SRBX1",
    double_stack = "", login_os = "Windows 10", login_name = "Windows",
}, { creating = false })
equal("campus.id", edit.id, "c1")
check("campus.keeps_password", edit.password == nil,
    "an empty password box on an edit must not clear the stored credential")
equal("campus.wired_iface_alias", edit.wired_iface, "wan.v2")
equal("campus.auth_enabled", edit.auth_enabled, true)
equal("campus.clears_ssid", edit.ssid, "")
equal("campus.clears_bssid", edit.bssid, "")
equal("campus.wired_ap_selection", edit.ap_selection, "auto")
equal("campus.double_stack_cleared", edit.login.double_stack, rpc.NULL)

local created = bridge.campus_patch({
    label = "", user_id = "2021003", operator_suffix = "", password = "",
    access_mode = "wifi", ssid = "JXNU", bssid = "", ap_selection = "",
    double_stack = "1",
}, { creating = true })
check("campus.new_id_absent", created.id == nil, "a new account must not choose its own id")
equal("campus.new_password_stored", created.password, "")
equal("campus.wifi_default_encryption", created.encryption, "none")
equal("campus.ap_selection_auto", created.ap_selection, "auto")
equal("campus.double_stack_true", created.login.double_stack, true)

local pinned = bridge.campus_patch({
    access_mode = "wifi", ssid = "JXNU", bssid = "02:11:22:33:44:55",
    ap_selection = "", double_stack = "0",
}, { creating = true })
equal("campus.ap_selection_from_bssid", pinned.ap_selection, "fixed")
equal("campus.double_stack_false", pinned.login.double_stack, false)

local hotspot = bridge.hotspot_patch(
    { id = "h1", label = "手机热点", ssid = "iPhone", encryption = "psk2", key = "", radio = "" },
    { creating = false })
check("hotspot.keeps_key", hotspot.key == nil, "an empty key box on an edit must not clear the stored key")
equal("hotspot.id", hotspot.id, "h1")

equal("label.with_suffix", bridge.default_label("2021001", "telecom", "未命名账号"), "2021001@telecom")
equal("label.plain", bridge.default_label("2021001", "", "未命名账号"), "2021001")
equal("label.fallback", bridge.default_label("", "", "未命名账号"), "未命名账号")

-- utc_seconds: known instants, and a difference that survives any local zone.
equal("time.epoch", bridge.utc_seconds("1970-01-01T00:00:00Z"), 0)
equal("time.known", bridge.utc_seconds("2026-09-18T12:00:00Z"), 1789732800)
equal("time.leap_day", bridge.utc_seconds("2024-02-29T00:00:00Z"), 1709164800)
check("time.rejects_garbage", bridge.utc_seconds("not a time") == nil, "a malformed timestamp is not a time")

-- status_view: what the page polls, built from a snapshot plus the config.
local snapshot = {
    service = "running", enabled = true, config_revision = 7,
    written_at = "2026-09-18T12:00:30Z",
    accounts = {
        { account_id = "c1", link = "Ready", auth = "VerifiedSelf",
          connectivity = "InternetReachable", identity = "2021001@telecom",
          line = { iface = "wan", device = "eth0.2", address = "10.0.0.77" } },
    },
    actions = {
        { id = "a1", kind = "manual_login", state = "succeeded", message = "认证完成",
          ended_at = "2026-09-18T12:00:00Z" },
    },
}
local view = bridge.status_view(snapshot, CONFIG, 1000)
equal("status.text", view.status, "已认证")
equal("status.connectivity", view.connectivity, "互联网可达")
equal("status.level", view.connectivity_level, "online")
equal("status.mode_label", view.mode_label, "校园网模式（有线）")
equal("status.access_mode", view.current_campus_access_mode, "wired")
equal("status.account_label", view.campus_account_label, "宿舍有线")
equal("status.identity", view.online_account_label, "2021001@telecom")
-- The line as observed, not as configured: the account says "wan", the device
-- that carried the attempt was eth0.2, and the address is one nobody could
-- have guessed from the configuration.
equal("status.iface", view.current_iface, "wan")
equal("status.device", view.current_device, "eth0.2")
equal("status.ip", view.current_ip, "10.0.0.77")
equal("status.not_paused", view.paused, false)
equal("status.not_in_quiet", view.in_quiet, false)
equal("status.hotspot_label", view.hotspot_profile_label, "手机热点")
equal("status.last_action", view.last_action, "manual_login")
equal("status.result", view.action_result, "ok")
equal("status.message", view.last_action_message, "认证完成")
-- The action finished 30 seconds before the snapshot was written, so it reads
-- as 30 seconds ago on this clock, whatever zone either machine is in.
equal("status.last_ts", view.last_action_ts, 970)
equal("status.pending", view.pending_action, "")
-- A managed wired account gets a session row; an unmanaged one does not.
check("status.no_sessions_without_multiwan", next(view.wired_auth_sessions) == nil,
    "multi_wan_enabled is off, so no account is managed")

local managed = {}
for key, value in pairs(CONFIG) do managed[key] = value end
managed.multi_wan_enabled = true
local managed_view = bridge.status_view(snapshot, managed, 1000)
check("status.session_present", managed_view.wired_auth_sessions.c1 ~= nil, "c1 opted in and the switch is on")
equal("status.session_online", managed_view.wired_auth_sessions.c1.online, true)

-- A queued action is what the progress dialog waits on.
local busy = {
    service = "running", enabled = true, written_at = "2026-09-18T12:00:30Z",
    accounts = {}, actions = {
        { id = "a2", kind = "switch_campus", state = "running" },
        { id = "a1", kind = "manual_login", state = "succeeded", ended_at = "2026-09-18T12:00:00Z" },
    },
}
local busy_view = bridge.status_view(busy, CONFIG, 1000)
equal("status.pending_kind", busy_view.pending_action, "switch_campus")
equal("status.pending_result", busy_view.action_result, "pending")
equal("status.pending_clears_last_action", busy_view.last_action, "")
equal("status.pending_clears_message", busy_view.last_action_message, "")
equal("status.pending_clears_timestamp", busy_view.last_action_ts, 0)

-- Background work and wizard jobs have their own results. They must not hide
-- the manual terminal the frozen progress dialog matches by kind and time.
for _, state in ipairs({ "succeeded", "failed", "cancelled", "interrupted" }) do
    for _, kind in ipairs({ "presets_refresh", "maintain", "forced_logout",
        "quiet_hotspot", "quiet_campus", "detect_acid", "detect_verify", "wifi_setup_start" }) do
        local mixed = {
            service = "running", written_at = "2026-09-18T12:00:30Z", actions = {
                { kind = "manual_logout", state = state, message = "manual result",
                  ended_at = "2026-09-18T12:00:10Z" },
                { kind = kind, state = "succeeded", message = "unrelated result",
                  ended_at = "2026-09-18T12:00:20Z" },
                { kind = "maintain", state = "running" },
            },
        }
        local result = bridge.status_view(mixed, CONFIG, 1000)
        local name = "status.manual_" .. state .. "_after_" .. kind
        equal(name .. ".kind", result.last_action, "manual_logout")
        equal(name .. ".message", result.last_action_message, "manual result")
        equal(name .. ".timestamp", result.last_action_ts, 980)
        equal(name .. ".result", result.action_result,
            state == "succeeded" and "ok" or state == "failed" and "error" or "forced")
        equal(name .. ".background_visible", result.pending_action, "maintain")
        equal(name .. ".no_manual_start", result.action_started_at, 0)
    end
end

local background_only = bridge.status_view({ actions = {
    { kind = "presets_refresh", state = "succeeded", message = "refreshed",
      ended_at = "2026-09-18T12:00:00Z" },
    { kind = "maintain", state = "queued" },
} }, CONFIG, 1000)
equal("status.background_has_no_manual_result", background_only.action_result, "")
equal("status.background_has_no_manual_message", background_only.last_action_message, "")

for _, kind in ipairs({ "manual_login", "manual_logout", "relogin", "switch_campus", "switch_hotspot" }) do
    local queued = bridge.status_view({ actions = {
        { kind = "maintain", state = "running" },
        { kind = kind, state = "queued" },
        { kind = "manual_login", state = "succeeded", message = "old result",
          ended_at = "2026-09-18T12:00:00Z" },
    } }, CONFIG, 1000)
    equal("status.queued_" .. kind, queued.action_result, "pending")
    equal("status.queued_clears_" .. kind, queued.last_action_message, "")
end

-- Mode follows the switch that actually succeeded, not the one requested.
local switched = {
    service = "running", enabled = true, written_at = "2026-09-18T12:10:00Z", accounts = {},
    actions = {
        { id = "a3", kind = "switch_hotspot", state = "succeeded", ended_at = "2026-09-18T12:05:00Z" },
        { id = "a4", kind = "switch_campus", state = "failed", ended_at = "2026-09-18T12:09:00Z" },
    },
}
local switched_view = bridge.status_view(switched, CONFIG, 1000)
equal("status.mode_after_switch", switched_view.current_mode, "hotspot")
equal("status.mode_label_hotspot", switched_view.mode_label, "热点模式")
equal("status.failed_switch_result", switched_view.action_result, "error")

-- Hotspot connectivity never inherits a campus identity or a campus probe.
-- The observed profile also survives service restart/action-history expiry.
local hotspot_snapshot = { service = "running", enabled = false, accounts = snapshot.accounts,
    actions = {}, wireless = { state = "associated", hotspot_id = "h1", radio = "radio0",
        ssid = "iPhone", iface = "wwan", device = "phy0-sta0", address = "192.0.2.2" } }
for _, case in ipairs({
    { "InternetReachable", "热点已联网", "online", "互联网可达" },
    { "Limited", "热点联网受限", "limited", "已连接但受限" },
    { "Offline", "热点联网检测失败", "offline", "互联网探测未通过" },
    { "Unknown", "热点已连接", "offline", "等待联网检测" },
}) do
    hotspot_snapshot.wireless.connectivity = case[1]
    local result = bridge.status_view(hotspot_snapshot, CONFIG, 1000)
    equal("hotspot.mode", result.mode, "hotspot")
    equal("hotspot.status_" .. case[1], result.status, case[2])
    equal("hotspot.level_" .. case[1], result.connectivity_level, case[3])
    equal("hotspot.connectivity_" .. case[1], result.connectivity, case[4])
    equal("hotspot.no_campus_identity", result.online_account_label, "")
    equal("hotspot.own_interface", result.current_iface, "wwan")
    equal("hotspot.own_address", result.current_ip, "192.0.2.2")
end
hotspot_snapshot.accounts = {}
hotspot_snapshot.wireless.address = ""
equal("hotspot.awaiting_lease", bridge.status_view(hotspot_snapshot, CONFIG, 1000).status, "热点等待 IP 地址")
hotspot_snapshot.actions = switched.actions
hotspot_snapshot.wireless = { state = "disconnected" }
local disconnected_hotspot = bridge.status_view(hotspot_snapshot, CONFIG, 1000)
equal("hotspot.disconnected", disconnected_hotspot.status, "热点未连接")
equal("hotspot.no_old_ip", disconnected_hotspot.current_ip, "")
hotspot_snapshot.wireless = nil
equal("hotspot.expired", bridge.status_view(hotspot_snapshot, CONFIG, 1000).status, "正在读取热点状态")
hotspot_snapshot.wireless = {state = "associated", hotspot_id = "h1", address = "192.0.2.2", connectivity = "InternetReachable"}
hotspot_snapshot.service = "stopped"
equal("hotspot.stopped", bridge.status_view(hotspot_snapshot, CONFIG, 1000).connectivity_level, "offline")

-- A cancelled action is "forced", which is what the dialog's stop button means.
local cancelled = {
    service = "running", enabled = true, written_at = "2026-09-18T12:00:10Z", accounts = {},
    actions = { { id = "a5", kind = "manual_login", state = "cancelled", ended_at = "2026-09-18T12:00:00Z" } },
}
equal("status.cancelled", bridge.status_view(cancelled, CONFIG, 1000).action_result, "forced")

-- Quiet hours suspend a service that is still switched on. The page has to be
-- able to say that without contradicting the switch beside it.
local quiet = {
    service = "running", enabled = true, pause = { "QuietHours" },
    written_at = "2026-09-18T12:00:00Z", accounts = {}, actions = {},
}
local quiet_view = bridge.status_view(quiet, CONFIG, 1000)
equal("status.quiet_paused", quiet_view.paused, true)
equal("status.quiet_flag", quiet_view.in_quiet, true)
equal("status.quiet_keeps_the_switch", quiet_view.enabled, true)

local disabled = {
    service = "running", enabled = false, pause = { "UserDisabled" },
    written_at = "2026-09-18T12:00:00Z", accounts = {}, actions = {},
}
equal("status.disabled_is_not_quiet", bridge.status_view(disabled, CONFIG, 1000).in_quiet, false)
equal("status.disabled_is_paused", bridge.status_view(disabled, CONFIG, 1000).paused, true)

-- A line nobody has observed yet leaves the address empty rather than
-- repeating the configuration.
local unobserved = {
    service = "running", enabled = true, written_at = "2026-09-18T12:00:00Z",
    accounts = { { account_id = "c1", link = "Ready", auth = "Unknown",
                   connectivity = "Unknown" } },
    actions = {},
}
local unobserved_view = bridge.status_view(unobserved, CONFIG, 1000)
equal("status.unobserved_ip", unobserved_view.current_ip, "")
equal("status.unobserved_device", unobserved_view.current_device, "")
-- The configured interface still stands in while the link is ready, which is
-- the first moment after a save and before the first attempt.
equal("status.unobserved_iface_falls_back", unobserved_view.current_iface, "wan")

-- A stopped service is a state, not a page error.
local stopped = bridge.status_view({ service = "stopped", enabled = false }, CONFIG, 1000)
equal("status.stopped", stopped.status, "认证服务已停止")
equal("status.stopped_level", stopped.connectivity_level, "offline")
equal("offline.status", bridge.offline_view("认证服务未在运行", 1000).status, "认证服务未在运行")
equal("offline.ts", bridge.offline_view(nil, 1000).ts, 1000)

-- A link that is not ready is reported as the link problem it is, not as an
-- authentication result the gateway never gave.
local pending_link = {
    service = "running", enabled = true, written_at = "2026-09-18T12:00:00Z",
    accounts = { { account_id = "c1", link = "AddressPending", auth = "Unknown", connectivity = "Unknown" } },
    actions = {},
}
equal("status.link_problem", bridge.status_view(pending_link, CONFIG, 1000).status, "等待 IPv4 地址")
equal("status.link_iface_unknown", bridge.status_view(pending_link, CONFIG, 1000).current_iface, "")

-- Actual association data must win over a configured fixed BSSID. No radio
-- observation may borrow the old address or invent a current AP from config.
local wifi_config = { selection = {active_campus_id = "wifi"}, campus_accounts = {
    {id = "wifi", access_mode = "wifi", ssid = "campus", bssid = "02:00:00:00:00:01", ap_selection = "fixed"}
} }
local wifi_state = {service = "running", accounts = {{account_id = "wifi", link = "Ready"}}, actions = {},
    wireless = {state = "associated", device = "phy1-sta0", iface = "wwan", address = "192.0.2.9",
        ssid = "campus", bssid = "02:00:00:00:00:02", signal = -53, channel = 44}}
local wifi_view = bridge.status_view(wifi_state, wifi_config, 1000)
equal("wifi.real_ap", wifi_view.current_bssid, "02:00:00:00:00:02")
equal("wifi.device", wifi_view.current_wireless_ifname, "phy1-sta0")
equal("wifi.signal", wifi_view.current_signal, -53)
equal("wifi.channel", wifi_view.current_channel, 44)
equal("wifi.policy", wifi_view.ap_selection_policy, "fixed")
equal("wifi.mismatch", wifi_view.ap_selection_reason, "当前接入点与固定 BSSID 不同")
wifi_state.wireless = nil
wifi_view = bridge.status_view(wifi_state, wifi_config, 1000)
equal("wifi.no_invented_ap", wifi_view.current_bssid, "")
equal("wifi.no_invented_ssid", wifi_view.current_ssid, "")

-- A failed manual login offers the school's page again, as 1.6.1 did.
--
-- The page has always had the link (renderPortalGuidance). The 2.0 bridge sent
-- "" in both places that feed it, so the path could never fire. The address is
-- the configured one for the account that failed, reduced to an origin.
local function failed(kind, account_id, state)
    return { service = "running", written_at = "2026-09-18T12:00:30Z", actions = {
        { kind = kind, account_id = account_id, state = state or "failed",
          message = "认证失败", ended_at = "2026-09-18T12:00:10Z" },
    } }
end
equal("portal.failed_login", bridge.status_view(failed("manual_login", "c1"), CONFIG, 1000)
    .last_action_portal_url, "http://10.0.0.55")
equal("portal.succeeded_login", bridge.status_view(failed("manual_login", "c1", "succeeded"), CONFIG, 1000)
    .last_action_portal_url, "")
equal("portal.cancelled_login", bridge.status_view(failed("manual_login", "c1", "cancelled"), CONFIG, 1000)
    .last_action_portal_url, "")
for _, kind in ipairs({ "manual_logout", "relogin", "switch_campus", "switch_hotspot" }) do
    equal("portal.not_after_" .. kind, bridge.status_view(failed(kind, "c1"), CONFIG, 1000)
        .last_action_portal_url, "")
end
-- The account the login was for, not the active one: c2 is not active here.
local elsewhere = {}
for key, value in pairs(CONFIG) do elsewhere[key] = value end
elsewhere.campus_accounts = {
    CONFIG.campus_accounts[1],
    { id = "c2", base_url = "https://portal.example.edu:8443/srun_portal_pc?ac_id=8" },
}
equal("portal.uses_the_failed_account", bridge.status_view(failed("manual_login", "c2"), elsewhere, 1000)
    .last_action_portal_url, "https://portal.example.edu:8443")
equal("portal.unknown_account", bridge.status_view(failed("manual_login", "gone"), CONFIG, 1000)
    .last_action_portal_url, "")

-- Only a bare origin survives. Everything refused here is something a browser
-- would resolve differently from what the user sees, or would carry
-- credentials or control characters into a link.
for value, want in pairs({
    ["http://10.0.0.55"] = "http://10.0.0.55",
    ["http://10.0.0.55/"] = "http://10.0.0.55",
    ["  HTTP://gw.example.edu:801/path  "] = "http://gw.example.edu:801",
    ["http://[fe80::1]:8080/x"] = "http://[fe80::1]:8080",
    ["http://user:pw@10.0.0.55"] = "",
    ["http://10.0.0.55\\@evil.example"] = "",
    ["javascript:alert(1)"] = "",
    ["ftp://10.0.0.55"] = "",
    ["http://"] = "",
    ["http://gw example"] = "",
    ["http://gw.example:port"] = "",
    ["http://gw.example:123456"] = "",
    ["10.0.0.55"] = "",
    [""] = "",
}) do
    equal("portal.origin[" .. value .. "]", bridge.portal_origin(value), want)
end

-- The progress dialog reads its own receipt through action_feedback.
local real_call = rpc.call
local answers = {}
rpc.call = function(method) return answers[method] end
answers["config.get"] = CONFIG
answers["action.get"] = { id = "a1", kind = "manual_login", account_id = "c1", state = "failed", message = "认证失败" }
equal("feedback.failed_login_portal", bridge.action_feedback("a1").last_action_portal_url, "http://10.0.0.55")
answers["action.get"] = { id = "a1", kind = "manual_login", account_id = "c1", state = "running" }
answers["config.get"] = nil
equal("feedback.running_reads_no_config", bridge.action_feedback("a1").last_action_portal_url, "")
answers["action.get"] = { id = "a1", kind = "manual_logout", account_id = "c1", state = "failed", message = "x" }
answers["config.get"] = CONFIG
equal("feedback.logout_has_no_portal", bridge.action_feedback("a1").last_action_portal_url, "")
answers["action.get"] = { id = "a1", kind = "manual_login", account_id = "c1", state = "failed", message = "x" }
answers["config.get"] = nil
equal("feedback.config_unreadable", bridge.action_feedback("a1").last_action_portal_url, "")
rpc.call = real_call

if failures > 0 then
    io.stderr:write(string.format("%d check(s) failed\n", failures))
    os.exit(1)
end
print("bridge probe: all checks passed")
