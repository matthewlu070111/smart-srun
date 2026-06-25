"""Shared placeholder portal URLs for tests.

These values are only used by source-level tests. They do not trigger real
network requests unless a test explicitly patches a fetcher to do so.

School maintainers can override the placeholders locally without editing test
files, for example:

    SMARTSRUN_TEST_PORTAL_ORIGIN=http://portal.example.edu
    SMARTSRUN_TEST_DEFAULT_BASE_URL=http://172.17.1.2
    SMARTSRUN_TEST_PORTAL_IPV4_ORIGIN=http://198.51.100.10
    SMARTSRUN_TEST_PORTAL_BARE_HOST=203.0.113.5
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
    "SMARTSRUN_TEST_PORTAL_ORIGIN", "http://portal.example.edu"
)
PORTAL_HOST = urlsplit(_DEFAULT_PORTAL_ORIGIN).netloc
PORTAL_ORIGIN = "http://" + PORTAL_HOST
PORTAL_HTTPS_ORIGIN = _origin(
    "SMARTSRUN_TEST_PORTAL_HTTPS_ORIGIN", "https://" + PORTAL_HOST
)
PORTAL_IPV4_ORIGIN = _origin(
    "SMARTSRUN_TEST_PORTAL_IPV4_ORIGIN", "http://198.51.100.10"
)
PORTAL_BARE_HOST = _host("SMARTSRUN_TEST_PORTAL_BARE_HOST", "203.0.113.5")
PORTAL_BARE_ORIGIN = "http://" + PORTAL_BARE_HOST

CLIENT_IP = os.environ.get("SMARTSRUN_TEST_CLIENT_IP", "192.0.2.8").strip()
BIND_IP = os.environ.get("SMARTSRUN_TEST_BIND_IP", "192.0.2.9").strip()
PROJECT_DEFAULT_BASE_URL = _origin(
    "SMARTSRUN_TEST_DEFAULT_BASE_URL", "http://172.17.1.2"
)


def portal_page_url(origin=PORTAL_ORIGIN, acid="1", extra=""):
    query = "ac_id=" + str(acid)
    if extra:
        query += "&" + str(extra).lstrip("&")
    return origin.rstrip("/") + "/srun_portal_pc?" + query


PORTAL_ACID1_PAGE_URL = portal_page_url(PORTAL_ORIGIN, "1")
PORTAL_ACID4_THEME_URL = portal_page_url(PORTAL_ORIGIN, "4", "theme=basic2")
PORTAL_IPV4_ACID4_THEME_URL = portal_page_url(
    PORTAL_IPV4_ORIGIN, "4", "theme=basic2"
)
PORTAL_BARE_ACID1_PAGE_URL = PORTAL_BARE_HOST + "/srun_portal_pc?ac_id=1"
PORTAL_HTTPS_LOGIN_PATH_URL = PORTAL_HTTPS_ORIGIN + "/path/to/login"
PORTAL_ACID9_PAGE_PATH = "/srun_portal_pc?ac_id=9"
PORTAL_ACID9_PAGE_URL = PORTAL_ORIGIN + PORTAL_ACID9_PAGE_PATH
PORTAL_PING_URL = PORTAL_ORIGIN + "/ping"
PORTAL_LOGIN_URL = PORTAL_ORIGIN + "/cgi-bin/srun_portal"
