"""Published catalogue data, verification status and shared JSON presentation."""

import importlib.util
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest


REPO_ROOT = Path(__file__).resolve().parents[1]
MODULE_ROOT = REPO_ROOT / "root/usr/lib/smart_srun"
if str(MODULE_ROOT) not in sys.path:
    sys.path.insert(0, str(MODULE_ROOT))

import school_presets  # noqa: E402

SPEC = importlib.util.spec_from_file_location(
    "format_school_presets", REPO_ROOT / "scripts/format_school_presets.py"
)
formatter = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(formatter)


class PresetCatalogueTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.payload = json.loads((REPO_ROOT / formatter.DOC_PATH).read_text(encoding="utf-8"))
        cls.schools = {school["id"]: school for school in cls.payload["schools"]}

    def test_published_pair_uses_shared_format_and_identical_content(self):
        self.assertEqual(formatter.format_catalogue(REPO_ROOT), [])
        fallback = json.loads((REPO_ROOT / formatter.FALLBACK_PATH).read_text(encoding="utf-8"))
        self.assertEqual(fallback["source"], "bundled fallback")
        fallback["source"] = self.payload["source"]
        self.assertEqual(fallback, self.payload)
        for relative in (formatter.DOC_PATH, formatter.FALLBACK_PATH):
            with self.subTest(path=str(relative)):
                raw = (REPO_ROOT / relative).read_bytes()
                self.assertTrue(raw.endswith(b"\n"))
                self.assertFalse(raw.endswith(b"\n\n"))
                self.assertNotIn(b"\r", raw)
                self.assertIn(b'\n  "schema_version": 1,\n', raw)
                self.assertIn(b'"operators": [\n        {\n          "suffix":', raw)

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


class PresetFormatterTests(unittest.TestCase):
    def test_check_is_read_only_and_write_repairs_fallback_without_losing_metadata(self):
        payload = {"schools": [{"name": "Example", "id": "example", "status": "draft",
                                "defaults": {"ac_id": "9"}, "extra_notes": {"unconfirmed": True}}],
                   "source": "fixture", "schema_version": 1, "updated_at": "2026-09-03"}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            main = root / formatter.DOC_PATH
            main.parent.mkdir(parents=True)
            before = json.dumps(payload).encode("utf-8")
            main.write_bytes(before)
            self.assertEqual(len(formatter.format_catalogue(root)), 2)
            self.assertEqual(main.read_bytes(), before)
            self.assertFalse((root / formatter.FALLBACK_PATH).exists())
            self.assertEqual(len(formatter.format_catalogue(root, write=True)), 2)
            self.assertEqual(formatter.format_catalogue(root), [])
            after = json.loads(main.read_text(encoding="utf-8"))
            self.assertEqual(after, payload)
            self.assertNotIn("base_url", after["schools"][0]["defaults"])
            self.assertEqual(main.read_text(encoding="utf-8"), school_presets.format_preset_payload(payload))


if __name__ == "__main__":
    unittest.main()
