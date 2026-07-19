"""
配置管理 -- 加载/保存/迁移 JSON 配置、默认值、常量、日志、策略查询函数。

不依赖 SRun 协议、无线管理或守护循环。
"""

import json
import os
import re
import time
from contextlib import contextmanager
from datetime import datetime

import logger as _logger

try:
    from school_presets import normalize_base_url
except ImportError:  # pragma: no cover - defensive for partial installs
    def normalize_base_url(value):
        return str(value or "").strip().rstrip("/")

BEIJING_TZ = _logger.BEIJING_TZ
LOG_FILE = _logger.LOG_FILE
LOG_MAX_BYTES = _logger.LOG_MAX_BYTES
LOG_LEVEL_NAMES = _logger.LOG_LEVEL_NAMES
DEFAULT_LOG_LEVEL = _logger.DEFAULT_LOG_LEVEL
append_log = _logger.append_log
clear_log_context = _logger.clear_log_context
get_log_threshold = _logger.get_log_threshold
log = _logger.log
normalize_log_level = _logger.normalize_level
set_log_context = _logger.set_log_context
set_log_threshold = _logger.set_log_threshold
timed = _logger.timed

JSON_CONFIG_FILE = "/usr/lib/smart_srun/config.json"
STATE_FILE = "/var/run/smart_srun/state.json"
ACTION_FILE = "/var/run/smart_srun/action.json"
INFLIGHT_ACTION_FILE = "/var/run/smart_srun/action_inflight.json"
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
    "log_level",
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

def normalize_wifi_encryption(value):
    enc = str(value or "").strip().lower()
    if enc in ("", "none", "open", "nopass"):
        return "none"
    return enc


def normalize_operator_id(value):
    text = str(value or "").strip().lower()
    return "" if text in ("xn", "??") else text


def normalize_operator_suffix(value):
    text = str(value or "").strip()
    # "??" 是学校预设里"运营商后缀未经验证"的哨兵值，绝不能当作真实后缀拼进用户名，
    # 否则会构造出 user@?? 导致登录失败。和历史占位符 "xn" 一样归一化为空。
    return "" if text.lower() in ("xn", "??") else text


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
    except (OSError, TypeError, ValueError):
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
        "switch_ready_timeout_seconds": "30",
        "manual_terminal_check_max_attempts": "5",
        "manual_terminal_check_interval_seconds": "2",
        "hotspot_failback_enabled": "1",
        "connectivity_check_mode": "internet",
        "backoff_exponent_factor": "1.5",
        "backoff_inter_const_factor": "0",
        "backoff_outer_const_factor": "0",
        "interval": "60",
        "developer_mode": "0",
        "log_level": "INFO",
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
DEFAULT_LOGIN_N = str(DEFAULTS.get("n", "200") or "200")
DEFAULT_LOGIN_TYPE = str(DEFAULTS.get("type", "1") or "1")
DEFAULT_LOGIN_ENC = str(DEFAULTS.get("enc", "srun_bx1") or "srun_bx1")
DEFAULT_INFO_PREFIX = "SRBX1"
DEFAULT_DOUBLE_STACK = "0"
DEFAULT_LOGIN_OS = "Windows 10"
DEFAULT_LOGIN_NAME = "Windows"


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
            # 用 lockf（POSIX 记录锁），与 LuCI 侧 nixio 的 lockf 是同一类锁，
            # 才能真正互斥；此前用 flock（BSD 锁）与 nixio 互不阻塞，config.json
            # 的读改写在 Python 与 Lua 之间形同无锁。
            fcntl_mod.lockf(lock_file.fileno(), fcntl_mod.LOCK_EX)
        yield lock_file
    finally:
        if fcntl_mod is not None:
            try:
                fcntl_mod.lockf(lock_file.fileno(), fcntl_mod.LOCK_UN)
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
    except (OSError, TypeError, ValueError):
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
    except (OSError, TypeError, ValueError):
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


