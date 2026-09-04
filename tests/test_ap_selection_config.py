"""AP policy migration, account isolation and runtime binding inputs."""

import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


MODULE_ROOT = Path(__file__).resolve().parents[1] / "root/usr/lib/smart_srun"
if str(MODULE_ROOT) not in sys.path:
    sys.path.insert(0, str(MODULE_ROOT))

import config  # noqa: E402


BSSID = "02:11:22:33:44:55"


class APSelectionConfigTests(unittest.TestCase):
    def test_legacy_lock_migrates_but_explicit_policy_wins(self):
        for value, address, expected in (
            (None, "", "auto"), ("", "  ", "auto"),
            (None, BSSID, "fixed"), ("invalid", BSSID, "fixed"),
            (" auto ", BSSID, "auto"), ("STRONGEST", BSSID, "strongest"),
            ("fixed", "", "fixed"),
        ):
            with self.subTest(value=value, address=address):
                self.assertEqual(config.normalize_ap_selection(value, address), expected)

    def test_normalization_preserves_input_and_remembered_bssid(self):
        account = {"id": "campus1", "bssid": BSSID, "ap_selection": "strongest", "custom": 7}
        normalized = config.normalize_campus_account(account)
        self.assertIsNot(normalized, account)
        self.assertEqual(account, {"id": "campus1", "bssid": BSSID, "ap_selection": "strongest", "custom": 7})
        self.assertEqual(normalized["bssid"], BSSID)
        self.assertEqual(normalized["ap_selection"], "strongest")
        self.assertEqual(normalized["custom"], 7)

    def test_runtime_only_passes_fixed_bssid_and_keeps_accounts_isolated(self):
        accounts = [
            {"id": "fixed", "bssid": BSSID},
            {"id": "strong", "bssid": BSSID, "ap_selection": "strongest"},
            {"id": "auto", "bssid": BSSID, "ap_selection": "auto"},
            {"id": "wired", "bssid": BSSID, "ap_selection": "fixed", "access_mode": "wired"},
        ]
        cfg = {"campus_accounts": accounts, "hotspot_profiles": []}
        for account_id, policy, binding in (
            ("fixed", "fixed", BSSID), ("strong", "strongest", ""),
            ("auto", "auto", ""), ("wired", "auto", ""), ("fixed", "fixed", BSSID),
        ):
            with self.subTest(account=account_id):
                cfg["active_campus_id"] = account_id
                config.resolve_active_items(cfg)
                self.assertEqual(cfg["campus_ap_selection"], policy)
                self.assertEqual(cfg["campus_bssid"], binding)
        self.assertTrue(all(account["bssid"] == BSSID for account in accounts))
        self.assertEqual(config.normalize_campus_account(accounts[3])["ap_selection"], "")

    def test_persistence_keeps_policy_and_excludes_runtime_projection(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "config.json"
            cfg = dict(config.DEFAULTS, campus_accounts=[{
                "id": "one", "bssid": BSSID, "ap_selection": "auto",
            }], active_campus_id="one")
            config.resolve_active_items(cfg)
            with mock.patch.object(config, "JSON_CONFIG_FILE", str(target)):
                config.save_json_raw_config(cfg)
            saved = json.loads(target.read_text(encoding="utf-8"))
        self.assertEqual(saved["campus_accounts"][0]["ap_selection"], "auto")
        self.assertEqual(saved["campus_accounts"][0]["bssid"], BSSID)
        self.assertNotIn("campus_ap_selection", saved)

    def test_flat_legacy_config_preserves_fixed_or_explicit_auto(self):
        for policy, expected in ((None, "fixed"), ("auto", "auto")):
            migrated = config._migrate_legacy_config({
                "user_id": "student", "campus_bssid": BSSID,
                "campus_ap_selection": policy,
            })
            self.assertEqual(migrated["campus_accounts"][0]["ap_selection"], expected)

    def test_fixed_bssid_must_be_a_complete_unicast_address(self):
        self.assertTrue(config.is_valid_bssid(" 02:AA:bb:CC:dd:EE "))
        for value in (None, "", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff",
                      "01:11:22:33:44:55", "02:11:22:33:44", "02-11-22-33-44-55",
                      "02:11:22:33:44:gg", BSSID + "\nmalformed"):
            with self.subTest(value=value):
                self.assertFalse(config.is_valid_bssid(value))


if __name__ == "__main__":
    unittest.main()
