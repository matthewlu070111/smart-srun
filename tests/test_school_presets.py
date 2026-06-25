import importlib
import json
import os
import sys
import tempfile
import unittest
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")
PRESET_CAPTURE_SCRIPT = os.path.join(
    REPO_ROOT, "scripts", "srun_school_preset_capture.user.js"
)

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import config
import school_presets
from _portal_urls import (
    PORTAL_ACID1_PAGE_URL,
    PORTAL_BARE_ACID1_PAGE_URL,
    PORTAL_BARE_ORIGIN,
    PORTAL_HTTPS_LOGIN_PATH_URL,
    PORTAL_HTTPS_ORIGIN,
    PORTAL_IPV4_ACID4_THEME_URL,
    PORTAL_IPV4_ORIGIN,
    PORTAL_ORIGIN,
    PORTAL_ACID4_THEME_URL,
    PROJECT_DEFAULT_BASE_URL,
)


class SchoolPresetTests(unittest.TestCase):
    def test_normalize_base_url_accepts_portal_page_urls(self):
        cases = {
            PORTAL_ACID1_PAGE_URL: PORTAL_ORIGIN,
            PORTAL_IPV4_ACID4_THEME_URL: PORTAL_IPV4_ORIGIN,
            PORTAL_BARE_ACID1_PAGE_URL: PORTAL_BARE_ORIGIN,
            PORTAL_HTTPS_LOGIN_PATH_URL: PORTAL_HTTPS_ORIGIN,
        }
        for raw, expected in cases.items():
            with self.subTest(raw=raw):
                self.assertEqual(school_presets.normalize_base_url(raw), expected)

    def test_builtin_presets_include_active_schools_but_hide_drafts(self):
        items = school_presets.list_presets()
        school_ids = {item["short_name"] for item in items}

        self.assertIn("jxnu", school_ids)
        self.assertTrue(all(item["status"] == "active" for item in items))

        jxnu = school_presets.get_preset("jxnu")
        self.assertEqual(jxnu["observed_login_shape"]["info_prefix"], "SRBX1")
        self.assertEqual(jxnu["observed_login_shape"]["enc"], "srun_bx1")
        self.assertEqual(jxnu["observed_login_shape"]["os"], "Windows 10")
        self.assertEqual(jxnu["observed_login_shape"]["name"], "Windows")
        operators_by_id = {item["id"]: item for item in jxnu["operators"]}
        self.assertIn("cmcc", operators_by_id)
        self.assertIn("ctcc", operators_by_id)
        self.assertIn("cucc", operators_by_id)
        self.assertIn("", operators_by_id)
        self.assertNotIn("operator", jxnu["defaults"])
        self.assertNotIn("operator_suffix", jxnu["defaults"])
        self.assertNotIn("no_suffix_operators", jxnu)
        for operator in jxnu["operators"]:
            self.assertNotIn("operator_suffix", operator)

    def test_remote_cache_overrides_builtin_presets(self):
        payload = {
            "schema_version": 1,
            "schools": [
                {
                    "id": "remote-campus",
                    "name": "示例大学",
                    "status": "active",
                    "defaults": {"base_url": PORTAL_ORIGIN, "ac_id": "9"},
                    "observed_login_shape": {
                        "n": "128",
                        "type": "3",
                        "enc": "custom_enc",
                        "info_prefix": "{CUSTOM}",
                        "double_stack": "1",
                        "os": "windows",
                        "name": "Windows",
                    },
                }
            ],
        }
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = os.path.join(tmp, "school_presets_cache.json")
            with open(cache_path, "w", encoding="utf-8") as handle:
                json.dump(payload, handle)
            with mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path):
                preset = school_presets.get_preset("remote-campus")

        self.assertEqual(preset["defaults"]["base_url"], PORTAL_ORIGIN)
        self.assertEqual(preset["defaults"]["ac_id"], "9")
        self.assertEqual(
            preset["observed_login_shape"],
            {
                "n": "128",
                "type": "3",
                "enc": "custom_enc",
                "info_prefix": "CUSTOM",
                "double_stack": "1",
                "os": "windows",
                "name": "Windows",
            },
        )

    def test_legacy_verified_preset_cache_is_accepted_but_not_exported(self):
        payload = {
            "schema_version": 1,
            "schools": [
                {
                    "id": "legacy",
                    "name": "旧缓存学校",
                    "verified": True,
                    "operators": [
                        {"id": "cmcc", "label": "中国移动", "verified": True}
                    ],
                    "defaults": {"base_url": PORTAL_ORIGIN},
                }
            ],
        }

        items = school_presets.normalize_payload(payload)

        self.assertEqual(items[0]["status"], "active")
        self.assertNotIn("verified", items[0])
        self.assertNotIn("verified", items[0]["operators"][0])

    def test_legacy_default_operator_is_migrated_to_operators(self):
        payload = {
            "schema_version": 1,
            "schools": [
                {
                    "id": "legacy-operator",
                    "name": "旧运营商字段",
                    "status": "active",
                    "defaults": {
                        "base_url": PORTAL_ORIGIN,
                        "operator": "cmcc",
                        "operator_suffix": "hcmcc",
                    },
                }
            ],
        }

        items = school_presets.normalize_payload(payload)

        self.assertEqual(items[0]["operators"][0]["id"], "hcmcc")
        self.assertNotIn("operator", items[0]["defaults"])
        self.assertNotIn("operator_suffix", items[0]["defaults"])

    def test_legacy_xn_default_operator_is_exported_as_empty_suffix(self):
        payload = {
            "schema_version": 1,
            "schools": [
                {
                    "id": "legacy-xn",
                    "name": "旧 xn 字段",
                    "status": "active",
                    "defaults": {
                        "base_url": PORTAL_ORIGIN,
                        "operator": "xn",
                    },
                }
            ],
        }

        items = school_presets.normalize_payload(payload)

        self.assertEqual(items[0]["operators"][0]["id"], "")
        self.assertNotIn("xn", [item["id"] for item in items[0]["operators"]])

    def test_preset_capture_userscript_exports_school_preset_entry(self):
        with open(PRESET_CAPTURE_SCRIPT, "r", encoding="utf-8") as handle:
            source = handle.read()

        self.assertIn("smart-srun school preset capture", source)
        self.assertIn("buildPresetEntry", source)
        self.assertIn("operator_suffix", source)
        self.assertNotIn("defaults.operator =", source)
        self.assertNotIn("defaults.operator_suffix =", source)
        self.assertNotIn("entry.no_suffix_operators", source)
        self.assertIn("GM_setClipboard", source)
        self.assertIn("buildOperatorsForEntry", source)
        self.assertIn("pushUniqueOperator", source)
        operator_builder = source[
            source.index("function buildOperatorsForEntry"):
            source.index("function operatorLabelFromSuffix")
        ]
        self.assertNotIn("return []", operator_builder)
        self.assertIn("entry.operators = operators", source)
        self.assertIn("setButtonFeedback", source)
        self.assertIn("已重新检查", source)
        self.assertIn("请手动复制", source)
        self.assertIn("makeDraggable", source)
        self.assertIn("toggleMinimized", source)
        self.assertIn("hidePanel", source)
        self.assertIn("renderStep", source)
        self.assertIn("observed_login_shape", source)
        self.assertIn("buildObservedLoginShape", source)
        self.assertIn("info_prefix_supported", source)
        self.assertIn("decoded.enc_ver", source)
        self.assertIn("double_stack", source)
        self.assertIn("paramsFromBody", source)
        self.assertIn("mergeParams(queryParams(url), paramsFromBody(body))", source)
        self.assertIn("state.login_os", source)
        self.assertIn("state.login_name", source)
        self.assertIn("copyCaptureSummary", source)
        self.assertIn("提交信息，协助开发者", source)
        self.assertIn("登录失败，但已捕获请求信息", source)
        self.assertIn('parts.push("os=" + state.login_os)', source)
        self.assertIn('parts.push("name=" + state.login_name)', source)
        self.assertIn("当前脚本只支持解码 {SRBX1}", source)
        self.assertIn("捕获运营商后缀", source)
        self.assertIn("raw username/password/challenge/info are not exported", source)
        self.assertNotIn("operators: DEFAULT_OPERATORS.slice(0)", source)
        self.assertNotIn("[已验证]", source)

    def test_presets_do_not_register_as_school_runtimes(self):
        for name in ["schools", "school_runtime"]:
            if name in sys.modules:
                importlib.reload(sys.modules[name])
        schools = importlib.import_module("schools")

        listed = {item["short_name"]: item for item in schools.list_schools()}
        self.assertIn("jxnu", listed)
        self.assertNotIn("lnut-hld", listed)
        self.assertNotIn("qdu", listed)


