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

    def _school_metadata(self, descriptors=None, no_suffix_operators=None):
        return {
            "short_name": "runtime-school",
            "no_suffix_operators": list(no_suffix_operators or ["xn"]),
            "school_extra": list(descriptors or []),
        }

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

    def test_load_config_uses_school_metadata_for_no_suffix_operators(self):
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
                    "base_url": "http://172.17.1.2",
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
                return_value=self._school_metadata(no_suffix_operators=["cucc"]),
            ),
            mock.patch(
                "schools.get_profile",
                side_effect=AssertionError("legacy lookup should not run"),
            ),
        ):
            loaded = config.load_config()

        self.assertEqual("alice", loaded["username"])

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
