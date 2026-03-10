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
ONLINE_CHECK_MAX_SECONDS = 60
DISCONNECT_RETRY_DELAY_SECONDS = 3
CAMPUS_FIXED_SSID = "jxnu_stu"
CAMPUS_FIXED_ENCRYPTION = "none"
DEFAULTS_JSON_FILE = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "defaults.json"
)


def _load_defaults():
    try:
        with open(DEFAULTS_JSON_FILE, "r", encoding="utf-8") as f:
            data = json.load(f)
        if isinstance(data, dict):
            return {k: str(v) for k, v in data.items()}
    except Exception:
        pass
    return {
        "enabled": "0",
        "user_id": "",
        "operator": "cucc",
        "password": "",
        "quiet_hours_enabled": "1",
        "quiet_start": "00:00",
        "quiet_end": "06:00",
        "force_logout_in_quiet": "1",
        "developer_mode": "0",
        "failover_enabled": "1",
        "sta_iface": "",
        "campus_ssid": "jxnu_stu",
        "campus_encryption": "none",
        "campus_key": "",
        "hotspot_ssid": "",
        "hotspot_encryption": "psk2",
        "hotspot_key": "",
        "hotspot_radio": "",
        "backoff_enable": "1",
        "backoff_max_retries": "0",
        "backoff_initial_duration": "10",
        "backoff_max_duration": "600",
        "backoff_exponent_factor": "1.5",
        "backoff_inter_const_factor": "0",
        "backoff_outer_const_factor": "0",
        "base_url": "http://172.17.1.2",
        "ac_id": "1",
        "n": "200",
        "type": "1",
        "enc": "srun_bx1",
        "interval": "180",
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
    ensure_json_config_file()
    try:
        with open(JSON_CONFIG_FILE, "r", encoding="utf-8") as rf:
            data = json.load(rf)
        if isinstance(data, dict):
            return {k: str(v) for k, v in data.items() if k in DEFAULTS}
    except Exception:
        pass
    return {}


def save_json_raw_config(raw_cfg):
    payload = {}
    for key, default_value in DEFAULTS.items():
        payload[key] = str(raw_cfg.get(key, default_value))

    ensure_parent_dir(JSON_CONFIG_FILE)
    with open(JSON_CONFIG_FILE, "w", encoding="utf-8") as wf:
        json.dump(payload, wf, ensure_ascii=False, indent=2, sort_keys=True)
        wf.write("\n")


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
    section = get_active_sta_section(cfg, data)
    profile = get_sta_profile_from_section(section, data) if section else {}
    ssid = str(profile.get("ssid", "")).strip()
    net = get_network_interface_from_sta_section(section, data) if section else None
    ip = get_ipv4_from_network_interface(net) if net else None
    previous = load_runtime_state()
    if ssid == str(cfg.get("hotspot_ssid", "")).strip() and ssid:
        mode = "hotspot"
    elif ssid == str(cfg.get("campus_ssid", "")).strip() and ssid:
        mode = "campus"
    else:
        mode = "unknown"

    connectivity = "未连接"
    connectivity_level = "offline"
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

    if mode == "campus":
        mode_label = "校园网模式"
    elif mode == "hotspot":
        mode_label = "热点模式"
    else:
        mode_label = "未知模式"

    return {
        "current_mode": mode,
        "mode": mode,
        "mode_label": mode_label,
        "current_ssid": ssid,
        "current_iface": str(net or ""),
        "current_ip": str(ip or ""),
        "connectivity": connectivity,
        "connectivity_level": connectivity_level,
        "connectivity_checked_at": int(previous.get("connectivity_checked_at", 0) or 0),
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


def load_config():
    raw = load_json_raw_config()
    cfg = {k: str(raw.get(k, v)).strip() for k, v in DEFAULTS.items()}

    cfg["operator"] = cfg["operator"].lower()
    if cfg["operator"] not in OPERATORS:
        cfg["operator"] = "cucc"
    cfg["base_url"] = cfg["base_url"].rstrip("/")
    cfg["campus_ssid"] = CAMPUS_FIXED_SSID
    cfg["campus_encryption"] = CAMPUS_FIXED_ENCRYPTION
    cfg["campus_key"] = ""
    cfg["hotspot_encryption"] = cfg["hotspot_encryption"].lower()
    cfg["hotspot_radio"] = str(cfg.get("hotspot_radio", "")).strip()

    cfg["username"] = ""
    if cfg["user_id"]:
        if cfg["operator"] == "xn":
            cfg["username"] = cfg["user_id"]
        else:
            cfg["username"] = cfg["user_id"] + "@" + cfg["operator"]

    cfg["campus_encryption"] = normalize_wifi_encryption(CAMPUS_FIXED_ENCRYPTION)
    cfg["hotspot_encryption"] = normalize_wifi_encryption(cfg["hotspot_encryption"])
    cfg["backoff_max_retries"] = parse_non_negative_int(cfg["backoff_max_retries"], 0)
    cfg["backoff_initial_duration"] = parse_non_negative_float(
        cfg["backoff_initial_duration"], 10.0
    )
    cfg["backoff_max_duration"] = parse_non_negative_float(
        cfg["backoff_max_duration"], 600.0
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


def backoff_enabled(cfg):
    return cfg.get("backoff_enable") == "1"


def calc_backoff_delay_seconds(cfg, failure_index):
    n_val = max(int(failure_index), 1)
    initial = float(cfg.get("backoff_initial_duration", 10.0))
    max_duration = float(cfg.get("backoff_max_duration", 600.0))
    exponent_factor = float(cfg.get("backoff_exponent_factor", 1.5))
    inter_const_factor = float(cfg.get("backoff_inter_const_factor", 0.0))
    outer_const_factor = float(cfg.get("backoff_outer_const_factor", 0.0))

    core = math.pow(max(n_val + inter_const_factor, 0.0), exponent_factor)
    delay = outer_const_factor + (initial * core)
    if delay < 0:
        delay = 0.0
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


def run_once_with_retry(cfg):
    ok, message = run_once_safe(cfg)
    if ok:
        return True, message

    append_log("[JXNU-SRun] 首次登录失败: %s" % message)

    if not backoff_enabled(cfg):
        append_log(
            "[JXNU-SRun] 已关闭退避重试，%d 秒后执行一次重试"
            % DISCONNECT_RETRY_DELAY_SECONDS
        )
        time.sleep(DISCONNECT_RETRY_DELAY_SECONDS)
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

        if runtime_cfg.get("enabled") != "1":
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


def detect_runtime_mode(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    section = get_active_sta_section(cfg, data)
    if not section:
        return "unknown"
    profile = get_sta_profile_from_section(section, data)
    ssid = str(profile.get("ssid", "")).strip()
    if ssid and ssid == str(cfg.get("hotspot_ssid", "")).strip():
        return "hotspot"
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
    known_ssids = {
        str((cfg or {}).get("campus_ssid", "")).strip(),
        str((cfg or {}).get("hotspot_ssid", "")).strip(),
    }

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

    if msgs:
        return section, "；".join(msgs)
    return section, ""


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
    ]:
        c_ok, c_msg = run_cmd(["uci", "set", arg])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

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
        "ssid": str(cfg.get(prefix + "_ssid", "")).strip(),
        "encryption": normalize_wifi_encryption(
            cfg.get(prefix + "_encryption", "none")
        ),
        "key": str(cfg.get(prefix + "_key", "")).strip(),
        "label": "\u70ed\u70b9" if expect_hotspot else "\u6821\u56ed\u7f51",
    }


def profiles_match(current, expected):
    if str(current.get("ssid", "")).strip() != str(expected.get("ssid", "")).strip():
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


def get_preferred_hotspot_radio(cfg, wireless_data=None):
    radio = str((cfg or {}).get("hotspot_radio", "")).strip()
    if not radio:
        return ""

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    bands = parse_radio_bands()
    if radio in bands:
        return radio

    devices = set()
    for opts in data.values():
        device = str(opts.get("device", "")).strip()
        if device:
            devices.add(device)
    return radio if radio in devices else ""


def select_sta_section(cfg, expect_hotspot, base_section, target, wireless_data):
    existing = _find_sta_by_ssid(target["ssid"], wireless_data)
    if not expect_hotspot:
        return existing or base_section, ""

    preferred_radio = get_preferred_hotspot_radio(cfg, wireless_data)
    if not preferred_radio:
        return existing or base_section, ""

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
        return None, create_msg or ("无法在 %s 上创建 STA 接口节" % preferred_radio)
    return created, create_msg


def switch_sta_profile(cfg, expect_hotspot):
    data = parse_wireless_iface_data()
    named_ok, named_result = ensure_named_managed_sta_sections(cfg, data)
    if not named_ok:
        return False, named_result or "整理无线接口节名称失败。"
    if named_result:
        data = parse_wireless_iface_data()

    base_section = get_sta_section(cfg, data)
    if not base_section:
        return False, "未找到可用的 STA 接口节。"

    target = build_expected_profile(cfg, expect_hotspot)
    if not target["ssid"]:
        return False, "%s SSID 未配置。" % target["label"]

    section, select_msg = select_sta_section(
        cfg, expect_hotspot, base_section, target, data
    )
    if not section:
        return False, select_msg or "未找到可用的 STA 接口节。"

    data_after_select = parse_wireless_iface_data()
    ok, msg = apply_sta_profile(cfg, section, target, data_after_select)
    if (not ok) and msg:
        return False, msg
    if not ok:
        return False, "写入无线配置失败。"

    if int(SWITCH_DELAY_SECONDS) > 0:
        time.sleep(int(SWITCH_DELAY_SECONDS))

    refreshed_data = parse_wireless_iface_data()
    radio = get_radio_for_section(section, refreshed_data)
    bands = parse_radio_bands()
    bl = band_label(bands.get(radio, ""))

    if not expect_hotspot:
        _, ip = wait_for_sta_ipv4(section, timeout_seconds=SSID_READY_TIMEOUT_SECONDS)
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

    _, ip = wait_for_sta_ipv4(section, timeout_seconds=SSID_READY_TIMEOUT_SECONDS)
    if ip:
        dns_ok, _ = test_internet_connectivity()
        conn_hint = "连通" if dns_ok else "不通"
        hint = "已切换为%s配置（%s %s, %s）" % (
            target["label"],
            radio or "?",
            bl,
            conn_hint,
        )
        if select_msg:
            hint = hint + "；" + select_msg
        return True, hint

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

    data = parse_wireless_iface_data()
    section = get_sta_section(cfg, data)
    if not section:
        return False, "未找到可用的 STA 接口节。", last_switch_ts

    expected = build_expected_profile(cfg, expect_hotspot)
    if not expected["ssid"]:
        return False, "%s SSID 未配置。" % expected["label"], last_switch_ts
    if wifi_key_required(expected["encryption"]) and not expected["key"]:
        return False, "%s 配置缺少密码。" % expected["label"], last_switch_ts

    existing = _find_sta_by_ssid(expected["ssid"], data)
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
    }


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
        return False, "离线: " + localize_error(msg)

    online_name = str(data.get("user_name", "")).strip()
    expected_main = expected_username.split("@", 1)[0]
    if bool(online_name) and online_name == expected_main:
        return True, "在线"
    return False, "离线"


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


def logout(srun_portal_api, cfg, ip, bind_ip=None):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery11240645308969735664_" + str(now),
        "action": "logout",
        "username": cfg["username"],
        "ac_id": cfg["ac_id"],
        "ip": ip,
        "_": now,
    }
    data = parse_jsonp(
        http_get(srun_portal_api, params=params, timeout=5, bind_ip=bind_ip)
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


def run_status(cfg):
    mode_hint = ""
    if failover_enabled(cfg):
        mode_hint = "（校园网SSID: %s，热点SSID: %s）" % (
            CAMPUS_FIXED_SSID,
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
    ok, message = logout(urls["srun_portal_api"], cfg, ip)
    if ok:
        return True, "夜间停用下线成功"
    return False, "夜间停用下线失败: " + localize_error(message)


def run_switch(cfg, expect_hotspot):
    target = build_expected_profile(cfg, expect_hotspot)
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

    action_map = {
        "switch_hotspot": True,
        "switch_campus": False,
    }
    if action not in action_map:
        message = "忽略未知动作: %s" % action
        append_log("[JXNU-SRun] %s" % message)
        save_runtime_status(
            message,
            state,
            last_action=action,
            action_result="ignored",
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
        action_result=action_result,
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
    online_interval = min(interval, ONLINE_CHECK_MAX_SECONDS)
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
            if not state["was_online"]:
                message = "检测到在线，下一次检测间隔 %d 秒" % online_interval
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
    state = _make_daemon_state()
    save_runtime_status(
        "守护进程已启动",
        state,
        daemon_running=True,
        enabled=True,
        pending_action="",
        **build_runtime_snapshot(load_config(), state),
    )

    while True:
        cfg = load_config()
        interval = max(int(cfg["interval"]), 5)

        if cfg["enabled"] != "1":
            state.update(_make_daemon_state())
            save_runtime_status(
                "服务未启用",
                state,
                daemon_running=True,
                enabled=False,
                **build_runtime_snapshot(cfg, state),
            )
            time.sleep(interval)
            continue

        action_handled, action_message = handle_runtime_action(cfg, state)
        if action_handled:
            save_runtime_status(
                action_message,
                state,
                daemon_running=True,
                enabled=True,
                in_quiet=in_quiet_window(cfg),
                **build_runtime_snapshot(cfg, state),
            )
            time.sleep(1)
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
