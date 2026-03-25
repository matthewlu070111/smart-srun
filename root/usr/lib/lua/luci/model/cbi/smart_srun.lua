local fs = require "nixio.fs"
local sys = require "luci.sys"
local util = require "luci.util"
local jsonc = require "luci.jsonc"

local CONFIG_FILE = "/usr/lib/smart_srun/config.json"
local STATE_FILE = "/var/run/smart_srun/state.json"

-- 全局标量字段名
local GLOBAL_SCALAR_KEYS = {
    "enabled", "quiet_hours_enabled", "quiet_start", "quiet_end",
    "force_logout_in_quiet", "failover_enabled", "backoff_enable",
    "backoff_max_retries", "backoff_initial_duration", "backoff_max_duration",
    "retry_cooldown_seconds", "retry_max_cooldown_seconds",
    "switch_ready_timeout_seconds", "manual_terminal_check_max_attempts",
    "manual_terminal_check_interval_seconds", "hotspot_failback_enabled",
    "connectivity_check_mode",
    "backoff_exponent_factor", "backoff_inter_const_factor",
    "backoff_outer_const_factor", "interval", "developer_mode",
    "sta_iface", "n", "type", "enc", "school",
}
-- 指针字段名
local POINTER_KEYS = {
    "active_campus_id", "default_campus_id",
    "active_hotspot_id", "default_hotspot_id",
}
-- 列表字段名
local LIST_KEYS = { "campus_accounts", "hotspot_profiles" }
local SUPPORTED_SCHOOL_EXTRA_TYPES = {
    string = true,
    bool = true,
    int = true,
    enum = true,
}
local cfg
local changed = false
-- 标量默认值
local SCALAR_DEFAULTS = {
    enabled = "0", quiet_hours_enabled = "1",
    quiet_start = "00:00", quiet_end = "06:00",
    force_logout_in_quiet = "1", failover_enabled = "1",
    backoff_enable = "1", backoff_max_retries = "0",
    backoff_initial_duration = "10", backoff_max_duration = "600",
    retry_cooldown_seconds = "10", retry_max_cooldown_seconds = "600",
    switch_ready_timeout_seconds = "12",
    manual_terminal_check_max_attempts = "5",
    manual_terminal_check_interval_seconds = "2",
    hotspot_failback_enabled = "1",
    connectivity_check_mode = "internet",
    backoff_exponent_factor = "1.5", backoff_inter_const_factor = "0",
    backoff_outer_const_factor = "0", interval = "60",
    developer_mode = "0", sta_iface = "",
    n = "200", ["type"] = "1", enc = "srun_bx1",
    school = "jxnu",
}
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
        local j = jsonc.stringify(parsed)
        if j then fs.writefile(CONFIG_FILE, j .. "\n") end
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
    cfg.school_extra = type(parsed.school_extra) == "table" and parsed.school_extra or {}
    return cfg
end

