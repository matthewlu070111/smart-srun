"""
配置管理 -- 加载/保存/迁移 JSON 配置、默认值、常量、日志、策略查询函数。

不依赖 SRun 协议、无线管理或守护循环。
"""

import json
import os
import re
import time
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone

BEIJING_TZ = timezone(timedelta(hours=8))
LOG_FILE = "/var/log/smart_srun.log"
JSON_CONFIG_FILE = "/usr/lib/smart_srun/config.json"
STATE_FILE = "/var/run/smart_srun/state.json"
ACTION_FILE = "/var/run/smart_srun/action.json"
LOG_MAX_BYTES = 512 * 1024
CONNECTIVITY_CACHE_SECONDS = 15
SWITCH_DELAY_SECONDS = 2
SSID_READY_TIMEOUT_SECONDS = 12
SSID_EXPECTED_RETRY_SECONDS = 30
DISCONNECT_RETRY_DELAY_SECONDS = 3
DEFAULTS_JSON_FILE = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "defaults.json"
)

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
    "school",
}

POINTER_KEYS = {
    "active_campus_id",
    "default_campus_id",
    "active_hotspot_id",
    "default_hotspot_id",
}

LIST_KEYS = {"campus_accounts", "hotspot_profiles"}

SCHOOL_EXTRA_KEY = "school_extra"

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

OPERATORS = {"cmcc", "ctcc", "cucc", "xn"}


def normalize_wifi_encryption(value):
    enc = str(value or "").strip().lower()
    if enc in ("", "none", "open", "nopass"):
        return "none"
    return enc


def wifi_key_required(encryption):
    return normalize_wifi_encryption(encryption) != "none"


def _load_defaults():
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
        "school": "jxnu",
        "active_campus_id": "",
        "default_campus_id": "",
        "active_hotspot_id": "",
        "default_hotspot_id": "",
        "campus_accounts": [],
        "hotspot_profiles": [],
    }


DEFAULTS = _load_defaults()


# ---------------------------------------------------------------------------
# 文件 I/O 工具
# ---------------------------------------------------------------------------


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


@contextmanager
def _exclusive_file_lock(path):
    lock_path = str(path) + ".lock"
    ensure_parent_dir(lock_path)
    lock_file = open(lock_path, "a+", encoding="utf-8")
    fcntl_mod = None
    try:
        try:
            import fcntl as fcntl_mod
        except ImportError:
            fcntl_mod = None
        if fcntl_mod is not None:
            fcntl_mod.flock(lock_file.fileno(), fcntl_mod.LOCK_EX)
        yield lock_file
    finally:
        if fcntl_mod is not None:
            try:
                fcntl_mod.flock(lock_file.fileno(), fcntl_mod.LOCK_UN)
            except OSError:
                pass
        lock_file.close()


def _load_json_file_unlocked(path, allowed_keys=None):
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


def load_json_file(path, allowed_keys=None):
    return _load_json_file_unlocked(path, allowed_keys=allowed_keys)


def _atomic_save_json_unlocked(path, payload):
    tmp_path = str(path) + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as wf:
        json.dump(payload, wf, ensure_ascii=False, indent=2, sort_keys=True)
        wf.write("\n")
    os.replace(tmp_path, path)


def save_json_file(path, payload):
    ensure_parent_dir(path)
    with _exclusive_file_lock(path):
        _atomic_save_json_unlocked(path, payload)


def update_json_file(path, updater, allowed_keys=None):
    ensure_parent_dir(path)
    with _exclusive_file_lock(path):
        current = _load_json_file_unlocked(path, allowed_keys=allowed_keys)
        result = updater(current)
        payload = result if isinstance(result, dict) else current
        _atomic_save_json_unlocked(path, payload)
        return payload


def load_json_raw_config():
    ensure_json_config_file()
    try:
        with open(JSON_CONFIG_FILE, "r", encoding="utf-8") as rf:
            data = json.load(rf)
        if isinstance(data, dict):
            return data
    except Exception:
        pass
    return {}


def _normalize_json_raw_config(raw_cfg):
    payload = {}
    for key in GLOBAL_SCALAR_KEYS:
        default_val = DEFAULTS.get(key, "")
        payload[key] = str(raw_cfg.get(key, default_val))
    for key in POINTER_KEYS:
        payload[key] = str(raw_cfg.get(key, ""))
    for key in LIST_KEYS:
        val = raw_cfg.get(key)
        payload[key] = val if isinstance(val, list) else []
    payload[SCHOOL_EXTRA_KEY] = _normalize_declared_school_extra(raw_cfg)
    return payload