# logger helpers are re-exported for existing modules that import them from config.py.


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
            except (TypeError, ValueError):
                errors.append({"key": key, "message": "%s must be an integer." % label})
        elif value_type == "float":
            try:
                float(text)
            except (TypeError, ValueError):
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
        except (TypeError, ValueError):
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
    except (AttributeError, ImportError, LookupError, ValueError):
        return {"short_name": school_key}


# ---------------------------------------------------------------------------
# 手动登录服务保护
# ---------------------------------------------------------------------------


def begin_manual_login_service_guard():
    previous_enabled = get_json_scalar_config("enabled", DEFAULTS.get("enabled", "0"))
    if previous_enabled != "1":
        return False, previous_enabled

    set_json_scalar_config("enabled", "0")

    def _apply(payload):
        payload["manual_service_guard_active"] = True
        payload["manual_service_enabled_before"] = previous_enabled
        payload["updated_at"] = int(time.time())
        return payload

    update_json_file(STATE_FILE, _apply)
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

    def _apply(payload):
        payload["manual_service_guard_active"] = False
        payload["manual_service_enabled_before"] = ""
        payload["updated_at"] = int(time.time())
        return payload

    update_json_file(STATE_FILE, _apply)
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
    # 原子 RMW：在 STATE_FILE 锁内重读磁盘、合并本进程的 state/extra 后写回，
    # 避免与另一进程（如 CLI relogin 写入 manual_service_guard_active）的
    # 无锁读-改-写交错，把对方刚写入的持久字段覆盖丢失。
    def _apply(payload):
        if state:
            payload.update(state)
        payload.update(extra)
        payload["message"] = str(message or "")
        payload["updated_at"] = int(time.time())
        return payload

    update_json_file(STATE_FILE, _apply)


# ---------------------------------------------------------------------------
# Runtime action queue (读写 action.json)
# ---------------------------------------------------------------------------


def queue_runtime_action(action):
    payload = {
        "action": str(action or "").strip(),
        "requested_at": int(time.time()),
    }
    save_json_file(ACTION_FILE, payload)
    log(
        "DEBUG",
        "config_action_queued",
        action=payload["action"],
        requested_at=payload["requested_at"],
    )


def pop_runtime_action():
    # 在 ACTION_FILE 锁内读+删，且仅当解析出 action 时才删除：
    # 配合 LuCI 侧的原子写，避免撕裂/空读时把刚入队的动作连文件一起删掉。
    with _exclusive_file_lock(ACTION_FILE):
        payload = _load_json_file_unlocked(ACTION_FILE)
        if isinstance(payload, dict) and payload.get("action"):
            try:
                os.remove(ACTION_FILE)
            except OSError:
                pass
    if isinstance(payload, dict) and payload.get("action"):
        requested_at = int(payload.get("requested_at") or 0)
        lag_ms = int(max(0, time.time() - requested_at) * 1000) if requested_at else 0
        log(
            "DEBUG",
            "config_action_consumed",
            action=str(payload.get("action", "")),
            requested_at=requested_at,
            lag_ms=lag_ms,
        )
    return payload if isinstance(payload, dict) else {}


def requeue_runtime_action(payload):
    """把中断的动作原样写回队列，保留 requeue_count 等附加字段。

    requested_at 必须刷新：LuCI 侧会把滞留超过一定时长的队列文件当作
    死队列丢弃，沿用旧时间戳会让刚重排的动作立刻被判定为滞留。
    """
    data = dict(payload or {})
    data["action"] = str(data.get("action") or "").strip()
    if not data["action"]:
        return
    data["requested_at"] = int(time.time())
    save_json_file(ACTION_FILE, data)
    log(
        "INFO",
        "action_requeued",
        "requeued interrupted action",
        action=data["action"],
        requeue_count=int(data.get("requeue_count") or 0),
    )


def mark_inflight_action(payload):
    """动作出队后、执行前落盘一个执行中标记；执行结束后清除。

    守护进程若在执行途中被杀（例如再次点击手动动作触发的 service restart），
    重启后可凭该标记把动作重新排队一次，避免动作凭空丢失、界面卡在待执行。
    """
    data = dict(payload or {})
    if not str(data.get("action") or "").strip():
        return
    save_json_file(INFLIGHT_ACTION_FILE, data)


