#!/usr/bin/python3

import argparse
import hashlib
import hmac
import ipaddress
import json
import math
import os
import re
import socket
import subprocess
import time
from datetime import datetime, timedelta, timezone

try:
    import urllib.error as urllib_error
    import urllib.request as urllib_request

    HAVE_URLLIB = True
except ModuleNotFoundError:
    urllib_error = None
    urllib_request = None
    HAVE_URLLIB = False

HEADER = {
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/63.0.3239.26 Safari/537.36"
    )
}

BEIJING_TZ = timezone(timedelta(hours=8))
LOG_FILE = "/var/log/jxnu_srun.log"
JSON_CONFIG_FILE = "/usr/lib/jxnu_srun/config.json"
STATE_FILE = "/var/run/jxnu_srun/state.json"
ACTION_FILE = "/var/run/jxnu_srun/action.json"
LOG_MAX_BYTES = 512 * 1024
CONNECTIVITY_CACHE_SECONDS = 15
SWITCH_DELAY_SECONDS = 2
SSID_READY_TIMEOUT_SECONDS = 12
SSID_EXPECTED_RETRY_SECONDS = 30
DISCONNECT_RETRY_DELAY_SECONDS = 3
DEFAULTS_JSON_FILE = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "defaults.json"
)

# 全局标量字段（字符串类型），不包含数组和指针
GLOBAL_SCALAR_KEYS = {
    "enabled",
    "quiet_hours_enabled",
    "quiet_start",
    "quiet_end",
    "force_logout_in_quiet",
    "failover_enabled",
    "backoff_enable",
    "backoff_max_retries",
    "backoff_initial_duration",
    "backoff_max_duration",
    "retry_cooldown_seconds",
    "retry_max_cooldown_seconds",
    "switch_ready_timeout_seconds",
    "manual_terminal_check_max_attempts",
    "manual_terminal_check_interval_seconds",
    "hotspot_failback_enabled",
    "connectivity_check_mode",
    "backoff_exponent_factor",
    "backoff_inter_const_factor",
    "backoff_outer_const_factor",
    "interval",
    "developer_mode",
    "sta_iface",
    "n",
    "type",
    "enc",
}

# 指针字段
POINTER_KEYS = {
    "active_campus_id",
    "default_campus_id",
    "active_hotspot_id",
    "default_hotspot_id",
}

# 列表字段
LIST_KEYS = {"campus_accounts", "hotspot_profiles"}

# 旧版扁平字段（用于迁移检测）
LEGACY_CAMPUS_KEYS = {
    "user_id",
    "operator",
    "password",
    "base_url",
    "ac_id",
    "campus_ssid",
    "campus_encryption",
    "campus_key",
}
LEGACY_HOTSPOT_KEYS = {
    "hotspot_ssid",
    "hotspot_encryption",
    "hotspot_key",
    "hotspot_radio",
}


def _load_defaults():
    """加载默认配置，支持新版嵌套 JSON 结构"""
    try:
        with open(DEFAULTS_JSON_FILE, "r", encoding="utf-8") as f:
            data = json.load(f)
        if isinstance(data, dict):
            out = {}
            for k, v in data.items():
                if k in LIST_KEYS:
                    out[k] = v if isinstance(v, list) else []
                else:
                    out[k] = str(v)
            return out
    except Exception:
        pass
    return {
        "enabled": "0",
        "quiet_hours_enabled": "1",
        "quiet_start": "00:00",
        "quiet_end": "06:00",
        "force_logout_in_quiet": "1",
        "failover_enabled": "1",
        "backoff_enable": "1",
        "backoff_max_retries": "0",
        "backoff_initial_duration": "10",
        "backoff_max_duration": "600",
        "retry_cooldown_seconds": "10",
        "retry_max_cooldown_seconds": "600",
        "switch_ready_timeout_seconds": "12",
        "manual_terminal_check_max_attempts": "5",
        "manual_terminal_check_interval_seconds": "2",
        "hotspot_failback_enabled": "1",
        "connectivity_check_mode": "internet",
        "backoff_exponent_factor": "1.5",
        "backoff_inter_const_factor": "0",
        "backoff_outer_const_factor": "0",
        "interval": "60",
        "developer_mode": "0",
        "sta_iface": "",
        "n": "200",
        "type": "1",
        "enc": "srun_bx1",
        "active_campus_id": "",
        "default_campus_id": "",
        "active_hotspot_id": "",
        "default_hotspot_id": "",
        "campus_accounts": [],
        "hotspot_profiles": [],
    }


DEFAULTS = _load_defaults()

OPERATORS = {"cmcc", "ctcc", "cucc", "xn"}
PAD_CHAR = "="
ALPHA = "LVoJPiCN2R8G90yg+hmFHuacZ1OWMnrsSTXkYpUq/3dlbfKwv6xztjI7DeBE45QA"
HTTP_EXCEPTIONS = (socket.timeout,)
if HAVE_URLLIB:
    HTTP_EXCEPTIONS = HTTP_EXCEPTIONS + (urllib_error.URLError,)


def ensure_parent_dir(path):
    parent = os.path.dirname(str(path or ""))
    if parent:
        os.makedirs(parent, exist_ok=True)


def ensure_json_config_file():
    ensure_parent_dir(JSON_CONFIG_FILE)
    if os.path.exists(JSON_CONFIG_FILE):
        return
    with open(JSON_CONFIG_FILE, "w", encoding="utf-8") as wf:
        wf.write("{}\n")


def load_json_raw_config():
    """读取 config.json 原始内容，支持新版嵌套结构和旧版扁平结构"""
    ensure_json_config_file()
    try:
        with open(JSON_CONFIG_FILE, "r", encoding="utf-8") as rf:
            data = json.load(rf)
        if isinstance(data, dict):
            return data
    except Exception:
        pass
    return {}


def save_json_raw_config(raw_cfg):
    """保存 config.json，全局标量和指针存字符串，列表存原生数组"""
    payload = {}
    for key in GLOBAL_SCALAR_KEYS:
        default_val = DEFAULTS.get(key, "")
        payload[key] = str(raw_cfg.get(key, default_val))
    for key in POINTER_KEYS:
        payload[key] = str(raw_cfg.get(key, ""))
    for key in LIST_KEYS:
        val = raw_cfg.get(key)
        payload[key] = val if isinstance(val, list) else []

    ensure_parent_dir(JSON_CONFIG_FILE)
    with open(JSON_CONFIG_FILE, "w", encoding="utf-8") as wf:
        json.dump(payload, wf, ensure_ascii=False, indent=2, sort_keys=True)
        wf.write("\n")


def get_json_scalar_config(key, default_value=""):
    raw = load_json_raw_config()
    value = raw.get(str(key), default_value)
    if value is None:
        value = default_value
    return str(value).strip()


def set_json_scalar_config(key, value):
    raw = load_json_raw_config()
    raw[str(key)] = str(value)
    save_json_raw_config(raw)


def _state_flag_enabled(value):
    return str(value).strip().lower() in ("1", "true", "yes", "on")


def begin_manual_login_service_guard():
    previous_enabled = get_json_scalar_config("enabled", DEFAULTS.get("enabled", "0"))
    if previous_enabled != "1":
        return False, previous_enabled

    set_json_scalar_config("enabled", "0")
    runtime_state = load_runtime_state()
    runtime_state["manual_service_guard_active"] = True
    runtime_state["manual_service_enabled_before"] = previous_enabled
    save_runtime_state(runtime_state)
    return True, previous_enabled


def restore_manual_login_service_guard(clear_only=False):
    runtime_state = load_runtime_state()
    if not _state_flag_enabled(runtime_state.get("manual_service_guard_active")):
        return False, ""

    previous_enabled = str(
        runtime_state.get("manual_service_enabled_before", "")
    ).strip()
    if (not clear_only) and previous_enabled:
        set_json_scalar_config("enabled", previous_enabled)

    runtime_state["manual_service_guard_active"] = False
    runtime_state["manual_service_enabled_before"] = ""
    save_runtime_state(runtime_state)
    return True, previous_enabled


def reconcile_manual_login_service_guard():
    runtime_state = load_runtime_state()
    if not _state_flag_enabled(runtime_state.get("manual_service_guard_active")):
        return False

    pending_action = str(load_json_file(ACTION_FILE).get("action", "")).strip()
    if pending_action == "manual_login":
        return False

    restored, previous_enabled = restore_manual_login_service_guard()
    if restored and previous_enabled == "1":
        append_log("[JXNU-SRun] 检测到遗留的手动登录保护状态，已恢复自动服务开关。")
    return restored


def _pointer_meta(expect_hotspot):
    if expect_hotspot:
        return {
            "label": "热点配置",
            "list_key": "hotspot_profiles",
            "active_key": "active_hotspot_id",
            "default_key": "default_hotspot_id",
        }
    return {
        "label": "校园网账号",
        "list_key": "campus_accounts",
        "active_key": "active_campus_id",
        "default_key": "default_campus_id",
    }


def apply_default_selection_for_runtime(expect_hotspot, reason=""):
    meta = _pointer_meta(expect_hotspot)
    raw = load_json_raw_config()
    items = raw.get(meta["list_key"])
    if not isinstance(items, list) or not items:
        return load_config(), False, ""

    default_id = str(raw.get(meta["default_key"], "")).strip()
    if not default_id:
        return load_config(), False, ""

    found = _find_item_by_id(items, default_id)
    if not found:
        return load_config(), False, ""

    active_id = str(raw.get(meta["active_key"], "")).strip()
    if active_id == default_id:
        return load_config(), False, ""

    raw[meta["active_key"]] = default_id
    save_json_raw_config(raw)

    suffix = ""
    if reason:
        suffix = "（%s）" % str(reason).strip()
    append_log(
        "[JXNU-SRun] 已应用默认%s到运行态%s：%s -> %s。"
        % (meta["label"], suffix, active_id or "未设置", default_id)
    )
    return load_config(), True, default_id


def load_json_file(path, allowed_keys=None):
    try:
        with open(path, "r", encoding="utf-8") as rf:
            data = json.load(rf)
        if isinstance(data, dict):
            if allowed_keys is None:
                return data
            return {k: data[k] for k in data if k in allowed_keys}
    except Exception:
        pass
    return {}


def save_json_file(path, payload):
    ensure_parent_dir(path)
    tmp_path = str(path) + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as wf:
        json.dump(payload, wf, ensure_ascii=False, indent=2, sort_keys=True)
        wf.write("\n")
    os.replace(tmp_path, path)


def load_runtime_state():
    data = load_json_file(STATE_FILE)
    return data if isinstance(data, dict) else {}


def save_runtime_state(state):
    payload = dict(state or {})
    payload["updated_at"] = int(time.time())
    save_json_file(STATE_FILE, payload)


def save_runtime_status(message, state=None, **extra):
    payload = load_runtime_state()
    if state:
        payload.update(state)
    payload.update(extra)
    payload["message"] = str(message or "")
    save_runtime_state(payload)


def build_runtime_snapshot(cfg, state=None):
    data = parse_wireless_iface_data()
    section = get_runtime_sta_section(cfg, data)
    profile = get_sta_profile_from_section(section, data) if section else {}
    ssid = str(profile.get("ssid", "")).strip()
    bssid = str(profile.get("bssid", "")).strip().lower()
    net = get_network_interface_from_sta_section(section, data) if section else None
    ip = get_ipv4_from_network_interface(net) if net else None
    previous = load_runtime_state()
    wired_mode = campus_uses_wired(cfg)
    wan_ip = get_ipv4_from_network_interface("wan") if wired_mode else None
    wired_online = False

    if wired_mode and wan_ip:
        ssid = "有线接入"
        bssid = ""
        net = "wan"
        ip = wan_ip

    connectivity = "未连接"
    connectivity_level = "offline"
    online_account_label = ""
    if ip:
        now_ts = int(time.time())
        cache_ip = str(previous.get("current_ip", "")).strip()
        cache_level = str(previous.get("connectivity_level", "")).strip()
        cache_text = str(previous.get("connectivity", "")).strip()
        cache_ts = int(previous.get("connectivity_checked_at", 0) or 0)
        cache_valid = (
            cache_ip == ip
            and cache_level
            and cache_text
            and (now_ts - cache_ts) <= CONNECTIVITY_CACHE_SECONDS
        )
        if cache_valid:
            connectivity = cache_text
            connectivity_level = cache_level
        else:
            internet_ok, internet_msg = test_internet_connectivity(timeout=2)
            if internet_ok:
                connectivity = "互联网可达"
                connectivity_level = "online"
            else:
                portal_ok, portal_msg = _test_portal_reachability(cfg, timeout=2)
                if portal_ok:
                    connectivity = "认证网关可达"
                    connectivity_level = "portal"
                else:
                    detail = internet_msg or portal_msg or "连通性未知"
                    connectivity = "已连接但受限: %s" % detail
                    connectivity_level = "limited"
            previous["connectivity_checked_at"] = now_ts
    else:
        previous["connectivity_checked_at"] = int(time.time())

    if cfg.get("username") and wired_mode and wan_ip:
        try:
            urls = build_urls(cfg["base_url"])
            online_now, online_user, _ = query_online_identity(
                urls["rad_user_info_api"], cfg["username"], bind_ip=wan_ip
            )
            if online_now and online_user:
                wired_online = True
                online_account_label = online_user
        except Exception:
            wired_online = False

    if wired_online:
        mode = "campus"
    elif ssid == str(cfg.get("hotspot_ssid", "")).strip() and ssid:
        mode = "hotspot"
    elif ssid == str(cfg.get("campus_ssid", "")).strip() and ssid:
        mode = "campus"
    else:
        mode = "unknown"

    if mode != "hotspot" and cfg.get("username") and not wired_online:
        try:
            urls = build_urls(cfg["base_url"])
            online_now, online_user, _ = query_online_identity(
                urls["rad_user_info_api"], cfg["username"]
            )
            if online_now and online_user:
                online_account_label = online_user
        except Exception:
            online_account_label = ""

    if mode == "campus":
        mode_label = "校园网模式（有线）" if wired_mode else "校园网模式"
    elif mode == "hotspot":
        mode_label = "热点模式"
    else:
        mode_label = "未知模式"

    current_campus_access_mode = ""
    if mode == "campus":
        current_campus_access_mode = "wired" if wired_mode and net == "wan" else "wifi"

    return {
        "current_mode": mode,
        "mode": mode,
        "mode_label": mode_label,
        "current_ssid": ssid,
        "current_bssid": bssid,
        "current_iface": str(net or ""),
        "current_ip": str(ip or ""),
        "connectivity": connectivity,
        "connectivity_level": connectivity_level,
        "connectivity_checked_at": int(previous.get("connectivity_checked_at", 0) or 0),
        "campus_account_label": str(cfg.get("campus_account_label", "")),
        "campus_access_mode": str(cfg.get("campus_access_mode", "wifi")),
        "current_campus_access_mode": current_campus_access_mode,
        "online_account_label": online_account_label,
        "hotspot_profile_label": str(cfg.get("hotspot_profile_label", "")),
        "campus_ssid": str(cfg.get("campus_ssid", "")),
        "campus_bssid": str(cfg.get("campus_bssid", "")).strip().lower(),
    }


