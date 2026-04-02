"""
守护循环 -- 守护进程主循环、runtime action 分发、状态管理。

依赖 orchestrator（编排）、wireless（WiFi）、srun_auth（认证）、config（配置）、snapshot（快照）。
"""

import os
import time

from config import (
    ACTION_FILE,
    LOG_FILE,
    append_log,
    build_school_runtime_luci_contract,
    log,
    campus_uses_wired,
    ensure_parent_dir,
    failover_enabled,
    in_quiet_window,
    load_config,
    load_json_file,
    load_json_raw_config,
    load_runtime_state,
    localize_error,
    pop_runtime_action,
    save_runtime_status,
    reconcile_manual_login_service_guard,
    wifi_key_required,
)
from network import (
    HTTP_EXCEPTIONS,
)
from wireless import (
    build_expected_profile,
    detect_runtime_mode,
    ensure_expected_profile,
    switch_to_campus,
)
import orchestrator
import school_runtime
import srun_auth
from snapshot import build_runtime_snapshot


DAEMON_LOCK_FILE = "/var/run/smart_srun/daemon.lock"


# ---------------------------------------------------------------------------
# Daemon state
# ---------------------------------------------------------------------------


def _make_daemon_state():
    return {
        "was_in_quiet": False,
        "quiet_logout_done": False,
        "current_mode": "campus",
        "was_online": False,
        "last_switch_ts": 0,
    }


def load_pending_runtime_action():
    payload = load_json_file(ACTION_FILE)
    return payload if isinstance(payload, dict) else {}


def _build_startup_status_payload():
    runtime_state = load_runtime_state()
    queued_action = load_pending_runtime_action()

    pending_action = str(
        queued_action.get("action") or runtime_state.get("pending_action") or ""
    ).strip()
    action_result = str(runtime_state.get("action_result") or "").strip()
    requested_at = int(
        queued_action.get("requested_at")
        or runtime_state.get("action_started_at")
        or runtime_state.get("last_action_ts")
        or 0
    )

    if pending_action and (queued_action.get("action") or action_result == "pending"):
        return (
            str(runtime_state.get("message") or ("正在执行动作: %s" % pending_action)),
            {
                "last_action": str(runtime_state.get("last_action") or pending_action),
                "last_action_ts": requested_at,
                "action_result": "pending",
                "action_started_at": requested_at,
                "pending_action": pending_action,
            },
        )

    return (
        "守护进程已启动",
        {
            "last_action": "",
            "last_action_ts": 0,
            "action_result": "",
            "action_started_at": 0,
            "pending_action": "",
        },
    )


def _safe_call(fn, *args, **kwargs):
    try:
        return fn(*args, **kwargs)
    except HTTP_EXCEPTIONS as exc:
        return False, "网络错误: " + localize_error(exc)
    except ValueError as exc:
        return False, "响应解析错误: " + localize_error(exc)
    except Exception as exc:
        return False, "错误: " + localize_error(exc)


# ---------------------------------------------------------------------------
# Switch
# ---------------------------------------------------------------------------


def run_switch(cfg, expect_hotspot):
    from wireless import switch_sta_profile

    target = build_expected_profile(cfg, expect_hotspot)
    if (not expect_hotspot) and campus_uses_wired(cfg):
        switched, message = switch_to_campus(cfg)
        if switched:
            return True, "切换成功: " + (message or "")
        return False, "切换失败: " + (message or "未知错误")

    if not target["ssid"]:
        return False, "%s SSID 未配置" % target["label"]
    if wifi_key_required(target["encryption"]) and not target["key"]:
        return False, "%s 配置缺少密码" % target["label"]

    switched, message = switch_sta_profile(cfg, expect_hotspot)
    if switched:
        return True, "切换成功: " + (message or "")
    return False, "切换失败: " + (message or "未知错误")


# ---------------------------------------------------------------------------
# Handle runtime actions (from LuCI)
# ---------------------------------------------------------------------------


def _handle_runtime_action_core(app_ctx, state, action):
    cfg = app_ctx["cfg"]
    action_map = {
        "switch_hotspot": True,
        "switch_campus": False,
    }

    if action == "manual_login":
        ok, message = orchestrator.run_manual_login(cfg)
        return ok, message

    if action == "manual_logout":
        ok, message = orchestrator.run_manual_logout(cfg)
        return ok, message

    if action not in action_map:
        message = "忽略未知动作: %s" % action
        return True, message

    ok, message = run_switch(cfg, expect_hotspot=action_map[action])
    return ok, message


def handle_runtime_action(cfg, state, runtime=None, app_ctx=None):
    payload = pop_runtime_action()
    action = str(payload.get("action", "")).strip()
    if not action:
        return False, ""

    runtime = runtime or school_runtime.resolve_runtime(cfg)
    app_ctx = app_ctx or school_runtime.build_app_context(cfg, runtime=runtime)

    action_started_at = int(time.time())
    save_runtime_status(
        "正在执行动作: %s" % action,
        state,
        last_action=action,
        last_action_ts=action_started_at,
        action_result="pending",
        pending_action=action,
        action_started_at=action_started_at,
        **build_runtime_snapshot(cfg, state),
    )

    ok, message = school_runtime.dispatch_runtime_action(
        runtime, app_ctx, action, state
    )
    action_result = "ok" if ok else "error"
    if message.startswith("忽略未知动作:"):
        action_result = "ignored"
    if action.startswith("switch_") and ok:
        if action == "switch_hotspot":
            state["current_mode"] = "hotspot"
        elif action == "switch_campus":
            state["current_mode"] = "campus"
            state["last_switch_ts"] = 0
    if action.startswith("manual_"):
        log("INFO", "action_result", message, action=action, ok=ok)
    elif action.startswith("switch_"):
        log("INFO", "action_result", message, action=action, ok=ok)
    elif ok:
        log("INFO", "action_result", message, action=action, ok=ok)
    else:
        log("WARN", "action_result", message, action=action, ok=ok)
    save_runtime_status(
        message,
        state,
        last_action=action,
        last_action_ts=int(time.time()),
        action_result=action_result,
        action_started_at=0,
        pending_action="",
        **build_runtime_snapshot(cfg, state),
    )
    return True, message