def load_inflight_action():
    payload = load_json_file(INFLIGHT_ACTION_FILE)
    return payload if isinstance(payload, dict) else {}


def clear_inflight_action():
    try:
        os.remove(INFLIGHT_ACTION_FILE)
    except OSError:
        pass


# ---------------------------------------------------------------------------
# 数值解析
# ---------------------------------------------------------------------------


def parse_non_negative_int(value, default_value):
    try:
        parsed = int(str(value).strip())
        return parsed if parsed >= 0 else int(default_value)
    except (TypeError, ValueError):
        return int(default_value)


def parse_non_negative_float(value, default_value):
    try:
        parsed = float(str(value).strip())
        return parsed if parsed >= 0 else float(default_value)
    except (TypeError, ValueError):
        return float(default_value)


def _login_shape_number(value, default_value):
    text = str(value or "").strip()
    return text if text.isdigit() and int(text) >= 0 else str(default_value)


def _login_shape_text(value, default_value):
    text = str(value or "").strip()
    return text or str(default_value)


def _normalize_info_prefix(value, default_value=DEFAULT_INFO_PREFIX):
    text = str(value or "").strip()
    if text.startswith("{") and text.endswith("}") and len(text) > 2:
        text = text[1:-1].strip()
    return text or str(default_value)


