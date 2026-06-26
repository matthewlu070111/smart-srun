import json
import os
import sys
import unittest


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")
CLOUDFLARE_DIR = os.path.join(REPO_ROOT, "cloudflare-pages")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)

import school_presets


class CloudflarePagesMirrorTests(unittest.TestCase):
    def test_plugin_prefers_cloudflare_preset_mirror(self):
        self.assertEqual(
            school_presets.REMOTE_PRESETS_URL,
            "https://srun.edu-publish.site/school-presets.json",
        )
        self.assertEqual(
            school_presets.REMOTE_PRESETS_URLS[0],
            school_presets.MIRROR_PRESETS_URL,
        )
        self.assertIn(
            school_presets.GITHUB_PRESETS_URL,
            school_presets.REMOTE_PRESETS_URLS,
        )

    def test_pages_static_json_matches_doc_presets(self):
        doc_path = os.path.join(REPO_ROOT, "doc", "school-presets.json")
        fallback_path = os.path.join(
            CLOUDFLARE_DIR, "public", "fallback-school-presets.json"
        )
        public_path = os.path.join(CLOUDFLARE_DIR, "public", "school-presets.json")

        with open(doc_path, "r", encoding="utf-8") as handle:
            doc_payload = json.load(handle)
        with open(fallback_path, "r", encoding="utf-8") as handle:
            fallback_payload = json.load(handle)
        with open(public_path, "r", encoding="utf-8") as handle:
            public_payload = json.load(handle)

        self.assertEqual(fallback_payload, doc_payload)
        self.assertEqual(public_payload, doc_payload)

    def test_pages_files_expose_expected_routes(self):
        with open(
            os.path.join(CLOUDFLARE_DIR, "package.json"),
            "r",
            encoding="utf-8",
        ) as handle:
            package_json = json.load(handle)
        with open(
            os.path.join(CLOUDFLARE_DIR, "public", "_redirects"),
            "r",
            encoding="utf-8",
        ) as handle:
            redirects = handle.read()
        with open(
            os.path.join(CLOUDFLARE_DIR, "public", "_headers"),
            "r",
            encoding="utf-8",
        ) as handle:
            headers = handle.read()
        with open(
            os.path.join(CLOUDFLARE_DIR, "functions", "school-presets.json.js"),
            "r",
            encoding="utf-8",
        ) as handle:
            function_source = handle.read()

        self.assertIn("/presets /school-presets.json 200", redirects)
        self.assertIn("/school-presets.json", headers)
        self.assertIn("Access-Control-Allow-Origin: *", headers)
        self.assertEqual(package_json["type"], "module")
        self.assertIn("env.ASSETS.fetch", function_source)
        self.assertIn("fallback-school-presets.json", function_source)
        self.assertIn(school_presets.GITHUB_PRESETS_URL, function_source)


if __name__ == "__main__":
    unittest.main()