def queue_runtime_action(action):
    payload = {
        "action": str(action or "").strip(),
        "requested_at": int(time.time()),
    }
    save_json_file(ACTION_FILE, payload)


def pop_runtime_action():
    payload = load_json_file(ACTION_FILE)
    try:
        os.remove(ACTION_FILE)
    except OSError:
        pass
    return payload if isinstance(payload, dict) else {}


def parse_non_negative_int(value, default_value):
    try:
        parsed = int(str(value).strip())
        return parsed if parsed >= 0 else int(default_value)
    except Exception:
        return int(default_value)


def parse_non_negative_float(value, default_value):
    try:
        parsed = float(str(value).strip())
        return parsed if parsed >= 0 else float(default_value)
    except Exception:
        return float(default_value)


def _is_legacy_config(raw):
    """检测是否为旧版扁平配置（没有 campus_accounts 数组）"""
    if "campus_accounts" in raw and isinstance(raw["campus_accounts"], list):
        return False
    return any(k in raw for k in LEGACY_CAMPUS_KEYS)


def _next_id(items, prefix):
    """生成下一个递增 ID，如 campus-1, campus-2, ..."""
    max_num = 0
    for item in items:
        item_id = str(item.get("id", ""))
        if item_id.startswith(prefix + "-"):
            try:
                num = int(item_id[len(prefix) + 1 :])
                if num > max_num:
                    max_num = num
            except ValueError:
                pass
    return "%s-%d" % (prefix, max_num + 1)


def _make_campus_label(account):
    """为校园网账号生成默认显示名"""
    label = str(account.get("label", "")).strip()
    if label:
        return label
    user_id = str(account.get("user_id", "")).strip()
    operator = str(account.get("operator", "")).strip()
    if user_id and operator and operator != "xn":
        return "%s@%s" % (user_id, operator)
    return user_id or "未命名账号"


def _make_hotspot_label(profile):
    """为热点配置生成默认显示名"""
    label = str(profile.get("label", "")).strip()
    if label:
        return label
    return str(profile.get("ssid", "")).strip() or "未命名热点"


def normalize_campus_access_mode(value):
    mode = str(value or "wifi").strip().lower()
    if mode not in ("wifi", "wired"):
        mode = "wifi"
    return mode


def campus_uses_wired(cfg):
    return (
        normalize_campus_access_mode((cfg or {}).get("campus_access_mode")) == "wired"
    )


def _migrate_legacy_config(raw):
    """将旧版扁平配置迁移为新版嵌套结构（内存中）"""
    migrated = {}
    for key in GLOBAL_SCALAR_KEYS:
        if key in raw:
            migrated[key] = str(raw[key])
        elif key in DEFAULTS:
            migrated[key] = (
                str(DEFAULTS[key]) if not isinstance(DEFAULTS[key], list) else ""
            )

    user_id = str(raw.get("user_id", "")).strip()
    campus_account = {
        "id": "campus-1",
        "label": "",
        "access_mode": "wifi",
        "base_url": str(raw.get("base_url", "http://172.17.1.2")).strip(),
        "ac_id": str(raw.get("ac_id", "1")).strip(),
        "user_id": user_id,
        "password": str(raw.get("password", "")).strip(),
        "operator": str(raw.get("operator", "cucc")).strip().lower(),
        "ssid": str(raw.get("campus_ssid", "jxnu_stu")).strip(),
        "bssid": str(raw.get("campus_bssid", "")).strip(),
        "radio": str(raw.get("campus_radio", "")).strip(),
    }
    campus_account["label"] = _make_campus_label(campus_account)
    migrated["campus_accounts"] = [campus_account] if user_id else []

    hotspot_ssid = str(raw.get("hotspot_ssid", "")).strip()
    hotspot_profile = {
        "id": "hotspot-1",
        "label": "",
        "ssid": hotspot_ssid,
        "encryption": str(raw.get("hotspot_encryption", "psk2")).strip().lower(),
        "key": str(raw.get("hotspot_key", "")).strip(),
        "radio": str(raw.get("hotspot_radio", "")).strip(),
    }
    hotspot_profile["label"] = _make_hotspot_label(hotspot_profile)
    migrated["hotspot_profiles"] = [hotspot_profile] if hotspot_ssid else []

    if migrated["campus_accounts"]:
        migrated["active_campus_id"] = "campus-1"
        migrated["default_campus_id"] = "campus-1"
    else:
        migrated["active_campus_id"] = ""
        migrated["default_campus_id"] = ""
    if migrated["hotspot_profiles"]:
        migrated["active_hotspot_id"] = "hotspot-1"
        migrated["default_hotspot_id"] = "hotspot-1"
    else:
        migrated["active_hotspot_id"] = ""
        migrated["default_hotspot_id"] = ""
    return migrated


def _find_item_by_id(items, target_id):
    """在列表中按 id 查找项"""
    for item in items:
        if isinstance(item, dict) and str(item.get("id", "")) == target_id:
            return item
    return None


def get_active_campus_account(cfg):
    """获取当前校园网账号"""
    accounts = cfg.get("campus_accounts", [])
    if not isinstance(accounts, list) or not accounts:
        return {}
    active_id = str(cfg.get("active_campus_id", "")).strip()
    if active_id:
        found = _find_item_by_id(accounts, active_id)
        if found:
            return found
    default_id = str(cfg.get("default_campus_id", "")).strip()
    if default_id:
        found = _find_item_by_id(accounts, default_id)
        if found:
            return found
    return accounts[0]


def get_active_hotspot_profile(cfg):
    """获取当前热点配置"""
    profiles = cfg.get("hotspot_profiles", [])
    if not isinstance(profiles, list) or not profiles:
        return {}
    active_id = str(cfg.get("active_hotspot_id", "")).strip()
    if active_id:
        found = _find_item_by_id(profiles, active_id)
        if found:
            return found
    default_id = str(cfg.get("default_hotspot_id", "")).strip()
    if default_id:
        found = _find_item_by_id(profiles, default_id)
        if found:
            return found
    return profiles[0]


def resolve_active_items(cfg):
    """解析当前运行时使用的校园网账号和热点配置，将关键字段打平到 cfg"""
    campus = get_active_campus_account(cfg)
    hotspot = get_active_hotspot_profile(cfg)

    cfg["user_id"] = str(campus.get("user_id", "")).strip()
    cfg["operator"] = str(campus.get("operator", "cucc")).strip().lower()
    if cfg["operator"] not in OPERATORS:
        cfg["operator"] = "cucc"
    cfg["password"] = str(campus.get("password", "")).strip()
    cfg["base_url"] = (
        str(campus.get("base_url", "http://172.17.1.2")).strip().rstrip("/")
    )
    cfg["campus_access_mode"] = normalize_campus_access_mode(
        campus.get("access_mode", "wifi")
    )
    cfg["ac_id"] = str(campus.get("ac_id", "1")).strip()
    cfg["campus_ssid"] = str(campus.get("ssid", "jxnu_stu")).strip()
    cfg["campus_bssid"] = str(campus.get("bssid", "")).strip()
    cfg["campus_radio"] = str(campus.get("radio", "")).strip()
    cfg["campus_encryption"] = normalize_wifi_encryption(
        str(campus.get("encryption", "none")).strip() or "none"
    )
    cfg["campus_key"] = ""
    cfg["campus_account_label"] = _make_campus_label(campus)

    cfg["hotspot_ssid"] = str(hotspot.get("ssid", "")).strip()
    cfg["hotspot_encryption"] = normalize_wifi_encryption(
        str(hotspot.get("encryption", "psk2")).strip() or "psk2"
    )
    cfg["hotspot_key"] = str(hotspot.get("key", "")).strip()
    cfg["hotspot_radio"] = str(hotspot.get("radio", "")).strip()
    cfg["hotspot_profile_label"] = _make_hotspot_label(hotspot)

    cfg["username"] = ""
    if cfg["user_id"]:
        if cfg["operator"] == "xn":
            cfg["username"] = cfg["user_id"]
        else:
            cfg["username"] = cfg["user_id"] + "@" + cfg["operator"]
    return cfg


def load_config():
    """加载并解析配置，自动兼容旧版格式并迁移"""
    raw = load_json_raw_config()

    if _is_legacy_config(raw):
        raw = _migrate_legacy_config(raw)
        try:
            save_json_raw_config(raw)
            append_log("[JXNU-SRun] 旧版配置已自动迁移为新格式")
        except Exception:
            pass

    cfg = {}
    for key in GLOBAL_SCALAR_KEYS:
        default_val = DEFAULTS.get(key, "")
        if isinstance(default_val, list):
            default_val = ""
        val = raw.get(key)
        if val is None or (isinstance(val, str) and val.strip() == ""):
            cfg[key] = str(default_val)
        else:
            cfg[key] = str(val).strip()

    for key in POINTER_KEYS:
        cfg[key] = str(raw.get(key, "")).strip()

    for key in LIST_KEYS:
        val = raw.get(key)
        cfg[key] = val if isinstance(val, list) else []

    if str(raw.get("retry_cooldown_seconds", "")).strip() == "":
        cfg["retry_cooldown_seconds"] = str(
            raw.get("backoff_initial_duration", cfg.get("retry_cooldown_seconds", "10"))
        ).strip()
    if str(raw.get("retry_max_cooldown_seconds", "")).strip() == "":
        cfg["retry_max_cooldown_seconds"] = str(
            raw.get(
                "backoff_max_duration", cfg.get("retry_max_cooldown_seconds", "600")
            )
        ).strip()

    resolve_active_items(cfg)

    cfg["backoff_max_retries"] = parse_non_negative_int(cfg["backoff_max_retries"], 0)
    cfg["backoff_initial_duration"] = parse_non_negative_float(
        cfg["backoff_initial_duration"], 10.0
    )
    cfg["backoff_max_duration"] = parse_non_negative_float(
        cfg["backoff_max_duration"], 600.0
    )
    cfg["retry_cooldown_seconds"] = parse_non_negative_float(
        cfg["retry_cooldown_seconds"], 10.0
    )
    cfg["retry_max_cooldown_seconds"] = parse_non_negative_float(
        cfg["retry_max_cooldown_seconds"], 600.0
    )
    cfg["switch_ready_timeout_seconds"] = parse_non_negative_int(
        cfg["switch_ready_timeout_seconds"], 12
    )
    cfg["manual_terminal_check_interval_seconds"] = parse_non_negative_int(
        cfg["manual_terminal_check_interval_seconds"], 2
    )
    cfg["backoff_exponent_factor"] = parse_non_negative_float(
        cfg["backoff_exponent_factor"], 1.5
    )
    cfg["backoff_inter_const_factor"] = parse_non_negative_float(
        cfg["backoff_inter_const_factor"], 0.0
    )
    cfg["backoff_outer_const_factor"] = parse_non_negative_float(
        cfg["backoff_outer_const_factor"], 0.0
    )

    mode = str(cfg.get("connectivity_check_mode", "internet")).strip().lower()
    if mode not in ("internet", "portal", "ssid"):
        mode = "internet"
    cfg["connectivity_check_mode"] = mode

    cfg["quiet_start"], cfg["quiet_start_minutes"] = normalize_hhmm(
        cfg["quiet_start"], "00:00"
    )
    cfg["quiet_end"], cfg["quiet_end_minutes"] = normalize_hhmm(
        cfg["quiet_end"], "06:00"
    )

    try:
        interval = int(cfg["interval"])
        cfg["interval"] = interval if interval > 0 else 180
    except ValueError:
        cfg["interval"] = 180
    return cfg


def localize_error(message):
    mapping = {
        "challenge_expire_error": "\u6311\u6218\u7801\u5df2\u8fc7\u671f\uff0c\u8bf7\u91cd\u8bd5\u3002",
        "no_response_data_error": "\u7f51\u5173\u8fd4\u56de\u5f02\u5e38\uff08\u53ef\u80fd\u5df2\u5728\u7ebf\uff09\u3002",
        "login_error": "\u8ba4\u8bc1\u5931\u8d25\u3002",
        "sign_error": "\u7b7e\u540d\u9519\u8bef\uff08\u53c2\u6570\u4e0d\u5339\u914d\uff09\u3002",
        "username_or_password_error": "\u7528\u6237\u540d\u6216\u5bc6\u7801\u9519\u8bef\u3002",
        "ip_already_online_error": "IP \u5df2\u5728\u7ebf\u3002",
        "radius_error": "RADIUS \u8ba4\u8bc1\u5931\u8d25\u3002",
        "unknown response": "\u7f51\u5173\u8fd4\u56de\u672a\u77e5\u54cd\u5e94\u3002",
    }
    text = str(message or "").strip()
    if not text:
        return "\u672a\u77e5\u9519\u8bef"

    lower_text = text.lower()
    for key, localized in mapping.items():
        if lower_text == key or key in lower_text:
            return localized

    return text


