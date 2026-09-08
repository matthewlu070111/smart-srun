"""Portal page probes for SRun metadata such as AC_ID."""

import re
import json
import time
from html import unescape
from html.parser import HTMLParser

from network import (
    HAVE_URLLIB,
    HEADER,
    _http_get_via_stdlib,
    decode_portal_body,
    http_get,
    humanize_http_errors,
    probe_captive_portal,
    resolve_http_binding,
    run_cmd,
)
from school_presets import normalize_base_url

try:
    from urllib import error as urllib_error
    from urllib import parse as urllib_parse
    from urllib import request as urllib_request
except ImportError:  # OpenWrt python3-light may omit urllib pieces.
    urllib_error = None
    urllib_parse = None
    urllib_request = None


MAX_REDIRECTS = 8


def _acid_from_url(url):
    if not urllib_parse:
        match = re.search(r"[?&]ac_id=([^&#]+)", str(url or ""))
        return match.group(1).strip() if match else ""
    try:
        parsed = urllib_parse.urlsplit(str(url or ""))
    except ValueError:
        return ""
    values = urllib_parse.parse_qs(parsed.query, keep_blank_values=False)
    acid_values = values.get("ac_id") or values.get("acid") or []
    return str(acid_values[0]).strip() if acid_values else ""


def _valid_acid(value):
    text = str(value or "").strip()
    if not text:
        return ""
    return text if re.match(r"^[A-Za-z0-9_.-]+$", text) else ""


def _acid_from_html(html):
    text = str(html or "")
    patterns = [
        r"<input[^>]+name=[\"']ac_id[\"'][^>]*value=[\"']([^\"']+)[\"']",
        r"<input[^>]+value=[\"']([^\"']+)[\"'][^>]*name=[\"']ac_id[\"']",
        r"[\"']ac_id[\"'][^<>]{0,120}?value=[\"']([^\"']+)[\"']",
        r"\bac_id\s*[:=]\s*[\"']?([A-Za-z0-9_.-]+)",
        r"[?&]ac_id=([A-Za-z0-9_.-]+)",
    ]
    for pattern in patterns:
        match = re.search(pattern, text, re.I | re.S)
        if match:
            acid = _valid_acid(match.group(1))
            if acid:
                return acid
    return ""


def _html_redirect_location(html):
    text = str(html or "")
    patterns = [
        r"<script[^>]*>\s*top\.self\.location\.href\s*=\s*[\"']([^\"']+)[\"']",
        r"\blocation\.href\s*=\s*[\"']([^\"']+)[\"']",
        r"<meta[^>]+http-equiv=[\"']?refresh[\"']?[^>]+content=[\"'][^\"']*url=([^\"']+)[\"']",
    ]
    for pattern in patterns:
        match = re.search(pattern, text, re.I | re.S)
        if match:
            return match.group(1).strip()
    return ""


class _NoRedirectHandler(urllib_request.HTTPRedirectHandler if urllib_request else object):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _stdlib_http_is_usable():
    """OpenWrt python3-light ships urllib but often omits the idna codec.

    Hostname encoding then raises LookupError('unknown encoding: idna')
    before any network IO, so detect it up front and let the system HTTP
    client (uclient-fetch / wget) run the probe instead.
    """
    if not HAVE_URLLIB or not urllib_request:
        return False
    try:
        "example.com".encode("idna")
    except LookupError:
        return False
    return True


def _fetch_via_system_client(url, timeout):
    """Probe without stdlib networking.

    uclient-fetch / wget follow redirects themselves, so no response headers
    are available and the body is already the final landing page. _probe_url
    still finds AC_ID via the HTML patterns.
    """
    return 200, {}, http_get(url, timeout=timeout)