def _apply_login_shape(cfg, campus):
    # 账号字段存为空字符串时视同未设置，回落到 legacy 全局值，保持
    # account > legacy global > default 三级优先级。
    cfg["n"] = _login_shape_number(
        campus.get("n") or cfg.get("n", ""),
        DEFAULT_LOGIN_N,
    )
    cfg["type"] = _login_shape_number(
        campus.get("type") or cfg.get("type", ""),
        DEFAULT_LOGIN_TYPE,
    )
    cfg["enc"] = _login_shape_text(
        campus.get("enc") or cfg.get("enc", ""),
        DEFAULT_LOGIN_ENC,
    )
    cfg["info_prefix"] = _normalize_info_prefix(
        campus.get("info_prefix") or cfg.get("info_prefix", ""),
        DEFAULT_INFO_PREFIX,
    )
    cfg["double_stack"] = _login_shape_number(
        campus.get("double_stack") or cfg.get("double_stack", ""),
        DEFAULT_DOUBLE_STACK,
    )
    cfg["login_os"] = _login_shape_text(
        campus.get("login_os") or cfg.get("login_os", ""),
        DEFAULT_LOGIN_OS,
    )
    cfg["login_name"] = _login_shape_text(
        campus.get("login_name") or cfg.get("login_name", ""),
        DEFAULT_LOGIN_NAME,
    )
    return cfg


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
    if suffix and user_id:
        return "%s@%s" % (user_id, suffix)
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
    legacy_operator = normalize_operator_id(raw.get("operator", "cucc"))
    campus_account = {
        "id": "campus-1",
        "label": "",
        "access_mode": "wifi",
        "base_url": normalize_base_url(raw.get("base_url", "http://172.17.1.2")),
        "ac_id": str(raw.get("ac_id", "1")).strip(),
        "user_id": user_id,
        "password": str(raw.get("password", "")).strip(),
        "operator": legacy_operator,
        "operator_suffix": legacy_operator,
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


def resolve_active_items(cfg):
    campus = get_active_campus_account(cfg)
    hotspot = get_active_hotspot_profile(cfg)

    cfg["user_id"] = str(campus.get("user_id", "")).strip()
    cfg["operator"] = normalize_operator_id(campus.get("operator", ""))
    cfg["password"] = str(campus.get("password", "")).strip()
    cfg["base_url"] = normalize_base_url(campus.get("base_url", "http://172.17.1.2"))
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
    cfg["operator_suffix"] = normalize_operator_suffix(
        campus.get("operator_suffix", "")
    )
    cfg["campus_account_label"] = _make_campus_label(campus)
    _apply_login_shape(cfg, campus)

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
            cfg["username"] = cfg["user_id"]
    return cfg


# ---------------------------------------------------------------------------
# load_config -- 主入口
# ---------------------------------------------------------------------------


def _reconcile_selection_pointers(raw):
    """收敛历史遗留的 default/active 选择指针分裂。

    v1.3.4 的“设为默认”只写 default_*_id（提示“手动登录后生效”），active 仍指向
    旧账号；此后所有自动路径（掉线重连、门户注销后的自动重登）按 active 解析，
    会持续用错账号（例如丢失运营商后缀、落到校园网/学生侧）。现行代码所有写入
    点都成对更新两个指针，因此不一致只可能是旧数据，以 default 为准收敛。
    """
    changed = False
    pairs = (
        ("campus_accounts", "default_campus_id", "active_campus_id"),
        ("hotspot_profiles", "default_hotspot_id", "active_hotspot_id"),
    )
    for list_key, default_key, active_key in pairs:
        items = raw.get(list_key)
        if not isinstance(items, list) or not items:
            continue
        default_id = str(raw.get(default_key, "") or "").strip()
        active_id = str(raw.get(active_key, "") or "").strip()
        if not default_id or default_id == active_id:
            continue
        if not _find_item_by_id(items, default_id):
            continue
        raw[active_key] = default_id
        changed = True
        log(
            "INFO",
            "config_legacy_fix",
            "reconciled stale selection pointer to default",
            key=active_key,
            value=default_id,
            previous=active_id,
        )
    return changed


_WARNED_UNKNOWN_SCHOOLS = set()


def _sanitize_school(cfg):
    """未知 school 值就地降级为内置默认，避免守护进程解析运行时时崩溃重启。

    非破坏性：只改内存中的 cfg，不回写配置文件——若 schools/<name>.py 是稍后
    才热部署的，下次 load_config 会自动识别并恢复。同一坏值每进程只告警一次，
    防止每个 tick 刷日志。schools 模块不可用时直接放行，交给守护进程侧
    resolve_runtime_safe 兜底。
    """
    name = str(cfg.get("school", "")).strip()
    if not name or name == "default":
        return
    try:
        import schools

        known = schools.get_school_entry(name) is not None
    except Exception:
        return
    if known:
        return
    if name not in _WARNED_UNKNOWN_SCHOOLS:
        _WARNED_UNKNOWN_SCHOOLS.add(name)
        log(
            "WARN",
            "config_legacy_fix",
            "unknown school in config, using built-in default",
            school=name,
        )
    cfg["school"] = "default"


def load_config():
    raw = load_json_raw_config()

    if _is_legacy_config(raw):
        raw = _migrate_legacy_config(raw)
        try:
            save_json_raw_config(raw)
            log("INFO", "config_migrated", "legacy config migrated to new format")
        except OSError:
            pass

    if _reconcile_selection_pointers(raw):
        try:
            save_json_raw_config(raw)
        except OSError:
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

    _sanitize_school(cfg)

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
        cfg["switch_ready_timeout_seconds"], 30
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

    cfg["log_level"] = set_log_threshold(cfg.get("log_level"))

    log(
        "DEBUG",
        "config_loaded",
        "config loaded",
        school=cfg.get("school", ""),
        enabled=cfg.get("enabled", ""),
        failover_enabled=cfg.get("failover_enabled", ""),
        log_level=cfg.get("log_level", ""),
        interval=cfg.get("interval", ""),
    )

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
    value = int(cfg.get("switch_ready_timeout_seconds", 30))
    return value if value > 0 else 30


def get_manual_terminal_check_interval_seconds(cfg):
    value = int(cfg.get("manual_terminal_check_interval_seconds", 2))
    return value if value > 0 else 2


def get_manual_terminal_check_attempts(cfg):
    try:
        attempts = int(str(cfg.get("manual_terminal_check_max_attempts", "5")).strip())
        return attempts if attempts > 0 else 5
    except (TypeError, ValueError):
        return 5


def get_manual_terminal_check_label(cfg):
    mode = str(cfg.get("connectivity_check_mode", "internet")).strip().lower()
    if mode == "portal":
        return "认证网关可达"
    if mode == "ssid":
        return "已关联目标 SSID"
    return "互联网可达"