def save_json_raw_config(raw_cfg):
    save_json_file(JSON_CONFIG_FILE, _normalize_json_raw_config(raw_cfg))


def update_json_raw_config(updater):
    ensure_json_config_file()

    def _apply(raw):
        result = updater(raw)
        next_raw = result if isinstance(result, dict) else raw
        return _normalize_json_raw_config(next_raw)

    return update_json_file(JSON_CONFIG_FILE, _apply)


# ---------------------------------------------------------------------------
# 日志
# ---------------------------------------------------------------------------


def log(level, event, msg="", **ctx):
    """
    Structured log entry.

    level: "INFO" | "WARN" | "ERROR"
    event: english snake_case event key (e.g. "login_success")
    msg:   optional human-readable english summary
    ctx:   key=value context pairs
    """
    timestamp = datetime.now(BEIJING_TZ).strftime("%Y-%m-%d %H:%M:%S")
    parts = [level, event]
    for k, v in ctx.items():
        sv = str(v)
        if " " in sv or not sv:
            sv = '"%s"' % sv.replace('"', '\\"')
        parts.append("%s=%s" % (k, sv))
    if msg:
        parts.append("| %s" % msg)
    _write_log("[%s] %s" % (timestamp, " ".join(parts)))


def append_log(line):
    """Legacy compat wrapper. New code should use log()."""
    _write_log(
        "[%s] %s"
        % (
            datetime.now(BEIJING_TZ).strftime("%Y-%m-%d %H:%M:%S"),
            str(line).strip(),
        )
    )


def _write_log(log_line):
    """Write a pre-formatted log line to stdout and log file with rotation."""
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


# ---------------------------------------------------------------------------
# 配置标量查询 / 修改
# ---------------------------------------------------------------------------


def get_json_scalar_config(key, default_value=""):
    raw = load_json_raw_config()
    value = raw.get(str(key), default_value)
    if value is None:
        value = default_value
    return str(value).strip()


def set_json_scalar_config(key, value):
    def _apply(raw):
        raw[str(key)] = str(value)

    update_json_raw_config(_apply)


def _state_flag_enabled(value):
    return str(value).strip().lower() in ("1", "true", "yes", "on")


def load_school_extra(raw_cfg):
    payload = {}
    if isinstance(raw_cfg, dict):
        payload = raw_cfg.get(SCHOOL_EXTRA_KEY)
    return dict(payload) if isinstance(payload, dict) else {}


def _school_extra_value_text(value):
    if value is None:
        return ""
    if isinstance(value, str):
        return value.strip()
    return str(value).strip()


def _normalize_school_extra_descriptor(descriptor):
    if not isinstance(descriptor, dict):
        return None

    key = str(descriptor.get("key", "")).strip()
    if not key:
        return None

    label = str(descriptor.get("label") or key).strip() or key
    choices = descriptor.get("choices")
    return {
        "default": _coerce_school_extra_value(
            descriptor.get("default", ""),
            {
                "type": str(descriptor.get("type") or "string").strip().lower()
                or "string"
            },
        ),
        "key": key,
        "type": str(descriptor.get("type") or "string").strip().lower() or "string",
        "required": bool(descriptor.get("required", False)),
        "label": label,
        "description": str(descriptor.get("description") or ""),
        "choices": [str(choice) for choice in choices]
        if isinstance(choices, list)
        else [],
        "secret": bool(descriptor.get("secret", False)),
    }


def _normalize_school_extra_descriptors(descriptors):
    items = []
    if not isinstance(descriptors, list):
        return items
    for descriptor in descriptors:
        item = _normalize_school_extra_descriptor(descriptor)
        if item:
            items.append(item)
    return items


def _coerce_school_extra_value(value, descriptor):
    value_type = descriptor.get("type", "string")
    text = _school_extra_value_text(value)

    if value_type == "bool":
        lowered = text.lower()
        if lowered in ("1", "true", "yes", "on"):
            return "1"
        if lowered in ("", "0", "false", "no", "off"):
            return "0"
        return text

    if value_type == "int":
        if text == "":
            return ""
        return str(int(text))

    if value_type == "float":
        if text == "":
            return ""
        return str(float(text))

    return text


