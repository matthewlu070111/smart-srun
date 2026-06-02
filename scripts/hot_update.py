#!/usr/bin/env python3

import argparse
import json
import os
import posixpath
import re
import shlex
import sys
import time
from http import cookiejar
from pathlib import Path
from urllib import error
from urllib import parse, request


REPO_ROOT = Path(__file__).resolve().parents[1]
ROUTER_HOST = os.environ.get("SMARTSRUN_ROUTER_HOST", "10.0.0.1")
ROUTER_USER = os.environ.get("SMARTSRUN_ROUTER_USER", "root")
ROUTER_PASSWORD = os.environ.get("SMARTSRUN_ROUTER_PASSWORD")
LUCI_BASE_URL = os.environ.get(
    "SMARTSRUN_LUCI_BASE_URL", "http://%s/cgi-bin/luci" % ROUTER_HOST
)
FORCE_LF_TARGETS = {"/etc/init.d/smart_srun", "/usr/bin/srunnet"}
EXECUTABLE_TARGETS = ["/usr/bin/srunnet", "/etc/init.d/smart_srun"]
PROBE_ROOT_PREFIX = "/tmp/smart_srun_probe"

RUNTIME_TARGETS = [
    {
        "local": "root/usr/bin/srunnet",
        "remote": "/usr/bin/srunnet",
    },
    {
        "local": "root/usr/lib/smart_srun/client.py",
        "remote": "/usr/lib/smart_srun/client.py",
    },
    {
        "local": "root/usr/lib/smart_srun/cli.py",
        "remote": "/usr/lib/smart_srun/cli.py",
    },
    {
        "local": "root/usr/lib/smart_srun/config.py",
        "remote": "/usr/lib/smart_srun/config.py",
    },
    {
        "local": "root/usr/lib/smart_srun/version_info.py",
        "remote": "/usr/lib/smart_srun/version_info.py",
    },
    {
        "local": "root/usr/lib/smart_srun/crypto.py",
        "remote": "/usr/lib/smart_srun/crypto.py",
    },
    {
        "local": "root/usr/lib/smart_srun/logger.py",
        "remote": "/usr/lib/smart_srun/logger.py",
    },
    {
        "local": "root/usr/lib/smart_srun/network.py",
        "remote": "/usr/lib/smart_srun/network.py",
    },
    {
        "local": "root/usr/lib/smart_srun/wireless.py",
        "remote": "/usr/lib/smart_srun/wireless.py",
    },
    {
        "local": "root/usr/lib/smart_srun/srun_auth.py",
        "remote": "/usr/lib/smart_srun/srun_auth.py",
    },
    {
        "local": "root/usr/lib/smart_srun/orchestrator.py",
        "remote": "/usr/lib/smart_srun/orchestrator.py",
    },
    {
        "local": "root/usr/lib/smart_srun/daemon.py",
        "remote": "/usr/lib/smart_srun/daemon.py",
    },
    {
        "local": "root/usr/lib/smart_srun/snapshot.py",
        "remote": "/usr/lib/smart_srun/snapshot.py",
    },
    {
        "local": "root/usr/lib/smart_srun/school_runtime.py",
        "remote": "/usr/lib/smart_srun/school_runtime.py",
    },
    {
        "local": "root/usr/lib/smart_srun/defaults.json",
        "remote": "/usr/lib/smart_srun/defaults.json",
    },
    {
        "local": "root/usr/lib/smart_srun/schools/__init__.py",
        "remote": "/usr/lib/smart_srun/schools/__init__.py",
    },
    {
        "local": "root/usr/lib/smart_srun/schools/_base.py",
        "remote": "/usr/lib/smart_srun/schools/_base.py",
    },
    {
        "local": "root/usr/lib/smart_srun/schools/jxnu.py",
        "remote": "/usr/lib/smart_srun/schools/jxnu.py",
    },
]

LUA_AND_SERVICE_TARGETS = [
    {
        "local": "root/usr/lib/lua/luci/controller/smart_srun.lua",
        "remote": "/usr/lib/lua/luci/controller/smart_srun.lua",
    },
    {
        "local": "root/usr/lib/lua/luci/smart_srun/schema.lua",
        "remote": "/usr/lib/lua/luci/smart_srun/schema.lua",
    },
    {
        "local": "root/usr/lib/lua/luci/model/cbi/smart_srun.lua",
        "remote": "/usr/lib/lua/luci/model/cbi/smart_srun.lua",
    },
    {
        "local": "root/www/luci-static/resources/smart_srun.js",
        "remote": "/www/luci-static/resources/smart_srun.js",
    },
    {
        "local": "root/etc/init.d/smart_srun",
        "remote": "/etc/init.d/smart_srun",
    },
]

