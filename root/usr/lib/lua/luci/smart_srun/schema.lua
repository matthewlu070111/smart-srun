local fs = require "nixio.fs"
local jsonc = require "luci.jsonc"
local nixio = require "nixio"

local DEFAULTS_FILE = "/usr/lib/smart_srun/defaults.json"
local OPKG_STATUS_FILE = "/usr/lib/opkg/status"
local DEFAULT_VERSION = "v0.0.0-r1"

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

local function normalize_version_string(raw)
    local value = tostring(raw or "")
    local version, release = value:match("^v?([^-]+)-r?(%d+)$")
    if version and release then
        return string.format("v%s-r%s", version, release)
    end
    return DEFAULT_VERSION
end

local function package_versions()
    local versions = {}
    local text = (fs.readfile(OPKG_STATUS_FILE) or "") .. "\n\n"
    for block in text:gmatch("(.-)\n\n") do
        local package_name = block:match("\n?Package:%s*([^\r\n]+)")
        if package_name then
            versions[package_name] = block:match("\n?Version:%s*([^\r\n]+)") or ""
        end
    end
    return versions
end

function M.installed_package_name()
    local versions = package_versions()
    if versions["luci-app-smart-srun-bundle"] then
        return "luci-app-smart-srun-bundle"
    end
    if versions["luci-app-smart-srun"] then
        return "luci-app-smart-srun"
    end
    return "smart-srun"
end

function M.installed_package_display_text()
    local versions = package_versions()
    local package_name = M.installed_package_name()
    local label = package_name == "luci-app-smart-srun-bundle" and "Bundle 版"
        or package_name == "luci-app-smart-srun" and "标准版"
        or "CLI 版"
    local version = normalize_version_string(versions[package_name] or "")
    return string.format("%s %s", label, version)
end

return M