# ---------------------------------------------------------------------------
# Daemon tick
# ---------------------------------------------------------------------------


def _daemon_tick_quiet(cfg, state, interval):
    mode_msg = ""
    runtime_mode = detect_runtime_mode(cfg)

    if not state["was_in_quiet"]:
        state["quiet_logout_done"] = False

    if state["quiet_logout_done"]:
        conn_state = orchestrator.quiet_connection_state(cfg)
        message = "夜间停用（%s）" % conn_state
    else:
        if runtime_mode == "hotspot":
            state["quiet_logout_done"] = True
            message = "夜间停用（热点已连接）"
        else:
            ok, message = _safe_call(orchestrator.run_quiet_logout, cfg)
            state["quiet_logout_done"] = ok

    if failover_enabled(cfg):
        ssid_ok, ssid_msg, state["last_switch_ts"] = ensure_expected_profile(
            cfg,
            expect_hotspot=True,
            last_switch_ts=state["last_switch_ts"],
        )
        if ssid_ok:
            state["current_mode"] = "hotspot"
        if ssid_msg:
            mode_msg = ssid_msg
        if not ssid_ok:
            state["was_in_quiet"] = True
            state["was_online"] = False
            state["current_mode"] = "hotspot"
            wait_message = "夜间停用（未连接）"
            if message:
                wait_message = wait_message + "；" + message
            if mode_msg:
                wait_message = wait_message + "；" + mode_msg
            return wait_message, min(interval, 60)

    if mode_msg:
        message = message + "；" + mode_msg

    state["was_in_quiet"] = True
    state["was_online"] = False
    return message, min(interval, 60)


def _daemon_tick_active(cfg, state, interval):
    online_interval = interval
    mode_msg = ""

    if state["was_in_quiet"]:
        log("INFO", "quiet_exit", "leaving quiet hours, switching back to campus")
        state["quiet_logout_done"] = False
        state["was_in_quiet"] = False
        state["was_online"] = False
        state["last_switch_ts"] = 0
        if failover_enabled(cfg):
            switched, sw_msg = switch_to_campus(cfg)
            state["current_mode"] = "campus" if switched else "hotspot"
            if sw_msg:
                mode_msg = sw_msg

    if failover_enabled(cfg):
        ready_ok, ready_msg, state["last_switch_ts"] = ensure_expected_profile(
            cfg,
            expect_hotspot=False,
            last_switch_ts=state["last_switch_ts"],
        )
        if ready_ok:
            state["current_mode"] = "campus"
            if ready_msg:
                mode_msg = (mode_msg + "；" if mode_msg else "") + ready_msg
        else:
            state["current_mode"] = "hotspot"
            state["was_online"] = False
            message = "校园网配置未就绪"
            if ready_msg:
                message = message + "；" + ready_msg
            return message, min(interval, 30)

    if failover_enabled(cfg) and state["current_mode"] == "hotspot":
        state["was_online"] = False
        message = "已切换到热点SSID，校园网SSID恢复后将自动切回"
        if mode_msg:
            message = message + "；" + mode_msg
        return message, interval

    srun_profile = srun_auth.get_profile(cfg)
    next_sleep = interval
    try:
        urls = srun_auth.build_urls(cfg)
        online_now = False
        status_message = ""
        if cfg["username"]:
            online_now, status_message = srun_auth.query_online_status(
                srun_profile, urls["rad_user_info_api"], cfg["username"]
            )

        if online_now:
            message = "在线，下一次检测间隔 %d 秒" % online_interval
            state["was_online"] = True
            next_sleep = online_interval
        else:
            if state["was_online"]:
                log(
                    "WARN",
                    "disconnect_detected",
                    "disconnected, reconnecting immediately",
                )
            state["was_online"] = False
            ok, message = orchestrator.run_once_with_retry(cfg)
            state["was_online"] = bool(ok)
            if not ok and status_message:
                message = "%s；状态检测结果: %s" % (message, status_message)
    except HTTP_EXCEPTIONS as exc:
        log(
            "WARN",
            "status_check_error",
            "network error during status check, reconnecting",
            error_type="network",
        )
        state["was_online"] = False
        ok, message = orchestrator.run_once_with_retry(cfg)
        if not ok:
            message = "网络异常: %s；重连结果: %s" % (localize_error(exc), message)
    except ValueError as exc:
        log(
            "WARN",
            "status_check_error",
            "parse error during status check, reconnecting",
            error_type="parse",
        )
        state["was_online"] = False
        ok, message = orchestrator.run_once_with_retry(cfg)
        if not ok:
            message = "解析异常: %s；重连结果: %s" % (localize_error(exc), message)
    except Exception as exc:
        log(
            "WARN",
            "status_check_error",
            "unexpected error during status check, reconnecting",
            error_type="unknown",
        )
        state["was_online"] = False
        ok, message = orchestrator.run_once_with_retry(cfg)
        if not ok:
            message = "异常: %s；重连结果: %s" % (localize_error(exc), message)

    if mode_msg:
        message = message + "；" + mode_msg
    return message, next_sleep


