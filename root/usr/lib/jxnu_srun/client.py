#!/usr/bin/python3

import argparse
import hashlib
import hmac
import ipaddress
import json
import math
import re
import socket
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

HEADER = {
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/63.0.3239.26 Safari/537.36"
    )
}

BEIJING_TZ = timezone(timedelta(hours=8))

DEFAULTS = {
    "enabled": "0",
    "user_id": "",
    "operator": "cucc",
    "password": "",
    "quiet_hours_enabled": "1",
    "force_logout_in_quiet": "1",
    "base_url": "http://172.17.1.2",
    "ac_id": "1",
    "n": "200",
    "type": "1",
    "enc": "srun_bx1",
    "interval": "300",
}

OPERATORS = {"cmcc", "ctcc", "cucc", "xn"}
PAD_CHAR = "="
ALPHA = "LVoJPiCN2R8G90yg+hmFHuacZ1OWMnrsSTXkYpUq/3dlbfKwv6xztjI7DeBE45QA"


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

    try:
        interval = int(cfg["interval"])
        cfg["interval"] = interval if interval > 0 else 300
    except ValueError:
        cfg["interval"] = 300
    return cfg


def localize_error(message):
    mapping = {
        "challenge_expire_error": "挑战码已过期，请重试。",
        "no_response_data_error": "网关返回异常（可能已在线）。",
        "login_error": "认证失败。",
        "sign_error": "签名错误（参数不匹配）。",
        "unknown response": "网关返回未知响应。",
    }
    text = str(message or "").strip()
    return mapping.get(text, text)


def is_quiet_hours_now():
    now = datetime.now(BEIJING_TZ)
    return 0 <= now.hour < 6


def quiet_hours_enabled(cfg):
    return cfg.get("quiet_hours_enabled") == "1"


def in_quiet_window(cfg):
    return quiet_hours_enabled(cfg) and is_quiet_hours_now()


def http_get(url, params=None, timeout=5):
    if params:
        query = urllib.parse.urlencode(params)
        url = url + ("&" if "?" in url else "?") + query
    req = urllib.request.Request(url, headers=HEADER, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read().decode("utf-8", errors="replace")


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
        return False, "夜间停用中（北京时间 00:00-06:00），不执行登录"

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
    if in_quiet_window(cfg):
        return False, "夜间停用中（北京时间 00:00-06:00）"

    if not cfg["username"]:
        return False, "未配置学工号"

    urls = build_urls(cfg["base_url"])
    online, message = query_online_status(urls["rad_user_info_api"], cfg["username"])
    return online, message


def run_quiet_logout(cfg):
    if cfg.get("force_logout_in_quiet") != "1":
        return True, "夜间停用中（未启用强制下线）"

    if not cfg["username"]:
        return False, "夜间停用下线失败: 未配置学工号"

    urls = build_urls(cfg["base_url"])
    ip = init_getip(urls["init_url"])
    ok, message = logout(urls["srun_portal_api"], cfg, ip)
    if ok:
        return True, "夜间停用已下线，06:00 自动恢复连接"
    return False, "夜间停用下线失败: " + localize_error(message)


def run_daemon():
    last_message = ""
    was_in_quiet = False
    quiet_logout_done = False

    while True:
        cfg = load_config()
        interval = max(cfg["interval"], 10)

        if cfg["enabled"] != "1":
            was_in_quiet = False
            quiet_logout_done = False
            time.sleep(interval)
            continue

        if in_quiet_window(cfg):
            try:
                if not was_in_quiet:
                    quiet_logout_done = False

                if quiet_logout_done:
                    message = "夜间停用中（北京时间 00:00-06:00）"
                    ok = True
                else:
                    ok, message = run_quiet_logout(cfg)
                    quiet_logout_done = ok
            except (urllib.error.URLError, socket.timeout) as exc:
                ok, message = False, "网络错误: " + str(exc)
            except ValueError as exc:
                ok, message = False, "响应解析错误: " + str(exc)
            except Exception as exc:
                ok, message = False, "错误: " + str(exc)

            log_line = ("[JXNU-SRun] " + message).strip()
            if (not ok) or (message != last_message):
                print(log_line, flush=True)
            last_message = message
            was_in_quiet = True
            time.sleep(min(interval, 60))
            continue

        if was_in_quiet:
            try:
                _, message = run_once(cfg)
            except (urllib.error.URLError, socket.timeout) as exc:
                message = "网络错误: " + str(exc)
            except ValueError as exc:
                message = "响应解析错误: " + str(exc)
            except Exception as exc:
                message = "错误: " + str(exc)

            print("[JXNU-SRun] " + message, flush=True)
            last_message = message
            was_in_quiet = False
            quiet_logout_done = False
            time.sleep(interval)
            continue

        try:
            ok, message = run_once(cfg)
        except (urllib.error.URLError, socket.timeout) as exc:
            ok, message = False, "网络错误: " + str(exc)
        except ValueError as exc:
            ok, message = False, "响应解析错误: " + str(exc)
        except Exception as exc:
            ok, message = False, "错误: " + str(exc)

        log_line = ("[JXNU-SRun] " + message).strip()
        if (not ok) or (message != last_message):
            print(log_line, flush=True)
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
    except (urllib.error.URLError, socket.timeout) as exc:
        print("网络错误: " + str(exc))
    except ValueError as exc:
        print("响应解析错误: " + str(exc))
    except Exception as exc:
        print("错误: " + str(exc))


if __name__ == "__main__":
    main()