def validate_school_extra(raw_cfg, descriptors):
    payload = load_school_extra(raw_cfg)
    errors = []

    for descriptor in _normalize_school_extra_descriptors(descriptors):
        key = descriptor["key"]
        label = descriptor["label"]
        text = _school_extra_value_text(payload.get(key))

        if descriptor["required"] and not text:
            errors.append({"key": key, "message": "%s is required." % label})
            continue

        if not text:
            continue

        choices = descriptor.get("choices", [])
        if choices and text not in choices:
            errors.append(
                {
                    "key": key,
                    "message": "%s must be one of: %s." % (label, ", ".join(choices)),
                }
            )
            continue

        value_type = descriptor.get("type", "string")
        if value_type == "int":
            try:
                int(text)
            except Exception:
                errors.append({"key": key, "message": "%s must be an integer." % label})
        elif value_type == "float":
            try:
                float(text)
            except Exception:
                errors.append({"key": key, "message": "%s must be a number." % label})
        elif value_type == "bool":
            if text.lower() not in (
                "1",
                "0",
                "true",
                "false",
                "yes",
                "no",
                "on",
                "off",
            ):
                errors.append(
                    {"key": key, "message": "%s must be true or false." % label}
                )

    return len(errors) == 0, errors


def normalize_school_extra(raw_cfg, descriptors):
    payload = load_school_extra(raw_cfg)
    if not payload:
        return {}

    ok, _ = validate_school_extra({SCHOOL_EXTRA_KEY: payload}, descriptors)
    if not ok:
        return {}

    normalized = {}
    for descriptor in _normalize_school_extra_descriptors(descriptors):
        key = descriptor["key"]
        if key not in payload:
            continue
        try:
            value = _coerce_school_extra_value(payload.get(key), descriptor)
        except Exception:
            return {}
        if value != "":
            normalized[key] = value
    return normalized


def _get_school_extra_descriptors(cfg):
    metadata = _get_school_metadata(cfg)
    descriptors = metadata.get("school_extra")
    if isinstance(descriptors, list):
        return descriptors
    descriptors = metadata.get("school_extra_descriptors")
    if isinstance(descriptors, list):
        return descriptors
    return []


def _normalize_declared_school_extra(cfg):
    return normalize_school_extra(cfg, _get_school_extra_descriptors(cfg))


def build_school_runtime_luci_contract(cfg, inspection=None):
    raw_inspection = inspection if isinstance(inspection, dict) else {}
    result = dict(raw_inspection)

    capabilities = raw_inspection.get("capabilities")
    if not isinstance(capabilities, list):
        capabilities = raw_inspection.get("declared_capabilities")
    if not isinstance(capabilities, list):
        capabilities = []

    descriptors = raw_inspection.get("field_descriptors")
    if not isinstance(descriptors, list):
        descriptors = raw_inspection.get("school_extra")
    if not isinstance(descriptors, list):
        descriptors = raw_inspection.get("school_extra_descriptors")
    descriptors = (
        _normalize_school_extra_descriptors(descriptors)
        if isinstance(descriptors, list)
        else None
    )

    school_extra = None
    if descriptors is not None:
        school_extra = normalize_school_extra(cfg or {}, descriptors)

    result["runtime_type"] = str(raw_inspection.get("runtime_type") or "unknown")
    result["runtime_api_version"] = raw_inspection.get("runtime_api_version", 1)
    result["capabilities"] = [str(item) for item in capabilities]
    result["field_descriptors"] = descriptors if descriptors is not None else None
    result["school_extra"] = school_extra if school_extra is not None else None
    return result


def _get_school_metadata(cfg):
    school_key = str((cfg or {}).get("school", "jxnu")).strip() or "jxnu"
    try:
        import schools

        metadata = schools.get_school_metadata(school_key)
        if metadata:
            return metadata
        return schools.get_default_school_metadata()
    except Exception:
        return {"short_name": school_key, "no_suffix_operators": ["xn"]}


# ---------------------------------------------------------------------------
# 手动登录服务保护
# ---------------------------------------------------------------------------


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
        log(
            "INFO",
            "config_legacy_fix",
            "restored manual login service guard",
            previous_enabled=previous_enabled,
        )
    return restored


# ---------------------------------------------------------------------------
# Runtime state (读写 state.json)
# ---------------------------------------------------------------------------


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


# ---------------------------------------------------------------------------
# Runtime action queue (读写 action.json)
# ---------------------------------------------------------------------------


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


# ---------------------------------------------------------------------------
# 数值解析
# ---------------------------------------------------------------------------


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


