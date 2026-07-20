import io
import importlib.util
import json
import os
import re
import sys
import tempfile
import unittest
from contextlib import ExitStack, redirect_stdout
from urllib.error import HTTPError
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

# Python 3.14 起 argparse 会给帮助输出着色，断言前需剥离 ANSI 转义序列。
_ANSI_ESCAPE_RE = re.compile(r"\x1b\[[0-9;]*m")


def strip_ansi(text):
    return _ANSI_ESCAPE_RE.sub("", text)


if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


from _portal_urls import PORTAL_ACID4_THEME_URL, PORTAL_HTTPS_ORIGIN, PORTAL_ORIGIN
import daemon
import version_info
import school_runtime
import schools


def load_hot_update_module(test_case):
    script_path = os.path.join(REPO_ROOT, "scripts", "hot_update.py")
    if not os.path.exists(script_path):
        test_case.fail("scripts/hot_update.py missing")

    spec = importlib.util.spec_from_file_location("hot_update", script_path)
    if spec is None or spec.loader is None:
        test_case.fail("failed to load scripts/hot_update.py")

    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def read_repo_text(*parts):
    path = os.path.join(REPO_ROOT, *parts)
    with open(path, "r", encoding="utf-8") as handle:
        return handle.read()


class FakeRuntime(object):
    def __init__(self):
        self.calls = []
        self.extra_commands = []
        self.extra_result = None
        self.daemon_result = None

    def get_cli_commands(self):
        return list(self.extra_commands)

    def handle_cli_command(self, app_ctx, args):
        self.calls.append(("handle_cli_command", args.command))
        return self.extra_result

    def cli_status(self, app_ctx, args):
        self.calls.append(("cli_status", args.command))
        print("STATUS:%s" % app_ctx["cfg"].get("school", "default"))
        return True, 0, ""

    def cli_login(self, app_ctx, args):
        self.calls.append(("cli_login", args.command))
        return True, 0, "runtime-cli-login"

    def cli_logout(self, app_ctx, args):
        self.calls.append(("cli_logout", args.command))
        return True, 0, "runtime-cli-logout"

    def cli_relogin(self, app_ctx, args):
        self.calls.append(("cli_relogin", args.command))
        return True, 0, "runtime-cli-relogin"

    def cli_daemon(self, app_ctx, args):
        self.calls.append(("cli_daemon", args.command))
        return True, 0, "runtime-cli-daemon"

    def status(self, app_ctx):
        self.calls.append(("status", app_ctx["cfg"].get("school")))
        return True, "runtime-status"

    def daemon_before_tick(self, app_ctx, state, interval):
        self.calls.append(("daemon_before_tick", interval))
        return self.daemon_result

    def handle_runtime_action(self, app_ctx, action, state):
        self.calls.append(("handle_runtime_action", action))
        return True, "runtime-action:%s" % action


