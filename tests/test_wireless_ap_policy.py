"""AP policy integration at the UCI, association and authentication boundaries."""

import itertools
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


MODULE_ROOT = Path(__file__).resolve().parents[1] / "root/usr/lib/smart_srun"
if str(MODULE_ROOT) not in sys.path:
    sys.path.insert(0, str(MODULE_ROOT))

from _portal_urls import CLIENT_IP, PORTAL_ORIGIN  # noqa: E402
import daemon  # noqa: E402
import orchestrator  # noqa: E402
import snapshot  # noqa: E402
import wireless  # noqa: E402


AP_OLD = "02:00:00:00:00:10"
AP_FIRST = "02:00:00:00:00:11"
AP_SECOND = "02:00:00:00:00:12"
AP_THIRD = "02:00:00:00:00:13"
SECTION = "campus_sta"


def campus_config(**updates):
    cfg = {
        "school": "default", "campus_account_id": "example", "username": "example-user",
        "base_url": PORTAL_ORIGIN, "campus_access_mode": "wifi", "campus_ssid": "Example Campus",
        "campus_radio": "radio1", "campus_encryption": "none", "campus_ap_selection": "strongest",
        "campus_bssid": "", "sta_iface": SECTION, "failover_enabled": "0", "multi_wan_enabled": "0",
        "switch_ready_timeout_seconds": "7", "enabled": "1",
    }
    cfg.update(updates)
    return cfg


def station_data():
    return {SECTION: {
        "mode": "sta", "ssid": "Example Campus", "device": "radio1", "network": "wwan",
        "disabled": "0", "encryption": "none", "bssid": AP_OLD, "jxnu_auto": "1",
        "smart_srun_ap_selection": "strongest",
    }}


class WirelessFixture(unittest.TestCase):
    def setUp(self):
        self.cfg = campus_config()
        self.data = station_data()
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.record_path = str(Path(self.directory.name) / "ap-selection.json")
        self.enterContext(mock.patch.object(wireless, "AP_SELECTION_FILE", self.record_path))
        self.enterContext(mock.patch.object(wireless, "log"))
        self.enterContext(mock.patch.object(wireless.time, "sleep"))
        self.commands = self.enterContext(mock.patch.object(wireless, "run_cmd", return_value=(True, "")))
        self.enterContext(mock.patch.object(wireless, "parse_wireless_iface_data", return_value=self.data))
        self.enterContext(mock.patch.object(wireless, "get_ipv4_from_network_interface", return_value=CLIENT_IP))


