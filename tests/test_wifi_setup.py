import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "root/usr/lib/smart_srun"))
import wifi_setup as wifi


PLAN = dict(section="smart_srun_setup_radio0", radio="radio0", iface="wwan", ssid="Campus O'Neil $()",
            encryption="psk2", key="pass'word $()", unchanged=False)
WIRELESS = """config wifi-device 'radio0'
 option type 'mac80211'
config wifi-iface 'home_ap'
 option device 'radio0'
 option mode 'ap'
 option network 'lan'
 option ssid 'Home'
"""
NETWORK = """config interface 'lan'
 option proto 'static'
 option device 'br-lan'
 option ipaddr '192.0.2.1'
"""
FIREWALL = """config zone
 option name 'lan'
 list network 'lan'
 option input 'ACCEPT'
config zone
 option name 'wan'
 option input 'REJECT'
 option masq '1'
 list network 'wan'
config forwarding
 option src 'lan'
 option dest 'wan'
"""


class WifiPlanningTests(unittest.TestCase):
    def plan_scan(self, payload, entries, interfaces=None):
        with mock.patch.object(wifi.wireless, "parse_wireless_iface_data", return_value=interfaces or {}), \
                mock.patch.object(wifi, "_connected", return_value="192.0.2.2"), \
                mock.patch.object(wifi.wireless_ap, "_wireless_status", return_value={"radio0": {}}), \
                mock.patch.object(wifi.wireless_ap, "_scan_device", return_value=("radio0", "")), \
                mock.patch.object(wifi.wireless_ap, "_iwinfo", return_value={"results": entries}):
            return wifi._plan(dict(ssid="Campus", **payload))

    def test_requested_modes_filter_scan_and_preserve_uci_encryption(self):
        modes = {
            "none": {"enabled": False},
            "psk": {"enabled": True, "authentication": ["psk"], "wpa": [1]},
            "psk2": {"enabled": True, "authentication": ["psk"], "wpa": [2]},
            "psk-mixed": {"enabled": True, "authentication": ["psk"], "wpa": [1, 2]},
            "sae": {"enabled": True, "authentication": ["sae"], "wpa": [3]},
            "sae-mixed": {"enabled": True, "authentication": ["psk", "sae"], "wpa": [2, 3]},
        }
        for mode, observed in modes.items():
            with self.subTest(mode=mode):
                entry = dict(ssid="Campus", bssid="02:11:22:33:44:55", signal=-40, encryption=observed)
                result = self.plan_scan({"encryption": mode, "key": "test-password"}, [entry])
                self.assertEqual(result["encryption"], mode)
                self.assertEqual(result["key"], "" if mode == "none" else "test-password")

    def test_wrong_security_does_not_silently_select_open_or_enterprise_network(self):
        for observed in ({"enabled": False}, {"enabled": True, "authentication": ["eap"], "wpa": [2]}):
            entry = dict(ssid="Campus", bssid="02:11:22:33:44:55", signal=-40, encryption=observed)
            with self.subTest(observed=observed), self.assertRaisesRegex(ValueError, "加密方式"):
                self.plan_scan({"encryption": "psk2", "key": "test-password"}, [entry])

    def test_auto_with_password_ignores_stronger_open_duplicate(self):
        entries = [
            dict(ssid="Campus", bssid="02:11:22:33:44:55", signal=-20, encryption={"enabled": False}),
            dict(ssid="Campus", bssid="02:11:22:33:44:66", signal=-60,
                 encryption={"enabled": True, "authentication": ["psk"], "wpa": [2]}),
        ]
        self.assertEqual(self.plan_scan({"key": "test-password"}, entries)["encryption"], "psk2")

    def test_manual_mode_change_does_not_reuse_incompatible_existing_connection(self):
        interfaces = {"sta": dict(mode="sta", ssid="Campus", device="radio0", network="wwan",
                                  encryption="psk2", key="test-password")}
        entry = dict(ssid="Campus", bssid="02:11:22:33:44:55", signal=-40,
                     encryption={"enabled": True, "authentication": ["sae"], "wpa": [3]})
        result = self.plan_scan({"encryption": "sae", "key": "test-password"}, [entry], interfaces)
        self.assertFalse(result["unchanged"])
        self.assertEqual(result["encryption"], "sae")

    def test_unknown_mode_and_short_mixed_psk_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "不支持"), \
                mock.patch.object(wifi.wireless, "parse_wireless_iface_data") as read:
            wifi._plan({"ssid": "Campus", "encryption": "wpa2-enterprise"})
        read.assert_not_called()
        entry = dict(ssid="Campus", bssid="02:11:22:33:44:55", signal=-40,
                     encryption={"enabled": True, "authentication": ["psk"], "wpa": [1, 2]})
        with self.assertRaisesRegex(ValueError, "8–63"):
            self.plan_scan({"encryption": "psk-mixed", "key": "short"}, [entry])

    def test_existing_association_requires_measured_ssid_and_ipv4(self):
        with mock.patch.object(wifi.wireless_ap, "read_association", return_value={
                "ssid": "Other", "bssid": "02:11:22:33:44:55"}), \
                mock.patch.object(wifi, "get_ipv4_from_network_interface", return_value="192.0.2.3") as ip:
            self.assertEqual(wifi._connected("sta", "wwan", "Campus"), "")
            ip.assert_not_called()

    def test_connected_ssid_is_reused_without_scanning_or_writing(self):
        opts = dict(mode="sta", ssid="Campus", device="radio1", network="wwan", key="wifi-key", encryption="psk2")
        with mock.patch.object(wifi.wireless, "parse_wireless_iface_data", return_value={"sta": opts}), \
                mock.patch.object(wifi, "_connected", return_value="192.0.2.2"), \
                mock.patch.object(wifi.wireless_ap, "_wireless_status") as scan:
            result = wifi._plan({"ssid": "Campus"})
        self.assertTrue(result["unchanged"])
        self.assertEqual(result["key"], "wifi-key")
        scan.assert_not_called()

    def test_ambiguous_connection_requires_explicit_interface(self):
        opts = dict(mode="sta", ssid="Campus", device="radio1")
        with mock.patch.object(wifi.wireless, "parse_wireless_iface_data", return_value={
                "sta1": dict(opts, network="wwan"), "sta2": dict(opts, network="wwan2")}), \
                mock.patch.object(wifi, "_connected", return_value="192.0.2.2"):
            with self.assertRaisesRegex(ValueError, "多个出口"):
                wifi._plan({"ssid": "Campus"})
            self.assertEqual(wifi._plan({"ssid": "Campus", "iface": "wwan2"})["section"], "sta2")

    def test_cannot_select_an_unrelated_active_client_radio(self):
        with mock.patch.object(wifi.wireless, "parse_wireless_iface_data", return_value={
                "sta": dict(mode="sta", ssid="Other", network="other", device="radio0")}), \
                mock.patch.object(wifi.wireless_ap, "_wireless_status", return_value={"radio0": {}}), \
                mock.patch.object(wifi.wireless_ap, "_iwinfo") as scan:
            with self.assertRaises(ValueError):
                wifi._plan({"ssid": "Campus"})
            scan.assert_not_called()

    def test_scan_selects_radio_and_requires_wireless_password(self):
        ap = dict(ssid="Campus", bssid="02:11:22:33:44:55", signal=-40,
                  encryption={"enabled": True, "authentication": ["psk"], "wpa": [2]})
        with mock.patch.object(wifi.wireless, "parse_wireless_iface_data", return_value={}), \
                mock.patch.object(wifi.wireless_ap, "_wireless_status", return_value={"radio0": {}}), \
                mock.patch.object(wifi.wireless_ap, "_scan_device", return_value=("radio0", "")), \
                mock.patch.object(wifi.wireless_ap, "_iwinfo", return_value={"results": [ap]}):
            with self.assertRaisesRegex(ValueError, "需要无线密码"):
                wifi._plan({"ssid": "Campus"})
            result = wifi._plan({"ssid": "Campus", "key": "test-password"})
            self.assertEqual((result["radio"], result["iface"], result["encryption"]), ("radio0", "wwan", "psk2"))

    def test_job_path_does_not_accept_traversal(self):
        for job in ("../x", "a" * 31, "a" * 32 + "/../other"):
            with self.subTest(job=job), self.assertRaises(ValueError):
                wifi._path(job)


