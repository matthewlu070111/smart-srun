"""AP selection uses measured, radio-scoped data and never changes wireless config."""

import copy
import json
import sys
import unittest
from pathlib import Path
from unittest import mock

LIB_DIR = Path(__file__).resolve().parents[1] / "root" / "usr" / "lib" / "smart_srun"
if str(LIB_DIR) not in sys.path:
    sys.path.insert(0, str(LIB_DIR))

import wireless_ap  # noqa: E402


SSID = "Example AP Lab"
SECTION = "lab_client"
RADIO = "lab_radio"
STA_IFNAME = "labsta0"
AP_IFNAME = "labap0"


def _bssid(index):
    return "02:11:22:%02x:%02x:%02x" % ((index >> 16) & 255, (index >> 8) & 255, index & 255)


def _status(channel="auto"):
    return {
        RADIO: {
            "up": True,
            "config": {"channel": channel},
            "interfaces": [
                {"section": SECTION, "ifname": STA_IFNAME,
                 "config": {"mode": "sta", "ssid": SSID, "bssid": _bssid(99), "key": "private-fixture"}},
                {"section": "lab_access", "ifname": AP_IFNAME, "config": {"mode": "ap"}},
            ],
        },
        "other_radio": {
            "config": {"channel": "auto"},
            "interfaces": [{"section": "other_access", "ifname": "otherap0", "config": {"mode": "ap"}}],
        },
    }


def _entry(index, signal=-50, **fields):
    entry = {"ssid": SSID, "bssid": _bssid(index), "signal": signal,
             "channel": 6, "mode": "Master", "encryption": {"enabled": False}}
    entry.update(fields)
    return entry


def _security(versions, authentication):
    return {"enabled": True, "wpa": versions, "authentication": authentication, "ciphers": ["ccmp"]}


class BssidTests(unittest.TestCase):
    def test_valid_bssid_accepts_real_unicast_shape(self):
        for value in (_bssid(1), "AA:BB:CC:DD:EE:02", " 00:11:22:33:44:55 "):
            with self.subTest(value=value):
                self.assertTrue(wireless_ap.valid_bssid(value))

    def test_invalid_or_non_unicast_bssid_is_rejected(self):
        for value in (None, 123, "", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff",
                      "01:11:22:33:44:55", "02-11-22-33-44-55", "02:11:22:33:44",
                      "02:11:22:33:44:gg", "02:11:22:33:44:55:66"):
            with self.subTest(value=value):
                self.assertFalse(wireless_ap.valid_bssid(value))