class SchoolRuntimeCliTests(unittest.TestCase):
    def setUp(self):
        self.cfg = {"school": "custom", "enabled": "1", "interval": "30"}
        self.runtime = FakeRuntime()
        self.app_ctx = school_runtime.build_app_context(self.cfg, runtime=self.runtime)

    def run_main(self, argv):
        return self.run_main_with_runtime(argv, runtime=self.runtime)

    def run_main_with_runtime(
        self, argv, runtime=None, build_app_ctx=None, patch_runtime=True
    ):
        runtime = runtime if runtime is not None else self.runtime
        app_ctx = build_app_ctx if build_app_ctx is not None else self.app_ctx
        stdout = io.StringIO()
        with ExitStack() as stack:
            stack.enter_context(mock.patch.object(sys, "argv", ["srunnet"] + argv))
            stack.enter_context(
                mock.patch.object(daemon, "load_config", return_value=dict(self.cfg))
            )
            if patch_runtime:
                stack.enter_context(
                    mock.patch.object(
                        school_runtime, "resolve_runtime", return_value=runtime
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        school_runtime, "build_app_context", return_value=app_ctx
                    )
                )
            stack.enter_context(redirect_stdout(stdout))
            try:
                daemon.main()
                code = 0
            except SystemExit as exc:
                code = exc.code
        return code, stdout.getvalue()

    def test_bare_command_matches_status_dispatch(self):
        bare_code, bare_output = self.run_main([])
        status_code, status_output = self.run_main(["status"])

        self.assertEqual(bare_code, 0)
        self.assertEqual(status_code, 0)
        self.assertEqual(bare_output, status_output)
        self.assertIn(("status", "custom"), self.runtime.calls)
        self.assertNotIn(("cli_status", "status"), self.runtime.calls)

    def test_schools_list_keeps_metadata_shape(self):
        payload = [
            {
                "short_name": "jxnu",
                "name": "JXNU",
                "description": "desc",
                "contributors": ["a"],
                "operators": [{"id": "", "label": "校园网"}],
            }
        ]
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "schools"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(
                school_runtime, "resolve_runtime", return_value=self.runtime
            ),
            mock.patch.object(
                school_runtime, "build_app_context", return_value=self.app_ctx
            ),
            mock.patch.object(schools, "list_schools", return_value=payload),
            redirect_stdout(stdout),
        ):
            daemon.main()

        self.assertEqual(json.loads(stdout.getvalue()), payload)

    def test_schools_inspect_selected_returns_selected_runtime_metadata(self):
        inspect_payload = {
            "short_name": "custom",
            "runtime_type": "runtime_class",
            "runtime_api_version": 1,
            "source_file": "/tmp/custom.py",
            "declared_capabilities": ["cli", "daemon"],
        }
        stdout = io.StringIO()
        with (
            mock.patch.object(
                sys, "argv", ["srunnet", "schools", "inspect", "--selected"]
            ),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(
                school_runtime, "resolve_runtime", return_value=self.runtime
            ),
            mock.patch.object(
                school_runtime, "build_app_context", return_value=self.app_ctx
            ),
            mock.patch.object(
                school_runtime, "inspect_runtime", return_value=inspect_payload
            ),
            redirect_stdout(stdout),
        ):
            daemon.main()

        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "short_name": "custom",
                "runtime_type": "runtime_class",
                "runtime_api_version": 1,
                "source_file": "/tmp/custom.py",
                "declared_capabilities": ["cli", "daemon"],
                "capabilities": ["cli", "daemon"],
                "field_descriptors": None,
                "school_extra": None,
            },
        )

    def test_reserved_commands_cannot_be_replaced_by_runtime(self):
        self.runtime.extra_commands = [{"name": "status", "help": "bad"}]

        with self.assertRaisesRegex(ValueError, "reserved command"):
            self.run_main(["custom"])

    def test_all_builtin_top_level_commands_are_reserved(self):
        reserved = [
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
            "help",
            "man",
            "update",
            "presets",
            "detect",
        ]

        for name in reserved:
            with self.subTest(name=name):
                self.runtime.extra_commands = [{"name": name, "help": "bad"}]
                with self.assertRaisesRegex(ValueError, "reserved command"):
                    self.run_main(["custom"])

    def test_man_command_prints_full_manual_with_key_sections(self):
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "man"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            redirect_stdout(stdout),
        ):
            daemon.main()

        text = stdout.getvalue()
        for marker in (
            "SMART SRun",
            "名称",
            "用法",
            "命令分组",
            "主要配置项",
            "日志等级",
            "文件",
            "退出码",
            "示例",
            "log_level",
            "switch hotspot",
            "/var/log/smart_srun.log",
        ):
            self.assertIn(marker, text)

    def test_help_without_args_prints_top_level_help(self):
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "help"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            redirect_stdout(stdout),
        ):
            daemon.main()

        text = strip_ansi(stdout.getvalue())
        self.assertIn("usage: srunnet", text)
        self.assertIn("常用命令组", text)

    def test_help_with_command_prints_subcommand_help(self):
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "help", "config"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            redirect_stdout(stdout),
        ):
            daemon.main()

        text = strip_ansi(stdout.getvalue())
        self.assertIn("usage: srunnet config", text)
        self.assertIn("show", text)

    def test_help_with_nested_command_walks_subparser_chain(self):
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "help", "config", "account"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            redirect_stdout(stdout),
        ):
            daemon.main()

        text = strip_ansi(stdout.getvalue())
        self.assertIn("usage: srunnet config account", text)

    def test_help_with_unknown_command_returns_nonzero_and_writes_to_stderr(self):
        stderr = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "help", "no-such-cmd"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(sys, "stderr", stderr),
        ):
            with self.assertRaises(SystemExit) as exc:
                daemon.main()

        self.assertEqual(exc.exception.code, 2)
        self.assertIn("no-such-cmd", stderr.getvalue())

    def test_runtime_cli_dispatch_requires_fixed_result_shape(self):
        self.runtime.extra_commands = [{"name": "custom", "help": "custom command"}]
        self.runtime.extra_result = (0, "bad-shape")

        with self.assertRaisesRegex(RuntimeError, "CLI contract"):
            self.run_main(["custom"])

    def test_runtime_cli_dispatch_uses_exit_code_and_message(self):
        self.runtime.extra_commands = [{"name": "custom", "help": "custom command"}]
        self.runtime.extra_result = (True, 7, "runtime custom message")

        code, output = self.run_main(["custom"])

        self.assertEqual(code, 7)
        self.assertIn("runtime custom message", output)
        self.assertIn(("handle_cli_command", "custom"), self.runtime.calls)

    def test_daemon_early_stop_requires_ok_message_tuple(self):
        self.runtime.daemon_result = (True, "stop", "bad")

        with self.assertRaisesRegex(RuntimeError, "daemon contract"):
            daemon._run_runtime_daemon_hook(self.app_ctx, {"was_online": False}, 30)

    def test_reserved_status_command_ignores_runtime_cli_hook(self):
        with mock.patch.object(daemon, "_show_status") as show_status:
            code, output = self.run_main(["status"])

        self.assertEqual(code, 0)
        self.assertEqual(output, "")
        show_status.assert_called_once_with(self.cfg)
        self.assertNotIn(("cli_status", "status"), self.runtime.calls)

    def test_reserved_login_logout_relogin_commands_ignore_runtime_cli_hooks(self):
        with (
            mock.patch.object(
                daemon, "_runtime_cli_login", return_value=(True, 0, "core-login")
            ),
            mock.patch.object(
                daemon, "_runtime_cli_logout", return_value=(True, 0, "core-logout")
            ),
            mock.patch.object(
                daemon, "_runtime_cli_relogin", return_value=(True, 0, "core-relogin")
            ),
        ):
            login_code, login_output = self.run_main(["login"])
            logout_code, logout_output = self.run_main(["logout"])
            relogin_code, relogin_output = self.run_main(["relogin"])

        self.assertEqual((login_code, login_output.strip()), (0, "core-login"))
        self.assertEqual((logout_code, logout_output.strip()), (0, "core-logout"))
        self.assertEqual((relogin_code, relogin_output.strip()), (0, "core-relogin"))
        self.assertNotIn(("cli_login", "login"), self.runtime.calls)
        self.assertNotIn(("cli_logout", "logout"), self.runtime.calls)
        self.assertNotIn(("cli_relogin", "relogin"), self.runtime.calls)

    def test_reserved_daemon_command_ignores_runtime_cli_hook(self):
        with mock.patch.object(daemon, "run_daemon") as run_daemon:
            code, output = self.run_main(["daemon"])

        self.assertEqual(code, 0)
        self.assertEqual(output, "")
        run_daemon.assert_called_once_with(runtime=self.runtime)
        self.assertNotIn(("cli_daemon", "daemon"), self.runtime.calls)

    def test_log_runtime_prints_selected_runtime_block(self):
        inspect_payload = {
            "short_name": "custom",
            "runtime_type": "runtime_class",
            "runtime_api_version": 3,
            "capabilities": ["cli", "daemon"],
            "field_descriptors": [{"key": "region", "label": "Region", "type": "text"}],
            "school_extra": {"region": "north"},
        }

        with mock.patch.object(
            daemon,
            "build_school_runtime_luci_contract",
            return_value=inspect_payload,
        ) as build_contract:
            with mock.patch.object(
                school_runtime,
                "inspect_runtime",
                return_value={"short_name": "custom"},
            ) as inspect_runtime:
                code, output = self.run_main(["log", "runtime"])

        self.assertEqual(code, 0)
        self.assertEqual(
            output,
            "School: custom\n"
            "Runtime type: runtime_class\n"
            "Runtime API version: 3\n"
            "Capabilities: cli, daemon\n"
            'Field descriptors: [{"key": "region", "label": "Region", "type": "text"}]\n'
            'School extra: {"region": "north"}\n',
        )
        inspect_runtime.assert_called_once_with(self.cfg)
        build_contract.assert_called_once_with(self.cfg, {"short_name": "custom"})

    def test_log_runtime_uses_stable_fallbacks_for_minimal_or_empty_payload(self):
        cases = [
            ({}, self.cfg.get("school", "jxnu")),
            (
                {
                    "short_name": "",
                    "runtime_type": "",
                    "runtime_api_version": None,
                    "capabilities": [None, ""],
                    "field_descriptors": None,
                    "school_extra": None,
                },
                self.cfg.get("school", "jxnu"),
            ),
        ]

        for payload, expected_school in cases:
            with self.subTest(payload=payload):
                with mock.patch.object(
                    daemon,
                    "build_school_runtime_luci_contract",
                    return_value=payload,
                ):
                    with mock.patch.object(
                        school_runtime,
                        "inspect_runtime",
                        return_value={"short_name": "custom"},
                    ):
                        code, output = self.run_main(["log", "runtime"])

                self.assertEqual(code, 0)
                self.assertEqual(
                    output,
                    "School: %s\n"
                    "Runtime type: unknown\n"
                    "Runtime API version: 1\n"
                    "Capabilities: (none)\n"
                    "Field descriptors: null\n"
                    "School extra: null\n" % expected_school,
                )

    def test_log_runtime_handles_runtime_inspection_failure_without_traceback(self):
        with mock.patch.object(
            school_runtime,
            "inspect_runtime",
            side_effect=LookupError("boom"),
        ):
            code, output = self.run_main(["log", "runtime"])

        self.assertEqual(code, 1)
        self.assertIn("Runtime inspection failed: boom", output)
        self.assertNotIn("Traceback", output)

    def test_log_n_still_dispatches_to_tail_log(self):
        with mock.patch.object(daemon, "_tail_log") as tail_log:
            code, output = self.run_main(["log", "-n", "5"])

        self.assertEqual(code, 0)
        self.assertEqual(output, "")
        tail_log.assert_called_once_with(5)

    def test_log_without_args_still_dispatches_to_tail_log_follow_mode(self):
        with mock.patch.object(daemon, "_tail_log") as tail_log:
            code, output = self.run_main(["log"])

        self.assertEqual(code, 0)
        self.assertEqual(output, "")
        tail_log.assert_called_once_with(0)

    def test_version_flag_prints_cli_package_and_version(self):
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "--version"]),
            mock.patch.object(
                version_info,
                "get_cli_version_string",
                return_value="luci-app-smart-srun-bundle v1.3.0",
            ),
            redirect_stdout(stdout),
        ):
            with self.assertRaises(SystemExit) as exc:
                daemon.main()

        self.assertEqual(0, exc.exception.code)
        self.assertEqual("luci-app-smart-srun-bundle v1.3.0\n", stdout.getvalue())

    def test_schools_command_works_when_runtime_resolution_is_broken(self):
        payload = [{"short_name": "jxnu"}]
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "schools"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(
                school_runtime,
                "resolve_runtime",
                side_effect=AssertionError("should not resolve"),
            ),
            mock.patch.object(schools, "list_schools", return_value=payload),
            redirect_stdout(stdout),
        ):
            daemon.main()

        self.assertEqual(json.loads(stdout.getvalue()), payload)

    def test_update_status_command_prints_json_status(self):
        import updater

        payload = {"ok": True, "phase": "idle", "message": "未开始更新"}
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "update", "status"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(updater, "get_status", return_value=payload),
            redirect_stdout(stdout),
        ):
            daemon.main()

        self.assertEqual(json.loads(stdout.getvalue()), payload)

    def test_presets_refresh_command_prints_json_result(self):
        import school_presets

        payload = {"ok": True, "schools": [{"short_name": "qdu"}]}
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "presets", "refresh"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(school_presets, "refresh_remote_presets", return_value=payload),
            redirect_stdout(stdout),
        ):
            daemon.main()

        self.assertEqual(json.loads(stdout.getvalue()), payload)

    def test_detect_acid_command_prints_json_result(self):
        import portal_detect

        payload = {
            "ok": True,
            "acid": "4",
            "base_url": PORTAL_ORIGIN,
            "source": "input_url",
        }
        stdout = io.StringIO()
        with (
            mock.patch.object(
                sys,
                "argv",
                ["srunnet", "detect", "acid", PORTAL_ACID4_THEME_URL],
            ),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(portal_detect, "detect_acid", return_value=payload) as detect_acid,
            redirect_stdout(stdout),
        ):
            daemon.main()

        detect_acid.assert_called_once_with(
            PORTAL_ACID4_THEME_URL,
            reality_url="",
        )
        self.assertEqual(json.loads(stdout.getvalue()), payload)

    def test_detect_acid_command_uses_current_base_url_when_omitted(self):
        import portal_detect

        cfg = dict(self.cfg)
        cfg["base_url"] = PORTAL_HTTPS_ORIGIN
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "detect", "acid"]),
            mock.patch.object(daemon, "load_config", return_value=cfg),
            mock.patch.object(
                portal_detect, "detect_acid", return_value={"ok": False}
            ) as detect_acid,
            redirect_stdout(stdout),
        ):
            daemon.main()

        detect_acid.assert_called_once_with(
            PORTAL_HTTPS_ORIGIN,
            reality_url="",
        )

    def test_presets_list_command_prints_active_presets(self):
        import school_presets

        payload = [{"short_name": "jxnu"}, {"short_name": "qdu"}]
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "presets", "list"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(school_presets, "list_presets", return_value=payload) as list_presets,
            redirect_stdout(stdout),
        ):
            daemon.main()

        list_presets.assert_called_once_with(include_draft=False)
        self.assertEqual(json.loads(stdout.getvalue()), payload)

    def test_presets_list_command_keeps_operator_suffixes_in_operators(self):
        import school_presets

        stdout = io.StringIO()
        with tempfile.TemporaryDirectory() as tmp:
            cache_path = os.path.join(tmp, "missing-presets-cache.json")
            with (
                mock.patch.object(sys, "argv", ["srunnet", "presets", "list"]),
                mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
                mock.patch.object(school_presets, "CACHE_PRESETS_FILE", cache_path),
                redirect_stdout(stdout),
            ):
                daemon.main()

        payload = json.loads(stdout.getvalue())
        lnut = next(item for item in payload if item["short_name"] == "lnut-hld")
        jxnu = next(item for item in payload if item["short_name"] == "jxnu")
        self.assertNotIn("operator", lnut["defaults"])
        self.assertNotIn("operator_suffix", lnut["defaults"])
        self.assertNotIn("operator", jxnu["defaults"])
        self.assertNotIn("operator_suffix", jxnu["defaults"])
        self.assertEqual(lnut["operators"][0]["suffix"], "hcmcc")
        for preset in (lnut, jxnu):
            for operator in preset["operators"]:
                self.assertNotIn("operator_suffix", operator)

    def test_config_account_table_labels_follow_explicit_suffix_only(self):
        stdout = io.StringIO()
        raw = {
            "default_campus_id": "campus-1",
            "campus_accounts": [
                {
                    "id": "campus-1",
                    "user_id": "alice",
                    "operator": "cucc",
                    "operator_suffix": "",
                    "access_mode": "wifi",
                    "ssid": "campus",
                },
                {
                    "id": "campus-2",
                    "user_id": "bob",
                    "operator": "hcmcc",
                    "operator_suffix": "hcmcc",
                    "access_mode": "wired",
                    "n": "128",
                    "type": "3",
                    "enc": "custom_enc",
                    "info_prefix": "CUSTOM",
                },
            ],
        }

        with redirect_stdout(stdout):
            daemon._print_account_table(raw)

        text = stdout.getvalue()
        self.assertIn("alice", text)
        self.assertNotIn("alice@cucc", text)
        self.assertIn("bob@hcmcc", text)
        self.assertIn("128/3/custom_enc/C", text)

    def test_interactive_account_allows_custom_operator_suffix(self):
        with (
            mock.patch.object(daemon, "_get_current_profile", return_value=None),
            mock.patch(
                "builtins.input",
                side_effect=[
                    "",
                    "alice",
                    "hcmcc",
                    "wired",
                    PORTAL_ORIGIN,
                    "4",
                    "128",
                    "3",
                    "custom_enc",
                    "CUSTOM",
                    "1",
                    "windows",
                    "Windows",
                ],
            ),
            mock.patch("getpass.getpass", return_value="pw"),
        ):
            fields = daemon._interactive_campus()

        self.assertNotIn("operator", fields)
        self.assertEqual(fields["operator_suffix"], "hcmcc")
        self.assertEqual(fields["n"], "128")
        self.assertEqual(fields["type"], "3")
        self.assertEqual(fields["enc"], "custom_enc")
        self.assertEqual(fields["info_prefix"], "CUSTOM")
        self.assertEqual(fields["double_stack"], "1")
        self.assertEqual(fields["login_os"], "windows")
        self.assertEqual(fields["login_name"], "Windows")
        self.assertEqual(fields["label"], "alice@hcmcc")

    def test_config_show_works_when_runtime_resolution_is_broken(self):
        with (
            mock.patch.object(
                school_runtime,
                "resolve_runtime",
                side_effect=AssertionError("should not resolve"),
            ),
            mock.patch.object(daemon, "_show_config") as show_config,
        ):
            code, output = self.run_main_with_runtime(
                ["config", "show"],
                runtime=None,
                build_app_ctx=None,
                patch_runtime=False,
            )

        self.assertEqual(code, 0)
        self.assertEqual(output, "")
        show_config.assert_called_once_with()

    def test_top_level_help_works_when_runtime_resolution_is_broken(self):
        stdout = io.StringIO()
        with (
            mock.patch.object(sys, "argv", ["srunnet", "--help"]),
            mock.patch.object(daemon, "load_config", return_value=dict(self.cfg)),
            mock.patch.object(
                school_runtime,
                "resolve_runtime",
                side_effect=AssertionError("should not resolve"),
            ),
            redirect_stdout(stdout),
        ):
            with self.assertRaises(SystemExit) as exc:
                daemon.main()

        self.assertEqual(exc.exception.code, 0)
        self.assertIn("usage: srunnet", strip_ansi(stdout.getvalue()))

    def test_runtime_action_contract_error_is_isolated(self):
        state = {"current_mode": "campus", "last_switch_ts": 0}
        with (
            mock.patch.object(
                daemon, "pop_runtime_action", return_value={"action": "custom"}
            ),
            mock.patch.object(daemon, "save_runtime_status"),
            mock.patch.object(daemon, "build_runtime_snapshot", return_value={}),
            mock.patch.object(daemon, "mark_inflight_action"),
            mock.patch.object(daemon, "clear_inflight_action"),
        ):
            self.runtime.handle_runtime_action = mock.Mock(
                return_value=(True, "bad", "shape")
            )
            handled, message = daemon.handle_runtime_action(
                dict(self.cfg),
                state,
                runtime=self.runtime,
                app_ctx=self.app_ctx,
            )

        self.assertTrue(handled)
        self.assertIn("runtime action contract error", message)

    def test_runtime_action_exception_is_isolated(self):
        state = {"current_mode": "campus", "last_switch_ts": 0}
        with (
            mock.patch.object(
                daemon, "pop_runtime_action", return_value={"action": "custom"}
            ),
            mock.patch.object(daemon, "save_runtime_status"),
            mock.patch.object(daemon, "build_runtime_snapshot", return_value={}),
            mock.patch.object(daemon, "mark_inflight_action"),
            mock.patch.object(daemon, "clear_inflight_action"),
        ):
            self.runtime.handle_runtime_action = mock.Mock(
                side_effect=RuntimeError("boom")
            )
            handled, message = daemon.handle_runtime_action(
                dict(self.cfg),
                state,
                runtime=self.runtime,
                app_ctx=self.app_ctx,
            )

        self.assertTrue(handled)
        self.assertIn("runtime action failed", message)


