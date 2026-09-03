"""Shared placeholder portal URLs for tests.

These values are used by source contracts and behavioral tests with mocked
network operations. Defining a fixture does not initiate a network request.

School maintainers can override the placeholders locally without editing test
files, for example:

    SMARTSRUN_TEST_PORTAL_ORIGIN=http://portal.example.test
    SMARTSRUN_TEST_PORTAL_IPV4_ORIGIN=http://198.51.100.10
    SMARTSRUN_TEST_PORTAL_BARE_HOST=203.0.113.5
    SMARTSRUN_TEST_WIRED_BIND_IP=192.0.2.20

Default hostnames use the reserved .test suffix; IPv4 defaults use documentation
ranges (RFC 5737). Keep real campus gateways out of this file: they belong in
the preset JSON and tests that explicitly verify that catalogue's data.
"""

import os
from urllib.parse import urlsplit


def _origin(env_name, default):
    text = os.environ.get(env_name, default).strip() or default
    if "://" not in text:
        text = "http://" + text
    parsed = urlsplit(text)
    if not parsed.scheme or not parsed.netloc:
        parsed = urlsplit(default)
    return "%s://%s" % (parsed.scheme, parsed.netloc)


def _host(env_name, default):
    text = os.environ.get(env_name, default).strip() or default
    if "://" in text:
        parsed = urlsplit(text)
        return parsed.netloc or default
    return text.split("/", 1)[0]


_DEFAULT_PORTAL_ORIGIN = _origin(
    "SMARTSRUN_TEST_PORTAL_ORIGIN", "http://portal.example.test"
)
PORTAL_HOST = urlsplit(_DEFAULT_PORTAL_ORIGIN).netloc
PORTAL_ORIGIN = "http://" + PORTAL_HOST
PORTAL_DNS_NAME = urlsplit(PORTAL_ORIGIN).hostname
PORTAL_PORT = urlsplit(PORTAL_ORIGIN).port or 80
PORTAL_HTTPS_ORIGIN = _origin(
    "SMARTSRUN_TEST_PORTAL_HTTPS_ORIGIN", "https://" + PORTAL_HOST
)
PORTAL_IPV4_ORIGIN = _origin(
    "SMARTSRUN_TEST_PORTAL_IPV4_ORIGIN", "http://198.51.100.10"
)
PORTAL_IPV4_HOST = urlsplit(PORTAL_IPV4_ORIGIN).hostname
PORTAL_IPV4_PORT = urlsplit(PORTAL_IPV4_ORIGIN).port or 80
PORTAL_BARE_HOST = _host("SMARTSRUN_TEST_PORTAL_BARE_HOST", "203.0.113.5")
PORTAL_BARE_ORIGIN = "http://" + PORTAL_BARE_HOST

CLIENT_IP = os.environ.get("SMARTSRUN_TEST_CLIENT_IP", "192.0.2.8").strip()
BIND_IP = os.environ.get("SMARTSRUN_TEST_BIND_IP", "192.0.2.9").strip()
DNS_SERVER_IP = os.environ.get("SMARTSRUN_TEST_DNS_SERVER_IP", "192.0.2.53").strip()

# The address the pinned WAN hands out in multi-WAN tests. Interface names
# such as "wan.v2" stay inline: they are an OpenWrt VLAN naming shape that the
# LuCI editor already shows every user, not a campus address.
WIRED_BIND_IP = os.environ.get("SMARTSRUN_TEST_WIRED_BIND_IP", "192.0.2.20").strip()


def portal_page_url(origin=PORTAL_ORIGIN, acid="1", extra=""):
    query = "ac_id=" + str(acid)
    if extra:
        query += "&" + str(extra).lstrip("&")
    return origin.rstrip("/") + "/srun_portal_pc?" + query


PORTAL_ACID1_PAGE_URL = portal_page_url(PORTAL_ORIGIN, "1")
PORTAL_ACID4_THEME_URL = portal_page_url(PORTAL_ORIGIN, "4", "theme=basic2")
PORTAL_IPV4_ACID4_THEME_URL = portal_page_url(PORTAL_IPV4_ORIGIN, "4", "theme=basic2")
PORTAL_BARE_ACID1_PAGE_URL = PORTAL_BARE_HOST + "/srun_portal_pc?ac_id=1"
PORTAL_HTTPS_LOGIN_PATH_URL = PORTAL_HTTPS_ORIGIN + "/path/to/login"
PORTAL_ACID9_PAGE_PATH = "/srun_portal_pc?ac_id=9"
PORTAL_ACID9_PAGE_URL = PORTAL_ORIGIN + PORTAL_ACID9_PAGE_PATH
PORTAL_PING_URL = PORTAL_ORIGIN + "/ping"
PORTAL_LOGIN_URL = PORTAL_ORIGIN + "/cgi-bin/srun_portal"
CONNECTIVITY_PROBE_URL = PORTAL_ORIGIN + "/generate_204"