class CampusAPConnectionTests(WirelessFixture):
    def setUp(self):
        super().setUp()
        candidates = [
            {"bssid": bssid, "signal": signal, "channel": 36}
            for bssid, signal in ((AP_FIRST, -40), (AP_SECOND, -55), (AP_THIRD, -70))
        ]
        self.scan = self.enterContext(mock.patch.object(wireless, "select_candidates", return_value=(candidates, "")))
        self.wait = self.enterContext(mock.patch.object(wireless, "wait_for_sta_ipv4", return_value=("wwan", CLIENT_IP)))
        self.enterContext(mock.patch.object(wireless, "read_association", return_value={"bssid": AP_FIRST}))
        self.auth = self.enterContext(mock.patch.object(orchestrator.srun_auth, "run_once_safe"))

    def connect(self, **kwargs):
        target = wireless.build_expected_profile(self.cfg, expect_hotspot=False)
        result = wireless._connect_campus_ap(self.cfg, SECTION, target, self.data, **kwargs)
        self.auth.assert_not_called()
        return result

    def selected_bssids(self):
        return [call.kwargs.get("expected_bssid", "") for call in self.wait.call_args_list]

    def test_first_success_stops_after_one_scan_and_one_association(self):
        self.assertEqual(self.connect(), (True, "", CLIENT_IP))
        self.scan.assert_called_once_with(SECTION, "radio1", wireless.build_expected_profile(self.cfg, False))
        self.assertEqual(self.selected_bssids(), [AP_FIRST])
        self.assertIn(mock.call(["uci", "set", "wireless.%s.bssid=%s" % (SECTION, AP_FIRST)]),
                      self.commands.call_args_list)

    def test_association_or_dhcp_failure_tries_next_candidate_without_rescanning(self):
        self.wait.side_effect = [("wwan", None), ("wwan", CLIENT_IP)]
        self.assertEqual(self.connect(), (True, "", CLIENT_IP))
        self.scan.assert_called_once()
        self.assertEqual(self.selected_bssids(), [AP_FIRST, AP_SECOND])
        self.assertTrue(all(call.kwargs["timeout_seconds"] == 7 for call in self.wait.call_args_list))
        self.assertTrue(all(call.kwargs["expected_ssid"] == self.cfg["campus_ssid"]
                            for call in self.wait.call_args_list))

    def test_all_candidates_fail_after_at_most_three_attempts(self):
        self.scan.return_value[0].append({"bssid": "02:00:00:00:00:14", "signal": -80, "channel": 36})
        self.wait.return_value = ("wwan", None)
        self.assertEqual(self.connect(), (True, "", None))
        self.scan.assert_called_once()
        self.assertEqual(self.selected_bssids(), [AP_FIRST, AP_SECOND, AP_THIRD])

    def test_failed_scan_uses_system_selection_and_clears_previous_pin(self):
        self.scan.return_value = ([], "scan unavailable")
        self.assertEqual(self.connect(), (True, "", CLIENT_IP))
        self.assertEqual(self.selected_bssids(), [""])
        self.assertIn(mock.call(["uci", "-q", "delete", "wireless.%s.bssid" % SECTION]),
                      self.commands.call_args_list)
        record = json.loads(Path(self.record_path).read_text(encoding="utf-8"))
        self.assertIn("scan unavailable", record["reason"])
        self.assertEqual(record["bssid"], "")

    def test_fixed_bssid_failure_does_not_scan_or_fall_back(self):
        self.cfg.update(campus_ap_selection="fixed", campus_bssid=AP_OLD)
        self.wait.return_value = ("wwan", None)
        self.assertEqual(self.connect(), (True, "", None))
        self.scan.assert_not_called()
        self.assertEqual(self.selected_bssids(), [AP_OLD])
        self.assertNotIn(mock.call(["uci", "-q", "delete", "wireless.%s.bssid" % SECTION]),
                         self.commands.call_args_list)

    def test_system_auto_ignores_saved_fixed_bssid_and_removes_uci_pin(self):
        self.cfg.update(campus_ap_selection="auto", campus_bssid=AP_OLD)
        self.assertEqual(self.connect(), (True, "", CLIENT_IP))
        self.scan.assert_not_called()
        self.assertEqual(self.selected_bssids(), [""])
        self.assertIn(mock.call(["uci", "-q", "delete", "wireless.%s.bssid" % SECTION]),
                      self.commands.call_args_list)

    def test_session_recovery_reuses_existing_pin_without_scanning(self):
        self.assertEqual(self.connect(reselect_ap=False), (True, "", CLIENT_IP))
        self.scan.assert_not_called()
        self.assertEqual(self.selected_bssids(), [AP_OLD])
        self.assertEqual(self.cfg["campus_bssid"], "", "automatic choice must not change account configuration")

    def test_uci_error_stops_before_association_or_candidate_retry(self):
        self.commands.return_value = (False, "write failed")
        ok, message, ip = self.connect()
        self.assertFalse(ok)
        self.assertIn("write failed", message)
        self.assertIsNone(ip)
        self.wait.assert_not_called()


