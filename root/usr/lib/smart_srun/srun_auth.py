"""
SRun 认证 API -- challenge、login、logout、在线查询。

通过 SchoolProfile 完成加密和参数构建，通过 network.py 完成 HTTP 请求。
不管 WiFi 连没连，不管重试策略。
"""

import time

from config import localize_error, log, timed
from network import (
    HTTP_EXCEPTIONS,
    extract_ip_from_text,
    get_local_ip_for_target,
    http_get,
    parse_jsonp,
    pick_valid_ip,
    resolve_bind_ip,
)
from school_runtime import build_app_context


def get_profile(cfg):
    return ensure_app_context(cfg)["runtime"]


def is_app_context(value):
    return isinstance(value, dict) and "cfg" in value and "runtime" in value


def ensure_app_context(cfg_or_app_ctx, runtime=None):
    if is_app_context(cfg_or_app_ctx):
        return cfg_or_app_ctx
    return build_app_context(cfg_or_app_ctx or {}, runtime=runtime)


def get_logout_username(cfg):
    user_id = str(cfg.get("user_id", "")).strip()
    if user_id:
        return user_id
    return str(cfg.get("username", "")).split("@", 1)[0].strip()


# ---------------------------------------------------------------------------
# 基础 API 调用
# ---------------------------------------------------------------------------
def init_getip(init_url, bind_ip=None):
    text = http_get(init_url, timeout=5, bind_ip=bind_ip)
    ip = extract_ip_from_text(text)
    # 门户首页不一定回显 user_ip。显式绑定了多 WAN 源地址时，应继续使用该
    # DHCP 地址作为 challenge/login 的 ip 参数，不能再次按默认路由选源。
    if not ip:
        ip = pick_valid_ip(bind_ip)
    if not ip:
        target_host = init_url.split("://", 1)[-1].split("/", 1)[0]
        ip = get_local_ip_for_target(target_host)
    if not ip:
        raise RuntimeError("无法获取本机登录 IP")
    return ip


def get_token(get_challenge_api, username, ip, bind_ip=None):
    now = int(time.time() * 1000)
    params = {
        "callback": "jQuery112404953340710317169_" + str(now),
        "username": username,
        "ip": ip,
        "_": now,
    }
    log(
        "DEBUG",
        "srun_challenge",
        "requesting SRun challenge token",
        username=username,
        ip=ip,
        bind_ip=bind_ip or "",
    )
    with timed() as t:
        raw = http_get(get_challenge_api, params=params, timeout=5, bind_ip=bind_ip)
    try:
        data = parse_jsonp(raw)
    except ValueError as exc:
        log(
            "WARN",
            "srun_challenge_result",
            "challenge response parse failed",
            username=username,
            duration_ms=t.ms,
            error=str(exc),
        )
        raise
    token = data.get("challenge")
    if not token:
        msg = data.get("error_msg") or data.get("error") or "unknown response"
        log(
            "WARN",
            "srun_challenge_result",
            "challenge rejected",
            username=username,
            duration_ms=t.ms,
            error_code=str(msg),
        )
        raise RuntimeError("获取挑战码失败: " + localize_error(msg))
    resolved_ip = pick_valid_ip(data.get("client_ip"), data.get("online_ip"), ip)
    if not resolved_ip:
        log(
            "WARN",
            "srun_challenge_result",
            "challenge returned no client IP",
            username=username,
            duration_ms=t.ms,
        )
        raise RuntimeError("获取挑战码失败: 未获得有效客户端 IP")
    log(
        "DEBUG",
        "srun_challenge_result",
        challenge_ok=True,
        username=username,
        resolved_ip=resolved_ip,
        duration_ms=t.ms,
    )
    return token, resolved_ip


def login(profile, srun_portal_api, cfg, ip, i_value, hmd5, chksum, bind_ip=None):
    params = profile.build_login_params(cfg, ip, i_value, hmd5, chksum)
    log(
        "DEBUG",
        "srun_login_submit",
        "submitting SRun login request",
        username=cfg.get("username", ""),
        ip=ip,
        bind_ip=bind_ip or "",
        enc=cfg.get("enc", ""),
        n=cfg.get("n", ""),
        type=cfg.get("type", ""),
    )
    with timed() as t:
        raw = http_get(srun_portal_api, params=params, timeout=5, bind_ip=bind_ip)
        try:
            data = parse_jsonp(raw)
        except (TypeError, ValueError):
            # SRun 网关必须回 JSONP。回不出来说明这次请求压根没到网关，绝大多数
            # 是上游强制门户把它劫成了一张 HTML 认证页。不单独标出来的话，用户
            # 只会看到一条 JSON 解析异常，不知道要先开浏览器过网页认证。
            log(
                "WARN",
                "srun_login_response",
                "login response was not JSONP",
                username=cfg.get("username", ""),
                ok=False,
                error_code="portal_intercept_error",
                duration_ms=t.ms,
                body_head=str(raw or "")[:80].replace("\n", " "),
            )
            return False, "portal_intercept_error"
    ok, message = profile.parse_login_response(data)
    log(
        "INFO" if ok else "WARN",
        "srun_login_response",
        "login response received",
        username=cfg.get("username", ""),
        ok=ok,
        error_code=message if not ok else "ok",
        duration_ms=t.ms,
    )
    return ok, message


