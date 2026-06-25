import os
import sys
import unittest
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import network
import portal_detect
from _portal_urls import (
    PORTAL_ACID4_THEME_URL,
    PORTAL_ACID9_PAGE_PATH,
    PORTAL_ACID9_PAGE_URL,
    PORTAL_HTTPS_ORIGIN,
    PORTAL_ORIGIN,
)


class PortalDetectTests(unittest.TestCase):
    def test_detect_acid_reads_full_portal_url_before_normalizing_base_url(self):
        payload = portal_detect.detect_acid(PORTAL_ACID4_THEME_URL)

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "4")
        self.assertEqual(payload["base_url"], PORTAL_ORIGIN)
        self.assertEqual(payload["source"], "input_url")

    def test_detect_acid_follows_redirect_location(self):
        def fake_fetch(url, timeout):
            self.assertEqual(url, PORTAL_ORIGIN)
            return 302, {"Location": PORTAL_ACID9_PAGE_PATH}, ""

        with mock.patch.object(portal_detect, "_fetch_once", side_effect=fake_fetch):
            payload = portal_detect.detect_acid(PORTAL_ORIGIN)

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "9")
        self.assertEqual(payload["source"], "redirect_url")
        self.assertEqual(payload["detected_url"], PORTAL_ACID9_PAGE_URL)

    def test_detect_acid_reads_hidden_input_from_html(self):
        html = '<input type="hidden" name="ac_id" value="17">'
        with mock.patch.object(portal_detect, "_fetch_once", return_value=(200, {}, html)):
            payload = portal_detect.detect_acid(PORTAL_ORIGIN)

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["acid"], "17")
        self.assertEqual(payload["source"], "html")

    def test_https_error_message_mentions_python_openssl(self):
        message = network.humanize_http_errors(
            PORTAL_HTTPS_ORIGIN, [RuntimeError("ssl module unavailable")]
        )

        self.assertIn("HTTPS", message)
        self.assertIn("python3-openssl", message)


if __name__ == "__main__":
    unittest.main()
