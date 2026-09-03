import os
import sys
import unittest
from unittest import mock

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import wireless

# Real `uci show wireless` shape on a mac80211 device (radio0 / radio1).
MAC80211_UCI = """wireless.radio0=wifi-device
wireless.radio0.type='mac80211'
wireless.radio0.band='2g'
wireless.radio0.channel='auto'
wireless.default_radio0=wifi-iface
wireless.default_radio0.device='radio0'
wireless.default_radio0.mode='ap'
wireless.radio1=wifi-device
wireless.radio1.type='mac80211'
wireless.radio1.band='5g'
wireless.default_radio1=wifi-iface
wireless.default_radio1.device='radio1'
wireless.default_radio1.mode='ap'
"""

# Real `uci show wireless` shape on MediaTek mtwifi (MT7981, ImmortalWrt 24.10).
MTWIFI_UCI = """wireless.MT7981_1_1=wifi-device
wireless.MT7981_1_1.type='mtwifi'
wireless.MT7981_1_1.phy='ra0'
wireless.MT7981_1_1.hwmode='11g'
wireless.MT7981_1_1.band='2g'
wireless.default_MT7981_1_1=wifi-iface
wireless.default_MT7981_1_1.device='MT7981_1_1'
wireless.default_MT7981_1_1.mode='ap'
wireless.MT7981_1_2=wifi-device
wireless.MT7981_1_2.type='mtwifi'
wireless.MT7981_1_2.phy='rax0'
wireless.MT7981_1_2.hwmode='11a'
wireless.MT7981_1_2.band='5g'
wireless.default_MT7981_1_2=wifi-iface
wireless.default_MT7981_1_2.device='MT7981_1_2'
wireless.default_MT7981_1_2.mode='ap'
wireless.wifinet2=wifi-iface
wireless.wifinet2.device='MT7981_1_2'
wireless.wifinet2.mode='sta'
wireless.wifinet2.ssid='campus'
"""

# hwmode only, no band -- older configs.
HWMODE_ONLY_UCI = """wireless.MT7981_1_1=wifi-device
wireless.MT7981_1_1.hwmode='11g'
wireless.MT7981_1_2=wifi-device
wireless.MT7981_1_2.hwmode='11a'
"""

# wifi-device sections with neither band nor hwmode -- exercises the fallback.
NO_BAND_UCI = """wireless.MT7981_1_1=wifi-device
wireless.MT7981_1_1.type='mtwifi'
wireless.MT7981_1_2=wifi-device
wireless.MT7981_1_2.type='mtwifi'
wireless.wifinet2=wifi-iface
wireless.wifinet2.device='MT7981_1_2'
"""


class WifiDeviceSectionTests(unittest.TestCase):
    def test_parses_mac80211_radio_names(self):
        self.assertEqual(
            wireless.parse_wifi_device_sections(MAC80211_UCI), ["radio0", "radio1"]
        )

    def test_parses_mtwifi_vendor_radio_names(self):
        self.assertEqual(
            wireless.parse_wifi_device_sections(MTWIFI_UCI),
            ["MT7981_1_1", "MT7981_1_2"],
        )

    def test_ignores_wifi_iface_sections(self):
        self.assertNotIn("wifinet2", wireless.parse_wifi_device_sections(MTWIFI_UCI))
        self.assertNotIn(
            "default_MT7981_1_1", wireless.parse_wifi_device_sections(MTWIFI_UCI)
        )

    def test_empty_output(self):
        self.assertEqual(wireless.parse_wifi_device_sections(""), [])
        self.assertEqual(wireless.parse_wifi_device_sections(None), [])


class RadioDetectionTests(unittest.TestCase):
    """MediaTek mtwifi names radios MT7981_1_1/_1_2, not radio0/radio1.

    The old regexes hardcoded radioN, so detection returned nothing and
    ensure_runtime_wireless_prerequisites reported
    '当前路由器未发现可用无线射频' on every reconnect attempt.
    """

    def _with_uci(self, text):
        return mock.patch.object(wireless, "run_cmd", return_value=(True, text))

    def test_mac80211_bands_still_detected(self):
        with self._with_uci(MAC80211_UCI):
            self.assertEqual(
                wireless.parse_radio_bands(), {"radio0": "2g", "radio1": "5g"}
            )
            self.assertEqual(
                wireless.get_available_wifi_radios(), ["radio1", "radio0"]
            )

    def test_mtwifi_bands_detected(self):
        with self._with_uci(MTWIFI_UCI):
            self.assertEqual(
                wireless.parse_radio_bands(),
                {"MT7981_1_1": "2g", "MT7981_1_2": "5g"},
            )

    def test_mtwifi_radios_are_found_5g_first(self):
        with self._with_uci(MTWIFI_UCI):
            self.assertEqual(
                wireless.get_available_wifi_radios(),
                ["MT7981_1_2", "MT7981_1_1"],
            )

    def test_hwmode_only_is_mapped_to_bands(self):
        with self._with_uci(HWMODE_ONLY_UCI):
            self.assertEqual(
                wireless.parse_radio_bands(),
                {"MT7981_1_1": "2g", "MT7981_1_2": "5g"},
            )

    def test_fallback_enumerates_devices_without_band(self):
        with self._with_uci(NO_BAND_UCI):
            self.assertEqual(wireless.parse_radio_bands(), {})
            self.assertEqual(
                wireless.get_available_wifi_radios(),
                ["MT7981_1_1", "MT7981_1_2"],
            )

    def test_no_radios_when_uci_unavailable(self):
        with mock.patch.object(wireless, "run_cmd", return_value=(False, "")):
            self.assertEqual(wireless.get_available_wifi_radios(), [])


if __name__ == "__main__":
    unittest.main()
