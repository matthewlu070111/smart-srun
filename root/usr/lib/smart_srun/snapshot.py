"""
运行时快照 -- 收集当前无线状态、连通性、在线身份信息。

从 daemon.py 中提取，消除 orchestrator→daemon 的循环依赖。
"""

import time

from config import (
    CONNECTIVITY_CACHE_SECONDS,
    campus_uses_wired,
    load_runtime_state,
)
from network import (
    get_ipv4_from_network_interface,
    test_internet_connectivity,
    test_portal_reachability,
)
from wireless import (
    get_network_interface_from_sta_section,
    get_runtime_sta_section,
    get_sta_profile_from_section,
    parse_wireless_iface_data,
)
import srun_auth
from school_runtime import build_app_context


def build_runtime_snapshot(cfg, state=None):
    app_ctx = build_app_context(cfg)
    runtime = app_ctx["runtime"]
    data = parse_wireless_iface_data()
    section = get_runtime_sta_section(cfg, data)
    profile = get_sta_profile_from_section(section, data) if section else {}
    ssid = str(profile.get("ssid", "")).strip()
    bssid = str(profile.get("bssid", "")).strip().lower()
    net = get_network_interface_from_sta_section(section, data) if section else None
    ip = get_ipv4_from_network_interface(net) if net else None
    previous = state if state is not None else load_runtime_state()
    wired_mode = campus_uses_wired(cfg)
    wan_ip = get_ipv4_from_network_interface("wan") if wired_mode else None
    wired_online = False

    if wired_mode and wan_ip:
        ssid = "有线接入"
        bssid = ""
        net = "wan"
        ip = wan_ip

    connectivity = "未连接"
    connectivity_level = "offline"
    online_account_label = ""
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
                portal_ok, portal_msg = test_portal_reachability(cfg, timeout=2)
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

    if cfg.get("username") and wired_mode and wan_ip:
        try:
            online_now, online_user, _ = runtime.query_online_identity(
                app_ctx, expected_username=cfg.get("username", ""), bind_ip=wan_ip
            )
            if online_now and online_user:
                wired_online = True
                online_account_label = online_user
        except Exception:
            wired_online = False

    if wired_online:
        mode = "campus"
    elif ssid == str(cfg.get("hotspot_ssid", "")).strip() and ssid:
        mode = "hotspot"
    elif ssid == str(cfg.get("campus_ssid", "")).strip() and ssid:
        mode = "campus"
    else:
        mode = "unknown"

    if mode != "hotspot" and cfg.get("username") and not wired_online:
        try:
            online_now, online_user, _ = runtime.query_online_identity(
                app_ctx, expected_username=cfg.get("username", "")
            )
            if online_now and online_user:
                online_account_label = online_user
        except Exception:
            online_account_label = ""

    if mode == "campus":
        mode_label = "校园网模式（有线）" if wired_mode else "校园网模式"
    elif mode == "hotspot":
        mode_label = "热点模式"
    else:
        mode_label = "未知模式"

    current_campus_access_mode = ""
    if mode == "campus":
        current_campus_access_mode = "wired" if wired_mode and net == "wan" else "wifi"

    return {
        "current_mode": mode,
        "mode": mode,
        "mode_label": mode_label,
        "current_ssid": ssid,
        "current_bssid": bssid,
        "current_iface": str(net or ""),
        "current_ip": str(ip or ""),
        "connectivity": connectivity,
        "connectivity_level": connectivity_level,
        "connectivity_checked_at": int(previous.get("connectivity_checked_at", 0) or 0),
        "campus_account_label": str(cfg.get("campus_account_label", "")),
        "campus_access_mode": str(cfg.get("campus_access_mode", "wifi")),
        "current_campus_access_mode": current_campus_access_mode,
        "online_account_label": online_account_label,
        "hotspot_profile_label": str(cfg.get("hotspot_profile_label", "")),
        "campus_ssid": str(cfg.get("campus_ssid", "")),
        "campus_bssid": str(cfg.get("campus_bssid", "")).strip().lower(),
        "school_runtime_type": str(getattr(runtime, "runtime_type", "")),
        "school_runtime_api_version": int(
            getattr(
                runtime, "runtime_api_version", app_ctx.get("runtime_api_version", 0)
            )
            or 0
        ),
        "school_runtime_short_name": str(getattr(runtime, "SHORT_NAME", "")),
    }
