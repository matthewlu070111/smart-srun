"""Validate the single catalogue installed verbatim by the Go SDK package."""
import json
from pathlib import Path
import re
import unittest
from urllib.parse import unquote, urlsplit

REPO_ROOT = Path(__file__).resolve().parents[1]
DOC_LINK_RE = re.compile(r"^/[^/]+/[^/]+/blob/[^/]+/doc/(?P<path>.+)$")
LOGIN_SHAPE_KEYS = frozenset(("n", "type", "enc", "double_stack", "info_prefix", "os", "name", "login_os", "login_name"))

class PresetCatalogueTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.payload = json.loads((REPO_ROOT / "doc/school-presets.json").read_text(encoding="utf-8"))
        cls.schools = {school["id"]: school for school in cls.payload["schools"]}

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
