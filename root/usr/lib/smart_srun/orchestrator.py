"""
操作编排 -- 手动登录/登出全流程、重试策略、WiFi 前置准备。

依赖 srun_auth（认证）、wireless（WiFi切换）、config（配置/策略）。
"""

import math
import time

from config import (
    ACTION_FILE,
    append_log,
    log,
    apply_default_selection_for_runtime,
    backoff_enabled,
    begin_manual_login_service_guard,
    campus_uses_wired,
    get_manual_terminal_check_attempts,
    get_manual_terminal_check_interval_seconds,
    get_manual_terminal_check_label,
    get_retry_cooldown_seconds,
    get_retry_max_cooldown_seconds,
    get_switch_ready_timeout_seconds,
    in_quiet_window,
    load_config,
    load_json_file,
    localize_error,
    quiet_window_label,
    restore_manual_login_service_guard,
)
from network import (
    HTTP_EXCEPTIONS,
    resolve_bind_ip,
    test_internet_connectivity,
)
from wireless import (
    detect_runtime_mode,
    disable_managed_sta_sections,
    ensure_expected_profile,
    parse_wireless_iface_data,
    switch_to_campus,
    wait_for_network_interface_ipv4,
)
import srun_auth
from school_runtime import build_app_context
from snapshot import build_runtime_snapshot


# ---------------------------------------------------------------------------
# 退避计算
# ---------------------------------------------------------------------------


def connectivity_mode_matches(snapshot, cfg, require_ssid=False):
    mode = str(cfg.get("connectivity_check_mode", "internet")).strip().lower()
    current_ssid = str(snapshot.get("current_ssid", "")).strip()
    target_ssid = str(cfg.get("campus_ssid", "")).strip()
    if campus_uses_wired(cfg):
        require_ssid = False
    ssid_ok = (not require_ssid) or (current_ssid and current_ssid == target_ssid)
    if not ssid_ok:
        return False

    level = str(snapshot.get("connectivity_level", "offline")).strip().lower()
    if mode == "ssid":
        return bool(ssid_ok)
    if mode == "portal":
        return level in ("online", "portal")
    return level == "online"


def calc_backoff_delay_seconds(cfg, failure_index):
    n_val = max(int(failure_index), 1)
    base = get_retry_cooldown_seconds(cfg)
    max_duration = get_retry_max_cooldown_seconds(cfg)
    delay = base * math.pow(2, max(n_val - 1, 0))
    if max_duration > 0:
        delay = min(delay, max_duration)
    return delay


# ---------------------------------------------------------------------------
# 重试包装
# ---------------------------------------------------------------------------


def _pending_runtime_action():
    payload = load_json_file(ACTION_FILE)
    if not isinstance(payload, dict):
        return ""
    return str(payload.get("action", "")).strip()


def _interruptible_sleep(total_seconds):
    remaining = max(float(total_seconds), 0.0)
    while remaining > 0:
        chunk = min(remaining, 2.0)
        time.sleep(chunk)
        remaining -= chunk
        if _pending_runtime_action():
            return False
    return True


def run_once_with_retry(cfg, ignore_service_disabled=False):
    ok, message = srun_auth.run_once_safe(cfg)
    if ok:
        return True, message

    log("ERROR", "login_failed", "first attempt failed", reason=message)

    if not backoff_enabled(cfg):
        log(
            "INFO",
            "retry_scheduled",
            "single retry scheduled",
            delay=int(get_retry_cooldown_seconds(cfg)),
            backoff="off",
        )
        time.sleep(get_retry_cooldown_seconds(cfg))
        retry_ok, retry_message = srun_auth.run_once_safe(cfg)
        if retry_ok:
            log("INFO", "retry_success", "single retry succeeded")
            return True, "重试成功"
        log("ERROR", "retry_failed", "single retry failed", reason=retry_message)
        return False, retry_message

    retries = 0
    failures = 1

    while True:
        runtime_cfg = load_config()
        max_retries = int(runtime_cfg.get("backoff_max_retries", 0))

        if runtime_cfg.get("enabled") != "1" and not ignore_service_disabled:
            return False, "服务已禁用，停止重试"
        if not backoff_enabled(runtime_cfg):
            return False, message
        if in_quiet_window(runtime_cfg):
            return False, "进入夜间停用时段，停止重试"
        if max_retries > 0 and retries >= max_retries:
            return False, message
        pending_action = _pending_runtime_action()
        if pending_action:
            log(
                "INFO",
                "retry_interrupted",
                "pending action detected, aborting retry loop",
                pending_action=pending_action,
            )
            return False, "检测到待处理操作，中断重试"

        delay = calc_backoff_delay_seconds(runtime_cfg, failures)
        log("INFO", "retry_scheduled", attempt=retries + 1, delay=round(delay, 1))
        if delay > 0 and not _interruptible_sleep(delay):
            pending_action = _pending_runtime_action()
            log(
                "INFO",
                "retry_interrupted",
                "pending action detected during retry wait",
                pending_action=pending_action,
            )
            return False, "检测到待处理操作，中断重试"

        retry_ok, retry_message = srun_auth.run_once_safe(runtime_cfg)
        retries += 1
        if retry_ok:
            log("INFO", "retry_success", attempt=retries)
            return True, "重试成功（第 %d 次）" % retries

        log("ERROR", "retry_failed", attempt=retries, reason=retry_message)
        message = retry_message
        failures += 1