def normalize_hhmm(value, default_value):
    text = str(value or "").strip()
    match = re.match(r"^(\d{1,2}):(\d{2})$", text)
    if not match:
        text = default_value
        match = re.match(r"^(\d{1,2}):(\d{2})$", text)

    hour = int(match.group(1))
    minute = int(match.group(2))
    if hour < 0 or hour > 23 or minute < 0 or minute > 59:
        hour, minute = [int(x) for x in default_value.split(":", 1)]

    return "%02d:%02d" % (hour, minute), (hour * 60 + minute)


def is_quiet_hours_now(cfg):
    now = datetime.now(BEIJING_TZ)
    now_minutes = now.hour * 60 + now.minute
    start_minutes = int(cfg.get("quiet_start_minutes", 0))
    end_minutes = int(cfg.get("quiet_end_minutes", 360))

    if start_minutes == end_minutes:
        return False
    if start_minutes < end_minutes:
        return start_minutes <= now_minutes < end_minutes
    return now_minutes >= start_minutes or now_minutes < end_minutes


def quiet_window_label(cfg):
    return "%s-%s" % (cfg.get("quiet_start", "00:00"), cfg.get("quiet_end", "06:00"))


def quiet_hours_enabled(cfg):
    return cfg.get("quiet_hours_enabled") == "1"


def in_quiet_window(cfg):
    return quiet_hours_enabled(cfg) and is_quiet_hours_now(cfg)


def failover_enabled(cfg):
    return cfg.get("failover_enabled") == "1"


def hotspot_failback_enabled(cfg):
    return cfg.get("hotspot_failback_enabled") == "1"


def backoff_enabled(cfg):
    return cfg.get("backoff_enable") == "1"


def get_retry_cooldown_seconds(cfg):
    return max(float(cfg.get("retry_cooldown_seconds", 10.0)), 0.0)


def get_retry_max_cooldown_seconds(cfg):
    value = max(float(cfg.get("retry_max_cooldown_seconds", 600.0)), 0.0)
    return value if value > 0 else 600.0


def get_switch_ready_timeout_seconds(cfg):
    value = int(cfg.get("switch_ready_timeout_seconds", 12))
    return value if value > 0 else 12


def get_manual_terminal_check_interval_seconds(cfg):
    value = int(cfg.get("manual_terminal_check_interval_seconds", 2))
    return value if value > 0 else 2


def connectivity_mode_matches(snapshot, cfg, require_ssid=False):
    mode = str(cfg.get("connectivity_check_mode", "internet")).strip().lower()
    current_ssid = str(snapshot.get("current_ssid", "")).strip()
    target_ssid = str(cfg.get("campus_ssid", "")).strip()
    if campus_uses_wired(cfg):
        require_ssid = False
    ssid_ok = (not require_ssid) or (current_ssid and current_ssid == target_ssid)
    if not ssid_ok:
        return False

    level = str(snapshot.get("connectivity_level", "offline")).strip().lower()
    if mode == "ssid":
        return bool(ssid_ok)
    if mode == "portal":
        return level in ("online", "portal")
    return level == "online"


def calc_backoff_delay_seconds(cfg, failure_index):
    n_val = max(int(failure_index), 1)
    base = get_retry_cooldown_seconds(cfg)
    max_duration = get_retry_max_cooldown_seconds(cfg)
    delay = base * math.pow(2, max(n_val - 1, 0))
    if max_duration > 0:
        delay = min(delay, max_duration)
    return delay


def run_once_safe(cfg):
    try:
        return run_once(cfg)
    except HTTP_EXCEPTIONS as exc:
        return False, "网络错误: " + localize_error(exc)
    except ValueError as exc:
        return False, "响应解析错误: " + localize_error(exc)
    except Exception as exc:
        return False, "错误: " + localize_error(exc)


def run_once_with_retry(cfg, ignore_service_disabled=False):
    ok, message = run_once_safe(cfg)
    if ok:
        return True, message

    append_log("[JXNU-SRun] 首次登录失败: %s" % message)

    if not backoff_enabled(cfg):
        append_log(
            "[JXNU-SRun] 已关闭退避重试，%d 秒后执行一次重试"
            % int(get_retry_cooldown_seconds(cfg))
        )
        time.sleep(get_retry_cooldown_seconds(cfg))
        retry_ok, retry_message = run_once_safe(cfg)
        if retry_ok:
            append_log("[JXNU-SRun] 单次重试成功")
            return True, "重试成功"
        append_log("[JXNU-SRun] 单次重试失败: %s" % retry_message)
        return False, retry_message

    retries = 0
    failures = 1

    while True:
        runtime_cfg = load_config()
        max_retries = int(runtime_cfg.get("backoff_max_retries", 0))

        if runtime_cfg.get("enabled") != "1" and not ignore_service_disabled:
            return False, "服务已禁用，停止重试"
        if not backoff_enabled(runtime_cfg):
            return False, message
        if in_quiet_window(runtime_cfg):
            return False, "进入夜间停用时段，停止重试"
        if max_retries > 0 and retries >= max_retries:
            return False, message

        delay = calc_backoff_delay_seconds(runtime_cfg, failures)
        append_log("[JXNU-SRun] 第 %d 次重试将在 %.1f 秒后执行" % (retries + 1, delay))
        if delay > 0:
            time.sleep(delay)

        retry_ok, retry_message = run_once_safe(runtime_cfg)
        retries += 1
        if retry_ok:
            append_log("[JXNU-SRun] 第 %d 次重试成功" % retries)
            return True, "重试成功（第 %d 次）" % retries

        append_log("[JXNU-SRun] 第 %d 次重试失败: %s" % (retries, retry_message))
        message = retry_message
        failures += 1


def run_once_manual(cfg):
    ok, message = run_once_safe(cfg)
    if ok:
        return True, message
    append_log("[JXNU-SRun] 手动登录阶段失败: %s" % message)
    return False, message


def quiet_connection_state(cfg, urls=None):
    runtime_mode = detect_runtime_mode(cfg)
    if runtime_mode == "hotspot":
        return "热点已连接"

    if not cfg.get("username"):
        return "未连接"

    if urls is None:
        urls = build_urls(cfg["base_url"])

    try:
        online, _ = query_online_status(urls["rad_user_info_api"], cfg["username"])
        return "在线" if online else "未连接"
    except Exception:
        return "未连接"


def _url_encode_component(value):
    safe = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
    out = []
    for b in str(value).encode("utf-8"):
        if b in safe:
            out.append(chr(b))
        elif b == 0x20:
            out.append("+")
        else:
            out.append("%%%02X" % b)
    return "".join(out)


def _urlencode(params):
    parts = []
    for key, value in params.items():
        parts.append(_url_encode_component(key) + "=" + _url_encode_component(value))
    return "&".join(parts)


def extract_host_from_url(url):
    match = re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*://([^/:?#]+)", str(url or ""))
    return match.group(1) if match else ""


def compact_http_error_detail(detail, max_len=180):
    text = re.sub(r"\s+", " ", str(detail or "")).strip()
    if not text:
        return ""
    if len(text) > max_len:
        return text[:max_len] + "..."
    return text


def humanize_http_errors(url, errors):
    host = extract_host_from_url(url) or str(url or "")
    lower = " | ".join([str(e or "") for e in errors]).lower()

    reasons = []
    if ("network unreachable" in lower) or ("no route to host" in lower):
        reasons.append("当前网络到认证网关不通（通常是还没连上校园网）")
    if "operation not permitted" in lower:
        reasons.append("请求被系统策略拦截（可能是防火墙或权限限制）")
    if ("timed out" in lower) or ("timeout" in lower):
        reasons.append("网关响应超时")
    if "connection refused" in lower:
        reasons.append("网关拒绝连接")
    if not reasons:
        reasons.append("与网关通信失败")

    details = []
    for e in errors:
        d = compact_http_error_detail(e)
        if d:
            details.append(d)
    details_text = " | ".join(details[:3]) if details else "无"
    return "无法访问认证网关 %s：%s。技术详情：%s" % (
        host,
        "；".join(reasons),
        details_text,
    )


def resolve_bind_ip(url, cfg):
    host = extract_host_from_url(url)
    bind_ip = get_local_ip_for_target(host) if host else None
    host_ip = pick_valid_ip(host)
    if host_ip:
        try:
            if ipaddress.ip_address(host_ip).is_private:
                sta_section = get_sta_section(cfg)
                if sta_section:
                    sta_net = get_network_interface_from_sta_section(sta_section)
                    if sta_net:
                        sta_ip = get_ipv4_from_network_interface(sta_net)
                        if sta_ip:
                            bind_ip = sta_ip
        except ValueError:
            pass
    return bind_ip


def http_get(url, params=None, timeout=5, bind_ip=None):
    if params:
        query = _urlencode(params)
        url = url + ("&" if "?" in url else "?") + query

    errors = []

    if HAVE_URLLIB:
        try:
            req = urllib_request.Request(url, headers=HEADER, method="GET")
            with urllib_request.urlopen(req, timeout=timeout) as resp:
                return resp.read().decode("utf-8", errors="replace")
        except Exception as exc:
            errors.append("urllib: %s" % str(exc))

    if bind_ip is None:
        host = extract_host_from_url(url)
        bind_ip = get_local_ip_for_target(host) if host else None

    candidates = [
        ("/usr/bin/wget", "wget"),
        ("/bin/wget", "wget"),
        ("/bin/uclient-fetch", "uclient-fetch"),
        ("/usr/bin/uclient-fetch", "uclient-fetch"),
    ]

    available = False
    for path, kind in candidates:
        if not os.path.exists(path):
            continue
        available = True

        if kind == "wget":
            cmd = [path, "-q", "-O", "-", "--timeout=%d" % int(timeout)]
            if bind_ip:
                cmd.append("--bind-address=%s" % bind_ip)
            cmd.append(url)
        else:
            cmd = [path, "-q", "-O", "-", "--timeout", str(int(timeout)), url]

        try:
            output = subprocess.check_output(cmd, stderr=subprocess.STDOUT)
            return output.decode("utf-8", errors="replace")
        except subprocess.CalledProcessError as exc:
            details = exc.output.decode("utf-8", errors="replace") if exc.output else ""
            if not details:
                details = "exit status %s" % getattr(exc, "returncode", "unknown")
            errors.append("%s: %s" % (kind, details.strip()))
        except OSError as exc:
            errors.append("%s: %s" % (kind, str(exc)))

    if not available:
        raise RuntimeError("未找到可用 HTTP 客户端（uclient-fetch/wget）")

    raise RuntimeError(humanize_http_errors(url, [e for e in errors if e]))


def parse_jsonp(text):
    wrapped = re.search(r"^[^(]*\((.*)\)\s*$", text, re.S)
    payload = wrapped.group(1) if wrapped else text
    return json.loads(payload)


def pick_valid_ip(*values):
    for value in values:
        candidate = str(value or "").strip()
        if not candidate:
            continue
        try:
            return str(ipaddress.ip_address(candidate))
        except ValueError:
            continue
    return None


def extract_ip_from_text(text):
    patterns = [
        r'id=["\']user_ip["\']\s+value=["\'](.*?)["\']',
        r"\buser_ip\s*=\s*[\"\'](.*?)[\"\']",
        r"\bclient_ip\s*=\s*[\"\'](.*?)[\"\']",
        r'"user_ip"\s*:\s*"(.*?)"',
        r'"online_ip"\s*:\s*"(.*?)"',
    ]
    for pattern in patterns:
        match = re.search(pattern, text)
        if not match:
            continue
        candidate = match.group(1).strip()
        try:
            return str(ipaddress.ip_address(candidate))
        except ValueError:
            continue
    return None


def get_local_ip_for_target(target_host):
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            sock.connect((target_host, 80))
            return sock.getsockname()[0]
        finally:
            sock.close()
    except OSError:
        return None


def get_ipv4_from_network_interface(iface_name):
    if not iface_name:
        return None

    ok, out = run_cmd(["ubus", "call", "network.interface.%s" % iface_name, "status"])
    if ok and out:
        try:
            data = json.loads(out)
            ipv4_list = data.get("ipv4-address") or data.get("ipv4_address") or []
            if isinstance(ipv4_list, list):
                for item in ipv4_list:
                    if isinstance(item, dict):
                        addr = pick_valid_ip(item.get("address"))
                        if addr:
                            return addr
        except Exception:
            pass

    dev = iface_name
    if ok and out:
        try:
            data = json.loads(out)
            dev = data.get("l3_device") or data.get("device") or dev
        except Exception:
            pass

    ok2, out2 = run_cmd(["ip", "-4", "-o", "addr", "show", "dev", dev])
    if ok2 and out2:
        match = re.search(r"\binet\s+(\d+\.\d+\.\d+\.\d+)/", out2)
        if match:
            return match.group(1)

    return None


def wait_for_network_interface_ipv4(iface_name, timeout_seconds=12, interval_seconds=1):
    deadline = time.time() + max(int(timeout_seconds), 1)
    while time.time() < deadline:
        ip = get_ipv4_from_network_interface(iface_name)
        if ip:
            return ip
        time.sleep(max(int(interval_seconds), 1))
    return None