local function save_cfg(cfg)
    local out = {}
    for _, key in ipairs(GLOBAL_SCALAR_KEYS) do
        out[key] = tostring(cfg[key] or SCALAR_DEFAULTS[key] or "")
    end
    for _, key in ipairs(POINTER_KEYS) do
        out[key] = tostring(cfg[key] or "")
    end
    for _, key in ipairs(LIST_KEYS) do
        out[key] = type(cfg[key]) == "table" and cfg[key] or {}
    end
    out.school_extra = type(cfg.school_extra) == "table" and cfg.school_extra or {}
    ensure_json_file()
    fs.writefile(CONFIG_FILE, (jsonc.stringify(out) or "{}") .. "\n")
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
<script type="text/javascript">
(function() {
  if (window.__smartSchoolInfoInit) return;
  window.__smartSchoolInfoInit = true;
  var schoolDataEl = document.getElementById('smart-school-data');
  var schools = [];
  var infoBox = document.getElementById('smart-school-info');
  var descEl = document.getElementById('smart-school-desc');
  var descTextEl = document.getElementById('smart-school-desc-text');
  var contribEl = document.getElementById('smart-school-contrib');
  var contribTextEl = document.getElementById('smart-school-contrib-text');
  var helperEl = document.getElementById('smart-school-helper');
  var outerDescEl = null;
  if (!infoBox || !descEl || !descTextEl || !contribEl || !contribTextEl || !helperEl) return;

  for (var parent = infoBox.parentNode; parent; parent = parent.parentNode) {
    if (parent.className && String(parent.className).indexOf('cbi-value-description') >= 0) {
      outerDescEl = parent;
      break;
    }
  }

  if (schoolDataEl) {
    try {
      schools = JSON.parse(schoolDataEl.value || schoolDataEl.textContent || '[]');
    } catch (e) {
      schools = [];
    }
  }

  function lookup(sn) {
    for (var i = 0; i < schools.length; i++) {
      if (schools[i].short_name === sn) return schools[i];
    }
    return null;
  }

  function isGithubUsername(value) {
    return /^@[A-Za-z0-9](?:[A-Za-z0-9-]{0,37})$/.test(value || '')
      && !/--/.test(value || '')
      && !/-$/.test(value || '');
  }

  function clearNode(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function renderContributors(contributors) {
    clearNode(contribTextEl);
    for (var i = 0; i < contributors.length; i++) {
      var text = String(contributors[i] == null ? '' : contributors[i]);
      if (i > 0) contribTextEl.appendChild(document.createTextNode(', '));
      if (isGithubUsername(text)) {
        var link = document.createElement('a');
        link.href = 'https://github.com/' + text.substring(1);
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        link.textContent = text;
        contribTextEl.appendChild(link);
      } else {
        var span = document.createElement('span');
        span.textContent = text;
        contribTextEl.appendChild(span);
      }
    }
  }

  function sync(desc, contributors) {
    var hasContrib = contributors && contributors.length;
    infoBox.style.display = 'block';
    if (outerDescEl) outerDescEl.style.display = 'block';
    contribEl.style.display = hasContrib ? 'block' : 'none';
    contribEl.style.marginTop = '%s';
    helperEl.style.marginTop = '%s';
    if (desc) descTextEl.textContent = desc;
    renderContributors(hasContrib ? contributors : []);
  }

  function update(val) {
    var school = lookup(val);
    if (!school) {
      sync('', []);
      return;
    }
    sync(
      school.description || '',
      (school.contributors && school.contributors.length)
        ? school.contributors
        : []
    );
  }

  function findSchoolSelect() {
    var node = infoBox;
    while (node) {
      if (node.className && String(node.className).indexOf('cbi-value-field') >= 0) {
        var inner = node.querySelector('select');
        if (inner) return inner;
        break;
      }
      node = node.parentNode;
    }

    return document.getElementById('widget.cbid.smart_srun.main.school')
      || document.getElementById('cbid.smart_srun.main.school')
      || document.querySelector('select[name="cbid.smart_srun.main.school"]');
  }

  var sel = findSchoolSelect();
  if (!sel) return;
  update(sel.value);
  sel.addEventListener('change', function() { update(sel.value); });
})();
</script>
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
        util.pcdata(js_data),
        contrib_spacing,
        helper_spacing)
end

local function ensure_school_extra_table()
    if type(cfg.school_extra) ~= "table" then
        cfg.school_extra = {}
    end
    return cfg.school_extra
end

local function set_school_extra_value(key, value)
    local school_extra = ensure_school_extra_table()
    local normalized = tostring(value or "")
    if school_extra[key] ~= normalized then
        school_extra[key] = normalized
        changed = true
    end
end

local function remove_school_extra_value(key)
    local school_extra = ensure_school_extra_table()
    if school_extra[key] ~= nil then
        school_extra[key] = nil
        changed = true
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
    cfg.school_extra = school_runtime_contract.school_extra
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