UPLOAD_TARGETS = [
    *RUNTIME_TARGETS,
    *LUA_AND_SERVICE_TARGETS,
]

LOGGER_PROBE_CODE = r"""
import sys
import tempfile

sys.path.insert(0, "usr/lib/smart_srun")
import logger

logger.LOG_FILE = tempfile.mktemp(prefix="smart_srun_probe_log_", dir="/tmp")
logger.log(
    "INFO",
    "probe",
    "line1\nline2",
    password="secret",
    url="http://x/login",
)
data = open(logger.LOG_FILE, "r", encoding="utf-8").read()
print(data.strip())
assert "\\nline2" in data
assert "\nline2" not in data
assert "secret" not in data
assert "password=***" in data
assert data.count("\n") == 1
"""


def _lua_string(value):
    return json.dumps(str(value), ensure_ascii=True)


def build_luci_friendly_probe_code(probe_root):
    return """
local PROBE = %s
package.path = PROBE .. "/usr/lib/lua/?.lua;" .. PROBE .. "/usr/lib/lua/?/init.lua;" .. package.path
dofile(PROBE .. "/usr/lib/lua/luci/controller/smart_srun.lua")
local controller = package.loaded["luci.controller.smart_srun"]
assert(controller and controller.friendly_line)
local out = controller.friendly_line('[2026-06-01 22:00:00] INFO http_fetch_result url="http://example/login" status_code=200 duration_ms=123 password=*** | ok')
print(out)
assert(out:find("URL=http://example/login", 1, true))
assert(out:find("状态码=200", 1, true))
assert(out:find("耗时=123ms", 1, true))
assert(not out:find("password", 1, true))
assert(not out:find("***", 1, true))
""" % _lua_string(probe_root)


def build_remote_commands():
    python_files = [
        item["remote"] for item in UPLOAD_TARGETS if item["remote"].endswith(".py")
    ]
    return {
        "syntax_checks": [
            "python3 -m py_compile %s" % " ".join(python_files),
            "lua -e \"assert(loadfile('/usr/lib/lua/luci/controller/smart_srun.lua'))\"",
            "lua -e \"assert(loadfile('/usr/lib/lua/luci/smart_srun/schema.lua'))\"",
            "lua -e \"assert(loadfile('/usr/lib/lua/luci/model/cbi/smart_srun.lua'))\"",
            "sh -n /etc/init.d/smart_srun",
        ],
        "cache_cleanup": [
            "rm -rf /tmp/luci-*",
            "rm -f /usr/lib/smart_srun/__pycache__/*.pyc",
            "rm -f /usr/lib/smart_srun/schools/__pycache__/*.pyc",
        ],
        "restart": [
            "/etc/init.d/smart_srun restart",
            "/etc/init.d/uwsgi restart",
        ],
        "sanity_checks": [
            "python3 -c \"import sys; sys.path.insert(0, '/usr/lib/smart_srun'); import cli; import school_runtime; import schools; import srun_auth; import orchestrator; import snapshot; import daemon; print('runtime-loader-import-ok')\"",
            "srunnet status",
            "srunnet schools",
            "srunnet schools inspect --selected",
        ],
    }


def remote_path_for(remote_path, remote_root=None):
    if not remote_root:
        return remote_path
    return posixpath.join(remote_root, str(remote_path).lstrip("/"))


def remote_target_paths(remote_root=None):
    return [
        {
            "local": item["local"],
            "remote": remote_path_for(item["remote"], remote_root),
            "original_remote": item["remote"],
        }
        for item in UPLOAD_TARGETS
    ]