def run_once_manual(cfg):
    ok, message = srun_auth.run_once_safe(cfg)
    if ok:
        return True, message
    log("ERROR", "manual_login_failed", "manual login stage failed", reason=message)
    return False, message


# ---------------------------------------------------------------------------
# 安静时段 / 状态查询
# ---------------------------------------------------------------------------


def quiet_connection_state(cfg, urls=None):
    app_ctx = build_app_context(cfg)
    runtime_mode = detect_runtime_mode(cfg)
    if runtime_mode == "hotspot":
        return "热点已连接"

    if not cfg.get("username"):
        return "未连接"

    if urls is None:
        urls = app_ctx["runtime"].build_urls(cfg["base_url"])

    try:
        online, _ = app_ctx["runtime"].query_online_status(app_ctx)
        return "在线" if online else "未连接"
    except Exception:
        return "未连接"


def default_run_status(app_ctx):
    cfg = app_ctx["cfg"]
    mode_hint = ""
    from config import failover_enabled

    if failover_enabled(cfg):
        mode_hint = "（校园网SSID: %s，热点SSID: %s）" % (
            cfg.get("campus_ssid", "jxnu_stu"),
            cfg.get("hotspot_ssid", "未设置"),
        )

    if in_quiet_window(cfg):
        state = quiet_connection_state(cfg)
        return False, "夜间停用（%s）" % state + mode_hint

    if not cfg["username"]:
        return False, "未配置学工号" + mode_hint

    online, message = app_ctx["runtime"].query_online_status(
        app_ctx, expected_username=cfg["username"]
    )
    return online, localize_error(message) + mode_hint


def run_status(cfg):
    app_ctx = build_app_context(cfg)
    return app_ctx["runtime"].status(app_ctx)


def default_run_quiet_logout(app_ctx):
    cfg = app_ctx["cfg"]
    runtime = app_ctx["runtime"]

    if cfg.get("force_logout_in_quiet") != "1":
        state = quiet_connection_state(cfg)
        return True, "夜间停用（%s）" % state

    if not cfg["username"]:
        return False, "夜间停用下线失败: 未配置学工号"

    ok, message = runtime.logout_once(app_ctx)
    if ok:
        offline, offline_msg = srun_auth.wait_for_logout_status(
            app_ctx,
            None,
            cfg,
        )
        if offline:
            return True, "夜间停用下线成功"
        return (
            False,
            "夜间停用下线失败: 请求已发送，但当前仍在线（%s）"
            % localize_error(offline_msg),
        )
    return False, "夜间停用下线失败: " + localize_error(message)


def run_quiet_logout(cfg):
    app_ctx = build_app_context(cfg)
    return app_ctx["runtime"].quiet_logout(app_ctx)


# ---------------------------------------------------------------------------
# WiFi 前置准备
# ---------------------------------------------------------------------------


def prepare_campus_for_login(cfg):
    ok, msg, _ = ensure_expected_profile(cfg, expect_hotspot=False, last_switch_ts=0)
    if ok:
        return True, ""
    return False, msg


# ---------------------------------------------------------------------------
# 手动登出
# ---------------------------------------------------------------------------


