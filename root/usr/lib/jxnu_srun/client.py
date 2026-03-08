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
LOG_MAX_BYTES = 512 * 1024
SWITCH_DELAY_SECONDS = 2
SSID_READY_TIMEOUT_SECONDS = 12
SSID_EXPECTED_RETRY_SECONDS = 30
ONLINE_CHECK_MAX_SECONDS = 60
DISCONNECT_RETRY_DELAY_SECONDS = 3
CAMPUS_FIXED_SSID = "jxnu_stu"
CAMPUS_FIXED_ENCRYPTION = "none"

DEFAULTS = {
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
    cfg = {k: str(raw.get(k, v)) for k, v in DEFAULTS.items()}
    cfg["enabled"] = str(cfg["enabled"]).strip()
    cfg["user_id"] = str(cfg["user_id"]).strip()
    cfg["operator"] = str(cfg["operator"]).strip().lower()
    cfg["password"] = str(cfg["password"]).strip()
    cfg["quiet_hours_enabled"] = str(cfg["quiet_hours_enabled"]).strip()
    cfg["force_logout_in_quiet"] = str(cfg["force_logout_in_quiet"]).strip()
    cfg["quiet_start"] = str(cfg.get("quiet_start", "")).strip()
    cfg["quiet_end"] = str(cfg.get("quiet_end", "")).strip()
    cfg["failover_enabled"] = str(cfg["failover_enabled"]).strip()

    cfg["sta_iface"] = str(cfg.get("sta_iface", "")).strip()
    cfg["campus_ssid"] = CAMPUS_FIXED_SSID
    cfg["campus_encryption"] = CAMPUS_FIXED_ENCRYPTION
    cfg["campus_key"] = ""
    cfg["hotspot_ssid"] = str(cfg.get("hotspot_ssid", "")).strip()
    cfg["hotspot_encryption"] = str(cfg.get("hotspot_encryption", "psk2")).strip().lower()
    cfg["hotspot_key"] = str(cfg.get("hotspot_key", "")).strip()
    cfg["backoff_enable"] = str(cfg.get("backoff_enable", "1")).strip()
    cfg["backoff_max_retries"] = str(cfg.get("backoff_max_retries", "0")).strip()
    cfg["backoff_initial_duration"] = str(cfg.get("backoff_initial_duration", "10")).strip()
    cfg["backoff_max_duration"] = str(cfg.get("backoff_max_duration", "600")).strip()
    cfg["backoff_exponent_factor"] = str(cfg.get("backoff_exponent_factor", "1.5")).strip()
    cfg["backoff_inter_const_factor"] = str(cfg.get("backoff_inter_const_factor", "0")).strip()
    cfg["backoff_outer_const_factor"] = str(cfg.get("backoff_outer_const_factor", "0")).strip()

    cfg["base_url"] = str(cfg["base_url"]).strip().rstrip("/")
    cfg["ac_id"] = str(cfg["ac_id"]).strip()
    cfg["n"] = str(cfg["n"]).strip()
    cfg["type"] = str(cfg["type"]).strip()
    cfg["enc"] = str(cfg["enc"]).strip()
    cfg["interval"] = str(cfg["interval"]).strip()

    if cfg["operator"] not in OPERATORS:
        cfg["operator"] = "cucc"

    cfg["username"] = ""
    if cfg["user_id"]:
        cfg["username"] = cfg["user_id"] + "@" + cfg["operator"]

    cfg["campus_encryption"] = normalize_wifi_encryption(CAMPUS_FIXED_ENCRYPTION)
    cfg["hotspot_encryption"] = normalize_wifi_encryption(cfg.get("hotspot_encryption", "psk2"))
    cfg["backoff_max_retries"] = parse_non_negative_int(cfg.get("backoff_max_retries", "0"), 0)
    cfg["backoff_initial_duration"] = parse_non_negative_float(cfg.get("backoff_initial_duration", "10"), 10.0)
    cfg["backoff_max_duration"] = parse_non_negative_float(cfg.get("backoff_max_duration", "600"), 600.0)
    cfg["backoff_exponent_factor"] = parse_non_negative_float(cfg.get("backoff_exponent_factor", "1.5"), 1.5)
    cfg["backoff_inter_const_factor"] = parse_non_negative_float(cfg.get("backoff_inter_const_factor", "0"), 0.0)
    cfg["backoff_outer_const_factor"] = parse_non_negative_float(cfg.get("backoff_outer_const_factor", "0"), 0.0)

    cfg["quiet_start"], cfg["quiet_start_minutes"] = normalize_hhmm(cfg.get("quiet_start", ""), "00:00")
    cfg["quiet_end"], cfg["quiet_end_minutes"] = normalize_hhmm(cfg.get("quiet_end", ""), "06:00")

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
    return "无法访问认证网关 %s：%s。技术详情：%s" % (host, "；".join(reasons), details_text)


def http_get(url, params=None, timeout=5):
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

    host = extract_host_from_url(url)
    bind_ip = get_local_ip_for_target(host) if host else None

    # For private portal hosts, prefer the STA profile interface IP.
    host_ip = pick_valid_ip(host)
    if host_ip:
        try:
            if ipaddress.ip_address(host_ip).is_private:
                cfg = load_config()
                sta_section = get_sta_section(cfg)
                if sta_section:
                    sta_net = get_network_interface_from_sta_section(sta_section)
                    if sta_net:
                        sta_ip = get_ipv4_from_network_interface(sta_net)
                        if sta_ip:
                            bind_ip = sta_ip
        except ValueError:
            pass

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
            keep = content[-(LOG_MAX_BYTES // 2):]
            with open(LOG_FILE, "w", encoding="utf-8") as wf:
                wf.write(keep)

        with open(LOG_FILE, "a", encoding="utf-8") as af:
            af.write(log_line + "\n")
    except OSError:
        pass


def run_cmd(cmd):
    try:
        res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        return res.returncode == 0, (res.stdout or res.stderr or "").strip()
    except OSError as exc:
        return False, str(exc)


def parse_uci_value(raw):
    text = str(raw or "").strip()
    if len(text) >= 2 and text[0] == text[-1] and text[0] in ("\"", "'"):
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
        if opt not in ("ssid", "mode", "network", "disabled", "encryption", "key"):
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


def commit_reload_wireless():
    ok1, msg1 = run_cmd(["uci", "commit", "wireless"])
    ok2, msg2 = run_cmd(["wifi", "reload"])
    if ok1 and ok2:
        return True, ""
    return False, "\uFF1B".join([x for x in [msg1, msg2] if x])


def apply_sta_profile(section, profile):
    sec = str(section or "").strip()
    if not sec:
        return False, "\u672A\u914D\u7F6E STA \u63A5\u53E3\u8282\u3002"

    ssid = str(profile.get("ssid", "")).strip()
    encryption = normalize_wifi_encryption(profile.get("encryption", "none"))
    key = str(profile.get("key", "")).strip()

    if not ssid:
        return False, "\u76EE\u6807 SSID \u4E3A\u7A7A\u3002"
    if wifi_key_required(encryption) and not key:
        return False, "\u76EE\u6807 SSID \u9700\u8981\u5BC6\u7801\uFF0C\u4F46\u672A\u914D\u7F6E key\u3002"

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

    ok2, msg2 = commit_reload_wireless()
    ok = ok and ok2
    if msg2:
        msgs.append(msg2)

    return ok, "\uFF1B".join([x for x in msgs if x])


def build_expected_profile(cfg, expect_hotspot):
    prefix = "hotspot" if expect_hotspot else "campus"
    return {
        "ssid": str(cfg.get(prefix + "_ssid", "")).strip(),
        "encryption": normalize_wifi_encryption(cfg.get(prefix + "_encryption", "none")),
        "key": str(cfg.get(prefix + "_key", "")).strip(),
        "label": "\u70ED\u70B9" if expect_hotspot else "\u6821\u56ED\u7F51",
    }


def profiles_match(current, expected):
    if str(current.get("ssid", "")).strip() != str(expected.get("ssid", "")).strip():
        return False

    current_enc = normalize_wifi_encryption(current.get("encryption", "none"))
    expected_enc = normalize_wifi_encryption(expected.get("encryption", "none"))
    if current_enc != expected_enc:
        return False

    if wifi_key_required(expected_enc):
        return str(current.get("key", "")).strip() == str(expected.get("key", "")).strip()
    return True


def switch_sta_profile(cfg, expect_hotspot):
    data = parse_wireless_iface_data()
    section = get_sta_section(cfg, data)
    if not section:
        return False, "\u672A\u627E\u5230\u53EF\u7528\u7684 STA \u63A5\u53E3\u8282\u3002"

    target = build_expected_profile(cfg, expect_hotspot)
    if not target["ssid"]:
        return False, "%s SSID \u672A\u914D\u7F6E\u3002" % target["label"]

    ok, msg = apply_sta_profile(section, target)
    if (not ok) and msg:
        return False, msg
    if not ok:
        return False, "\u5199\u5165\u65E0\u7EBF\u914D\u7F6E\u5931\u8D25\u3002"

    if int(SWITCH_DELAY_SECONDS) > 0:
        time.sleep(int(SWITCH_DELAY_SECONDS))

    return True, "\u5DF2\u5207\u6362\u4E3A%s\u914D\u7F6E\uFF08\u63A5\u53E3\u8282 %s\uFF09" % (target["label"], section)


def switch_to_hotspot(cfg):
    return switch_sta_profile(cfg, expect_hotspot=True)


def switch_to_campus(cfg):
    return switch_sta_profile(cfg, expect_hotspot=False)


def wait_for_sta_ipv4(section, timeout_seconds=SSID_READY_TIMEOUT_SECONDS, interval_seconds=1):
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


def ensure_expected_profile(cfg, expect_hotspot, last_switch_ts=0):
    if not failover_enabled(cfg):
        return True, "", last_switch_ts

    data = parse_wireless_iface_data()
    section = get_sta_section(cfg, data)
    if not section:
        return False, "\u672A\u627E\u5230\u53EF\u7528\u7684 STA \u63A5\u53E3\u8282\u3002", last_switch_ts

    expected = build_expected_profile(cfg, expect_hotspot)
    if not expected["ssid"]:
        return False, "%s SSID \u672A\u914D\u7F6E\u3002" % expected["label"], last_switch_ts
    if wifi_key_required(expected["encryption"]) and not expected["key"]:
        return False, "%s \u914D\u7F6E\u7F3A\u5C11\u5BC6\u7801\u3002" % expected["label"], last_switch_ts

    current = get_sta_profile_from_section(section, data)
    _, ip_now = wait_for_sta_ipv4(section, timeout_seconds=1, interval_seconds=1)
    if profiles_match(current, expected) and ip_now:
        return True, "", last_switch_ts

    now = time.time()
    if last_switch_ts and (now - last_switch_ts) < SSID_EXPECTED_RETRY_SECONDS:
        return False, "%s\u672A\u5C31\u7EEA\uFF0C\u7B49\u5F85\u540E\u91CD\u8BD5\u5207\u6362\u3002" % expected["label"], last_switch_ts

    switched, sw_msg = switch_sta_profile(cfg, expect_hotspot)
    switched_at = now
    if not switched:
        detail = sw_msg or "\u5207\u6362\u547D\u4EE4\u6267\u884C\u5931\u8D25"
        return False, "%s\u672A\u5C31\u7EEA\uFF0C\u81EA\u52A8\u5207\u6362\u5931\u8D25: %s" % (expected["label"], detail), switched_at

    _, ip_after = wait_for_sta_ipv4(section, timeout_seconds=SSID_READY_TIMEOUT_SECONDS, interval_seconds=1)
    if ip_after:
        note = "%s\u672A\u5C31\u7EEA\uFF0C\u5DF2\u81EA\u52A8\u5207\u6362\u5230\u671F\u671B\u914D\u7F6E\u3002" % expected["label"]
        if sw_msg:
            note = note + " " + sw_msg
        return True, note, switched_at

    detail = sw_msg or "\u5207\u6362\u540E\u4ECD\u672A\u83B7\u53D6IPv4\u5730\u5740"
    return False, "%s\u672A\u5C31\u7EEA\uFF0C\u81EA\u52A8\u5207\u6362\u540E\u4ECD\u4E0D\u53EF\u7528: %s" % (expected["label"], detail), switched_at


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
        b10 = (_getbyte(value, idx) << 16) | (_getbyte(value, idx + 1) << 8) | _getbyte(value, idx + 2)
        output.append(ALPHA[(b10 >> 18)])
        output.append(ALPHA[((b10 >> 12) & 63)])
        output.append(ALPHA[((b10 >> 6) & 63)])
        output.append(ALPHA[(b10 & 63)])

    idx = imax
    if len(value) - imax == 1:
        b10 = _getbyte(value, idx) << 16
        output.append(ALPHA[(b10 >> 18)] + ALPHA[((b10 >> 12) & 63)] + PAD_CHAR + PAD_CHAR)
    else:
        b10 = (_getbyte(value, idx) << 16) | (_getbyte(value, idx + 1) << 8)
        output.append(ALPHA[(b10 >> 18)] + ALPHA[((b10 >> 12) & 63)] + ALPHA[((b10 >> 6) & 63)] + PAD_CHAR)
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
    i_value = re.sub("'", '"', str(info_temp))
    i_value = re.sub(" ", "", i_value)
    return i_value


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

def init_getip(init_url):
    text = http_get(init_url, timeout=5)
    ip = extract_ip_from_text(text)
    if not ip:
        target_host = init_url.split("://", 1)[-1].split("/", 1)[0]
        ip = get_local_ip_for_target(target_host)
    if not ip:
        raise RuntimeError("无法获取本机登录 IP")
    return ip


def get_token(get_challenge_api, username, ip):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery112404953340710317169_" + str(now),
        "username": username,
        "ip": ip,
        "_": now,
    }
    data = parse_jsonp(http_get(get_challenge_api, params=params, timeout=5))
    token = data.get("challenge")
    if not token:
        msg = data.get("error_msg") or data.get("error") or "unknown response"
        raise RuntimeError("获取挑战码失败: " + localize_error(msg))
    resolved_ip = pick_valid_ip(data.get("client_ip"), data.get("online_ip"), ip)
    if not resolved_ip:
        raise RuntimeError("获取挑战码失败: 未获得有效客户端 IP")
    return token, resolved_ip


def query_online_status(rad_user_info_api, expected_username):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery112406118340540763985_" + str(now),
        "_": now,
    }
    data = parse_jsonp(http_get(rad_user_info_api, params=params, timeout=5))
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
        get_chksum(token, cfg["username"], hmd5, cfg["ac_id"], ip, cfg["n"], cfg["type"], i_value)
    )
    return i_value, hmd5, chksum


