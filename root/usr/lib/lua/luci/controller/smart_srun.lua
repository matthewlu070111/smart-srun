module("luci.controller.smart_srun", package.seeall)

local http = require "luci.http"
local jsonc = require "luci.jsonc"
local sys = require "luci.sys"
local util = require "luci.util"
local fs = require "nixio.fs"
local rpc = require "luci.smart_srun.rpc"
local bridge = require "luci.smart_srun.bridge"

local LOG_TAIL_SOURCE_LINES = 2000

local NETWORK_EVENTS = {
    bind_ip_resolved = true,
    http_bind_device = true,
    http_fetch = true,
    http_fetch_result = true,
    connectivity_probe_begin = true,
    connectivity_probe_result = true,
    dns_probe_failed = true,
    srun_challenge = true,
    srun_challenge_result = true,
    srun_login_submit = true,
    srun_login_response = true,
    srun_logout_response = true,
    srun_online_query = true,
    srun_online_result = true,
    ip_wait_progress = true,
    ip_wait_result = true,
    dhcp_kick = true,
    sta_iface_up = true,
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
    entry({"admin", "services", "smart_srun", "config_export"}, call("action_config_export")).leaf = true
    entry({"admin", "services", "smart_srun", "config_import"}, call("action_config_import")).leaf = true
    entry({"admin", "services", "smart_srun"}, cbi("smart_srun"), _("SMART SRun"), 80).dependent = true
    entry({"admin", "services", "smart_srun", "status"}, call("action_status")).leaf = true
    entry({"admin", "services", "smart_srun", "enqueue"}, call("action_enqueue")).leaf = true
    entry({"admin", "services", "smart_srun", "log_tail"}, call("action_log_tail")).leaf = true
    entry({"admin", "services", "smart_srun", "log_clear"}, call("action_log_clear")).leaf = true
    entry({"admin", "services", "smart_srun", "update_check"}, call("action_update_check")).leaf = true
    entry({"admin", "services", "smart_srun", "update_start"}, call("action_update_start")).leaf = true
    entry({"admin", "services", "smart_srun", "update_status"}, call("action_update_status")).leaf = true
    entry({"admin", "services", "smart_srun", "presets_refresh"}, call("action_presets_refresh")).leaf = true
    entry({"admin", "services", "smart_srun", "user_presets_set"}, call("action_user_presets_set")).leaf = true
    entry({"admin", "services", "smart_srun", "detect_acid"}, call("action_detect_acid")).leaf = true
    entry({"admin", "services", "smart_srun", "detect_env"}, call("action_detect_env")).leaf = true
    entry({"admin", "services", "smart_srun", "detect_operator"}, call("action_detect_operator")).leaf = true
    entry({"admin", "services", "smart_srun", "discover_operators"}, call("action_discover_operators")).leaf = true
    entry({"admin", "services", "smart_srun", "setup_wifi"}, call("action_setup_wifi")).leaf = true
end

local function write_json_response(payload)
    http.prepare_content("application/json")
    http.write(jsonc.stringify(payload or {}) or "{}")
end

local function run_srunnet_json(args)
    -- opkg briefly removes execute permission while replacing /usr/bin/srunnet.
    -- The private worker copy remains executable throughout that interval and
    -- reads exactly the same fixed, bounded status files without starting RPC.
    local program = "/usr/bin/srunnet"
    if args == "update status" and fs.access("/var/run/smart-srun/update-worker", "x") then
        program = "/var/run/smart-srun/update-worker"
    end
    local output = sys.exec(program .. " " .. args .. " 2>&1") or ""
    local parsed = jsonc.parse(output)
    if type(parsed) == "table" then
        return parsed
    end
    return {
        ok = false,
        message = util.trim(output) ~= "" and util.trim(output) or "命令返回非 JSON 输出",
    }
end

function action_update_check()
    -- A cold page must not start a stopped service just to check a release.
    local payload, err = rpc.call("update.check", nil)
    write_json_response(payload or { ok = false, message = rpc.message(err, "无法检查更新") })
end

function action_update_start()
    local dispatcher = require "luci.dispatcher"
    if not dispatcher.test_post_security() then return end
    local ok, err = rpc.start_update(tostring(http.formvalue("plan_id") or ""))
    if not ok then
        write_json_response({ ok = false, message = rpc.message(err, "无法提交更新") })
        return
    end
    write_json_response(run_srunnet_json("update status"))
end

function action_update_status()
    local job_id = tostring(http.formvalue("job_id") or "")
    if job_id ~= "" then
        if #job_id ~= 32 or not job_id:match("^[0-9a-f]+$") then
            write_json_response({ ok = false, message = "更新任务编号无效" })
            return
        end
        local payload, err = rpc.call("update.status", { job_id = job_id })
        write_json_response(payload or { ok = false, message = rpc.message(err, "无法读取检查结果") })
        return
    end
    -- The helper only reads bounded, validated fixed-path files. It does not
    -- call ensure-running while the worker owns installation and restart.
    write_json_response(run_srunnet_json("update status"))
end

-- 状态改为从 Go 守护进程的组合快照读取。
-- 轮询只读缓存：不启动服务，也不触发认证或同步探测（规范 02/03）。
function action_status()
    local action_id = http.formvalue("action_id")
    if action_id ~= nil then
        write_json_response(bridge.action_feedback(action_id))
        return
    end
    local payload, err = bridge.status()
    if not payload then
        payload = bridge.offline_view(rpc.message(err, "无法读取认证服务状态"), os.time())
    end
    write_json_response(payload)
end

local function fv(name)
    return tostring(http.formvalue(name) or ""):match("^%s*(.-)%s*$")
end

-- 用户自定义预设/运营商存储由守护进程持有（独立 revision + CAS）。
-- 浏览器整份提交，这里只补齐 schema_version/revision 并转交；校验、去重、
-- 上限和与公共目录的冲突检查都在 Go 侧完成，页面不再自己落盘。

local function sanitize_user_operator(raw)
    if type(raw) ~= "table" then
        return nil
    end
    local item = {
        suffix = tostring(raw.suffix or ""),
        label = tostring(raw.label or ""),
    }
    if item.suffix == "" and item.label == "" then
        return nil
    end
    return item
end

local function sanitize_user_preset(raw)
    if type(raw) ~= "table" then
        return nil
    end
    local name = util.trim(tostring(raw.name or ""))
    local short_name = util.trim(tostring(raw.short_name or ""))
    if name == "" or not short_name:match("^custom%-[%w%-]+$") then
        return nil
    end
    local defaults_raw = type(raw.defaults) == "table" and raw.defaults or {}
    local shape_raw = type(raw.observed_login_shape) == "table" and raw.observed_login_shape or {}
    local item = {
        short_name = short_name,
        name = name,
        custom = true,
        defaults = {
            base_url = tostring(defaults_raw.base_url or ""),
            ac_id = tostring(defaults_raw.ac_id or ""),
            ssid = tostring(defaults_raw.ssid or ""),
            access_mode = tostring(defaults_raw.access_mode or ""),
            wired_iface = tostring(defaults_raw.wired_iface or ""),
        },
        observed_login_shape = {
            n = tostring(shape_raw.n or ""),
            type = tostring(shape_raw.type or ""),
            enc = tostring(shape_raw.enc or ""),
            info_prefix = tostring(shape_raw.info_prefix or ""),
            double_stack = tostring(shape_raw.double_stack or ""),
            os = tostring(shape_raw.os or ""),
            name = tostring(shape_raw.name or ""),
        },
        operators = {},
    }
    if type(raw.operators) == "table" then
        for _, op in ipairs(raw.operators) do
            if #item.operators >= USER_PRESETS_MAX_ITEMS then break end
            local so = sanitize_user_operator(op)
            if so then
                item.operators[#item.operators + 1] = so
            end
        end
    end
    return item
end

-- 浏览器提交的整份存储只做形状归整：条目原样转交，未知字段保留。
-- schema_version/revision 由本次读到的版本决定，CAS 冲突时由守护进程拒绝。
local function shape_user_preset_store(raw, revision)
    local store = { schema_version = 2, revision = revision, presets = {}, operators = {} }
    if type(raw) ~= "table" then
        return store
    end
    if type(raw.presets) == "table" then
        for _, item in ipairs(raw.presets) do
            -- The shaping keeps every nested object non-empty, which an empty
            -- Lua table could not express: it would encode as [] and the
            -- daemon would refuse a preset whose defaults were an array.
            local shaped = sanitize_user_preset(item)
            if shaped then
                store.presets[#store.presets + 1] = shaped
            end
        end
    end
    if type(raw.operators) == "table" then
        for _, operator in ipairs(raw.operators) do
            local shaped = sanitize_user_operator(operator)
            if shaped then
                store.operators[#store.operators + 1] = shaped
            end
        end
    end
    return store
end

function action_user_presets_set()
    local dispatcher = require "luci.dispatcher"
    if not dispatcher.test_post_security() then return end
    local parsed = jsonc.parse(tostring(http.formvalue("data") or ""))
    if type(parsed) ~= "table" then
        write_json_response({ ok = false, message = "数据格式错误" })
        return
    end

    -- Read the current version first: the store has its own revision, and the
    -- daemon refuses a save carrying a stale one rather than overwriting an
    -- edit made from another tab.
    local current, err = rpc.call("user_presets.get", nil)
    if not current then
        write_json_response({ ok = false, message = rpc.message(err, "无法读取用户预设") })
        return
    end
    local store = shape_user_preset_store(parsed, tonumber(current.revision) or 0)
    local saved, save_err = rpc.call_started("user_presets.set", {
        expected_revision = store.revision,
        document = store,
    })
    if not saved then
        write_json_response({ ok = false, message = rpc.message(save_err, "保存用户预设失败") })
        return
    end
    write_json_response({
        ok = true,
        presets = #store.presets,
        operators = #store.operators,
    })
end

local function discovery_job(method)
    local dispatcher = require "luci.dispatcher"
    if not dispatcher.test_post_security() then return end
    local session = dispatcher.context.authsession
    if type(session) ~= "string" or session == "" then
        write_json_response({ ok = false, message = "LuCI 登录已失效，请重新登录" })
        return
    end
    local action = fv("action")
    local payload, err
    if action == "status" or action == "cancel" then
        payload, err = rpc.call(action == "status" and "action.get" or "action.cancel", {
            action_id = fv("action_id"), session = session,
        })
    elseif action == "" or action == "start" then
        local params = {
            base_url = fv("base_url"), access_mode = fv("access_mode"),
            iface = fv("iface"), ssid = fv("ssid"),
            school = fv("school"),
            ac_id = fv("ac_id"),
            idempotency_key = fv("idempotency_key"), session = session,
        }
        if method == "detect.identity" or method == "detect.verify" then
            params.user_id = fv("user_id")
        end
        if method == "detect.verify" then
            params.password = tostring(http.formvalue("password") or "")
            params.candidates = jsonc.parse(fv("candidates"))
            params.max_attempts = tonumber(fv("max_attempts")) or 5
            params.login = {
                n = fv("n"), type = fv("type"), enc = fv("enc"), info_prefix = fv("info_prefix"),
                os = fv("login_os"), name = fv("login_name"),
            }
            if fv("double_stack") ~= "" then params.login.double_stack = fv("double_stack") == "1" end
        end
        if method == "presets.refresh" then
            local iface = fv("iface")
            if iface == "" then
                local status = bridge.status()
                iface = type(status) == "table" and tostring(status.current_iface or "") or ""
            end
            params = { iface = iface, idempotency_key = fv("idempotency_key"), session = session }
        end
        payload, err = rpc.call_started(method, params)
    else
        write_json_response({ ok = false, message = "探测操作无效" })
        return
    end
    if not payload then
        write_json_response({ ok = false, code = err and err.code, message = rpc.message(err, "探测失败") })
        return
    end
    if method == "presets.refresh" and action == "status" and payload.state == "succeeded" then
        local schools, list_err = bridge.presets()
        if not schools then
            write_json_response({ ok = false, message = rpc.message(list_err, "无法读取刷新后的预设") })
            return
        end
        payload.result = { ok = true, schools = schools, message = payload.message }
    end
    payload.ok = true
    if type(payload.result) == "table" then
        payload.result.value = tostring(payload.result.acid or "")
        payload.result.ac_id = tostring(payload.result.acid or "")
    end
    write_json_response(payload)
end

function action_config_export()
    if not require("luci.dispatcher").test_post_security() then return end
    http.header("Cache-Control", "no-store")
    local backup, err = rpc.call("config.export", { include_secrets = true, as_json = true })
    if not backup or type(backup.data) ~= "string" or #backup.data > 524288 then
        write_json_response({ ok = false, message = rpc.message(err, "导出失败") })
        return
    end
    http.header("Content-Disposition", 'attachment; filename="smart-srun-config.json"')
    -- Preserve {} versus [] (and every credential byte) across Lua's JSON
    -- table representation, just as the import path preserves uploaded text.
    http.prepare_content("application/json")
    http.write(backup.data)
end

function action_config_import()
    if not require("luci.dispatcher").test_post_security() then return end
    http.header("Cache-Control", "no-store")
    local data = http.formvalue("data")
    local preview = http.formvalue("check") == "1"
    local revision = http.formvalue("expected_revision")
    if type(data) ~= "string" or #data > 524288 or
        (not preview and (type(revision) ~= "string" or not revision:match("^%d+$") or #revision > 15)) then
        write_json_response({ ok = false, message = "备份或配置版本无效，请重新选择备份" })
        return
    end
    -- Preserve the original JSON string for Go's duplicate/type/depth checks.
    local result, err = rpc.call("config.import", {
        data = data, check_only = preview,
        expected_revision = not preview and tonumber(revision) or nil,
    })
    write_json_response(result or { ok = false, message = rpc.message(err, "导入失败") })
end

function action_presets_refresh()
    discovery_job("presets.refresh")
end

function action_detect_acid()
    discovery_job("detect.acid")
end

-- 出口自检：绑定所选接口；已在线时仍可检查预设、已有账号及本线路网关。
function action_detect_env()
    discovery_job("detect.environment")
end

-- 显式区分只读身份查询和主动验证。凭据仅经 socket 传给有界任务。
function action_detect_operator()
    discovery_job(fv("read_only") == "1" and "detect.identity" or "detect.verify")
end

function action_setup_wifi()
    local dispatcher = require "luci.dispatcher"
    if not dispatcher.test_post_security() then return end
    local session = dispatcher.context.authsession
    if type(session) ~= "string" or session == "" then
        write_json_response({ ok = false, message = "LuCI 登录已失效，请重新登录" })
        return
    end
    local job = fv("job")
    if #job ~= 32 or not job:match("^[a-f0-9]+$") then
        write_json_response({ ok = false, message = "无线任务编号无效" })
        return
    end
    local action = fv("action")
    if action ~= "start" and action ~= "status" and action ~= "cancel" then
        write_json_response({ ok = false, message = "无线操作无效" })
        return
    end
    local params = { job = job, session = session }
    if action == "start" then
        params.ssid = tostring(http.formvalue("ssid") or "")
        params.key = tostring(http.formvalue("key") or "")
        params.encryption = fv("encryption")
        params.iface = fv("iface")
        params.radio = fv("radio")
    end
    local invoke = action == "start" and rpc.call_started or rpc.call
    local result, err = invoke("setup_wifi." .. action, params)
    write_json_response(result or { ok = false, state = action == "status" and "missing" or "failed",
        code = err and err.code, message = rpc.message(err, "无线连接操作失败") })
end

function action_discover_operators()
    discovery_job("detect.operators")
end

local function normalize_base_url(value)
    local text = tostring(value or ""):match("^%s*(.-)%s*$")
    if text == "" then return "" end
    local origin = text:match("^(https?://[^/%?#]+)")
    if origin then return origin end
    if not text:match("^%a[%w+.-]*://") then
        local host = text:match("^([^/%?#]+)")
        if host and host ~= "" then
            return "http://" .. host
        end
    end
    return (text:gsub("/+$", ""))
end

-- 手动动作交给 Go 协调器排队，配置写入走 campus/hotspot 的 CAS 提交。
-- Lua 不再写 config.json / action.json，也不再重启服务：配置提交后守护进程
-- 自行重新观测，服务生命周期只经固定 helper。
local DAEMON_ACTIONS = {
    switch_hotspot = "已提交切到热点请求，自动守护已暂停",
    switch_campus = "已提交切回校园网请求",
    manual_login = "已提交手动登录请求",
    manual_logout = "已提交手动登出请求",
}

-- Go owns priority, cancellation and per-line serialization. Background work
-- must not stop a higher-priority user request at the LuCI preflight.
local BACKGROUND_ACTIONS = {
    maintain = true, presets_refresh = true, forced_logout = true,
    quiet_hotspot = true, quiet_campus = true,
}

-- 一次点击一个幂等键。同一秒内的重复提交会被协调器认成同一个动作，
-- 这正是双击应该发生的事——不是第二次登录。
local function idempotency_key(action, requested_at)
    local nixio = require "nixio"
    return string.format("luci-%s-%d-%d", action, requested_at, nixio.getpid())
end

local function running_action(snapshot)
    for _, action in ipairs(type(snapshot) == "table" and snapshot.actions or {}) do
        local state = tostring(action.state or "")
        if (state == "queued" or state == "running")
            and not BACKGROUND_ACTIONS[tostring(action.kind or "")] then
            return tostring(action.kind or "")
        end
    end
    return ""
end

local function submit_daemon_action(action)
    local requested_at = os.time()
    -- 只在快照可读时挡：服务停着时读不到，本来就没有在执行的动作。
    local pending = running_action(rpc.call("status.get", nil))
    if pending ~= "" then
        write_json_response({
            ok = false,
            message = "已有动作正在执行: " .. pending .. "，请等待完成后再试",
            action = action,
            pending_action = pending,
            ts = requested_at,
        })
        return
    end

    -- 明确的用户动作可以先拉起服务，再提交自己的请求（规范 02）。
    local config, err = rpc.call_started("config.get", nil)
    if not config then
        write_json_response({ ok = false, message = rpc.message(err, "无法读取配置"), action = action })
        return
    end
    local selection = type(config.selection) == "table" and config.selection or {}
    local params = {
        kind = action,
        idempotency_key = idempotency_key(action, requested_at),
        expected_revision = tonumber(config.revision) or 0,
    }
    if action == "switch_hotspot" then
        params.hotspot_id = tostring(selection.default_hotspot_id or "")
        if params.hotspot_id == "" then
            write_json_response({ ok = false, message = "尚未配置热点，请先添加", action = action })
            return
        end
    else
        -- 切回校园网用默认账号，登录/登出用当前账号，与 CLI 同一套选择规则。
        params.account_id = tostring(selection.active_campus_id or "")
        if action == "switch_campus" then
            params.account_id = tostring(selection.default_campus_id or "")
        end
        if params.account_id == "" then
            write_json_response({ ok = false, message = "尚未配置校园网账号，请先添加", action = action })
            return
        end
    end

    local receipt, submit_err = rpc.call("action.submit", params)
    if not receipt then
        write_json_response({ ok = false, message = rpc.message(submit_err, "提交动作失败"), action = action })
        return
    end
    write_json_response({
        ok = true,
        message = DAEMON_ACTIONS[action],
        action = action,
        action_id = tostring(receipt.action_id or ""),
        requested_at = requested_at,
    })
end

-- 强停映射固定的 `srunnet service stop`：先取消本项目进行中的动作再停服务，
-- 不改用户的自动认证开关，也不再遍历进程表逐个杀。
local function handle_force_stop()
    local ok, err = rpc.stop_service()
    if not ok then
        return false, rpc.message(err, "停止认证服务失败")
    end
    return true, "已强制关闭插件并停止服务"
end

local function campus_form()
    return {
        id = fv("id"),
        label = fv("label"),
        user_id = fv("user_id"),
        operator = fv("operator"),
        operator_suffix = fv("operator_suffix"),
        password = fv("password"),
        access_mode = fv("access_mode"),
        wired_iface = fv("wired_iface"),
        network_interface = fv("network_interface"),
        auth_enabled = fv("auth_enabled"),
        base_url = normalize_base_url(fv("base_url")),
        ac_id = fv("ac_id"),
        ssid = fv("ssid"),
        bssid = fv("bssid"),
        radio = fv("radio"),
        ap_selection = fv("ap_selection"),
        n = fv("n"),
        type = fv("type"),
        enc = fv("enc"),
        info_prefix = fv("info_prefix"),
        double_stack = fv("double_stack"),
        login_os = fv("login_os"),
        login_name = fv("login_name"),
    }
end

-- 字段级校验只有 Go 一处：这里把它的中文说明原样交给弹窗。
local function config_write(method, params, success_message)
    local result, err = rpc.call_started(method, params)
    if not result then
        return false, rpc.message(err, "保存失败")
    end
    return true, success_message
end

function action_enqueue()
    local dispatcher = require "luci.dispatcher"
    if not dispatcher.test_post_security() then return end
    local action = fv("action")

    if action == "force_stop" then
        local ok_force, message_force = handle_force_stop()
        write_json_response({ ok = ok_force, message = message_force, action = action, ts = os.time() })
        return
    end

    if DAEMON_ACTIONS[action] then
        submit_daemon_action(action)
        return
    end

    local setup_job = fv("setup_job")
    local setup_session
    if setup_job ~= "" then
        setup_session = dispatcher.context.authsession
        if action ~= "add_campus" or type(setup_session) ~= "string" or setup_session == "" then
            write_json_response({ ok = false, message = "无线向导会话或保存操作无效", action = action })
            return
        end
    end

    local config, err = rpc.call_started("config.get", nil)
    if not config then
        write_json_response({ ok = false, message = rpc.message(err, "无法读取配置"), action = action })
        return
    end
    local revision = tonumber(config.revision) or 0

    local ok, message = false, "不支持的动作"
    local id = fv("id")

    if action == "add_campus" or action == "edit_campus" then
        local creating = action == "add_campus"
        local patch = bridge.campus_patch(campus_form(), { creating = creating })
        if patch.label == "" then
            patch.label = bridge.default_label(patch.user_id, patch.operator_suffix, "未命名账号")
        end
        if not creating and tostring(patch.id or "") == "" then
            ok, message = false, "未找到 ID: " .. id
        else
            local params = { expected_revision = revision, account = patch }
            local method = "campus.upsert"
            if setup_job ~= "" then
                method = "setup_wifi.account"
                params.job, params.session = setup_job, setup_session
            end
            ok, message = config_write(method, params,
                creating and "已添加" or "已更新")
        end

    elseif action == "add_hotspot" or action == "edit_hotspot" then
        local creating = action == "add_hotspot"
        local patch = bridge.hotspot_patch({
            id = id, label = fv("label"), ssid = fv("ssid"),
            encryption = fv("encryption"), key = fv("key"), radio = fv("radio"),
        }, { creating = creating })
        if patch.label == "" then
            patch.label = patch.ssid ~= "" and patch.ssid or "未命名热点"
        end
        if not creating and tostring(patch.id or "") == "" then
            ok, message = false, "未找到 ID: " .. id
        else
            ok, message = config_write("hotspot.upsert",
                { expected_revision = revision, profile = patch },
                creating and "已添加" or "已更新")
        end

    elseif action == "delete_campus" or action == "delete_hotspot" then
        local method = action == "delete_campus" and "campus.remove" or "hotspot.remove"
        ok, message = config_write(method, { expected_revision = revision, id = id }, "已删除")

    elseif action == "set_default_campus" or action == "set_default_hotspot" then
        local method = action == "set_default_campus" and "campus.set_default" or "hotspot.set_default"
        local done = action == "set_default_campus" and "已设为当前默认账号" or "已设为当前默认热点"
        ok, message = config_write(method, { expected_revision = revision, id = id }, done)
    end

    write_json_response({ ok = ok, message = message, action = action, ts = os.time() })
end

-- Structured log translation table (event -> Chinese)
local event_zh = {
    login_success       = "登录成功",
    login_failed        = "登录失败",
    retry_scheduled     = "即将重试",
    retry_interrupted   = "重试被中断",
    retry_success       = "重试成功",
    retry_failed        = "重试失败",
    retry_stopped       = "停止重试",
    disconnect_detected = "检测到断线",
    status_check_error  = "状态检测异常",
    online_account_mismatch = "在线账号与配置不符",
    multi_wan_session   = "有线账号认证状态",
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
    action_requeued     = "动作重新排队",
    action_interrupted  = "动作已中断",
    switch_progress     = "切换进度",
    ap_selection        = "AP 选择",
    ap_association      = "AP 关联",
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
    switch_guard_restored = "已恢复自动守护",
    stale_session_rebuild = "清理残留会话并重建连接",
    config_migrated     = "配置已迁移",
    config_legacy_fix   = "修复遗留状态",
    config_default_applied = "应用默认配置",
    config_action_queued = "操作已入队",
    config_action_consumed = "操作已出队",
    config_loaded       = "配置已加载",
    config_applied      = "配置已保存",
    -- Go 事件目录新增的条目；每个都必须在这里有中文，见 tests/test_log_event_translations.py
    action_phase        = "动作进度",
    maintain_queued     = "已排队自动认证",
    pause_changed       = "自动认证暂停状态",
    quiet_logout_queued = "静默时段排队下线",
    line_conflict       = "线路冲突",
    internal_error      = "内部错误",
    log_cleared         = "日志已清空",
    detect_probe        = "探测认证页面",
    status_query        = "状态查询",
    update_status       = "更新状态",
    presets_refresh     = "学校预设刷新",
    -- 重试与生命周期
    retry_cycle_start   = "进入重试循环",
    retry_cycle_end     = "重试循环结束",
    tick_begin          = "守护进程心跳",
    -- 网络底层
    bind_ip_resolved    = "绑定 IP 已解析",
    http_bind_device    = "认证连接已绑定到设备",
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
    dhcp_kick           = "重新触发 DHCP",
    sta_iface_up        = "拉起 STA 网络接口",
    runtime_mode_detect = "检测运行模式",
    profile_rebuild     = "重建无线配置",
    uci_wireless_update = "更新 UCI 无线配置",
    switch_evaluate     = "评估切换决策",
    -- SRun 认证
    srun_challenge      = "请求 SRun 挑战码",
    srun_challenge_result = "SRun 挑战码结果",
    srun_login_submit   = "提交 SRun 登录",
    srun_login_response = "SRun 登录响应",
    srun_logout_response = "SRun 登出响应",
    srun_online_query   = "查询 SRun 在线状态",
    srun_online_result  = "SRun 在线状态结果",
    -- 学校运行时
    runtime_resolved    = "学校运行时已加载",
    runtime_hook        = "学校运行时钩子",
    runtime_dispatch    = "学校运行时派发",
}

-- Error reason translation for raw structured log codes.
local reason_zh = {
    username_or_password_error = "用户名或密码错误",
    ip_already_online_error    = "IP已在线",
    challenge_expire_error     = "挑战码已过期",
    sign_error                 = "签名错误",
    radius_error               = "RADIUS认证失败",
    login_error                = "认证失败",
    no_response_data_error     = "网关未返回认证数据，请核对认证地址、AC_ID 和账号参数",
    not_online_error           = "网关显示当前 IP 未在线",
    portal_intercept_error     = "认证接口返回了网页，请检查认证地址或尝试网页登录",
    auth_html_response_error   = "认证接口返回了网页，请检查认证地址或尝试网页登录",
    auth_response_parse_error  = "认证接口返回的数据格式异常",
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

local function parse_quoted_log_value(text, idx)
    local out = {}
    idx = idx + 1
    while idx <= #text do
        local ch = text:sub(idx, idx)
        if ch == "\\" and idx < #text then
            local next_ch = text:sub(idx + 1, idx + 1)
            if next_ch == '"' or next_ch == "\\" then
                out[#out + 1] = next_ch
            elseif next_ch == "n" or next_ch == "r" or next_ch == "t" then
                out[#out + 1] = "\\" .. next_ch
            else
                out[#out + 1] = next_ch
            end
            idx = idx + 2
        elseif ch == '"' then
            return table.concat(out), idx + 1
        else
            out[#out + 1] = ch
            idx = idx + 1
        end
    end
    return table.concat(out), idx
end

local function parse_structured_fields(rest, level, event)
    local text = extract_structured_suffix(rest, level, event)
    local pipe_pos = text:find("%s|%s*")
    if pipe_pos then
        text = text:sub(1, pipe_pos - 1)
    end

    local fields = {}
    local by_key = {}
    local idx = 1
    while idx <= #text do
        while idx <= #text and text:sub(idx, idx):match("%s") do
            idx = idx + 1
        end
        if idx > #text or text:sub(idx, idx) == "|" then
            break
        end

        local remaining = text:sub(idx)
        local key = remaining:match("^([%w_]+)=")
        if not key then
            local next_space = text:find("%s+", idx)
            if not next_space then
                break
            end
            idx = next_space + 1
        else
            idx = idx + #key + 1
            local value = ""
            if text:sub(idx, idx) == '"' then
                value, idx = parse_quoted_log_value(text, idx)
            else
                value = text:match("^(%S+)", idx) or ""
                idx = idx + #value
            end
            fields[#fields + 1] = { key = key, value = value }
            if by_key[key] == nil then
                by_key[key] = value
            end
        end
    end
    return fields, by_key
end

local friendly_field_order = {
    "action",
    "result",
    "ok",
    "duration_ms",
    "queue_lag_ms",
    "username",
    "expected_username",
    "username_reported",
    "ip",
    "resolved_ip",
    "bind_ip",
    "host",
    "url",
    "method",
    "status_code",
    "outcome",
    "bytes_received",
    "errors",
    "error",
    "error_code",
    "attempts",
    "delay",
    "failures",
    "target",
    "ssid",
    "radio",
    "band",
    "portal",
    "section",
    "network",
    "iface",
    "source",
    "runtime_type",
    "hook",
    "school",
    "mode",
    "pid",
    "interval",
    "log_level",
}

local friendly_field_labels = {
    action = "动作",
    result = "结果",
    ok = "成功",
    duration_ms = "耗时",
    queue_lag_ms = "排队",
    username = "账号",
    expected_username = "期望账号",
    username_reported = "在线账号",
    ip = "IP",
    resolved_ip = "客户端IP",
    bind_ip = "绑定IP",
    host = "主机",
    url = "URL",
    method = "方法",
    status_code = "状态码",
    outcome = "结果",
    bytes_received = "字节",
    errors = "错误数",
    error = "错误",
    error_code = "错误码",
    attempts = "次数",
    delay = "延迟",
    failures = "失败数",
    target = "目标",
    ssid = "SSID",
    radio = "频段",
    band = "频段",
    portal = "认证页",
    section = "配置段",
    network = "网络",
    iface = "接口",
    source = "来源",
    runtime_type = "运行时",
    hook = "钩子",
    account_id = "账号ID",
    wired_iface = "有线接口",
    bind_device = "绑定设备",
    response_type = "响应类型",
    strict = "严格绑定",
    school = "学校",
    mode = "模式",
    pid = "PID",
    interval = "间隔",
    log_level = "日志等级",
}

local hidden_friendly_fields = {
    op_id = true,
    cycle_id = true,
}

local sensitive_friendly_key_parts = {
    "password",
    "passwd",
    "secret",
    "token",
    "chksum",
    "checksum",
    "hmd5",
    "credential",
    "authorization",
    "cookie",
    "session",
    "private_key",
    "campus_key",
    "hotspot_key",
    "wifi_key",
    "psk",
}

local function is_hidden_friendly_field(key)
    local name = tostring(key or ""):lower()
    if hidden_friendly_fields[name] then
        return true
    end
    for _, part in ipairs(sensitive_friendly_key_parts) do
        if name:find(part, 1, true) then
            return true
        end
    end
    return false
end

local function friendly_field_value(key, value)
    local text = tostring(value or "")
    if key == "reason" or key == "error_code" then
        text = reason_zh[text] or text
    elseif text == "True" or text == "true" then
        text = "是"
    elseif text == "False" or text == "false" then
        text = "否"
    end

    if key == "duration_ms" or key == "queue_lag_ms" then
        text = text .. "ms"
    end
    return text
end

local function append_friendly_fields(parts, field_list, field_map, skipped)
    local rendered = {}
    local used = {}
    skipped = skipped or {}

    local function add_field(key, value)
        if used[key] or skipped[key] or is_hidden_friendly_field(key) then
            return
        end
        if value == nil or tostring(value) == "" then
            return
        end
        used[key] = true
        rendered[#rendered + 1] = (friendly_field_labels[key] or key) .. "=" .. friendly_field_value(key, value)
    end

    for _, key in ipairs(friendly_field_order) do
        add_field(key, field_map[key])
    end
    for _, item in ipairs(field_list) do
        add_field(item.key, item.value)
    end

    if #rendered > 0 then
        parts[#parts + 1] = "（" .. table.concat(rendered, "，") .. "）"
    end
end

local function append_message_detail(parts, rest, has_prior_detail)
    local detail = rest:match("|%s*(.+)$")
    if detail and detail ~= "" then
        parts[#parts + 1] = (has_prior_detail and " · " or ": ") .. detail
    end
end

local function skip_fields(...)
    local skipped = {}
    for i = 1, select("#", ...) do
        skipped[tostring(select(i, ...))] = true
    end
    return skipped
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

    local field_list, field_map = parse_structured_fields(rest, level, event)

    if event == "switch_progress" then
        local stage = field_map.stage or extract_kv(rest, "stage")
        local stage_zh = stage and switch_stage_zh[stage]
        if stage_zh then parts[#parts + 1] = " · " .. stage_zh end
        local target = field_map.target or extract_kv(rest, "target")
        if target then parts[#parts + 1] = "（" .. target .. "）" end
        append_message_detail(parts, rest, false)
        append_friendly_fields(parts, field_list, field_map, skip_fields("stage", "target"))
        return table.concat(parts)
    end

    if event == "action_started" then
        local action = field_map.action or extract_kv(rest, "action")
        if action then parts[#parts + 1] = "：" .. action end
        append_message_detail(parts, rest, false)
        append_friendly_fields(parts, field_list, field_map, skip_fields("action"))
        return table.concat(parts)
    end

    local account = field_map.account or extract_kv(rest, "account")
    if account then parts[#parts + 1] = " [" .. account .. "]" end

    local reason = field_map.reason or extract_kv(rest, "reason")
    local has_detail = false
    if reason then
        local rzh = reason_zh[reason]
        parts[#parts + 1] = ": " .. (rzh or reason)
        has_detail = true
    end

    local attempt = field_map.attempt or rest:match("attempt=(%d+)")
    if attempt then parts[#parts + 1] = " (第" .. attempt .. "次)" end

    append_message_detail(parts, rest, has_detail)
    append_friendly_fields(parts, field_list, field_map, skip_fields("account", "reason", "attempt"))

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

local function tail_text(text, lines)
    lines = tonumber(lines) or 0
    if lines <= 0 or text == "" then
        return text
    end

    local entries = {}
    for line in text:gmatch("[^\n]+") do
        entries[#entries + 1] = line
    end
    if #entries <= lines then
        return table.concat(entries, "\n")
    end

    local kept = {}
    for idx = #entries - lines + 1, #entries do
        kept[#kept + 1] = entries[idx]
    end
    return table.concat(kept, "\n")
end

-- 插件日志由守护进程持有（/tmp 下有界的结构化事件日志），这里只读取。
-- 解析和中文渲染仍在下面的 friendly_* 里：事件码由 Go 的目录定义，
-- 文案留在页面这一层。
local function read_plugin_log_text(lines, since)
    local page = rpc.call("log.tail", {
        lines = tonumber(lines) or 0,
        since = tonumber(since) or 0,
    })
    if type(page) ~= "table" or type(page.lines) ~= "table" then
        return ""
    end
    return table.concat(page.lines, "\n")
end

local function read_plugin_full_log_text()
    local result = rpc.call("log.download", nil)
    if type(result) ~= "table" then
        return ""
    end
    return tostring(result.text or "")
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
    lines = tonumber(lines) or LOG_TAIL_SOURCE_LINES
    local ok, text = pcall(sys.exec, "logread -l " .. lines .. " 2>/dev/null")
    if ok and text and text ~= "" then
        return text
    end
    ok, text = pcall(sys.exec, "logread 2>/dev/null")
    if ok and text then
        return tail_text(text, lines)
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

    -- 系统日志按当前线路的上下文过滤；上下文来自守护进程快照，
    -- 读不到（服务停止）就退回关键词匹配，不再读旧的运行态文件。
    local state = bridge.status() or {}
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
    -- since is applied by the daemon, which holds the records with their own
    -- timestamps; the local filter stays for the network channel, where the
    -- system log's lines are merged in here.
    return read_plugin_log_text(lines, since)
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

function action_log_clear()
    local dispatcher = require "luci.dispatcher"
    if not dispatcher.test_post_security() then return end
    local channel = http.formvalue("channel") or "plugin"
    if channel ~= "plugin" then
        write_json_response({ ok = false, message = "系统网络日志不能由插件清空", channel = channel })
        return
    end
    -- 只清本项目的日志。系统全局日志不属于这里，守护进程也不会去动它。
    local result, err = rpc.call_started("log.clear", nil)
    write_json_response({
        ok = result and true or false,
        message = result and "日志已清空" or rpc.message(err, "清空日志失败"),
        channel = "plugin",
        ts = os.time(),
    })
end
