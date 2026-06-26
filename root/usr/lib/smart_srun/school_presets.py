"""Remote school preset loading and normalization."""

import json
import os
import re
import shutil
import subprocess
import time

try:
    from urllib import request as urlrequest
except ImportError:  # OpenWrt python3-light may omit urllib.
    urlrequest = None


SCHEMA_VERSION = 1
MIRROR_PRESETS_URL = "https://srun.edu-publish.site/school-presets.json"
GITHUB_PRESETS_URL = (
    "https://raw.githubusercontent.com/matthewlu070111/"
    "smart-srun/main/doc/school-presets.json"
)
REMOTE_PRESETS_URL = MIRROR_PRESETS_URL
REMOTE_PRESETS_URLS = (MIRROR_PRESETS_URL, GITHUB_PRESETS_URL)
MODULE_DIR = os.path.dirname(os.path.abspath(__file__))
FALLBACK_PRESETS_FILE = os.path.join(MODULE_DIR, "school_presets_fallback.json")
CACHE_PRESETS_FILE = os.path.join(MODULE_DIR, "school_presets_cache.json")
REMOTE_TIMEOUT_SECONDS = 8

DEFAULT_OPERATORS = [
    {"suffix": "cmcc", "label": "中国移动"},
    {"suffix": "ctcc", "label": "中国电信"},
    {"suffix": "cucc", "label": "中国联通"},
    {"suffix": "", "label": "校园网"},
]


def _read_json(path):
    try:
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
        return data if isinstance(data, dict) else {}
    except (OSError, TypeError, ValueError):
        return {}


def _write_json(path, payload):
    parent = os.path.dirname(path)
    if parent and not os.path.exists(parent):
        os.makedirs(parent)
    tmp_path = path + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")
    os.replace(tmp_path, path)


def normalize_base_url(value):
    text = str(value or "").strip()
    if not text:
        return ""
    if "://" not in text:
        text = "http://" + text

    match = re.match(r"^(https?)://([^/%?#]+)", text)
    if match:
        return "%s://%s" % (match.group(1), match.group(2))
    if not re.match(r"^[A-Za-z][A-Za-z0-9+.-]*://", text):
        host = re.match(r"^([^/%?#]+)", text)
        if host and host.group(1):
            return "http://" + host.group(1)
    return text.rstrip("/")


def _copy_string_list(value):
    if not isinstance(value, list):
        return []
    out = []
    for item in value:
        text = str(item or "").strip()
        if text:
            out.append(text)
    return out


def _normalize_operator(item):
    if not isinstance(item, dict):
        return None
    # 字段已从 id 改名为 suffix；仍接受旧的 id 以兼容尚未刷新的远端预设。
    has_suffix = "suffix" in item
    has_legacy_id = "id" in item
    if not has_suffix and not has_legacy_id:
        return None
    raw = item.get("suffix") if has_suffix else item.get("id")
    suffix = str(raw or "").strip().lower()
    operator = {
        "suffix": suffix,
        "label": str(item.get("label") or suffix or "校园网").strip()
        or "校园网",
    }
    return operator


def _canonical_operator_suffix(value):
    text = str(value or "").strip().lower()
    return "" if text == "xn" else text


def _legacy_default_operator(defaults):
    if not isinstance(defaults, dict):
        return None
    if "operator_suffix" in defaults:
        return _canonical_operator_suffix(defaults.get("operator_suffix"))
    if "operator" in defaults:
        return _canonical_operator_suffix(defaults.get("operator"))
    return None


def _operator_label_from_suffix(suffix):
    text = str(suffix or "").strip().lower()
    for item in DEFAULT_OPERATORS:
        if item["suffix"] == text:
            return item["label"]
    return text or "校园网"


def _normalize_operators(value, legacy_defaults=None):
    operators = []
    if isinstance(value, list):
        for raw in value:
            operator = _normalize_operator(raw)
            if operator:
                operators.append(operator)
    legacy_operator = _legacy_default_operator(legacy_defaults)
    if legacy_operator is not None and not any(
        item["suffix"] == legacy_operator for item in operators
    ):
        operators.insert(
            0,
            {
                "suffix": legacy_operator,
                "label": _operator_label_from_suffix(legacy_operator),
            },
        )
    return operators or [dict(item) for item in DEFAULT_OPERATORS]


def _normalize_defaults(value):
    if not isinstance(value, dict):
        return {}
    out = {}
    for key in (
        "base_url",
        "ac_id",
        "ssid",
        "access_mode",
    ):
        text = str(value.get(key, "")).strip()
        if not text:
            continue
        out[key] = normalize_base_url(text) if key == "base_url" else text
    if out.get("access_mode") not in ("wifi", "wired", None):
        out.pop("access_mode", None)
    return out


def _normalize_observed_login_shape(value):
    if not isinstance(value, dict):
        return {}
    out = {}
    for key in ("n", "type", "enc", "double_stack"):
        text = str(value.get(key, "")).strip()
        if text:
            out[key] = text
    info_prefix = str(value.get("info_prefix", "")).strip()
    if (
        info_prefix.startswith("{")
        and info_prefix.endswith("}")
        and len(info_prefix) > 2
    ):
        info_prefix = info_prefix[1:-1].strip()
    if info_prefix:
        out["info_prefix"] = info_prefix
    login_os = str(value.get("os", value.get("login_os", ""))).strip()
    login_name = str(value.get("name", value.get("login_name", ""))).strip()
    if login_os:
        out["os"] = login_os
    if login_name:
        out["name"] = login_name
    return out


def _safe_school_id(value):
    text = str(value or "").strip().lower()
    text = re.sub(r"[^a-z0-9_.-]+", "-", text).strip("-")
    return text


