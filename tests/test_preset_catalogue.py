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

# Match any owner/repo/ref so a fork or a rename cannot silently stop checking
# the links instead of failing on a broken one.
DOC_LINK_RE = re.compile(r"^/[^/]+/[^/]+/blob/[^/]+/doc/(?P<path>.+)$")

# Every spelling `_normalize_observed_login_shape` consumes. Catching a typo is
# the point; the published values themselves belong to whoever captured them.
LOGIN_SHAPE_KEYS = frozenset(
    ("n", "type", "enc", "double_stack", "info_prefix",
     "os", "name", "login_os", "login_name")
)


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
        for school in self.payload["schools"]:
            url = urlsplit(school.get("doc_url", ""))
            link = DOC_LINK_RE.match(url.path) if url.netloc == "github.com" else None
            if link:
                with self.subTest(school=school["id"]):
                    target = REPO_ROOT / "doc" / unquote(link.group("path"))
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
                shape = school.get("observed_login_shape", {})
                self.assertLessEqual(set(shape), LOGIN_SHAPE_KEYS)
                for value in shape.values():
                    self.assertIsInstance(value, str)

    def test_published_status_decides_visibility_without_naming_any_school(self):
        active = {school["id"] for school in self.payload["schools"]
                  if school["status"] == "active"}
        visible = {item["short_name"]
                   for item in school_presets.normalize_payload(self.payload)}
        with_drafts = {item["short_name"] for item
                       in school_presets.normalize_payload(self.payload, include_draft=True)}
        self.assertEqual(visible, active)
        self.assertEqual(with_drafts, set(self.schools))


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
