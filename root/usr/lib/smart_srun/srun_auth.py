"""
SRun 认证 API -- challenge、login、logout、在线查询。

通过 SchoolProfile 完成加密和参数构建，通过 network.py 完成 HTTP 请求。
不管 WiFi 连没连，不管重试策略。
"""

import time

from config import append_log, localize_error
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
    data = parse_jsonp(
        http_get(get_challenge_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    token = data.get("challenge")
    if not token:
        msg = data.get("error_msg") or data.get("error") or "unknown response"
        raise RuntimeError("获取挑战码失败: " + localize_error(msg))
    resolved_ip = pick_valid_ip(data.get("client_ip"), data.get("online_ip"), ip)
    if not resolved_ip:
        raise RuntimeError("获取挑战码失败: 未获得有效客户端 IP")
    return token, resolved_ip


def login(profile, srun_portal_api, cfg, ip, i_value, hmd5, chksum, bind_ip=None):
    params = profile.build_login_params(cfg, ip, i_value, hmd5, chksum)
    data = parse_jsonp(
        http_get(srun_portal_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    return profile.parse_login_response(data)


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
    data = parse_jsonp(
        http_get(rad_user_info_api, params=params, timeout=5, bind_ip=bind_ip)
    )
    return profile.parse_online_status(data, expected_username)


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
    return query_online_identity(runtime, urls["rad_user_info_api"], expected, bind_ip)


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
            online, online_msg = default_query_online_status(
                app_ctx, expected_username=cfg["username"], bind_ip=bip
            )
            if online:
                return True, "已在线"
            return False, online_msg
        except Exception:
            pass

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