class SchoolPresetConfigTests(unittest.TestCase):
    def test_resolve_active_items_ignores_school_preset_defaults(self):
        cfg = {
            "school": "runtime-school",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "20260001",
                    "password": "secret",
                }
            ],
            "hotspot_profiles": [],
        }
        metadata = {
            "short_name": "runtime-school",
            "defaults": {
                "base_url": PORTAL_ACID4_THEME_URL,
                "ac_id": "4",
                "access_mode": "wired",
            },
        }
        with mock.patch("schools.get_school_metadata", return_value=metadata):
            resolved = config.resolve_active_items(cfg)

        self.assertEqual(resolved["base_url"], PROJECT_DEFAULT_BASE_URL)
        self.assertEqual(resolved["ac_id"], "1")
        self.assertEqual(resolved["campus_access_mode"], "wifi")
        self.assertEqual(resolved["username"], "20260001")

    def test_resolve_active_items_still_normalizes_user_supplied_portal_origin(self):
        cfg = {
            "school": "runtime-school",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "u",
                    "operator": "",
                    "password": "p",
                    "base_url": PORTAL_ACID1_PAGE_URL,
                }
            ],
            "hotspot_profiles": [],
        }
        with mock.patch("schools.get_school_metadata", return_value={"short_name": "runtime-school"}):
            resolved = config.resolve_active_items(cfg)

        self.assertEqual(resolved["base_url"], PORTAL_ORIGIN)
        self.assertEqual(resolved["username"], "u")


if __name__ == "__main__":
    unittest.main()
