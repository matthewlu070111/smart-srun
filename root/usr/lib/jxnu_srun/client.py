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
LOG_MAX_BYTES = 512 * 1024
SWITCH_DELAY_SECONDS = 2
SSID_READY_TIMEOUT_SECONDS = 12
SSID_EXPECTED_RETRY_SECONDS = 30
ONLINE_CHECK_MULTIPLIER = 3
ONLINE_CHECK_MIN_SECONDS = 180
DISCONNECT_RETRY_DELAY_SECONDS = 3

DEFAULTS = {
    "enabled": "0",
    "user_id": "",
    "operator": "cucc",
    "password": "",
    "quiet_hours_enabled": "1",
    "force_logout_in_quiet": "1",
    "quiet_start": "00:00",
    "quiet_end": "06:00",
    "failover_enabled": "1",
    "campus_ssid": "",
    "hotspot_ssid": "",
    "connectivity_check_host": "8.8.8.8",
    "base_url": "http://172.17.1.2",
    "ac_id": "1",
    "n": "200",
    "type": "1",
    "enc": "srun_bx1",
    "interval": "60",
}

OPERATORS = {"cmcc", "ctcc", "cucc", "xn"}
PAD_CHAR = "="
ALPHA = "LVoJPiCN2R8G90yg+hmFHuacZ1OWMnrsSTXkYpUq/3dlbfKwv6xztjI7DeBE45QA"
HTTP_EXCEPTIONS = (socket.timeout,)
if HAVE_URLLIB:
    HTTP_EXCEPTIONS = HTTP_EXCEPTIONS + (urllib_error.URLError,)


def uci_get(option, default=""):
    key = "jxnu_srun.main." + option
    cmd = ["uci", "-q", "get", key]
    try:
        out = subprocess.check_output(cmd, stderr=subprocess.DEVNULL, text=True).strip()
        return out if out else default
    except (OSError, subprocess.CalledProcessError):
        return default


def load_config():
    cfg = {k: uci_get(k, v) for k, v in DEFAULTS.items()}
    cfg["enabled"] = str(cfg["enabled"]).strip()
    cfg["user_id"] = str(cfg["user_id"]).strip()
    cfg["operator"] = str(cfg["operator"]).strip().lower()
    cfg["password"] = str(cfg["password"]).strip()
    cfg["quiet_hours_enabled"] = str(cfg["quiet_hours_enabled"]).strip()
    cfg["force_logout_in_quiet"] = str(cfg["force_logout_in_quiet"]).strip()
    cfg["quiet_start"] = str(cfg.get("quiet_start", "")).strip()
    cfg["quiet_end"] = str(cfg.get("quiet_end", "")).strip()
    cfg["failover_enabled"] = str(cfg["failover_enabled"]).strip()
    cfg["campus_ssid"] = str(cfg.get("campus_ssid", "")).strip()
    cfg["hotspot_ssid"] = str(cfg.get("hotspot_ssid", "")).strip()
    cfg["connectivity_check_host"] = str(cfg["connectivity_check_host"]).strip()
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

    if not cfg["connectivity_check_host"]:
        cfg["connectivity_check_host"] = "8.8.8.8"

    # Backward compatibility for old interface-based keys.
    if not cfg["campus_ssid"]:
        cfg["campus_ssid"] = str(uci_get("campus_interface", "")).strip()
    if not cfg["campus_ssid"]:
        cfg["campus_ssid"] = str(uci_get("primary_interface", "")).strip()
    if not cfg["hotspot_ssid"]:
        cfg["hotspot_ssid"] = str(uci_get("hotspot_interface", "")).strip()
    if not cfg["hotspot_ssid"]:
        cfg["hotspot_ssid"] = str(uci_get("backup_interface", "")).strip()

    cfg["quiet_start"], cfg["quiet_start_minutes"] = normalize_hhmm(cfg.get("quiet_start", ""), "00:00")
    cfg["quiet_end"], cfg["quiet_end_minutes"] = normalize_hhmm(cfg.get("quiet_end", ""), "06:00")

    try:
        interval = int(cfg["interval"])
        cfg["interval"] = interval if interval > 0 else 60
    except ValueError:
        cfg["interval"] = 60
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

    # For private portal hosts (e.g. campus gateway), prefer campus interface IP.
    host_ip = pick_valid_ip(host)
    if host_ip:
        try:
            if ipaddress.ip_address(host_ip).is_private:
                campus_ssid = str(uci_get("campus_ssid", "")).strip()
                if not campus_ssid:
                    campus_ssid = str(uci_get("campus_interface", "")).strip()
                if not campus_ssid:
                    campus_ssid = str(uci_get("primary_interface", "")).strip()
                campus_net = get_network_interface_from_ssid(campus_ssid)
                if campus_net:
                    campus_ip = get_ipv4_from_network_interface(campus_net)
                    if campus_ip:
                        bind_ip = campus_ip
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
            details = exc.output.decode("utf-8", errors="replace") if exc.output else str(exc)
            errors.append("%s: %s" % (kind, details.strip()))
        except OSError as exc:
            errors.append("%s: %s" % (kind, str(exc)))

    if not available:
        raise RuntimeError("未找到可用 HTTP 客户端（uclient-fetch/wget）。")

    raise RuntimeError("HTTP 请求失败: " + " | ".join([e for e in errors if e]))


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