def run_manual_logout(cfg, override_user_id=None):
    if not cfg["username"]:
        return False, "未配置学工号"

    app_ctx = build_app_context(cfg)
    runtime = app_ctx["runtime"]
    urls = runtime.build_urls(cfg["base_url"])
    bip = resolve_bind_ip(urls["init_url"], cfg)

    try:
        online_now, online_user, _ = runtime.query_online_identity(app_ctx, bind_ip=bip)
        logout_user = str(override_user_id or online_user or "").strip()
        if not online_now or not logout_user:
            return True, "已离线"

        logout_cfg = dict(cfg)
        logout_cfg["user_id"] = logout_user
        logout_cfg["username"] = logout_user
        log(
            "INFO",
            "logout_request",
            "sending logout request",
            account=logout_user,
        )
        ok, message = runtime.logout_once(
            build_app_context(logout_cfg, runtime=runtime),
            override_user_id=logout_user,
            bind_ip=bip,
        )
        if ok:
            log("INFO", "logout_request", "logout request accepted", result=message)
            max_attempts = get_manual_terminal_check_attempts(cfg)
            interval_seconds = get_manual_terminal_check_interval_seconds(cfg)
            ready_ok, ready_msg = wait_for_manual_logout_ready(
                build_app_context(logout_cfg, runtime=runtime),
                logout_cfg,
                bind_ip=bip,
                attempts=max_attempts,
                delay_seconds=interval_seconds,
            )
            if ready_ok:
                log("INFO", "logout_success", account=logout_user)
                return True, "登出成功"
            log("WARN", "logout_verify_failed", attempts=max_attempts, result=ready_msg)
            return False, "登出失败：%s" % ready_msg

        localized = localize_error(message)
        log("ERROR", "logout_failed", reason=localized)
        try:
            online, online_msg = runtime.query_online_status(app_ctx, bind_ip=bip)
            if not online:
                return True, "已离线"
            return False, "登出失败: " + localize_error(online_msg)
        except Exception:
            return False, "登出失败: " + localized
    except Exception as exc:
        return False, "登出失败: " + localize_error(exc)


def wait_for_manual_logout_ready(
    app_ctx, cfg, bind_ip=None, attempts=5, delay_seconds=2
):
    attempts = max(int(attempts), 1)
    last_message = ""
    for idx in range(attempts):
        log("INFO", "status_query", "logout verify check", attempt=idx + 1)
        online, offline_msg = app_ctx["runtime"].query_online_status(
            app_ctx, bind_ip=bind_ip
        )
        if not online:
            return True, "已确认离线"
        last_message = localize_error(offline_msg)

        if idx + 1 < attempts:
            time.sleep(max(int(delay_seconds), 1))

    return False, last_message or "终态校验超时"


# ---------------------------------------------------------------------------
# 手动登录预清理
# ---------------------------------------------------------------------------


def clean_slate_for_manual_login(cfg, online_user=""):
    if campus_uses_wired(cfg):
        if online_user:
            log(
                "INFO",
                "manual_preclean",
                "found online account, logging out",
                account=online_user,
            )
            ok, message = run_manual_logout(cfg, override_user_id=online_user)
            if not ok:
                log(
                    "ERROR",
                    "manual_login_failed",
                    "preclean logout failed",
                    reason=message,
                )
                return False, message
            log(
                "INFO",
                "manual_preclean_done",
                "preclean done: cleared previous online account",
            )

        active_data = parse_wireless_iface_data()
        log("INFO", "manual_preclean", "wired mode: disabling all managed STA sections")
        ok, message = disable_managed_sta_sections(cfg, active_data)
        if not ok:
            log(
                "ERROR",
                "manual_login_failed",
                "preclean failed: could not disable managed STA",
                reason=message or "unknown",
            )
            return False, message or "禁用历史 STA 接口失败"

        log(
            "INFO",
            "manual_preclean_done",
            "wired mode: skipping wireless rebuild, using WAN",
        )
        wan_ip = wait_for_network_interface_ipv4(
            "wan", timeout_seconds=get_switch_ready_timeout_seconds(cfg)
        )
        if not wan_ip:
            return False, "有线校园网模式下，WAN 口尚未获取到 IPv4 地址"
        return True, ""

    active_data = parse_wireless_iface_data()

    if online_user:
        log(
            "INFO",
            "manual_preclean",
            "found online account, logging out",
            account=online_user,
        )
        ok, message = run_manual_logout(cfg, override_user_id=online_user)
        if not ok:
            log(
                "ERROR", "manual_login_failed", "preclean logout failed", reason=message
            )
            return False, message
        log(
            "INFO",
            "manual_preclean_done",
            "preclean done: cleared previous online account",
        )

    log(
        "INFO",
        "manual_preclean",
        "disabling all managed STA sections to clear stale connections",
    )
    ok, message = disable_managed_sta_sections(cfg, active_data)
    if not ok:
        log(
            "ERROR",
            "manual_login_failed",
            "preclean failed: could not disable managed STA",
            reason=message or "unknown",
        )
        return False, message or "禁用历史 STA 接口失败"

    log(
        "INFO",
        "manual_preclean_done",
        "managed STA disabled, rebuilding campus connection",
    )
    ok2, sw_msg = switch_to_campus(cfg)
    if not ok2:
        log(
            "ERROR",
            "manual_login_failed",
            "preclean failed: could not rebuild campus connection",
            reason=sw_msg or "unknown",
        )
        return False, sw_msg or "切换校园网失败"
    log("INFO", "manual_preclean_done", "campus wireless profile rebuilt")

    return True, ""


