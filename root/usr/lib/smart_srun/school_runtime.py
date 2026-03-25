"""
School runtime loader and compatibility adapters.
"""

import inspect
import types

import crypto
import schools

from schools._base import SchoolProfile


RUNTIME_API_VERSION = 1
CORE_RESERVED_COMMANDS = (
    "status",
    "login",
    "logout",
    "relogin",
    "daemon",
    "schools",
    "config",
    "switch",
    "log",
    "enable",
    "disable",
)


def build_core_api():
    import orchestrator
    import srun_auth

    return {
        "runtime_api_version": RUNTIME_API_VERSION,
        "get_base64": crypto.get_base64,
        "get_xencode": crypto.get_xencode,
        "get_md5": crypto.get_md5,
        "get_sha1": crypto.get_sha1,
        "get_info": crypto.get_info,
        "get_chksum": crypto.get_chksum,
        "default_login_once": srun_auth.default_login_once,
        "default_logout_once": srun_auth.default_logout_once,
        "default_query_online_identity": srun_auth.default_query_online_identity,
        "default_query_online_status": srun_auth.default_query_online_status,
        "default_run_status": orchestrator.default_run_status,
        "default_run_quiet_logout": orchestrator.default_run_quiet_logout,
    }


def _apply_legacy_profile_metadata(runtime, metadata):
    runtime.SHORT_NAME = metadata.get("short_name", "")
    runtime.NAME = metadata.get("name", "")
    runtime.DESCRIPTION = metadata.get("description", "")
    runtime.CONTRIBUTORS = tuple(metadata.get("contributors", ()))
    runtime.OPERATORS = tuple(metadata.get("operators", ()))
    runtime.NO_SUFFIX_OPERATORS = tuple(metadata.get("no_suffix_operators", ()))
    return runtime


class LegacyProfileRuntimeAdapter(object):
    def __init__(self, profile, source_file=None, metadata=None):
        self._profile = profile
        self.runtime_type = "legacy_profile"
        self.runtime_api_version = RUNTIME_API_VERSION
        self.source_file = source_file or getattr(profile.__class__, "__file__", "")
        self.declared_capabilities = tuple((metadata or {}).get("capabilities", ()))
        _apply_legacy_profile_metadata(self, metadata or {})

    def __getattr__(self, name):
        return getattr(self._profile, name)

    def login_once(self, app_ctx):
        return app_ctx["core_api"]["default_login_once"](app_ctx)

    def logout_once(self, app_ctx, override_user_id=None, bind_ip=None):
        return app_ctx["core_api"]["default_logout_once"](
            app_ctx, override_user_id=override_user_id, bind_ip=bind_ip
        )

    def query_online_identity(self, app_ctx, expected_username=None, bind_ip=None):
        return app_ctx["core_api"]["default_query_online_identity"](
            app_ctx, expected_username=expected_username, bind_ip=bind_ip
        )

    def query_online_status(self, app_ctx, expected_username=None, bind_ip=None):
        return app_ctx["core_api"]["default_query_online_status"](
            app_ctx, expected_username=expected_username, bind_ip=bind_ip
        )

    def status(self, app_ctx):
        return app_ctx["core_api"]["default_run_status"](app_ctx)

    def quiet_logout(self, app_ctx):
        return app_ctx["core_api"]["default_run_quiet_logout"](app_ctx)

    def cli_status(self, app_ctx, args):
        import daemon

        daemon._show_status(app_ctx["cfg"])
        return True, 0, ""

    def cli_login(self, app_ctx, args):
        import daemon

        return daemon._runtime_cli_login(app_ctx)

    def cli_logout(self, app_ctx, args):
        import daemon

        return daemon._runtime_cli_logout(app_ctx)

    def cli_relogin(self, app_ctx, args):
        import daemon

        return daemon._runtime_cli_relogin(app_ctx)

    def cli_daemon(self, app_ctx, args):
        import daemon

        daemon.run_daemon(runtime=self)
        return True, 0, ""

    def get_cli_commands(self):
        return []

    def handle_cli_command(self, app_ctx, args):
        return False, 0, ""

    def daemon_before_tick(self, app_ctx, state, interval):
        return None

    def handle_runtime_action(self, app_ctx, action, state):
        import daemon

        return daemon._handle_runtime_action_core(app_ctx, state, action)