def interface_up(name):
    if not name:
        return False, "接口名为空"
    ok, msg = run_cmd(["ifup", name])
    return ok, ("启用接口 %s" % name) if ok else ("启用接口 %s 失败: %s" % (name, msg))


def interface_down(name):
    if not name:
        return False, "接口名为空"
    ok, msg = run_cmd(["ifdown", name])
    return ok, ("关闭接口 %s" % name) if ok else ("关闭接口 %s 失败: %s" % (name, msg))


def ping_ok(host):
    if not host:
        return False
    ok, _ = run_cmd(["ping", "-c", "1", "-W", "2", host])
    if ok:
        return True
    ok, _ = run_cmd(["ping", "-c", "1", "-w", "2", host])
    return ok


def connectivity_ok(cfg):
    return ping_ok(cfg.get("connectivity_check_host", "8.8.8.8"))


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
        if opt not in ("ssid", "mode", "network", "disabled"):
            continue
        data.setdefault(sec, {})[opt] = parse_uci_value(val)
    return data


def split_network_value(value):
    return [x for x in str(value or "").split() if x]


def find_sta_sections_by_ssid(ssid, wireless_data=None):
    target = str(ssid or "").strip()
    if not target:
        return []

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    matched = []
    for sec, opts in data.items():
        mode = str(opts.get("mode", "")).strip().lower()
        if mode and mode != "sta":
            continue
        if str(opts.get("ssid", "")).strip() == target:
            matched.append(sec)
    return matched


def get_network_interface_from_ssid(ssid):
    data = parse_wireless_iface_data()
    sections = find_sta_sections_by_ssid(ssid, data)
    for sec in sections:
        nets = split_network_value(data.get(sec, {}).get("network", ""))
        if nets:
            return nets[0]
    return None


def set_wifi_sections_disabled(sections, disabled):
    if not sections:
        return False, "未找到对应 SSID 的无线配置"

    errs = []
    val = "1" if disabled else "0"
    for sec in sections:
        ok, msg = run_cmd(["uci", "set", "wireless.%s.disabled=%s" % (sec, val)])
        if not ok:
            errs.append("%s: %s" % (sec, msg))
    if errs:
        return False, "；".join(errs)
    return True, ""


def commit_reload_wireless():
    ok1, msg1 = run_cmd(["uci", "commit", "wireless"])
    ok2, msg2 = run_cmd(["wifi", "reload"])
    if ok1 and ok2:
        return True, ""
    return False, "；".join([x for x in [msg1, msg2] if x])