def login(srun_portal_api, cfg, ip, i_value, hmd5, chksum):
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
    data = parse_jsonp(http_get(srun_portal_api, params=params, timeout=5))
    error = str(data.get("error", "")).lower()
    result = str(data.get("res", "")).lower()
    success = error == "ok" or result == "ok"
    message = data.get("error_msg") or data.get("error") or "unknown response"
    return success, str(message)


def logout(srun_portal_api, cfg, ip):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery11240645308969735664_" + str(now),
        "action": "logout",
        "username": cfg["username"],
        "ac_id": cfg["ac_id"],
        "ip": ip,
        "_": now,
    }
    data = parse_jsonp(http_get(srun_portal_api, params=params, timeout=5))
    error = str(data.get("error", "")).lower()
    result = str(data.get("res", "")).lower()
    success = error == "ok" or result == "ok"
    message = data.get("error_msg") or data.get("error") or data.get("res") or "unknown response"
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
    ip = init_getip(urls["init_url"])
    token, ip = get_token(urls["get_challenge_api"], cfg["username"], ip)
    i_value, hmd5, chksum = do_complex_work(cfg, ip, token)
    ok, message = login(urls["srun_portal_api"], cfg, ip, i_value, hmd5, chksum)

    if (not ok) and ("challenge_expire_error" in message.lower()):
        token, ip = get_token(urls["get_challenge_api"], cfg["username"], ip)
        i_value, hmd5, chksum = do_complex_work(cfg, ip, token)
        ok, message = login(urls["srun_portal_api"], cfg, ip, i_value, hmd5, chksum)

    if (not ok) and ("no_response_data_error" in message.lower()):
        try:
            online, online_msg = query_online_status(urls["rad_user_info_api"], cfg["username"])
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