def logout(profile, rad_user_dm_api, cfg, ip, bind_ip=None):
    params = profile.build_logout_params(cfg, ip)
    data = parse_jsonp(
        http_get(rad_user_dm_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    return profile.parse_logout_response(data)


def query_online_identity(
    profile, rad_user_info_api=None, expected_username=None, bind_ip=None
):
    if is_app_context(profile):
        app_ctx = profile
        resolved_username = expected_username
        if resolved_username is None:
            resolved_username = rad_user_info_api
        if resolved_username is None:
            resolved_username = app_ctx["cfg"].get("username", "")
        return app_ctx["runtime"].query_online_identity(
            app_ctx, expected_username=resolved_username, bind_ip=bind_ip
        )
    params = profile.build_online_query_params()
    log(
        "DEBUG",
        "srun_online_query",
        "querying SRun online status",
        expected_username=expected_username or "",
        bind_ip=bind_ip or "",
    )
    with timed() as t:
        try:
            data = parse_jsonp(
                http_get(rad_user_info_api, params=params, timeout=5, bind_ip=bind_ip)
            )
        except ValueError as exc:
            log(
                "WARN",
                "srun_online_result",
                "online query response parse failed",
                duration_ms=t.ms,
                error=str(exc),
            )
            raise
    online, username_reported, message = profile.parse_online_status(
        data, expected_username
    )
    log(
        "DEBUG",
        "srun_online_result",
        online=online,
        username_reported=username_reported or "",
        duration_ms=t.ms,
    )
    return online, username_reported, message


def query_online_status(
    profile, rad_user_info_api=None, expected_username=None, bind_ip=None
):
    if is_app_context(profile):
        app_ctx = profile
        resolved_username = expected_username
        if resolved_username is None:
            resolved_username = rad_user_info_api
        if resolved_username is None:
            resolved_username = app_ctx["cfg"].get("username", "")
        return app_ctx["runtime"].query_online_status(
            app_ctx, expected_username=resolved_username, bind_ip=bind_ip
        )
    online, _, message = query_online_identity(
        profile, rad_user_info_api, expected_username, bind_ip
    )
    return online, message


def wait_for_logout_status(
    profile, rad_user_info_api, cfg, bind_ip=None, attempts=3, delay_seconds=1
):
    app_ctx = None
    expected_username = cfg["username"]
    query_target = profile
    query_api = rad_user_info_api

    if is_app_context(profile):
        app_ctx = profile
        expected_username = app_ctx["cfg"].get("username", expected_username)
        query_target = app_ctx
        query_api = None

    attempts = max(int(attempts), 1)
    last_message = ""
    for idx in range(attempts):
        online, message = query_online_status(
            query_target, query_api, expected_username, bind_ip=bind_ip
        )
        last_message = message
        if not online:
            return True, message
        if idx + 1 < attempts and delay_seconds > 0:
            time.sleep(delay_seconds)
    return False, last_message or "在线"


# ---------------------------------------------------------------------------
# 核心登录流程（纯认证，不管 WiFi）
# ---------------------------------------------------------------------------
def build_urls(cfg):
    app_ctx = ensure_app_context(cfg)
    return app_ctx["runtime"].build_urls(app_ctx["cfg"]["base_url"])


def default_query_online_identity(app_ctx, expected_username=None, bind_ip=None):
    runtime = app_ctx["runtime"]
    cfg = app_ctx["cfg"]
    urls = runtime.build_urls(cfg["base_url"])
    expected = expected_username or cfg.get("username", "")
    bip = bind_ip
    if bip is None:
        bip = resolve_bind_ip(urls["init_url"], cfg)
    return query_online_identity(runtime, urls["rad_user_info_api"], expected, bip)


def default_query_online_status(app_ctx, expected_username=None, bind_ip=None):
    online, _, message = default_query_online_identity(
        app_ctx, expected_username=expected_username, bind_ip=bind_ip
    )
    return online, message


def default_login_once(app_ctx):
    cfg = app_ctx["cfg"]
    runtime = app_ctx["runtime"]
    urls = runtime.build_urls(cfg["base_url"])
    bip = resolve_bind_ip(urls["init_url"], cfg)
    ip = init_getip(urls["init_url"], bind_ip=bip)
    token, ip = get_token(urls["get_challenge_api"], cfg["username"], ip, bind_ip=bip)
    i_value, hmd5, chksum = runtime.do_complex_work(cfg, ip, token)
    ok, message = login(
        runtime, urls["srun_portal_api"], cfg, ip, i_value, hmd5, chksum, bind_ip=bip
    )

    if (not ok) and ("challenge_expire_error" in message.lower()):
        token, ip = get_token(
            urls["get_challenge_api"], cfg["username"], ip, bind_ip=bip
        )
        i_value, hmd5, chksum = runtime.do_complex_work(cfg, ip, token)
        ok, message = login(
            runtime,
            urls["srun_portal_api"],
            cfg,
            ip,
            i_value,
            hmd5,
            chksum,
            bind_ip=bip,
        )

    if (not ok) and ("no_response_data_error" in message.lower()):
        try:
            online, _online_msg = default_query_online_status(
                app_ctx, expected_username=cfg["username"], bind_ip=bip
            )
            if online:
                return True, "已在线"
        except Exception:
            pass
        # 这里曾经返回在线查询的结果，于是网关的 no_response_data_error 被一句
        # not_online_error 覆盖掉：那句话只是复述"你没登上"，把唯一能定位问题
        # 的错误码丢了，也没告诉用户下一步做什么（issue #29）。保留原始错误码，
        # 由 localize_error 给出可执行的说明。
        return False, message

    if (not ok) and (
        "already online" in message.lower() or "e2620" in message.lower()
    ):
        # 网关报 E2620 already online，分两种情况：
        # a) 本 IP 已认证（路由器重启后拿到同一 IP，旧会话仍有效）——
        #    "已在线"就是目标状态，按成功处理，否则守护会带退避无限重试；
        # b) 会话挂在旧 IP 上（重启/重连后 DHCP 换了 IP）——本 IP 查询显示
        #    未在线，需先 unbind 登出踢掉旧会话，再重新走一次完整登录。
        try:
            online, _online_msg = default_query_online_status(
                app_ctx, expected_username=cfg["username"], bind_ip=bip
            )
        except Exception:
            # 状态复核失败（如网络抖动）时采信网关的明确答复
            return True, "已在线"
        if online:
            return True, "已在线"
        try:
            default_logout_once(app_ctx, bind_ip=bip)
        except Exception:
            pass
        # E2532: 网关要求两次认证间隔 >=3 秒；刚发过一次登录（E2620），
        # 不等待的话重登必然被频率限制拦下，真实结果全被 E2532 遮住。
        time.sleep(3.5)
        try:
            token, ip = get_token(
                urls["get_challenge_api"], cfg["username"], ip, bind_ip=bip
            )
            i_value, hmd5, chksum = runtime.do_complex_work(cfg, ip, token)
            ok, message = login(
                runtime,
                urls["srun_portal_api"],
                cfg,
                ip,
                i_value,
                hmd5,
                chksum,
                bind_ip=bip,
            )
            if ok:
                return True, "登录成功（已清理旧会话）"
        except Exception as exc:
            message = str(exc)
        return False, "登录失败: " + localize_error(message)

    if ok:
        return True, "登录成功"
    return False, "登录失败: " + localize_error(message)


def run_logout_once(cfg, override_user_id=None, app_ctx=None, bind_ip=None):
    app_ctx = ensure_app_context(app_ctx or cfg)
    return app_ctx["runtime"].logout_once(
        app_ctx, override_user_id=override_user_id, bind_ip=bind_ip
    )


def default_logout_once(app_ctx, override_user_id=None, bind_ip=None):
    cfg = app_ctx["cfg"]
    runtime = app_ctx["runtime"]
    urls = runtime.build_urls(cfg["base_url"])
    bip = bind_ip or resolve_bind_ip(urls["init_url"], cfg)
    logout_cfg = dict(cfg)
    logout_user = str(override_user_id or "").strip()
    if logout_user:
        logout_cfg["user_id"] = logout_user
        logout_cfg["username"] = logout_user
    ip = init_getip(urls["init_url"], bind_ip=bip)
    return logout(runtime, urls["rad_user_dm_api"], logout_cfg, ip, bind_ip=bip)


def run_once(cfg):
    """纯认证流程：challenge -> 加密 -> login API。
    不管 WiFi、不管 quiet hours、不管重试。"""
    app_ctx = ensure_app_context(cfg)
    return app_ctx["runtime"].login_once(app_ctx)


def run_once_safe(cfg):
    try:
        app_ctx = ensure_app_context(cfg)
        return run_once(app_ctx)
    except HTTP_EXCEPTIONS as exc:
        return False, "网络错误: " + localize_error(exc)
    except ValueError as exc:
        return False, "响应解析错误: " + localize_error(exc)
    except Exception as exc:
        return False, "错误: " + localize_error(exc)