class CandidateTests(unittest.TestCase):
    def scan(self, entries, status=None, profile=None, section=SECTION, radio=RADIO,
             device=STA_IFNAME, scan_response=None):
        status = _status() if status is None else status
        profile = {"ssid": SSID, "encryption": "none"} if profile is None else profile

        def command(args, timeout):
            if args == ["ubus", "call", "network.wireless", "status"]:
                self.assertLessEqual(timeout, 2)
                return True, json.dumps(status)
            if args[:4] == ["ubus", "call", "iwinfo", "scan"]:
                self.assertEqual(json.loads(args[4]), {"device": device})
                self.assertLessEqual(timeout, 8)
                return scan_response if scan_response is not None else (True, json.dumps({"results": entries}))
            self.fail("Unexpected command: %r" % args)

        with mock.patch.object(wireless_ap, "run_cmd", side_effect=command) as run:
            result = wireless_ap.select_candidates(section, radio, profile)
        return result, run.call_args_list

    def test_all_results_are_considered_before_limiting_to_three(self):
        entries = [_entry(index, -90) for index in range(100)]
        entries.extend([_entry(100, -42), _entry(101, -25), _entry(102, -31)])
        (candidates, reason), calls = self.scan(entries)
        self.assertEqual(reason, "")
        self.assertEqual([item["bssid"] for item in candidates], [_bssid(101), _bssid(102), _bssid(100)])
        self.assertEqual([item["signal"] for item in candidates], [-25, -31, -42])
        self.assertEqual(len(calls), 2)  # One status read and exactly one scan.
        self.assertTrue(all(set(item) == {"bssid", "ssid", "signal", "channel"} for item in candidates))

    def test_ties_and_duplicate_bssids_are_stable(self):
        entries = [_entry(3, -50), _entry(2, -50), _entry(1, -60),
                   _entry(1, -50, bssid=_bssid(1).upper()), _entry(1, -50, channel=11)]
        (first, _), _ = self.scan(entries)
        (second, _), _ = self.scan(list(reversed(entries)))
        self.assertEqual(first, second)
        self.assertEqual([item["bssid"] for item in first], [_bssid(1), _bssid(2), _bssid(3)])
        self.assertEqual(first[0]["channel"], 6)

    def test_invalid_ssid_mode_mac_rssi_channel_and_security_are_filtered(self):
        entries = [None, [], {}, _entry(1, ssid=SSID + " "), _entry(2, mode="Client"),
                   _entry(3, bssid="ff:ff:ff:ff:ff:ff"), _entry(4, bssid="00:00:00:00:00:00"),
                   _entry(5, 0), _entry(6, -200), _entry(7, None), _entry(8, True),
                   _entry(9, -float("inf")), _entry(10, channel=0), _entry(11, channel=True),
                   _entry(12, encryption={}), _entry(13, encryption=_security([2], ["psk"])),
                   _entry(14, -44, mode="AP")]
        (candidates, reason), _ = self.scan(entries)
        self.assertEqual(reason, "")
        self.assertEqual([item["bssid"] for item in candidates], [_bssid(14)])

    def test_exact_ssid_can_include_whitespace(self):
        wanted = " Example AP Lab "
        (candidates, _), _ = self.scan(
            [_entry(1, ssid=SSID), _entry(2, ssid=wanted)],
            profile={"ssid": wanted, "encryption": "none"},
        )
        self.assertEqual([item["ssid"] for item in candidates], [wanted])

    def test_unsigned_signal_is_converted_to_negative_dbm(self):
        (candidates, _), _ = self.scan([_entry(1, 2**32 - 45)])
        self.assertEqual(candidates[0]["signal"], -45)

    def test_security_compatibility(self):
        cases = [
            ("none", {"enabled": False}, True),
            ("none", {"enabled": 0}, True),
            ("none", {}, False),
            ("none", _security([2], ["psk"]), False),
            ("psk", _security([1], ["psk"]), True),
            ("psk", _security([2], ["psk"]), False),
            ("psk2", _security([2], ["psk"]), True),
            ("psk2", _security([2], ["802.1x"]), False),
            ("psk2", _security([3], ["sae"]), False),
            ("psk-mixed", _security([1, 2], ["psk"]), True),
            ("psk-mixed", _security([2], ["psk"]), True),
            ("sae", _security([3], ["sae"]), True),
            ("sae", _security([2], ["sae"]), True),
            ("sae", _security([2], ["psk"]), False),
            ("sae-mixed", _security([2], ["psk"]), True),
            ("sae-mixed", _security([2, 3], ["psk", "sae"]), True),
            ("sae-mixed", _security([1], ["psk"]), False),
            ("psk2", {"enabled": True, "wep": ["open"]}, False),
            ("psk2", {"enabled": True, "wpa": 2, "authentication": "psk"}, False),
        ]
        for configured, observed, compatible in cases:
            with self.subTest(configured=configured, observed=observed):
                (candidates, reason), _ = self.scan(
                    [_entry(1, encryption=observed)], profile={"ssid": SSID, "encryption": configured}
                )
                self.assertEqual(bool(candidates), compatible)
                self.assertEqual(bool(reason), not compatible)

    def test_fixed_numeric_radio_channel_is_respected(self):
        for channel in (6, "6"):
            with self.subTest(channel=channel):
                (candidates, _), _ = self.scan([_entry(1, -20, channel=11), _entry(2, -70)], _status(channel))
                self.assertEqual([item["bssid"] for item in candidates], [_bssid(2)])

    def test_auto_channel_does_not_filter_scan_channels(self):
        (candidates, _), _ = self.scan([_entry(1, -20, channel=11), _entry(2, -70)])
        self.assertEqual(candidates[0]["channel"], 11)

    def test_disabled_sta_uses_only_an_ap_on_its_radio(self):
        status = _status()
        status[RADIO]["interfaces"][0]["config"]["disabled"] = "1"
        (candidates, reason), _ = self.scan([_entry(1)], status, device=AP_IFNAME)
        self.assertEqual(reason, "")
        self.assertEqual(len(candidates), 1)

    def test_absent_sta_uses_same_radio_ap(self):
        status = _status()
        status[RADIO]["interfaces"].pop(0)
        (candidates, _), _ = self.scan([_entry(1)], status, device=AP_IFNAME)
        self.assertEqual(len(candidates), 1)

    def test_no_same_radio_interface_uses_explicit_phy_or_radio(self):
        for phy in (None, "labphy0"):
            with self.subTest(phy=phy):
                status = _status()
                status[RADIO]["interfaces"] = []
                if phy:
                    status[RADIO]["config"]["phy"] = phy
                (candidates, _), _ = self.scan([_entry(1)], status, device=phy or RADIO)
                self.assertEqual(len(candidates), 1)

    def test_missing_radio_can_be_inferred_from_unique_sta(self):
        (candidates, _), _ = self.scan([_entry(1)], radio="")
        self.assertEqual(len(candidates), 1)

    def test_ambiguous_or_wrong_radio_mapping_never_scans(self):
        ambiguous = _status()
        ambiguous["other_radio"]["interfaces"].append(copy.deepcopy(ambiguous[RADIO]["interfaces"][0]))
        disabled = _status()
        disabled[RADIO]["disabled"] = True
        wrong_mode = _status()
        wrong_mode[RADIO]["interfaces"][0]["config"]["mode"] = "ap"
        for status, radio in ((_status(), "other_radio"), (ambiguous, RADIO), (disabled, RADIO), (wrong_mode, RADIO)):
            with self.subTest(radio=radio, status=status):
                (candidates, reason), calls = self.scan([], status, radio=radio)
                self.assertEqual(candidates, [])
                self.assertTrue(reason)
                self.assertEqual(len(calls), 1)

    def test_scan_failure_or_bad_json_falls_back_without_another_scan(self):
        for response in ((False, "not found private-fixture"), (True, "<html>bad</html>"),
                         (True, "[]"), (True, '{"results": {}}')):
            with self.subTest(response=response):
                (candidates, reason), calls = self.scan([], scan_response=response)
                self.assertEqual(candidates, [])
                self.assertIn("系统自动", reason)
                self.assertNotIn("private-fixture", reason)
                self.assertEqual(len(calls), 2)

    def test_unavailable_status_and_unsupported_security_are_safe(self):
        with mock.patch.object(wireless_ap, "run_cmd", return_value=(False, "private-fixture")) as run:
            candidates, reason = wireless_ap.select_candidates(SECTION, RADIO, {"ssid": SSID})
            self.assertEqual(candidates, [])
            self.assertNotIn("private-fixture", reason)
            self.assertEqual(run.call_count, 1)
            run.reset_mock()
            candidates, reason = wireless_ap.select_candidates(SECTION, RADIO, {"ssid": SSID, "encryption": "wpa2"})
            self.assertEqual(candidates, [])
            self.assertIn("安全方式", reason)
            run.assert_not_called()

    def test_invalid_command_text_falls_back_without_leaking_output(self):
        error = UnicodeDecodeError("utf-8", b"\xff", 0, 1, "invalid SSID byte")
        with mock.patch.object(wireless_ap, "run_cmd", side_effect=error):
            candidates, reason = wireless_ap.select_candidates(SECTION, RADIO, {"ssid": SSID})
        self.assertEqual(candidates, [])
        self.assertIn("系统自动", reason)