def _fetch_once(url, timeout, **binding):
    if binding:
        if not HAVE_URLLIB:
            raise RuntimeError("指定出口探测需要 Python HTTP 支持，不能降级到其它出口")
        body, status, headers = _http_get_via_stdlib(
            url, timeout, return_headers=True, **binding
        )
        return status, headers, body
    if not _stdlib_http_is_usable():
        return _fetch_via_system_client(url, timeout)

    opener = urllib_request.build_opener(_NoRedirectHandler)
    req = urllib_request.Request(url, headers=HEADER, method="GET")
    try:
        response = opener.open(req, timeout=timeout)
        try:
            headers = dict(response.headers.items())
            body = decode_portal_body(response.read(), headers)
            return response.getcode(), headers, body
        finally:
            response.close()
    except urllib_error.HTTPError as exc:
        headers = dict(exc.headers.items())
        body = decode_portal_body(exc.read(), headers)
        return exc.code, headers, body
    except LookupError:
        # idna / codec path can still fail late on some builds.
        return _fetch_via_system_client(url, timeout)
    except Exception as exc:
        raise RuntimeError(humanize_http_errors(url, [exc]))


def _join_url(base, location):
    if not location:
        return ""
    if urllib_parse:
        return urllib_parse.urljoin(base, location)
    if location.startswith("http://") or location.startswith("https://"):
        return location
    return base.rstrip("/") + "/" + location.lstrip("/")


def _probe_url(start_url, timeout=5, **binding):
    current = str(start_url or "").strip()
    seen = set()
    deadline = time.monotonic() + timeout
    for _idx in range(MAX_REDIRECTS):
        if not current or current in seen:
            break
        seen.add(current)

        acid = _acid_from_url(current)
        if acid:
            return acid, "url", current

        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        status, headers, body = _fetch_once(current, remaining, **binding)
        acid = _acid_from_html(body)
        if acid:
            return acid, "html", current

        location = ""
        for key, value in headers.items():
            if str(key).lower() == "location":
                location = str(value or "").strip()
                break
        if not location:
            location = _html_redirect_location(body)
        if not location:
            break

        next_url = _join_url(current, location)
        acid = _acid_from_url(next_url)
        if acid:
            return acid, "redirect_url", next_url
        current = next_url

        if status >= 400:
            break
    return "", "", ""


