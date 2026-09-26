-- AP selection through the real controller and the real bridge.
--
-- The policy and its pinned BSSID have to survive the trip from the dialog to
-- the daemon unchanged, the wired half has to be cleared rather than carried,
-- and a refusal has to reach the user instead of being swallowed into a save
-- that appears to have worked.
local repo = ... or "."
local harness = dofile(repo .. "/tests/lua/controller_harness.lua")(repo)
local controller = harness.controller
local schema = require "luci.smart_srun.schema"

assert(schema.normalize_ap_selection(nil, "02:11:22:33:44:55") == "fixed")
assert(schema.normalize_ap_selection("auto", "02:11:22:33:44:55") == "auto")

local CONFIG = {
    revision = 3,
    selection = { active_campus_id = "c1", default_campus_id = "c1" },
    campus_accounts = { { id = "c1", label = "one", access_mode = "wifi", ssid = "Campus",
                          bssid = "02:11:22:33:44:55", ap_selection = "fixed", login = {} } },
    hotspot_profiles = {},
}

harness.responses["config.get"] = CONFIG
harness.responses["campus.upsert"] = { config_revision = 4, id = "c1" }

for _, policy in ipairs({ "auto", "strongest", "fixed" }) do
    harness.reset()
    harness.form = { action = "edit_campus", id = "c1", access_mode = "wifi",
                     ap_selection = policy, bssid = "02:11:22:33:44:55", ssid = "Campus" }
    controller.action_enqueue()
    local call = assert(harness.last_call("campus.upsert"), "the save must reach the daemon")
    assert(call.started, "a save may start the service")
    assert(call.params.expected_revision == 3, "the save carries the revision it read")
    local account = call.params.account
    assert(account.id == "c1", account.id)
    assert(account.ap_selection == policy, account.ap_selection)
    assert(account.bssid == "02:11:22:33:44:55", account.bssid)
    assert(harness.output.ok == true, harness.output.message)
    assert(harness.output.message == "已更新", harness.output.message)
    assert(#harness.writes == 0, "the page must not write any file")
end

-- An unset policy with a pinned address still means "fixed": the frozen dialog
-- can submit an empty selector, and losing the pin would silently roam.
harness.reset()
harness.form = { action = "edit_campus", id = "c1", access_mode = "wifi",
                 ap_selection = "", bssid = "02:11:22:33:44:55", ssid = "Campus" }
controller.action_enqueue()
assert(harness.last_call("campus.upsert").params.account.ap_selection == "fixed")

-- The wired half clears the wireless one, so a stored account never carries an
-- effective setting for the mode it is not in.
harness.reset()
harness.form = { action = "edit_campus", id = "c1", access_mode = "wired",
                 wired_iface = "wan", ap_selection = "fixed", bssid = "02:11:22:33:44:55" }
controller.action_enqueue()
local wired = harness.last_call("campus.upsert").params.account
assert(wired.ssid == "" and wired.bssid == "" and wired.radio == "", "wireless half must be cleared")
assert(wired.ap_selection == "auto", wired.ap_selection)
assert(wired.wired_iface == "wan", wired.wired_iface)

-- Validation lives in the daemon now. What the page owes the user is the
-- refusal, in the daemon's own words, with nothing saved.
harness.reset()
harness.responses["campus.upsert"] = {
    error = { code = "InvalidConfig", message = "不是有效的单播 BSSID（形如 aa:bb:cc:dd:ee:ff）",
              field = "campus_accounts[0].bssid" },
}
harness.form = { action = "edit_campus", id = "c1", access_mode = "wifi",
                 ap_selection = "fixed", bssid = "01:11:22:33:44:55", ssid = "Campus" }
controller.action_enqueue()
assert(harness.output.ok == false, "an invalid address must not report success")
assert(harness.output.message:find("单播 BSSID", 1, true), harness.output.message)
assert(#harness.writes == 0)
harness.responses["campus.upsert"] = { config_revision = 5, id = "c1" }

-- The status answer reports the wireless detail it actually has. The daemon's
-- projection does not carry SSID, BSSID, signal or channel yet (batch B), and
-- the page publishes them empty rather than repeating the configuration as if
-- it had been observed.
harness.reset()
harness.responses["status.get"] = {
    service = "running", enabled = true, config_revision = 3,
    written_at = "2026-09-18T12:00:00Z",
    accounts = { { account_id = "c1", link = "Ready", auth = "VerifiedSelf",
                   connectivity = "InternetReachable", identity = "student" } },
    actions = {},
    wireless = { state = "associated", device = "phy1-sta0", ssid = "Campus",
        bssid = "02:11:22:33:44:55", signal = -52, channel = 44, iface = "wwan" },
}
controller.action_status()
local status = harness.output
assert(status.current_ssid == "Campus", status.current_ssid)
assert(status.current_bssid == "02:11:22:33:44:55", status.current_bssid)
assert(status.current_signal == -52 and status.current_channel == 44)
assert(status.ap_selection_policy == "fixed" and status.ap_selection_reason == "已连接指定接入点")
assert(status.connectivity_level == "online", status.connectivity_level)

-- A line that is not ready claims nothing about the air.
harness.reset()
harness.responses["status.get"] = {
    service = "running", enabled = true, written_at = "2026-09-18T12:00:00Z",
    accounts = { { account_id = "c1", link = "LinkDown", auth = "Unknown", connectivity = "Offline" } },
    actions = {},
}
controller.action_status()
assert(harness.output.current_ssid == "", harness.output.current_ssid)
assert(harness.output.campus_ssid == "Campus", "the configured SSID is still shown as configured")

for _, event in ipairs({ "ap_selection", "ap_association" }) do
    local line = controller.friendly_line("[2026-06-01 22:00:00] INFO " .. event .. " | fixture")
    assert(not line:find(event, 1, true), line)
end

print("ap selection probe: ok")
