"""Read-only AP discovery and association details for one wireless radio."""

import json
import re

from network import run_cmd


READ_TIMEOUT = 2
SCAN_TIMEOUT = 20
MAX_CANDIDATES = 3
_STA_MODES = {"sta", "client", "station"}
_AP_MODES = {"ap", "master"}
_ENCRYPTIONS = {"none", "psk", "psk2", "psk-mixed", "sae", "sae-mixed"}


def valid_bssid(value):
    """Accept a six-octet unicast MAC, excluding the unset all-zero address."""
    if not isinstance(value, str):
        return False
    value = value.strip()
    if not re.fullmatch(r"(?:[0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}", value):
        return False
    return value != "00:00:00:00:00:00" and not (int(value[:2], 16) & 1)


def _json_command(command, timeout=READ_TIMEOUT):
    try:
        ok, output = run_cmd(command, timeout=timeout)
        if not ok:
            return None
        data = json.loads(output)
    except (OSError, TypeError, ValueError):
        return None
    return data if isinstance(data, dict) else None


def _wireless_status():
    # network.wireless status can include passwords: never return or log it.
    return _json_command(["ubus", "call", "network.wireless", "status"])


def _iwinfo(method, device, timeout=READ_TIMEOUT):
    return _json_command(
        ["ubus", "call", "iwinfo", method, json.dumps({"device": device})],
        timeout=timeout,
    )


def _config(entry):
    value = entry.get("config")
    return value if isinstance(value, dict) else {}


def _disabled(entry):
    return any(
        str(value).strip().lower() in ("1", "true", "yes", "on")
        for value in (entry.get("disabled", False), _config(entry).get("disabled", False))
    )


def _interfaces(radio):
    items = radio.get("interfaces")
    return [item for item in items if isinstance(item, dict)] if isinstance(items, list) else []


def _target_interfaces(status, section):
    return [
        (radio, entry)
        for radio, data in status.items() if isinstance(data, dict)
        for entry in _interfaces(data) if entry.get("section") == section
    ]


def _ifname(value):
    if isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.:-]{0,14}", value):
        return value
    return ""


def _mode(entry):
    return str(entry.get("mode") or "").strip().lower()


def _channel(value):
    if isinstance(value, bool):
        return None
    text = str(value)
    if not text.isdigit():
        return None
    number = int(text)
    return number if 1 <= number <= 233 else None


def _signal(value):
    if isinstance(value, bool):
        return None
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    # Some ubus JSON encoders expose iwinfo's signed signal as uint32.
    if 2**31 <= number <= 2**32 - 1:
        number -= 2**32
    if -127 <= number < 0:
        rounded = int(number)
        return rounded if rounded < 0 else None
    return None


def _security_matches(encryption, observed):
    if not isinstance(observed, dict):
        return False
    enabled = observed.get("enabled")
    if encryption == "none":
        return enabled is False or (type(enabled) is int and enabled == 0)
    if not (enabled is True or (type(enabled) is int and enabled == 1)):
        return False
    authentication = observed.get("authentication")
    versions = observed.get("wpa")
    if not isinstance(authentication, list) or not isinstance(versions, list):
        return False
    auth = {item.lower() for item in authentication if isinstance(item, str)}
    wpa = {str(item) for item in versions if not isinstance(item, bool)}
    psk1 = "psk" in auth and "1" in wpa
    psk2 = "psk" in auth and "2" in wpa
    sae = "sae" in auth and bool(wpa.intersection({"2", "3"}))
    return {
        "psk": psk1,
        "psk2": psk2,
        "psk-mixed": psk1 or psk2,
        "sae": sae,
        "sae-mixed": psk2 or sae,
    }.get(encryption, False)


def _scan_device(status, section, radio):
    matches = _target_interfaces(status, section) if section else []
    if len(matches) > 1:
        return "", "目标 STA 的无线电映射不明确"
    if matches:
        mapped_radio, target = matches[0]
        if radio and radio != mapped_radio:
            return "", "目标 STA 不属于所选无线电"
        radio = mapped_radio
        if _mode(_config(target)) not in _STA_MODES:
            return "", "目标接口不是 STA 接口"
    else:
        target = {}
    data = status.get(radio)
    if not isinstance(data, dict) or _disabled(data):
        return "", "所选无线电不可用或已禁用"
    target_ifname = _ifname(target.get("ifname"))
    if target_ifname and not _disabled(target):
        return target_ifname, ""
    ap_ifnames = sorted(
        _ifname(entry.get("ifname")) for entry in _interfaces(data)
        if _mode(_config(entry)) in _AP_MODES and not _disabled(entry)
        and _ifname(entry.get("ifname"))
    )
    if ap_ifnames:
        return ap_ifnames[0], ""
    # A disabled STA may have no netifd interface entry. Only use an explicitly
    # named PHY from this radio, or the selected radio itself; never guess phyN.
    device = _ifname(data.get("phy")) or _ifname(_config(data).get("phy")) or _ifname(radio)
    return (device, "") if device else ("", "所选无线电没有可用的扫描设备")


def _available_channels(device):
    frequencies = _iwinfo("freqlist", device)
    if not frequencies or not isinstance(frequencies.get("results"), list):
        return None
    channels = {
        _channel(entry.get("channel")) for entry in frequencies["results"]
        if isinstance(entry, dict)
    }
    channels.discard(None)
    return channels or None


