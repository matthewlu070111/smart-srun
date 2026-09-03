"""Unit tests for binding campus accounts to different network interfaces.

Originally written against #32's ``network_interface`` field. #31 shipped the
same concept as ``wired_iface`` and also binds the socket to the owning device,
so the two were merged onto ``wired_iface``. These tests keep #32's coverage --
including the read-side fallback that still accepts the old spelling.
"""

import sys
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parents[1]
LIB_DIR = REPO_ROOT / "root" / "usr" / "lib" / "smart_srun"

if str(LIB_DIR) not in sys.path:
    sys.path.insert(0, str(LIB_DIR))

from _portal_urls import WIRED_BIND_IP  # noqa: E402
import config  # noqa: E402
import wireless  # noqa: E402


class CampusNetworkInterfaceTests(unittest.TestCase):
    def test_normalize_defaults_to_wan(self):
        self.assertEqual(config.normalize_wired_iface(""), "wan")
        self.assertEqual(config.normalize_wired_iface(None), "wan")
        self.assertEqual(config.normalize_wired_iface("wan2"), "wan2")

    def test_resolve_active_items_uses_account_interface(self):
        raw = {
            "campus_accounts": [
                {
                    "id": "c1",
                    "access_mode": "wired",
                    "wired_iface": "wan2",
                    "user_id": "u1",
                    "operator_suffix": "",
                }
            ],
            "active_campus_id": "c1",
            "default_campus_id": "c1",
        }
        resolved = config.resolve_active_items(raw)
        self.assertEqual(resolved["campus_access_mode"], "wired")
        self.assertEqual(resolved["wired_iface"], "wan2")

    def test_resolve_active_items_accepts_legacy_network_interface(self):
        """#32's spelling still resolves, so hand-written configs keep working."""
        raw = {
            "campus_accounts": [
                {
                    "id": "c1",
                    "access_mode": "wired",
                    "network_interface": "wan2",
                    "user_id": "u1",
                }
            ],
            "active_campus_id": "c1",
            "default_campus_id": "c1",
        }
        resolved = config.resolve_active_items(raw)
        self.assertEqual(resolved["wired_iface"], "wan2")

    def test_wired_iface_wins_when_both_spellings_are_present(self):
        raw = {
            "campus_accounts": [
                {
                    "id": "c1",
                    "access_mode": "wired",
                    "wired_iface": "wan.v2",
                    "network_interface": "wan2",
                    "user_id": "u1",
                }
            ],
            "active_campus_id": "c1",
            "default_campus_id": "c1",
        }
        resolved = config.resolve_active_items(raw)
        self.assertEqual(resolved["wired_iface"], "wan.v2")

    def test_resolve_active_items_defaults_to_wan(self):
        raw = {
            "campus_accounts": [
                {"id": "c1", "access_mode": "wired", "user_id": "u1"}
            ],
            "active_campus_id": "c1",
            "default_campus_id": "c1",
        }
        resolved = config.resolve_active_items(raw)
        self.assertEqual(resolved["wired_iface"], "wan")

    def test_get_wired_iface_defaults_to_wan(self):
        self.assertEqual(config.get_wired_iface({}), "wan")
        self.assertEqual(config.get_wired_iface({"wired_iface": "wan6"}), "wan6")

    def test_loading_legacy_interface_exposes_canonical_account_for_editing(self):
        account = {
            "id": "c1",
            "user_id": "u1",
            "access_mode": "wired",
            "network_interface": " wan2 ",
            "auth_enabled": True,
        }
        raw = {
            "school": "default",
            "campus_accounts": [account],
            "active_campus_id": "c1",
            "default_campus_id": "c1",
        }
        with mock.patch.object(config, "load_json_raw_config", return_value=raw):
            loaded = config.load_config()

        editable = loaded["campus_accounts"][0]
        self.assertEqual("wan2", editable["wired_iface"])
        self.assertEqual("1", editable["auth_enabled"])
        self.assertNotIn("network_interface", editable)
        self.assertNotIn("wired_iface", account)
        editable["label"] = "Edited label"
        saved = config._normalize_json_raw_config(loaded)
        self.assertEqual("wan2", saved["campus_accounts"][0]["wired_iface"])
        self.assertNotIn("network_interface", saved["campus_accounts"][0])

    def test_save_normalizes_interface_alias_and_wired_guard_without_mutation(self):
        accounts = [
            {"id": "c1", "access_mode": "wired", "wired_iface": " wan3 ",
             "network_interface": "wan2", "auth_enabled": "yes"},
            {"id": "c2", "access_mode": "wired", "wired_iface": "  ",
             "network_interface": " wan4 ", "auth_enabled": "off"},
            {"id": "c3", "access_mode": "wifi", "auth_enabled": "1"},
        ]

        saved = config._normalize_json_raw_config({"campus_accounts": accounts})

        self.assertEqual(
            ["wan3", "wan4", "wan"],
            [a["wired_iface"] for a in saved["campus_accounts"]],
        )
        self.assertEqual(
            ["1", "0", "0"],
            [a["auth_enabled"] for a in saved["campus_accounts"]],
        )
        self.assertTrue(all(
            "network_interface" not in a for a in saved["campus_accounts"]
        ))
        self.assertEqual("yes", accounts[0]["auth_enabled"])
        self.assertEqual("wan2", accounts[0]["network_interface"])
        self.assertNotIn("wired_iface", accounts[2])

    def test_switch_to_campus_uses_bound_interface(self):
        cfg = {"campus_access_mode": "wired", "wired_iface": "wan2"}
        with (
            mock.patch.object(wireless, "campus_uses_wired", return_value=True),
            mock.patch.object(wireless, "parse_wireless_iface_data", return_value={}),
            mock.patch.object(
                wireless, "disable_managed_sta_sections", return_value=(True, "")
            ),
            mock.patch.object(
                wireless, "teardown_managed_sta_interfaces", return_value=(True, "")
            ),
            mock.patch.object(
                wireless, "wait_for_network_interface_ipv4", return_value=WIRED_BIND_IP
            ) as wf,
        ):
            ok, message = wireless.switch_to_campus(cfg)
            self.assertTrue(ok)
            self.assertIn("wan2", message)
            wf.assert_called_once_with(
                "wan2", timeout_seconds=wireless.get_switch_ready_timeout_seconds(cfg)
            )


if __name__ == "__main__":
    unittest.main()