# ---------------------------------------------------------------------------
# Daemon main loop
# ---------------------------------------------------------------------------


def _run_runtime_daemon_hook(app_ctx, state, interval):
    return school_runtime.dispatch_daemon_hook(
        app_ctx["runtime"],
        "daemon_before_tick",
        app_ctx,
        state,
        interval,
    )


def _acquire_daemon_lock():
    ensure_parent_dir(DAEMON_LOCK_FILE)
    lock_handle = open(DAEMON_LOCK_FILE, "a+", encoding="utf-8")

    try:
        import fcntl

        fcntl.flock(lock_handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except ImportError:
        pass
    except OSError:
        lock_handle.close()
        print("另一个 daemon 实例已在运行，退出。", flush=True)
        raise SystemExit(1)

    lock_handle.seek(0)
    lock_handle.truncate()
    lock_handle.write(str(os.getpid()))
    lock_handle.flush()
    return lock_handle


def run_daemon(runtime=None):
    daemon_lock = _acquire_daemon_lock()
    reconcile_manual_login_service_guard()
    state = _make_daemon_state()
    startup_cfg = load_config()
    runtime = runtime or school_runtime.resolve_runtime(startup_cfg)
    startup_message, startup_action_state = _build_startup_status_payload()
    save_runtime_status(
        startup_message,
        state,
        daemon_running=True,
        enabled=True,
        **startup_action_state,
        **build_runtime_snapshot(startup_cfg, state),
    )

    while True:
        cfg = load_config()
        interval = max(int(cfg["interval"]), 5)
        runtime = school_runtime.resolve_runtime(cfg)
        app_ctx = school_runtime.build_app_context(cfg, runtime=runtime)

        action_handled, action_message = handle_runtime_action(
            cfg, state, runtime=runtime, app_ctx=app_ctx
        )
        if action_handled:
            time.sleep(1)
            continue

        if cfg["enabled"] != "1":
            state.update(_make_daemon_state())
            save_runtime_status(
                "自动登录服务未启用",
                state,
                daemon_running=True,
                enabled=False,
                **build_runtime_snapshot(cfg, state),
            )
            time.sleep(interval)
            continue

        hook_result = _run_runtime_daemon_hook(app_ctx, state, interval)
        if hook_result is not None:
            ok, message = hook_result
            log("INFO" if ok else "WARN", "daemon_tick", message)
            save_runtime_status(
                message,
                state,
                daemon_running=True,
                enabled=cfg.get("enabled", "0"),
                in_quiet=in_quiet_window(cfg),
                **build_runtime_snapshot(cfg, state),
            )
            time.sleep(interval)
            continue

        if in_quiet_window(cfg):
            message, sleep = _daemon_tick_quiet(cfg, state, interval)
        else:
            message, sleep = _daemon_tick_active(cfg, state, interval)

        log("INFO", "daemon_tick", message)
        save_runtime_status(
            message,
            state,
            daemon_running=True,
            enabled=cfg.get("enabled", "0"),
            in_quiet=in_quiet_window(cfg),
            **build_runtime_snapshot(cfg, state),
        )
        time.sleep(sleep)


# ---------------------------------------------------------------------------
# CLI: log
# ---------------------------------------------------------------------------


def _tail_log(last_n):
    """Tail the daemon log file. If last_n > 0, show last N lines and exit."""
    import os as _os

    if not _os.path.exists(LOG_FILE):
        print("Log file not found: %s" % LOG_FILE)
        return

    show_n = last_n if last_n > 0 else 20
    with open(LOG_FILE, "r", encoding="utf-8", errors="replace") as f:
        lines = f.readlines()
        for line in lines[-show_n:]:
            print(line, end="")
        pos = f.tell()

    if last_n > 0:
        return

    try:
        while True:
            try:
                size = _os.path.getsize(LOG_FILE)
            except OSError:
                time.sleep(1)
                continue
            if size < pos:
                pos = 0
            if size == pos:
                time.sleep(0.5)
                continue
            with open(LOG_FILE, "r", encoding="utf-8", errors="replace") as f:
                f.seek(pos)
                new = f.read()
                pos = f.tell()
            if new:
                print(new, end="", flush=True)
    except KeyboardInterrupt:
        pass


def _show_runtime_log(cfg):
    import json as _json

    try:
        inspect_payload = build_school_runtime_luci_contract(
            cfg, school_runtime.inspect_runtime(cfg)
        )
    except Exception as exc:
        print("Runtime inspection failed: %s" % localize_error(exc))
        raise SystemExit(1)
    capabilities = inspect_payload.get("capabilities")
    if not isinstance(capabilities, list):
        capabilities = []
    capabilities_text = (
        ", ".join(
            [
                str(item).strip()
                for item in capabilities
                if item is not None and str(item).strip()
            ]
        )
        or "(none)"
    )
    school_name = str(inspect_payload.get("short_name") or cfg.get("school") or "jxnu")
    runtime_type = str(inspect_payload.get("runtime_type") or "unknown")
    runtime_api_version = inspect_payload.get("runtime_api_version")
    if runtime_api_version in (None, ""):
        runtime_api_version = 1
    print("School: %s" % school_name)
    print("Runtime type: %s" % runtime_type)
    print("Runtime API version: %s" % runtime_api_version)
    print("Capabilities: %s" % capabilities_text)
    print(
        "Field descriptors: %s"
        % _json.dumps(inspect_payload.get("field_descriptors"), ensure_ascii=False)
    )
    print(
        "School extra: %s"
        % _json.dumps(inspect_payload.get("school_extra"), ensure_ascii=False)
    )


# ---------------------------------------------------------------------------
# CLI: status
# ---------------------------------------------------------------------------


def _show_status(cfg):
    """Print full runtime status matching LuCI status endpoint."""
    from config import load_runtime_state, load_json_raw_config

    state = load_runtime_state()
    raw = load_json_raw_config()

    ok, message = orchestrator.run_status(cfg)

    conn = state.get("connectivity", "--") or "--"
    conn_level = str(state.get("connectivity_level", "")).strip()
    ip = state.get("current_ip", "--") or "--"
    ssid = state.get("current_ssid", "--") or "--"
    mode_label = state.get("mode_label", "--") or "--"
    account = state.get("campus_account_label", "--") or "--"
    interval = raw.get("interval", "60")
    enabled = raw.get("enabled", "0") == "1"
    in_quiet = state.get("in_quiet", False)
    daemon_running = state.get("daemon_running", False)

    if ok and conn_level == "online":
        status_str = "在线"
    elif in_quiet:
        status_str = "夜间停用"
    else:
        status_str = message or "离线"

    print("=== SMART SRun Status ===\n")
    print("  状态:   %s" % status_str)
    print("  模式:   %s" % mode_label)
    print("  账号:   %s" % account)
    print("  IP:     %s" % ip)
    print("  SSID:   %s" % ssid)
    print("  连通:   %s" % conn)
    print(
        "  守护:   %s"
        % ("运行中" if daemon_running else ("已禁用" if not enabled else "已停止"))
    )
    print("  间隔:   %ss" % interval)

    if state.get("last_action"):
        ts = state.get("last_action_ts", 0)
        result = state.get("action_result", "")
        from datetime import datetime, timezone, timedelta

        ts_str = ""
        if ts:
            try:
                ts_str = datetime.fromtimestamp(
                    int(ts), tz=timezone(timedelta(hours=8))
                ).strftime(" (%H:%M:%S)")
            except (ValueError, TypeError, OSError):
                pass
        print("\n  最近操作: %s -> %s%s" % (state["last_action"], result, ts_str))

    if in_quiet:
        print(
            "  夜间时段: %s - %s"
            % (raw.get("quiet_start", "?"), raw.get("quiet_end", "?"))
        )


def _runtime_cli_login(app_ctx):
    from config import (
        apply_default_selection_for_runtime,
        in_quiet_window as runtime_in_quiet_window,
        quiet_window_label,
    )

    cfg, _, _ = apply_default_selection_for_runtime(False, "登录前")
    runtime = school_runtime.resolve_runtime(cfg)
    app_ctx = school_runtime.build_app_context(cfg, runtime=runtime)
    if runtime_in_quiet_window(cfg):
        return (
            True,
            0,
            "夜间停用中（北京时间 %s），不执行登录" % quiet_window_label(cfg),
        )
    if not cfg["username"] or not cfg["password"]:
        return True, 0, "请先配置学工号和密码: srunnet config account add"
    ok_prep, msg_prep = orchestrator.prepare_campus_for_login(cfg)
    if not ok_prep:
        return True, 0, msg_prep
    ok, message = runtime.login_once(app_ctx)
    log("INFO", "action_result", "login: " + message, action="login")
    return True, 0, message


def _runtime_cli_logout(app_ctx):
    cfg = app_ctx["cfg"]
    ok, message = orchestrator.run_manual_logout(cfg)
    log("INFO", "action_result", "logout: " + message, action="logout")
    return True, 0, message


def _runtime_cli_relogin(app_ctx):
    cfg = app_ctx["cfg"]
    ok, message = orchestrator.run_manual_login(cfg)
    log("INFO", "action_result", "relogin: " + message, action="relogin")
    return True, 0, message


def _emit_cli_result(result):
    handled, exit_code, message = result
    if not handled:
        return False
    if message:
        print(message)
    if exit_code:
        raise SystemExit(exit_code)
    return True


# ---------------------------------------------------------------------------
# CLI: config show
# ---------------------------------------------------------------------------


def _show_config():
    """Print a human-readable configuration summary."""
    raw = load_json_raw_config()

    school = raw.get("school", "jxnu")
    enabled = raw.get("enabled", "0") == "1"
    interval = raw.get("interval", "60")

    print("=== SMART SRun Configuration ===\n")
    print("School:    %s" % school)
    print("Enabled:   %s" % ("yes" if enabled else "no"))
    print("Interval:  %ss" % interval)

    _print_account_table(raw)
    _print_hotspot_table(raw)

    quiet_on = raw.get("quiet_hours_enabled", "0") == "1"
    print("\n--- Quiet Hours ---")
    if quiet_on:
        print(
            "  Enabled:      yes (%s - %s)"
            % (raw.get("quiet_start", "23:00"), raw.get("quiet_end", "06:30"))
        )
        force = raw.get("force_logout_in_quiet", "0") == "1"
        print("  Force logout: %s" % ("yes" if force else "no"))
    else:
        print("  Enabled:      no")

    backoff_on = raw.get("backoff_enable", "0") == "1"
    failover_on = raw.get("failover_enabled", "0") == "1"
    print("\n--- Advanced ---")
    if backoff_on:
        print(
            "  Backoff:    on (max_retries=%s, initial=%ss, max=%ss)"
            % (
                raw.get("backoff_max_retries", "0"),
                raw.get("backoff_initial_duration", "10"),
                raw.get("backoff_max_duration", "300"),
            )
        )
    else:
        print("  Backoff:    off")
    if failover_on:
        failback = raw.get("hotspot_failback_enabled", "0") == "1"
        print(
            "  Failover:   on (failback=%s, timeout=%ss)"
            % (
                "on" if failback else "off",
                raw.get("switch_ready_timeout_seconds", "30"),
            )
        )
    else:
        print("  Failover:   off")
    print("  Conn check: %s" % raw.get("connectivity_check_mode", "internet"))


def _print_account_table(raw):
    accounts = raw.get("campus_accounts", [])
    default_campus = str(raw.get("default_campus_id", "")).strip()
    print("\n--- Campus Accounts ---")
    if not accounts:
        print("  (none)")
        return
    # Header
    print(
        "  %-12s %-20s %-16s %-10s %-6s %-14s"
        % ("ID", "Label", "User", "Op", "Mode", "SSID")
    )
    print("  " + "-" * 80)
    for acc in accounts:
        aid = str(acc.get("id", ""))
        is_default = aid == default_campus
        user_id = acc.get("user_id", "")
        op = acc.get("operator", "")
        suffix = str(acc.get("operator_suffix", "")).strip()
        op_display = op
        if suffix and suffix != op:
            op_display = "%s(%s)" % (op, suffix)
        mode = "wired" if acc.get("access_mode") == "wired" else "wifi"
        label = acc.get("label", "") or ("%s@%s" % (user_id, op) if op else user_id)
        ssid = acc.get("ssid", "") if mode != "wired" else "-"
        marker = " *" if is_default else ""
        print(
            "  %-12s %-20s %-16s %-10s %-6s %-14s%s"
            % (aid, label[:20], user_id[:16], op_display[:10], mode, ssid[:14], marker)
        )
    print("  (* = default)")


def _print_hotspot_table(raw):
    hotspots = raw.get("hotspot_profiles", [])
    default_hp = str(raw.get("default_hotspot_id", "")).strip()
    print("\n--- Hotspot Profiles ---")
    if not hotspots:
        print("  (none)")
        return
    print("  %-12s %-20s %-20s %-10s" % ("ID", "Label", "SSID", "Encryption"))
    print("  " + "-" * 64)
    for hp in hotspots:
        hid = str(hp.get("id", ""))
        is_default = hid == default_hp
        label = hp.get("label", "") or hp.get("ssid", "") or "(unnamed)"
        ssid = hp.get("ssid", "")
        enc = hp.get("encryption", "none")
        marker = " *" if is_default else ""
        print("  %-12s %-20s %-20s %-10s%s" % (hid, label[:20], ssid[:20], enc, marker))
    print("  (* = default)")


# ---------------------------------------------------------------------------
# CLI: config get / config set
# ---------------------------------------------------------------------------


def _config_get(key):
    from config import GLOBAL_SCALAR_KEYS

    raw = load_json_raw_config()
    if key not in GLOBAL_SCALAR_KEYS:
        print("未知配置项: %s" % key)
        print("可用: %s" % ", ".join(sorted(GLOBAL_SCALAR_KEYS)))
        return
    print(raw.get(key, ""))


def _config_set(pairs, json_file=None):
    """Set config values from KEY=VALUE pairs or import from a JSON file."""
    import json as _json
    from config import save_json_raw_config, GLOBAL_SCALAR_KEYS

    raw = load_json_raw_config()

    if json_file:
        try:
            with open(json_file, "r", encoding="utf-8") as f:
                imported = _json.load(f)
        except Exception as exc:
            print("无法读取 JSON 文件: %s" % exc)
            return
        if not isinstance(imported, dict):
            print("JSON 文件内容必须是对象")
            return
        raw.update(imported)
        save_json_raw_config(raw)
        print("已从 %s 导入配置（%d 个字段）" % (json_file, len(imported)))
        return

    if not pairs:
        print("用法: srunnet config set KEY=VALUE ...")
        return

    changed = []
    for pair in pairs:
        if "=" not in pair:
            print("格式错误（需 KEY=VALUE）: %s" % pair)
            return
        key, val = pair.split("=", 1)
        key = key.strip()
        if key not in GLOBAL_SCALAR_KEYS:
            print("未知配置项: %s" % key)
            print("可用: %s" % ", ".join(sorted(GLOBAL_SCALAR_KEYS)))
            return
        old = raw.get(key, "")
        raw[key] = val
        changed.append((key, old, val))

    save_json_raw_config(raw)
    for key, old, val in changed:
        print("  %s: %s -> %s" % (key, old or "(empty)", val))
    print("配置已保存。重启生效: /etc/init.d/smart_srun restart")


# ---------------------------------------------------------------------------
# CLI: config account / config hotspot CRUD
# ---------------------------------------------------------------------------

OPERATOR_LABELS_FALLBACK = {
    "cmcc": "移动",
    "ctcc": "电信",
    "cucc": "联通",
    "xn": "校内网",
}


def _get_current_profile():
    try:
        import schools

        raw = load_json_raw_config()
        school_key = str(raw.get("school", "jxnu")).strip()
        return schools.get_profile(school_key)
    except Exception:
        return None


def _get_operator_choices(profile=None):
    if profile and profile.OPERATORS:
        ids = [o["id"] for o in profile.OPERATORS]
        labels = {o["id"]: o["label"] for o in profile.OPERATORS}
        no_suffix = set(profile.NO_SUFFIX_OPERATORS)
        return ids, labels, no_suffix
    return (
        ["cmcc", "ctcc", "cucc", "xn"],
        OPERATOR_LABELS_FALLBACK,
        {"xn"},
    )


ENC_LABELS = {
    "none": "Open",
    "psk": "WPA-PSK",
    "psk2": "WPA2-PSK",
    "psk-mixed": "WPA/WPA2",
    "sae": "WPA3-SAE",
    "sae-mixed": "WPA2/WPA3",
}


def _prompt(label, default="", choices=None, password=False):
    """Interactive prompt with optional default and choices."""
    suffix = ""
    if choices:
        suffix = " [%s]" % "/".join(choices)
    if default:
        suffix += " (%s)" % default
    suffix += ": "

    if password:
        try:
            import getpass

            value = getpass.getpass(label + suffix)
        except (ImportError, EOFError):
            value = input(label + suffix)
    else:
        try:
            value = input(label + suffix)
        except EOFError:
            value = ""

    value = value.strip()
    if not value and default:
        return default
    if choices and value and value not in choices:
        print("  无效选项: %s，使用默认值: %s" % (value, default))
        return default
    return value


def _interactive_campus(existing=None):
    """Interactive prompts for campus account fields. Returns dict."""
    item = existing or {}
    fields = {}
    profile = _get_current_profile()
    op_ids, op_labels, no_suffix_ops = _get_operator_choices(profile)

    fields["label"] = _prompt("标签（选填）", item.get("label", ""))
    fields["user_id"] = _prompt("学工号", item.get("user_id", ""))
    if not fields["user_id"]:
        print("学工号不能为空")
        return None
    fields["operator"] = _prompt(
        "运营商", item.get("operator", op_ids[0]), choices=op_ids
    )

    default_suffix_hint = (
        "(无后缀)" if fields["operator"] in no_suffix_ops else fields["operator"]
    )
    fields["operator_suffix"] = _prompt(
        "运营商后缀（留空使用默认: %s）" % default_suffix_hint,
        item.get("operator_suffix", ""),
    )

    fields["password"] = _prompt("密码", item.get("password", ""), password=True)
    fields["access_mode"] = _prompt(
        "接入方式", item.get("access_mode", "wifi"), choices=["wifi", "wired"]
    )
    fields["base_url"] = _prompt("认证地址", item.get("base_url", "http://172.17.1.2"))
    fields["ac_id"] = _prompt("AC_ID", item.get("ac_id", "1"))
    if fields["access_mode"] != "wired":
        fields["ssid"] = _prompt("校园网 SSID", item.get("ssid", "jxnu_stu"))
        fields["bssid"] = _prompt("BSSID（留空不锁定）", item.get("bssid", ""))
        fields["radio"] = _prompt("频段（留空自动）", item.get("radio", ""))
    else:
        fields["ssid"] = item.get("ssid", "jxnu_stu")
        fields["bssid"] = item.get("bssid", "")
        fields["radio"] = item.get("radio", "")

    if not fields["label"]:
        suffix = fields.get("operator_suffix", "")
        op = fields["operator"]
        if suffix:
            fields["label"] = "%s@%s" % (fields["user_id"], suffix)
        elif op and op not in no_suffix_ops:
            fields["label"] = "%s@%s" % (fields["user_id"], op)
        else:
            fields["label"] = fields["user_id"]
    return fields


def _interactive_hotspot(existing=None):
    """Interactive prompts for hotspot profile fields. Returns dict."""
    item = existing or {}
    fields = {}
    fields["label"] = _prompt("标签（选填）", item.get("label", ""))
    fields["ssid"] = _prompt("SSID", item.get("ssid", ""))
    if not fields["ssid"]:
        print("SSID 不能为空")
        return None
    fields["encryption"] = _prompt(
        "加密方式",
        item.get("encryption", "psk2"),
        choices=["none", "psk", "psk2", "psk-mixed", "sae", "sae-mixed"],
    )
    if fields["encryption"] != "none":
        fields["key"] = _prompt("密码", item.get("key", ""), password=True)
    else:
        fields["key"] = ""
    fields["radio"] = _prompt("频段（留空自动）", item.get("radio", ""))

    if not fields["label"]:
        fields["label"] = fields["ssid"]
    return fields


def _config_account(args):
    from config import save_json_raw_config, _next_id, _find_item_by_id

    raw = load_json_raw_config()
    accounts = raw.get("campus_accounts", [])
    if not isinstance(accounts, list):
        accounts = []

    subcmd = args.account_command

    if not subcmd:
        _print_account_table(raw)
        return

    if subcmd == "add":
        fields = _interactive_campus()
        if not fields:
            return
        new_id = _next_id(accounts, "campus")
        fields["id"] = new_id
        accounts.append(fields)
        raw["campus_accounts"] = accounts
        if not raw.get("default_campus_id"):
            raw["default_campus_id"] = new_id
            raw["active_campus_id"] = new_id
        save_json_raw_config(raw)
        print("\n已添加: %s (%s)" % (new_id, fields.get("label", "")))
        return

    if subcmd == "edit":
        item = _find_item_by_id(accounts, args.id)
        if not item:
            print("未找到账号: %s" % args.id)
            return
        fields = _interactive_campus(existing=item)
        if not fields:
            return
        fields["id"] = args.id
        for i, acc in enumerate(accounts):
            if str(acc.get("id", "")) == args.id:
                accounts[i] = fields
                break
        raw["campus_accounts"] = accounts
        save_json_raw_config(raw)
        print("\n已更新: %s (%s)" % (args.id, fields.get("label", "")))
        return

    if subcmd == "rm":
        found = _find_item_by_id(accounts, args.id)
        if not found:
            print("未找到账号: %s" % args.id)
            return
        accounts = [a for a in accounts if str(a.get("id", "")) != args.id]
        raw["campus_accounts"] = accounts
        if raw.get("default_campus_id") == args.id:
            raw["default_campus_id"] = accounts[0]["id"] if accounts else ""
        if raw.get("active_campus_id") == args.id:
            raw["active_campus_id"] = raw.get("default_campus_id", "")
        save_json_raw_config(raw)
        print("已删除: %s" % args.id)
        return

    if subcmd == "default":
        found = _find_item_by_id(accounts, args.id)
        if not found:
            print("未找到账号: %s" % args.id)
            return
        raw["default_campus_id"] = args.id
        save_json_raw_config(raw)
        print("已设为默认: %s (%s)" % (args.id, found.get("label", "")))
        return


def _config_hotspot(args):
    from config import save_json_raw_config, _next_id, _find_item_by_id

    raw = load_json_raw_config()
    hotspots = raw.get("hotspot_profiles", [])
    if not isinstance(hotspots, list):
        hotspots = []

    subcmd = args.hotspot_command

    if not subcmd:
        _print_hotspot_table(raw)
        return

    if subcmd == "add":
        fields = _interactive_hotspot()
        if not fields:
            return
        new_id = _next_id(hotspots, "hotspot")
        fields["id"] = new_id
        hotspots.append(fields)
        raw["hotspot_profiles"] = hotspots
        if not raw.get("default_hotspot_id"):
            raw["default_hotspot_id"] = new_id
            raw["active_hotspot_id"] = new_id
        save_json_raw_config(raw)
        print("\n已添加: %s (%s)" % (new_id, fields.get("label", "")))
        return

    if subcmd == "edit":
        item = _find_item_by_id(hotspots, args.id)
        if not item:
            print("未找到热点配置: %s" % args.id)
            return
        fields = _interactive_hotspot(existing=item)
        if not fields:
            return
        fields["id"] = args.id
        for i, hp in enumerate(hotspots):
            if str(hp.get("id", "")) == args.id:
                hotspots[i] = fields
                break
        raw["hotspot_profiles"] = hotspots
        save_json_raw_config(raw)
        print("\n已更新: %s (%s)" % (args.id, fields.get("label", "")))
        return

    if subcmd == "rm":
        found = _find_item_by_id(hotspots, args.id)
        if not found:
            print("未找到热点配置: %s" % args.id)
            return
        hotspots = [h for h in hotspots if str(h.get("id", "")) != args.id]
        raw["hotspot_profiles"] = hotspots
        if raw.get("default_hotspot_id") == args.id:
            raw["default_hotspot_id"] = hotspots[0]["id"] if hotspots else ""
        if raw.get("active_hotspot_id") == args.id:
            raw["active_hotspot_id"] = raw.get("default_hotspot_id", "")
        save_json_raw_config(raw)
        print("已删除: %s" % args.id)
        return

    if subcmd == "default":
        found = _find_item_by_id(hotspots, args.id)
        if not found:
            print("未找到热点配置: %s" % args.id)
            return
        raw["default_hotspot_id"] = args.id
        save_json_raw_config(raw)
        print("已设为默认: %s (%s)" % (args.id, found.get("label", "")))
        return


# ---------------------------------------------------------------------------
# CLI entry point (subcommand style)
# ---------------------------------------------------------------------------


def main():
    import argparse
    import json as _json
    import schools
    import sys

    cfg = load_config()
    runtime = None
    app_ctx = None
    argv = sys.argv[1:]

    parser = argparse.ArgumentParser(
        prog="srunnet",
        description="SMART SRun campus network client for OpenWrt",
    )
    sub = parser.add_subparsers(dest="command")

    # --- top-level commands ---
    sub.add_parser("status", help="show current status")
    sub.add_parser("login", help="login once")
    sub.add_parser("logout", help="logout current account")
    sub.add_parser("relogin", help="logout then login")
    sub.add_parser("daemon", help="run as daemon (used by init script)")
    sub.add_parser("enable", help="enable the daemon service")
    sub.add_parser("disable", help="disable the daemon service")
    p_schools = sub.add_parser("schools", help="list available school profiles (JSON)")
    schools_sub = p_schools.add_subparsers(dest="schools_command")
    p_schools_inspect = schools_sub.add_parser("inspect", help="inspect school runtime")
    p_schools_inspect.add_argument(
        "--selected",
        action="store_true",
        help="show selected runtime metadata (JSON)",
    )

    p_log = sub.add_parser("log", help="tail the daemon log")
    p_log.add_argument(
        "-n", type=int, default=0, help="show last N lines then exit (default: follow)"
    )
    p_log.add_argument(
        "log_target",
        nargs="?",
        choices=["runtime"],
        help="show selected runtime diagnostics",
    )

    p_switch = sub.add_parser("switch", help="switch network mode")
    p_switch.add_argument(
        "target", choices=["hotspot", "campus"], help="switch to hotspot or campus"
    )

    # --- config subcommand tree ---
    p_config = sub.add_parser("config", help="view or modify configuration")
    config_sub = p_config.add_subparsers(dest="config_command")

    config_sub.add_parser("show", help="show full configuration summary")

    p_get = config_sub.add_parser("get", help="get a scalar config value")
    p_get.add_argument("key", help="config key name")

    p_set = config_sub.add_parser("set", help="set config values or import JSON")
    p_set.add_argument(
        "pairs", nargs="*", metavar="KEY=VALUE", help="scalar config values to set"
    )
    p_set.add_argument(
        "-f", "--file", metavar="PATH", help="import config from a JSON file"
    )

    # config account
    p_account = config_sub.add_parser("account", help="manage campus accounts")
    account_sub = p_account.add_subparsers(dest="account_command")
    account_sub.add_parser("add", help="add a campus account (interactive)")
    p_acc_edit = account_sub.add_parser("edit", help="edit a campus account")
    p_acc_edit.add_argument("id", help="account ID (e.g. campus-1)")
    p_acc_rm = account_sub.add_parser("rm", help="remove a campus account")
    p_acc_rm.add_argument("id", help="account ID")
    p_acc_def = account_sub.add_parser("default", help="set default campus account")
    p_acc_def.add_argument("id", help="account ID")

    # config hotspot
    p_hotspot = config_sub.add_parser("hotspot", help="manage hotspot profiles")
    hotspot_sub = p_hotspot.add_subparsers(dest="hotspot_command")
    hotspot_sub.add_parser("add", help="add a hotspot profile (interactive)")
    p_hp_edit = hotspot_sub.add_parser("edit", help="edit a hotspot profile")
    p_hp_edit.add_argument("id", help="hotspot ID (e.g. hotspot-1)")
    p_hp_rm = hotspot_sub.add_parser("rm", help="remove a hotspot profile")
    p_hp_rm.add_argument("id", help="hotspot ID")
    p_hp_def = hotspot_sub.add_parser("default", help="set default hotspot profile")
    p_hp_def.add_argument("id", help="hotspot ID")

    needs_runtime_for_parse = bool(argv) and not argv[0].startswith("-")
    if needs_runtime_for_parse and argv[0] not in school_runtime.CORE_RESERVED_COMMANDS:
        runtime = school_runtime.resolve_runtime(cfg)
        app_ctx = school_runtime.build_app_context(cfg, runtime=runtime)
        for item in school_runtime.get_runtime_cli_commands(runtime):
            sub.add_parser(item["name"], help=item.get("help") or None)

    args = parser.parse_args()

    # No command → show status
    if not args.command:
        _show_status(cfg)
        return

    # --- dispatch ---
    if args.command == "status":
        _show_status(cfg)
        return

    if args.command == "login":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        app_ctx = app_ctx or school_runtime.build_app_context(cfg, runtime=runtime)
        _emit_cli_result(_runtime_cli_login(app_ctx))
        return

    if args.command == "logout":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        app_ctx = app_ctx or school_runtime.build_app_context(cfg, runtime=runtime)
        _emit_cli_result(_runtime_cli_logout(app_ctx))
        return

    if args.command == "relogin":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        app_ctx = app_ctx or school_runtime.build_app_context(cfg, runtime=runtime)
        _emit_cli_result(_runtime_cli_relogin(app_ctx))
        return

    if args.command == "daemon":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        run_daemon(runtime=runtime)
        return

    if args.command == "log":
        if getattr(args, "log_target", "") == "runtime":
            _show_runtime_log(cfg)
            return
        _tail_log(args.n)
        return

    if args.command == "enable":
        _config_set(["enabled=1"])
        return

    if args.command == "disable":
        _config_set(["enabled=0"])
        return

    if args.command == "schools":
        if getattr(args, "schools_command", "") == "inspect" and getattr(
            args, "selected", False
        ):
            inspect_payload = build_school_runtime_luci_contract(
                cfg, school_runtime.inspect_runtime(cfg)
            )
            print(
                _json.dumps(
                    inspect_payload,
                    ensure_ascii=False,
                    indent=2,
                )
            )
            return
        print(_json.dumps(schools.list_schools(), ensure_ascii=False, indent=2))
        return

    if args.command == "switch":
        cfg = load_config()
        expect_hotspot = args.target == "hotspot"
        _, message = run_switch(cfg, expect_hotspot=expect_hotspot)
        log(
            "INFO",
            "action_result",
            "switch %s: %s" % (args.target, message),
            action="switch_%s" % args.target,
        )
        print(message)
        return

    if args.command == "config":
        cmd = args.config_command

        if not cmd or cmd == "show":
            _show_config()
            return

        if cmd == "get":
            _config_get(args.key)
            return

        if cmd == "set":
            _config_set(args.pairs, json_file=args.file)
            return

        if cmd == "account":
            _config_account(args)
            return

        if cmd == "hotspot":
            _config_hotspot(args)
            return

        # Shouldn't reach here
        parser.parse_args(["config", "--help"])
        return

    _emit_cli_result(school_runtime.dispatch_custom_cli(runtime, app_ctx, args))