_BOUNDARY_METHODS = (
    "login_once",
    "logout_once",
    "query_online_identity",
    "query_online_status",
    "status",
    "quiet_logout",
    "cli_status",
    "cli_login",
    "cli_logout",
    "cli_relogin",
    "cli_daemon",
    "get_cli_commands",
    "handle_cli_command",
    "daemon_before_tick",
    "handle_runtime_action",
)


def _attach_default_boundary_methods(runtime):
    for name in _BOUNDARY_METHODS:
        if callable(getattr(runtime, name, None)):
            continue
        method = getattr(LegacyProfileRuntimeAdapter, name)
        setattr(runtime, name, types.MethodType(method, runtime))
    return runtime


class DefaultRuntime(LegacyProfileRuntimeAdapter):
    def __init__(self):
        profile = SchoolProfile()
        LegacyProfileRuntimeAdapter.__init__(
            self,
            profile,
            source_file=inspect.getsourcefile(SchoolProfile) or "",
            metadata=schools.get_default_school_metadata(),
        )
        self.runtime_type = "default"


def _get_runtime_metadata(short_name):
    if short_name == "default":
        return schools.get_default_school_metadata()
    metadata = schools.get_school_metadata(short_name)
    if metadata:
        return metadata
    return schools.get_default_school_metadata()


def _finalize_runtime(runtime, metadata, runtime_type, source_file):
    _apply_legacy_profile_metadata(runtime, metadata)
    _attach_default_boundary_methods(runtime)
    runtime.runtime_type = getattr(runtime, "runtime_type", runtime_type)
    runtime.runtime_api_version = getattr(
        runtime, "runtime_api_version", RUNTIME_API_VERSION
    )
    runtime.source_file = getattr(runtime, "source_file", source_file)
    runtime.declared_capabilities = tuple(
        getattr(runtime, "declared_capabilities", metadata.get("capabilities", ()))
    )
    return runtime


def resolve_runtime(cfg):
    cfg = cfg or {}
    short_name = str(cfg.get("school", "")).strip()
    if not short_name or short_name == "default":
        return DefaultRuntime()

    entry = schools.get_school_entry(short_name)
    if not entry:
        raise LookupError("unknown school runtime: %s" % short_name)

    module = entry["module"]
    metadata = entry["metadata"]
    core_api = build_core_api()

    if callable(getattr(module, "build_runtime", None)):
        runtime = module.build_runtime(core_api, cfg)
        return _finalize_runtime(
            runtime, metadata, "build_runtime", entry["source_file"]
        )

    runtime_class = getattr(module, "Runtime", None)
    if runtime_class:
        runtime = runtime_class(core_api, cfg)
        return _finalize_runtime(
            runtime, metadata, "runtime_class", entry["source_file"]
        )

    profile_class = getattr(module, "Profile", None)
    if profile_class:
        return LegacyProfileRuntimeAdapter(
            profile_class(),
            source_file=entry["source_file"],
            metadata=metadata,
        )

    raise LookupError("school runtime has no supported entrypoint: %s" % short_name)


def build_app_context(cfg, runtime=None):
    cfg = cfg or {}
    runtime = runtime or resolve_runtime(cfg)
    short_name = str(cfg.get("school", "")).strip() or getattr(
        runtime, "SHORT_NAME", "default"
    )
    return {
        "cfg": cfg,
        "runtime": runtime,
        "core_api": build_core_api(),
        "runtime_api_version": getattr(
            runtime, "runtime_api_version", RUNTIME_API_VERSION
        ),
        "school_metadata": _get_runtime_metadata(short_name),
    }


