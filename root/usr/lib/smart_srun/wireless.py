"""
无线管理 -- UCI 无线解析、STA section 管理、SSID 切换、failover。

依赖 network.py（run_cmd、IP 工具）和 config.py（常量、策略查询）。
不知道 SRun 协议的存在。
"""

import re
import time

from config import (
    append_log,
    log,
    campus_uses_wired,
    failover_enabled,
    get_switch_ready_timeout_seconds,
    hotspot_failback_enabled,
    normalize_campus_access_mode,
    normalize_wifi_encryption,
    wifi_key_required,
    SWITCH_DELAY_SECONDS,
    SSID_EXPECTED_RETRY_SECONDS,
    SSID_READY_TIMEOUT_SECONDS,
    apply_default_selection_for_runtime,
)
from network import (
    get_ipv4_from_network_interface,
    get_local_ip_for_target,
    http_get,
    parse_uci_value,
    run_cmd,
    test_internet_connectivity,
    test_portal_reachability,
    wait_for_network_interface_ipv4,
)


# ---------------------------------------------------------------------------
# WiFi helpers
# ---------------------------------------------------------------------------


def split_network_value(value):
    return [x for x in str(value or "").split() if x]


def _sanitize_uci_value(value):
    return (
        str(value or "").strip().replace("\n", "").replace("\r", "").replace("\x00", "")
    )


# ---------------------------------------------------------------------------
# UCI wireless data parsing
# ---------------------------------------------------------------------------


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
            "bssid",
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


# ---------------------------------------------------------------------------
# STA section queries
# ---------------------------------------------------------------------------


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


