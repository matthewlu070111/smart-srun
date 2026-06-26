import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock


THIS_DIR = os.path.dirname(os.path.abspath(__file__))
WORKTREE_ROOT = os.path.dirname(THIS_DIR)
MODULE_DIR = os.path.join(WORKTREE_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_DIR not in sys.path:
    sys.path.insert(0, MODULE_DIR)

from _portal_urls import PORTAL_ORIGIN
import config


class SchoolRuntimeConfigTests(unittest.TestCase):
    def setUp(self):
        self.tmp_dir = tempfile.mkdtemp(prefix="school-runtime-config-")
        self.config_path = os.path.join(self.tmp_dir, "config.json")
        self.original_json_config_file = config.JSON_CONFIG_FILE
        config.JSON_CONFIG_FILE = self.config_path

    def tearDown(self):
        config.JSON_CONFIG_FILE = self.original_json_config_file
        shutil.rmtree(self.tmp_dir)

    def _school_metadata(self, descriptors=None):
        return {
            "short_name": "runtime-school",
            "school_extra": list(descriptors or []),
        }

    def test_legacy_config_migration_materializes_implicit_operator_suffix(self):
        migrated = config._migrate_legacy_config(
            {
                "user_id": "alice",
                "operator": "cmcc",
                "password": "pw",
                "base_url": PORTAL_ORIGIN,
                "ac_id": "1",
                "campus_ssid": "jxnu_stu",
            }
        )

        self.assertEqual("cmcc", migrated["campus_accounts"][0]["operator"])
        self.assertEqual("cmcc", migrated["campus_accounts"][0]["operator_suffix"])

    def test_save_and_load_school_extra_contract(self):
        descriptors = [
            {
                "key": "domain",
                "type": "string",
                "default": "campus.example",
                "required": True,
                "label": "Domain",
                "description": "Portal domain",
                "choices": [],
                "secret": False,
            }
        ]
        raw_cfg = {
            "enabled": "1",
            "school": "jxnu",
            "school_extra": {
                "domain": "override.example",
                "ignored": "drop-me",
            },
        }

        normalized = config.normalize_school_extra(raw_cfg, descriptors)
        self.assertEqual({"domain": "override.example"}, normalized)

        raw_cfg["school_extra"] = normalized
        with mock.patch(
            "schools.get_school_metadata",
            return_value=self._school_metadata(descriptors=descriptors),
        ):
            config.save_json_raw_config(raw_cfg)
            persisted = config.load_json_raw_config()

        self.assertEqual({"domain": "override.example"}, persisted.get("school_extra"))
        self.assertEqual(
            {"domain": "override.example"}, config.load_school_extra(persisted)
        )

    def test_invalid_school_extra_payload_collapses_and_reports_errors(self):
        descriptors = [
            {
                "key": "domain",
                "type": "string",
                "default": "",
                "required": True,
                "label": "Domain",
                "description": "Portal domain",
                "choices": [],
                "secret": False,
            },
            {
                "key": "operator_mode",
                "type": "string",
                "default": "auto",
                "required": False,
                "label": "Operator mode",
                "description": "How operator suffix is resolved",
                "choices": ["auto", "manual"],
                "secret": False,
            },
        ]
        raw_cfg = {
            "school_extra": {
                "domain": "   ",
                "operator_mode": "broken",
                "unexpected": "value",
            }
        }

        ok, errors = config.validate_school_extra(raw_cfg, descriptors)

        self.assertFalse(ok)
        self.assertEqual(
            [
                {"key": "domain", "message": "Domain is required."},
                {
                    "key": "operator_mode",
                    "message": "Operator mode must be one of: auto, manual.",
                },
            ],
            errors,
        )
        self.assertEqual({}, config.load_school_extra({"school_extra": ["bad"]}))
        self.assertEqual({}, config.normalize_school_extra(raw_cfg, descriptors))

    def test_school_extra_falsey_json_values_are_preserved(self):
        descriptors = [
            {
                "key": "retry_count",
                "type": "int",
                "default": 3,
                "required": True,
                "label": "Retry count",
                "description": "Retry count",
                "choices": [],
                "secret": False,
            },
            {
                "key": "timeout_ratio",
                "type": "float",
                "default": 1.0,
                "required": True,
                "label": "Timeout ratio",
                "description": "Timeout ratio",
                "choices": [],
                "secret": False,
            },
            {
                "key": "strict_mode",
                "type": "bool",
                "default": True,
                "required": True,
                "label": "Strict mode",
                "description": "Strict mode",
                "choices": [],
                "secret": False,
            },
        ]
        raw_cfg = {
            "school_extra": {
                "retry_count": 0,
                "timeout_ratio": 0.0,
                "strict_mode": False,
            }
        }

        ok, errors = config.validate_school_extra(raw_cfg, descriptors)

        self.assertTrue(ok)
        self.assertEqual([], errors)
        self.assertEqual(
            {"retry_count": "0", "timeout_ratio": "0.0", "strict_mode": "0"},
            config.normalize_school_extra(raw_cfg, descriptors),
        )

    def test_main_config_helpers_normalize_dirty_school_extra_with_descriptors(self):
        descriptors = [
            {
                "key": "strict_mode",
                "type": "bool",
                "default": True,
                "required": False,
                "label": "Strict mode",
                "description": "Strict mode",
                "choices": [],
                "secret": False,
            },
            {
                "key": "retry_count",
                "type": "int",
                "default": 3,
                "required": False,
                "label": "Retry count",
                "description": "Retry count",
                "choices": [],
                "secret": False,
            },
        ]
        raw_cfg = {
            "school": "runtime-school",
            "school_extra": {
                "strict_mode": False,
                "retry_count": 0,
                "unknown": "drop-me",
            },
        }

        with mock.patch(
            "schools.get_school_metadata",
            return_value=self._school_metadata(descriptors=descriptors),
        ):
            config.save_json_raw_config(raw_cfg)
            persisted = config.load_json_raw_config()
            loaded = config.load_config()

        self.assertEqual(
            {"strict_mode": "0", "retry_count": "0"},
            persisted.get("school_extra"),
        )
        self.assertEqual(
            {"strict_mode": "0", "retry_count": "0"},
            loaded.get("school_extra"),
        )

    def test_main_config_helpers_collapse_invalid_school_extra_payload(self):
        descriptors = [
            {
                "key": "strict_mode",
                "type": "bool",
                "default": True,
                "required": False,
                "label": "Strict mode",
                "description": "Strict mode",
                "choices": [],
                "secret": False,
            }
        ]
        raw_cfg = {
            "school": "runtime-school",
            "school_extra": {
                "strict_mode": "definitely",
                "unknown": "drop-me",
            },
        }

        with mock.patch(
            "schools.get_school_metadata",
            return_value=self._school_metadata(descriptors=descriptors),
        ):
            config.save_json_raw_config(raw_cfg)
            persisted = config.load_json_raw_config()
            loaded = config.load_config()

        self.assertEqual({}, persisted.get("school_extra"))
        self.assertEqual({}, loaded.get("school_extra"))

    def test_blank_operator_suffix_keeps_plain_username(self):
        raw_cfg = {
            "school": "runtime-school",
            "active_campus_id": "campus-1",
            "default_campus_id": "campus-1",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "alice",
                    "operator": "cucc",
                    "password": "pw",
                    "base_url": PORTAL_ORIGIN,
                    "ac_id": "1",
                    "access_mode": "wifi",
                    "ssid": "jxnu_stu",
                    "encryption": "none",
                }
            ],
            "hotspot_profiles": [],
        }

        with (
            mock.patch.object(config, "load_json_raw_config", return_value=raw_cfg),
            mock.patch(
                "schools.get_school_metadata",
                return_value=self._school_metadata(),
            ),
            mock.patch(
                "schools.get_profile",
                side_effect=AssertionError("legacy lookup should not run"),
            ),
        ):
            loaded = config.load_config()

        self.assertEqual("alice", loaded["username"])

    def test_explicit_operator_suffix_is_appended_to_username(self):
        raw_cfg = {
            "school": "runtime-school",
            "active_campus_id": "campus-1",
            "default_campus_id": "campus-1",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "alice",
                    "operator": "hcmcc",
                    "operator_suffix": "hcmcc",
                    "password": "pw",
                    "base_url": PORTAL_ORIGIN,
                    "ac_id": "1",
                    "access_mode": "wifi",
                    "ssid": "jxnu_stu",
                    "encryption": "none",
                }
            ],
            "hotspot_profiles": [],
        }

        with (
            mock.patch.object(config, "load_json_raw_config", return_value=raw_cfg),
            mock.patch(
                "schools.get_school_metadata",
                return_value=self._school_metadata(),
            ),
        ):
            loaded = config.load_config()

        self.assertEqual("alice@hcmcc", loaded["username"])
        self.assertEqual("hcmcc", loaded["operator"])

    def test_custom_operator_id_is_preserved_but_blank_suffix_stays_plain(self):
        raw_cfg = {
            "school": "runtime-school",
            "active_campus_id": "campus-1",
            "default_campus_id": "campus-1",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "alice",
                    "operator": "hcmcc",
                    "operator_suffix": "",
                    "password": "pw",
                    "base_url": PORTAL_ORIGIN,
                    "ac_id": "1",
                }
            ],
            "hotspot_profiles": [],
        }

        with (
            mock.patch.object(config, "load_json_raw_config", return_value=raw_cfg),
            mock.patch(
                "schools.get_school_metadata",
                return_value=self._school_metadata(),
            ),
        ):
            loaded = config.load_config()

        self.assertEqual("hcmcc", loaded["operator"])
        self.assertEqual("", loaded["operator_suffix"])
        self.assertEqual("alice", loaded["username"])
        self.assertEqual("alice", loaded["campus_account_label"])

    def test_account_login_shape_overrides_legacy_global_values(self):
        raw_cfg = {
            "school": "runtime-school",
            "n": "199",
            "type": "2",
            "enc": "global_enc",
            "active_campus_id": "campus-1",
            "default_campus_id": "campus-1",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "alice",
                    "operator": "cucc",
                    "password": "pw",
                    "base_url": PORTAL_ORIGIN,
                    "ac_id": "1",
                    "n": "128",
                    "type": "3",
                    "enc": "custom_enc",
                    "info_prefix": "{CUSTOM}",
                    "double_stack": "1",
                    "login_os": "windows",
                    "login_name": "Windows",
                }
            ],
            "hotspot_profiles": [],
        }

        with (
            mock.patch.object(config, "load_json_raw_config", return_value=raw_cfg),
            mock.patch(
                "schools.get_school_metadata",
                return_value=self._school_metadata(),
            ),
        ):
            loaded = config.load_config()

        self.assertEqual("128", loaded["n"])
        self.assertEqual("3", loaded["type"])
        self.assertEqual("custom_enc", loaded["enc"])
        self.assertEqual("CUSTOM", loaded["info_prefix"])
        self.assertEqual("1", loaded["double_stack"])
        self.assertEqual("windows", loaded["login_os"])
        self.assertEqual("Windows", loaded["login_name"])

    def test_invalid_login_shape_numbers_fall_back_to_safe_defaults(self):
        raw_cfg = {
            "school": "runtime-school",
            "n": "bad",
            "type": "-1",
            "enc": "",
            "active_campus_id": "campus-1",
            "default_campus_id": "campus-1",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "alice",
                    "operator": "cucc",
                    "password": "pw",
                    "base_url": PORTAL_ORIGIN,
                    "ac_id": "1",
                    "n": "oops",
                    "type": "nope",
                }
            ],
            "hotspot_profiles": [],
        }

        with (
            mock.patch.object(config, "load_json_raw_config", return_value=raw_cfg),
            mock.patch(
                "schools.get_school_metadata",
                return_value=self._school_metadata(),
            ),
        ):
            loaded = config.load_config()

        self.assertEqual("200", loaded["n"])
        self.assertEqual("1", loaded["type"])
        self.assertEqual("srun_bx1", loaded["enc"])
        self.assertEqual("SRBX1", loaded["info_prefix"])
        self.assertEqual("0", loaded["double_stack"])
        self.assertEqual("Windows 10", loaded["login_os"])
        self.assertEqual("Windows", loaded["login_name"])

    def test_school_metadata_lookup_error_falls_back_to_minimal_metadata(self):
        with mock.patch(
            "schools.get_school_metadata",
            side_effect=LookupError("missing runtime-school"),
        ):
            metadata = config._get_school_metadata({"school": "runtime-school"})

        self.assertEqual(
            {"short_name": "runtime-school"},
            metadata,
        )

    def test_school_metadata_does_not_swallow_unexpected_runtime_errors(self):
        with mock.patch(
            "schools.get_school_metadata",
            side_effect=RuntimeError("unexpected metadata failure"),
        ):
            with self.assertRaisesRegex(RuntimeError, "unexpected metadata failure"):
                config._get_school_metadata({"school": "runtime-school"})

    def test_luci_contract_normalizes_bool_descriptor_defaults(self):
        contract = config.build_school_runtime_luci_contract(
            {"school": "jxnu", config.SCHOOL_EXTRA_KEY: {}},
            {
                "runtime_type": "runtime_class",
                "runtime_api_version": 1,
                "field_descriptors": [
                    {
                        "key": "auto_bind",
                        "type": "bool",
                        "label": "Auto Bind",
                        "default": True,
                    }
                ],
                "school_extra": [],
            },
        )

        self.assertEqual("1", contract["field_descriptors"][0]["default"])

    def test_luci_contract_keeps_int_descriptor_default_empty_when_missing(self):
        contract = config.build_school_runtime_luci_contract(
            {"school": "jxnu", config.SCHOOL_EXTRA_KEY: {}},
            {
                "runtime_type": "runtime_class",
                "runtime_api_version": 1,
                "field_descriptors": [
                    {
                        "key": "retry_limit",
                        "type": "int",
                        "label": "Retry Limit",
                    }
                ],
                "school_extra": [],
            },
        )

        self.assertEqual("", contract["field_descriptors"][0]["default"])


if __name__ == "__main__":
    unittest.main()