class _OperatorOptions(HTMLParser):
    """Read actual realm selectors; never guess from arbitrary @ strings."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.selector = ""
        self.option = None
        self.operators = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "select":
            names = (str(attrs.get("id") or "").lower(), str(attrs.get("name") or "").lower())
            self.selector = next((key for key in ("domain", "realm", "suffix", "operator_suffix", "operator", "service") if key in names), "")
        elif tag == "option" and self.selector:
            self._finish_option()
            if "value" in attrs and "disabled" not in attrs:
                self.option = {"value": attrs["value"], "text": ""}

    def handle_data(self, data):
        if self.option is not None:
            self.option["text"] += data

    def handle_endtag(self, tag):
        if tag in ("option", "select"):
            self._finish_option()
        if tag == "select":
            self.selector = ""

    def _finish_option(self):
        option, self.option = self.option, None
        if option is None:
            return
        value = str(option["value"] or "").strip()
        label = " ".join(option["text"].split())[:80]
        if any(word in label.lower() for word in ("请选择", "select", "choose")):
            return
        # Some SRun templates encode a display index as "1 - @domain".
        match = re.fullmatch(r"(?:\d+\s*-\s*)?@([A-Za-z0-9_.-]{1,128})", value)
        if match:
            suffix = match.group(1)
        elif self.selector in ("operator", "service"):
            return  # Service IDs require an explicit @ suffix encoding.
        elif re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", value):
            suffix = value
        elif value == "" and label:
            suffix = ""
        else:
            return
        if suffix == "??" or any(op["suffix"] == suffix for op in self.operators):
            return
        if len(self.operators) < 20:
            self.operators.append({"suffix": suffix, "label": label or suffix or "不加后缀"})


def discover_operators(base_url, ac_id="", access_mode="", iface="", ssid="", timeout=15):
    """Discover offered suffixes from the portal without credentials/login."""
    raw = str(base_url or "").strip()
    if raw and "://" not in raw:
        raw = "http://" + raw
    base = normalize_base_url(raw)
    result = {"ok": False, "operators": [], "source_url": "", "message": "未识别到认证后缀。"}
    if not base:
        result["message"] = "请先填写认证地址"
        return result
    try:
        binding, _iface = _detect_binding(access_mode, iface, ssid)
        deadline = time.monotonic() + timeout
        url = raw
        if ac_id and "?" not in url and _valid_acid(ac_id):
            url += "?ac_id=" + ac_id
        seen = set()
        for _ in range(MAX_REDIRECTS):
            if url in seen or time.monotonic() >= deadline:
                break
            seen.add(url)
            parts = urllib_parse.urlsplit(url)
            # Follow the selected portal's own page only. No unrelated hosts,
            # scripts, or links to login/logout actions are fetched or run.
            if (parts.scheme not in ("http", "https") or parts.username or parts.password
                    or parts.hostname != urllib_parse.urlsplit(base).hostname
                    or re.search(r"(?:/cgi-bin/|logout|action=)", url, re.I)):
                result["message"] = "认证页跳转到其它服务，请填写最终登录页地址后重新读取后缀"
                break
            status, headers, body = _fetch_once(url, max(0.1, deadline - time.monotonic()), **binding)
            if status >= 400:
                result["message"] = "认证页读取失败（HTTP %s），请检查认证地址" % status
                break
            parser = _OperatorOptions()
            parser.feed(body[:524288])
            parser.close()
            parser._finish_option()
            if parser.operators:
                result.update(ok=True, operators=parser.operators, source_url=url,
                              message="已读取 %s 个认证后缀。" % len(parser.operators))
                break
            location = next((str(v) for k, v in headers.items() if k.lower() == "location"), "")
            location = location or _html_redirect_location(body)
            if not location:
                break
            url = _join_url(url, unescape(location))
    except Exception as exc:
        result["message"] = "读取认证页后缀失败：" + str(exc)
    return result


# 运营商后缀探测的结果分类。
#
# 凭据错误和 IP 已在线都不能证明本次用户名正确；只停止尝试，不确认后缀。
_ACCOUNT_UNKNOWN_MARKS = ("e2531", "user not found", "userid error", "用户不存在")
_CREDENTIAL_MARKS = ("用户名或密码错误", "password error", "bad password")
_ONLINE_MARKS = ("e2620", "already online", "已在线")

_OPERATOR_OUTCOME_TEXT = {
    "hit": "登录成功，后缀已确认",
    "online": "网关提示已在线，无法据此确认后缀；已停止尝试，请手动选择",
    "credential": "网关提示账号或密码错误，无法据此确认后缀；请检查凭据",
    "miss": "账号不存在或认证后缀不匹配",
    "other": "网关返回了无法归类的结果",
    "limited": "网关限制了认证频率，请稍后再试",
}


def classify_login_attempt(ok, message):
    """把一次试探性登录的结果归到 hit / online / credential / miss / other。"""
    text = str(message or "").strip().lower()
    for marks, outcome in (
        (_ONLINE_MARKS, "online"),
        (("e2532", "too frequent", "频繁", "频率"), "limited"),
        (_ACCOUNT_UNKNOWN_MARKS, "miss"),
        (_CREDENTIAL_MARKS, "credential"),
    ):
        for mark in marks:
            if mark in text:
                return outcome
    return "hit" if ok else "other"


def detect_operator(base_url, ac_id, user_id, password, candidates,
                    school="", max_attempts=5, access_mode="", iface="", ssid="",
                    login_shape=None):
    """Resolve online identity or verify supplied suffixes with bounded logins."""
    import config as _config
    import srun_auth

    def _fail(message):
        return {"ok": False, "confirmed": False, "suffix": "",
                "attempts": [], "message": message}

    user_id = str(user_id or "").strip()
    if not user_id:
        return _fail("请先填写学工号")
    base = normalize_base_url(base_url)
    if not base:
        return _fail("请先确定认证地址")

    ordered = []
    for raw in (candidates or []):
        # Missing or unverified values are not evidence for an empty suffix.
        if not isinstance(raw, str) or raw.strip() == "??":
            continue
        suffix = _config.normalize_operator_suffix(raw)
        if suffix not in ordered:
            ordered.append(suffix)
    try:
        budget = min(5, max(1, int(max_attempts)))
    except (TypeError, ValueError):
        budget = 5
    ordered = ordered[:budget]

    base_cfg = dict(_config.load_config())
    try:
        binding, selected_iface = _detect_binding(access_mode, iface, ssid)
    except (RuntimeError, ValueError) as exc:
        return _fail(str(exc))
    if binding:
        base_cfg["_probe_iface"] = selected_iface
        base_cfg["_multi_wan_strict_bind"] = "1"
        base_cfg["campus_access_mode"] = access_mode
        base_cfg["wired_iface"] = selected_iface
    if access_mode:
        # Resolve a new account's shape, never inherit the active account's values.
        _config._apply_login_shape(base_cfg, login_shape or {})
        base_cfg["school"] = str(school or "").strip() or "default"

    base_cfg.update(base_url=base, ac_id=str(ac_id or "1"), user_id=user_id,
                    username=user_id, password=str(password or ""))
    try:
        online, reported, _message = srun_auth.probe_online_identity(base_cfg)
    except Exception:
        online, reported = False, ""
    if online:
        reported = str(reported or "").strip()
        if ("@" in user_id and reported == user_id) or reported.startswith(user_id + "@"):
            suffix = reported[len(user_id) + 1:]
            if suffix != "??" and (not suffix or "@" not in suffix):
                return {"ok": True, "confirmed": True, "suffix": suffix, "attempts": [],
                        "source": "online_identity", "password_verified": False,
                        "message": "已从网关当前在线账号确认登录后缀；本次填写的密码尚未验证"}
        if reported == user_id:
            return _fail("在线账号未包含认证后缀，请手动选择。")
        return _fail("在线账号与填写的校园网账号不匹配，请核对账号。")
    if not str(password or ""):
        return _fail("未找到可确认的在线账号，请填写密码后验证")
    if not ordered:
        return _fail("未提供认证后缀。请填写后缀或选择“不加后缀”后验证登录。")

    attempts = []
    for suffix in ordered:
        cfg = dict(base_cfg)
        cfg["base_url"] = base
        cfg["ac_id"] = str(ac_id or "").strip() or str(base_cfg.get("ac_id", "1"))
        cfg["user_id"] = user_id
        cfg["password"] = str(password)
        cfg["operator_suffix"] = suffix
        cfg["username"] = user_id + ("@" + suffix if suffix else "")
        if str(school or "").strip():
            cfg["school"] = str(school).strip()

        if attempts:
            # E2532 is also used for authentication rate limiting on SRun.
            time.sleep(3.5)
        ok, message = srun_auth.probe_login_once(cfg)
        outcome = classify_login_attempt(ok, message)
        password_verified = outcome == "hit"
        if outcome in ("hit", "online"):
            try:
                online, reported, _message = srun_auth.probe_online_identity(cfg)
            except Exception:
                online, reported = False, ""
            if online and reported == cfg["username"] and (outcome == "hit" or "@" in reported):
                outcome = "hit"
            elif online and reported != cfg["user_id"]:
                outcome = "other"
                message = "网关报告的在线账号与候选不一致，未确认此后缀"
        attempts.append({
            "suffix": suffix,
            "username": cfg["username"],
            "outcome": outcome,
            "message": str(message or ""),
        })
        if outcome in ("hit", "online", "credential", "limited"):
            return {
                "ok": outcome == "hit",
                "confirmed": outcome == "hit",
                "suffix": suffix if outcome == "hit" else "",
                "attempts": attempts,
                "source": "login" if password_verified else "online_identity",
                "password_verified": password_verified,
                "message": ("已从网关完整在线账号确认后缀；本次密码尚未验证"
                            if outcome == "hit" and not password_verified else _OPERATOR_OUTCOME_TEXT[outcome]),
            }
        if outcome == "other":
            return {
                "ok": False, "confirmed": False, "suffix": "",
                "attempts": attempts,
                "message": "认证结果无法识别，已停止验证：" + str(message or ""),
            }

    return {
        "ok": False, "confirmed": False, "suffix": "", "attempts": attempts,
        "message": "现有认证后缀均未匹配到账号。请核对账号或补充认证后缀。",
    }


def _portal_origin(url):
    """从认证页完整地址取出网关来源，供 base_url 直接使用。"""
    return normalize_base_url(str(url or "").strip())


def _detect_binding(access_mode="", iface="", ssid=""):
    if not access_mode and not iface:
        return {}, ""
    if access_mode not in ("wired", "wifi"):
        raise ValueError("请先选择有线或无线接入方式")
    iface = str(iface or "").strip()
    if access_mode == "wifi":
        from wireless import parse_wireless_iface_data, get_enabled_sta_sections
        from wireless import get_network_interface_from_sta_section

        data = parse_wireless_iface_data()
        matches = []
        for section in get_enabled_sta_sections(data):
            net = get_network_interface_from_sta_section(section, data)
            if (net and (not iface or net == iface)
                    and (not ssid or str(data[section].get("ssid", "")) == ssid)):
                matches.append(net)
        matches = list(dict.fromkeys(matches))
        if len(matches) != 1:
            raise RuntimeError("未找到唯一匹配的无线客户端，请先连接校园网 Wi-Fi 并确认出口接口与 SSID")
        iface = matches[0]
    if not iface or not re.match(r"^[A-Za-z0-9_.:-]{1,64}$", iface):
        raise ValueError("请填写有效的出口接口名，例如 wan 或 wwan")
    binding = resolve_http_binding("", {"_probe_iface": iface})
    return binding, iface


def _environment_candidates(base_url, school, access_mode, iface, ssid):
    """Only inspect supplied/saved campus addresses and this interface's gateway."""
    import config
    from school_presets import get_preset

    candidates = []

    def add(url, source):
        url = _portal_origin(url)
        if url and url.startswith(("http://", "https://")) and url not in [u for u, _ in candidates]:
            candidates.append((url, source))

    add(base_url, "填写的认证地址")
    if school:
        preset = get_preset(school) or {}
        add((preset.get("defaults") or {}).get("base_url"), "学校预设")
    if access_mode:
        for account in config.load_config().get("campus_accounts", []):
            if account.get("access_mode", "wifi") != access_mode:
                continue
            if access_mode == "wired" and config.get_wired_iface(account) != iface:
                continue
            # Do not borrow another wireless network's campus address.
            if access_mode == "wifi" and (not ssid or account.get("ssid") != ssid):
                continue
            add(account.get("base_url"), "该线路已有账号")
    if iface:
        ok, output = run_cmd(["ubus", "call", "network.interface.%s" % iface, "status"], timeout=5)
        try:
            status = json.loads(output) if ok else {}
        except (ValueError, TypeError):
            status = {}
        for route in status.get("route", []):
            gateway = str(route.get("nexthop", ""))
            if route.get("target") == "0.0.0.0" and re.match(r"^\d+\.\d+\.\d+\.\d+$", gateway):
                add("http://" + gateway, "所选出口的网关")
    return candidates[:4]