def switch_ssid_by_stage(from_ssid, to_ssid, delay_seconds=SWITCH_DELAY_SECONDS):
    from_ssid = str(from_ssid or "").strip()
    to_ssid = str(to_ssid or "").strip()
    if not from_ssid or not to_ssid:
        return False, "未配置校园网 SSID 或热点 SSID"
    if from_ssid == to_ssid:
        return False, "校园网 SSID 与热点 SSID 不能相同"

    data = parse_wireless_iface_data()
    if not data:
        return False, "读取无线配置失败"

    from_sections = find_sta_sections_by_ssid(from_ssid, data)
    to_sections = find_sta_sections_by_ssid(to_ssid, data)
    if not from_sections:
        return False, "未找到校园网 SSID 对应的 STA 配置"
    if not to_sections:
        return False, "未找到热点 SSID 对应的 STA 配置"

    from_nets = []
    for sec in from_sections:
        from_nets.extend(split_network_value(data.get(sec, {}).get("network", "")))
    to_nets = []
    for sec in to_sections:
        to_nets.extend(split_network_value(data.get(sec, {}).get("network", "")))

    msgs = []
    ok = True

    ok1, msg1 = set_wifi_sections_disabled(from_sections, True)
    ok = ok and ok1
    if msg1:
        msgs.append(msg1)

    ok1b, msg1b = set_wifi_sections_disabled(to_sections, False)
    ok = ok and ok1b
    if msg1b:
        msgs.append(msg1b)

    ok2, msg2 = commit_reload_wireless()
    ok = ok and ok2
    if msg2:
        msgs.append(msg2)

    for net in sorted(set(from_nets)):
        d_ok, d_msg = interface_down(net)
        ok = ok and d_ok
        msgs.append(d_msg)

    if from_ssid != to_ssid and int(delay_seconds) > 0:
        time.sleep(int(delay_seconds))

    for net in sorted(set(to_nets)):
        u_ok, u_msg = interface_up(net)
        ok = ok and u_ok
        msgs.append(u_msg)

    return ok, "；".join([x for x in msgs if x])

def switch_to_hotspot(cfg):
    campus = cfg.get("campus_ssid", "").strip()
    hotspot = cfg.get("hotspot_ssid", "").strip()
    return switch_ssid_by_stage(campus, hotspot, SWITCH_DELAY_SECONDS)


def switch_to_campus(cfg):
    campus = cfg.get("campus_ssid", "").strip()
    hotspot = cfg.get("hotspot_ssid", "").strip()
    return switch_ssid_by_stage(hotspot, campus, SWITCH_DELAY_SECONDS)


def ensure_expected_ssid(cfg, expect_hotspot, last_switch_ts=0):
    if not failover_enabled(cfg):
        return True, "", last_switch_ts

    expected_ssid = str(cfg.get("hotspot_ssid" if expect_hotspot else "campus_ssid", "")).strip()
    expected_label = "\u70ed\u70b9SSID" if expect_hotspot else "\u6821\u56ed\u7f51SSID"
    if not expected_ssid:
        return False, "%s\u672a\u914d\u7f6e\uff0c\u65e0\u6cd5\u81ea\u52a8\u5207\u6362\u3002" % expected_label, last_switch_ts

    _, ip_now = wait_for_ssid_ipv4(expected_ssid, timeout_seconds=1, interval_seconds=1)
    if ip_now:
        return True, "", last_switch_ts

    now = time.time()
    if last_switch_ts and (now - last_switch_ts) < SSID_EXPECTED_RETRY_SECONDS:
        return False, "%s\u672a\u5c31\u7eea\uff0c\u7b49\u5f85\u540e\u91cd\u8bd5\u5207\u6362\u3002" % expected_label, last_switch_ts

    switched, sw_msg = (switch_to_hotspot(cfg) if expect_hotspot else switch_to_campus(cfg))
    switched_at = now
    if not switched:
        detail = sw_msg or "\u5207\u6362\u547d\u4ee4\u6267\u884c\u5931\u8d25"
        return False, "%s\u672a\u5c31\u7eea\uff0c\u81ea\u52a8\u5207\u6362\u5931\u8d25: %s" % (expected_label, detail), switched_at

    _, ip_after = wait_for_ssid_ipv4(
        expected_ssid,
        timeout_seconds=SSID_READY_TIMEOUT_SECONDS,
        interval_seconds=1,
    )
    if ip_after:
        note = "%s\u672a\u5c31\u7eea\uff0c\u5df2\u81ea\u52a8\u5207\u6362\u5230\u671f\u671bSSID\u3002" % expected_label
        if sw_msg:
            note = note + " " + sw_msg
        return True, note, switched_at

    detail = sw_msg or "\u5207\u6362\u540e\u4ecd\u672a\u83b7\u53d6IPv4\u5730\u5740"
    return False, "%s\u672a\u5c31\u7eea\uff0c\u81ea\u52a8\u5207\u6362\u540e\u4ecd\u4e0d\u53ef\u7528: %s" % (expected_label, detail), switched_at


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


def wait_for_ssid_ipv4(ssid, timeout_seconds=SSID_READY_TIMEOUT_SECONDS, interval_seconds=1):
    deadline = time.time() + max(int(timeout_seconds), 1)
    last_net = None

    while time.time() < deadline:
        net = get_network_interface_from_ssid(ssid)
        if net:
            last_net = net
            ip = get_ipv4_from_network_interface(net)
            if ip:
                return net, ip
        time.sleep(max(int(interval_seconds), 1))

    return last_net, None