def get_runtime_sta_section(cfg=None, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    active = get_active_sta_section(cfg, data)
    if active:
        return active

    known_ssids = []
    hotspot_ssid = str((cfg or {}).get("hotspot_ssid", "")).strip()
    campus_ssid = str((cfg or {}).get("campus_ssid", "")).strip()
    if hotspot_ssid:
        known_ssids.append(hotspot_ssid)
    if campus_ssid:
        known_ssids.append(campus_ssid)

    for sec in get_enabled_sta_sections(data):
        profile = get_sta_profile_from_section(sec, data)
        if str(profile.get("ssid", "")).strip() in known_ssids:
            return sec

    for sec in get_sta_sections(data):
        profile = get_sta_profile_from_section(sec, data)
        if str(profile.get("ssid", "")).strip() in known_ssids:
            return sec

    enabled = get_enabled_sta_sections(data)
    if enabled:
        return enabled[0]
    sections = get_sta_sections(data)
    return sections[0] if sections else None


def detect_runtime_mode(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    section = get_runtime_sta_section(cfg, data)
    profile = get_sta_profile_from_section(section, data) if section else {}
    ssid = str(profile.get("ssid", "")).strip()
    if ssid and ssid == str(cfg.get("hotspot_ssid", "")).strip():
        return "hotspot"
    if campus_uses_wired(cfg) and get_ipv4_from_network_interface("wan"):
        return "campus"
    if not section:
        return "unknown"
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
        "bssid": str(opts.get("bssid", "")).strip().lower(),
        "encryption": normalize_wifi_encryption(opts.get("encryption", "none")),
        "key": str(opts.get("key", "")).strip(),
    }


# ---------------------------------------------------------------------------
# Radio helpers
# ---------------------------------------------------------------------------


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


def get_available_wifi_radios(wireless_data=None):
    radios = []
    bands = parse_radio_bands()
    for radio in sorted(bands.keys(), reverse=True):
        radios.append(radio)

    if radios:
        return radios

    ok, out = run_cmd(["uci", "show", "wireless"])
    if not ok or not out:
        return []

    seen = set()
    for line in out.splitlines():
        match = re.match(r"^wireless\.(radio\d+)\.=wifi-device$", line.strip())
        if not match:
            continue
        radio = match.group(1)
        if radio not in seen:
            radios.append(radio)
            seen.add(radio)
    return radios


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


# ---------------------------------------------------------------------------
# Managed STA sections
# ---------------------------------------------------------------------------


def get_managed_sta_sections(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    managed = []
    preferred = str((cfg or {}).get("sta_iface", "")).strip()
    known_ssids = set()
    known_ssids.add(str((cfg or {}).get("campus_ssid", "")).strip())
    known_ssids.add(str((cfg or {}).get("hotspot_ssid", "")).strip())

    for item in list((cfg or {}).get("campus_accounts", []) or []):
        if isinstance(item, dict):
            known_ssids.add(str(item.get("ssid", "")).strip())

    for item in list((cfg or {}).get("hotspot_profiles", []) or []):
        if isinstance(item, dict):
            known_ssids.add(str(item.get("ssid", "")).strip())

    known_ssids.discard("")

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


# ---------------------------------------------------------------------------
# Network interface creation
# ---------------------------------------------------------------------------


def ensure_network_interface(name="wwan"):
    iface = str(name or "wwan").strip() or "wwan"
    ok, out = run_cmd(["uci", "-q", "get", "network.%s" % iface])
    if ok and out:
        proto_ok, proto_out = run_cmd(["uci", "-q", "get", "network.%s.proto" % iface])
        if proto_ok and str(proto_out or "").strip():
            return True, iface, ""

    msgs = []
    for cmd in [
        ["uci", "set", "network.%s=interface" % iface],
        ["uci", "set", "network.%s.proto=dhcp" % iface],
    ]:
        c_ok, c_msg = run_cmd(cmd)
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

    commit_ok, commit_msg = run_cmd(["uci", "commit", "network"])
    if (not commit_ok) and commit_msg:
        msgs.append(commit_msg)

    reload_ok, reload_msg = run_cmd(["/etc/init.d/network", "reload"])
    if (not reload_ok) and reload_msg:
        msgs.append(reload_msg)

    if msgs:
        return False, iface, "；".join(msgs)
    return True, iface, "已自动创建网络接口 %s" % iface


# ---------------------------------------------------------------------------
# STA creation
# ---------------------------------------------------------------------------


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

    ssid = _sanitize_uci_value(profile.get("ssid", ""))
    bssid = _sanitize_uci_value(profile.get("bssid", "")).lower()
    encryption = normalize_wifi_encryption(profile.get("encryption", "none"))
    key = _sanitize_uci_value(profile.get("key", ""))

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

    if bssid:
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.bssid=%s" % (section, bssid)])
        if not c_ok and c_msg:
            msgs.append(c_msg)
    else:
        run_cmd(["uci", "-q", "delete", "wireless.%s.bssid" % section])

    if msgs:
        return section, "；".join(msgs)
    return section, ""


# ---------------------------------------------------------------------------
# Profile matching and switching
# ---------------------------------------------------------------------------


def commit_reload_wireless():
    ok1, msg1 = run_cmd(["uci", "commit", "wireless"])
    ok2, msg2 = run_cmd(["wifi", "reload"])
    if ok1 and ok2:
        return True, ""
    return False, "；".join([x for x in [msg1, msg2] if x])


def build_expected_profile(cfg, expect_hotspot):
    prefix = "hotspot" if expect_hotspot else "campus"
    return {
        "access_mode": "wifi"
        if expect_hotspot
        else normalize_campus_access_mode(cfg.get("campus_access_mode", "wifi")),
        "ssid": str(cfg.get(prefix + "_ssid", "")).strip(),
        "bssid": str(cfg.get(prefix + "_bssid", "")).strip().lower(),
        "encryption": normalize_wifi_encryption(
            cfg.get(prefix + "_encryption", "none")
        ),
        "key": str(cfg.get(prefix + "_key", "")).strip(),
        "label": "热点" if expect_hotspot else "校园网",
    }


def profiles_match(current, expected):
    if str(current.get("ssid", "")).strip() != str(expected.get("ssid", "")).strip():
        return False

    expected_bssid = str(expected.get("bssid", "")).strip().lower()
    current_bssid = str(current.get("bssid", "")).strip().lower()
    if expected_bssid and current_bssid != expected_bssid:
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


def _find_sta_by_profile(profile, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    target_ssid = str((profile or {}).get("ssid", "")).strip()
    target_bssid = str((profile or {}).get("bssid", "")).strip().lower()
    if not target_ssid:
        return None
    for sec in sorted(data.keys()):
        opts = data[sec]
        if str(opts.get("mode", "")).strip().lower() != "sta":
            continue
        if str(opts.get("ssid", "")).strip() != target_ssid:
            continue
        if target_bssid and str(opts.get("bssid", "")).strip().lower() != target_bssid:
            continue
        return sec
    return None


def _set_sta_profile_uci(section, profile):
    sec = str(section or "").strip()
    if not sec:
        return False, "未配置 STA 接口节。"

    ssid = _sanitize_uci_value(profile.get("ssid", ""))
    bssid = _sanitize_uci_value(profile.get("bssid", "")).lower()
    encryption = normalize_wifi_encryption(profile.get("encryption", "none"))
    key = _sanitize_uci_value(profile.get("key", ""))

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
        "wireless.%s.jxnu_auto=1" % sec,
    ]:
        c_ok, c_msg = run_cmd(["uci", "set", arg])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

    if bssid:
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.bssid=%s" % (sec, bssid)])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)
    else:
        run_cmd(["uci", "-q", "delete", "wireless.%s.bssid" % sec])

    if wifi_key_required(encryption):
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.key=%s" % (sec, key)])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)
    else:
        run_cmd(["uci", "-q", "delete", "wireless.%s.key" % sec])

    return ok, "；".join([x for x in msgs if x])


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


def apply_sta_profile(cfg, section, profile, wireless_data=None):
    ok, msg = _set_sta_profile_uci(section, profile)
    if not ok:
        return ok, msg
    ok2, msg2 = activate_sta_section(cfg, section, wireless_data)
    if not ok2:
        parts = [x for x in [msg, msg2] if x]
        return False, "；".join(parts)
    return True, msg


def disable_managed_sta_sections(cfg, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    managed = get_managed_sta_sections(cfg, data)
    if not managed:
        return True, ""

    msgs = []
    ok = True
    for sec in sorted(set(managed)):
        c_ok, c_msg = run_cmd(["uci", "set", "wireless.%s.disabled=1" % sec])
        ok = ok and c_ok
        if (not c_ok) and c_msg:
            msgs.append(c_msg)

    ok2, msg2 = commit_reload_wireless()
    ok = ok and ok2
    if msg2:
        msgs.append(msg2)
    return ok, "；".join([x for x in msgs if x])


# ---------------------------------------------------------------------------
# Radio selection helpers
# ---------------------------------------------------------------------------


def choose_fallback_radio(cfg, expect_hotspot, wireless_data=None):
    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()

    explicit = str(
        (cfg or {}).get("hotspot_radio" if expect_hotspot else "campus_radio", "")
    ).strip()
    if explicit:
        return explicit

    active = get_active_sta_section(cfg, data)
    active_radio = get_radio_for_section(active, data) if active else ""
    if active_radio:
        return active_radio

    target = build_expected_profile(cfg, expect_hotspot)
    existing = _find_sta_by_profile(target, data)
    existing_radio = get_radio_for_section(existing, data) if existing else ""
    if existing_radio:
        return existing_radio

    available = get_available_wifi_radios(data)
    if not available:
        return ""
    if "radio1" in available:
        return "radio1"
    if "radio0" in available:
        return "radio0"
    return available[0]


def get_preferred_profile_radio(cfg, expect_hotspot, wireless_data=None):
    key = "hotspot_radio" if expect_hotspot else "campus_radio"
    radio = str((cfg or {}).get(key, "")).strip()
    if not radio:
        return choose_fallback_radio(cfg, expect_hotspot, wireless_data)

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    bands = parse_radio_bands()
    if radio in bands:
        return radio

    devices = set()
    for opts in data.values():
        device = str(opts.get("device", "")).strip()
        if device:
            devices.add(device)
    if radio in devices:
        return radio
    return choose_fallback_radio(cfg, expect_hotspot, wireless_data)


def get_preferred_hotspot_radio(cfg, wireless_data=None):
    return get_preferred_profile_radio(cfg, True, wireless_data)


# ---------------------------------------------------------------------------
# Wireless prerequisites
# ---------------------------------------------------------------------------


def ensure_runtime_wireless_prerequisites(cfg, expect_hotspot, wireless_data=None):
    if (not expect_hotspot) and campus_uses_wired(cfg):
        data = (
            wireless_data if wireless_data is not None else parse_wireless_iface_data()
        )
        wan_ip = get_ipv4_from_network_interface("wan")
        if wan_ip:
            return True, "检测到有线校园网入口（wan=%s）" % wan_ip, data
        return (
            False,
            "当前校园网账号已设为有线接入模式，但 WAN 口还没有可用 IPv4。",
            data,
        )

    data = wireless_data if wireless_data is not None else parse_wireless_iface_data()
    radios = get_available_wifi_radios(data)
    if not radios:
        return False, "当前路由器未发现可用无线射频，请先确认无线功能已启用。", data

    target = build_expected_profile(cfg, expect_hotspot)
    if not target.get("ssid"):
        return False, "%s SSID 未配置。" % target["label"], data
    if wifi_key_required(target.get("encryption", "none")) and not target.get("key"):
        return False, "%s 需要密码，但当前配置为空。" % target["label"], data

    ok, _, message = ensure_network_interface("wwan")
    data = parse_wireless_iface_data()
    if not ok:
        return (
            False,
            "已尝试自动创建网络接口 wwan，但失败：%s" % (message or "未知错误"),
            data,
        )
    return True, message, data


# ---------------------------------------------------------------------------
# STA section selection
# ---------------------------------------------------------------------------


def select_sta_section(cfg, expect_hotspot, base_section, target, wireless_data):
    existing = _find_sta_by_profile(target, wireless_data)
    preferred_radio = get_preferred_profile_radio(cfg, expect_hotspot, wireless_data)
    if not preferred_radio:
        return existing or base_section, "未找到合适的无线频段，已尝试自动选择但失败。"

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
        return (
            None,
            create_msg
            or (
                "当前没有可用的无线接口，且未能在 %s 上自动创建 STA 接口节"
                % preferred_radio
            ),
        )
    return created, create_msg


# ---------------------------------------------------------------------------
# Wait helpers
# ---------------------------------------------------------------------------


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


# ---------------------------------------------------------------------------
# High-level switching
# ---------------------------------------------------------------------------


def switch_sta_profile(cfg, expect_hotspot):
    cfg, _, _ = apply_default_selection_for_runtime(expect_hotspot, "执行无线切换前")
    data = parse_wireless_iface_data()
    ready_ok, ready_msg, data = ensure_runtime_wireless_prerequisites(
        cfg, expect_hotspot, data
    )
    if not ready_ok:
        return False, ready_msg
    named_ok, named_result = ensure_named_managed_sta_sections(cfg, data)
    if not named_ok:
        return False, named_result or "整理无线接口节名称失败。"
    if named_result:
        data = parse_wireless_iface_data()

    base_section = get_sta_section(cfg, data)
    target = build_expected_profile(cfg, expect_hotspot)

    section, select_msg = select_sta_section(
        cfg, expect_hotspot, base_section, target, data
    )
    if not section:
        return (
            False,
            select_msg
            or "当前路由器还没有可用于连接目标网络的无线接口，且自动创建失败。",
        )

    data_after_select = parse_wireless_iface_data()
    ok, msg = apply_sta_profile(cfg, section, target, data_after_select)
    if (not ok) and msg:
        return False, msg
    if not ok:
        return False, "写入无线配置失败。"

    settle_delay = min(SWITCH_DELAY_SECONDS, get_switch_ready_timeout_seconds(cfg))
    if settle_delay > 0:
        time.sleep(settle_delay)

    refreshed_data = parse_wireless_iface_data()
    radio = get_radio_for_section(section, refreshed_data)
    bands = parse_radio_bands()
    bl = band_label(bands.get(radio, ""))

    if not expect_hotspot:
        _, ip = wait_for_sta_ipv4(
            section, timeout_seconds=get_switch_ready_timeout_seconds(cfg)
        )
        if ip:
            portal_ok, portal_detail = test_portal_reachability(cfg)
            if portal_ok:
                conn_hint = "网关可达"
            else:
                conn_hint = "网关不可达"
                if portal_detail:
                    conn_hint = conn_hint + ": " + portal_detail
            log(
                "INFO",
                "switch_campus_done",
                "campus switch complete",
                radio=radio or "?",
                band=bl,
                portal=conn_hint,
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
        log(
            "WARN",
            "switch_campus_no_ip",
            "no IPv4 after campus switch",
            radio=radio or "?",
            band=bl,
        )
        return False, "已切换为%s配置但未获取到IPv4地址（%s %s）" % (
            target["label"],
            radio or "?",
            bl,
        )

    _, ip = wait_for_sta_ipv4(
        section, timeout_seconds=get_switch_ready_timeout_seconds(cfg)
    )
    if ip:
        dns_ok, _ = test_internet_connectivity()
        conn_hint = "连通" if dns_ok else "不通"
        if (not dns_ok) and hotspot_failback_enabled(cfg):
            log(
                "INFO",
                "hotspot_failback",
                "hotspot no internet, rolling back to campus",
            )
            rollback_ok, rollback_msg = switch_to_campus(cfg)
            if rollback_ok:
                return False, "热点未确认连通，已自动回切校园网：%s" % (
                    rollback_msg or "回切成功"
                )
            return False, "热点未确认连通，自动回切校园网失败：%s" % (
                rollback_msg or "未知错误"
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

    if hotspot_failback_enabled(cfg):
        log("WARN", "hotspot_failback", "hotspot no IPv4, rolling back to campus")
        rollback_ok, rollback_msg = switch_to_campus(cfg)
        if rollback_ok:
            return False, "热点未获取到 IPv4，已自动回切校园网：%s" % (
                rollback_msg or "回切成功"
            )
        return False, "热点未获取到 IPv4，自动回切校园网失败：%s" % (
            rollback_msg or "未知错误"
        )

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
    if campus_uses_wired(cfg):
        disable_managed_sta_sections(cfg, parse_wireless_iface_data())
        wan_ip = wait_for_network_interface_ipv4(
            "wan", timeout_seconds=get_switch_ready_timeout_seconds(cfg)
        )
        if wan_ip:
            return True, "已切换为有线校园网模式（wan, %s）" % wan_ip
        return False, "已切到有线校园网模式，但 WAN 口暂未获取到 IPv4 地址"
    return switch_sta_profile(cfg, expect_hotspot=False)


# ---------------------------------------------------------------------------
# Failover: ensure expected profile
# ---------------------------------------------------------------------------


def ensure_expected_profile(cfg, expect_hotspot, last_switch_ts=0):
    if not failover_enabled(cfg):
        return True, "", last_switch_ts

    if (not expect_hotspot) and campus_uses_wired(cfg):
        wan_ip = get_ipv4_from_network_interface("wan")
        if wan_ip:
            return True, "", last_switch_ts
        return False, "有线校园网未就绪，WAN 口尚未获取到 IPv4。", last_switch_ts

    data = parse_wireless_iface_data()
    section = get_sta_section(cfg, data)
    if not section:
        return False, "未找到可用的 STA 接口节。", last_switch_ts

    expected = build_expected_profile(cfg, expect_hotspot)
    if not expected["ssid"]:
        return False, "%s SSID 未配置。" % expected["label"], last_switch_ts
    if wifi_key_required(expected["encryption"]) and not expected["key"]:
        return False, "%s 配置缺少密码。" % expected["label"], last_switch_ts

    existing = _find_sta_by_profile(expected, data)
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