def detect_environment(timeout=5, access_mode="", iface="", ssid="", base_url="", school=""):
    """检查所选出口；未被劫持时仍检查已知认证页和本线路网关。"""
    try:
        binding, selected_iface = _detect_binding(access_mode, iface, ssid)
    except (RuntimeError, ValueError) as exc:
        return {"ok": False, "state": "down", "status_code": 0, "checked_url": "",
                "portal_url": "", "base_url": "", "acid": "", "acid_source": "",
                "message": str(exc), "binding_error": True, "iface": iface}
    probe = probe_captive_portal(timeout=timeout, **binding)
    if access_mode == "wifi" and selected_iface and not ssid:
        from wireless import parse_wireless_iface_data, get_enabled_sta_sections
        from wireless import get_network_interface_from_sta_section

        data = parse_wireless_iface_data()
        for section in get_enabled_sta_sections(data):
            if get_network_interface_from_sta_section(section, data) == selected_iface:
                ssid = str(data[section].get("ssid", ""))
                break
    result = {
        "ok": False,
        "state": probe["state"],
        "status_code": probe["status_code"],
        "checked_url": probe["checked_url"],
        "portal_url": "",
        "base_url": "",
        "acid": "",
        "acid_source": "",
        "message": probe["message"],
        "iface": selected_iface,
        "ssid": ssid,
        "address_source": "",
    }
    if probe["state"] != "portal":
        checked = []
        for candidate, label in _environment_candidates(base_url, school, access_mode, selected_iface, ssid):
            checked.append(label + "：" + candidate)
            try:
                acid, source, detected_url = _probe_url(candidate, timeout=timeout, **binding)
            except Exception:
                continue
            if acid:
                result.update(ok=True, base_url=_portal_origin(detected_url or candidate),
                              acid=acid, acid_source=source, portal_url=detected_url,
                              address_source=label,
                              message="认证参数来源：%s。" % label)
                break
        result["candidates_checked"] = checked
        if not result["ok"] and probe["state"] == "online":
            result["message"] = "未识别到认证地址。可在下一步选择学校预设或填写登录页地址。"
        return result

    portal_url = _join_url(probe["checked_url"], probe["location"])
    result["portal_url"] = portal_url
    if portal_url:
        result["base_url"] = _portal_origin(portal_url)

    # 没有 Location 的拦截（部分门户直接回 200 + HTML 跳转）仍值得再走一次
    # 完整抓取：_probe_url 会读正文，能认出 meta refresh / location.href。
    start_url = portal_url or probe["checked_url"]
    try:
        acid, source, detected_url = _probe_url(start_url, timeout=timeout, **binding)
    except Exception as exc:
        result["message"] = str(exc)
        result["ok"] = bool(result["base_url"])
        return result

    if detected_url and not result["base_url"]:
        result["base_url"] = _portal_origin(detected_url)
    if acid:
        result["acid"] = acid
        result["acid_source"] = source
        if detected_url:
            result["portal_url"] = result["portal_url"] or detected_url
        result["message"] = "已从强制门户跳转中识别出认证地址与 AC_ID"
    elif result["base_url"]:
        result["message"] = "已捕获认证地址，但未发现 AC_ID"

    result["ok"] = bool(result["base_url"])
    return result


