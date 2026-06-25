"""
CLI entrypoint for SMART SRun.

Keeps argparse and top-level command dispatch separate from daemon runtime logic.

设计要点：
- 顶层 parser 给出分组命令一览与示例（epilog），便于直接 --help 速查；
- 每个子命令带中文 description，对 'srunnet CMD --help' 友好；
- 'srunnet man' 打印完整手册（man-page 风格），
  'srunnet help [COMMAND]' 等价于 'srunnet [COMMAND] --help'。
"""

import argparse
import json
import sys

import daemon
import school_runtime
import schools
import version_info


TOP_DESCRIPTION = (
    "SMART SRun -- OpenWrt 智慧深澜校园网认证客户端 CLI。\n"
    "无参数运行（srunnet）等同 'srunnet status'；\n"
    "运行 'srunnet help' 查看子命令帮助，'srunnet man' 查看完整中文手册。"
)


TOP_EPILOG = (
    "常用命令组（详见 'srunnet man'）：\n"
    "  认证      login / logout / relogin / status\n"
    "  守护      daemon / enable / disable\n"
    "  切网      switch {hotspot|campus}\n"
    "  日志      log [-n N] / log runtime\n"
    "  配置      config show / config get KEY / config set KEY=VAL [...]\n"
    "            config account [add|edit|rm|default] [ID]\n"
    "            config hotspot [add|edit|rm|default] [ID]\n"
    "  学校      schools / schools inspect --selected\n"
    "  预设      presets list / presets refresh\n"
    "  探测      detect acid [BASE_URL]\n"
    "  更新      update check / update run / update status\n"
    "  帮助      help [COMMAND] / man / --version\n"
    "\n"
    "示例：\n"
    "  srunnet                                # 等同 srunnet status\n"
    "  srunnet login                          # 触发一次登录\n"
    "  srunnet log -n 50                      # 查看最近 50 行日志\n"
    "  srunnet config set interval=30 log_level=DEBUG\n"
    "  srunnet switch hotspot                 # 切换到热点\n"
    "  srunnet help config                    # 查看 config 子命令帮助\n"
)


def _make_subparser(sub, name, *, help_text, description, epilog=None):
    return sub.add_parser(
        name,
        help=help_text,
        description=description,
        epilog=epilog,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )


def _build_parser():
    parser = argparse.ArgumentParser(
        prog="srunnet",
        description=TOP_DESCRIPTION,
        epilog=TOP_EPILOG,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--version",
        action="version",
        version=version_info.get_cli_version_string(),
    )
    sub = parser.add_subparsers(dest="command", metavar="COMMAND")

    _make_subparser(
        sub, "status",
        help_text="显示当前状态（默认命令）",
        description="打印当前在线状态、守护进程是否运行、最近一次动作及错误信息。\n"
                    "无参数调用 srunnet 等同此命令。",
    )

    _make_subparser(
        sub, "login",
        help_text="触发一次登录",
        description="通过 SRun 协议执行一次完整的 challenge -> 加密 -> login 流程。\n"
                    "成功后退出码为 0；失败时打印错误并返回非零码。",
    )

    _make_subparser(
        sub, "logout",
        help_text="登出当前在线账号",
        description="向认证网关发送下线请求，并轮询确认终态。\n"
                    "若守护进程开启，下次循环可能立即重新登录；如需保持登出请先 'srunnet disable'。",
    )

    _make_subparser(
        sub, "relogin",
        help_text="先登出再登录",
        description="组合命令：等价于先 'srunnet logout' 再 'srunnet login'，常用于强制刷新会话。",
    )

    _make_subparser(
        sub, "daemon",
        help_text="以守护进程方式运行（init 脚本调用）",
        description="进入主循环：周期性检查在线状态、按夜间时段切网、执行待处理 runtime action。\n"
                    "正常情况下不需要手动执行；交给 procd 即可。",
    )

    _make_subparser(
        sub, "enable",
        help_text="启用守护进程（写入 enabled=1）",
        description="把 enabled 配置项设为 \"1\" 并落盘，下次 daemon 循环或 init 启动后生效。",
    )

    _make_subparser(
        sub, "disable",
        help_text="禁用守护进程（写入 enabled=0）",
        description="把 enabled 配置项设为 \"0\" 并落盘。daemon 进入待机，不再自动登录。",
    )

    p_schools = _make_subparser(
        sub, "schools",
        help_text="列出所有可用学校（JSON 输出）",
        description="不带子命令时打印所有 schools/ 模块的 SCHOOL_METADATA 列表。",
    )
    schools_sub = p_schools.add_subparsers(dest="schools_command", metavar="SUBCOMMAND")
    p_schools_inspect = _make_subparser(
        schools_sub, "inspect",
        help_text="查看学校 runtime 详情",
        description="结合 --selected 打印当前生效学校的 runtime 元数据 / capabilities / school_extra（JSON）。",
    )
    p_schools_inspect.add_argument(
        "--selected",
        action="store_true",
        help="打印当前生效学校的 runtime 元数据（JSON）",
    )

    p_presets = _make_subparser(
        sub,
        "presets",
        help_text="管理远端学校预设",
        description="列出或刷新 GitHub 远端学校预设缓存。",
    )
    presets_sub = p_presets.add_subparsers(dest="presets_command", metavar="SUBCOMMAND")
    _make_subparser(
        presets_sub,
        "list",
        help_text="列出学校预设",
        description="输出可用学校预设列表（JSON），供 LuCI 账号管理页面使用。",
    )
    _make_subparser(
        presets_sub,
        "refresh",
        help_text="刷新远端学校预设缓存",
        description="从 raw GitHub 拉取 doc/school-presets.json 并写入本地缓存。",
    )

    p_detect = _make_subparser(
        sub,
        "detect",
        help_text="探测认证网关参数",
        description="从认证地址、跳转链或 Portal 页面中探测 AC_ID 等登录参数。",
    )
    detect_sub = p_detect.add_subparsers(dest="detect_command", metavar="SUBCOMMAND")
    p_detect_acid = _make_subparser(
        detect_sub,
        "acid",
        help_text="嗅探 AC_ID",
        description="非侵入式读取 URL、跳转和 Portal 页面中的 ac_id，不做登录枚举测试。",
    )
    p_detect_acid.add_argument(
        "base_url",
        nargs="?",
        help="认证地址；留空则使用当前默认校园网账号的认证地址",
    )
    p_detect_acid.add_argument(
        "--reality-url",
        default="",
        help="可选：先访问一个会被校园网劫持的 HTTP 地址并从跳转中提取 ac_id",
    )

    p_update = _make_subparser(
        sub,
        "update",
        help_text="检查或安装 SMART SRun 更新",
        description="检查 latest release，或下载并安装匹配当前包型的更新包。",
    )
    update_sub = p_update.add_subparsers(dest="update_command", metavar="SUBCOMMAND")
    _make_subparser(
        update_sub,
        "check",
        help_text="检查 latest release",
        description="输出当前版本、latest release、包型和可更新状态（JSON）。",
    )
    p_update_run = _make_subparser(
        update_sub,
        "run",
        help_text="下载并安装更新",
        description="前台执行完整更新；LuCI 可使用 --background 后台执行。",
    )
    p_update_run.add_argument(
        "--background",
        action="store_true",
        help="后台执行更新并立即返回状态",
    )
    p_update_run.add_argument(
        "--foreground",
        action="store_true",
        help=argparse.SUPPRESS,
    )
    _make_subparser(
        update_sub,
        "status",
        help_text="查看更新任务状态",
        description="读取 /var/run/smart_srun/update_status.json（JSON）。",
    )

    p_log = _make_subparser(
        sub, "log",
        help_text="跟随或查看运行日志",
        description="不带参数时跟随 /var/log/smart_srun.log；\n"
                    "-n N 显示最后 N 行后退出；\n"
                    "log runtime 显示当前学校 runtime 的诊断块。",
        epilog="示例：\n"
               "  srunnet log              # 实时跟随\n"
               "  srunnet log -n 100       # 最近 100 行\n"
               "  srunnet log runtime      # runtime 诊断\n",
    )
    p_log.add_argument(
        "-n", type=int, default=0,
        help="显示最后 N 行后退出（默认 0 = 持续跟随）",
    )
    p_log.add_argument(
        "log_target",
        nargs="?",
        choices=["runtime"],
        help="可选目标：'runtime' 打印学校 runtime 诊断信息",
    )

    p_switch = _make_subparser(
        sub, "switch",
        help_text="手动切换网络（热点 / 校园网）",
        description="切到热点（hotspot）或切回校园网（campus）。\n"
                    "切换会临时改写 wireless 配置并等待 IP 就绪。",
    )
    p_switch.add_argument(
        "target", choices=["hotspot", "campus"],
        help="目标：hotspot = 切到个人热点；campus = 切回校园网",
    )

    p_config = _make_subparser(
        sub, "config",
        help_text="查看或修改配置",
        description="包含若干子命令：show / get / set / account / hotspot。\n"
                    "不带子命令等同 'config show'。",
        epilog="示例：\n"
               "  srunnet config                                # 显示完整配置\n"
               "  srunnet config get interval\n"
               "  srunnet config set interval=30 log_level=DEBUG\n"
               "  srunnet config set -f /tmp/my_config.json\n"
               "  srunnet config account add\n"
               "  srunnet config account default campus-1\n"
               "  srunnet config hotspot rm hotspot-2\n",
    )
    config_sub = p_config.add_subparsers(dest="config_command", metavar="SUBCOMMAND")

    _make_subparser(
        config_sub, "show",
        help_text="显示完整配置摘要",
        description="打印当前 config.json 的人类可读摘要。",
    )

    p_get = _make_subparser(
        config_sub, "get",
        help_text="读取一个标量配置项",
        description="按 key 名读取配置标量值（campus_accounts / hotspot_profiles 等列表请用 'show'）。",
    )
    p_get.add_argument("key", help="配置 key 名（如 enabled、interval、log_level）")

    p_set = _make_subparser(
        config_sub, "set",
        help_text="写入标量值或从 JSON 文件导入",
        description="位置参数 KEY=VALUE 可以传多个；\n"
                    "或用 -f / --file 从 JSON 文件批量导入（覆盖式合并）。",
    )
    p_set.add_argument(
        "pairs", nargs="*", metavar="KEY=VALUE",
        help="待写入的标量配置（可重复，例：interval=30 log_level=DEBUG）",
    )
    p_set.add_argument(
        "-f", "--file", metavar="PATH",
        help="从 JSON 文件导入配置",
    )

    p_account = _make_subparser(
        config_sub, "account",
        help_text="管理校园网账号（add / edit / rm / default）",
        description="不带子命令时列出所有校园网账号。",
    )
    account_sub = p_account.add_subparsers(dest="account_command", metavar="SUBCOMMAND")
    _make_subparser(
        account_sub, "add",
        help_text="交互式新增校园网账号",
        description="按提示输入学工号、密码、运营商等字段。",
    )
    p_acc_edit = _make_subparser(
        account_sub, "edit",
        help_text="编辑指定 ID 的校园网账号",
        description="打开交互式编辑会话，回车跳过保留原值。",
    )
    p_acc_edit.add_argument("id", help="账号 ID（如 campus-1）")
    p_acc_rm = _make_subparser(
        account_sub, "rm",
        help_text="删除指定 ID 的校园网账号",
        description="删除前会请求确认。若被删除的是默认账号，default 指针会清空。",
    )
    p_acc_rm.add_argument("id", help="账号 ID（如 campus-1）")
    p_acc_def = _make_subparser(
        account_sub, "default",
        help_text="设置默认校园网账号",
        description="把指定 ID 设为 default_campus_id，daemon 启动时优先使用。",
    )
    p_acc_def.add_argument("id", help="账号 ID（如 campus-1）")

    p_hotspot = _make_subparser(
        config_sub, "hotspot",
        help_text="管理热点配置（add / edit / rm / default）",
        description="不带子命令时列出所有热点配置。",
    )
    hotspot_sub = p_hotspot.add_subparsers(dest="hotspot_command", metavar="SUBCOMMAND")
    _make_subparser(
        hotspot_sub, "add",
        help_text="交互式新增热点配置",
        description="按提示输入 SSID、加密方式、密码等字段。",
    )
    p_hp_edit = _make_subparser(
        hotspot_sub, "edit",
        help_text="编辑指定 ID 的热点配置",
        description="打开交互式编辑会话，回车跳过保留原值。",
    )
    p_hp_edit.add_argument("id", help="热点 ID（如 hotspot-1）")
    p_hp_rm = _make_subparser(
        hotspot_sub, "rm",
        help_text="删除指定 ID 的热点配置",
        description="删除前会请求确认。若被删除的是默认热点，default 指针会清空。",
    )
    p_hp_rm.add_argument("id", help="热点 ID（如 hotspot-1）")
    p_hp_def = _make_subparser(
        hotspot_sub, "default",
        help_text="设置默认热点配置",
        description="把指定 ID 设为 default_hotspot_id，failover 时优先使用。",
    )
    p_hp_def.add_argument("id", help="热点 ID（如 hotspot-1）")

    p_help = _make_subparser(
        sub, "help",
        help_text="显示子命令帮助（等价 --help）",
        description="不带参数时打印顶层帮助；后跟命令名等价于 'srunnet COMMAND --help'。\n"
                    "支持嵌套，如 'srunnet help config account'。",
    )
    p_help.add_argument(
        "help_targets", nargs="*", metavar="COMMAND",
        help="目标命令（可嵌套，如：config account）",
    )

    _make_subparser(
        sub, "man",
        help_text="显示完整中文手册",
        description="打印一份 man-page 风格的中文手册，覆盖命令、配置项、文件、日志等。",
    )

    return parser, sub