def build_probe_commands(probe_root):
    targets = remote_target_paths(probe_root)
    python_files = [
        item["remote"] for item in targets if item["remote"].endswith(".py")
    ]
    python_path = remote_path_for("/usr/lib/smart_srun", probe_root)
    controller_path = remote_path_for(
        "/usr/lib/lua/luci/controller/smart_srun.lua", probe_root
    )
    schema_path = remote_path_for("/usr/lib/lua/luci/smart_srun/schema.lua", probe_root)
    model_path = remote_path_for(
        "/usr/lib/lua/luci/model/cbi/smart_srun.lua", probe_root
    )
    init_path = remote_path_for("/etc/init.d/smart_srun", probe_root)
    return {
        "probe_checks": [
            "python3 -m py_compile %s" % " ".join(python_files),
            "cd %s && PYTHONPATH=%s python3 -B -c %s"
            % (
                shlex.quote(probe_root),
                shlex.quote(python_path),
                shlex.quote(
                    "import sys; sys.path.insert(0, 'usr/lib/smart_srun'); "
                    "import cli; import school_runtime; import schools; "
                    "import srun_auth; import orchestrator; import snapshot; "
                    "import daemon; print('runtime-loader-import-ok')"
                ),
            ),
            "cd %s && PYTHONPATH=%s python3 -B logger_probe.py"
            % (shlex.quote(probe_root), shlex.quote(python_path)),
            "lua -e %s"
            % shlex.quote(
                "package.path='%s/usr/lib/lua/?.lua;%s/usr/lib/lua/?/init.lua;'..package.path; "
                "assert(loadfile('%s'))"
                % (probe_root, probe_root, controller_path)
            ),
            "lua -e %s"
            % shlex.quote(
                "package.path='%s/usr/lib/lua/?.lua;%s/usr/lib/lua/?/init.lua;'..package.path; "
                "assert(loadfile('%s'))"
                % (probe_root, probe_root, schema_path)
            ),
            "lua -e %s"
            % shlex.quote(
                "package.path='%s/usr/lib/lua/?.lua;%s/usr/lib/lua/?/init.lua;'..package.path; "
                "assert(loadfile('%s'))"
                % (probe_root, probe_root, model_path)
            ),
            "lua %s" % shlex.quote(posixpath.join(probe_root, "luci_friendly_probe.lua")),
            "sh -n %s" % shlex.quote(init_path),
            "cd %s && PYTHONPATH=%s python3 -B usr/lib/smart_srun/client.py --version"
            % (shlex.quote(probe_root), shlex.quote(python_path)),
        ],
        "cleanup": [
            "rm -rf %s /tmp/smart_srun_probe_log_*" % shlex.quote(probe_root),
        ],
    }


def require_router_password():
    if ROUTER_PASSWORD:
        return ROUTER_PASSWORD
    raise RuntimeError(
        "SMARTSRUN_ROUTER_PASSWORD is required; export it in the environment before running hot_update.py"
    )


def load_paramiko():
    try:
        import paramiko
    except ImportError as exc:
        raise RuntimeError(
            "paramiko is required for router hot update; install it with 'python -m pip install paramiko'"
        ) from exc
    return paramiko


def ensure_local_files():
    missing = []
    for item in UPLOAD_TARGETS:
        path = REPO_ROOT / item["local"]
        if not path.exists():
            missing.append(item["local"])
    if missing:
        raise RuntimeError("missing local upload targets: %s" % ", ".join(missing))


def print_block(title, text):
    print("== %s ==" % title)
    if text:
        print(text.rstrip())
    else:
        print("(no output)")


def run_remote(ssh, command, timeout=60):
    stdin, stdout, stderr = ssh.exec_command(command, timeout=timeout)
    del stdin
    output = stdout.read().decode("utf-8", errors="replace")
    error = stderr.read().decode("utf-8", errors="replace")
    exit_code = stdout.channel.recv_exit_status()
    return exit_code, output, error


def ensure_remote_parent_dirs(ssh, targets=None):
    targets = targets or remote_target_paths()
    parents = sorted({posixpath.dirname(item["remote"]) for item in targets})
    command = "mkdir -p %s" % " ".join(shlex.quote(path) for path in parents)
    code, output, error = run_remote(ssh, command)
    if code != 0:
        raise RuntimeError(
            "failed to prepare remote directories: %s%s" % (output, error)
        )


def upload_files(sftp, targets=None):
    targets = targets or remote_target_paths()
    for item in targets:
        local_path = REPO_ROOT / item["local"]
        remote_path = item["remote"]
        print("UPLOAD %s -> %s" % (item["local"], remote_path))
        original_remote = item.get("original_remote", remote_path)
        if original_remote in FORCE_LF_TARGETS:
            payload = local_path.read_bytes().replace(b"\r\n", b"\n")
            with sftp.file(remote_path, "wb") as remote_file:
                remote_file.write(payload)
            continue
        sftp.put(str(local_path), remote_path)


def restore_executable_permissions(ssh, remote_root=None):
    paths = [remote_path_for(path, remote_root) for path in EXECUTABLE_TARGETS]
    command = "chmod 755 %s" % " ".join(
        shlex.quote(path) for path in paths
    )
    code, output, error = run_remote(ssh, command)
    if code != 0:
        raise RuntimeError(
            "failed to restore executable permissions: %s"
            % (output or error or "no output")
        )