def append_log(line):
    timestamp = datetime.now(BEIJING_TZ).strftime("%Y-%m-%d %H:%M:%S")
    log_line = "[%s] %s" % (timestamp, str(line).strip())
    print(log_line, flush=True)

    try:
        if os.path.exists(LOG_FILE) and os.path.getsize(LOG_FILE) > LOG_MAX_BYTES:
            with open(LOG_FILE, "r", encoding="utf-8", errors="replace") as rf:
                content = rf.read()
            keep = content[-(LOG_MAX_BYTES // 2) :]
            with open(LOG_FILE, "w", encoding="utf-8") as wf:
                wf.write(keep)

        with open(LOG_FILE, "a", encoding="utf-8") as af:
            af.write(log_line + "\n")
    except OSError:
        pass


def run_cmd(cmd):
    try:
        res = subprocess.run(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True
        )
        return res.returncode == 0, (res.stdout or res.stderr or "").strip()
    except OSError as exc:
        return False, str(exc)


def parse_uci_value(raw):
    text = str(raw or "").strip()
    if len(text) >= 2 and text[0] == text[-1] and text[0] in ('"', "'"):
        return text[1:-1]
    return text


def parse_wireless_iface_data():
    ok, out = run_cmd(["uci", "show", "wireless"])
    if not ok or not out:
        return {}

    data = {}
    for line in out.splitlines():
        m = re.match(r"^wireless\.([^.]+)\.([^.=]+)=(.+)$", line.strip())
        if not m:
            continue
        sec, opt, val = m.groups()
        if opt not in (
            "ssid",
            "bssid",
            "mode",
            "network",
            "disabled",
            "encryption",
            "key",
            "device",
            "jxnu_auto",
        ):
            continue
        data.setdefault(sec, {})[opt] = parse_uci_value(val)
    return data


def split_network_value(value):
    return [x for x in str(value or "").split() if x]


def normalize_wifi_encryption(value):
    enc = str(value or "").strip().lower()
    if enc in ("", "none", "open", "nopass"):
        return "none"
    return enc


def wifi_key_required(encryption):
    return normalize_wifi_encryption(encryption) != "none"


def get_sta_sections(wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    sections = []
    for sec, opts in data.items():
        if str(opts.get("mode", "")).strip().lower() == "sta":
            sections.append(sec)
    return sorted(sections)


def get_sta_section(cfg=None, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    sections = get_sta_sections(data)
    preferred = str((cfg or {}).get("sta_iface", "")).strip()
    if preferred and preferred in sections:
        return preferred
    return sections[0] if sections else None


def get_enabled_sta_sections(wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    sections = []
    for sec in get_sta_sections(data):
        if str(data.get(sec, {}).get("disabled", "0")).strip() != "1":
            sections.append(sec)
    return sections


def get_active_sta_section(cfg=None, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    enabled = get_enabled_sta_sections(data)
    for sec in enabled:
        net = get_network_interface_from_sta_section(sec, data)
        ip = get_ipv4_from_network_interface(net) if net else None
        if ip:
            return sec
    if enabled:
        preferred = str((cfg or {}).get("sta_iface", "")).strip()
        if preferred and preferred in enabled:
            return preferred
        return enabled[0]
    return get_sta_section(cfg, data)


def get_runtime_sta_section(cfg=None, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    active = get_active_sta_section(cfg, data)
    if active:
        return active

    known_ssids = []
    hotspot_ssid = str((cfg or {}).get("hotspot_ssid", "")).strip()
    campus_ssid = str((cfg or {}).get("campus_ssid", "")).strip()
    if hotspot_ssid:
        known_ssids.append(hotspot_ssid)
    if campus_ssid:
        known_ssids.append(campus_ssid)

    for sec in get_enabled_sta_sections(data):
        profile = get_sta_profile_from_section(sec, data)
        if str(profile.get("ssid", "")).strip() in known_ssids:
            return sec

    for sec in get_sta_sections(data):
        profile = get_sta_profile_from_section(sec, data)
        if str(profile.get("ssid", "")).strip() in known_ssids:
            return sec

    enabled = get_enabled_sta_sections(data)
    if enabled:
        return enabled[0]
    sections = get_sta_sections(data)
    return sections[0] if sections else None


def detect_runtime_mode(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    section = get_runtime_sta_section(cfg, data)
    profile = get_sta_profile_from_section(section, data) if section else {}
    ssid = str(profile.get("ssid", "")).strip()
    if ssid and ssid == str(cfg.get("hotspot_ssid", "")).strip():
        return "hotspot"
    if campus_uses_wired(cfg) and get_ipv4_from_network_interface("wan"):
        return "campus"
    if not section:
        return "unknown"
    if ssid and ssid == str(cfg.get("campus_ssid", "")).strip():
        return "campus"
    return "unknown"


def get_network_interface_from_sta_section(section, wireless_data=None):
    sec = str(section or "").strip()
    if not sec:
        return None

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    nets = split_network_value(data.get(sec, {}).get("network", ""))
    return nets[0] if nets else None


def get_sta_profile_from_section(section, wireless_data=None):
    sec = str(section or "").strip()
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    opts = data.get(sec, {})
    return {
        "ssid": str(opts.get("ssid", "")).strip(),
        "bssid": str(opts.get("bssid", "")).strip().lower(),
        "encryption": normalize_wifi_encryption(opts.get("encryption", "none")),
        "key": str(opts.get("key", "")).strip(),
    }


def parse_radio_bands():
    ok, out = run_cmd(["uci", "show", "wireless"])
    if not ok or not out:
        return {}
    bands = {}
    for line in out.splitlines():
        m = re.match(r"^wireless\.(radio\d+)\.(band|hwmode)=(.+)$", line.strip())
        if not m:
            continue
        radio, opt, val = m.groups()
        val = parse_uci_value(val).lower()
        if opt == "band":
            bands[radio] = val
        elif opt == "hwmode" and radio not in bands:
            if "a" in val:
                bands[radio] = "5g"
            else:
                bands[radio] = "2g"
    return bands


def get_available_wifi_radios(wireless_data=None):
    radios = []
    bands = parse_radio_bands()
    for radio in sorted(bands.keys(), reverse=True):
        radios.append(radio)

    if radios:
        return radios

    ok, out = run_cmd(["uci", "show", "wireless"])
    if not ok or not out:
        return []

    seen = set()
    for line in out.splitlines():
        match = re.match(r"^wireless\.(radio\d+)\.=wifi-device$", line.strip())
        if not match:
            continue
        radio = match.group(1)
        if radio not in seen:
            radios.append(radio)
            seen.add(radio)
    return radios


def band_label(band):
    labels = {"2g": "2.4GHz", "5g": "5GHz", "6g": "6GHz"}
    return labels.get(str(band or "").lower(), str(band or "?"))


def get_radio_for_section(section, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    return str(data.get(str(section or ""), {}).get("device", "")).strip() or None


def find_sta_on_radio(radio, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    target = str(radio or "").strip()
    for sec in sorted(data.keys()):
        opts = data[sec]
        if (
            str(opts.get("mode", "")).strip().lower() == "sta"
            and str(opts.get("device", "")).strip() == target
        ):
            return sec
    return None


def find_managed_sta_on_radio(cfg, radio, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    managed = set(get_managed_sta_sections(cfg, data))
    target = str(radio or "").strip()
    for sec in sorted(data.keys()):
        opts = data[sec]
        if sec not in managed:
            continue
        if (
            str(opts.get("mode", "")).strip().lower() == "sta"
            and str(opts.get("device", "")).strip() == target
        ):
            return sec
    return None


def is_anonymous_section_name(section):
    sec = str(section or "").strip()
    return bool(re.match(r"^cfg[0-9a-fA-F]+$", sec))


def make_managed_sta_section_name(radio, index=0):
    base = "jxnu_sta_%s" % re.sub(r"[^a-zA-Z0-9_]+", "_", str(radio or "sta"))
    if index <= 0:
        return base
    return "%s_%d" % (base, index)


def rename_wireless_section(old_section, new_section):
    old_sec = str(old_section or "").strip()
    new_sec = str(new_section or "").strip()
    if not old_sec or not new_sec or old_sec == new_sec:
        return True, ""
    return run_cmd(["uci", "rename", "wireless.%s=%s" % (old_sec, new_sec)])


def get_managed_sta_sections(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    managed = []
    preferred = str((cfg or {}).get("sta_iface", "")).strip()
    known_ssids = set()
    known_ssids.add(str((cfg or {}).get("campus_ssid", "")).strip())
    known_ssids.add(str((cfg or {}).get("hotspot_ssid", "")).strip())

    for item in list((cfg or {}).get("campus_accounts", []) or []):
        if isinstance(item, dict):
            known_ssids.add(str(item.get("ssid", "")).strip())

    for item in list((cfg or {}).get("hotspot_profiles", []) or []):
        if isinstance(item, dict):
            known_ssids.add(str(item.get("ssid", "")).strip())

    known_ssids.discard("")

    for sec in sorted(data.keys()):
        opts = data[sec]
        if str(opts.get("mode", "")).strip().lower() != "sta":
            continue
        ssid = str(opts.get("ssid", "")).strip()
        if (
            sec == preferred
            or str(opts.get("jxnu_auto", "")).strip() == "1"
            or (ssid and ssid in known_ssids)
        ):
            managed.append(sec)
    return managed


def ensure_named_managed_sta_sections(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    managed = get_managed_sta_sections(cfg, data)
    renamed = []

    for sec in managed:
        if not is_anonymous_section_name(sec):
            continue

        radio = get_radio_for_section(sec, data) or "sta"
        target = make_managed_sta_section_name(radio)
        suffix = 0
        while target in data and target != sec:
            suffix += 1
            target = make_managed_sta_section_name(radio, suffix)

        ok, msg = rename_wireless_section(sec, target)
        if not ok:
            return False, msg or ("重命名无线接口节 %s 失败" % sec)

        data[target] = data.pop(sec)
        renamed.append((sec, target))

    return True, renamed


def create_sta_on_radio(radio, network_name, profile):
    ok, out = run_cmd(["uci", "add", "wireless", "wifi-iface"])
    if not ok or not out:
        return None, "uci add wifi-iface 失败"
    section = out.strip()

    if is_anonymous_section_name(section):
        target = make_managed_sta_section_name(radio)
        ok, existing = run_cmd(["uci", "show", "wireless.%s" % target])
        if ok and existing:
            suffix = 1
            while True:
                candidate = make_managed_sta_section_name(radio, suffix)
                c_ok, c_existing = run_cmd(["uci", "show", "wireless.%s" % candidate])
                if not c_ok or not c_existing:
                    target = candidate
                    break
                suffix += 1
        ok, msg = rename_wireless_section(section, target)
        if ok:
            section = target
        else:
            return None, msg or ("重命名无线接口节 %s 失败" % section)

    ssid = str(profile.get("ssid", "")).strip()
    bssid = str(profile.get("bssid", "")).strip().lower()
    encryption = normalize_wifi_encryption(profile.get("encryption", "none"))
    key = str(profile.get("key", "")).strip()

    cmds = [
        ["uci", "set", "wireless.%s.device=%s" % (section, radio)],
        ["uci", "set", "wireless.%s.mode=sta" % section],
        ["uci", "set", "wireless.%s.network=%s" % (section, network_name)],
        ["uci", "set", "wireless.%s.ssid=%s" % (section, ssid)],
        ["uci", "set", "wireless.%s.encryption=%s" % (section, encryption)],
        ["uci", "set", "wireless.%s.jxnu_auto=1" % section],
        ["uci", "set", "wireless.%s.disabled=0" % section],
    ]
    if wifi_key_required(encryption) and key:
        cmds.append(["uci", "set", "wireless.%s.key=%s" % (section, key)])

    msgs = []
    for cmd in cmds:
        c_ok, c_msg = run_cmd(cmd)
        if not c_ok and c_msg:
            msgs.append(c_msg)

    if bssid:
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.bssid=%s" % (section, bssid)])
        if not c_ok and c_msg:
            msgs.append(c_msg)
    else:
        run_cmd(["uci", "-q", "delete", "wireless.%s.bssid" % section])

    if msgs:
        return section, "；".join(msgs)
    return section, ""


def ensure_network_interface(name="wwan"):
    iface = str(name or "wwan").strip() or "wwan"
    ok, out = run_cmd(["uci", "-q", "get", "network.%s" % iface])
    if ok and out:
        proto_ok, proto_out = run_cmd(["uci", "-q", "get", "network.%s.proto" % iface])
        if proto_ok and str(proto_out or "").strip():
            return True, iface, ""

    msgs = []
    for cmd in [
        ["uci", "set", "network.%s=interface" % iface],
        ["uci", "set", "network.%s.proto=dhcp" % iface],
    ]:
        c_ok, c_msg = run_cmd(cmd)
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

    commit_ok, commit_msg = run_cmd(["uci", "commit", "network"])
    if (not commit_ok) and commit_msg:
        msgs.append(commit_msg)

    reload_ok, reload_msg = run_cmd(["/etc/init.d/network", "reload"])
    if (not reload_ok) and reload_msg:
        msgs.append(reload_msg)

    if msgs:
        return False, iface, "；".join(msgs)
    return True, iface, "已自动创建网络接口 %s" % iface


def choose_fallback_radio(cfg, expect_hotspot, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()

    explicit = str(
        (cfg or {}).get("hotspot_radio" if expect_hotspot else "campus_radio", "")
    ).strip()
    if explicit:
        return explicit

    active = get_active_sta_section(cfg, data)
    active_radio = get_radio_for_section(active, data) if active else ""
    if active_radio:
        return active_radio

    target = build_expected_profile(cfg, expect_hotspot)
    existing = _find_sta_by_profile(target, data)
    existing_radio = get_radio_for_section(existing, data) if existing else ""
    if existing_radio:
        return existing_radio

    available = get_available_wifi_radios(data)
    if not available:
        return ""
    if "radio1" in available:
        return "radio1"
    if "radio0" in available:
        return "radio0"
    return available[0]


def ensure_runtime_wireless_prerequisites(cfg, expect_hotspot, wireless_data=None):
    if (not expect_hotspot) and campus_uses_wired(cfg):
        data = (
            wireless_data if wireless_data is not None else parse_wireless_iface_data()
        )
        wan_ip = get_ipv4_from_network_interface("wan")
        if wan_ip:
            return True, "检测到有线校园网入口（wan=%s）" % wan_ip, data
        return (
            False,
            "当前校园网账号已设为有线接入模式，但 WAN 口还没有可用 IPv4。",
            data,
        )

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    radios = get_available_wifi_radios(data)
    if not radios:
        return False, "当前路由器未发现可用无线射频，请先确认无线功能已启用。", data

    target = build_expected_profile(cfg, expect_hotspot)
    if not target.get("ssid"):
        return False, "%s SSID 未配置。" % target["label"], data
    if wifi_key_required(target.get("encryption", "none")) and not target.get("key"):
        return False, "%s 需要密码，但当前配置为空。" % target["label"], data

    ok, _, message = ensure_network_interface("wwan")
    data = parse_wireless_iface_data()
    if not ok:
        return (
            False,
            "已尝试自动创建网络接口 wwan，但失败：%s" % (message or "未知错误"),
            data,
        )
    return True, message, data


def activate_sta_section(cfg, enable_sec, wireless_data=None):
    sec = str(enable_sec or "").strip()
    if not sec:
        return False, "未找到要启用的 STA 接口节。"

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    managed = get_managed_sta_sections(cfg, data)
    if sec not in managed:
        managed.append(sec)

    msgs = []
    ok = True
    for item in sorted(set(managed)):
        want_disabled = "0" if item == sec else "1"
        c_ok, c_msg = run_cmd(
            ["uci", "set", "wireless.%s.disabled=%s" % (item, want_disabled)]
        )
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

    ok2, msg2 = commit_reload_wireless()
    ok = ok and ok2
    if msg2:
        msgs.append(msg2)
    return ok, "；".join([x for x in msgs if x])


def commit_reload_wireless():
    ok1, msg1 = run_cmd(["uci", "commit", "wireless"])
    ok2, msg2 = run_cmd(["wifi", "reload"])
    if ok1 and ok2:
        return True, ""
    return False, "\uff1b".join([x for x in [msg1, msg2] if x])


def _set_sta_profile_uci(section, profile):
    sec = str(section or "").strip()
    if not sec:
        return False, "未配置 STA 接口节。"

    ssid = str(profile.get("ssid", "")).strip()
    bssid = str(profile.get("bssid", "")).strip().lower()
    encryption = normalize_wifi_encryption(profile.get("encryption", "none"))
    key = str(profile.get("key", "")).strip()

    if not ssid:
        return False, "目标 SSID 为空。"
    if wifi_key_required(encryption) and not key:
        return False, "目标 SSID 需要密码，但未配置 key。"

    msgs = []
    ok = True

    for arg in [
        "wireless.%s.disabled=0" % sec,
        "wireless.%s.ssid=%s" % (sec, ssid),
        "wireless.%s.encryption=%s" % (sec, encryption),
        "wireless.%s.jxnu_auto=1" % sec,
    ]:
        c_ok, c_msg = run_cmd(["uci", "set", arg])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

    if bssid:
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.bssid=%s" % (sec, bssid)])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)
    else:
        run_cmd(["uci", "-q", "delete", "wireless.%s.bssid" % sec])

    if wifi_key_required(encryption):
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.key=%s" % (sec, key)])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)
    else:
        run_cmd(["uci", "-q", "delete", "wireless.%s.key" % sec])

    return ok, "；".join([x for x in msgs if x])


def apply_sta_profile(cfg, section, profile, wireless_data=None):
    ok, msg = _set_sta_profile_uci(section, profile)
    if not ok:
        return ok, msg
    ok2, msg2 = activate_sta_section(cfg, section, wireless_data)
    if not ok2:
        parts = [x for x in [msg, msg2] if x]
        return False, "；".join(parts)
    return True, msg


def build_expected_profile(cfg, expect_hotspot):
    prefix = "hotspot" if expect_hotspot else "campus"
    return {
        "access_mode": "wifi"
        if expect_hotspot
        else normalize_campus_access_mode(cfg.get("campus_access_mode", "wifi")),
        "ssid": str(cfg.get(prefix + "_ssid", "")).strip(),
        "bssid": str(cfg.get(prefix + "_bssid", "")).strip().lower(),
        "encryption": normalize_wifi_encryption(
            cfg.get(prefix + "_encryption", "none")
        ),
        "key": str(cfg.get(prefix + "_key", "")).strip(),
        "label": "\u70ed\u70b9" if expect_hotspot else "\u6821\u56ed\u7f51",
    }


def profiles_match(current, expected):
    if str(current.get("ssid", "")).strip() != str(expected.get("ssid", "")).strip():
        return False

    expected_bssid = str(expected.get("bssid", "")).strip().lower()
    current_bssid = str(current.get("bssid", "")).strip().lower()
    if expected_bssid and current_bssid != expected_bssid:
        return False

    current_enc = normalize_wifi_encryption(current.get("encryption", "none"))
    expected_enc = normalize_wifi_encryption(expected.get("encryption", "none"))
    if current_enc != expected_enc:
        return False

    if wifi_key_required(expected_enc):
        return (
            str(current.get("key", "")).strip() == str(expected.get("key", "")).strip()
        )
    return True


def _find_sta_by_ssid(ssid, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    target = str(ssid or "").strip()
    if not target:
        return None
    for sec in sorted(data.keys()):
        opts = data[sec]
        if str(opts.get("mode", "")).strip().lower() != "sta":
            continue
        if str(opts.get("ssid", "")).strip() == target:
            return sec
    return None


def _find_sta_by_profile(profile, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    target_ssid = str((profile or {}).get("ssid", "")).strip()
    target_bssid = str((profile or {}).get("bssid", "")).strip().lower()
    if not target_ssid:
        return None
    for sec in sorted(data.keys()):
        opts = data[sec]
        if str(opts.get("mode", "")).strip().lower() != "sta":
            continue
        if str(opts.get("ssid", "")).strip() != target_ssid:
            continue
        if target_bssid and str(opts.get("bssid", "")).strip().lower() != target_bssid:
            continue
        return sec
    return None


def get_preferred_profile_radio(cfg, expect_hotspot, wireless_data=None):
    key = "hotspot_radio" if expect_hotspot else "campus_radio"
    radio = str((cfg or {}).get(key, "")).strip()
    if not radio:
        return choose_fallback_radio(cfg, expect_hotspot, wireless_data)

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    bands = parse_radio_bands()
    if radio in bands:
        return radio

    devices = set()
    for opts in data.values():
        device = str(opts.get("device", "")).strip()
        if device:
            devices.add(device)
    if radio in devices:
        return radio
    return choose_fallback_radio(cfg, expect_hotspot, wireless_data)


def get_preferred_hotspot_radio(cfg, wireless_data=None):
    return get_preferred_profile_radio(cfg, True, wireless_data)


def select_sta_section(cfg, expect_hotspot, base_section, target, wireless_data):
    existing = _find_sta_by_profile(target, wireless_data)
    preferred_radio = get_preferred_profile_radio(cfg, expect_hotspot, wireless_data)
    if not preferred_radio:
        return existing or base_section, "未找到合适的无线频段，已尝试自动选择但失败。"

    if existing and get_radio_for_section(existing, wireless_data) == preferred_radio:
        return existing, ""

    radio_section = find_managed_sta_on_radio(cfg, preferred_radio, wireless_data)
    if radio_section:
        return radio_section, ""

    network_name = (
        get_network_interface_from_sta_section(base_section, wireless_data) or "wwan"
    )
    created, create_msg = create_sta_on_radio(preferred_radio, network_name, target)
    if not created:
        return (
            None,
            create_msg
            or (
                "当前没有可用的无线接口，且未能在 %s 上自动创建 STA 接口节"
                % preferred_radio
            ),
        )
    return created, create_msg


def switch_sta_profile(cfg, expect_hotspot):
    cfg, _, _ = apply_default_selection_for_runtime(expect_hotspot, "执行无线切换前")
    data = parse_wireless_iface_data()
    ready_ok, ready_msg, data = ensure_runtime_wireless_prerequisites(
        cfg, expect_hotspot, data
    )
    if not ready_ok:
        return False, ready_msg
    named_ok, named_result = ensure_named_managed_sta_sections(cfg, data)
    if not named_ok:
        return False, named_result or "整理无线接口节名称失败。"
    if named_result:
        data = parse_wireless_iface_data()

    base_section = get_sta_section(cfg, data)
    target = build_expected_profile(cfg, expect_hotspot)

    section, select_msg = select_sta_section(
        cfg, expect_hotspot, base_section, target, data
    )
    if not section:
        return (
            False,
            select_msg
            or "当前路由器还没有可用于连接目标网络的无线接口，且自动创建失败。",
        )

    data_after_select = parse_wireless_iface_data()
    ok, msg = apply_sta_profile(cfg, section, target, data_after_select)
    if (not ok) and msg:
        return False, msg
    if not ok:
        return False, "写入无线配置失败。"

    settle_delay = min(
        max(get_switch_ready_timeout_seconds(cfg), 1), SWITCH_DELAY_SECONDS
    )
    if settle_delay > 0:
        time.sleep(settle_delay)

    refreshed_data = parse_wireless_iface_data()
    radio = get_radio_for_section(section, refreshed_data)
    bands = parse_radio_bands()
    bl = band_label(bands.get(radio, ""))

    if not expect_hotspot:
        _, ip = wait_for_sta_ipv4(
            section, timeout_seconds=get_switch_ready_timeout_seconds(cfg)
        )
        if ip:
            portal_ok, portal_detail = _test_portal_reachability(cfg)
            if portal_ok:
                conn_hint = "网关可达"
            else:
                conn_hint = "网关不可达"
                if portal_detail:
                    conn_hint = conn_hint + ": " + portal_detail
            append_log(
                "[JXNU-SRun] 校园网切换完成 (%s %s, %s)" % (radio or "?", bl, conn_hint)
            )
            hint = "已切换为%s配置（%s %s, %s）" % (
                target["label"],
                radio or "?",
                bl,
                conn_hint,
            )
            if select_msg:
                hint = hint + "；" + select_msg
            return True, hint
        append_log("[JXNU-SRun] 校园网切换后未获取到 IPv4 (%s %s)" % (radio or "?", bl))
        return False, "已切换为%s配置但未获取到IPv4地址（%s %s）" % (
            target["label"],
            radio or "?",
            bl,
        )

    _, ip = wait_for_sta_ipv4(
        section, timeout_seconds=get_switch_ready_timeout_seconds(cfg)
    )
    if ip:
        dns_ok, _ = test_internet_connectivity()
        conn_hint = "连通" if dns_ok else "不通"
        if (not dns_ok) and hotspot_failback_enabled(cfg):
            append_log("[JXNU-SRun] 热点切换后未确认互联网连通，开始自动回切校园网。")
            rollback_ok, rollback_msg = switch_to_campus(cfg)
            if rollback_ok:
                return False, "热点未确认连通，已自动回切校园网：%s" % (
                    rollback_msg or "回切成功"
                )
            return False, "热点未确认连通，自动回切校园网失败：%s" % (
                rollback_msg or "未知错误"
            )
        hint = "已切换为%s配置（%s %s, %s）" % (
            target["label"],
            radio or "?",
            bl,
            conn_hint,
        )
        if select_msg:
            hint = hint + "；" + select_msg
        return True, hint

    if hotspot_failback_enabled(cfg):
        append_log("[JXNU-SRun] 热点切换后未获取到 IPv4，开始自动回切校园网。")
        rollback_ok, rollback_msg = switch_to_campus(cfg)
        if rollback_ok:
            return False, "热点未获取到 IPv4，已自动回切校园网：%s" % (
                rollback_msg or "回切成功"
            )
        return False, "热点未获取到 IPv4，自动回切校园网失败：%s" % (
            rollback_msg or "未知错误"
        )

    if get_preferred_hotspot_radio(cfg, refreshed_data):
        return False, "已切换为%s配置但未获取到IPv4地址（%s %s）" % (
            target["label"],
            radio or "?",
            bl,
        )
    return (
        False,
        "已切换为%s配置但未获取到IPv4地址（%s %s）。如果热点在另一频段，请在 LuCI 中手动指定热点 radio。"
        % (target["label"], radio or "?", bl),
    )


def switch_to_hotspot(cfg):
    return switch_sta_profile(cfg, expect_hotspot=True)


def switch_to_campus(cfg):
    if campus_uses_wired(cfg):
        disable_managed_sta_sections(cfg, parse_wireless_iface_data())
        wan_ip = wait_for_network_interface_ipv4(
            "wan", timeout_seconds=get_switch_ready_timeout_seconds(cfg)
        )
        if wan_ip:
            return True, "已切换为有线校园网模式（wan, %s）" % wan_ip
        return False, "已切到有线校园网模式，但 WAN 口暂未获取到 IPv4 地址"
    return switch_sta_profile(cfg, expect_hotspot=False)


def wait_for_sta_ipv4(
    section, timeout_seconds=SSID_READY_TIMEOUT_SECONDS, interval_seconds=1
):
    sec = str(section or "").strip()
    deadline = time.time() + max(int(timeout_seconds), 1)
    last_net = get_network_interface_from_sta_section(sec)

    while time.time() < deadline:
        net = get_network_interface_from_sta_section(sec)
        if net:
            last_net = net
            ip = get_ipv4_from_network_interface(net)
            if ip:
                return net, ip
        time.sleep(max(int(interval_seconds), 1))

    return last_net, None


CONNECTIVITY_CHECK_URLS = [
    "http://connect.rom.miui.com/generate_204",
    "http://wifi.vivo.com.cn/generate_204",
]


def test_internet_connectivity(timeout=5):
    for url in CONNECTIVITY_CHECK_URLS:
        try:
            body = http_get(url, timeout=timeout)
            if len(str(body or "")) < 64:
                return True, ""
            return False, "疑似被重定向到认证页面"
        except Exception:
            continue
    return False, "无法访问连通性检测服务器"


def _test_portal_reachability(cfg, timeout=3):
    base_url = str(cfg.get("base_url", "")).strip()
    if not base_url:
        return False, "认证网关地址未配置"
    try:
        http_get(base_url, timeout=timeout)
        return True, ""
    except Exception as exc:
        detail = str(exc)
        if len(detail) > 120:
            detail = detail[:120] + "..."
        return False, detail


def ensure_expected_profile(cfg, expect_hotspot, last_switch_ts=0):
    if not failover_enabled(cfg):
        return True, "", last_switch_ts

    if (not expect_hotspot) and campus_uses_wired(cfg):
        wan_ip = get_ipv4_from_network_interface("wan")
        if wan_ip:
            return True, "", last_switch_ts
        return False, "有线校园网未就绪，WAN 口尚未获取到 IPv4。", last_switch_ts

    data = parse_wireless_iface_data()
    section = get_sta_section(cfg, data)
    if not section:
        return False, "未找到可用的 STA 接口节。", last_switch_ts

    expected = build_expected_profile(cfg, expect_hotspot)
    if not expected["ssid"]:
        return False, "%s SSID 未配置。" % expected["label"], last_switch_ts
    if wifi_key_required(expected["encryption"]) and not expected["key"]:
        return False, "%s 配置缺少密码。" % expected["label"], last_switch_ts

    existing = _find_sta_by_profile(expected, data)
    check = existing if existing else section

    current = get_sta_profile_from_section(check, data)
    active_section = get_active_sta_section(cfg, data)
    check_enabled = str(data.get(check, {}).get("disabled", "0")).strip() != "1"
    _, ip_now = wait_for_sta_ipv4(check, timeout_seconds=1, interval_seconds=1)
    if (
        profiles_match(current, expected)
        and ip_now
        and check_enabled
        and active_section == check
    ):
        return True, "", last_switch_ts

    now = time.time()
    if last_switch_ts and (now - last_switch_ts) < SSID_EXPECTED_RETRY_SECONDS:
        return False, "%s未就绪，等待后重试切换。" % expected["label"], last_switch_ts

    switched, sw_msg = switch_sta_profile(cfg, expect_hotspot)
    switched_at = now

    if not switched:
        detail = sw_msg or "切换命令执行失败"
        return (
            False,
            "%s未就绪，自动切换失败: %s" % (expected["label"], detail),
            switched_at,
        )

    note = "%s未就绪，已自动切换到期望配置。" % expected["label"]
    if sw_msg:
        note = note + " " + sw_msg
    return True, note, switched_at


def get_md5(password, token):
    return hmac.new(token.encode(), password.encode(), hashlib.md5).hexdigest()


def get_sha1(value):
    return hashlib.sha1(value.encode()).hexdigest()


def _getbyte(value, idx):
    ch = ord(value[idx])
    if ch > 255:
        raise ValueError("INVALID_CHARACTER_ERR")
    return ch


def get_base64(value):
    b10 = 0
    output = []
    imax = len(value) - len(value) % 3
    if len(value) == 0:
        return value

    for idx in range(0, imax, 3):
        b10 = (
            (_getbyte(value, idx) << 16)
            | (_getbyte(value, idx + 1) << 8)
            | _getbyte(value, idx + 2)
        )
        output.append(ALPHA[(b10 >> 18)])
        output.append(ALPHA[((b10 >> 12) & 63)])
        output.append(ALPHA[((b10 >> 6) & 63)])
        output.append(ALPHA[(b10 & 63)])

    idx = imax
    remain = len(value) - imax
    if remain == 0:
        return "".join(output)
    if remain == 1:
        b10 = _getbyte(value, idx) << 16
        output.append(
            ALPHA[(b10 >> 18)] + ALPHA[((b10 >> 12) & 63)] + PAD_CHAR + PAD_CHAR
        )
    else:
        b10 = (_getbyte(value, idx) << 16) | (_getbyte(value, idx + 1) << 8)
        output.append(
            ALPHA[(b10 >> 18)]
            + ALPHA[((b10 >> 12) & 63)]
            + ALPHA[((b10 >> 6) & 63)]
            + PAD_CHAR
        )
    return "".join(output)


def ordat(msg, idx):
    if len(msg) > idx:
        return ord(msg[idx])
    return 0


def sencode(msg, key):
    length = len(msg)
    pwd = []
    for i in range(0, length, 4):
        pwd.append(
            ordat(msg, i)
            | ordat(msg, i + 1) << 8
            | ordat(msg, i + 2) << 16
            | ordat(msg, i + 3) << 24
        )
    if key:
        pwd.append(length)
    return pwd


def lencode(msg, key):
    length = len(msg)
    ll = (length - 1) << 2
    if key:
        m_val = msg[length - 1]
        if m_val < ll - 3 or m_val > ll:
            return None
        ll = m_val
    for i in range(0, length):
        msg[i] = (
            chr(msg[i] & 0xFF)
            + chr(msg[i] >> 8 & 0xFF)
            + chr(msg[i] >> 16 & 0xFF)
            + chr(msg[i] >> 24 & 0xFF)
        )
    if key:
        return "".join(msg)[0:ll]
    return "".join(msg)


def get_xencode(msg, key):
    if msg == "":
        return ""
    pwd = sencode(msg, True)
    pwdk = sencode(key, False)
    if len(pwdk) < 4:
        pwdk = pwdk + [0] * (4 - len(pwdk))

    n_val = len(pwd) - 1
    z_val = pwd[n_val]
    c_val = 0x86014019 | 0x183639A0
    q_val = math.floor(6 + 52 / (n_val + 1))
    d_val = 0

    while 0 < q_val:
        d_val = d_val + c_val & (0x8CE0D9BF | 0x731F2640)
        e_val = d_val >> 2 & 3
        p_val = 0
        while p_val < n_val:
            y_val = pwd[p_val + 1]
            m_val = z_val >> 5 ^ y_val << 2
            m_val = m_val + ((y_val >> 3 ^ z_val << 4) ^ (d_val ^ y_val))
            m_val = m_val + (pwdk[(p_val & 3) ^ e_val] ^ z_val)
            pwd[p_val] = pwd[p_val] + m_val & (0xEFB8D130 | 0x10472ECF)
            z_val = pwd[p_val]
            p_val = p_val + 1
        y_val = pwd[0]
        m_val = z_val >> 5 ^ y_val << 2
        m_val = m_val + ((y_val >> 3 ^ z_val << 4) ^ (d_val ^ y_val))
        m_val = m_val + (pwdk[(p_val & 3) ^ e_val] ^ z_val)
        pwd[n_val] = pwd[n_val] + m_val & (0xBB390742 | 0x44C6F8BD)
        z_val = pwd[n_val]
        q_val = q_val - 1
    return lencode(pwd, False)


def get_info(username, password, ip, ac_id, enc):
    info_temp = {
        "username": username,
        "password": password,
        "ip": ip,
        "acid": ac_id,
        "enc_ver": enc,
    }
    return json.dumps(info_temp, separators=(",", ":"))


def get_chksum(token, username, hmd5, ac_id, ip, n_value, type_value, i_value):
    chkstr = token + username
    chkstr += token + hmd5
    chkstr += token + ac_id
    chkstr += token + ip
    chkstr += token + n_value
    chkstr += token + type_value
    chkstr += token + i_value
    return chkstr


def build_urls(base_url):
    return {
        "init_url": base_url,
        "get_challenge_api": base_url + "/cgi-bin/get_challenge",
        "srun_portal_api": base_url + "/cgi-bin/srun_portal",
        "rad_user_info_api": base_url + "/cgi-bin/rad_user_info",
        "rad_user_dm_api": base_url + "/cgi-bin/rad_user_dm",
    }


def get_logout_username(cfg):
    user_id = str(cfg.get("user_id", "")).strip()
    if user_id:
        return user_id
    return str(cfg.get("username", "")).split("@", 1)[0].strip()


def get_logout_sign(now_ts, username, ip, unbind="1"):
    stamp = str(int(now_ts))
    payload = stamp + str(username or "") + str(ip or "") + str(unbind) + stamp
    return get_sha1(payload)


def wait_for_logout_status(
    rad_user_info_api, cfg, bind_ip=None, attempts=3, delay_seconds=1
):
    attempts = max(int(attempts), 1)
    last_message = ""
    for idx in range(attempts):
        online, message = query_online_status(
            rad_user_info_api, cfg["username"], bind_ip=bind_ip
        )
        last_message = message
        if not online:
            return True, message
        if idx + 1 < attempts and delay_seconds > 0:
            time.sleep(delay_seconds)
    return False, last_message or "在线"


def disable_managed_sta_sections(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    managed = get_managed_sta_sections(cfg, data)
    if not managed:
        return True, ""

    msgs = []
    ok = True
    for sec in sorted(set(managed)):
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.disabled=1" % sec])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

    ok2, msg2 = commit_reload_wireless()
    ok = ok and ok2
    if msg2:
        msgs.append(msg2)
    return ok, "；".join([x for x in msgs if x])


def wait_for_manual_login_ready(cfg, attempts=5, delay_seconds=2):
    attempts = max(int(attempts), 1)
    last_message = ""
    ready_label = get_manual_terminal_check_label(cfg)
    wired_mode = campus_uses_wired(cfg)
    urls = build_urls(cfg["base_url"])
    bind_ip = resolve_bind_ip(urls["init_url"], cfg)
    for idx in range(attempts):
        append_log(
            "[JXNU-SRun] 正在执行手动登录终态校验：第%d次检查连通性。" % (idx + 1)
        )
        snapshot = build_runtime_snapshot(cfg)
        ssid_ok = wired_mode or snapshot.get("current_ssid") == cfg.get("campus_ssid")
        bssid_expect = str(cfg.get("campus_bssid", "")).strip().lower()
        current_bssid = str(snapshot.get("current_bssid", "")).strip().lower()
        bssid_ok = wired_mode or (
            (not bssid_expect) or (not current_bssid) or current_bssid == bssid_expect
        )
        online_ok = connectivity_mode_matches(snapshot, cfg, require_ssid=True)
        auth_online = False
        auth_message = ""
        try:
            auth_online, auth_message = query_online_status(
                urls["rad_user_info_api"], cfg["username"], bind_ip=bind_ip
            )
        except Exception as exc:
            auth_online = False
            auth_message = localize_error(exc)

        if wired_mode and auth_online:
            return True, "已切到有线校园网并确认认证在线"
        if ssid_ok and bssid_ok and online_ok:
            if wired_mode:
                return True, "已切到有线校园网并确认%s" % ready_label
            return True, "已关联目标校园网并确认%s" % ready_label
        if (not wired_mode) and ssid_ok and bssid_ok and auth_online:
            return True, "已关联目标校园网并确认认证在线"
        if ssid_ok and online_ok and bssid_expect and not current_bssid:
            return (
                True,
                "已关联目标校园网并确认%s（BSSID 暂未上报，忽略本次终态校验阻塞）"
                % ready_label,
            )
        last_message = "当前 SSID=%s BSSID=%s 连通性=%s" % (
            snapshot.get("current_ssid", "") or "-",
            current_bssid or "-",
            snapshot.get("connectivity", "未知") or "未知",
        )
        if auth_message:
            last_message = last_message + "；认证状态=%s" % auth_message
        if idx + 1 < attempts:
            time.sleep(max(int(delay_seconds), 1))
    return False, last_message


def wait_for_manual_logout_ready(
    rad_user_info_api, cfg, bind_ip=None, attempts=5, delay_seconds=2
):
    attempts = max(int(attempts), 1)
    last_message = ""
    for idx in range(attempts):
        append_log(
            "[JXNU-SRun] 正在执行手动登出终态校验：第%d次检查连通性。" % (idx + 1)
        )
        online, offline_msg = query_online_status(
            rad_user_info_api, cfg["username"], bind_ip=bind_ip
        )
        if not online:
            internet_ok, internet_msg = test_internet_connectivity(timeout=2)
            if not internet_ok:
                return True, "已确认离线，互联网连通性检查结果=不可达"
            last_message = "离线后互联网仍可达（%s）" % (internet_msg or "可达")
        else:
            last_message = localize_error(offline_msg)

        if idx + 1 < attempts:
            time.sleep(max(int(delay_seconds), 1))

    return False, last_message or "终态校验超时"


def get_manual_terminal_check_attempts(cfg):
    try:
        attempts = int(str(cfg.get("manual_terminal_check_max_attempts", "5")).strip())
        return attempts if attempts > 0 else 5
    except Exception:
        return 5


def get_manual_terminal_check_label(cfg):
    mode = str(cfg.get("connectivity_check_mode", "internet")).strip().lower()
    if mode == "portal":
        return "认证网关可达"
    if mode == "ssid":
        return "已关联目标 SSID"
    return "互联网可达"


def clean_slate_for_manual_login(cfg, online_user=""):
    if campus_uses_wired(cfg):
        if online_user:
            append_log(
                "[JXNU-SRun] 正在执行手动登录预清理：检测到已有在线账号 %s，开始注销。"
                % online_user
            )
            ok, message = run_manual_logout(cfg, override_user_id=online_user)
            if not ok:
                append_log(
                    "[JXNU-SRun] 手动登录预清理失败：注销在线账号失败，返回结果：%s"
                    % message
                )
                return False, message
            append_log("[JXNU-SRun] 手动登录预清理成功：历史在线账号已注销。")

        active_data = parse_wireless_iface_data()
        append_log(
            "[JXNU-SRun] 当前校园网账号使用有线接入模式：开始禁用全部受管 STA 接口，确保后续认证流量走 WAN 口。"
        )
        ok, message = disable_managed_sta_sections(cfg, active_data)
        if not ok:
            append_log(
                "[JXNU-SRun] 手动登录预清理失败：禁用受管 STA 接口失败，返回结果：%s"
                % (message or "未知错误")
            )
            return False, message or "禁用历史 STA 接口失败"

        append_log(
            "[JXNU-SRun] 当前校园网账号使用有线接入模式：跳过无线重建，直接使用 WAN 口继续登录。"
        )
        wan_ip = wait_for_network_interface_ipv4(
            "wan", timeout_seconds=get_switch_ready_timeout_seconds(cfg)
        )
        if not wan_ip:
            return False, "有线校园网模式下，WAN 口尚未获取到 IPv4 地址"
        return True, ""

    active_data = parse_wireless_iface_data()
    active_section = get_active_sta_section(cfg, active_data)
    active_profile = (
        get_sta_profile_from_section(active_section, active_data)
        if active_section
        else {}
    )
    target_profile = build_expected_profile(cfg, expect_hotspot=False)
    target_radio = get_preferred_profile_radio(cfg, False, active_data)
    active_radio = get_radio_for_section(active_section, active_data)

    profile_changed = False
    if not profiles_match(active_profile, target_profile):
        profile_changed = True
    elif target_radio and active_radio and target_radio != active_radio:
        profile_changed = True

    if online_user:
        append_log(
            "[JXNU-SRun] 正在执行手动登录预清理：检测到已有在线账号 %s，开始注销。"
            % online_user
        )
        ok, message = run_manual_logout(cfg, override_user_id=online_user)
        if not ok:
            append_log(
                "[JXNU-SRun] 手动登录预清理失败：注销在线账号失败，返回结果：%s"
                % message
            )
            return False, message
        append_log("[JXNU-SRun] 手动登录预清理成功：历史在线账号已注销。")

    append_log(
        "[JXNU-SRun] 正在执行手动登录预清理：开始禁用全部受管 STA 接口，确保不存在历史连接残留。"
    )
    ok, message = disable_managed_sta_sections(cfg, active_data)
    if not ok:
        append_log(
            "[JXNU-SRun] 手动登录预清理失败：禁用受管 STA 接口失败，返回结果：%s"
            % (message or "未知错误")
        )
        return False, message or "禁用历史 STA 接口失败"

    if online_user or profile_changed:
        append_log(
            "[JXNU-SRun] 手动登录预清理成功：受管 STA 接口已全部禁用，开始重建目标校园网连接。"
        )
        ok2, sw_msg = switch_to_campus(cfg)
        if not ok2:
            append_log(
                "[JXNU-SRun] 手动登录预清理失败：重建校园网连接失败，返回结果：%s"
                % (sw_msg or "未知错误")
            )
            return False, sw_msg or "切换校园网失败"
        append_log("[JXNU-SRun] 手动登录预清理成功：目标校园网无线配置已重建。")

    return True, ""


def prepare_campus_for_login(cfg):
    ok, msg, _ = ensure_expected_profile(cfg, expect_hotspot=False, last_switch_ts=0)
    if ok:
        return True, ""
    return False, msg


def init_getip(init_url, bind_ip=None):
    text = http_get(init_url, timeout=5, bind_ip=bind_ip)
    ip = extract_ip_from_text(text)
    if not ip:
        target_host = init_url.split("://", 1)[-1].split("/", 1)[0]
        ip = get_local_ip_for_target(target_host)
    if not ip:
        raise RuntimeError("无法获取本机登录 IP")
    return ip


def get_token(get_challenge_api, username, ip, bind_ip=None):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery112404953340710317169_" + str(now),
        "username": username,
        "ip": ip,
        "_": now,
    }
    data = parse_jsonp(
        http_get(get_challenge_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    token = data.get("challenge")
    if not token:
        msg = data.get("error_msg") or data.get("error") or "unknown response"
        raise RuntimeError("获取挑战码失败: " + localize_error(msg))
    resolved_ip = pick_valid_ip(data.get("client_ip"), data.get("online_ip"), ip)
    if not resolved_ip:
        raise RuntimeError("获取挑战码失败: 未获得有效客户端 IP")
    return token, resolved_ip


def query_online_status(rad_user_info_api, expected_username, bind_ip=None):
    online, _, message = query_online_identity(
        rad_user_info_api, expected_username, bind_ip
    )
    return online, message


def query_online_identity(rad_user_info_api, expected_username, bind_ip=None):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery112406118340540763985_" + str(now),
        "_": now,
    }
    data = parse_jsonp(
        http_get(rad_user_info_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    if str(data.get("error", "")).lower() != "ok":
        msg = data.get("error_msg") or data.get("error") or "unknown response"
        return False, "", "离线: " + localize_error(msg)

    online_name = str(data.get("user_name", "")).strip()
    expected_main = expected_username.split("@", 1)[0]
    if bool(online_name) and online_name == expected_main:
        return True, online_name, "在线"
    if online_name:
        return True, online_name, "在线账号: %s" % online_name
    return False, "", "离线"


def do_complex_work(cfg, ip, token):
    i_value = get_info(cfg["username"], cfg["password"], ip, cfg["ac_id"], cfg["enc"])
    i_value = "{SRBX1}" + get_base64(get_xencode(i_value, token))
    hmd5 = get_md5(cfg["password"], token)
    chksum = get_sha1(
        get_chksum(
            token,
            cfg["username"],
            hmd5,
            cfg["ac_id"],
            ip,
            cfg["n"],
            cfg["type"],
            i_value,
        )
    )
    return i_value, hmd5, chksum


def login(srun_portal_api, cfg, ip, i_value, hmd5, chksum, bind_ip=None):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery11240645308969735664_" + str(now),
        "action": "login",
        "username": cfg["username"],
        "password": "{MD5}" + hmd5,
        "ac_id": cfg["ac_id"],
        "ip": ip,
        "chksum": chksum,
        "info": i_value,
        "n": cfg["n"],
        "type": cfg["type"],
        "os": "openwrt",
        "name": "openwrt",
        "double_stack": "0",
        "_": now,
    }
    data = parse_jsonp(
        http_get(srun_portal_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    error = str(data.get("error", "")).lower()
    result = str(data.get("res", "")).lower()
    success = error == "ok" or result == "ok"
    message = data.get("error_msg") or data.get("error") or "unknown response"
    return success, str(message)


def logout(rad_user_dm_api, cfg, ip, bind_ip=None):
    now = int(time.time())
    username = get_logout_username(cfg)
    unbind = "1"
    params = {
        "callback": "jQuery11240645308969735664_" + str(now),
        "time": str(now),
        "unbind": unbind,
        "ip": ip,
        "username": username,
        "sign": get_logout_sign(now, username, ip, unbind),
    }
    data = parse_jsonp(
        http_get(rad_user_dm_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    error = str(data.get("error", "")).lower()
    result = str(data.get("res", "")).lower()
    success = error == "ok" or result == "ok"
    message = (
        data.get("error_msg")
        or data.get("error")
        or data.get("res")
        or "unknown response"
    )
    return success, str(message)


def run_once(cfg):
    cfg, _, _ = apply_default_selection_for_runtime(False, "登录前")
    if in_quiet_window(cfg):
        return False, "夜间停用中（北京时间 %s），不执行登录" % quiet_window_label(cfg)

    if not cfg["username"] or not cfg["password"]:
        return False, "请先在 LuCI 页面填写学工号和密码"

    campus_ready, campus_msg = prepare_campus_for_login(cfg)
    if not campus_ready:
        return False, campus_msg

    urls = build_urls(cfg["base_url"])
    bip = resolve_bind_ip(urls["init_url"], cfg)
    ip = init_getip(urls["init_url"], bind_ip=bip)
    token, ip = get_token(urls["get_challenge_api"], cfg["username"], ip, bind_ip=bip)
    i_value, hmd5, chksum = do_complex_work(cfg, ip, token)
    ok, message = login(
        urls["srun_portal_api"], cfg, ip, i_value, hmd5, chksum, bind_ip=bip
    )

    if (not ok) and ("challenge_expire_error" in message.lower()):
        token, ip = get_token(
            urls["get_challenge_api"], cfg["username"], ip, bind_ip=bip
        )
        i_value, hmd5, chksum = do_complex_work(cfg, ip, token)
        ok, message = login(
            urls["srun_portal_api"], cfg, ip, i_value, hmd5, chksum, bind_ip=bip
        )

    if (not ok) and ("no_response_data_error" in message.lower()):
        try:
            online, online_msg = query_online_status(
                urls["rad_user_info_api"], cfg["username"], bind_ip=bip
            )
            if online:
                return True, "已在线"
            return False, online_msg
        except Exception:
            pass

    if ok:
        return True, "登录成功"
    return False, "登录失败: " + localize_error(message)


def run_manual_logout(cfg, override_user_id=None):
    if not cfg["username"]:
        return False, "未配置学工号"

    urls = build_urls(cfg["base_url"])
    bip = resolve_bind_ip(urls["init_url"], cfg)

    try:
        online_now, online_user, _ = query_online_identity(
            urls["rad_user_info_api"], cfg["username"], bind_ip=bip
        )
        logout_user = str(override_user_id or online_user or "").strip()
        if not online_now or not logout_user:
            return True, "已离线"

        logout_cfg = dict(cfg)
        logout_cfg["user_id"] = logout_user
        logout_cfg["username"] = logout_user
        ip = init_getip(urls["init_url"], bind_ip=bip)
        append_log(
            "[JXNU-SRun] 正在执行手动登出：发送注销请求，账号=%s，绑定IP=%s。"
            % (logout_user, ip)
        )
        ok, message = logout(urls["rad_user_dm_api"], logout_cfg, ip, bind_ip=bip)
        if ok:
            append_log(
                "[JXNU-SRun] 手动登出请求已受理：接口返回结果=%s，开始校验离线状态。"
                % message
            )
            max_attempts = get_manual_terminal_check_attempts(cfg)
            interval_seconds = get_manual_terminal_check_interval_seconds(cfg)
            ready_ok, ready_msg = wait_for_manual_logout_ready(
                urls["rad_user_info_api"],
                logout_cfg,
                bind_ip=bip,
                attempts=max_attempts,
                delay_seconds=interval_seconds,
            )
            if ready_ok:
                append_log("[JXNU-SRun] 手动登出成功：%s。" % ready_msg)
                return True, "登出成功"
            append_log(
                "[JXNU-SRun] 手动登出校验失败：达到最大检查次数 %d 次，返回结果=%s。"
                % (max_attempts, ready_msg)
            )
            return False, "登出失败：%s" % ready_msg

        localized = localize_error(message)
        append_log("[JXNU-SRun] 手动登出失败：注销接口返回结果=%s。" % localized)
        try:
            online, online_msg = query_online_status(
                urls["rad_user_info_api"], cfg["username"], bind_ip=bip
            )
            if not online:
                return True, "已离线"
            return False, "登出失败: " + localize_error(online_msg)
        except Exception:
            return False, "登出失败: " + localized
    except Exception as exc:
        return False, "登出失败: " + localize_error(exc)


def run_manual_login(cfg):
    service_guard_enabled = False

    try:
        service_guard_enabled, _ = begin_manual_login_service_guard()
        if service_guard_enabled:
            cfg["enabled"] = "0"
            append_log(
                "[JXNU-SRun] 手动登录保护已启用：检测到自动服务原本开启，当前流程执行期间将临时停用守护逻辑。"
            )

        cfg, _, _ = apply_default_selection_for_runtime(False, "手动登录前")
        urls = build_urls(cfg["base_url"])

        try:
            online_now, online_user, _ = query_online_identity(
                urls["rad_user_info_api"], cfg["username"]
            )
        except Exception:
            online_now, online_user = False, ""

        clean_ok, clean_msg = clean_slate_for_manual_login(
            cfg, online_user if online_now else ""
        )
        if not clean_ok:
            return False, clean_msg

        append_log(
            "[JXNU-SRun] 正在执行手动登录：开始提交认证请求，目标账号=%s。"
            % get_logout_username(cfg)
        )
        login_ok, login_msg = run_once_manual(cfg)
        if login_ok:
            append_log(
                "[JXNU-SRun] 手动登录请求已成功：登录阶段返回结果=%s，开始校验目标接入配置与认证/连通性。"
                % login_msg
            )
            max_attempts = get_manual_terminal_check_attempts(cfg)
            interval_seconds = get_manual_terminal_check_interval_seconds(cfg)
            ready_ok, ready_msg = wait_for_manual_login_ready(
                cfg, attempts=max_attempts, delay_seconds=interval_seconds
            )
            if ready_ok:
                append_log("[JXNU-SRun] 手动登录成功：%s。" % ready_msg)
                return True, "登录成功"
            append_log(
                "[JXNU-SRun] 手动登录校验失败：达到最大检查次数 %d 次，返回结果=%s。"
                % (max_attempts, ready_msg)
            )
            return False, "登录后校验失败：%s" % ready_msg

        append_log("[JXNU-SRun] 手动登录失败：登录阶段返回结果=%s。" % login_msg)
        return False, login_msg
    finally:
        if service_guard_enabled:
            restored, restored_enabled = restore_manual_login_service_guard()
            if restored and restored_enabled == "1":
                append_log(
                    "[JXNU-SRun] 手动登录收尾完成：已恢复自动服务开关到执行前状态。"
                )


def run_status(cfg):
    mode_hint = ""
    if failover_enabled(cfg):
        mode_hint = "（校园网SSID: %s，热点SSID: %s）" % (
            cfg.get("campus_ssid", "jxnu_stu"),
            cfg.get("hotspot_ssid", "未设置"),
        )

    urls = build_urls(cfg["base_url"])

    if in_quiet_window(cfg):
        state = quiet_connection_state(cfg, urls)
        return False, "夜间停用（%s）" % state + mode_hint

    if not cfg["username"]:
        return False, "未配置学工号" + mode_hint

    online, message = query_online_status(urls["rad_user_info_api"], cfg["username"])
    return online, localize_error(message) + mode_hint


def run_quiet_logout(cfg):
    urls = build_urls(cfg["base_url"])

    if cfg.get("force_logout_in_quiet") != "1":
        state = quiet_connection_state(cfg, urls)
        return True, "夜间停用（%s）" % state

    if not cfg["username"]:
        return False, "夜间停用下线失败: 未配置学工号"

    ip = init_getip(urls["init_url"])
    ok, message = logout(urls["rad_user_dm_api"], cfg, ip)
    if ok:
        offline, offline_msg = wait_for_logout_status(urls["rad_user_info_api"], cfg)
        if offline:
            return True, "夜间停用下线成功"
        return (
            False,
            "夜间停用下线失败: 请求已发送，但当前仍在线（%s）"
            % localize_error(offline_msg),
        )
    return False, "夜间停用下线失败: " + localize_error(message)


def run_switch(cfg, expect_hotspot):
    target = build_expected_profile(cfg, expect_hotspot)
    if (not expect_hotspot) and campus_uses_wired(cfg):
        switched, message = switch_to_campus(cfg)
        if switched:
            return True, "切换成功: " + (message or "")
        return False, "切换失败: " + (message or "未知错误")

    if not target["ssid"]:
        return False, "%s SSID 未配置" % target["label"]
    if wifi_key_required(target["encryption"]) and not target["key"]:
        return False, "%s 配置缺少密码" % target["label"]

    switched, message = switch_sta_profile(cfg, expect_hotspot)
    if switched:
        return True, "切换成功: " + (message or "")
    return False, "切换失败: " + (message or "未知错误")


def handle_runtime_action(cfg, state):
    payload = pop_runtime_action()
    action = str(payload.get("action", "")).strip()
    if not action:
        return False, ""

    action_started_at = int(time.time())
    save_runtime_status(
        "正在执行动作: %s" % action,
        state,
        last_action=action,
        last_action_ts=action_started_at,
        action_result="pending",
        pending_action=action,
        action_started_at=action_started_at,
        **build_runtime_snapshot(cfg, state),
    )

    action_map = {
        "switch_hotspot": True,
        "switch_campus": False,
    }
    if action == "manual_login":
        ok, message = run_manual_login(cfg)
        append_log("[JXNU-SRun] 异步动作 %s: %s" % (action, message))
        save_runtime_status(
            message,
            state,
            last_action=action,
            last_action_ts=int(time.time()),
            action_result="ok" if ok else "error",
            action_started_at=0,
            pending_action="",
            **build_runtime_snapshot(cfg, state),
        )
        return True, message

    if action == "manual_logout":
        ok, message = run_manual_logout(cfg)
        append_log("[JXNU-SRun] 异步动作 %s: %s" % (action, message))
        save_runtime_status(
            message,
            state,
            last_action=action,
            last_action_ts=int(time.time()),
            action_result="ok" if ok else "error",
            action_started_at=0,
            pending_action="",
            **build_runtime_snapshot(cfg, state),
        )
        return True, message

    if action not in action_map:
        message = "忽略未知动作: %s" % action
        append_log("[JXNU-SRun] %s" % message)
        save_runtime_status(
            message,
            state,
            last_action=action,
            last_action_ts=int(time.time()),
            action_result="ignored",
            action_started_at=0,
            **build_runtime_snapshot(cfg, state),
        )
        return True, message

    ok, message = run_switch(cfg, expect_hotspot=action_map[action])
    action_result = "ok" if ok else "error"
    target_mode = "hotspot" if action_map[action] else "campus"
    if ok:
        state["current_mode"] = target_mode
        if not action_map[action]:
            state["last_switch_ts"] = 0
    append_log("[JXNU-SRun] 异步动作 %s: %s" % (action, message))
    save_runtime_status(
        message,
        state,
        last_action=action,
        last_action_ts=int(time.time()),
        action_result=action_result,
        action_started_at=0,
        pending_action="",
        **build_runtime_snapshot(cfg, state),
    )
    return True, message


def _make_daemon_state():
    return {
        "was_in_quiet": False,
        "quiet_logout_done": False,
        "current_mode": "campus",
        "was_online": False,
        "last_switch_ts": 0,
    }


def _safe_call(fn, *args, **kwargs):
    try:
        return fn(*args, **kwargs)
    except HTTP_EXCEPTIONS as exc:
        return False, "网络错误: " + localize_error(exc)
    except ValueError as exc:
        return False, "响应解析错误: " + localize_error(exc)
    except Exception as exc:
        return False, "错误: " + localize_error(exc)


def _daemon_tick_quiet(cfg, state, interval):
    mode_msg = ""
    runtime_mode = detect_runtime_mode(cfg)

    if not state["was_in_quiet"]:
        state["quiet_logout_done"] = False

    if state["quiet_logout_done"]:
        conn_state = quiet_connection_state(cfg)
        message = "夜间停用（%s）" % conn_state
    else:
        if runtime_mode == "hotspot":
            state["quiet_logout_done"] = True
            message = "夜间停用（热点已连接）"
        else:
            ok, message = _safe_call(run_quiet_logout, cfg)
            state["quiet_logout_done"] = ok

    if failover_enabled(cfg):
        ssid_ok, ssid_msg, state["last_switch_ts"] = ensure_expected_profile(
            cfg,
            expect_hotspot=True,
            last_switch_ts=state["last_switch_ts"],
        )
        if ssid_ok:
            state["current_mode"] = "hotspot"
        if ssid_msg:
            mode_msg = ssid_msg
        if not ssid_ok:
            state["was_in_quiet"] = True
            state["was_online"] = False
            state["current_mode"] = "hotspot"
            wait_message = "夜间停用（未连接）"
            if message:
                wait_message = wait_message + "；" + message
            if mode_msg:
                wait_message = wait_message + "；" + mode_msg
            return wait_message, min(interval, 60)

    if mode_msg:
        message = message + "；" + mode_msg

    state["was_in_quiet"] = True
    state["was_online"] = False
    return message, min(interval, 60)


def _daemon_tick_active(cfg, state, interval):
    online_interval = interval
    mode_msg = ""

    if state["was_in_quiet"]:
        append_log("[JXNU-SRun] 退出夜间时段，准备切回校园网配置")
        state["quiet_logout_done"] = False
        state["was_in_quiet"] = False
        state["was_online"] = False
        state["last_switch_ts"] = 0
        if failover_enabled(cfg):
            switched, sw_msg = switch_to_campus(cfg)
            state["current_mode"] = "campus" if switched else "hotspot"
            if sw_msg:
                mode_msg = sw_msg

    if failover_enabled(cfg):
        ready_ok, ready_msg, state["last_switch_ts"] = ensure_expected_profile(
            cfg,
            expect_hotspot=False,
            last_switch_ts=state["last_switch_ts"],
        )
        if ready_ok:
            state["current_mode"] = "campus"
            if ready_msg:
                mode_msg = (mode_msg + "；" if mode_msg else "") + ready_msg
        else:
            state["current_mode"] = "hotspot"
            state["was_online"] = False
            message = "校园网配置未就绪"
            if ready_msg:
                message = message + "；" + ready_msg
            return message, min(interval, 30)

    if failover_enabled(cfg) and state["current_mode"] == "hotspot":
        state["was_online"] = False
        message = "已切换到热点SSID，校园网SSID恢复后将自动切回"
        if mode_msg:
            message = message + "；" + mode_msg
        return message, interval

    next_sleep = interval
    try:
        urls = build_urls(cfg["base_url"])
        online_now = False
        status_message = ""
        if cfg["username"]:
            online_now, status_message = query_online_status(
                urls["rad_user_info_api"], cfg["username"]
            )

        if online_now:
            message = "在线，下一次检测间隔 %d 秒" % online_interval
            state["was_online"] = True
            next_sleep = online_interval
        else:
            if state["was_online"]:
                append_log("[JXNU-SRun] 检测到断线，立即开始重连")
            state["was_online"] = False
            ok, message = run_once_with_retry(cfg)
            state["was_online"] = bool(ok)
            if not ok and status_message:
                message = "%s；状态检测结果: %s" % (message, status_message)
    except HTTP_EXCEPTIONS as exc:
        append_log("[JXNU-SRun] 状态检测网络异常，尝试重连")
        state["was_online"] = False
        ok, message = run_once_with_retry(cfg)
        if not ok:
            message = "网络异常: %s；重连结果: %s" % (localize_error(exc), message)
    except ValueError as exc:
        append_log("[JXNU-SRun] 状态检测解析异常，尝试重连")
        state["was_online"] = False
        ok, message = run_once_with_retry(cfg)
        if not ok:
            message = "解析异常: %s；重连结果: %s" % (localize_error(exc), message)
    except Exception as exc:
        append_log("[JXNU-SRun] 状态检测异常，尝试重连")
        state["was_online"] = False
        ok, message = run_once_with_retry(cfg)
        if not ok:
            message = "异常: %s；重连结果: %s" % (localize_error(exc), message)

    if mode_msg:
        message = message + "；" + mode_msg
    return message, next_sleep


def run_daemon():
    reconcile_manual_login_service_guard()
    state = _make_daemon_state()
    save_runtime_status(
        "守护进程已启动",
        state,
        daemon_running=True,
        enabled=True,
        last_action="",
        last_action_ts=0,
        action_result="",
        action_started_at=0,
        pending_action="",
        **build_runtime_snapshot(load_config(), state),
    )

    while True:
        cfg = load_config()
        interval = max(int(cfg["interval"]), 5)

        action_handled, action_message = handle_runtime_action(cfg, state)
        if action_handled:
            time.sleep(1)
            continue

        if cfg["enabled"] != "1":
            state.update(_make_daemon_state())
            save_runtime_status(
                "自动登录服务未启用",
                state,
                daemon_running=True,
                enabled=False,
                **build_runtime_snapshot(cfg, state),
            )
            time.sleep(interval)
            continue

        if in_quiet_window(cfg):
            message, sleep = _daemon_tick_quiet(cfg, state, interval)
        else:
            message, sleep = _daemon_tick_active(cfg, state, interval)

        append_log(("[JXNU-SRun] " + message).strip())
        save_runtime_status(
            message,
            state,
            daemon_running=True,
            enabled=True,
            in_quiet=in_quiet_window(cfg),
            **build_runtime_snapshot(cfg, state),
        )
        time.sleep(sleep)


def main():
    parser = argparse.ArgumentParser(description="JXNU SRun client for OpenWrt")
    parser.add_argument("--daemon", action="store_true", help="run as daemon loop")
    parser.add_argument("--once", action="store_true", help="run login once")
    parser.add_argument("--logout", action="store_true", help="logout current account")
    parser.add_argument("--relogin", action="store_true", help="logout then login once")
    parser.add_argument("--status", action="store_true", help="query online status")
    parser.add_argument(
        "--switch-hotspot", action="store_true", help="switch STA profile to hotspot"
    )
    parser.add_argument(
        "--switch-campus", action="store_true", help="switch STA profile to campus"
    )
    args = parser.parse_args()

    cfg = load_config()

    if args.daemon:
        run_daemon()
        return

    if args.switch_hotspot and args.switch_campus:
        print("参数错误：不能同时指定 --switch-hotspot 和 --switch-campus")
        return

    selected = sum(
        1
        for flag in [
            args.once,
            args.logout,
            args.relogin,
            args.status,
            args.switch_hotspot,
            args.switch_campus,
        ]
        if flag
    )
    if selected > 1:
        print("参数错误：一次只能执行一种操作")
        return

    if args.switch_hotspot:
        _, message = run_switch(cfg, expect_hotspot=True)
        append_log("[JXNU-SRun] 手动切换热点结果: " + message)
        print(message)
        return

    if args.switch_campus:
        _, message = run_switch(cfg, expect_hotspot=False)
        append_log("[JXNU-SRun] 手动切换校园网结果: " + message)
        print(message)
        return

    if args.logout:
        ok, message = run_manual_logout(cfg)
        append_log("[JXNU-SRun] 手动登出结果: " + message)
        print(message)
        return

    if args.relogin:
        ok, message = run_manual_login(cfg)
        append_log("[JXNU-SRun] 手动登录结果: " + message)
        print(message)
        return

    exec_label = "手动登录结果" if args.once else "单次执行结果"

    try:
        if args.status:
            _, message = run_status(cfg)
            print(message)
            return

        if args.once:
            _, message = run_once(cfg)
        else:
            # Legacy no-arg invocation is often used by one-shot auto triggers.
            # Use retry path here to reduce failures during transient network bring-up.
            _, message = run_once_with_retry(cfg)
        append_log("[JXNU-SRun] %s: %s" % (exec_label, message))
        print(message)
    except HTTP_EXCEPTIONS as exc:
        message = "网络错误: " + localize_error(exc)
        append_log("[JXNU-SRun] %s: %s" % (exec_label, message))
        print(message)
    except ValueError as exc:
        message = "响应解析错误: " + localize_error(exc)
        append_log("[JXNU-SRun] %s: %s" % (exec_label, message))
        print(message)
    except Exception as exc:
        message = "错误: " + localize_error(exc)
        append_log("[JXNU-SRun] %s: %s" % (exec_label, message))
        print(message)


if __name__ == "__main__":
    main()