class HotUpdateScriptTests(unittest.TestCase):
    def test_hot_update_defaults_to_current_router_host(self):
        hot_update = load_hot_update_module(self)

        self.assertEqual(hot_update.ROUTER_HOST, "10.0.0.1")

    def test_hot_update_has_no_default_router_password(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            hot_update = load_hot_update_module(self)

        self.assertIsNone(hot_update.ROUTER_PASSWORD)

    def test_hot_update_requires_password_from_environment(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            hot_update = load_hot_update_module(self)

        with self.assertRaises(RuntimeError) as exc:
            hot_update.require_router_password()

        self.assertIn("SMARTSRUN_ROUTER_PASSWORD", str(exc.exception))

    def test_hot_update_probe_arg_is_explicit(self):
        hot_update = load_hot_update_module(self)

        self.assertFalse(hot_update.build_arg_parser().parse_args([]).probe)
        self.assertTrue(hot_update.build_arg_parser().parse_args(["--probe"]).probe)
        self.assertTrue(hot_update.build_arg_parser().parse_args(["--dry-run"]).dry_run)
        parsed = hot_update.build_arg_parser().parse_args(["--probe", "--dry-run"])
        self.assertTrue(parsed.probe)
        self.assertTrue(parsed.dry_run)

    def test_hot_update_probe_remote_paths_are_rebased_to_tmp(self):
        hot_update = load_hot_update_module(self)

        self.assertEqual(
            hot_update.remote_path_for("/usr/bin/srunnet", "/tmp/probe"),
            "/tmp/probe/usr/bin/srunnet",
        )
        targets = hot_update.remote_target_paths("/tmp/probe")
        self.assertTrue(targets)
        self.assertTrue(
            all(item["remote"].startswith("/tmp/probe/") for item in targets)
        )
        self.assertIn(
            {
                "local": "root/usr/bin/srunnet",
                "remote": "/tmp/probe/usr/bin/srunnet",
                "original_remote": "/usr/bin/srunnet",
            },
            targets,
        )

    def test_hot_update_probe_commands_do_not_restart_production_services(self):
        hot_update = load_hot_update_module(self)

        commands = hot_update.build_probe_commands("/tmp/probe")
        probe_text = "\n".join(commands["probe_checks"])
        cleanup_text = "\n".join(commands["cleanup"])

        self.assertIn("/tmp/probe/usr/lib/smart_srun/school_runtime.py", probe_text)
        self.assertIn(
            "/tmp/probe/usr/lib/lua/luci/controller/smart_srun.lua", probe_text
        )
        self.assertIn("logger_probe.py", probe_text)
        self.assertIn("luci_friendly_probe.lua", probe_text)
        self.assertIn("client.py --version", probe_text)
        self.assertIn("rm -rf /tmp/probe", cleanup_text)
        self.assertNotIn("/etc/init.d/smart_srun restart", probe_text)
        self.assertNotIn("/etc/init.d/uwsgi restart", probe_text)

    def test_hot_update_main_probe_dispatches_without_hot_update(self):
        hot_update = load_hot_update_module(self)

        class FakeSftp(object):
            def __init__(self):
                self.closed = False

            def close(self):
                self.closed = True

        class FakeSsh(object):
            def __init__(self):
                self.sftp = FakeSftp()
                self.closed = False

            def open_sftp(self):
                return self.sftp

            def close(self):
                self.closed = True

        fake_ssh = FakeSsh()
        fake_paramiko = object()

        with (
            mock.patch.object(hot_update, "ensure_local_files") as ensure_files,
            mock.patch.object(
                hot_update, "require_router_password", return_value="secret"
            ) as require_password,
            mock.patch.object(
                hot_update, "load_paramiko", return_value=fake_paramiko
            ) as load_paramiko,
            mock.patch.object(
                hot_update, "connect_ssh", return_value=fake_ssh
            ) as connect_ssh,
            mock.patch.object(hot_update, "run_probe", return_value=0) as run_probe,
            mock.patch.object(hot_update, "run_hot_update") as run_hot_update,
        ):
            code = hot_update.main(["--probe"])

        self.assertEqual(code, 0)
        ensure_files.assert_called_once_with()
        require_password.assert_called_once_with()
        load_paramiko.assert_called_once_with()
        connect_ssh.assert_called_once_with(fake_paramiko, "secret")
        run_probe.assert_called_once_with(fake_ssh, fake_ssh.sftp)
        run_hot_update.assert_not_called()
        self.assertTrue(fake_ssh.sftp.closed)
        self.assertTrue(fake_ssh.closed)

    def test_hot_update_main_dry_run_does_not_connect_or_require_password(self):
        hot_update = load_hot_update_module(self)

        with (
            mock.patch.object(hot_update, "ensure_local_files") as ensure_files,
            mock.patch.object(hot_update, "require_router_password") as require_password,
            mock.patch.object(hot_update, "load_paramiko") as load_paramiko,
            mock.patch.object(hot_update, "connect_ssh") as connect_ssh,
            mock.patch.object(hot_update, "run_dry_run", return_value=0) as run_dry_run,
        ):
            code = hot_update.main(["--probe", "--dry-run"])

        self.assertEqual(code, 0)
        ensure_files.assert_called_once_with()
        run_dry_run.assert_called_once_with(probe=True)
        require_password.assert_not_called()
        load_paramiko.assert_not_called()
        connect_ssh.assert_not_called()

    def test_hot_update_probe_dry_run_prints_tmp_plan_without_restarts(self):
        hot_update = load_hot_update_module(self)
        stdout = io.StringIO()

        with redirect_stdout(stdout):
            code = hot_update.run_dry_run(probe=True)

        text = stdout.getvalue()
        self.assertEqual(code, 0)
        self.assertIn("DRY RUN: probe upload and smoke checks", text)
        self.assertIn("/tmp/smart_srun_probe_DRY_RUN/usr/bin/srunnet", text)
        self.assertIn("logger_probe.py", text)
        self.assertIn("luci_friendly_probe.lua", text)
        self.assertNotIn("/etc/init.d/smart_srun restart", text)
        self.assertNotIn("/etc/init.d/uwsgi restart", text)


class DaemonStartupStateTests(unittest.TestCase):
    def test_routine_online_tick_updates_state_without_info_log(self):
        self.assertFalse(
            daemon._should_log_daemon_tick("在线，下一次检测间隔 30 秒", {})
        )
        self.assertTrue(daemon._should_log_daemon_tick("校园网配置未就绪", {}))

    def test_run_daemon_preserves_pending_action_context_on_startup(self):
        cfg = {"enabled": "1", "interval": "30", "school": "custom"}
        startup_state = {
            "pending_action": "manual_login",
            "last_action": "manual_login",
            "last_action_ts": 1711111111,
            "action_result": "pending",
            "action_started_at": 1711111111,
            "message": "已提交手动登录请求",
        }
        save_calls = []

        class StopLoop(Exception):
            pass

        def fake_save_runtime_status(message, state=None, **extra):
            save_calls.append((message, dict(state or {}), dict(extra)))

        with (
            mock.patch.object(
                daemon, "_acquire_daemon_lock", return_value=object(), create=True
            ),
            mock.patch.object(daemon, "reconcile_manual_login_service_guard"),
            mock.patch.object(daemon, "load_config", return_value=dict(cfg)),
            mock.patch.object(
                school_runtime, "resolve_runtime", return_value=FakeRuntime()
            ),
            mock.patch.object(daemon, "build_runtime_snapshot", return_value={}),
            mock.patch.object(
                daemon, "save_runtime_status", side_effect=fake_save_runtime_status
            ),
            mock.patch.object(
                daemon,
                "handle_runtime_action",
                side_effect=StopLoop("stop after startup save"),
            ),
            mock.patch.object(
                daemon,
                "load_runtime_state",
                return_value=dict(startup_state),
                create=True,
            ),
            mock.patch.object(
                daemon,
                "load_pending_runtime_action",
                return_value={"action": "manual_login", "requested_at": 1711111111},
                create=True,
            ),
        ):
            with self.assertRaises(StopLoop):
                daemon.run_daemon()

        self.assertTrue(save_calls)
        _, _, startup_extra = save_calls[0]
        self.assertEqual(startup_extra["pending_action"], "manual_login")
        self.assertEqual(startup_extra["last_action"], "manual_login")
        self.assertEqual(startup_extra["action_result"], "pending")
        self.assertEqual(startup_extra["last_action_ts"], 1711111111)


class DaemonSingleInstanceTests(unittest.TestCase):
    def test_run_daemon_acquires_process_lock_before_entering_loop(self):
        cfg = {"enabled": "1", "interval": "30", "school": "custom"}

        class StopLoop(Exception):
            pass

        with (
            mock.patch.object(
                daemon, "_acquire_daemon_lock", create=True, return_value=object()
            ) as acquire_lock,
            mock.patch.object(daemon, "reconcile_manual_login_service_guard"),
            mock.patch.object(daemon, "load_config", return_value=dict(cfg)),
            mock.patch.object(
                school_runtime, "resolve_runtime", return_value=FakeRuntime()
            ),
            mock.patch.object(
                daemon, "_build_startup_status_payload", return_value=("startup", {})
            ),
            mock.patch.object(daemon, "build_runtime_snapshot", return_value={}),
            mock.patch.object(daemon, "save_runtime_status"),
            mock.patch.object(
                daemon, "handle_runtime_action", side_effect=StopLoop("stop after lock")
            ),
        ):
            with self.assertRaises(StopLoop):
                daemon.run_daemon()

        acquire_lock.assert_called_once_with()

    def test_run_daemon_stops_immediately_when_process_lock_is_unavailable(self):
        cfg = {"enabled": "1", "interval": "30", "school": "custom"}

        with (
            mock.patch.object(
                daemon, "_acquire_daemon_lock", create=True, side_effect=SystemExit(1)
            ),
            mock.patch.object(daemon, "reconcile_manual_login_service_guard"),
            mock.patch.object(daemon, "load_config", return_value=dict(cfg)),
            mock.patch.object(
                school_runtime, "resolve_runtime", return_value=FakeRuntime()
            ),
            mock.patch.object(
                daemon, "_build_startup_status_payload", return_value=("startup", {})
            ),
            mock.patch.object(daemon, "build_runtime_snapshot", return_value={}),
            mock.patch.object(daemon, "save_runtime_status"),
            mock.patch.object(
                daemon,
                "handle_runtime_action",
                side_effect=AssertionError("lock failure should stop before main loop"),
            ),
        ):
            with self.assertRaises(SystemExit):
                daemon.run_daemon()


class ForceClosePluginSourceTests(unittest.TestCase):
    def test_switch_section_exposes_page_level_force_close_flow(self):
        lua_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
        )
        js_source = read_repo_text(
            "root", "www", "luci-static", "resources", "smart_srun.js"
        )

        self.assertIn("smart-srun-force-close", lua_source)
        self.assertIn("强制关闭插件", lua_source)
        self.assertIn("/luci-static/resources/smart_srun.js", lua_source)
        self.assertIn(
            "forceClose.addEventListener('click', enqueueForceClose)", js_source
        )
        self.assertIn(
            "confirm('这会停止 SMART SRun 服务并终止插件进程，是否继续？')",
            js_source,
        )
        self.assertIn(
            "xhr.send('action=' + encodeURIComponent('force_stop'));", js_source
        )

    def test_shared_force_stop_controller_path_stays_smart_only(self):
        controller_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "controller", "smart_srun.lua"
        )

        self.assertIn('state.message = "已强制关闭插件并停止服务"', controller_source)
        self.assertIn(
            'return true, string.format("已强制关闭插件并停止服务（结束 %d 个进程）", #killed)',
            controller_source,
        )
        self.assertIn("/etc/init.d/smart_srun stop", controller_source)
        self.assertIn("/usr/lib/smart_srun/client.py", controller_source)
        self.assertNotIn("jxnu_srun", controller_source)


