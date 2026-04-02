"""
CLI entrypoint for SMART SRun.

Keeps argparse and top-level command dispatch separate from daemon runtime logic.
"""

import argparse
import json
import sys

import daemon
import school_runtime
import schools


def main():
    cfg = daemon.load_config()
    runtime = None
    app_ctx = None
    argv = sys.argv[1:]

    parser = argparse.ArgumentParser(
        prog="srunnet",
        description="SMART SRun campus network client for OpenWrt",
    )
    sub = parser.add_subparsers(dest="command")

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

    p_account = config_sub.add_parser("account", help="manage campus accounts")
    account_sub = p_account.add_subparsers(dest="account_command")
    account_sub.add_parser("add", help="add a campus account (interactive)")
    p_acc_edit = account_sub.add_parser("edit", help="edit a campus account")
    p_acc_edit.add_argument("id", help="account ID (e.g. campus-1)")
    p_acc_rm = account_sub.add_parser("rm", help="remove a campus account")
    p_acc_rm.add_argument("id", help="account ID")
    p_acc_def = account_sub.add_parser("default", help="set default campus account")
    p_acc_def.add_argument("id", help="account ID")

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

    if not args.command:
        daemon._show_status(cfg)
        return

    if args.command == "status":
        daemon._show_status(cfg)
        return

    if args.command == "login":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        app_ctx = app_ctx or school_runtime.build_app_context(cfg, runtime=runtime)
        daemon._emit_cli_result(daemon._runtime_cli_login(app_ctx))
        return

    if args.command == "logout":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        app_ctx = app_ctx or school_runtime.build_app_context(cfg, runtime=runtime)
        daemon._emit_cli_result(daemon._runtime_cli_logout(app_ctx))
        return

    if args.command == "relogin":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        app_ctx = app_ctx or school_runtime.build_app_context(cfg, runtime=runtime)
        daemon._emit_cli_result(daemon._runtime_cli_relogin(app_ctx))
        return

    if args.command == "daemon":
        runtime = runtime or school_runtime.resolve_runtime(cfg)
        daemon.run_daemon(runtime=runtime)
        return

    if args.command == "log":
        if getattr(args, "log_target", "") == "runtime":
            daemon._show_runtime_log(cfg)
            return
        daemon._tail_log(args.n)
        return

    if args.command == "enable":
        daemon._config_set(["enabled=1"])
        return

    if args.command == "disable":
        daemon._config_set(["enabled=0"])
        return

    if args.command == "schools":
        if getattr(args, "schools_command", "") == "inspect" and getattr(
            args, "selected", False
        ):
            inspect_payload = daemon.build_school_runtime_luci_contract(
                cfg, school_runtime.inspect_runtime(cfg)
            )
            print(json.dumps(inspect_payload, ensure_ascii=False, indent=2))
            return
        print(json.dumps(schools.list_schools(), ensure_ascii=False, indent=2))
        return

    if args.command == "switch":
        cfg = daemon.load_config()
        expect_hotspot = args.target == "hotspot"
        _, message = daemon.run_switch(cfg, expect_hotspot=expect_hotspot)
        daemon.log(
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
            daemon._show_config()
            return

        if cmd == "get":
            daemon._config_get(args.key)
            return

        if cmd == "set":
            daemon._config_set(args.pairs, json_file=args.file)
            return

        if cmd == "account":
            daemon._config_account(args)
            return

        if cmd == "hotspot":
            daemon._config_hotspot(args)
            return

        parser.parse_args(["config", "--help"])
        return

    daemon._emit_cli_result(school_runtime.dispatch_custom_cli(runtime, app_ctx, args))