# ---------------------------------------------------------------------------
# 指针 / 列表项操作
# ---------------------------------------------------------------------------


def _find_item_by_id(items, target_id):
    for item in items:
        if isinstance(item, dict) and str(item.get("id", "")) == target_id:
            return item
    return None


def _next_id(items, prefix):
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
    label = str(account.get("label", "")).strip()
    if label:
        return label
    user_id = str(account.get("user_id", "")).strip()
    suffix = str(account.get("operator_suffix", "")).strip()
    operator = str(account.get("operator", "")).strip()
    if suffix and user_id:
        return "%s@%s" % (user_id, suffix)
    if user_id and operator and operator != "xn":
        return "%s@%s" % (user_id, operator)
    return user_id or "未命名账号"


def _make_hotspot_label(profile):
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

    update_json_raw_config(
        lambda current: current.__setitem__(meta["active_key"], default_id)
    )

    suffix = ""
    if reason:
        suffix = "（%s）" % str(reason).strip()
    log(
        "INFO",
        "config_default_applied",
        "applied default %s to runtime" % meta["label"],
        kind=meta["label"],
        old=active_id or "unset",
        new=default_id,
        reason=str(reason or ""),
    )
    return load_config(), True, default_id


# ---------------------------------------------------------------------------
# 旧版配置迁移
# ---------------------------------------------------------------------------


def _is_legacy_config(raw):
    if "campus_accounts" in raw and isinstance(raw["campus_accounts"], list):
        return False
    return any(k in raw for k in LEGACY_CAMPUS_KEYS)


def _migrate_legacy_config(raw):
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
        "operator_suffix": "",
        "ssid": str(raw.get("campus_ssid", "jxnu_stu")).strip(),
        "bssid": str(raw.get("campus_bssid", "")).strip(),
        "radio": str(raw.get("campus_radio", "")).strip(),
    }
    campus_account["label"] = _make_campus_label(campus_account)
    campus_account["encryption"] = normalize_wifi_encryption(
        str(raw.get("campus_encryption", "none")).strip() or "none"
    )
    campus_account["key"] = ""
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
    migrated[SCHOOL_EXTRA_KEY] = {}
    return migrated


# ---------------------------------------------------------------------------
# 活跃账号 / 热点解析
# ---------------------------------------------------------------------------


def get_active_campus_account(cfg):
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


def _get_no_suffix_operators(cfg):
    metadata = _get_school_metadata(cfg)
    return set(metadata.get("no_suffix_operators", []) or ["xn"])


def resolve_active_items(cfg):
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
    cfg["operator_suffix"] = str(campus.get("operator_suffix", "")).strip()
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
        if cfg["operator_suffix"]:
            cfg["username"] = cfg["user_id"] + "@" + cfg["operator_suffix"]
        else:
            no_suffix_ops = _get_no_suffix_operators(cfg)
            if cfg["operator"] in no_suffix_ops:
                cfg["username"] = cfg["user_id"]
            else:
                cfg["username"] = cfg["user_id"] + "@" + cfg["operator"]
    return cfg


# ---------------------------------------------------------------------------
# load_config -- 主入口
# ---------------------------------------------------------------------------


def load_config():
    raw = load_json_raw_config()

    if _is_legacy_config(raw):
        raw = _migrate_legacy_config(raw)
        try:
            save_json_raw_config(raw)
            log("INFO", "config_migrated", "legacy config migrated to new format")
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
    cfg[SCHOOL_EXTRA_KEY] = _normalize_declared_school_extra(raw)

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


# ---------------------------------------------------------------------------
# 错误本地化
# ---------------------------------------------------------------------------


def localize_error(message):
    mapping = {
        "challenge_expire_error": "挑战码已过期，请重试。",
        "no_response_data_error": "网关返回异常（可能已在线）。",
        "login_error": "认证失败。",
        "sign_error": "签名错误（参数不匹配）。",
        "username_or_password_error": "用户名或密码错误。",
        "ip_already_online_error": "IP 已在线。",
        "radius_error": "RADIUS 认证失败。",
        "unknown response": "网关返回未知响应。",
    }
    text = str(message or "").strip()
    if not text:
        return "未知错误"

    lower_text = text.lower()
    for key, localized in mapping.items():
        if lower_text == key or key in lower_text:
            return localized

    return text


# ---------------------------------------------------------------------------
# 时间 / 策略查询
# ---------------------------------------------------------------------------


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