def run_daemon():
    was_in_quiet = False
    quiet_logout_done = False
    current_mode = "campus"
    was_online = False
    last_expected_ssid_switch_at = 0

    while True:
        cfg = load_config()
        interval = max(int(cfg["interval"]), 5)
        online_interval = min(interval, ONLINE_CHECK_MAX_SECONDS)

        if cfg["enabled"] != "1":
            was_in_quiet = False
            quiet_logout_done = False
            current_mode = "campus"
            was_online = False
            last_expected_ssid_switch_at = 0
            time.sleep(interval)
            continue

        in_quiet = in_quiet_window(cfg)
        mode_msg = ""

        if in_quiet:
            if failover_enabled(cfg):
                ssid_ok, ssid_msg, last_expected_ssid_switch_at = ensure_expected_profile(
                    cfg,
                    expect_hotspot=True,
                    last_switch_ts=last_expected_ssid_switch_at,
                )
                if ssid_ok:
                    current_mode = "hotspot"
                if ssid_msg:
                    mode_msg = (mode_msg + "；" if mode_msg else "") + ssid_msg
                if not ssid_ok:
                    message = "夜间停用（未连接）"
                    if mode_msg:
                        message = message + "；" + mode_msg
                    append_log(("[JXNU-SRun] " + message).strip())
                    was_in_quiet = True
                    was_online = False
                    current_mode = "hotspot"
                    time.sleep(min(interval, 60))
                    continue

            try:
                if not was_in_quiet:
                    quiet_logout_done = False

                if quiet_logout_done:
                    state = quiet_connection_state(cfg)
                    message = "夜间停用（%s）" % state
                    ok = True
                else:
                    ok, message = run_quiet_logout(cfg)
                    quiet_logout_done = ok
            except HTTP_EXCEPTIONS as exc:
                ok, message = False, "网络错误: " + localize_error(exc)
            except ValueError as exc:
                ok, message = False, "响应解析错误: " + localize_error(exc)
            except Exception as exc:
                ok, message = False, "错误: " + localize_error(exc)

            if mode_msg:
                message = message + "；" + mode_msg

            append_log(("[JXNU-SRun] " + message).strip())
            was_in_quiet = True
            was_online = False
            time.sleep(min(interval, 60))
            continue

        if was_in_quiet:
            append_log("[JXNU-SRun] 退出夜间时段，准备切回校园网配置")
            quiet_logout_done = False
            was_in_quiet = False
            was_online = False
            last_expected_ssid_switch_at = 0
            if failover_enabled(cfg):
                switched, sw_msg = switch_to_campus(cfg)
                current_mode = "campus" if switched else "hotspot"
                if sw_msg:
                    mode_msg = sw_msg

        if failover_enabled(cfg):
            ready_ok, ready_msg, last_expected_ssid_switch_at = ensure_expected_profile(
                cfg,
                expect_hotspot=False,
                last_switch_ts=last_expected_ssid_switch_at,
            )
            if ready_ok:
                current_mode = "campus"
                if ready_msg:
                    mode_msg = (mode_msg + "；" if mode_msg else "") + ready_msg
            else:
                current_mode = "hotspot"
                message = "校园网配置未就绪"
                if ready_msg:
                    message = message + "；" + ready_msg
                append_log("[JXNU-SRun] " + message)
                was_online = False
                time.sleep(min(interval, 30))
                continue

        if failover_enabled(cfg) and current_mode == "hotspot":
            was_online = False
            message = "已切换到热点SSID，校园网SSID恢复后将自动切回"
            if mode_msg:
                message = message + "；" + mode_msg
            append_log(("[JXNU-SRun] " + message).strip())
            time.sleep(interval)
            continue

        next_sleep = interval

        try:
            urls = build_urls(cfg["base_url"])
            online_now = False
            status_message = ""
            if cfg["username"]:
                online_now, status_message = query_online_status(urls["rad_user_info_api"], cfg["username"])

            if online_now:
                ok = True
                message = "在线，下一次检测间隔 %d 秒" % online_interval
                if not was_online:
                    message = "检测到在线，下一次检测间隔 %d 秒" % online_interval
                was_online = True
                next_sleep = online_interval
            else:
                if was_online:
                    append_log("[JXNU-SRun] 检测到断线，立即开始重连")
                was_online = False

                ok, message = run_once_with_retry(cfg)
                was_online = bool(ok)
                if not ok and status_message:
                    message = "%s；状态检测结果: %s" % (message, status_message)

        except HTTP_EXCEPTIONS as exc:
            append_log("[JXNU-SRun] 状态检测网络异常，尝试重连")
            was_online = False
            ok, message = run_once_with_retry(cfg)
            if not ok:
                message = "网络异常: %s；重连结果: %s" % (localize_error(exc), message)
        except ValueError as exc:
            append_log("[JXNU-SRun] 状态检测解析异常，尝试重连")
            was_online = False
            ok, message = run_once_with_retry(cfg)
            if not ok:
                message = "解析异常: %s；重连结果: %s" % (localize_error(exc), message)
        except Exception as exc:
            append_log("[JXNU-SRun] 状态检测异常，尝试重连")
            was_online = False
            ok, message = run_once_with_retry(cfg)
            if not ok:
                message = "异常: %s；重连结果: %s" % (localize_error(exc), message)

        if mode_msg:
            message = message + "；" + mode_msg

        append_log(("[JXNU-SRun] " + message).strip())
        time.sleep(next_sleep)

def main():
    parser = argparse.ArgumentParser(description="JXNU SRun client for OpenWrt")
    parser.add_argument("--daemon", action="store_true", help="run as daemon loop")
    parser.add_argument("--once", action="store_true", help="run login once")
    parser.add_argument("--status", action="store_true", help="query online status")
    parser.add_argument("--switch-hotspot", action="store_true", help="switch STA profile to hotspot")
    parser.add_argument("--switch-campus", action="store_true", help="switch STA profile to campus")
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