MANUAL_TEXT = """\
SMART SRun (srunnet)(1) -- OpenWrt 智慧深澜校园网认证客户端

名称
    srunnet -- 校园网自动认证守护进程与命令行入口

用法
    srunnet [COMMAND] [OPTIONS]

说明
    SMART SRun 是基于 SRun 4000 协议的 OpenWrt 校园网认证客户端，
    通过守护进程（daemon）自动维持在线，并支持手动登录登出、夜间时段
    自动切到个人热点、按账号分组管理等功能。
    本 CLI 与 LuCI Web 界面共用同一份配置 (/usr/lib/smart_srun/config.json)。

    无参数运行 srunnet 等同 'srunnet status'。

命令分组

  ▸ 认证
    status              显示当前在线 / 守护状态（默认命令）
    login               立即触发一次登录
    logout              登出当前在线账号
    relogin             先登出再登录，强制刷新会话

  ▸ 守护与服务
    daemon              进入守护循环（一般由 procd init 脚本调用）
    enable              写入 enabled=1
    disable             写入 enabled=0

  ▸ 网络切换
    switch hotspot      切到个人热点
    switch campus       切回校园网

  ▸ 日志
    log                 实时跟随 /var/log/smart_srun.log
    log -n N            显示最后 N 行后退出
    log runtime         打印当前学校 runtime 的诊断块

  ▸ 配置
    config              显示完整配置摘要（同 config show）
    config show         同上
    config get KEY      读取标量
    config set KEY=VAL [...]    写入标量（可重复）
    config set -f FILE.json     从 JSON 文件导入
    config account              列出所有校园网账号
    config account add          交互式新增
    config account edit ID      编辑指定 ID
    config account rm ID        删除
    config account default ID   设为默认
    config hotspot              列出所有热点
    config hotspot add / edit / rm / default ID    同上

  ▸ 学校
    schools                          列出可用学校 (JSON)
    schools inspect --selected       打印当前学校 runtime 元数据 (JSON)

  ▸ 探测
    detect acid [BASE_URL]            从认证地址 / Portal 页面嗅探 AC_ID

  ▸ 帮助与版本
    help [COMMAND]       显示子命令帮助（等价 --help）
    man                  显示本手册
    --version            显示当前安装包类型与版本

主要配置项（标量，UCI 风格字符串）
    enabled                       守护进程总开关（"0"/"1"）
    school                        当前学校短名（如 "jxnu"）
    interval                      守护循环间隔（秒）
    log_level                     日志等级 ALL / DEBUG / INFO / WARN / ERROR
    failover_enabled              登出时自动切到热点
    hotspot_failback_enabled      热点切换失败时自动回切校园网
    quiet_hours_enabled           启用夜间停用时段
    quiet_start, quiet_end        停用时段（北京时间 HH:MM）
    force_logout_in_quiet         进入停用时段时强制下线
    connectivity_check_mode       在线判定 internet / portal / ssid
    backoff_enable                启用指数退避
    backoff_initial_duration      退避初始秒数
    backoff_max_duration          退避上限秒数
    backoff_exponent_factor       退避指数因子
    retry_cooldown_seconds        重试基础冷却
    retry_max_cooldown_seconds    重试最大冷却
    switch_ready_timeout_seconds  切网后等待 IP 超时
    manual_terminal_check_max_attempts        手动终态校验次数
    manual_terminal_check_interval_seconds    手动终态校验间隔
    sta_iface                     绑定 STA 接口名（留空 = 自动）
    developer_mode                开发者模式

    复合配置 campus_accounts / hotspot_profiles 请通过
    'config account' / 'config hotspot' 子命令管理。

日志等级（按从详细到精简排序）
    ALL     最详细（包含所有 DEBUG 与内部细节）
    DEBUG   调试信息：HTTP 请求、SRun challenge / login 事件等
    INFO    默认：登录结果、切网结果、retry 周期摘要
    WARN    仅警告与错误
    ERROR   仅错误

文件
    /usr/lib/smart_srun/config.json     持久化配置（JSON）
    /usr/lib/smart_srun/defaults.json   默认值表
    /var/run/smart_srun/state.json      运行时状态
    /var/log/smart_srun.log             运行日志（512 KiB 自动轮转）
    /etc/init.d/smart_srun              procd 服务脚本
    /etc/config/smart_srun              UCI 配置（LuCI 写入入口）

退出码
    0    成功
    1    一般错误（认证失败、配置错误、网络异常等）
    其他 由各子命令 / 学校 runtime 自定义

环境变量（开发态）
    SMARTSRUN_ROUTER_HOST        scripts/hot_update.py 目标 IP（默认 10.0.0.1）
    SMARTSRUN_ROUTER_USER        SSH 用户名（默认 root）
    SMARTSRUN_ROUTER_PASSWORD    SSH 密码（必填）
    SMARTSRUN_LUCI_BASE_URL      LuCI 验证 URL 前缀

示例
    srunnet                                  # 等同 srunnet status
    srunnet login                            # 立即登录
    srunnet relogin                          # 强制刷新会话
    srunnet log -n 100                       # 最近 100 行
    srunnet log runtime                      # runtime 诊断
    srunnet config get interval
    srunnet config set interval=30 log_level=DEBUG
    srunnet config account default campus-1
    srunnet detect acid http://<portal-host>/srun_portal_pc?ac_id=<id>
    srunnet switch hotspot
    srunnet schools inspect --selected

相关链接
    LuCI 页面    服务 -> SMART SRun
    项目主页    https://github.com/matthewlu070111/smart-srun
    贡献指南    CONTRIBUTING.md
    开发者文档  CLAUDE.md / AGENTS.md（仓库根目录）

许可
    WTFPL
"""


