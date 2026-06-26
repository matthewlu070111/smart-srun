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

    def test_cbi_log_panel_renders_channel_switcher_and_toolbar(self):
        self.assertIn('local LOG_FILE = "/var/log/smart_srun.log"', self.cbi_text)
        self.assertIn('local function read_file_tail(path, lines)', self.cbi_text)
        self.assertIn('local t = read_file_tail(LOG_FILE, 100)', self.cbi_text)
        self.assertNotIn('tail -n 100 /var/log/smart_srun.log', self.cbi_text)
        self.assertIn('log_controller.friendly_log_text(t)', self.cbi_text)
        self.assertIn('data-channel="plugin"', self.cbi_text)
        self.assertIn('data-channel="network"', self.cbi_text)
        for element_id in [
            'smart-srun-log-start',
            'smart-srun-log-stop',
            'smart-srun-log-clear',
            'smart-srun-log-download',
        ]:
            self.assertIn(element_id, self.cbi_text)
        self.assertIn('max-height:560px', self.cbi_text)

    def test_cbi_channel_buttons_use_distinct_cbi_button_variant(self):
        # Channel tabs use a different cbi-button variant from the right-side action buttons
        # (which use cbi-button / cbi-button-apply). We pick action / neutral so themes
        # render them with a clearly different colour family.
        self.assertIn(
            'id="smart-srun-log-channel-plugin" data-channel="plugin" type="button" class="cbi-button cbi-button-action"',
            self.cbi_text,
        )
        self.assertIn(
            'id="smart-srun-log-channel-network" data-channel="network" type="button" class="cbi-button cbi-button-neutral"',
            self.cbi_text,
        )
        # No inline JS-set background should leak into the channel buttons; visual state lives in CSS classes.
        self.assertNotIn('id="smart-srun-log-channel-plugin" data-channel="plugin" type="button" style=', self.cbi_text)
        self.assertNotIn('id="smart-srun-log-channel-network" data-channel="network" type="button" style=', self.cbi_text)

    def test_js_log_view_tracks_channel_refresh_and_download_state(self):
        self.assertIn('var logState = {', self.js_text)
        self.assertIn("channel: 'plugin'", self.js_text)
        self.assertIn('refreshing: true', self.js_text)
        self.assertIn("rawText: pre.textContent || ''", self.js_text)
        self.assertIn('log_tail?channel=', self.js_text)
        self.assertIn('encodeURIComponent(logState.channel)', self.js_text)
        self.assertIn('downloadCurrentLog', self.js_text)
        self.assertIn("'smart_srun_' + logState.channel + '_'", self.js_text)
        self.assertIn('[信息]', self.js_text)

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
        # Display-side level filter weights and hook on the log_level dropdown.
        self.assertIn('LOG_LEVEL_WEIGHTS', self.js_text)
        self.assertIn("ALL: 0", self.js_text)
        self.assertIn("ERROR: 40", self.js_text)
        self.assertIn('logLineWeight', self.js_text)
        self.assertIn('filterByLevel', self.js_text)
        self.assertIn('findLogLevelSelect', self.js_text)
        self.assertIn('cbid.smart_srun.main.log_level', self.js_text)
        self.assertIn("levelSelect.addEventListener('change'", self.js_text)
        self.assertIn('displayLevel', self.js_text)

    def test_js_listens_via_event_delegation_for_widget_compat(self):
        # OpenWrt 22+/themes can render ListValue as a cbi-dropdown div, so a direct
        # listener on a <select> never fires. We must catch native change AND
        # cbi-dropdown-change at document level.
        self.assertIn('readLevelFromEvent', self.js_text)
        self.assertIn("document.addEventListener('change'", self.js_text)
        self.assertIn("document.addEventListener('cbi-dropdown-change'", self.js_text)
        self.assertIn('applyDisplayLevel', self.js_text)

    def test_js_skips_background_polling_when_page_hidden(self):
        self.assertIn('function isPageHidden()', self.js_text)
        self.assertIn('document.hidden === true', self.js_text)
        self.assertIn('document.webkitHidden === true', self.js_text)
        self.assertIn('function onPageVisible(callback)', self.js_text)
        self.assertIn("document.addEventListener('visibilitychange'", self.js_text)
        self.assertIn('if (!isPageHidden()) refreshOverview();', self.js_text)
        self.assertIn('if (isPageHidden()) return;', self.js_text)
        self.assertIn('if (logState.refreshing && !isPageHidden()) refresh();', self.js_text)
        self.assertIn('onPageVisible(refreshOverview)', self.js_text)
        self.assertIn('onPageVisible(function() {', self.js_text)

    def test_update_endpoints_and_frontend_flow_are_wired(self):
        for endpoint in [
            "update_check",
            "update_start",
            "update_status",
        ]:
            self.assertIn(endpoint, self.controller_text)
            self.assertIn(endpoint, self.js_text)
        self.assertIn('run_srunnet_json("update check")', self.controller_text)
        self.assertIn('run_srunnet_json("update run --background")', self.controller_text)
        self.assertIn('run_srunnet_json("update status")', self.controller_text)
        self.assertIn("确认自动更新到", self.js_text)
        self.assertIn("pollUpdateStatus", self.js_text)

    def test_school_preset_apply_button_is_explicit(self):
        self.assertIn("smart-school-preset-data", self.cbi_text)
        self.assertIn('run_client("presets list", false)', self.cbi_text)
        self.assertIn("schoolPresetList", self.js_text)
        self.assertIn("jm-school_preset", self.js_text)
        self.assertIn("jm-apply-school-defaults", self.js_text)
        self.assertIn("jm-reset-school-defaults", self.js_text)
        self.assertIn("applySchoolDefaultsToForm", self.js_text)
        self.assertIn("resetSchoolDefaultsForm", self.js_text)
        self.assertIn("initialValues.base_url", self.js_text)
        self.assertIn("应用预设", self.js_text)
        self.assertIn("无预设", self.js_text)
        self.assertIn("如何获取？", self.js_text)
        self.assertIn("refreshOperatorQuickpick", self.js_text)
        self.assertIn("applyOperatorPick", self.js_text)
        self.assertIn("jm-operator-suffix-hint", self.js_text)
        self.assertIn("未验证", self.js_text)
        self.assertIn("高级登录参数", self.js_text)
        self.assertIn("smart-native-advanced", self.cbi_text)
        self.assertIn("observed_login_shape", self.js_text)
        self.assertIn("applyLoginShapeToForm", self.js_text)
        self.assertIn("jm-login-n", self.js_text)
        self.assertIn("jm-login-type", self.js_text)
        self.assertIn("jm-login-enc", self.js_text)
        self.assertIn("jm-info-prefix", self.js_text)
        self.assertIn("jm-double-stack", self.js_text)
        self.assertIn("jm-login-os", self.js_text)
        self.assertIn("jm-login-name", self.js_text)
        self.assertIn("fd.append('info_prefix'", self.js_text)
        self.assertIn("login_os = fv(\"login_os\")", self.controller_text)
        self.assertIn("jm-detect-acid", self.js_text)
        self.assertIn("detectAcidForForm", self.js_text)
        self.assertIn("/cgi-bin/luci/admin/services/smart_srun/detect_acid", self.js_text)
        self.assertIn('run_srunnet_json("detect acid "', self.controller_text)
        self.assertIn("if (!id) applySchoolDefaultsToForm();", self.js_text)
        self.assertIn("DEFAULT_LOGIN_SHAPE", self.js_text)
        self.assertIn("'jm-login-os': DEFAULT_LOGIN_SHAPE.os", self.js_text)
        self.assertIn("operatorSuffixOf", self.js_text)
        self.assertNotIn("schoolDefaults.operator", self.js_text)
        self.assertNotIn("opId === 'xn' ? '' : opId", self.js_text)
        self.assertNotIn("operatorSuffixForPreset", self.js_text)
        self.assertNotIn("operator_suffix:'cmcc'", self.js_text)
        self.assertNotIn("operator_suffix: 'cmcc'", self.js_text)
        self.assertNotIn("operator_suffix:'ctcc'", self.js_text)
        self.assertNotIn("operator_suffix: 'ctcc'", self.js_text)
        self.assertNotIn("operator_suffix:'cucc'", self.js_text)
        self.assertNotIn("operator_suffix: 'cucc'", self.js_text)
        self.assertNotIn("不填则使用纯账号", self.js_text)
        self.assertNotIn("应用默认值", self.js_text)
        self.assertNotIn("留空则为默认", self.js_text)
        self.assertNotIn("留空则使用", self.js_text)
        self.assertNotIn("[已验证]", self.js_text)


if __name__ == "__main__":
    unittest.main()
