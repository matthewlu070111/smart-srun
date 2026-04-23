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
        self.assertIn('channel = channel,', self.controller_text)
        self.assertIn('return read_plugin_full_log_text()', self.controller_text)
        self.assertIn('local function resolve_network_source_lines(lines, download_mode)', self.controller_text)
        self.assertIn('local source_lines = requested * 4', self.controller_text)
        self.assertIn('local plugin_text = read_plugin_log_text(source_lines)', self.controller_text)
        self.assertIn('local system_text = read_system_log_text(source_lines)', self.controller_text)

    def test_cbi_log_panel_renders_channel_switcher_and_toolbar(self):
        self.assertIn('tail -n 100 /var/log/smart_srun.log', self.cbi_text)
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


if __name__ == "__main__":
    unittest.main()
