import importlib
import json
import os
import sys
import tempfile
import unittest
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")
DOC_PRESETS_FILE = os.path.join(REPO_ROOT, "doc", "school-presets.json")
FALLBACK_PRESETS_FILE = os.path.join(
    MODULE_ROOT, "school_presets_fallback.json"
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
        operators_by_suffix = {item["suffix"]: item for item in jxnu["operators"]}
        self.assertIn("cmcc", operators_by_suffix)
        self.assertIn("ctcc", operators_by_suffix)
        self.assertIn("cucc", operators_by_suffix)
        self.assertIn("", operators_by_suffix)
        self.assertNotIn("operator", jxnu["defaults"])
        self.assertNotIn("operator_suffix", jxnu["defaults"])
        self.assertNotIn("no_suffix_operators", jxnu)
        for operator in jxnu["operators"]:
            self.assertNotIn("operator_suffix", operator)

    def test_bundled_fallback_is_synced_with_doc_presets(self):
        with open(DOC_PRESETS_FILE, "r", encoding="utf-8") as handle:
            doc_payload = json.load(handle)
        with open(FALLBACK_PRESETS_FILE, "r", encoding="utf-8") as handle:
            fallback_payload = json.load(handle)

        self.assertEqual(fallback_payload.get("source"), "bundled fallback")
        fallback_payload["source"] = doc_payload.get("source")
        self.assertEqual(fallback_payload, doc_payload)

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

    def test_refresh_remote_presets_prefers_mirror_and_falls_back_to_github(self):
        payload = {
            "schema_version": 1,
            "schools": [
                {
                    "id": "mirror-fallback",
                    "name": "镜像回退学校",
                    "status": "active",
                    "defaults": {"base_url": PORTAL_ORIGIN},
                }
            ],
        }
        calls = []

        def fake_fetch(url, timeout):
            calls.append(url)
            if url == school_presets.MIRROR_PRESETS_URL:
                raise RuntimeError("mirror unavailable")
            return json.dumps(payload)

        with tempfile.TemporaryDirectory() as tmp:
            cache_path = os.path.join(tmp, "school_presets_cache.json")
            with (
                mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path),
                mock.patch.object(
                    school_presets, "_fetch_via_urllib", side_effect=fake_fetch
                ),
                mock.patch.object(
                    school_presets,
                    "_fetch_via_system_client",
                    side_effect=RuntimeError("no system fetcher"),
                ),
            ):
                result = school_presets.refresh_remote_presets()
                cached = json.load(open(cache_path, "r", encoding="utf-8"))

        self.assertEqual(
            calls,
            [
                school_presets.MIRROR_PRESETS_URL,
                school_presets.GITHUB_PRESETS_URL,
            ],
        )
        self.assertEqual(result["source_url"], school_presets.GITHUB_PRESETS_URL)
        self.assertEqual(cached["_source_url"], school_presets.GITHUB_PRESETS_URL)
        self.assertEqual(result["schools"][0]["short_name"], "mirror-fallback")

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

        self.assertEqual(items[0]["operators"][0]["suffix"], "hcmcc")
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

        self.assertEqual(items[0]["operators"][0]["suffix"], "")
        self.assertNotIn("xn", [item["suffix"] for item in items[0]["operators"]])

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