class CampusAPGuardTests(WirelessFixture):
    def setUp(self):
        super().setUp()
        self.wait = self.enterContext(mock.patch.object(wireless, "wait_for_sta_ipv4", return_value=("wwan", CLIENT_IP)))
        self.switch = self.enterContext(mock.patch.object(wireless, "switch_sta_profile", return_value=(True, "ready")))
        self.scan = self.enterContext(mock.patch.object(wireless, "select_candidates"))

    def test_healthy_strongest_connection_does_not_scan_or_rebuild(self):
        self.assertEqual(wireless.ensure_expected_profile(self.cfg, False, 123), (True, "", 123))
        self.wait.assert_called_once_with(SECTION, timeout_seconds=1, interval_seconds=1,
                                          expected_bssid=AP_OLD, expected_ssid=self.cfg["campus_ssid"])
        self.switch.assert_not_called()
        self.scan.assert_not_called()

    def test_changed_radio_or_policy_rebuilds_even_with_existing_ipv4(self):
        for updates in ({"campus_radio": "radio0"}, {"campus_ap_selection": "auto"}):
            with self.subTest(updates=updates):
                self.switch.reset_mock()
                ok, _, _ = wireless.ensure_expected_profile(campus_config(**updates), False)
                self.assertTrue(ok)
                self.switch.assert_called_once()

    def test_explicit_policy_creates_missing_sta_without_hotspot_failover(self):
        self.data.clear()
        for policy in ("strongest", "fixed"):
            with self.subTest(policy=policy):
                self.switch.reset_mock()
                cfg = campus_config(campus_ap_selection=policy, campus_bssid=AP_OLD)
                ok, _, _ = wireless.ensure_expected_profile(cfg, False)
                self.assertTrue(ok)
                self.switch.assert_called_once_with(cfg, expect_hotspot=False)

    def test_fixed_guard_requires_the_configured_real_association(self):
        self.cfg.update(campus_ap_selection="fixed", campus_bssid=AP_OLD)
        self.data[SECTION]["smart_srun_ap_selection"] = "fixed"
        self.wait.return_value = ("wwan", None)
        wireless.ensure_expected_profile(self.cfg, False)
        self.wait.assert_called_once_with(SECTION, timeout_seconds=1, interval_seconds=1,
                                          expected_bssid=AP_OLD, expected_ssid=self.cfg["campus_ssid"])
        self.switch.assert_called_once()

    def test_wired_accounts_bypass_ap_management_and_auto_only_cleans_existing_pin(self):
        self.assertFalse(wireless.campus_ap_policy_enabled(campus_config(campus_access_mode="wired")))
        auto_cfg = campus_config(campus_ap_selection="auto")
        self.assertTrue(wireless.campus_ap_policy_enabled(auto_cfg))
        self.data[SECTION].update(bssid="", smart_srun_ap_selection="auto")
        self.assertFalse(wireless.campus_ap_policy_enabled(auto_cfg))
        self.assertTrue(wireless.campus_ap_policy_enabled(campus_config()))

    def test_same_ssid_disabled_section_on_other_radio_does_not_trigger_rebuild(self):
        self.data["a_other_radio"] = dict(self.data[SECTION], device="radio0", disabled="1")
        ok, _, _ = wireless.ensure_expected_profile(self.cfg, False)
        self.assertTrue(ok)
        self.assertEqual(self.wait.call_args.args[0], SECTION)
        self.switch.assert_not_called()

    def test_lost_strongest_association_rebuilds_despite_old_interface_ipv4(self):
        self.wait.return_value = ("wwan", None)
        wireless.ensure_expected_profile(self.cfg, False)
        self.assertEqual(self.wait.call_args.kwargs["expected_bssid"], AP_OLD)
        self.switch.assert_called_once()