m = Map("smart_srun", "深澜校园网", "深澜校园网认证配置")
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
    return [[
<div id="smart-srun-overview" style="margin:4px 0 18px 0;border-left:4px solid #c62828;background:rgba(128,128,128,.08);padding:14px 16px;border-radius:0 6px 6px 0;box-shadow:none;">
  <div id="smart-srun-overview-title" style="font-size:18px;font-weight:700;color:#1f2937;margin-bottom:8px;">状态读取中</div>
  <div id="smart-srun-overview-meta" style="font-size:13px;color:#374151;display:flex;gap:14px;flex-wrap:wrap;line-height:1.6;">
    <span>WiFi: --</span>
    <span>模式: --</span>
    <span>连通性: --</span>
  </div>
</div>
<script type="text/javascript">
(function() {
  var root = document.getElementById('smart-srun-overview');
  var title = document.getElementById('smart-srun-overview-title');
  var meta = document.getElementById('smart-srun-overview-meta');
  if (!root || !title || !meta || window.__smartSrunOverviewInit) return;
  window.__smartSrunOverviewInit = true;

  var palette = {
    online: { border: '#2e7d32', bg: 'rgba(46,125,50,.10)', title: '#166534', meta: '#166534' },
    portal: { border: '#ef6c00', bg: 'rgba(239,108,0,.10)', title: '#b45309', meta: '#92400e' },
    limited: { border: '#c62828', bg: 'rgba(198,40,40,.10)', title: '#b91c1c', meta: '#991b1b' },
    offline: { border: '#6b7280', bg: 'rgba(107,114,128,.10)', title: '#374151', meta: '#4b5563' }
  };

  function applyTone(level) {
    var tone = palette[level] || palette.offline;
    root.style.borderLeftColor = tone.border;
    root.style.background = tone.bg;
    title.style.color = tone.title;
    meta.style.color = tone.meta;
  }

  function refreshOverview() {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', '/cgi-bin/luci/admin/services/smart_srun/status?_=' + Date.now(), true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      if (xhr.status !== 200) {
        applyTone('offline');
        title.textContent = '状态读取失败';
        meta.innerHTML = '<span>WiFi: --</span><span>模式: --</span><span>连通性: --</span>';
        return;
      }
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        var level = (typeof data.connectivity_level === 'string' && data.connectivity_level !== '') ? data.connectivity_level : 'offline';
        var status = (typeof data.status === 'string' && data.status !== '') ? data.status : '未知';
        var ssid = (typeof data.current_ssid === 'string' && data.current_ssid !== '') ? data.current_ssid : '未连接';
        var mode = (typeof data.mode_label === 'string' && data.mode_label !== '') ? data.mode_label : '未知模式';
        var conn = (typeof data.connectivity === 'string' && data.connectivity !== '') ? data.connectivity : '未知';
        var iface = (typeof data.current_iface === 'string' && data.current_iface !== '') ? data.current_iface : '--';
        var ip = (typeof data.current_ip === 'string' && data.current_ip !== '') ? data.current_ip : '--';
        var pending = (typeof data.pending_action === 'string' && data.pending_action !== '') ? ('；待执行动作: ' + data.pending_action) : '';
        var campusLabel = (typeof data.online_account_label === 'string' && data.online_account_label !== '') ? data.online_account_label : ((typeof data.campus_account_label === 'string' && data.campus_account_label !== '') ? data.campus_account_label : '--');
        var hotspotLabel = (typeof data.hotspot_profile_label === 'string' && data.hotspot_profile_label !== '') ? data.hotspot_profile_label : '--';

        applyTone(level);
        title.textContent = status + pending;
        var metaHtml = '<span>WiFi: ' + ssid + '</span><span>模式: ' + mode + '</span><span>连通性: ' + conn + '</span><span>接口/IP: ' + iface + ' / ' + ip + '</span>';
        if (mode === '热点模式') {
          metaHtml += '<span>热点: ' + hotspotLabel + '</span>';
        } else {
          metaHtml += '<span>账号: ' + campusLabel + '</span>';
        }
        meta.innerHTML = metaHtml;
      } catch (e) {
        applyTone('offline');
        title.textContent = '状态读取失败';
        meta.innerHTML = '<span>WiFi: --</span><span>模式: --</span><span>连通性: --</span>';
      }
    };
    xhr.send(null);
  }

  refreshOverview();
  setInterval(refreshOverview, 1200);
})();
</script>
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
        cfg.school_extra = {}
        changed = true
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
<script type="text/javascript">
(function() {
  var login = document.getElementById('smart-srun-manual-login');
  var logout = document.getElementById('smart-srun-manual-logout');
  var result = document.getElementById('smart-srun-manual-result');
  if (!login || !logout || !result || window.__smartSrunManualInit) return;
  window.__smartSrunManualInit = true;

  function fetchJson(url, callback) {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      if (xhr.status !== 200) {
        callback(new Error('http_' + xhr.status));
        return;
      }
      try {
        callback(null, JSON.parse(xhr.responseText || '{}'));
      } catch (e) {
        callback(e);
      }
    };
    xhr.send(null);
  }

  window.smartFetchJson = fetchJson;

  function openBlockingFeedback(action, requestedAt) {
    var logBox = E('pre', {
      'style': 'max-height:18rem;overflow:auto;margin:0;padding:.75rem;border:1px solid rgba(127,127,127,.28);background:rgba(127,127,127,.08);white-space:pre-wrap;word-break:break-word;'
    }, '等待后端反馈...');
    var titles = {
      manual_login: '正在登录',
      manual_logout: '正在登出',
      switch_hotspot: '正在切到热点',
      switch_campus: '正在切回校园网'
    };
    var tips = {
      manual_login: '正在执行登录流程，请勿关闭页面。',
      manual_logout: '正在执行登出流程，请稍候。',
      switch_hotspot: '正在切换到热点网络，请稍候。',
      switch_campus: '正在切换回校园网，请稍候。'
    };
    var tip = E('p', { 'style': 'margin:.5rem 0 1rem 0;' }, tips[action] || '正在执行网络动作，请稍候。');
    var footer = E('div', { 'class': 'right' });
    var closed = false;
    var timer = null;
    var progressButton = E('button', {
      'class': 'btn cbi-button',
      'disabled': 'disabled'
    }, '进行中');
    var forceButton = E('button', {
      'class': 'btn cbi-button cbi-button-remove',
      'click': function(ev) {
        ev.preventDefault();
        if (closed || forceButton.disabled) return;
        forceButton.disabled = true;
        result.textContent = '正在强制停止...';
        var xhr = new XMLHttpRequest();
        xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
        xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
        xhr.onreadystatechange = function() {
          if (xhr.readyState !== 4) return;
          var text = '已触发强制停止';
          if (xhr.status === 200) {
            try {
              var data = JSON.parse(xhr.responseText || '{}');
              if (typeof data.message === 'string' && data.message !== '')
                text = data.message;
            } catch (e) {}
          }
          unlock(text, false);
        };
        xhr.send('action=' + encodeURIComponent('force_stop'));
      }
    }, '强制停止');

    progressButton.addEventListener('click', function(ev) {
      if (progressButton.disabled) {
        ev.preventDefault();
        return;
      }
      L.hideModal();
      location.reload();
    });

    footer.appendChild(progressButton);
    footer.appendChild(forceButton);

    function setTerminalFooter() {
      progressButton.disabled = false;
      progressButton.textContent = '关闭返回';
      forceButton.disabled = true;
    }

    function unlock(text, success) {
      if (closed) return;
      closed = true;
      if (timer) window.clearInterval(timer);
      setTerminalFooter();
      if (text) result.textContent = text + (success ? ' 🎉' : ' ⚠');
    }

    function checkTerminal(statusData) {
      if (!statusData) return false;
      if (statusData.last_action !== action) return false;
      if ((statusData.last_action_ts || 0) < requestedAt) return false;
      if (statusData.action_result === 'forced') {
        unlock(statusData.status || '已强制停止', false);
        return true;
      }
      if (statusData.action_result === 'error') {
        unlock(statusData.status || '执行失败', false);
        return true;
      }

      if (action === 'manual_login') {
        if (statusData.action_result === 'ok') {
          unlock(statusData.status || '登录成功', true);
          return true;
        }
      }

      if (action === 'manual_logout') {
        if (statusData.action_result === 'ok') {
          unlock(statusData.status || '登出成功', true);
          return true;
        }
      }

      if (action === 'switch_hotspot') {
        if (statusData.action_result === 'ok') {
          unlock(statusData.status || '已切到热点', true);
          return true;
        }
      }

      if (action === 'switch_campus') {
        if (statusData.action_result === 'ok') {
          unlock(statusData.status || '已切回校园网', true);
          return true;
        }
      }

      return false;
    }

    function poll() {
      fetchJson('/cgi-bin/luci/admin/services/smart_srun/log_tail?lines=200&format=friendly&since=' + encodeURIComponent(requestedAt) + '&_=' + Date.now(), function(err, logData) {
        if (!err && logData && typeof logData.log === 'string') {
          logBox.textContent = logData.log;
          logBox.scrollTop = logBox.scrollHeight;
        }
      });

      fetchJson('/cgi-bin/luci/admin/services/smart_srun/status?_=' + Date.now(), function(err, statusData) {
        if (err) return;
        checkTerminal(statusData);
      });
    }

    L.showModal(titles[action] || '正在执行动作', [ tip, logBox, footer ], 'cbi-modal');
    timer = window.setInterval(poll, 1000);
    poll();
  }

  window.smartOpenBlockingFeedback = openBlockingFeedback;

  function submit(action) {
    result.textContent = '正在提交...';
    login.disabled = true;
    logout.disabled = true;

    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      login.disabled = false;
      logout.disabled = false;
      if (xhr.status !== 200) {
        result.textContent = '提交失败';
        return;
      }
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
        result.textContent = message;
        if (data.ok) {
          openBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
        }
      } catch (e) {
        result.textContent = '提交失败';
      }
    };
    xhr.send('action=' + encodeURIComponent(action));
  }

  login.addEventListener('click', function() { submit('manual_login'); });
  logout.addEventListener('click', function() { submit('manual_logout'); });
})();
</script>
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
<script type="text/javascript">
(function() {
  var hotspot = document.getElementById('smart-srun-switch-hotspot');
  var campus = document.getElementById('smart-srun-switch-campus');
  var forceClose = document.getElementById('smart-srun-force-close');
  var result = document.getElementById('smart-srun-switch-result');
  if (!hotspot || !campus || !forceClose || !result || window.__smartSrunSwitchInit) return;
  window.__smartSrunSwitchInit = true;

  function enqueue(action) {
    result.textContent = '正在提交...';
    hotspot.disabled = true;
    campus.disabled = true;
    forceClose.disabled = true;

    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      hotspot.disabled = false;
      campus.disabled = false;
      forceClose.disabled = false;
      if (xhr.status !== 200) {
        result.textContent = '提交失败';
        return;
      }
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
        result.textContent = message;
        if (data.ok && typeof window.smartOpenBlockingFeedback === 'function') {
          window.smartOpenBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
        }
      } catch (e) {
        result.textContent = '提交失败';
      }
    };
    xhr.send('action=' + encodeURIComponent(action));
  }

  function enqueueForceClose() {
    if (!confirm('这会停止 SMART SRun 服务并终止插件进程，是否继续？')) {
      return;
    }
    result.textContent = '正在强制关闭插件...';
    hotspot.disabled = true;
    campus.disabled = true;
    forceClose.disabled = true;

    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      hotspot.disabled = false;
      campus.disabled = false;
      forceClose.disabled = false;
      if (xhr.status !== 200) {
        result.textContent = '强制关闭失败';
        return;
      }
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        result.textContent = (typeof data.message === 'string' && data.message !== '') ? data.message : '已强制关闭插件';
        if (data.ok) {
          location.reload();
        }
      } catch (e) {
        result.textContent = '强制关闭失败';
      }
    };
    xhr.send('action=' + encodeURIComponent('force_stop'));
  }

  hotspot.addEventListener('click', function() { enqueue('switch_hotspot'); });
  campus.addEventListener('click', function() { enqueue('switch_campus'); });
  forceClose.addEventListener('click', enqueueForceClose);
})();
</script>
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

