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
    "campus_interface": "",
    "hotspot_interface": "",
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
    cfg["campus_interface"] = str(cfg.get("campus_interface", "")).strip()
    cfg["hotspot_interface"] = str(cfg.get("hotspot_interface", "")).strip()
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

    # Backward compatibility for old config keys.
    if not cfg["campus_interface"]:
        cfg["campus_interface"] = str(uci_get("primary_interface", "")).strip()
    if not cfg["hotspot_interface"]:
        cfg["hotspot_interface"] = str(uci_get("backup_interface", "")).strip()

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


def http_get(url, params=None, timeout=5):
    if params:
        query = _urlencode(params)
        url = url + ("&" if "?" in url else "?") + query

    if HAVE_URLLIB:
        req = urllib_request.Request(url, headers=HEADER, method="GET")
        with urllib_request.urlopen(req, timeout=timeout) as resp:
            return resp.read().decode("utf-8", errors="replace")

    candidates = [
        ("/bin/uclient-fetch", "uclient-fetch"),
        ("/usr/bin/uclient-fetch", "uclient-fetch"),
        ("/usr/bin/wget", "wget"),
        ("/bin/wget", "wget"),
    ]
    selected = None
    selected_type = ""
    for path, kind in candidates:
        if os.path.exists(path):
            selected = path
            selected_type = kind
            break

    if not selected:
        raise RuntimeError("未找到可用 HTTP 客户端（uclient-fetch/wget）。")

    if selected_type == "uclient-fetch":
        cmd = [selected, "-q", "-O", "-", "--timeout", str(int(timeout)), url]
    else:
        cmd = [selected, "-q", "-O", "-", "--timeout=%d" % int(timeout), url]

    try:
        output = subprocess.check_output(cmd, stderr=subprocess.STDOUT)
        return output.decode("utf-8", errors="replace")
    except subprocess.CalledProcessError as exc:
        details = exc.output.decode("utf-8", errors="replace") if exc.output else str(exc)
        raise RuntimeError("HTTP 请求失败: " + details)


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


def switch_by_stage(down_iface, up_iface, delay_seconds=10):
    msgs = []
    ok = True

    if down_iface:
        down_ok, down_msg = interface_down(down_iface)
        ok = ok and down_ok
        msgs.append(down_msg)

    if down_iface and up_iface and down_iface != up_iface:
        time.sleep(max(int(delay_seconds), 1))

    if up_iface:
        up_ok, up_msg = interface_up(up_iface)
        ok = ok and up_ok
        msgs.append(up_msg)

    return ok, "；".join([x for x in msgs if x])


def switch_to_hotspot(cfg):
    campus = cfg.get("campus_interface", "").strip()
    hotspot = cfg.get("hotspot_interface", "").strip()
    if not campus or not hotspot:
        return False, "未配置校园网接口或热点接口"
    if campus == hotspot:
        return False, "校园网接口与热点接口不能相同"
    return switch_by_stage(campus, hotspot, 10)


def switch_to_campus(cfg):
    campus = cfg.get("campus_interface", "").strip()
    hotspot = cfg.get("hotspot_interface", "").strip()
    if not campus or not hotspot:
        return False, "未配置校园网接口或热点接口"
    if campus == hotspot:
        return False, "校园网接口与热点接口不能相同"
    return switch_by_stage(hotspot, campus, 10)


