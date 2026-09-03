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


# Runtime modules use bare imports after the local source path is installed.
import config  # noqa: E402
import school_presets  # noqa: E402
from _portal_urls import (  # noqa: E402
    PORTAL_ACID1_PAGE_URL,
    PORTAL_BARE_ACID1_PAGE_URL,
    PORTAL_BARE_ORIGIN,
    PORTAL_HTTPS_LOGIN_PATH_URL,
    PORTAL_HTTPS_ORIGIN,
    PORTAL_IPV4_ACID4_THEME_URL,
    PORTAL_IPV4_ORIGIN,
    PORTAL_ORIGIN,
    PORTAL_ACID4_THEME_URL,
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
        self.assertIn("swpu", school_ids)
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

        swpu = school_presets.get_preset("swpu")
        self.assertEqual(swpu["defaults"]["base_url"], "http://172.16.245.50")
        self.assertEqual(swpu["defaults"]["ac_id"], "1")
        self.assertEqual(swpu["defaults"]["access_mode"], "wired")
        self.assertEqual(
            swpu["operators"],
            [
                {"suffix": "dxwx", "label": "电信"},
                {"suffix": "stu", "label": "学生"},
                {"suffix": "tch", "label": "教师"},
                {"suffix": "yd", "label": "移动无线"},
                {"suffix": "ydyx", "label": "移动有线"},
            ],
        )
        self.assertEqual(swpu["observed_login_shape"]["info_prefix"], "SRBX1")

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

    def test_refresh_remote_presets_uses_each_source_in_priority_order(self):
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
        self.assertEqual(
            school_presets.REMOTE_PRESETS_URLS,
            (
                "https://srun.guiguisocute.com/school-presets.json",
                "https://smart-srun--cloudflare-pages.pages.dev/school-presets.json",
                "https://raw.githubusercontent.com/matthewlu070111/smart-srun/main/doc/school-presets.json",
                "https://srun.edu-publish.site/school-presets.json",
            ),
        )
        for index, chosen in enumerate(school_presets.REMOTE_PRESETS_URLS):
            calls = []

            def fake_fetch(url, timeout):
                calls.append(url)
                if url != chosen:
                    raise RuntimeError("source unavailable")
                return json.dumps(payload)

            with self.subTest(source=chosen), tempfile.TemporaryDirectory() as tmp:
                cache_path = os.path.join(tmp, "school_presets_cache.json")
                with (
                    mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path),
                    mock.patch.object(school_presets, "_fetch_via_urllib", side_effect=fake_fetch),
                    mock.patch.object(school_presets, "_fetch_via_system_client",
                                      side_effect=RuntimeError("no system fetcher")),
                ):
                    result = school_presets.refresh_remote_presets()
                    with open(cache_path, "r", encoding="utf-8") as handle:
                        cached = json.load(handle)
                self.assertEqual(calls, list(school_presets.REMOTE_PRESETS_URLS[:index + 1]))
                self.assertEqual(result["source_url"], chosen)
                self.assertEqual(cached["_source_url"], chosen)
                self.assertIn("mirror-fallback", {item["short_name"] for item in result["schools"]})

    def test_invalid_source_responses_continue_to_the_next_source(self):
        valid = {"schema_version": 1, "schools": []}
        invalid = (
            "<html>not a preset</html>", "[]", "{}",
            '{"schema_version":1,"updated_at":"2026-09-04"}',
            '{"schema_version":2,"schools":[]}',
            '{"schema_version":1,"schools":{}}',
        )
        for body in invalid:
            with self.subTest(body=body), mock.patch.object(
                school_presets, "_fetch_via_urllib",
                side_effect=[body, json.dumps(valid)],
            ) as fetcher:
                payload, source = school_presets._fetch_remote_payload_with_source()
            self.assertEqual(payload, valid)
            self.assertEqual(source, school_presets.PAGES_PRESETS_URL)
            self.assertEqual(fetcher.call_count, 2)

    def test_empty_school_list_is_a_valid_remote_payload(self):
        payload = {"schema_version": 1, "updated_at": "2026-09-04", "schools": []}
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = os.path.join(tmp, "cache.json")
            with (
                mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path),
                mock.patch.object(school_presets, "_fetch_via_urllib",
                                  return_value=json.dumps(payload)) as fetcher,
            ):
                result = school_presets.refresh_remote_presets()
                with open(cache_path, encoding="utf-8") as handle:
                    cached = json.load(handle)
        self.assertTrue(result["ok"])
        self.assertEqual(result["source_url"], school_presets.MIRROR_PRESETS_URL)
        self.assertEqual(fetcher.call_count, 1)
        self.assertEqual(cached["schools"], [])

    def test_missing_school_list_preserves_cache_and_deprecated_status(self):
        bundled = {
            "schema_version": 1,
            "schools": [{"id": "retired-campus", "status": "active"}],
        }
        cached = {
            "schema_version": 1,
            "updated_at": "2026-09-03",
            "schools": [
                {"id": "retired-campus", "status": "deprecated"},
                {"id": "cached-campus", "status": "active"},
            ],
        }
        invalid = {"schema_version": 1, "updated_at": "2026-09-04"}
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = os.path.join(tmp, "cache.json")
            bundled_path = os.path.join(tmp, "bundled.json")
            original_bytes = json.dumps(cached).encode("utf-8")
            with open(cache_path, "wb") as handle:
                handle.write(original_bytes)
            with open(bundled_path, "w", encoding="utf-8") as handle:
                json.dump(bundled, handle)
            with (
                mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path),
                mock.patch.object(school_presets, "FALLBACK_PRESETS_FILE", bundled_path),
                mock.patch.object(school_presets, "_fetch_via_urllib",
                                  return_value=json.dumps(invalid)) as fetcher,
            ):
                with self.assertRaisesRegex(RuntimeError, "invalid school preset schema"):
                    school_presets.refresh_remote_presets()
                self.assertEqual(fetcher.call_count, len(school_presets.REMOTE_PRESETS_URLS))
                visible = school_presets.list_presets(refresh=True)
                all_schools = school_presets.list_presets(include_draft=True)
            with open(cache_path, "rb") as handle:
                self.assertEqual(handle.read(), original_bytes)
        self.assertEqual([school["short_name"] for school in visible], ["cached-campus"])
        self.assertEqual(all_schools[0]["status"], "deprecated")

    def test_explicit_source_does_not_silently_fetch_a_different_source(self):
        with (
            mock.patch.object(school_presets, "_fetch_via_urllib",
                              side_effect=RuntimeError("unavailable")) as fetcher,
            mock.patch.object(school_presets, "_fetch_via_system_client",
                              side_effect=RuntimeError("unavailable")),
        ):
            with self.assertRaises(RuntimeError):
                school_presets.fetch_remote_payload(url=PORTAL_ORIGIN + "/presets.json")
        self.assertEqual(fetcher.call_count, 1)

    def test_malformed_cached_school_list_does_not_crash_normalization(self):
        for value in (42, "schools", {"campus": {}}):
            with self.subTest(value=value):
                self.assertEqual(
                    school_presets.normalize_payload({"schema_version": 1, "schools": value}),
                    [],
                )

    def test_all_sources_unavailable_preserves_cache_and_bundled_schools(self):
        payload = {
            "schema_version": 1,
            "schools": [{"id": "cached-campus", "status": "active"}],
        }
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = os.path.join(tmp, "presets.json")
            with open(cache_path, "w", encoding="utf-8") as handle:
                json.dump(payload, handle)
            with (
                mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path),
                mock.patch.object(school_presets, "_fetch_via_urllib",
                                  side_effect=RuntimeError("offline")),
                mock.patch.object(school_presets, "_fetch_via_system_client",
                                  side_effect=RuntimeError("offline")),
            ):
                schools = school_presets.list_presets(refresh=True)
                with open(cache_path, "r", encoding="utf-8") as handle:
                    self.assertEqual(json.load(handle), payload)
                os.unlink(cache_path)
                bundled = school_presets.list_presets(refresh=True)
        self.assertIn("cached-campus", {item["short_name"] for item in schools})
        self.assertIn("jxnu", {item["short_name"] for item in schools})
        self.assertIn("jxnu", {item["short_name"] for item in bundled})

    def test_same_day_catalogue_and_source_changes_are_saved(self):
        cached = {
            "schema_version": 1, "updated_at": "2026-09-03",
            "_source_url": school_presets.LEGACY_PRESETS_URL,
            "schools": [{"id": "existing", "status": "active"}],
        }
        remote = {
            "schema_version": 1, "updated_at": "2026-09-03",
            "schools": cached["schools"] + [{"id": "new-campus", "status": "active"}],
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "cache.json")
            with open(path, "w", encoding="utf-8") as handle:
                json.dump(cached, handle)
            with (
                mock.patch.object(school_presets, "CACHE_PRESETS_FILE", path),
                mock.patch.object(school_presets, "_fetch_remote_payload_with_source",
                                  return_value=(remote, school_presets.MIRROR_PRESETS_URL)),
            ):
                result = school_presets.refresh_remote_presets()
                with open(path, "r", encoding="utf-8") as handle:
                    persisted = json.load(handle)
        self.assertEqual(result["source_url"], school_presets.MIRROR_PRESETS_URL)
        self.assertEqual(persisted["_source_url"], school_presets.MIRROR_PRESETS_URL)
        self.assertIn("new-campus", {item["id"] for item in persisted["schools"]})

    def test_refresh_remote_presets_keeps_newer_cached_payload(self):
        cached_payload = {
            "schema_version": 1,
            "updated_at": "2026-06-30",
            "_cached_at": 123,
            "_source_url": "cached",
            "schools": [
                {"id": "cached-campus", "name": "缓存学校", "status": "active"}
            ],
        }
        remote_payload = {
            "schema_version": 1,
            "updated_at": "2026-06-01",
            "schools": [
                {"id": "old-campus", "name": "旧远端学校", "status": "active"}
            ],
        }

        with tempfile.TemporaryDirectory() as tmp:
            cache_path = os.path.join(tmp, "school_presets_cache.json")
            with open(cache_path, "w", encoding="utf-8") as handle:
                json.dump(cached_payload, handle)
            with (
                mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path),
                mock.patch.object(
                    school_presets,
                    "_fetch_remote_payload_with_source",
                    return_value=(remote_payload, "remote"),
                ),
            ):
                result = school_presets.refresh_remote_presets()
                listed = school_presets.list_presets(refresh=True)
                with open(cache_path, "r", encoding="utf-8") as handle:
                    persisted = json.load(handle)

        self.assertIn("cached-campus", {item["short_name"] for item in result["schools"]})
        self.assertEqual(persisted["updated_at"], "2026-06-30")
        self.assertIn("cached-campus", {item["short_name"] for item in listed})
        self.assertNotIn("old-campus", {item["short_name"] for item in listed})

    def test_refresh_matches_public_list_and_preserves_drafts_in_cache(self):
        catalogue = {
            "schema_version": 1,
            "updated_at": "2026-09-03",
            "schools": [
                {"id": "remote-active", "status": "active"},
                {"id": "remote-draft", "status": "draft"},
                {"id": "jxnu", "status": "deprecated"},
            ],
        }
        for remote_date in ("2026-09-03", "2026-09-02"):
            with self.subTest(remote_date=remote_date), tempfile.TemporaryDirectory() as tmp:
                path = os.path.join(tmp, "cache.json")
                with open(path, "w", encoding="utf-8") as handle:
                    json.dump(catalogue, handle)
                remote = dict(catalogue, updated_at=remote_date)
                with (
                    mock.patch.object(school_presets, "CACHE_PRESETS_FILE", path),
                    mock.patch.object(school_presets, "_fetch_remote_payload_with_source",
                                      return_value=(remote, school_presets.MIRROR_PRESETS_URL)),
                ):
                    result = school_presets.refresh_remote_presets()
                    self.assertEqual(result["schools"], school_presets.list_presets())
                    public_ids = {item["short_name"] for item in result["schools"]}
                    all_ids = {item["short_name"] for item in school_presets.list_presets(include_draft=True)}
                with open(path, "r", encoding="utf-8") as handle:
                    persisted = json.load(handle)
                self.assertIn("remote-active", public_ids)
                self.assertIn("swpu", public_ids)  # Preserve bundled-only schools.
                self.assertNotIn("remote-draft", public_ids)
                self.assertNotIn("jxnu", public_ids)  # A remote demotion overrides bundled active.
                self.assertIn("remote-draft", all_ids)
                self.assertEqual(persisted["schools"], catalogue["schools"])

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
        self.assertIn("default", listed)
        self.assertNotIn("jxnu", listed)
        self.assertNotIn("lnut-hld", listed)
        self.assertNotIn("qdu", listed)


class SchoolPresetConfigTests(unittest.TestCase):
    def test_resolve_active_items_ignores_school_preset_defaults(self):
        cfg = {
            "school": "runtime-school",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "student-a",
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

        # 预设 defaults 不落入解析结果；账号缺 base_url 时不再回落到 jxnu 网关，
        # 而是保持为空，交给用户显式填写。
        self.assertEqual(resolved["base_url"], "")
        self.assertEqual(resolved["ac_id"], "1")
        self.assertEqual(resolved["campus_access_mode"], "wifi")
        self.assertEqual(resolved["username"], "student-a")

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
