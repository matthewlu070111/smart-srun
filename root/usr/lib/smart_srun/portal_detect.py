"""Portal page probes for SRun metadata such as AC_ID."""

import re

from network import HAVE_URLLIB, HEADER, humanize_http_errors, http_get
from school_presets import normalize_base_url

try:
    from urllib import error as urllib_error
    from urllib import parse as urllib_parse
    from urllib import request as urllib_request
except ImportError:  # OpenWrt python3-light may omit urllib pieces.
    urllib_error = None
    urllib_parse = None
    urllib_request = None


MAX_REDIRECTS = 8


def _acid_from_url(url):
    if not urllib_parse:
        match = re.search(r"[?&]ac_id=([^&#]+)", str(url or ""))
        return match.group(1).strip() if match else ""
    try:
        parsed = urllib_parse.urlsplit(str(url or ""))
    except ValueError:
        return ""
    values = urllib_parse.parse_qs(parsed.query, keep_blank_values=False)
    acid_values = values.get("ac_id") or values.get("acid") or []
    return str(acid_values[0]).strip() if acid_values else ""


def _valid_acid(value):
    text = str(value or "").strip()
    if not text:
        return ""
    return text if re.match(r"^[A-Za-z0-9_.-]+$", text) else ""


def _acid_from_html(html):
    text = str(html or "")
    patterns = [
        r"<input[^>]+name=[\"']ac_id[\"'][^>]*value=[\"']([^\"']+)[\"']",
        r"<input[^>]+value=[\"']([^\"']+)[\"'][^>]*name=[\"']ac_id[\"']",
        r"[\"']ac_id[\"'][^<>]{0,120}?value=[\"']([^\"']+)[\"']",
        r"\bac_id\s*[:=]\s*[\"']?([A-Za-z0-9_.-]+)",
        r"[?&]ac_id=([A-Za-z0-9_.-]+)",
    ]
    for pattern in patterns:
        match = re.search(pattern, text, re.I | re.S)
        if match:
            acid = _valid_acid(match.group(1))
            if acid:
                return acid
    return ""


def _html_redirect_location(html):
    text = str(html or "")
    patterns = [
        r"<script[^>]*>\s*top\.self\.location\.href\s*=\s*[\"']([^\"']+)[\"']",
        r"\blocation\.href\s*=\s*[\"']([^\"']+)[\"']",
        r"<meta[^>]+http-equiv=[\"']?refresh[\"']?[^>]+content=[\"'][^\"']*url=([^\"']+)[\"']",
    ]
    for pattern in patterns:
        match = re.search(pattern, text, re.I | re.S)
        if match:
            return match.group(1).strip()
    return ""


class _NoRedirectHandler(urllib_request.HTTPRedirectHandler if urllib_request else object):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _fetch_once(url, timeout):
    if not HAVE_URLLIB or not urllib_request:
        return 200, {}, http_get(url, timeout=timeout)

    opener = urllib_request.build_opener(_NoRedirectHandler)
    req = urllib_request.Request(url, headers=HEADER, method="GET")
    try:
        response = opener.open(req, timeout=timeout)
        try:
            body = response.read().decode("utf-8", errors="replace")
            return response.getcode(), dict(response.headers.items()), body
        finally:
            response.close()
    except urllib_error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        return exc.code, dict(exc.headers.items()), body
    except Exception as exc:
        raise RuntimeError(humanize_http_errors(url, [exc]))


def _join_url(base, location):
    if not location:
        return ""
    if urllib_parse:
        return urllib_parse.urljoin(base, location)
    if location.startswith("http://") or location.startswith("https://"):
        return location
    return base.rstrip("/") + "/" + location.lstrip("/")


def _probe_url(start_url, timeout=5):
    current = str(start_url or "").strip()
    seen = set()
    for _idx in range(MAX_REDIRECTS):
        if not current or current in seen:
            break
        seen.add(current)

        acid = _acid_from_url(current)
        if acid:
            return acid, "url", current

        status, headers, body = _fetch_once(current, timeout)
        acid = _acid_from_html(body)
        if acid:
            return acid, "html", current

        location = ""
        for key, value in headers.items():
            if str(key).lower() == "location":
                location = str(value or "").strip()
                break
        if not location:
            location = _html_redirect_location(body)
        if not location:
            break

        next_url = _join_url(current, location)
        acid = _acid_from_url(next_url)
        if acid:
            return acid, "redirect_url", next_url
        current = next_url

        if status >= 400:
            break
    return "", "", ""


def detect_acid(base_url, reality_url="", timeout=5):
    raw_url = str(base_url or "").strip()
    if not raw_url:
        return {
            "ok": False,
            "acid": "",
            "base_url": "",
            "source": "",
            "message": "请先填写认证地址",
        }

    acid = _acid_from_url(raw_url)
    normalized = normalize_base_url(raw_url)
    if acid:
        return {
            "ok": True,
            "acid": acid,
            "base_url": normalized,
            "source": "input_url",
            "message": "已从认证地址 URL 中发现 AC_ID",
        }

    try:
        if reality_url:
            acid, source, detected_url = _probe_url(reality_url, timeout=timeout)
            if acid:
                return {
                    "ok": True,
                    "acid": acid,
                    "base_url": normalized,
                    "source": "reality_" + source,
                    "detected_url": detected_url,
                    "message": "已从网络劫持跳转中发现 AC_ID",
                }

        acid, source, detected_url = _probe_url(normalized, timeout=timeout)
        if acid:
            return {
                "ok": True,
                "acid": acid,
                "base_url": normalized,
                "source": source,
                "detected_url": detected_url,
                "message": "已从认证网关页面中发现 AC_ID",
            }
    except Exception as exc:
        return {
            "ok": False,
            "acid": "",
            "base_url": normalized,
            "source": "",
            "message": str(exc),
        }

    return {
        "ok": False,
        "acid": "",
        "base_url": normalized,
        "source": "",
        "message": "未从认证地址、跳转链或页面中发现 AC_ID",
    }