def evaluate_failover_mode(cfg, current_mode):
    if not failover_enabled(cfg):
        return "campus", ""

    # In hotspot mode, periodically try switching back to campus network.
    if current_mode == "hotspot":
        switched, _ = switch_to_campus(cfg)
        if switched and connectivity_ok(cfg):
            return "campus", "校园网接口已恢复，自动切回。"
        switch_to_hotspot(cfg)
        return "hotspot", "校园网接口未恢复，保持热点接口。"

    # In campus mode, switch to hotspot when connectivity is lost.
    if connectivity_ok(cfg):
        if current_mode != "campus":
            switch_to_campus(cfg)
        return "campus", ""

    switched, _ = switch_to_hotspot(cfg)
    if switched:
        return "hotspot", "检测到断网，已切换到热点接口。"
    return "hotspot", "检测到断网，但切换热点接口失败。"


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
        mode_hint = "（校园网接口: %s, 热点接口: %s）" % (
            cfg.get("campus_interface", "未设置"),
            cfg.get("hotspot_interface", "未设置"),
        )

    if in_quiet_window(cfg):
        return False, "夜间停用中（北京时间 %s）" % quiet_window_label(cfg) + mode_hint

    if not cfg["username"]:
        return False, "未配置学工号" + mode_hint

    urls = build_urls(cfg["base_url"])
    online, message = query_online_status(urls["rad_user_info_api"], cfg["username"])
    return online, localize_error(message) + mode_hint


def run_quiet_logout(cfg):
    if cfg.get("force_logout_in_quiet") != "1":
        return True, "夜间停用中（未启用强制下线）"

    if not cfg["username"]:
        return False, "夜间停用下线失败: 未配置学工号"

    urls = build_urls(cfg["base_url"])
    ip = init_getip(urls["init_url"])
    ok, message = logout(urls["srun_portal_api"], cfg, ip)
    if ok:
        return True, "夜间停用已下线，%s 自动恢复连接" % cfg.get("quiet_end", "06:00")
    return False, "夜间停用下线失败: " + localize_error(message)


def run_daemon():
    last_message = ""
    was_in_quiet = False
    quiet_logout_done = False
    current_mode = "campus"
    quiet_switched = False

    while True:
        cfg = load_config()
        interval = max(int(cfg["interval"]), 5)

        if cfg["enabled"] != "1":
            was_in_quiet = False
            quiet_logout_done = False
            current_mode = "campus"
            quiet_switched = False
            time.sleep(interval)
            continue

        in_quiet = in_quiet_window(cfg)
        mode_msg = ""

        if in_quiet:
            if failover_enabled(cfg) and not quiet_switched:
                switched, sw_msg = switch_to_hotspot(cfg)
                current_mode = "hotspot"
                quiet_switched = switched
                if sw_msg:
                    mode_msg = sw_msg

            try:
                if not was_in_quiet:
                    quiet_logout_done = False

                if quiet_logout_done:
                    message = "夜间停用中（北京时间 %s）" % quiet_window_label(cfg)
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
            time.sleep(min(interval, 60))
            continue

        if was_in_quiet:
            quiet_logout_done = False
            was_in_quiet = False
            quiet_switched = False
            if failover_enabled(cfg):
                switched, sw_msg = switch_to_campus(cfg)
                current_mode = "campus"
                if sw_msg:
                    mode_msg = sw_msg

        if failover_enabled(cfg):
            current_mode, failover_msg = evaluate_failover_mode(cfg, current_mode)
            if failover_msg:
                mode_msg = (mode_msg + "；" if mode_msg else "") + failover_msg
        else:
            current_mode = "campus"

        if current_mode == "hotspot":
            message = "已切换到热点接口，校园网恢复后将自动切回"
            if mode_msg:
                message = message + "；" + mode_msg
            log_line = ("[JXNU-SRun] " + message).strip()
            if message != last_message:
                append_log(log_line)
            last_message = message
            time.sleep(interval)
            continue

        try:
            ok, message = run_once(cfg)
            if not ok:
                time.sleep(3)
                retry_ok, retry_message = run_once(cfg)
                if retry_ok:
                    ok, message = True, "首次失败，重试成功"
                else:
                    ok, message = False, retry_message
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
        time.sleep(interval)


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

    try:
        if args.status:
            _, message = run_status(cfg)
            print(message)
            return

        _, message = run_once(cfg)
        print(message)
    except HTTP_EXCEPTIONS as exc:
        print("网络错误: " + localize_error(exc))
    except ValueError as exc:
        print("响应解析错误: " + localize_error(exc))
    except Exception as exc:
        print("错误: " + localize_error(exc))


if __name__ == "__main__":
    main()