def select_candidates(section, radio, profile):
    """Scan once and return the strongest three compatible APs on one radio."""
    profile = profile if isinstance(profile, dict) else {}
    ssid = profile.get("ssid")
    if not isinstance(ssid, str) or not ssid:
        return [], "未配置目标 SSID"
    encryption = str(profile.get("encryption") or "none").strip().lower()
    if encryption not in _ENCRYPTIONS:
        return [], "此安全方式暂不支持 AP 排序，请使用系统自动选择"
    status = _wireless_status()
    if status is None:
        return [], "无法读取无线状态，请使用系统自动选择"
    device, reason = _scan_device(status, section, radio)
    if not device:
        return [], reason
    # A STA can move its radio's AP to the associated channel. The configured
    # AP channel is not a STA capability limit; use this device's frequency list.
    channels = _available_channels(device)
    scan = _iwinfo("scan", device, timeout=SCAN_TIMEOUT)
    if scan is None or not isinstance(scan.get("results"), list):
        return [], "iwinfo 扫描不可用或超时，请使用系统自动选择"
    candidates = {}
    for entry in scan["results"]:
        if not isinstance(entry, dict) or entry.get("ssid") != ssid:
            continue
        if _mode(entry) not in _AP_MODES or not valid_bssid(entry.get("bssid")):
            continue
        signal, channel = _signal(entry.get("signal")), _channel(entry.get("channel"))
        if signal is None or channel is None or (channels is not None and channel not in channels):
            continue
        if not _security_matches(encryption, entry.get("encryption")):
            continue
        bssid = entry["bssid"].strip().lower()
        candidate = {"bssid": bssid, "ssid": ssid, "signal": signal, "channel": channel}
        previous = candidates.get(bssid)
        if previous is None or (-signal, channel) < (-previous["signal"], previous["channel"]):
            candidates[bssid] = candidate
    selected = sorted(candidates.values(), key=lambda item: (-item["signal"], item["bssid"]))
    if not selected:
        return [], "未找到同 SSID、信道和安全方式兼容的 AP，请使用系统自动选择"
    return selected[:MAX_CANDIDATES], ""


def _association(ifname, ssid, bssid, signal, channel):
    signal, channel = _signal(signal), _channel(channel)
    if not isinstance(ssid, str) or not ssid or not valid_bssid(bssid):
        return {}
    # A measured BSSID proves association even if this driver cannot report
    # signal/channel. Preserve that evidence; unknown metrics remain None.
    return {"ifname": ifname, "ssid": ssid, "bssid": bssid.strip().lower(),
            "signal": signal, "channel": channel}


def _frequency_channel(frequency):
    if frequency == 2484:
        return 14
    if frequency == 5935:
        return 2
    for low, high, base in ((2412, 2472, 2407), (5005, 5895, 5000), (5955, 7115, 5950)):
        if low <= frequency <= high and (frequency - base) % 5 == 0:
            return int((frequency - base) // 5)
    return None


def _iw_link_association(ifname):
    try:
        ok, output = run_cmd(["iw", "dev", ifname, "link"], timeout=READ_TIMEOUT)
    except (OSError, UnicodeError):
        return None
    if not ok or not isinstance(output, str):
        return None
    if re.search(r"^Not connected\.?\s*$", output, re.I | re.M):
        return {}
    connected = re.search(r"^Connected to ([0-9a-f:]{17})(?: \(on ([^)]+)\))?\s*$", output, re.I | re.M)
    if not connected:
        return None
    if connected.group(2) and connected.group(2) != ifname:
        return {}
    ssid = re.search(r"^\s*SSID: (.*)$", output, re.M)
    signal = re.search(r"^\s*signal:\s*(-?\d+(?:\.\d+)?)\s+dBm\s*$", output, re.M)
    frequency = re.search(r"^\s*freq:\s*(\d+(?:\.\d+)?)\s*$", output, re.M)
    if not ssid:
        return {}
    return _association(
        ifname, ssid.group(1), connected.group(1), signal.group(1) if signal else None,
        _frequency_channel(float(frequency.group(1))) if frequency else None,
    )


def _interface_mac(ifname):
    try:
        with open("/sys/class/net/%s/address" % ifname, encoding="ascii") as source:
            address = source.read().strip().lower()
    except (OSError, UnicodeError):
        return ""
    return address if valid_bssid(address) else ""


def read_association(section):
    """Return only measured association data for the mapped target STA."""
    status = _wireless_status()
    if status is None or not section:
        return {}
    matches = _target_interfaces(status, section)
    if len(matches) != 1:
        return {}
    radio, target = matches[0]
    if (_disabled(status[radio]) or _disabled(target)
            or _mode(_config(target)) not in _STA_MODES):
        return {}
    ifname = _ifname(target.get("ifname"))
    if not ifname:
        return {}
    association = _iw_link_association(ifname)
    if association is not None:
        return association
    # Some iwinfo backends expose the local STA MAC as "bssid" in Client mode.
    # Without a known local address, that fallback cannot prove association.
    local_mac = _interface_mac(ifname)
    if not local_mac:
        return {}
    info = _iwinfo("info", ifname)
    if info and _mode(info):
        if _mode(info) not in {"client", "station"}:
            return {}
        association = _association(
            ifname, info.get("ssid"), info.get("bssid"), info.get("signal"), info.get("channel")
        )
        if association and association["bssid"] != local_mac:
            return association
    return {}
