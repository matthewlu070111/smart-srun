"""Published catalogue data, verification status and fallback synchronization."""

import importlib.util
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest
from urllib.parse import unquote, urlsplit


REPO_ROOT = Path(__file__).resolve().parents[1]
MODULE_ROOT = REPO_ROOT / "root/usr/lib/smart_srun"
if str(MODULE_ROOT) not in sys.path:
    sys.path.insert(0, str(MODULE_ROOT))

import school_presets  # noqa: E402

SPEC = importlib.util.spec_from_file_location(
    "sync_school_presets", REPO_ROOT / "scripts/sync_school_presets.py"
)
syncer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(syncer)


class PresetCatalogueTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.payload = json.loads((REPO_ROOT / syncer.DOC_PATH).read_text(encoding="utf-8"))
        cls.schools = {school["id"]: school for school in cls.payload["schools"]}

    def test_published_pair_has_identical_content_except_source(self):
        self.assertEqual(syncer.sync_catalogue(REPO_ROOT), [])
        fallback = json.loads((REPO_ROOT / syncer.FALLBACK_PATH).read_text(encoding="utf-8"))
        self.assertEqual(fallback["source"], "bundled fallback")
        fallback["source"] = self.payload["source"]
        self.assertEqual(fallback, self.payload)

    def test_links_to_repository_docs_have_existing_targets(self):
        prefix = "/matthewlu070111/smart-srun/blob/main/doc/"
        for school in self.payload["schools"]:
            url = urlsplit(school.get("doc_url", ""))
            if url.netloc == "github.com" and url.path.startswith(prefix):
                with self.subTest(school=school["id"]):
                    target = REPO_ROOT / "doc" / unquote(url.path[len(prefix):])
                    self.assertTrue(target.is_file(), school["doc_url"])

    def test_catalogue_ids_and_present_values_are_valid_without_filling_unknowns(self):
        self.assertEqual(len(self.schools), len(self.payload["schools"]), "school IDs must be unique")
        for school in self.payload["schools"]:
            with self.subTest(school=school["id"]):
                self.assertRegex(school["id"], r"^[a-z0-9][a-z0-9_-]*$")
                self.assertTrue(school["name"].strip())
                self.assertIn(school["status"], ("active", "draft"))
                defaults = school.get("defaults", {})
                self.assertFalse({"operator", "operator_suffix", "no_suffix_operators"} & set(defaults))
                self.assertNotIn("no_suffix_operators", school)
                if "base_url" in defaults:
                    self.assertTrue(re.match(r"^https?://[^\s]+$", defaults["base_url"]))
                if "ac_id" in defaults:
                    self.assertIsInstance(defaults["ac_id"], str)
                    self.assertTrue(defaults["ac_id"].isdigit())
                if "access_mode" in defaults:
                    self.assertIn(defaults["access_mode"], ("wired", "wifi"))
                for operator in school.get("operators", []):
                    self.assertIsInstance(operator.get("suffix"), str)
                    self.assertTrue(operator.get("label", "").strip())
                    self.assertNotIn("id", operator)
                for value in school.get("observed_login_shape", {}).values():
                    self.assertIsInstance(value, str)

    def test_issue_28_keeps_submitted_status_and_does_not_guess_access(self):
        school = self.schools["bucm"]
        self.assertEqual(school["name"], "北京中医药大学")
        self.assertEqual(school["status"], "active")
        self.assertEqual(school["defaults"], {"base_url": "http://10.2.20.20", "ac_id": "9"})
        self.assertEqual(school["operators"], [{"suffix": "", "label": "校园网"}])
        self.assertEqual(school["contributors"], ["@1036598718"])
        self.assertEqual(school["source_issue"], "https://github.com/matthewlu070111/smart-srun/issues/28")
        self.assertIn("未说明接入方式", school["description"])

    def test_issue_29_capture_is_draft_until_standalone_auth_is_verified(self):
        school = self.schools["zjut"]
        self.assertEqual(school["name"], "浙江工业大学")
        self.assertEqual(school["status"], "draft")
        self.assertEqual(school["defaults"], {
            "base_url": "http://192.168.210.171", "ac_id": "3", "access_mode": "wired",
        })
        self.assertEqual(school["operators"], [{"suffix": "", "label": "校园网"}])
        self.assertEqual(school["contributors"], ["@RCaquaer"])
        self.assertEqual(school["source_issue"], "https://github.com/matthewlu070111/smart-srun/issues/29")
        self.assertIn("插件独立认证尚未验证", school["description"])
        visible = {item["short_name"] for item in school_presets.normalize_payload(self.payload)}
        with_drafts = {item["short_name"] for item in school_presets.normalize_payload(self.payload, include_draft=True)}
        self.assertIn("bucm", visible)
        self.assertNotIn("zjut", visible)
        self.assertIn("zjut", with_drafts)

    def test_issue_capture_login_shapes_are_preserved(self):
        shape = {"n": "200", "type": "1", "enc": "srun_bx1", "info_prefix": "SRBX1",
                 "double_stack": "0", "os": "Windows 10", "name": "Windows"}
        for school_id in ("bucm", "zjut"):
            with self.subTest(school=school_id):
                self.assertEqual(self.schools[school_id]["observed_login_shape"], shape)


class PresetSynchronizationTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.main = self.root / syncer.DOC_PATH
        self.fallback = self.root / syncer.FALLBACK_PATH
        self.main.parent.mkdir(parents=True)
        self.fallback.parent.mkdir(parents=True)

    def test_check_is_read_only_and_accepts_different_formatting_and_key_order(self):
        main = b'{"schools":[],"source":"fixture","schema_version":1}'
        fallback = b'{\r\n "schema_version": 1, "source": "bundled fallback", "schools": []\r\n}'
        self.main.write_bytes(main)
        self.fallback.write_bytes(fallback)
        self.assertEqual(syncer.sync_catalogue(self.root), [])
        self.assertEqual(self.main.read_bytes(), main)
        self.assertEqual(self.fallback.read_bytes(), fallback)

    def test_write_preserves_main_bytes_and_changes_only_top_level_source(self):
        compact = ('{"schools":[{"source":"fixture","id":"second","status":"draft",'
                   '"defaults":{"ac_id":"9"},"unknown":[2,1]},{"id":"first"}],'
                   '"sour\\u0063e" : "fixture","extra":{"source":"fixture","label":"未确认"}}')
        for before in (compact, " \r\n" + compact + "\r\n", compact.replace(",", ",\n  ") + "\n"):
            with self.subTest(text=before):
                raw = before.encode("utf-8")
                expected = before.replace('"sour\\u0063e" : "fixture"',
                                          '"sour\\u0063e" : "bundled fallback"').encode("utf-8")
                self.main.write_bytes(raw)
                self.fallback.write_bytes(b"invalid old fallback")
                self.assertEqual(syncer.sync_catalogue(self.root), [syncer.FALLBACK_PATH.as_posix()])
                self.assertEqual(self.fallback.read_bytes(), b"invalid old fallback")
                self.assertEqual(syncer.sync_catalogue(self.root, write=True), [syncer.FALLBACK_PATH.as_posix()])
                self.assertEqual(self.main.read_bytes(), raw)
                self.assertEqual(self.fallback.read_bytes(), expected)
                self.assertEqual(syncer.sync_catalogue(self.root), [])
                self.assertEqual(syncer.sync_catalogue(self.root, write=True), [])

    def test_check_does_not_create_missing_fallback(self):
        self.main.write_bytes(b'{"source":"fixture","schools":[]}')
        self.assertEqual(syncer.sync_catalogue(self.root), [syncer.FALLBACK_PATH.as_posix()])
        self.assertFalse(self.fallback.exists())
        self.assertEqual(syncer.sync_catalogue(self.root, write=True), [syncer.FALLBACK_PATH.as_posix()])
        self.assertEqual(json.loads(self.fallback.read_bytes()), {"source": "bundled fallback", "schools": []})

    def test_check_detects_different_school_order_and_wrong_fallback_source(self):
        self.main.write_bytes(b'{"source":"fixture","schools":[{"id":"a"},{"id":"b"}]}')
        for fallback in (
            {"source": "bundled fallback", "schools": [{"id": "b"}, {"id": "a"}]},
            {"source": "fixture", "schools": [{"id": "a"}, {"id": "b"}]},
        ):
            with self.subTest(fallback=fallback):
                raw = json.dumps(fallback).encode("utf-8")
                self.fallback.write_bytes(raw)
                self.assertEqual(syncer.sync_catalogue(self.root), [syncer.FALLBACK_PATH.as_posix()])
                self.assertEqual(self.fallback.read_bytes(), raw)

    def test_invalid_main_or_ambiguous_source_does_not_write_either_file(self):
        for raw in (b'{"source":', b'[]', b'{"schools":[]}', b'{"source":3}',
                    b'{"source":"first","source":"last","schools":[]}'):
            with self.subTest(main=raw):
                self.main.write_bytes(raw)
                self.fallback.write_bytes(b"untouched")
                with self.assertRaises(ValueError):
                    syncer.sync_catalogue(self.root, write=True)
                self.assertEqual(self.main.read_bytes(), raw)
                self.assertEqual(self.fallback.read_bytes(), b"untouched")

    def test_cache_serializer_preserves_input_order_and_unknown_data(self):
        payload = {"z_extra": {"z": 1, "a": 2}, "source": "fixture", "schools": [
            {"status": "draft", "id": "example", "defaults": {"ac_id": "9"}},
        ], "schema_version": 1}
        restored = json.loads(school_presets.format_preset_payload(payload))
        self.assertEqual(restored, payload)
        self.assertEqual(list(restored), list(payload))
        self.assertEqual(list(restored["z_extra"]), ["z", "a"])
        self.assertEqual(list(restored["schools"][0]), ["status", "id", "defaults"])


if __name__ == "__main__":
    unittest.main()
