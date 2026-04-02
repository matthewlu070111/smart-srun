local fs = require "nixio.fs"
local jsonc = require "luci.jsonc"
local nixio = require "nixio"

local DEFAULTS_FILE = "/usr/lib/smart_srun/defaults.json"

local M = {}

M.POINTER_KEYS = {
    "active_campus_id", "default_campus_id",
    "active_hotspot_id", "default_hotspot_id",
}
M.LIST_KEYS = { "campus_accounts", "hotspot_profiles" }
M.SCHOOL_EXTRA_KEY = "school_extra"

local POINTER_KEY_SET = {}
for _, key in ipairs(M.POINTER_KEYS) do
    POINTER_KEY_SET[key] = true
end

local LIST_KEY_SET = {}
for _, key in ipairs(M.LIST_KEYS) do
    LIST_KEY_SET[key] = true
end

local function load_defaults()
    local parsed = jsonc.parse(fs.readfile(DEFAULTS_FILE) or "")
    if type(parsed) ~= "table" then
        parsed = {}
    end
    if parsed.school == nil then
        parsed.school = "jxnu"
    end

    local defaults = {}
    for key, value in pairs(parsed) do
        if not POINTER_KEY_SET[key] and not LIST_KEY_SET[key] then
            defaults[key] = tostring(value or "")
        end
    end
    return defaults
end

M.SCALAR_DEFAULTS = load_defaults()
M.GLOBAL_SCALAR_KEYS = {}
for key, _ in pairs(M.SCALAR_DEFAULTS) do
    M.GLOBAL_SCALAR_KEYS[#M.GLOBAL_SCALAR_KEYS + 1] = key
end
table.sort(M.GLOBAL_SCALAR_KEYS)

function M.global_scalar_key_set()
    local key_set = {}
    for _, key in ipairs(M.GLOBAL_SCALAR_KEYS) do
        key_set[key] = true
    end
    return key_set
end

function M.with_file_lock(path, callback)
    local oflags = nixio.open_flags("wronly", "creat")
    local lock, _, msg = nixio.open(tostring(path) .. ".lock", oflags)
    if not lock then
        error("Open lock failed: " .. tostring(msg or "unknown"))
    end

    local ok, _, lock_msg = lock:lock("lock")
    if not ok then
        lock:close()
        error("Lock failed: " .. tostring(lock_msg or "unknown"))
    end

    local call_ok, a, b, c = pcall(callback)
    lock:lock("ulock")
    lock:close()
    if not call_ok then
        error(a)
    end
    return a, b, c
end

return M