@unittest.skipUnless(shutil.which("uci"), "real UCI integration runs on OpenWrt")
class WifiUciIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = self.temp.name
        os.mkdir(self.directory + "/delta")
        for name, content in zip(wifi.PACKAGES, (WIRELESS, NETWORK, FIREWALL)):
            Path(self.directory, name).write_text(content, encoding="utf-8")

    def test_real_uci_builds_sta_dhcp_and_firewall_without_changing_home_ap(self):
        wifi._stage(self.directory, PLAN)
        self.assertIn("smart_srun_setup_radio0", Path(self.directory, "wireless").read_text())
        self.assertEqual(Path(self.directory, "delta", "wireless").read_text(), "")
        wireless = wifi._sections(self.directory, "wireless")
        self.assertEqual(wireless["home_ap"]["ssid"], ["Home"])
        sta = wireless[PLAN["section"]]
        self.assertEqual(sta["mode"], ["sta"])
        self.assertEqual(sta["ssid"], [PLAN["ssid"]])
        self.assertEqual(sta["key"], [PLAN["key"]])
        self.assertEqual(wifi._sections(self.directory, "network")["wwan"]["proto"], ["dhcp"])
        zones = wifi._sections(self.directory, "firewall").values()
        self.assertTrue(any("wwan" in zone.get("network", []) and zone.get("name") == ["wan"] for zone in zones))
        wifi._stage(self.directory, PLAN)
        zones = wifi._sections(self.directory, "firewall").values()
        self.assertEqual(sum(zone.get("network", []).count("wwan") for zone in zones), 1)

    def test_rejects_lan_and_pending_network_owner(self):
        with self.assertRaisesRegex(ValueError, "其它网络"):
            wifi._stage(self.directory, dict(PLAN, iface="lan"))
        self.assertEqual(wifi._sections(self.directory, "wireless")["home_ap"]["ssid"], ["Home"])

    def test_requested_encryption_is_written_and_open_mode_removes_old_key(self):
        for encryption in ("sae", "sae-mixed", "psk-mixed", "none"):
            with self.subTest(encryption=encryption):
                wifi._stage(self.directory, dict(PLAN, encryption=encryption))
                sta = wifi._sections(self.directory, "wireless")[PLAN["section"]]
                self.assertEqual(sta["encryption"], [encryption])
                if encryption == "none":
                    self.assertNotIn("key", sta)
                else:
                    self.assertEqual(sta["key"], [PLAN["key"]])

    def test_worker_restores_config_and_service_when_dhcp_fails(self):
        import daemon

        before = {name: Path(self.directory, name).read_bytes() for name in wifi.PACKAGES}
        job_root = self.directory + "/jobs"
        job = "a" * 32
        path = Path(job_root, job)
        path.mkdir(parents=True)
        (path / "input.json").write_text(json.dumps({"ssid": PLAN["ssid"]}))
        (path / "status.json").write_text('{"state":"starting"}')
        real_command = wifi._command
        services = []
        def command(args, *extra, **kwargs):
            if args[0] == "/etc/init.d/smart_srun":
                services.append(args[1]); return ""
            if args[:3] == ["uci", "-q", "changes"]:
                return ""
            return real_command(args, *extra, **kwargs)
        with mock.patch.object(wifi, "JOB_ROOT", job_root), \
                mock.patch.object(wifi, "CONFIG_ROOT", self.directory), \
                mock.patch.object(wifi, "_plan", return_value=PLAN), \
                mock.patch.object(wifi, "_reload"), \
                mock.patch.object(wifi, "_command", side_effect=command), \
                mock.patch.object(wifi, "_connected", side_effect=RuntimeError("DHCP unavailable")), \
                mock.patch.object(daemon, "daemon_is_alive", return_value=True):
            wifi.worker(job)
            result = wifi.status(job)
        self.assertEqual(result["state"], "failed")
        self.assertIn("已恢复原网络", result["message"])
        self.assertEqual(services, ["stop", "start"])
        for name in wifi.PACKAGES:
            self.assertEqual(Path(self.directory, name).read_bytes(), before[name])
        self.assertFalse((path / "account.json").exists())

    def test_worker_only_keeps_changes_after_commit_and_never_exposes_key(self):
        import daemon

        job_root = self.directory + "/jobs"
        job = "b" * 32
        path = Path(job_root, job)
        path.mkdir(parents=True)
        (path / "input.json").write_text(json.dumps({"ssid": PLAN["ssid"]}))
        (path / "status.json").write_text('{"state":"starting"}')
        real_write, real_command = wifi._write, wifi._command
        states = []
        def write(filename, payload):
            real_write(filename, payload)
            if filename.endswith("status.json"):
                states.append(payload)
                self.assertNotIn(PLAN["key"], json.dumps(payload))
                if payload["state"] == "ready":
                    self.assertEqual(wifi.account_fields(job, PLAN["ssid"])["key"], PLAN["key"])
                    wifi.control(job, "commit")
        def command(args, *extra, **kwargs):
            if args[0] == "/etc/init.d/smart_srun" or args[:3] == ["uci", "-q", "changes"]:
                return ""
            return real_command(args, *extra, **kwargs)
        with mock.patch.object(wifi, "JOB_ROOT", job_root), \
                mock.patch.object(wifi, "CONFIG_ROOT", self.directory), \
                mock.patch.object(wifi, "_plan", return_value=PLAN), \
                mock.patch.object(wifi, "_reload"), \
                mock.patch.object(wifi, "_write", side_effect=write), \
                mock.patch.object(wifi, "_command", side_effect=command), \
                mock.patch.object(wifi, "_connected", return_value="192.0.2.2"), \
                mock.patch.object(daemon, "daemon_is_alive", return_value=False):
            wifi.worker(job)
        self.assertEqual([item["state"] for item in states], ["connecting", "ready", "done"])
        sta = wifi._sections(self.directory, "wireless")[PLAN["section"]]
        self.assertEqual(sta["ssid"], [PLAN["ssid"]])
        self.assertEqual(sta["key"], [PLAN["key"]])
        self.assertFalse((path / "account.json").exists())


if __name__ == "__main__":
    unittest.main()