class AssociationTests(unittest.TestCase):
    def association(self, info=None, link="Not connected.", status=None):
        status = _status() if status is None else status

        def command(args, timeout):
            self.assertLessEqual(timeout, 2)
            if args == ["ubus", "call", "network.wireless", "status"]:
                return True, json.dumps(status)
            if args[:4] == ["ubus", "call", "iwinfo", "info"]:
                self.assertEqual(json.loads(args[4]), {"device": STA_IFNAME})
                return (True, json.dumps(info)) if info is not None else (False, "not found")
            if args == ["iw", "dev", STA_IFNAME, "link"]:
                return True, link
            self.fail("Unexpected command: %r" % args)

        with mock.patch.object(wireless_ap, "run_cmd", side_effect=command) as run:
            result = wireless_ap.read_association(SECTION)
        return result, run.call_args_list

    def test_info_uses_actual_sta_bssid_not_configured_bssid(self):
        for mode in ("Client", "Station"):
            with self.subTest(mode=mode):
                association, calls = self.association(_entry(1, -52, mode=mode))
                self.assertEqual(association, {"ifname": STA_IFNAME, "ssid": SSID,
                                              "bssid": _bssid(1), "signal": -52, "channel": 6})
                self.assertNotEqual(association["bssid"], _bssid(99))
                self.assertEqual(len(calls), 2)

    def test_ap_mode_is_not_reported_as_client_association(self):
        association, calls = self.association(_entry(1, mode="Master"))
        self.assertEqual(association, {})
        self.assertEqual(len(calls), 2)

    def test_no_measured_association_does_not_use_config(self):
        for info in ({}, _entry(1, mode="Client", bssid="00:00:00:00:00:00")):
            with self.subTest(info=info):
                association, _ = self.association(info)
                self.assertEqual(association, {})

    def test_known_association_survives_missing_or_invalid_metrics(self):
        for signal, channel in ((None, None), (0, 0), (-45, None), (None, 11)):
            with self.subTest(signal=signal, channel=channel):
                info = _entry(1, signal, mode="Client", channel=channel)
                association, calls = self.association(info)
                self.assertEqual(association["bssid"], _bssid(1))
                self.assertEqual(association["ssid"], SSID)
                self.assertEqual(association["signal"], signal or None)
                self.assertEqual(association["channel"], channel or None)
                self.assertEqual(len(calls), 2)

    def test_iw_link_known_association_without_metrics(self):
        link = "Connected to %s (on %s)\n\tSSID: %s\n" % (_bssid(2), STA_IFNAME, SSID)
        association, _ = self.association(link=link)
        self.assertEqual(association, {"ifname": STA_IFNAME, "ssid": SSID, "bssid": _bssid(2),
                                      "signal": None, "channel": None})

    def test_iw_link_fallback_reports_real_link_and_frequency(self):
        for frequency, channel in ((2412, 1), (2484, 14), (5180, 36), (5955, 1)):
            with self.subTest(frequency=frequency):
                link = "Connected to %s (on %s)\n\tSSID: %s\n\tfreq: %d\n\tsignal: -53.50 dBm\n" % (
                    _bssid(2), STA_IFNAME, SSID, frequency)
                association, calls = self.association(link=link)
                self.assertEqual(association, {"ifname": STA_IFNAME, "ssid": SSID,
                                              "bssid": _bssid(2), "signal": -53, "channel": channel})
                self.assertEqual(len(calls), 3)

    def test_iw_link_for_a_different_interface_is_rejected(self):
        link = "Connected to %s (on othersta0)\n\tSSID: %s\n\tfreq: 2412\n\tsignal: -40 dBm\n" % (_bssid(2), SSID)
        association, _ = self.association(link=link)
        self.assertEqual(association, {})

    def test_absent_disabled_or_non_sta_target_never_reads_ap_info(self):
        absent = _status()
        absent[RADIO]["interfaces"].pop(0)
        disabled = _status()
        disabled[RADIO]["interfaces"][0]["disabled"] = True
        wrong_mode = _status()
        wrong_mode[RADIO]["interfaces"][0]["config"]["mode"] = "ap"
        for status in (absent, disabled, wrong_mode):
            with self.subTest(status=status):
                association, calls = self.association(status=status)
                self.assertEqual(association, {})
                self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()