class APRecoveryContextTests(WirelessFixture):
    def test_preclean_preserves_active_radio_and_ap_across_disabled_old_sections(self):
        self.cfg.update(campus_radio="", sta_iface="")

        def disable_stations(_cfg, data):
            for options in data.values():
                options["disabled"] = "1"
            return True, ""

        for old_radio, active_pin in itertools.product(("radio0", "radio1"), (AP_FIRST, "")):
            with self.subTest(old_radio=old_radio, active_pin=active_pin):
                self.data.clear()
                self.data.update(station_data())
                self.data[SECTION]["bssid"] = active_pin
                self.data["a_old_sta"] = dict(
                    self.data[SECTION], device=old_radio, disabled="1", bssid=AP_OLD,
                )
                self.commands.reset_mock()
                with (
                    mock.patch.object(orchestrator, "log"),
                    mock.patch.object(orchestrator, "parse_wireless_iface_data", return_value=self.data),
                    mock.patch.object(orchestrator, "disable_managed_sta_sections", side_effect=disable_stations),
                    mock.patch.object(wireless, "apply_default_selection_for_runtime",
                                      side_effect=lambda *_: (dict(self.cfg), False, "")) as reload_config,
                    mock.patch.object(wireless, "ensure_runtime_wireless_prerequisites",
                                      return_value=(True, "", self.data)),
                    mock.patch.object(wireless, "ensure_named_managed_sta_sections", return_value=(True, [])),
                    mock.patch.object(wireless, "parse_radio_bands", return_value={"radio0": "2g", "radio1": "5g"}),
                    mock.patch.object(wireless, "read_association", return_value={
                        "ssid": self.cfg["campus_ssid"], "bssid": AP_FIRST,
                    }),
                    mock.patch.object(wireless, "select_candidates") as scan,
                    mock.patch.object(wireless, "wait_for_sta_ipv4", return_value=("wwan", CLIENT_IP)) as wait,
                    mock.patch.object(wireless, "test_portal_reachability", return_value=(True, "")),
                    mock.patch.object(orchestrator.srun_auth, "run_once_safe") as auth,
                ):
                    self.assertEqual(orchestrator.clean_slate_for_manual_login(
                        self.cfg, reselect_ap=False,
                    ), (True, ""))

                reload_config.assert_called_once()
                scan.assert_not_called()
                auth.assert_not_called()
                wait.assert_called_once()
                selected_section = wait.call_args.args[0]
                self.assertEqual(self.data[selected_section]["device"], "radio1")
                self.assertEqual(wait.call_args.kwargs["expected_bssid"], AP_FIRST)
                self.assertEqual(wait.call_args.kwargs["expected_ssid"], self.cfg["campus_ssid"])
                self.assertIn(mock.call([
                    "uci", "set", "wireless.%s.bssid=%s" % (selected_section, AP_FIRST),
                ]), self.commands.call_args_list)
                self.assertEqual(self.cfg["campus_radio"], "", "recovery must not persist its radio override")
                self.assertEqual(self.cfg["campus_bssid"], "", "recovery must not persist its selected AP")


class AssociationReadinessTests(WirelessFixture):
    def test_stale_ipv4_cannot_satisfy_a_different_or_unknown_fixed_association(self):
        for association in ({}, {"bssid": AP_SECOND, "ssid": self.cfg["campus_ssid"]},
                            {"bssid": AP_FIRST, "ssid": "Other SSID"}):
            with (
                self.subTest(association=association),
                mock.patch.object(wireless, "read_association", return_value=association),
                mock.patch.object(wireless.time, "time", side_effect=itertools.count(0, 0.25)),
            ):
                net, ip = wireless.wait_for_sta_ipv4(SECTION, timeout_seconds=1,
                    expected_bssid=AP_FIRST, expected_ssid=self.cfg["campus_ssid"])
                self.assertEqual(net, "wwan")
                self.assertIsNone(ip)

    def test_matching_observed_bssid_and_ssid_accept_the_ipv4(self):
        with mock.patch.object(wireless, "read_association", return_value={
            "bssid": AP_FIRST, "ssid": self.cfg["campus_ssid"],
        }):
            self.assertEqual(wireless.wait_for_sta_ipv4(SECTION, timeout_seconds=1,
                expected_bssid=AP_FIRST, expected_ssid=self.cfg["campus_ssid"]), ("wwan", CLIENT_IP))


class APSnapshotTests(WirelessFixture):
    def setUp(self):
        super().setUp()
        self.cfg["username"] = ""
        self.enterContext(mock.patch.object(snapshot, "parse_wireless_iface_data", return_value=self.data))
        self.enterContext(mock.patch.object(snapshot, "get_ipv4_from_network_interface", return_value=CLIENT_IP))
        self.enterContext(mock.patch.object(snapshot, "test_internet_connectivity", return_value=(True, "")))

    def test_snapshot_displays_observed_bssid_signal_and_channel_instead_of_config(self):
        actual = {"bssid": AP_SECOND, "ssid": self.cfg["campus_ssid"], "signal": -53,
                  "channel": 36, "ifname": "phy1-sta0"}
        with mock.patch.object(snapshot, "read_association", return_value=actual):
            result = snapshot.build_runtime_snapshot(self.cfg, state={})
        self.assertEqual(result["current_bssid"], AP_SECOND)
        self.assertEqual(result["current_signal"], -53)
        self.assertEqual(result["current_channel"], 36)
        self.assertEqual(result["current_wireless_ifname"], "phy1-sta0")

    def test_missing_observation_does_not_present_configured_bssid_as_real(self):
        with mock.patch.object(snapshot, "read_association", return_value={}):
            result = snapshot.build_runtime_snapshot(self.cfg, state={})
        self.assertEqual(result["current_bssid"], "")
        self.assertIsNone(result["current_signal"])
        self.assertIsNone(result["current_channel"])

    def test_selection_reason_does_not_leak_between_accounts_or_observed_aps(self):
        record = {"account_id": "example", "section": SECTION, "ssid": self.cfg["campus_ssid"],
                  "policy": "strongest", "bssid": AP_FIRST, "reason": "selected in this scan"}
        Path(self.record_path).write_text(json.dumps(record), encoding="utf-8")
        for account, bssid, expected in (("example", AP_FIRST, True), ("other", AP_FIRST, False),
                                         ("example", AP_SECOND, False), ("example", "", False)):
            with self.subTest(account=account, bssid=bssid):
                cfg = dict(self.cfg, campus_account_id=account)
                _, reason = wireless.get_ap_selection_status(cfg, SECTION, {"bssid": bssid})
                self.assertEqual(reason == record["reason"], expected)