def normalize_school(item):
    if not isinstance(item, dict):
        return None
    short_name = _safe_school_id(item.get("id") or item.get("short_name"))
    if not short_name:
        return None

    raw_status = str(item.get("status") or "").strip().lower()
    if raw_status == "verified":
        status = "active"
    elif raw_status in ("active", "draft", "deprecated"):
        status = raw_status
    elif bool(item.get("verified", False)):
        status = "active"
    else:
        status = "draft"

    defaults = _normalize_defaults(item.get("defaults"))
    name = str(item.get("name") or short_name).strip() or short_name
    description = str(item.get("description") or "").strip()
    if not description:
        description = "远端学校预设" if status == "active" else "远端草稿预设"

    return {
        "short_name": short_name,
        "name": name,
        "description": description,
        "contributors": _copy_string_list(item.get("contributors")),
        "operators": _normalize_operators(item.get("operators"), item.get("defaults")),
        "defaults": defaults,
        "observed_login_shape": _normalize_observed_login_shape(
            item.get("observed_login_shape")
        ),
        "status": status,
        "source_issue": str(item.get("source_issue") or "").strip(),
        "doc_url": str(item.get("doc_url") or "").strip(),
    }


def normalize_payload(payload, include_draft=False):
    if not isinstance(payload, dict):
        return []
    if int(payload.get("schema_version") or 0) != SCHEMA_VERSION:
        return []
    out = []
    seen = set()
    for raw in payload.get("schools") or []:
        item = normalize_school(raw)
        if not item:
            continue
        if item["short_name"] in seen:
            continue
        if not include_draft and item.get("status") != "active":
            continue
        seen.add(item["short_name"])
        out.append(item)
    return out


def _fetch_via_urllib(url, timeout):
    if urlrequest is None:
        raise RuntimeError("urllib is unavailable")
    req = urlrequest.Request(
        url,
        headers={
            "Accept": "application/json",
            "User-Agent": "smart-srun-presets/1",
        },
    )
    with urlrequest.urlopen(req, timeout=timeout) as response:
        return response.read().decode("utf-8", "replace")


def _fetch_via_system_client(url, timeout):
    for command in ("uclient-fetch", "wget"):
        binary = shutil.which(command)
        if not binary:
            continue
        if command == "uclient-fetch":
            args = [binary, "-q", "-O", "-", url]
        else:
            args = [binary, "-q", "-O", "-", url]
        try:
            return subprocess.check_output(
                args, stderr=subprocess.STDOUT, timeout=timeout
            ).decode("utf-8", "replace")
        except (OSError, subprocess.SubprocessError):
            continue
    raise RuntimeError("no usable HTTP client for school presets")


def _remote_urls(url=None):
    if url:
        return (url,)
    return REMOTE_PRESETS_URLS


def _fetch_remote_payload_with_source(url=None, timeout=REMOTE_TIMEOUT_SECONDS):
    errors = []
    for candidate_url in _remote_urls(url):
        try:
            text = _fetch_via_urllib(candidate_url, timeout)
        except Exception:
            try:
                text = _fetch_via_system_client(candidate_url, timeout)
            except Exception as exc:
                errors.append("%s: %s" % (candidate_url, exc))
                continue
        try:
            data = json.loads(text)
        except ValueError as exc:
            errors.append("%s: %s" % (candidate_url, exc))
            continue
        if not isinstance(data, dict):
            errors.append(
                "%s: remote school presets must be a JSON object" % candidate_url
            )
            continue
        return data, candidate_url
    raise RuntimeError("failed to fetch school presets: %s" % "; ".join(errors))


def fetch_remote_payload(url=None, timeout=REMOTE_TIMEOUT_SECONDS):
    data, _source_url = _fetch_remote_payload_with_source(url=url, timeout=timeout)
    return data


def refresh_remote_presets(url=None, timeout=REMOTE_TIMEOUT_SECONDS):
    payload, source_url = _fetch_remote_payload_with_source(url=url, timeout=timeout)
    payload["_cached_at"] = int(time.time())
    payload["_source_url"] = source_url
    _write_json(CACHE_PRESETS_FILE, payload)
    return {
        "ok": True,
        "source_url": source_url,
        "cached_at": payload["_cached_at"],
        "schools": normalize_payload(payload, include_draft=True),
    }


def _refresh_remote_payload_for_list():
    try:
        payload, source_url = _fetch_remote_payload_with_source(
            timeout=REMOTE_TIMEOUT_SECONDS
        )
    except Exception:
        return {}
    payload["_cached_at"] = int(time.time())
    payload["_source_url"] = source_url
    _write_json(CACHE_PRESETS_FILE, payload)
    return payload


def _merge_presets(base_items, override_items):
    merged = {}
    order = []
    for item in base_items + override_items:
        key = item["short_name"]
        if key not in merged:
            order.append(key)
        merged[key] = item
    return [merged[key] for key in order]


def list_presets(include_draft=False, refresh=False):
    builtin = normalize_payload(_read_json(FALLBACK_PRESETS_FILE), include_draft=True)
    cached_payload = {}
    if refresh:
        cached_payload = _refresh_remote_payload_for_list()
    if not cached_payload:
        cached_payload = _read_json(CACHE_PRESETS_FILE)

    cached = normalize_payload(cached_payload, include_draft=True)
    merged = _merge_presets(builtin, cached)
    if include_draft:
        return merged
    return [item for item in merged if item.get("status") == "active"]


def get_preset(short_name, include_draft=False):
    wanted = _safe_school_id(short_name)
    for item in list_presets(include_draft=include_draft):
        if item["short_name"] == wanted:
            return dict(item)
    return None