# ---------------------------------------------------------------------------
# 手动登录终态校验
# ---------------------------------------------------------------------------


def wait_for_manual_login_ready(cfg, attempts=5, delay_seconds=2):
    attempts = max(int(attempts), 1)
    last_message = ""
    ready_label = get_manual_terminal_check_label(cfg)
    wired_mode = campus_uses_wired(cfg)
    app_ctx = build_app_context(cfg)
    runtime = app_ctx["runtime"]
    urls = runtime.build_urls(cfg["base_url"])
    bind_ip = resolve_bind_ip(urls["init_url"], cfg)
    for idx in range(attempts):
        log("INFO", "status_query", "login verify check", attempt=idx + 1)
        snapshot = build_runtime_snapshot(cfg)
        ssid_ok = wired_mode or snapshot.get("current_ssid") == cfg.get("campus_ssid")
        bssid_expect = str(cfg.get("campus_bssid", "")).strip().lower()
        current_bssid = str(snapshot.get("current_bssid", "")).strip().lower()
        bssid_ok = wired_mode or (
            (not bssid_expect) or (not current_bssid) or current_bssid == bssid_expect
        )
        online_ok = connectivity_mode_matches(snapshot, cfg, require_ssid=True)
        auth_online = False
        auth_message = ""
        try:
            auth_online, auth_message = runtime.query_online_status(
                app_ctx, bind_ip=bind_ip
            )
        except Exception as exc:
            auth_online = False
            auth_message = localize_error(exc)

        if wired_mode and auth_online:
            return True, "已切到有线校园网并确认认证在线"
        if ssid_ok and bssid_ok and online_ok:
            if wired_mode:
                return True, "已切到有线校园网并确认%s" % ready_label
            return True, "已关联目标校园网并确认%s" % ready_label
        if (not wired_mode) and ssid_ok and bssid_ok and auth_online:
            return True, "已关联目标校园网并确认认证在线"
        if ssid_ok and online_ok and bssid_expect and not current_bssid:
            return (
                True,
                "已关联目标校园网并确认%s（BSSID 暂未上报，忽略本次终态校验阻塞）"
                % ready_label,
            )
        last_message = "当前 SSID=%s BSSID=%s 连通性=%s" % (
            snapshot.get("current_ssid", "") or "-",
            current_bssid or "-",
            snapshot.get("connectivity", "未知") or "未知",
        )
        if auth_message:
            last_message = last_message + "；认证状态=%s" % auth_message
        if idx + 1 < attempts:
            time.sleep(max(int(delay_seconds), 1))
    return False, last_message


# ---------------------------------------------------------------------------
# 手动登录全流程
# ---------------------------------------------------------------------------


def run_manual_login(cfg):
    service_guard_enabled = False

    try:
        service_guard_enabled, _ = begin_manual_login_service_guard()
        if service_guard_enabled:
            cfg["enabled"] = "0"
            log(
                "INFO",
                "manual_login_start",
                "service guard enabled: daemon paused during manual login",
            )

        cfg, _, _ = apply_default_selection_for_runtime(False, "手动登录前")
        app_ctx = build_app_context(cfg)
        runtime = app_ctx["runtime"]

        try:
            online_now, online_user, _ = runtime.query_online_identity(app_ctx)
        except Exception:
            online_now, online_user = False, ""

        clean_ok, clean_msg = clean_slate_for_manual_login(
            cfg, online_user if online_now else ""
        )
        if not clean_ok:
            return False, clean_msg

        log(
            "INFO",
            "manual_login_start",
            "submitting auth request",
            account=srun_auth.get_logout_username(cfg),
        )
        login_ok, login_msg = run_once_manual(cfg)
        if login_ok:
            log(
                "INFO",
                "manual_login_success",
                "login request accepted, starting verification",
                result=login_msg,
            )
            max_attempts = get_manual_terminal_check_attempts(cfg)
            interval_seconds = get_manual_terminal_check_interval_seconds(cfg)
            ready_ok, ready_msg = wait_for_manual_login_ready(
                cfg, attempts=max_attempts, delay_seconds=interval_seconds
            )
            if ready_ok:
                log("INFO", "manual_login_success", result=ready_msg)
                return True, "登录成功"
            log(
                "ERROR",
                "manual_login_failed",
                "post-login verification failed",
                attempts=max_attempts,
                result=ready_msg,
            )
            return False, "登录后校验失败：%s" % ready_msg

        log("ERROR", "manual_login_failed", "login stage failed", reason=login_msg)
        return False, login_msg
    finally:
        if service_guard_enabled:
            restored, restored_enabled = restore_manual_login_service_guard()
            if restored and restored_enabled == "1":
                log(
                    "INFO",
                    "manual_login_start",
                    "service guard restored: daemon re-enabled",
                )