class APAuthenticationBoundaryTests(unittest.TestCase):
    def test_daemon_checks_explicit_policy_before_auth_when_failover_is_disabled(self):
        cfg = campus_config()
        state = daemon._make_daemon_state()
        with (
            mock.patch.object(daemon, "ensure_expected_profile", return_value=(False, "association failed", 10)) as prepare,
            mock.patch.object(daemon.srun_auth, "query_online_identity") as query,
            mock.patch.object(orchestrator, "run_once_with_retry") as auth,
        ):
            message, _ = daemon._daemon_tick_active(cfg, state, 60)
        prepare.assert_called_once_with(cfg, expect_hotspot=False, last_switch_ts=0)
        self.assertIn("association failed", message)
        query.assert_not_called()
        auth.assert_not_called()

    def test_daemon_does_not_repeat_authentication_for_an_online_account(self):
        cfg = campus_config()
        state = daemon._make_daemon_state()
        with (
            mock.patch.object(daemon, "ensure_expected_profile", return_value=(True, "", 0)) as prepare,
            mock.patch.object(daemon, "resolve_bind_ip", return_value=CLIENT_IP),
            mock.patch.object(daemon.srun_auth, "query_online_identity", return_value=(True, cfg["username"], "online")),
            mock.patch.object(orchestrator, "run_once_with_retry") as auth,
        ):
            daemon._daemon_tick_active(cfg, state, 60)
        prepare.assert_called_once()
        auth.assert_not_called()
        self.assertTrue(state["was_online"])

    def test_e2620_recovery_requests_reconnect_without_ap_reselection(self):
        cfg = campus_config(backoff_enable="1", backoff_max_retries="1")
        with (
            mock.patch.object(orchestrator, "log"),
            mock.patch.object(orchestrator, "load_config", return_value=cfg),
            mock.patch.object(orchestrator, "backoff_enabled", return_value=True),
            mock.patch.object(orchestrator, "in_quiet_window", return_value=False),
            mock.patch.object(orchestrator, "_pending_runtime_action", return_value=""),
            mock.patch.object(orchestrator, "clean_slate_for_manual_login", return_value=(True, "")) as rebuild,
            mock.patch.object(orchestrator.srun_auth, "run_once_safe", side_effect=[
                (False, "E2620: already online"), (True, "ok"),
            ]) as auth,
        ):
            ok, _ = orchestrator.run_once_with_retry(cfg)
        self.assertTrue(ok)
        rebuild.assert_called_once_with(cfg, reselect_ap=False)
        self.assertEqual(auth.call_count, 2)

    def test_manual_preclean_forwards_the_no_reselection_boundary(self):
        cfg = campus_config()
        with (
            mock.patch.object(orchestrator, "log"),
            mock.patch.object(orchestrator, "parse_wireless_iface_data", return_value=station_data()),
            mock.patch.object(orchestrator, "disable_managed_sta_sections", return_value=(True, "")),
            mock.patch.object(orchestrator, "switch_to_campus", return_value=(True, "")) as switch,
        ):
            self.assertEqual(orchestrator.clean_slate_for_manual_login(cfg, reselect_ap=False), (True, ""))
        switch.assert_called_once_with(cfg, reselect_ap=False, ap_context={
            "ssid": cfg["campus_ssid"], "radio": "radio1", "bssid": AP_OLD,
        })


if __name__ == "__main__":
    unittest.main()