def _print_manual():
    sys.stdout.write(MANUAL_TEXT)
    if not MANUAL_TEXT.endswith("\n"):
        sys.stdout.write("\n")


def _dispatch_help(parser, sub, targets):
    if not targets:
        parser.print_help()
        return 0
    current_sub = sub
    current_parser = None
    for idx, name in enumerate(targets):
        if current_sub is None or name not in current_sub.choices:
            sys.stderr.write(
                "srunnet: 未知命令 '%s'。运行 'srunnet help' 查看可用命令。\n"
                % " ".join(targets[: idx + 1])
            )
            return 2
        current_parser = current_sub.choices[name]
        current_sub = None
        for action in current_parser._actions:
            if isinstance(action, argparse._SubParsersAction):
                current_sub = action
                break
    current_parser.print_help()
    return 0


def main():
    cfg = daemon.load_config()
    runtime = None
    app_ctx = None
    argv = sys.argv[1:]

    parser, sub = _build_parser()

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

    if args.command == "help":
        code = _dispatch_help(parser, sub, list(getattr(args, "help_targets", []) or []))
        if code:
            raise SystemExit(code)
        return

    if args.command == "man":
        _print_manual()
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

    if args.command == "presets":
        import school_presets

        if getattr(args, "presets_command", "") == "list":
            print(
                json.dumps(
                    school_presets.list_presets(include_draft=False),
                    ensure_ascii=False,
                    indent=2,
                )
            )
            return
        if getattr(args, "presets_command", "") == "refresh":
            payload = school_presets.refresh_remote_presets()
            print(json.dumps(payload, ensure_ascii=False, indent=2))
            return
        parser.parse_args(["presets", "--help"])
        return

    if args.command == "detect":
        import portal_detect

        if getattr(args, "detect_command", "") == "acid":
            base_url = getattr(args, "base_url", "") or cfg.get("base_url", "")
            payload = portal_detect.detect_acid(
                base_url,
                reality_url=getattr(args, "reality_url", "") or "",
            )
            print(json.dumps(payload, ensure_ascii=False, indent=2))
            return
        parser.parse_args(["detect", "--help"])
        return

    if args.command == "update":
        import updater

        update_command = getattr(args, "update_command", "") or "status"
        if update_command == "check":
            print(json.dumps(updater.check_update(), ensure_ascii=False, indent=2))
            return
        if update_command == "status":
            print(json.dumps(updater.get_status(), ensure_ascii=False, indent=2))
            return
        if update_command == "run":
            if getattr(args, "background", False):
                payload = updater.start_background_update()
            else:
                payload = updater.run_update()
            print(json.dumps(payload, ensure_ascii=False, indent=2))
            return
        parser.parse_args(["update", "--help"])
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
