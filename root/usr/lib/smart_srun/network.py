"""
网络基础设施 -- HTTP 客户端、IP 工具、shell 命令封装。

主要提供通用网络能力；绑定 IP 选择在非有线模式下会按需借助 wireless。
"""

import ipaddress
import json
import os
import re
import socket
import subprocess
import time

from config import campus_uses_wired

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

HTTP_EXCEPTIONS = (socket.timeout,)
if HAVE_URLLIB:
    HTTP_EXCEPTIONS = HTTP_EXCEPTIONS + (urllib_error.URLError,)

CONNECTIVITY_CHECK_URLS = [
    "http://connect.rom.miui.com/generate_204",
    "http://wifi.vivo.com.cn/generate_204",
]


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


def resolve_bind_ip(url, cfg):
    host = extract_host_from_url(url)
    bind_ip = get_local_ip_for_target(host) if host else None
    host_ip = pick_valid_ip(host)
    if host_ip and not campus_uses_wired(cfg):
        try:
            if ipaddress.ip_address(host_ip).is_private:
                from wireless import (
                    get_sta_section,
                    get_network_interface_from_sta_section,
                )

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

    if HAVE_URLLIB and not bind_ip:
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
    bind_capable = False
    for path, kind in candidates:
        if not os.path.exists(path):
            continue
        available = True
        if kind == "wget":
            bind_capable = True

        if bind_ip and kind != "wget":
            errors.append("%s: bind_ip requires wget --bind-address support" % kind)
            continue

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

    if bind_ip and not bind_capable:
        raise RuntimeError("bind_ip requires wget --bind-address support")

    raise RuntimeError(humanize_http_errors(url, [e for e in errors if e]))


def parse_jsonp(text):
    wrapped = re.search(r"^[^(]*\((.*)\)\s*$", text, re.S)
    payload = wrapped.group(1) if wrapped else text
    return json.loads(payload)


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


def test_portal_reachability(cfg, timeout=3):
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