<script type="text/javascript">
(function() {
if (window.__smartTablesInit) return;
window.__smartTablesInit = true;

var campusData = ]] .. campus_json .. [[;
var hotspotData = ]] .. hotspot_json .. [[;
var modalType = '';
var modalEditId = '';
var modalSaveHandler = null;

function renderPasswordField(containerId, fieldId, value) {
  var container = document.getElementById(containerId);
  if (!container) return;
  L.require('ui').then(function(ui) {
    var widget = new ui.Textfield(value || '', {
      id: fieldId,
      password: true,
      optional: true
    });
    return Promise.resolve(widget.render()).then(function(node) {
      container.innerHTML = '';
      container.appendChild(node);
    });
  });
}

function getFieldValue(id) {
  var node = document.getElementById('widget.' + id) || document.getElementById(id);
  return node ? node.value : '';
}

function setRowDisabled(rowId, inputId, disabled) {
  var row = document.getElementById(rowId);
  var input = document.getElementById(inputId);
  if (!row || !input) return;
  input.disabled = !!disabled;
  row.style.opacity = disabled ? '0.55' : '1';
}

function updateCampusAccessModeUI() {
  var mode = document.getElementById('jm-access_mode');
  if (!mode) return;
  var wired = mode.value === 'wired';
  setRowDisabled('jm-ssid-row', 'jm-ssid', wired);
  setRowDisabled('jm-bssid-row', 'jm-bssid', wired);
  setRowDisabled('jm-radio-row', 'jm-radio', wired);
}

function showNativeModal(title, bodyHtml, afterOpen, onSave) {
  var body = document.createElement('div');
  body.innerHTML = bodyHtml;

  var buttonRow = document.createElement('div');
  buttonRow.className = 'right';

  var cancelBtn = document.createElement('button');
  cancelBtn.type = 'button';
  cancelBtn.className = 'btn cbi-button';
  cancelBtn.textContent = '取消';
  cancelBtn.onclick = function() { L.hideModal(); };

  var saveBtn = document.createElement('button');
  saveBtn.type = 'button';
  saveBtn.className = 'btn cbi-button cbi-button-save important';
  saveBtn.textContent = '保存';
  saveBtn.onclick = function() {
    if (typeof modalSaveHandler === 'function')
      modalSaveHandler();
  };

  buttonRow.appendChild(cancelBtn);
  buttonRow.appendChild(document.createTextNode(' '));
  buttonRow.appendChild(saveBtn);

  modalSaveHandler = onSave;
  L.showModal(title, [ body, buttonRow ], 'cbi-modal');
  if (typeof afterOpen === 'function')
    afterOpen();
}

window.smartSetDefault = function(kind, id) {
  var fd = new FormData();
  fd.append('action', 'set_default_' + kind);
  fd.append('id', id);
  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
  xhr.onload = function() {
    var message = '已保存默认配置';
    if (xhr.status === 200) {
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        if (typeof data.message === 'string' && data.message !== '')
          message = data.message;
      } catch (e) {}
    }
    alert(message);
    location.reload();
  };
  xhr.send(fd);
};

window.smartDelete = function(kind, id) {
  if (!confirm('确定要删除此项吗？')) return;
  var fd = new FormData();
  fd.append('action', 'delete_' + kind);
  fd.append('id', id);
  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
  xhr.onload = function() { location.reload(); };
  xhr.send(fd);
};

function findById(arr, id) {
  for (var i = 0; i < arr.length; i++) {
    if (arr[i].id === id) return arr[i];
  }
  return null;
}

window.smartEditCampus = function(id) {
  modalType = 'campus';
  modalEditId = id;
  var item = id ? findById(campusData, id) : {};

  // 动态构建运营商选项
  var schoolDataEl = document.getElementById('smart-school-data');
  var allSchools = [];
  try { allSchools = JSON.parse(schoolDataEl ? (schoolDataEl.value || schoolDataEl.textContent || '[]') : '[]'); } catch(e) {}
  var curSchoolSel = document.getElementById('widget.cbid.smart_srun.main.school')
    || document.getElementById('cbid.smart_srun.main.school')
    || document.querySelector('select[name="cbid.smart_srun.main.school"]');
  var curSchool = curSchoolSel ? curSchoolSel.value : 'jxnu';
  var schoolObj = null;
  for (var si = 0; si < allSchools.length; si++) {
    if (allSchools[si].short_name === curSchool) { schoolObj = allSchools[si]; break; }
  }
  var ops = (schoolObj && schoolObj.operators && schoolObj.operators.length) ? schoolObj.operators : [
    {id:'cmcc', label:'中国移动'}, {id:'ctcc', label:'中国电信'},
    {id:'cucc', label:'中国联通'}, {id:'xn', label:'校内网'}
  ];
  var noSuffixOps = (schoolObj && schoolObj.no_suffix_operators) ? schoolObj.no_suffix_operators : ['xn'];
  var opOptions = '';
  for (var oi = 0; oi < ops.length; oi++) {
    var sel = (ops[oi].id === (item.operator || ops[0].id)) ? ' selected' : '';
    var badge = ops[oi].verified ? ' [已验证]' : '';
    opOptions += '<option value="' + ops[oi].id + '"' + sel + '>' + ops[oi].label + badge + '</option>';
  }

  var bodyHtml =
    '<div class="smart-native-row"><label>标签（选填）</label><input id="jm-label" value="' + (item.label || '') + '"></div>' +
    '<div class="smart-native-row"><label>学工号</label><input id="jm-user_id" value="' + (item.user_id || '') + '"></div>' +
    '<div class="smart-native-row"><label>运营商</label><select id="jm-operator">' + opOptions + '</select></div>' +
    '<div class="smart-native-row"><label>运营商后缀（留空则为默认）</label><input id="jm-operator_suffix" value="' + (item.operator_suffix || '') + '" placeholder=""></div>' +
    '<div class="smart-native-row"><label>接入方式</label><select id="jm-access_mode"><option value="wifi"' + (((item.access_mode || 'wifi')==='wifi')?' selected':'') + '>无线</option><option value="wired"' + ((item.access_mode==='wired')?' selected':'') + '>有线（WAN）</option></select></div>' +
    '<div class="smart-native-row"><label>密码</label><div id="jm-password-field"></div></div>' +
    '<div class="smart-native-row"><label>认证地址</label><input id="jm-base_url" value="' + (item.base_url || 'http://172.17.1.2') + '"></div>' +
    '<div class="smart-native-row"><label>AC_ID</label><input id="jm-ac_id" value="' + (item.ac_id || '1') + '"></div>' +
    '<div class="smart-native-row" id="jm-ssid-row"><label>校园网 SSID</label><input id="jm-ssid" value="' + (item.ssid || 'jxnu_stu') + '"></div>' +
    '<div class="smart-native-row" id="jm-bssid-row"><label>BSSID（留空则不锁定）</label><input id="jm-bssid" value="' + (item.bssid || '') + '"></div>' +
    '<div class="smart-native-row" id="jm-radio-row"><label>频段</label><select id="jm-radio">]] .. radio_options .. [[</select></div>';

  // 后缀 placeholder 联动函数
  var _noSuffixOps = noSuffixOps;
  function updateSuffixPlaceholder() {
    var opSel = document.getElementById('jm-operator');
    var sfx = document.getElementById('jm-operator_suffix');
    if (!opSel || !sfx) return;
    var opId = opSel.value;
    var isNoSuffix = false;
    for (var k = 0; k < _noSuffixOps.length; k++) {
      if (_noSuffixOps[k] === opId) { isNoSuffix = true; break; }
    }
    sfx.placeholder = isNoSuffix ? '(无后缀)' : ('留空则使用 "' + opId + '"');
  }

  showNativeModal(
    id ? '编辑校园网账号' : '新增校园网账号',
    bodyHtml,
    function() {
      document.getElementById('jm-radio').value = item.radio || '';
      document.getElementById('jm-access_mode').addEventListener('change', updateCampusAccessModeUI);
      document.getElementById('jm-operator').addEventListener('change', updateSuffixPlaceholder);
      updateCampusAccessModeUI();
      updateSuffixPlaceholder();
      renderPasswordField('jm-password-field', 'jm-password', item.password || '');
    },
    function() { smartModalSave(); }
  );
};

window.smartEditHotspot = function(id) {
  modalType = 'hotspot';
  modalEditId = id;
  var item = id ? findById(hotspotData, id) : {};
  var bodyHtml =
    '<div class="smart-native-row"><label>标签（选填）</label><input id="jm-label" value="' + (item.label || '') + '"></div>' +
    '<div class="smart-native-row"><label>SSID</label><input id="jm-ssid" value="' + (item.ssid || '') + '"></div>' +
    '<div class="smart-native-row"><label>加密方式</label><select id="jm-encryption"><option value="none"' + (item.encryption==='none'?' selected':'') + '>开放(none)</option><option value="psk"' + (item.encryption==='psk'?' selected':'') + '>WPA-PSK</option><option value="psk2"' + ((item.encryption==='psk2'||!item.encryption)?' selected':'') + '>WPA2-PSK</option><option value="psk-mixed"' + (item.encryption==='psk-mixed'?' selected':'') + '>WPA/WPA2</option><option value="sae"' + (item.encryption==='sae'?' selected':'') + '>WPA3-SAE</option><option value="sae-mixed"' + (item.encryption==='sae-mixed'?' selected':'') + '>WPA2/WPA3</option></select></div>' +
    '<div class="smart-native-row"><label>密码</label><div id="jm-key-field"></div></div>' +
    '<div class="smart-native-row"><label>频段</label><select id="jm-radio">]] .. radio_options .. [[</select></div>';
  showNativeModal(
    id ? '编辑热点配置' : '新增热点配置',
    bodyHtml,
    function() {
      document.getElementById('jm-encryption').value = item.encryption || 'psk2';
      document.getElementById('jm-radio').value = item.radio || '';
      renderPasswordField('jm-key-field', 'jm-key', item.key || '');
    },
    function() { smartModalSave(); }
  );
};

window.smartModalSave = function() {
  var fd = new FormData();
  fd.append('action', (modalEditId ? 'edit_' : 'add_') + modalType);
  if (modalEditId) fd.append('id', modalEditId);

  if (modalType === 'campus') {
    fd.append('label', document.getElementById('jm-label').value);
    fd.append('user_id', document.getElementById('jm-user_id').value);
    fd.append('operator', document.getElementById('jm-operator').value);
    fd.append('operator_suffix', document.getElementById('jm-operator_suffix').value);
    fd.append('access_mode', document.getElementById('jm-access_mode').value);
    fd.append('password', getFieldValue('jm-password'));
    fd.append('base_url', document.getElementById('jm-base_url').value);
    fd.append('ac_id', document.getElementById('jm-ac_id').value);
    fd.append('ssid', document.getElementById('jm-ssid').value);
    fd.append('bssid', document.getElementById('jm-bssid').value);
    fd.append('radio', document.getElementById('jm-radio').value);
  } else {
    fd.append('label', document.getElementById('jm-label').value);
    fd.append('ssid', document.getElementById('jm-ssid').value);
    fd.append('encryption', document.getElementById('jm-encryption').value);
    fd.append('key', getFieldValue('jm-key'));
    fd.append('radio', document.getElementById('jm-radio').value);
  }

  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
  xhr.onload = function() {
    L.hideModal();
    location.reload();
  };
  xhr.send(fd);
};
})();
</script>
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
<script type="text/javascript">
(function() {
  var box = document.getElementById('smart-srun-log-box');
  var pre = document.getElementById('smart-srun-log-pre');
  var toggle = document.getElementById('smart-srun-refresh-toggle');
  if (!box || !pre || !toggle || window.__smartSrunLogInit) return;
  window.__smartSrunLogInit = true;
  var autoRefresh = true;
  var timer = null;

  function atBottom() {
    return (box.scrollHeight - box.scrollTop - box.clientHeight) < 24;
  }
  function stickBottom() {
    box.scrollTop = box.scrollHeight;
  }
  function setToggleText() {
    toggle.textContent = autoRefresh ? '刷新: 开' : '刷新: 关';
  }
  function refresh() {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', '/cgi-bin/luci/admin/services/smart_srun/log_tail?lines=80&format=friendly&_=' + Date.now(), true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4 || xhr.status !== 200) return;
      try {
        var data = JSON.parse(xhr.responseText || '{}');
        if (typeof data.log !== 'string') return;
        var keepBottom = atBottom();
        pre.textContent = data.log;
        if (keepBottom) stickBottom();
      } catch (e) {}
    };
    xhr.send(null);
  }
  function startLoop() {
    if (timer) clearInterval(timer);
    timer = setInterval(function() {
      if (autoRefresh) refresh();
    }, 2000);
  }
  toggle.addEventListener('click', function() {
    autoRefresh = !autoRefresh;
    setToggleText();
    if (autoRefresh) refresh();
  });

  setToggleText();
  stickBottom();
  startLoop();
})();
</script>
]]
end

function m.parse(self, ...)
    changed = false
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