def inspect_runtime(cfg):
    runtime = resolve_runtime(cfg)
    short_name = getattr(
        runtime, "SHORT_NAME", str((cfg or {}).get("school", "")).strip() or "default"
    )
    metadata = _get_runtime_metadata(short_name)
    result = dict(metadata)
    result["runtime_type"] = getattr(runtime, "runtime_type", "unknown")
    result["runtime_api_version"] = getattr(
        runtime, "runtime_api_version", RUNTIME_API_VERSION
    )
    result["source_file"] = getattr(runtime, "source_file", "")
    result["declared_capabilities"] = list(
        getattr(runtime, "declared_capabilities", ())
    )
    return result


def get_runtime_cli_commands(runtime):
    commands = []
    if callable(getattr(runtime, "get_cli_commands", None)):
        commands = runtime.get_cli_commands() or []

    normalized = []
    seen = set()
    for item in commands:
        if not isinstance(item, dict):
            raise RuntimeError("runtime CLI contract error: command spec must be dict")
        name = str(item.get("name", "")).strip()
        if not name:
            raise RuntimeError("runtime CLI contract error: command spec missing name")
        if name in CORE_RESERVED_COMMANDS:
            raise ValueError("runtime cannot replace reserved command: %s" % name)
        if name in seen:
            raise ValueError("runtime CLI command duplicated: %s" % name)
        seen.add(name)
        normalized.append(
            {
                "name": name,
                "help": str(item.get("help", "")).strip(),
            }
        )
    return normalized


def _coerce_cli_result(name, result):
    if result is None:
        return False, 0, ""
    if not isinstance(result, tuple) or len(result) != 3:
        raise RuntimeError(
            "runtime CLI contract error: %s must return (handled, exit_code, message)"
            % name
        )
    handled, exit_code, message = result
    if not isinstance(handled, bool):
        raise RuntimeError(
            "runtime CLI contract error: %s handled flag must be bool" % name
        )
    try:
        exit_code = int(exit_code)
    except (TypeError, ValueError):
        raise RuntimeError(
            "runtime CLI contract error: %s exit_code must be int" % name
        )
    if message is None:
        message = ""
    elif not isinstance(message, str):
        message = str(message)
    return handled, exit_code, message


def dispatch_cli_hook(runtime, hook_name, app_ctx, args):
    fn = getattr(runtime, hook_name, None)
    if not callable(fn):
        return False, 0, ""
    return _coerce_cli_result(hook_name, fn(app_ctx, args))


def dispatch_custom_cli(runtime, app_ctx, args):
    fn = getattr(runtime, "handle_cli_command", None)
    if not callable(fn):
        return False, 0, ""
    return _coerce_cli_result("handle_cli_command", fn(app_ctx, args))


def dispatch_daemon_hook(runtime, hook_name, app_ctx, state, interval):
    fn = getattr(runtime, hook_name, None)
    if not callable(fn):
        return None
    result = fn(app_ctx, state, interval)
    if result is None:
        return None
    if not isinstance(result, tuple) or len(result) != 2:
        raise RuntimeError(
            "runtime daemon contract error: %s must return (ok, message)" % hook_name
        )
    ok, message = result
    if not isinstance(ok, bool):
        raise RuntimeError(
            "runtime daemon contract error: %s ok flag must be bool" % hook_name
        )
    if message is None:
        message = ""
    elif not isinstance(message, str):
        message = str(message)
    return ok, message


def dispatch_runtime_action(runtime, app_ctx, action, state):
    fn = getattr(runtime, "handle_runtime_action", None)
    if not callable(fn):
        raise RuntimeError(
            "runtime action contract error: handle_runtime_action missing"
        )
    try:
        result = fn(app_ctx, action, state)
    except Exception as exc:
        return False, "runtime action failed: %s" % exc
    if not isinstance(result, tuple) or len(result) != 2:
        return (
            False,
            "runtime action contract error: handle_runtime_action must return (ok, message)",
        )
    ok, message = result
    if not isinstance(ok, bool):
        return (
            False,
            "runtime action contract error: handle_runtime_action ok flag must be bool",
        )
    if message is None:
        message = ""
    elif not isinstance(message, str):
        message = str(message)
    return ok, message