def detect_acid(base_url, reality_url="", timeout=5, access_mode="", iface="", ssid=""):
    raw_url = str(base_url or "").strip()
    if raw_url and "://" not in raw_url:
        raw_url = "http://" + raw_url
    if not raw_url and not reality_url:
        # 地址空着不再是错误：先问出口自己有没有被强制门户拦下来。用户填不出
        # 认证地址正是这个功能要解决的问题，不该把它当作前置条件。
        env = detect_environment(timeout=timeout, access_mode=access_mode, iface=iface, ssid=ssid)
        return {
            "ok": bool(env["ok"] and env["acid"]),
            "acid": env["acid"],
            "base_url": env["base_url"],
            "source": ("environment_" + env["acid_source"]) if env["acid_source"] else "",
            "detected_url": env["portal_url"],
            "state": env["state"],
            "message": env["message"],
        }

    acid = _acid_from_url(raw_url)
    normalized = normalize_base_url(raw_url)
    if acid:
        return {
            "ok": True,
            "acid": acid,
            "base_url": normalized,
            "source": "input_url",
            "message": "已从认证地址 URL 中发现 AC_ID",
        }

    try:
        binding, _iface = _detect_binding(access_mode, iface, ssid)
        if reality_url:
            acid, source, detected_url = _probe_url(reality_url, timeout=timeout, **binding)
            if acid:
                return {
                    "ok": True,
                    "acid": acid,
                    # 只给了劫持地址、没填认证地址时，网关就从跳转链里取。
                    "base_url": normalized or _portal_origin(detected_url or reality_url),
                    "source": "reality_" + source,
                    "detected_url": detected_url,
                    "message": "已从网络劫持跳转中发现 AC_ID",
                }
        if not normalized:
            return {
                "ok": False,
                "acid": "",
                "base_url": "",
                "source": "",
                "message": "未从提供的跳转地址中发现 AC_ID",
            }

        acid, source, detected_url = _probe_url(raw_url, timeout=timeout, **binding)
        if acid:
            return {
                "ok": True,
                "acid": acid,
                "base_url": normalized,
                "source": source,
                "detected_url": detected_url,
                "message": "已从认证网关页面中发现 AC_ID",
            }
    except Exception as exc:
        return {
            "ok": False,
            "acid": "",
            "base_url": normalized,
            "source": "",
            "message": str(exc),
        }

    return {
        "ok": False,
        "acid": "",
        "base_url": normalized,
        "source": "",
        "message": "未从认证地址、跳转链或页面中发现 AC_ID",
    }