def upload_probe_helpers(sftp, probe_root):
    with sftp.file(posixpath.join(probe_root, "logger_probe.py"), "wb") as remote_file:
        remote_file.write(LOGGER_PROBE_CODE.encode("utf-8"))
    with sftp.file(
        posixpath.join(probe_root, "luci_friendly_probe.lua"), "wb"
    ) as remote_file:
        remote_file.write(build_luci_friendly_probe_code(probe_root).encode("utf-8"))


def run_command_group(ssh, name, commands, timeout=60):
    results = []
    for command in commands:
        print("RUN %s: %s" % (name, command))
        code, output, error = run_remote(ssh, command, timeout=timeout)
        print_block("%s exit=%s" % (command, code), output or error)
        if code != 0:
            if error and output:
                raise RuntimeError(
                    "remote command failed: %s\n%s\n%s" % (command, output, error)
                )
            raise RuntimeError(
                "remote command failed: %s\n%s"
                % (command, output or error or "no output")
            )
        results.append({"command": command, "stdout": output, "stderr": error})
    return results


def connect_ssh(paramiko, password):
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    print("Connecting to %s as %s" % (ROUTER_HOST, ROUTER_USER))
    ssh.connect(
        ROUTER_HOST,
        username=ROUTER_USER,
        password=password,
        look_for_keys=False,
        allow_agent=False,
        timeout=10,
    )
    return ssh


def run_probe(ssh, sftp):
    probe_root = "%s_%d" % (PROBE_ROOT_PREFIX, int(time.time()))
    targets = remote_target_paths(probe_root)
    commands = build_probe_commands(probe_root)
    try:
        ensure_remote_parent_dirs(ssh, targets)
        code, output, error = run_remote(ssh, "mkdir -p %s" % shlex.quote(probe_root))
        if code != 0:
            raise RuntimeError(
                "failed to prepare probe root: %s"
                % (output or error or "no output")
            )
        upload_files(sftp, targets)
        upload_probe_helpers(sftp, probe_root)
        restore_executable_permissions(ssh, remote_root=probe_root)
        run_command_group(ssh, "probe", commands["probe_checks"], timeout=90)
        print_block("probe root", probe_root)
        print("PROBE OK")
        return 0
    finally:
        for command in commands["cleanup"]:
            code, output, error = run_remote(ssh, command, timeout=30)
            if code != 0:
                print_block("probe cleanup failed", output or error or "no output")


def print_upload_plan(targets):
    print("== upload targets ==")
    for item in targets:
        print("%s -> %s" % (item["local"], item["remote"]))


def print_command_plan(commands):
    print("== remote commands ==")
    for name, command_list in commands.items():
        print("[%s]" % name)
        for command in command_list:
            print("  %s" % command)


def run_dry_run(probe=False):
    if probe:
        probe_root = "%s_DRY_RUN" % PROBE_ROOT_PREFIX
        print("DRY RUN: probe upload and smoke checks")
        print_block("probe root", probe_root)
        print_upload_plan(remote_target_paths(probe_root))
        print("helper -> %s" % posixpath.join(probe_root, "logger_probe.py"))
        print("helper -> %s" % posixpath.join(probe_root, "luci_friendly_probe.lua"))
        print_command_plan(build_probe_commands(probe_root))
        return 0

    print("DRY RUN: production hot update")
    print_upload_plan(remote_target_paths())
    print_command_plan(build_remote_commands())
    print("== luci verification ==")
    print("Fetch %s/admin/services/smart_srun after restart" % LUCI_BASE_URL)
    return 0


def build_arg_parser():
    parser = argparse.ArgumentParser(
        description="Upload SMART SRun files to an OpenWrt router and run smoke checks."
    )
    parser.add_argument(
        "--probe",
        action="store_true",
        help="upload to /tmp and run smoke checks without overwriting production files or restarting services",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="print planned uploads and remote commands without connecting to the router",
    )
    return parser


def build_luci_opener():
    jar = cookiejar.CookieJar()
    return request.build_opener(request.HTTPCookieProcessor(jar))


def open_url(opener, url, data=None, timeout=10, allow_statuses=()):
    try:
        response = opener.open(url, data=data, timeout=timeout)
        body = response.read().decode("utf-8", errors="replace")
        return response.status, body, response.geturl()
    except error.HTTPError as exc:
        try:
            body = exc.read().decode("utf-8", errors="replace")
            if exc.code in allow_statuses:
                return exc.code, body, exc.geturl()
            raise
        finally:
            exc.close()