def prepare_campus_for_login(cfg):
    if not failover_enabled(cfg):
        return True, ""

    campus_ssid = str(cfg.get("campus_ssid", "")).strip()
    if not campus_ssid:
        return True, ""

    _, campus_ip = wait_for_ssid_ipv4(campus_ssid, timeout_seconds=1, interval_seconds=1)
    if campus_ip:
        return True, ""

    switched, sw_msg = switch_to_campus(cfg)

    # Wait for WPA association and DHCP lease after SSID switching.
    _, campus_ip = wait_for_ssid_ipv4(campus_ssid, timeout_seconds=SSID_READY_TIMEOUT_SECONDS, interval_seconds=1)
    if campus_ip:
        return True, ""

    detail = sw_msg or "未获取到校园网SSID的IPv4地址"
    return False, "校园网SSID未就绪: %s。请确认已连接且获取IP。" % detail


def init_getip(init_url):
    text = http_get(init_url, timeout=5)
    ip = extract_ip_from_text(text)
    if not ip:
        target_host = init_url.split("://", 1)[-1].split("/", 1)[0]
        ip = get_local_ip_for_target(target_host)
    if not ip:
        raise RuntimeError("无法获取本机登录 IP。")
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
        raise RuntimeError("获取挑战码失败: 未获得有效客户端 IP。")
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
        return False, "请先在 LuCI 页面填写学工号和密码。"

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
        mode_hint = "（校园网SSID: %s, 热点SSID: %s）" % (
            cfg.get("campus_ssid", "未设置"),
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
        return True, "夜间停用（未连接）"
    return False, "夜间停用下线失败: " + localize_error(message)

def run_daemon():
    last_message = ""
    was_in_quiet = False
    quiet_logout_done = False
    current_mode = "campus"
    quiet_switched = False
    has_logged_in = False
    was_online = False
    last_expected_ssid_switch_at = 0

    while True:
        cfg = load_config()
        interval = max(int(cfg["interval"]), 5)
        online_interval = max(interval * ONLINE_CHECK_MULTIPLIER, ONLINE_CHECK_MIN_SECONDS)

        if cfg["enabled"] != "1":
            was_in_quiet = False
            quiet_logout_done = False
            current_mode = "campus"
            quiet_switched = False
            has_logged_in = False
            was_online = False
            last_expected_ssid_switch_at = 0
            time.sleep(interval)
            continue

        in_quiet = in_quiet_window(cfg)
        mode_msg = ""

        if in_quiet:
            if failover_enabled(cfg):
                ssid_ok, ssid_msg, last_expected_ssid_switch_at = ensure_expected_ssid(
                    cfg,
                    expect_hotspot=True,
                    last_switch_ts=last_expected_ssid_switch_at,
                )
                if ssid_ok:
                    current_mode = "hotspot"
                    quiet_switched = True
                if ssid_msg:
                    mode_msg = (mode_msg + "\uff1b" if mode_msg else "") + ssid_msg
                if not ssid_ok:
                    message = "\u591c\u95f4\u505c\u7528\uff08\u672a\u8fde\u63a5\uff09"
                    if mode_msg:
                        message = message + "\uff1b" + mode_msg
                    log_line = ("[JXNU-SRun] " + message).strip()
                    if message != last_message:
                        append_log(log_line)
                    last_message = message
                    was_in_quiet = True
                    was_online = False
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

            log_line = ("[JXNU-SRun] " + message).strip()
            if (not ok) or (message != last_message):
                append_log(log_line)
            last_message = message
            was_in_quiet = True
            was_online = False
            time.sleep(min(interval, 60))
            continue

        if was_in_quiet:
            quiet_logout_done = False
            was_in_quiet = False
            quiet_switched = False
            has_logged_in = False
            was_online = False
            if failover_enabled(cfg):
                switched, sw_msg = switch_to_campus(cfg)
                current_mode = "campus"
                if sw_msg:
                    mode_msg = sw_msg

        if failover_enabled(cfg) and current_mode == "hotspot" and has_logged_in:
            switched, sw_msg = switch_to_campus(cfg)
            if switched:
                current_mode = "campus"
                recover_msg = "热点模式下尝试切回校园网SSID。"
                if sw_msg:
                    recover_msg = recover_msg + " " + sw_msg
                mode_msg = (mode_msg + "；" if mode_msg else "") + recover_msg

        if failover_enabled(cfg) and current_mode == "campus":
            campus_ok, campus_msg, last_expected_ssid_switch_at = ensure_expected_ssid(
                cfg,
                expect_hotspot=False,
                last_switch_ts=last_expected_ssid_switch_at,
            )
            if campus_msg:
                mode_msg = (mode_msg + "\uff1b" if mode_msg else "") + campus_msg
            if not campus_ok:
                was_online = False
                message = "\u6821\u56ed\u7f51SSID\u672a\u5c31\u7eea\uff0c\u7a0d\u540e\u91cd\u8bd5"
                if mode_msg:
                    message = message + "\uff1b" + mode_msg
                log_line = ("[JXNU-SRun] " + message).strip()
                if message != last_message:
                    append_log(log_line)
                last_message = message
                time.sleep(interval)
                continue

        if current_mode == "hotspot":
            was_online = False
            message = "已切换到热点SSID，校园网SSID恢复后将自动切回"
            if mode_msg:
                message = message + "；" + mode_msg
            log_line = ("[JXNU-SRun] " + message).strip()
            if message != last_message:
                append_log(log_line)
            last_message = message
            time.sleep(interval)
            continue

        next_sleep = interval

        try:
            urls = build_urls(cfg["base_url"])
            online_now = False
            if cfg["username"]:
                online_now, _ = query_online_status(urls["rad_user_info_api"], cfg["username"])

            if online_now:
                ok = True
                has_logged_in = True
                message = "在线，降低检测频率（%d秒）" % online_interval
                if not was_online:
                    message = "检测到在线，降低检测频率（%d秒）" % online_interval
                was_online = True
                next_sleep = online_interval
            else:
                if was_online:
                    append_log("[JXNU-SRun] 检测到刚断线，立即重连。")
                was_online = False

                ok, message = run_once(cfg)
                if ok:
                    has_logged_in = True
                    was_online = True
                else:
                    time.sleep(DISCONNECT_RETRY_DELAY_SECONDS)
                    retry_ok, retry_message = run_once(cfg)
                    if retry_ok:
                        ok, message = True, "首次失败，重试成功"
                        has_logged_in = True
                        was_online = True
                    else:
                        ok, message = False, retry_message

        except HTTP_EXCEPTIONS as exc:
            if was_online:
                append_log("[JXNU-SRun] 检测到刚断线，立即重连。")
            was_online = False
            ok, message = False, "网络错误: " + localize_error(exc)
        except ValueError as exc:
            if was_online:
                append_log("[JXNU-SRun] 检测到刚断线，立即重连。")
            was_online = False
            ok, message = False, "响应解析错误: " + localize_error(exc)
        except Exception as exc:
            if was_online:
                append_log("[JXNU-SRun] 检测到刚断线，立即重连。")
            was_online = False
            ok, message = False, "错误: " + localize_error(exc)

        if failover_enabled(cfg) and has_logged_in and current_mode == "campus":
            if not connectivity_ok(cfg):
                switched, sw_msg = switch_to_hotspot(cfg)
                if switched:
                    current_mode = "hotspot"
                    was_online = False
                    next_sleep = interval
                    fail_msg = "检测到断网，已切换到热点SSID。"
                else:
                    fail_msg = "检测到断网，但切换热点SSID失败。"
                if sw_msg:
                    fail_msg = fail_msg + " " + sw_msg
                mode_msg = (mode_msg + "；" if mode_msg else "") + fail_msg

        if mode_msg:
            message = message + "；" + mode_msg

        log_line = ("[JXNU-SRun] " + message).strip()
        if (not ok) or (message != last_message):
            append_log(log_line)
        last_message = message
        time.sleep(next_sleep)


def main():
    parser = argparse.ArgumentParser(description="JXNU SRun client for OpenWrt")
    parser.add_argument("--daemon", action="store_true", help="run as daemon loop")
    parser.add_argument("--once", action="store_true", help="run login once")
    parser.add_argument("--status", action="store_true", help="query online status")
    args = parser.parse_args()

    cfg = load_config()

    if args.daemon:
        run_daemon()
        return

    exec_label = "手动登录结果" if args.once else "单次执行结果"

    try:
        if args.status:
            _, message = run_status(cfg)
            print(message)
            return

        _, message = run_once(cfg)
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

