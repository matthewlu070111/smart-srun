"""
网络基础设施 -- HTTP 客户端、IP 工具、shell 命令封装。

主要提供通用网络能力；绑定 IP 选择在非有线模式下会按需借助 wireless。
"""

import ipaddress
import json
import os
import re
import socket
import struct
import subprocess
import time

from config import campus_uses_wired, get_wired_iface, log, timed

try:
    import http.client as http_client
    import urllib.error as urllib_error
    import urllib.parse as urllib_parse
    import urllib.request as urllib_request

    HAVE_URLLIB = True
except ModuleNotFoundError:
    http_client = None
    urllib_error = None
    urllib_parse = None
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
    "http://connectivitycheck.platform.hicloud.com/generate_204",
    "http://wifi.vivo.com.cn/generate_204",
]


def run_cmd(cmd, timeout=60):
    try:
        res = subprocess.run(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=timeout,
        )
        return res.returncode == 0, (res.stdout or res.stderr or "").strip()
    except subprocess.TimeoutExpired:
        return False, "命令超时（%ds）: %s" % (timeout, " ".join(str(c) for c in cmd))
    except OSError as exc:
        return False, str(exc)


def _wget_supports_bind(path):
    """真实的 GNU wget 才支持 --bind-address；uclient-fetch / busybox 不支持。"""
    try:
        real = os.path.realpath(path)
    except OSError:
        real = path
    base = os.path.basename(real).lower()
    return "uclient" not in base and "busybox" not in base


def parse_uci_value(raw):
    text = str(raw or "").strip()
    if len(text) >= 2 and text[0] == text[-1] and text[0] in ('"', "'"):
        inner = text[1:-1]
        if text[0] == "'":
            # uci 把值内单引号输出为 '\''（关引号+转义引号+开引号），此处还原，
            # 否则含撇号的 SSID/密码读回值与写入值不符，会触发每 30s 重建循环、
            # 手动登录校验永远失败。
            inner = inner.replace("'\\''", "'")
        return inner
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


def redact_url_for_log(url):
    text = str(url or "").strip()
    if not text:
        return ""

    match = re.match(r"^([a-zA-Z][a-zA-Z0-9+.-]*://[^/?#]+(?:/[^?#]*)?)", text)
    if match:
        return match.group(1)

    text = text.split("#", 1)[0]
    return text.split("?", 1)[0]


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
    if str(url or "").lower().startswith("https://"):
        reasons.append(
            "如果该认证网关必须使用 HTTPS，请确认已安装 python3-openssl 后重试"
        )

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
        candidate = pick_valid_ip(match.group(1))
        if candidate:
            return candidate
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