def login_luci(opener):
    login_url = LUCI_BASE_URL + "/"
    open_url(opener, login_url, timeout=10, allow_statuses=(403,))
    password = require_router_password()
    payload = parse.urlencode(
        {
            "luci_username": ROUTER_USER,
            "luci_password": password,
        }
    ).encode("utf-8")
    _, body, _ = open_url(opener, login_url, data=payload, timeout=10)
    return body


def fetch_luci_page(opener):
    url = LUCI_BASE_URL + "/admin/services/smart_srun"
    _, body, _ = open_url(opener, url, timeout=15)
    return body


def fetch_luci_asset(opener, asset_path):
    base = parse.urlsplit(LUCI_BASE_URL)
    asset_url = parse.urlunsplit((base.scheme, base.netloc, asset_path, "", ""))
    _, body, _ = open_url(opener, asset_url, timeout=15)
    return body


def verify_luci_page(expected_descriptor_count):
    opener = build_luci_opener()
    login_body = login_luci(opener)
    if "luci_password" in login_body and "密码" in login_body:
        raise RuntimeError("LuCI login failed")

    page = fetch_luci_page(opener)
    required = [("school selector", "cbid.smart_srun.main.school")]
    if expected_descriptor_count > 0:
        required.append(("school_extra fields", "_school_extra_"))

    missing = [label for label, needle in required if needle not in page]
    if missing:
        raise RuntimeError(
            "LuCI page missing expected markers: %s" % ", ".join(missing)
        )
    forbidden = [
        ("runtime diagnostics block", "学校运行时诊断"),
        ("runtime diagnostics marker", "runtime diagnostics"),
    ]
    present = [label for label, needle in forbidden if needle in page]
    if present:
        raise RuntimeError(
            "LuCI page still contains removed markers: %s" % ", ".join(present)
        )

    match = re.search(r'(/luci-static/resources/smart_srun\.js(?:\?[^"\']*)?)', page)
    if not match:
        raise RuntimeError("LuCI page missing expected markers: smart_srun.js asset")

    asset_body = fetch_luci_asset(opener, match.group(1))
    if "smartOpenBlockingFeedback" not in asset_body:
        raise RuntimeError("LuCI static asset missing expected runtime hooks")
    return page


def parse_selected_runtime_metadata(inspect_output):
    payload = json.loads(inspect_output)
    descriptors = payload.get("field_descriptors")
    if not isinstance(descriptors, list):
        descriptors = payload.get("school_extra")
    if not isinstance(descriptors, list):
        descriptors = payload.get("school_extra_descriptors")
    if not isinstance(descriptors, list):
        descriptors = []
    return payload, descriptors


def run_hot_update(ssh, sftp):
    commands = build_remote_commands()

    ensure_remote_parent_dirs(ssh)
    upload_files(sftp)
    restore_executable_permissions(ssh)
    print("UPLOAD OK")

    run_command_group(ssh, "syntax", commands["syntax_checks"], timeout=90)
    run_command_group(ssh, "cleanup", commands["cache_cleanup"], timeout=30)
    run_command_group(ssh, "restart", commands["restart"], timeout=60)
    sanity_results = run_command_group(
        ssh, "sanity", commands["sanity_checks"], timeout=60
    )

    inspect_output = ""
    for item in sanity_results:
        if item["command"] == "srunnet schools inspect --selected":
            inspect_output = item["stdout"].strip()
            break
    if not inspect_output:
        raise RuntimeError("missing selected runtime inspection output")

    metadata, descriptors = parse_selected_runtime_metadata(inspect_output)
    time.sleep(2)
    page = verify_luci_page(len(descriptors))

    print_block(
        "selected runtime metadata",
        json.dumps(metadata, ensure_ascii=False, indent=2),
    )
    print_block(
        "luci verification",
        "OK: fetched LuCI page with expected school runtime markers",
    )
    print_block("luci page size", str(len(page)))
    print("HOT UPDATE OK")
    return 0


def main(argv=None):
    args = build_arg_parser().parse_args(argv)
    ensure_local_files()
    if args.dry_run:
        return run_dry_run(probe=args.probe)

    password = require_router_password()
    paramiko = load_paramiko()

    ssh = connect_ssh(paramiko, password)
    sftp = ssh.open_sftp()
    try:
        if args.probe:
            return run_probe(ssh, sftp)
        return run_hot_update(ssh, sftp)
    finally:
        sftp.close()
        ssh.close()


if __name__ == "__main__":
    raise SystemExit(main())