class LuciSourceHardeningTests(unittest.TestCase):
    def test_cbi_model_uses_escaped_hidden_json_payloads_and_static_js_asset(self):
        source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
        )

        self.assertIn("/luci-static/resources/smart_srun.js", source)
        self.assertIn('id="smart-campus-data"', source)
        self.assertIn('id="smart-hotspot-data"', source)
        self.assertIn("util.pcdata(campus_json)", source)
        self.assertIn("util.pcdata(hotspot_json)", source)
        self.assertNotIn("safe_json_for_script", source)
        self.assertNotIn('<script type="text/javascript">', source)

    def test_cbi_model_renders_version_badge_from_schema_module(self):
        source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
        )
        js_source = read_repo_text(
            "root", "www", "luci-static", "resources", "smart_srun.js"
        )
        schema_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "smart_srun", "schema.lua"
        )

        self.assertIn("schema.installed_package_display_text()", source)
        self.assertIn("深澜校园网认证配置", source)
        self.assertIn("当前版本：", source)
        self.assertIn("smart-srun-version-info", source)
        self.assertIn("smart-srun-update-dot", source)
        self.assertIn("Bundle 版", schema_source)
        self.assertIn("标准版", schema_source)
        self.assertIn(
            "https://api.github.com/repos/matthewlu070111/smart-srun/releases/latest",
            js_source,
        )
        self.assertIn(
            "https://github.com/matthewlu070111/smart-srun/releases",
            js_source,
        )
        self.assertIn("smart-srun-update-dot", js_source)
        self.assertIn("smart-srun-version-link", js_source)

    def test_luci_model_and_controller_share_schema_module(self):
        controller_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "controller", "smart_srun.lua"
        )
        model_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
        )
        schema_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "smart_srun", "schema.lua"
        )

        self.assertIn('require "luci.smart_srun.schema"', controller_source)
        self.assertIn('require "luci.smart_srun.schema"', model_source)
        self.assertIn("defaults.json", schema_source)
        self.assertIn("GLOBAL_SCALAR_KEYS", schema_source)
        self.assertIn("POINTER_KEYS", schema_source)
        self.assertIn("LIST_KEYS", schema_source)
        self.assertIn("global_scalar_key_set", schema_source)
        self.assertNotIn("local GLOBAL_SCALAR_KEYS_SET = {}", controller_source)

    def test_model_save_cfg_merges_latest_pointer_and_list_state(self):
        model_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
        )

        self.assertIn("local dirty_scalar_keys = {}", model_source)
        self.assertIn("local school_extra_dirty = false", model_source)
        self.assertIn(
            'local latest = jsonc.parse(fs.readfile(CONFIG_FILE) or "{}")', model_source
        )
        self.assertIn("dirty_scalar_keys[key]", model_source)
        self.assertIn('out[key] = tostring(latest[key] or "")', model_source)
        self.assertIn(
            'out[key] = type(latest[key]) == "table" and latest[key] or {}',
            model_source,
        )
        self.assertIn("function opt.remove(self, section)", model_source)
        self.assertIn('set_value(key, "")', model_source)

    def test_luci_presets_refresh_is_background_only(self):
        model_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
        )
        daemon_source = read_repo_text("root", "usr", "lib", "smart_srun", "daemon.py")

        self.assertIn('run_client("presets list", false)', model_source)
        self.assertIn("refresh_presets_cache_once()", model_source)
        self.assertIn("fs.access(PRESETS_CACHE_FILE)", model_source)
        self.assertIn("presets refresh >/dev/null 2>&1", model_source)
        self.assertIn("_refresh_school_presets_after_online", daemon_source)
        self.assertIn("school_presets.refresh_remote_presets(timeout=5)", daemon_source)
        self.assertIn('connectivity_level", "")).strip() != "online"', daemon_source)

    def test_luci_acid_detector_returns_fillable_aliases(self):
        controller_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "controller", "smart_srun.lua"
        )
        js_source = read_repo_text(
            "root", "www", "luci-static", "resources", "smart_srun.js"
        )

        self.assertIn("payload.value", controller_source)
        self.assertIn("payload.ac_id", controller_source)
        self.assertIn("data.acid || data.ac_id || data.value", js_source)

    def test_default_selection_updates_runtime_active_pointer(self):
        controller_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "controller", "smart_srun.lua"
        )
        daemon_source = read_repo_text("root", "usr", "lib", "smart_srun", "daemon.py")

        self.assertIn("cfg.default_campus_id = id", controller_source)
        self.assertIn("cfg.active_campus_id = id", controller_source)
        self.assertIn("cfg.default_hotspot_id = id", controller_source)
        self.assertIn("cfg.active_hotspot_id = id", controller_source)
        self.assertIn('"active_campus_id": args.id', daemon_source)
        self.assertIn('"active_hotspot_id": args.id', daemon_source)

    def test_luci_config_writes_use_temp_file_replace_flow(self):
        controller_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "controller", "smart_srun.lua"
        )
        model_source = read_repo_text(
            "root", "usr", "lib", "lua", "luci", "model", "cbi", "smart_srun.lua"
        )

        self.assertIn('local tmp = CONFIG_FILE .. ".tmp"', controller_source)
        self.assertIn("os.rename(tmp, CONFIG_FILE)", controller_source)
        self.assertIn('local tmp = CONFIG_FILE .. ".tmp"', model_source)
        self.assertIn("os.rename(tmp, CONFIG_FILE)", model_source)
        self.assertNotIn(
            'fs.writefile(CONFIG_FILE, (jsonc.stringify(out) or "{}") .. "\\n")',
            model_source,
        )

    def test_hot_update_uploads_runtime_payload_dependency_closure(self):
        hot_update = load_hot_update_module(self)

        uploaded = [item["local"] for item in hot_update.UPLOAD_TARGETS]
        expected_runtime_payload = {
            "root/usr/bin/srunnet",
            "root/usr/lib/smart_srun/client.py",
            "root/usr/lib/smart_srun/cli.py",
            "root/usr/lib/smart_srun/config.py",
            "root/usr/lib/smart_srun/crypto.py",
            "root/usr/lib/smart_srun/network.py",
            "root/usr/lib/smart_srun/wireless.py",
            "root/usr/lib/smart_srun/srun_auth.py",
            "root/usr/lib/smart_srun/orchestrator.py",
            "root/usr/lib/smart_srun/daemon.py",
            "root/usr/lib/smart_srun/snapshot.py",
            "root/usr/lib/smart_srun/school_runtime.py",
            "root/usr/lib/smart_srun/version_info.py",
            "root/usr/lib/smart_srun/school_presets.py",
            "root/usr/lib/smart_srun/portal_detect.py",
            "root/usr/lib/smart_srun/updater.py",
            "root/usr/lib/smart_srun/defaults.json",
            "root/usr/lib/smart_srun/school_presets_fallback.json",
            "root/usr/lib/smart_srun/schools/__init__.py",
            "root/usr/lib/smart_srun/schools/_base.py",
            "root/usr/lib/smart_srun/schools/jxnu.py",
            "root/usr/lib/lua/luci/smart_srun/schema.lua",
            "root/www/luci-static/resources/smart_srun.js",
        }

        self.assertTrue(
            expected_runtime_payload.issubset(set(uploaded)),
            "hot update payload must include runtime dependency closure",
        )

    def test_hot_update_forces_lf_for_init_script_upload(self):
        hot_update = load_hot_update_module(self)

        self.assertIn("/etc/init.d/smart_srun", hot_update.FORCE_LF_TARGETS)
        self.assertIn("/usr/bin/srunnet", hot_update.FORCE_LF_TARGETS)

    def test_hot_update_can_read_luci_login_page_from_http_403(self):
        hot_update = load_hot_update_module(self)

        class FakeOpener(object):
            def open(self, url, data=None, timeout=10):
                del url, data, timeout
                raise HTTPError(
                    "http://router/cgi-bin/luci/",
                    403,
                    "Forbidden",
                    None,
                    io.BytesIO(b"<form>login</form>"),
                )

        status, body, final_url = hot_update.open_url(
            FakeOpener(),
            "http://router/cgi-bin/luci/",
            allow_statuses=(403,),
        )

        self.assertEqual(status, 403)
        self.assertEqual(body, "<form>login</form>")
        self.assertEqual(final_url, "http://router/cgi-bin/luci/")

    def test_hot_update_remote_checks_cover_runtime_loader_smoke_paths(self):
        hot_update = load_hot_update_module(self)

        commands = hot_update.build_remote_commands()
        syntax_commands = commands["syntax_checks"]
        sanity_commands = commands["sanity_checks"]
        restart_commands = commands["restart"]

        self.assertTrue(
            any(
                "/usr/lib/smart_srun/school_runtime.py" in command
                for command in syntax_commands
            )
        )
        self.assertTrue(
            any(
                "/usr/lib/smart_srun/schools/__init__.py" in command
                for command in syntax_commands
            )
        )
        self.assertTrue(
            any(
                "/usr/lib/lua/luci/smart_srun/schema.lua" in command
                for command in syntax_commands
            )
        )
        self.assertTrue(
            any(
                "import school_runtime" in command
                and "import schools" in command
                and "import updater" in command
                and "import cli" in command
                for command in sanity_commands
            ),
            "hot update sanity checks must smoke-test runtime loader imports",
        )
        self.assertIn("srunnet schools", sanity_commands)
        self.assertIn("srunnet presets list", sanity_commands)
        self.assertIn("srunnet schools inspect --selected", sanity_commands)
        self.assertIn("srunnet update status", sanity_commands)
        self.assertIn("/etc/init.d/smart_srun restart", restart_commands)
        self.assertIn("/etc/init.d/uwsgi restart", restart_commands)

    def test_hot_update_restores_executable_permissions_for_entrypoints(self):
        hot_update = load_hot_update_module(self)

        class FakeSsh(object):
            pass

        with mock.patch.object(
            hot_update, "run_remote", return_value=(0, "", "")
        ) as run_remote:
            hot_update.restore_executable_permissions(FakeSsh())

        run_remote.assert_called_once_with(
            mock.ANY,
            "chmod 755 /usr/bin/srunnet /etc/init.d/smart_srun",
        )

    def test_verify_luci_page_accepts_school_runtime_markup_without_diagnostics(self):
        hot_update = load_hot_update_module(self)
        page = """
        <html>
            <script src="/luci-static/resources/smart_srun.js"></script>
            <input id="cbid.smart_srun.main.school" />
            <input id="cbid.smart_srun.main._school_extra_region" />
        </html>
        """

        with mock.patch.object(hot_update, "build_luci_opener", return_value=object()):
            with mock.patch.object(hot_update, "login_luci", return_value="ok"):
                with mock.patch.object(
                    hot_update, "fetch_luci_page", return_value=page
                ):
                    with mock.patch.object(
                        hot_update,
                        "open_url",
                        return_value=(
                            200,
                            "window.smartOpenBlockingFeedback = function() {};",
                            "http://router/luci-static/resources/smart_srun.js",
                        ),
                    ) as open_url:
                        verified = hot_update.verify_luci_page(
                            expected_descriptor_count=1
                        )

        self.assertEqual(verified, page)
        open_url.assert_called()


class CliSplitSourceTests(unittest.TestCase):
    def test_client_entrypoint_uses_cli_main(self):
        client_source = read_repo_text("root", "usr", "lib", "smart_srun", "client.py")
        cli_source = read_repo_text("root", "usr", "lib", "smart_srun", "cli.py")
        daemon_source = read_repo_text("root", "usr", "lib", "smart_srun", "daemon.py")

        self.assertIn("from cli import main", client_source)
        self.assertIn("import argparse", cli_source)
        self.assertIn('prog="srunnet"', cli_source)
        self.assertNotIn('prog="srunnet"', daemon_source)
        self.assertNotIn("import argparse", daemon_source)


class PackagingLayoutTests(unittest.TestCase):
    def test_makefile_installs_new_luci_schema_and_static_asset(self):
        makefile = read_repo_text("Makefile")

        self.assertIn("/usr/lib/lua/luci/smart_srun", makefile)
        self.assertIn("root/usr/lib/lua/luci/smart_srun/*.lua", makefile)
        self.assertIn("/www/luci-static/resources", makefile)
        self.assertIn("root/www/luci-static/resources/*.js", makefile)


if __name__ == "__main__":
    unittest.main()