def _parse_network_interface_status(text):
    try:
        data = json.loads(text)
    except (TypeError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def get_ipv4_from_network_interface(iface_name):
    if not iface_name:
        return None

    ok, out = run_cmd(["ubus", "call", "network.interface.%s" % iface_name, "status"])
    data = _parse_network_interface_status(out) if ok and out else {}
    if ok and out:
        ipv4_list = data.get("ipv4-address") or data.get("ipv4_address") or []
        if isinstance(ipv4_list, list):
            for item in ipv4_list:
                if isinstance(item, dict):
                    addr = pick_valid_ip(item.get("address"))
                    if addr:
                        return addr

    dev = iface_name
    if data:
        dev = data.get("l3_device") or data.get("device") or dev

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


def _valid_ipv4(value):
    candidate = pick_valid_ip(value)
    return candidate if candidate and ":" not in candidate else None


def _network_device_name(value):
    device = str(value or "").strip().split("@", 1)[0].rstrip(":")
    if not re.match(r"^[A-Za-z0-9_.:-]{1,15}$", device):
        return None
    return device


def resolve_wired_binding(cfg):
    """Resolve one selected interface to its IPv4 and L3 device together.

    A global address lookup cannot disambiguate overlapping campus subnets.
    Prefer netifd's interface identity; only a literal Linux device may use
    the device-scoped ip-address fallback.
    """
    iface = get_wired_iface(cfg)
    ok, output = run_cmd(
        ["ubus", "call", "network.interface.%s" % iface, "status"], timeout=5
    )
    data = _parse_network_interface_status(output) if ok else {}
    if data:
        device = _network_device_name(data.get("l3_device") or data.get("device"))
        if not device:
            raise RuntimeError("有线接口 %s 无法确定 L3 设备" % iface)
        if data.get("up") is False:
            raise RuntimeError("有线接口 %s 尚未就绪" % iface)
        addresses = data.get("ipv4-address") or data.get("ipv4_address") or []
        if isinstance(addresses, list):
            for item in addresses:
                address = _valid_ipv4(item.get("address")) if isinstance(item, dict) else None
                if address:
                    return address, device
    else:
        device = _network_device_name(iface)
    if not device:
        raise RuntimeError("有线接口 %s 无法确定 L3 设备" % iface)
    ok, output = run_cmd(["ip", "-4", "-o", "addr", "show", "dev", device], timeout=5)
    if ok:
        for line in output.splitlines():
            fields = line.split()
            if (len(fields) >= 4 and fields[2] == "inet"
                    and _network_device_name(fields[1]) == device):
                address = _valid_ipv4(fields[3].split("/", 1)[0])
                if address:
                    return address, device
    raise RuntimeError("有线接口 %s 尚未获取到 IPv4 地址" % iface)


def resolve_http_binding(url, cfg, bind_ip=None):
    """HTTP keyword arguments; legacy callers retain source-only behavior."""
    strict = campus_uses_wired(cfg) and str(cfg.get("_multi_wan_strict_bind", "0")).strip() == "1"
    if not strict:
        return {"bind_ip": bind_ip or resolve_bind_ip(url, cfg)}
    address, device = resolve_wired_binding(cfg)
    if bind_ip and str(bind_ip) != address:
        raise RuntimeError("有线接口 %s 的 IPv4 地址已变化，请重新检查线路" % get_wired_iface(cfg))
    return {"bind_ip": address, "bind_device": device, "strict": True,
            "bind_iface": get_wired_iface(cfg)}


def resolve_bind_ip(url, cfg):
    host = extract_host_from_url(url)
    wired_mode = campus_uses_wired(cfg)

    # 有线模式优先使用指定接口的 IPv4。多 WAN 的活动及逐账号视图会带上
    # 严格绑定标记，此时缺少接口地址必须失败，不能把认证流量发到其它 WAN；
    # 存量单有线配置没有该标记，缺地址时继续沿用原来的路由选源逻辑。
    if wired_mode:
        iface = get_wired_iface(cfg)
        if str(cfg.get("_multi_wan_strict_bind", "0")).strip() == "1":
            return resolve_wired_binding(cfg)[0]
        bind_ip = get_ipv4_from_network_interface(iface)
        log(
            "DEBUG",
            "bind_ip_resolved",
            host=host,
            iface=iface,
            bind_ip=bind_ip or "",
            reason="wired_interface" if bind_ip else "wired_interface_no_ip",
        )
        if bind_ip:
            return bind_ip

    bind_ip = get_local_ip_for_target(host) if host else None
    if wired_mode:
        reason = "wired_interface_no_ip_route_fallback" if bind_ip else "no_route"
    else:
        reason = "route_to_host" if bind_ip else "no_route"
    host_ip = pick_valid_ip(host)
    if host_ip and not wired_mode:
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
                            reason = "sta_override"
        except ValueError:
            pass
    log(
        "DEBUG",
        "bind_ip_resolved",
        host=host,
        bind_ip=bind_ip or "",
        reason=reason,
    )
    return bind_ip


def get_network_device_for_ip(bind_ip):
    """Return the L3 device which owns *bind_ip*, if it can be determined."""
    wanted = pick_valid_ip(bind_ip)
    if not wanted:
        return None
    ok, output = run_cmd(["ip", "-4", "-o", "addr", "show"], timeout=5)
    if not ok or not output:
        return None
    devices = set()
    for line in output.splitlines():
        fields = line.split()
        if len(fields) < 4 or fields[2] != "inet":
            continue
        address = fields[3].split("/", 1)[0]
        if address != wanted:
            continue
        # iproute2 may render stacked devices as "eth0@if5". SO_BINDTODEVICE
        # expects the local name before the peer suffix.
        device = _network_device_name(fields[1])
        if device:
            devices.add(device)
    return next(iter(devices)) if len(devices) == 1 else None


def validate_ip_device_binding(bind_ip, bind_device):
    """Reject stale DHCP addresses even if another device still owns the IP.

    Linux bind(source_ip) validates addresses across the whole namespace, not
    just SO_BINDTODEVICE's device. Check their association before sending.
    """
    address = _valid_ipv4(bind_ip)
    device = _network_device_name(bind_device)
    if not address or not device:
        raise RuntimeError("严格绑定缺少有效 IPv4 或 L3 设备")
    ok, output = run_cmd(["ip", "-4", "-o", "addr", "show", "dev", device], timeout=5)
    if ok:
        for line in output.splitlines():
            fields = line.split()
            if (len(fields) >= 4 and fields[2] == "inet"
                    and _network_device_name(fields[1]) == device
                    and fields[3].split("/", 1)[0] == address):
                return
    raise RuntimeError("绑定接口 %s 已不再持有 IPv4 地址 %s，请重新检查线路" % (device, address))


def _create_bound_connection(address, timeout, source_address, bind_device, strict=False, bind_iface=None):
    """socket.create_connection equivalent with optional SO_BINDTODEVICE."""
    last_error = None
    lookup_host = address[0]
    if isinstance(lookup_host, str):
        try:
            # OpenWrt python3-light omits the idna codec. Passing an ASCII byte
            # string keeps IPv4 and ordinary DNS names on libc's resolver path
            # without importing that optional codec.
            lookup_host = lookup_host.encode("ascii")
        except UnicodeEncodeError:
            pass
    if strict:
        source_ip = source_address[0] if source_address else None
        validate_ip_device_binding(source_ip, bind_device)
        host = lookup_host.decode("ascii") if isinstance(lookup_host, bytes) else lookup_host
        dns_timeout = timeout if isinstance(timeout, (int, float)) else 5
        ips = _resolve_probe_ips(host, dns_timeout, bind_ip=source_ip,
                                 bind_device=bind_device, iface=bind_iface, strict=True)
        addresses = [(socket.AF_INET, socket.SOCK_STREAM, 0, "", (ip, address[1])) for ip in ips]
    else:
        addresses = socket.getaddrinfo(lookup_host, address[1], 0, socket.SOCK_STREAM)
    for af, socktype, proto, _canonname, sockaddr in addresses:
        sock = None
        try:
            sock = socket.socket(af, socktype, proto)
            if timeout is not socket._GLOBAL_DEFAULT_TIMEOUT:
                sock.settimeout(timeout)
            if bind_device:
                option = getattr(socket, "SO_BINDTODEVICE", 25)
                device_bytes = str(bind_device).encode("utf-8") + b"\0"
                sock.setsockopt(socket.SOL_SOCKET, option, device_bytes)
            if source_address:
                sock.bind(source_address)
            sock.connect(sockaddr)
            return sock
        except OSError as exc:
            last_error = exc
            if sock is not None:
                sock.close()
    if last_error is not None:
        raise last_error
    raise OSError("getaddrinfo returns an empty list")


def _http_get_via_stdlib(url, timeout, bind_ip, bind_device=None, strict=False, bind_iface=None):
    """用 stdlib http.client 发起 GET，可选绑定源 IP 和所属 L3 设备。

    避免依赖 wget --bind-address（BusyBox wget / uclient-fetch 都不支持），
    在 python3-light 上即可完成多 WAN 绑定。返回 (body, status_code)。
    """
    parts = urllib_parse.urlsplit(url)
    scheme = (parts.scheme or "http").lower()
    host = parts.hostname or ""
    port = parts.port or (443 if scheme == "https" else 80)
    path = parts.path or "/"
    if parts.query:
        path = path + "?" + parts.query

    source = (bind_ip, 0) if bind_ip else None
    if bind_ip and bind_device is None:
        bind_device = get_network_device_for_ip(bind_ip)
    if scheme == "https":
        if not hasattr(http_client, "HTTPSConnection"):
            raise RuntimeError("当前 Python 缺少 HTTPS 支持，请安装 python3-openssl 后重试")
        conn = http_client.HTTPSConnection(
            host, port, timeout=timeout, source_address=source
        )
    else:
        conn = http_client.HTTPConnection(
            host, port, timeout=timeout, source_address=source
        )
    if bind_device:
        # HTTPConnection.connect() delegates socket creation to this instance
        # attribute. Replacing it lets HTTPSConnection keep its normal TLS wrap
        # while the underlying TCP socket is pinned to the selected WAN device.
        def create_connection(address, socket_timeout=socket._GLOBAL_DEFAULT_TIMEOUT,
                              source_address=None):
            return _create_bound_connection(
                address,
                socket_timeout,
                source_address,
                bind_device,
                strict=strict,
                bind_iface=bind_iface,
            )

        conn._create_connection = create_connection
        log(
            "DEBUG",
            "http_bind_device",
            bind_ip=bind_ip,
            bind_device=bind_device,
        )
    try:
        conn.request("GET", path, headers=HEADER)
        resp = conn.getresponse()
        body = resp.read().decode("utf-8", errors="replace")
        return body, resp.status
    finally:
        conn.close()


def http_get(url, params=None, timeout=5, bind_ip=None, bind_device=None, strict=False, bind_iface=None):
    if params:
        query = _urlencode(params)
        url = url + ("&" if "?" in url else "?") + query

    host = extract_host_from_url(url)
    log_url = redact_url_for_log(url)
    log(
        "DEBUG",
        "http_fetch",
        method="GET",
        url=log_url,
        host=host,
        timeout=timeout,
        bind_ip=bind_ip or "",
    )

    errors = []
    dns_failure_host = ""
    if strict and not _valid_ipv4(bind_ip):
        raise RuntimeError("严格绑定的 HTTP 请求缺少有效 IPv4 地址")
    if strict and not _network_device_name(bind_device):
        raise RuntimeError("严格绑定的 HTTP 请求缺少明确 L3 设备")
    if bind_device:
        bind_device = _network_device_name(bind_device)
        if not bind_device:
            raise RuntimeError("HTTP 请求的 L3 设备名称无效")
    if bind_device is None:
        bind_device = get_network_device_for_ip(bind_ip) if bind_ip else None
    if (strict or bind_device) and not HAVE_URLLIB:
        raise RuntimeError("绑定接口 %s 需要 Python HTTP 客户端，不能降级为 wget" % bind_device)
    if strict:
        validate_ip_device_binding(bind_ip, bind_device)

    with timed() as t:
        if HAVE_URLLIB:
            try:
                if bind_ip or bind_device:
                    bound_kwargs = {"bind_device": bind_device}
                    if strict:
                        bound_kwargs.update(strict=True, bind_iface=bind_iface)
                    body, status_code = _http_get_via_stdlib(url, timeout, bind_ip, **bound_kwargs)
                    client_name = "http.client(bound)"
                else:
                    req = urllib_request.Request(url, headers=HEADER, method="GET")
                    with urllib_request.urlopen(req, timeout=timeout) as resp:
                        body = resp.read().decode("utf-8", errors="replace")
                        status_code = getattr(resp, "status", None) or resp.getcode()
                    client_name = "urllib"
                log(
                    "DEBUG",
                    "http_fetch_result",
                    url=log_url,
                    host=host,
                    client=client_name,
                    status_code=status_code,
                    bytes_received=len(body),
                    duration_ms=t.ms,
                )
                return body
            except Exception as exc:
                msg = str(exc)
                errors.append("%s: %s" % ("http.client" if bind_ip or bind_device else "urllib", msg))
                lower = msg.lower()
                if ("name or service not known" in lower
                        or "nodename nor servname" in lower
                        or "temporary failure in name resolution" in lower
                        or "getaddrinfo" in lower):
                    dns_failure_host = host

                # Once the source IP has been mapped to an L3 device, falling
                # back to wget --bind-address would silently lose
                # SO_BINDTODEVICE and may send campus credentials via another
                # PPPoE WAN. Fail closed instead.
                if strict or bind_device:
                    raise RuntimeError(
                        "绑定接口 %s 的 HTTP 请求失败：%s" % (bind_device, msg)
                    )

        if bind_ip is None:
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
            # 原生 OpenWrt 的 /usr/bin/wget 往往是 uclient-fetch 的符号链接，
            # 不认识 --bind-address；按真实实现判断，避免给它传该参数直接报错退出。
            supports_bind = kind == "wget" and _wget_supports_bind(path)
            if supports_bind:
                bind_capable = True

            if bind_ip and not supports_bind:
                errors.append("%s: 不支持 --bind-address（uclient-fetch/busybox）" % kind)
                continue

            if kind == "wget":
                cmd = [path, "-q", "-O", "-", "--timeout=%d" % int(timeout)]
                if bind_ip:
                    cmd.append("--bind-address=%s" % bind_ip)
                cmd.append(url)
            else:
                cmd = [path, "-q", "-O", "-", "--timeout", str(int(timeout)), url]

            # GNU wget 的 --timeout 只约束单次尝试，默认还会重试 20 次并线性
            # 退避，实测单个探测可拖到 4 分钟以上，把守护循环整个卡住。
            # 用子进程级硬超时兜底，对 busybox/GNU 两种实现都成立。
            hard_cap = max(int(timeout) * 2, int(timeout) + 3)
            try:
                output = subprocess.check_output(
                    cmd, stderr=subprocess.STDOUT, timeout=hard_cap
                )
                body = output.decode("utf-8", errors="replace")
                log(
                    "DEBUG",
                    "http_fetch_result",
                    url=log_url,
                    host=host,
                    client=kind,
                    bytes_received=len(body),
                    duration_ms=t.ms,
                )
                return body
            except subprocess.TimeoutExpired:
                errors.append("%s: timed out after %ds (hard cap)" % (kind, hard_cap))
            except subprocess.CalledProcessError as exc:
                details = exc.output.decode("utf-8", errors="replace") if exc.output else ""
                if not details:
                    details = "exit status %s" % getattr(exc, "returncode", "unknown")
                errors.append("%s: %s" % (kind, details.strip()))
            except OSError as exc:
                errors.append("%s: %s" % (kind, str(exc)))

    if dns_failure_host:
        log("WARN", "dns_probe_failed", host=dns_failure_host, url=log_url)

    log(
        "WARN",
        "http_fetch_result",
        url=log_url,
        host=host,
        outcome="error",
        duration_ms=t.ms,
        errors=len(errors),
    )

    if not available:
        raise RuntimeError("未找到可用 HTTP 客户端（uclient-fetch/wget）")

    if bind_ip and not bind_capable and not HAVE_URLLIB:
        raise RuntimeError("bind_ip requires wget --bind-address support")

    raise RuntimeError(humanize_http_errors(log_url, [e for e in errors if e]))


def parse_jsonp(text):
    wrapped = re.search(r"^[^(]*\((.*)\)\s*$", text, re.S)
    payload = wrapped.group(1) if wrapped else text
    return json.loads(payload)


def _split_http_url(url):
    rest = url.split("://", 1)[1] if "://" in url else url
    hostport, _, path = rest.partition("/")
    host, _, port_text = hostport.partition(":")
    port = int(port_text) if port_text.strip().isdigit() else 80
    return host, port, "/" + path


def _uplink_dns_servers(iface=None, strict=False):
    """上行接口 DHCP 下发的 DNS 服务器（仅 IPv4）。

    连通性探测必须绕开本机 dnsmasq/代理解析链：路由器跑透明代理（如
    OpenClash）时其 DNS 一旦卡死，127.0.0.1 的解析整体失效，但上行链路本身
    是好的；只有直接问上行 DNS 才测得到真实连通性。
    """
    servers = []
    if strict and not iface:
        return servers
    for selected_iface in ((iface,) if iface else ("wwan", "wan")):
        ok, output = run_cmd(
            ["ubus", "-S", "call", "network.interface.%s" % selected_iface, "status"],
            timeout=5,
        )
        if not ok:
            continue
        try:
            payload = json.loads(output or "{}")
        except ValueError:
            continue
        if not isinstance(payload, dict):
            continue
        for item in payload.get("dns-server", []):
            item = str(item).strip()
            if item and ":" not in item and item not in servers:
                servers.append(item)
    if not servers and not strict:
        try:
            with open("/tmp/resolv.conf.d/resolv.conf.auto", "r") as handle:
                for line in handle:
                    fields = line.split()
                    if (
                        len(fields) == 2
                        and fields[0] == "nameserver"
                        and ":" not in fields[1]
                        and fields[1] not in servers
                    ):
                        servers.append(fields[1])
        except OSError:
            pass
    return servers


def _bind_probe_socket(sock, bind_ip=None, bind_device=None, strict=False):
    if strict and (not _valid_ipv4(bind_ip) or not _network_device_name(bind_device)):
        raise RuntimeError("严格绑定的连通性探测缺少 IPv4 或 L3 设备")
    if strict:
        validate_ip_device_binding(bind_ip, bind_device)
    if bind_device:
        sock.setsockopt(socket.SOL_SOCKET, getattr(socket, "SO_BINDTODEVICE", 25),
                        str(bind_device).encode("utf-8") + b"\0")
    if bind_ip:
        sock.bind((bind_ip, 0))


def _dns_query_a(host, server, timeout, bind_ip=None, bind_device=None, strict=False):
    """向指定 DNS 服务器发原始 UDP A 记录查询，返回 IPv4 列表。"""
    txid = os.urandom(2)
    header = txid + struct.pack(">HHHHH", 0x0100, 1, 0, 0, 0)
    qname = (
        b"".join(bytes([len(p)]) + p for p in host.encode("ascii").split(b"."))
        + b"\x00"
    )
    packet = header + qname + struct.pack(">HH", 1, 1)
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(timeout)
    try:
        _bind_probe_socket(sock, bind_ip, bind_device, strict)
        sock.sendto(packet, (server, 53))
        data, _ = sock.recvfrom(1024)
    finally:
        sock.close()
    if len(data) < 12 or data[:2] != txid:
        raise ValueError("DNS 响应无效")
    ancount = struct.unpack(">H", data[6:8])[0]
    i = 12
    while i < len(data) and data[i] != 0:
        i += data[i] + 1
    i += 5
    ips = []
    for _ in range(ancount):
        if i + 12 > len(data):
            break
        if data[i] & 0xC0:
            i += 2
        else:
            while i < len(data) and data[i] != 0:
                i += data[i] + 1
            i += 1
        rtype, _rclass, _ttl, rdlen = struct.unpack(">HHIH", data[i : i + 10])
        i += 10
        if rtype == 1 and rdlen == 4:
            ips.append(".".join(str(b) for b in data[i : i + 4]))
        i += rdlen
    if not ips:
        raise ValueError("无 A 记录")
    return ips


def _resolve_probe_ips(host, timeout, bind_ip=None, bind_device=None, iface=None, strict=False):
    try:
        socket.inet_aton(host)
        return [host]
    except OSError:
        pass
    dns_timeout = max(1.0, min(2.0, timeout / 2.0))
    scoped = bool(bind_ip or bind_device or iface or strict)
    servers = _uplink_dns_servers(iface=iface, strict=strict) if scoped else _uplink_dns_servers()
    for server in servers[:2]:
        try:
            if scoped:
                return _dns_query_a(host, server, dns_timeout, bind_ip=bind_ip,
                                    bind_device=bind_device, strict=strict)
            return _dns_query_a(host, server, dns_timeout)
        except Exception:
            continue
    if strict:
        raise OSError("所选接口的 DNS 不可用")
    # 上行 DNS 不可得时退回本机解析链。bytes 主机名直接走 C 解析器：
    # python3-light 缺 unicodedata 时 str 主机名会因 idna 编解码器不可用
    # 抛 LookupError。仅取 IPv4，避免 v6 黑洞路由拖满连接超时。
    infos = socket.getaddrinfo(
        host.encode("ascii"), 80, socket.AF_INET, socket.SOCK_STREAM
    )
    return [info[4][0] for info in infos]


def _probe_http_status(url, timeout, bind_ip=None, bind_device=None, iface=None, strict=False):
    """裸 socket 发 HTTP GET 并只读状态行，返回状态码。仅支持 http。

    刻意不用 urllib/http.client（缺 idna 编解码器的设备上 stdlib 解析直接
    抛错），DNS 也优先绕开本机代理链，见 _uplink_dns_servers。
    """
    host, port, path = _split_http_url(url)
    last_error = None
    if strict and (not _valid_ipv4(bind_ip) or not _network_device_name(bind_device)):
        raise RuntimeError("严格绑定的连通性探测缺少 IPv4 或 L3 设备")
    scoped = bool(bind_ip or bind_device or iface or strict)
    if scoped:
        addresses = _resolve_probe_ips(host, timeout, bind_ip=bind_ip,
                                       bind_device=bind_device, iface=iface, strict=strict)
    else:
        addresses = _resolve_probe_ips(host, timeout)
    for ip in addresses[:2]:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        try:
            _bind_probe_socket(sock, bind_ip, bind_device, strict)
            sock.connect((ip, port))
            request = (
                "GET %s HTTP/1.1\r\n"
                "Host: %s\r\n"
                "User-Agent: %s\r\n"
                "Accept: */*\r\n"
                "Connection: close\r\n\r\n" % (path, host, HEADER["User-Agent"])
            )
            sock.sendall(request.encode("ascii"))
            head = b""
            while b"\r\n" not in head and len(head) < 512:
                chunk = sock.recv(256)
                if not chunk:
                    break
                head += chunk
            status_line = head.split(b"\r\n", 1)[0].decode("ascii", "replace")
            fields = status_line.split()
            if len(fields) < 2 or not fields[1].isdigit():
                raise ValueError("异常的 HTTP 状态行: %r" % status_line[:64])
            return int(fields[1])
        except Exception as exc:
            last_error = exc
        finally:
            sock.close()
    raise last_error if last_error else OSError("无可用探测地址")


def test_internet_connectivity(timeout=5, bind_ip=None, bind_device=None, iface=None, strict=False, bind_iface=None):
    # 连通性探测必须快速失败且零依赖：裸 socket 单次请求读真实状态码判定——
    # generate_204 只有直连时才返回 204，被门户劫持时是 302/200，比"响应字节
    # 数<64"的启发式可靠。绝不落 http_get 的 wget/uclient-fetch 兜底链：外网
    # 被墙时该链每个 URL 会串行拖满多个子进程硬超时（实测 20s+），把登录成功
    # 后的终态校验整个拖死。
    iface = bind_iface or iface
    for url in CONNECTIVITY_CHECK_URLS:
        log("DEBUG", "connectivity_probe_begin", url=url, timeout=timeout)
        with timed() as t:
            try:
                if bind_ip or bind_device or iface or strict:
                    status_code = _probe_http_status(
                        url, timeout, bind_ip=bind_ip, bind_device=bind_device,
                        iface=iface, strict=strict,
                    )
                else:
                    status_code = _probe_http_status(url, timeout)
            except Exception as exc:
                log(
                    "DEBUG",
                    "connectivity_probe_result",
                    url=url,
                    outcome="error",
                    duration_ms=t.ms,
                    error=str(exc),
                )
                continue
            if status_code == 204:
                log(
                    "DEBUG",
                    "connectivity_probe_result",
                    url=url,
                    outcome="online",
                    status_code=status_code,
                    duration_ms=t.ms,
                )
                return True, ""
            log(
                "WARN",
                "connectivity_probe_result",
                url=url,
                outcome="portal",
                status_code=status_code,
                duration_ms=t.ms,
            )
            return False, "疑似被重定向到认证页面"
    return False, "无法访问连通性检测服务器"


def test_portal_reachability(cfg, timeout=3, bind_ip=None, bind_device=None, strict=False, bind_iface=None):
    base_url = str(cfg.get("base_url", "")).strip()
    if not base_url:
        return False, "认证网关地址未配置"
    try:
        if strict or bind_device:
            binding = {"bind_ip": bind_ip, "bind_device": bind_device, "strict": strict}
            if bind_iface:
                binding["bind_iface"] = bind_iface
            http_get(base_url, timeout=timeout, **binding)
        elif campus_uses_wired(cfg) and str(cfg.get("_multi_wan_strict_bind", "0")).strip() == "1":
            http_get(base_url, timeout=timeout, **resolve_http_binding(base_url, cfg, bind_ip))
        elif bind_ip:
            http_get(base_url, timeout=timeout, bind_ip=bind_ip)
        elif campus_uses_wired(cfg):
            http_get(base_url, timeout=timeout, **resolve_http_binding(base_url, cfg))
        else:
            http_get(base_url, timeout=timeout)
        return True, ""
    except Exception as exc:
        detail = str(exc)
        if len(detail) > 120:
            detail = detail[:120] + "..."
        return False, detail
