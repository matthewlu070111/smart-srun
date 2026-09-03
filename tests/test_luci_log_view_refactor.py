from pathlib import Path
import unittest


REPO_ROOT = Path(__file__).resolve().parents[1]
CONTROLLER_FILE = (
    REPO_ROOT / "root" / "usr" / "lib" / "lua" / "luci" / "controller" / "smart_srun.lua"
)
CBI_FILE = (
    REPO_ROOT / "root" / "usr" / "lib" / "lua" / "luci" / "model" / "cbi" / "smart_srun.lua"
)
JS_FILE = REPO_ROOT / "root" / "www" / "luci-static" / "resources" / "smart_srun.js"


class LuciLogViewRefactorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.controller_text = CONTROLLER_FILE.read_text(encoding="utf-8")
        cls.cbi_text = CBI_FILE.read_text(encoding="utf-8")
        cls.js_text = JS_FILE.read_text(encoding="utf-8")

    def test_controller_declares_network_event_allowlist(self):
        for event_name in [
            "bind_ip_resolved",
            "http_fetch",
            "http_fetch_result",
            "connectivity_probe_begin",
            "connectivity_probe_result",
            "dns_probe_failed",
            "srun_challenge",
            "srun_challenge_result",
            "srun_login_submit",
            "srun_login_response",
            "srun_online_query",
            "srun_online_result",
            "ip_wait_progress",
            "ip_wait_result",
            "wifi_reload",
            "sta_section_disabled",
            "uci_wireless_update",
        ]:
            self.assertIn(event_name, self.controller_text)

    def test_controller_uses_channel_parameter_and_info_prefix(self):
        self.assertIn('local channel = http.formvalue("channel") or "plugin"', self.controller_text)
        self.assertIn('local download_mode = tostring(http.formvalue("download") or "") == "1"', self.controller_text)
        self.assertIn('channel = channel == "network" and "network" or "plugin"', self.controller_text)
        self.assertIn('local lines = tonumber(http.formvalue("lines")) or 1000', self.controller_text)
        self.assertIn('[信息]', self.controller_text)
        self.assertIn('if not zh then', self.controller_text)
        self.assertIn('local suffix = extract_structured_suffix(rest, level, event)', self.controller_text)
        self.assertIn('parts[#parts + 1] = " " .. suffix', self.controller_text)
        self.assertIn('local function parse_structured_fields(rest, level, event)', self.controller_text)
        self.assertIn('local function append_friendly_fields(parts, field_list, field_map, skipped)', self.controller_text)
        self.assertIn('channel = channel,', self.controller_text)
        self.assertIn('return read_plugin_full_log_text()', self.controller_text)
        self.assertIn('local function resolve_network_source_lines(lines, download_mode)', self.controller_text)
        self.assertIn('local source_lines = requested * 4', self.controller_text)
        self.assertIn('local plugin_text = read_plugin_log_text(source_lines)', self.controller_text)
        self.assertIn('local system_text = read_system_log_text(source_lines)', self.controller_text)
        self.assertIn('local function tail_text(text, lines)', self.controller_text)
        self.assertIn('local read_file_tail', self.controller_text)
        self.assertIn('read_file_tail(LOG_FILE, 1)', self.controller_text)
        self.assertIn('return read_file_tail(LOG_FILE, lines)', self.controller_text)
        self.assertNotIn('tail -n 1 /var/log/smart_srun.log', self.controller_text)
        self.assertIn('"logread -l " .. lines .. " 2>/dev/null"', self.controller_text)
        self.assertIn('"logread 2>/dev/null"', self.controller_text)
        self.assertIn('return tail_text(text, lines)', self.controller_text)
        self.assertNotIn('"logread 2>/dev/null | tail -n " .. lines', self.controller_text)

    def test_friendly_log_translation_preserves_structured_context(self):
        # Known translated events still need the original structured fields; otherwise
        # LuCI shows only a friendly description and drops the useful detail.
        for key, label in [
            ("url", "URL"),
            ("status_code", "状态码"),
            ("duration_ms", "耗时"),
            ("queue_lag_ms", "排队"),
            ("bytes_received", "字节"),
            ("error_code", "错误码"),
            ("username_reported", "在线账号"),
            ("bind_ip", "绑定IP"),
        ]:
            self.assertIn(f'"{key}"', self.controller_text)
            self.assertIn(f'{key} = "{label}"', self.controller_text)
        self.assertIn('append_friendly_fields(parts, field_list, field_map, skip_fields("account", "reason", "attempt"))', self.controller_text)
        self.assertIn('append_message_detail(parts, rest, has_detail)', self.controller_text)
        self.assertIn('hidden_friendly_fields', self.controller_text)
        self.assertIn('sensitive_friendly_key_parts', self.controller_text)
        self.assertIn('local function is_hidden_friendly_field(key)', self.controller_text)
        self.assertIn('is_hidden_friendly_field(key)', self.controller_text)

    def test_cbi_log_panel_renders_simple_toolbar(self):
        self.assertIn('local LOG_FILE = "/var/log/smart_srun.log"', self.cbi_text)
        self.assertIn('local function read_file_tail(path, lines)', self.cbi_text)
        self.assertIn('local t = read_file_tail(LOG_FILE, 100)', self.cbi_text)
        self.assertNotIn('tail -n 100 /var/log/smart_srun.log', self.cbi_text)
        self.assertIn('log_controller.friendly_log_text(t)', self.cbi_text)
        self.assertNotIn('smart-srun-log-channels', self.cbi_text)
        self.assertNotIn('data-channel="network"', self.cbi_text)
        for element_id in [
            'smart-srun-log-start',
            'smart-srun-log-stop',
            'smart-srun-log-clear',
            'smart-srun-log-download',
            'smart-srun-log-level-filter',
        ]:
            self.assertIn(element_id, self.cbi_text)
        self.assertIn('max-height:560px', self.cbi_text)

    def test_js_log_view_tracks_refresh_and_download_state(self):
        self.assertIn('var logState = {', self.js_text)
        self.assertIn('refreshing: true', self.js_text)
        self.assertIn("rawText: pre.textContent || ''", self.js_text)
        self.assertIn('log_tail?channel=plugin&lines=', self.js_text)
        self.assertNotIn('logState.channel', self.js_text)
        self.assertIn('downloadCurrentLog', self.js_text)
        self.assertIn("'smart_srun_plugin_'", self.js_text)
        self.assertIn('[信息]', self.js_text)
        self.assertIn('/cgi-bin/luci/admin/services/smart_srun/log_clear', self.js_text)
        self.assertIn('levelFilter.addEventListener', self.js_text)
        self.assertIn('data.ok', self.js_text)

    def test_controller_exposes_plugin_log_clear_endpoint(self):
        self.assertIn('log_clear', self.controller_text)
        self.assertIn('function action_log_clear()', self.controller_text)
        self.assertIn('fs.writefile(LOG_FILE, "")', self.controller_text)
        self.assertIn('channel = "plugin"', self.controller_text)

    def test_js_uses_short_live_window_and_full_download_window(self):
        # Live refresh hits the server with a small line count (perf), while download
        # uses a dedicated raw/full request path.
        self.assertIn('var LOG_LIVE_LINES = 100', self.js_text)
        self.assertIn('var LOG_DOWNLOAD_LINES = 0', self.js_text)
        self.assertIn("buildLogUrl(LOG_LIVE_LINES, 'friendly', false)", self.js_text)
        self.assertIn("buildLogUrl(LOG_DOWNLOAD_LINES, 'raw', true)", self.js_text)
        self.assertIn("'&format=' + encodeURIComponent(format || 'friendly')", self.js_text)
        self.assertIn("(download ? '&download=1' : '')", self.js_text)

    def test_js_display_level_filter_is_live_and_hooks_log_level_select(self):
        # Display-side level filter weights are owned by the log toolbar itself.
        self.assertIn('LOG_LEVEL_WEIGHTS', self.js_text)
        self.assertIn("ALL: 0", self.js_text)
        self.assertIn("ERROR: 40", self.js_text)
        self.assertIn('logLineWeight', self.js_text)
        self.assertIn('filterByLevel', self.js_text)
        self.assertNotIn('findLogLevelSelect', self.js_text)
        self.assertIn('displayLevel', self.js_text)
        self.assertIn("levelFilter.addEventListener('change'", self.js_text)
        self.assertIn('applyDisplayLevel', self.js_text)

    def test_js_skips_background_polling_when_page_hidden(self):
        self.assertIn('function isPageHidden()', self.js_text)
        self.assertIn('document.hidden === true', self.js_text)
        self.assertIn('function onPageVisible(callback)', self.js_text)
        self.assertIn("document.addEventListener('visibilitychange'", self.js_text)
        self.assertIn('if (isPageHidden()) return;', self.js_text)
        self.assertIn('onPageVisible(refreshOverview)', self.js_text)

    def test_school_preset_apply_button_is_explicit(self):
        # Keep a compact source contract for the preset UI; full form wiring is
        # covered by runtime/config tests rather than brittle string laundry lists.
        self.assertIn("smart-school-preset-data", self.cbi_text)
        self.assertIn("presets_refresh", self.controller_text)
        self.assertIn("refreshSchoolPresets", self.js_text)
        self.assertIn("jm-apply-school-defaults", self.js_text)
        self.assertIn("applySchoolDefaultsToForm", self.js_text)
        self.assertIn("jm-save-school-preset", self.js_text)
        self.assertIn("user_presets_set", self.js_text)
        self.assertIn("action_user_presets_set", self.controller_text)
        self.assertNotIn("localStorage", self.js_text)
        self.assertIn("jm-operator-suffix-hint", self.js_text)
        self.assertIn("operatorSuffixOf", self.js_text)
        self.assertNotIn("schoolDefaults.operator", self.js_text)
        self.assertNotIn("opId === 'xn' ? '' : opId", self.js_text)
        self.assertNotIn("不填则使用纯账号", self.js_text)

    def test_apply_school_preset_replaces_operator_choices_not_merges(self):
        # Switching school A -> school B must drop A's operators (e.g. nchu
        # "学生用户") and install B's list; custom entries stay.
        self.assertIn("function replacePresetOperators(preset)", self.js_text)
        self.assertIn("var nextOperators = replacePresetOperators(preset)", self.js_text)
        self.assertNotIn("function mergePresetOperators", self.js_text)
        self.assertNotIn("mergePresetOperators(preset)", self.js_text)
        # Non-custom rows are discarded before re-adding the new preset ops.
        self.assertIn("if (operatorChoices[i].custom) kept.push(operatorChoices[i])", self.js_text)
        self.assertIn("operatorChoices = kept", self.js_text)
        # Empty operator list from a preset must clear leftover suffix text.
        self.assertIn("if (emptySuffix) emptySuffix.value = ''", self.js_text)


if __name__ == "__main__":
    unittest.main()
