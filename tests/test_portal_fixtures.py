"""Keep behavioral fixtures separate from intentional preset-data assertions."""

import ipaddress
import os
import runpy
import unittest
from pathlib import Path
from unittest import mock
from urllib.parse import urlsplit


FIXTURE_FILE = Path(__file__).with_name("_portal_urls.py")
DOCUMENTATION_NETWORKS = tuple(
    ipaddress.ip_network(cidr)
    for cidr in ("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24")
)


def load_fixtures(**overrides):
    environment = {
        key: value
        for key, value in os.environ.items()
        if not key.startswith("SMARTSRUN_TEST_")
    }
    environment.update(overrides)
    with mock.patch.dict(os.environ, environment, clear=True):
        return runpy.run_path(str(FIXTURE_FILE))


class PortalFixtureTests(unittest.TestCase):
    def test_defaults_use_reserved_names_and_documentation_addresses(self):
        fixtures = load_fixtures()
        # Real gateways belong in dedicated preset catalogue assertions, not
        # the default addresses used by socket mocks and behavioral tests.
        for key in ("PORTAL_ORIGIN", "PORTAL_HTTPS_ORIGIN"):
            with self.subTest(key=key):
                self.assertTrue(urlsplit(fixtures[key]).hostname.endswith(".test"))

        addresses = [
            fixtures["CLIENT_IP"],
            fixtures["BIND_IP"],
            fixtures["WIRED_BIND_IP"],
            fixtures["DNS_SERVER_IP"],
            fixtures["PORTAL_BARE_HOST"],
            urlsplit(fixtures["PORTAL_IPV4_ORIGIN"]).hostname,
        ]
        for value in addresses:
            with self.subTest(address=value):
                self.assertTrue(
                    any(
                        ipaddress.ip_address(value) in net
                        for net in DOCUMENTATION_NETWORKS
                    )
                )

    def test_local_overrides_keep_origin_and_scheme_contract(self):
        fixtures = load_fixtures(
            SMARTSRUN_TEST_PORTAL_ORIGIN="https://custom.example.test:8080/path?ac_id=9",
            SMARTSRUN_TEST_PORTAL_HTTPS_ORIGIN="https://secure.example.test:8443/path",
            SMARTSRUN_TEST_PORTAL_BARE_HOST="http://203.0.113.25/page",
        )
        self.assertEqual(fixtures["PORTAL_ORIGIN"], "http://custom.example.test:8080")
        self.assertEqual(fixtures["PORTAL_DNS_NAME"], "custom.example.test")
        self.assertEqual(fixtures["PORTAL_PORT"], 8080)
        self.assertEqual(
            fixtures["PORTAL_HTTPS_ORIGIN"], "https://secure.example.test:8443"
        )
        self.assertEqual(fixtures["PORTAL_BARE_ORIGIN"], "http://203.0.113.25")


if __name__ == "__main__":
    unittest.main()
