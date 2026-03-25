#!/usr/bin/env python3

import json
import os
import posixpath
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
        "local": "root/usr/lib/smart_srun/config.py",
        "remote": "/usr/lib/smart_srun/config.py",
    },
    {
        "local": "root/usr/lib/smart_srun/crypto.py",
        "remote": "/usr/lib/smart_srun/crypto.py",
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
        "local": "root/usr/lib/lua/luci/model/cbi/smart_srun.lua",
        "remote": "/usr/lib/lua/luci/model/cbi/smart_srun.lua",
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


def build_remote_commands():
    python_files = [
        item["remote"] for item in UPLOAD_TARGETS if item["remote"].endswith(".py")
    ]
    return {
        "syntax_checks": [
            "python3 -m py_compile %s" % " ".join(python_files),
            "lua -e \"assert(loadfile('/usr/lib/lua/luci/controller/smart_srun.lua'))\"",
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
            "python3 -c \"import sys; sys.path.insert(0, '/usr/lib/smart_srun'); import school_runtime; import schools; import srun_auth; import orchestrator; import snapshot; import daemon; print('runtime-loader-import-ok')\"",
            "srunnet status",
            "srunnet schools",
            "srunnet schools inspect --selected",
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


def ensure_remote_parent_dirs(ssh):
    parents = sorted({posixpath.dirname(item["remote"]) for item in UPLOAD_TARGETS})
    command = "mkdir -p %s" % " ".join(shlex.quote(path) for path in parents)
    code, output, error = run_remote(ssh, command)
    if code != 0:
        raise RuntimeError(
            "failed to prepare remote directories: %s%s" % (output, error)
        )


def upload_files(sftp):
    for item in UPLOAD_TARGETS:
        local_path = REPO_ROOT / item["local"]
        remote_path = item["remote"]
        print("UPLOAD %s -> %s" % (item["local"], remote_path))
        if remote_path in FORCE_LF_TARGETS:
            payload = local_path.read_bytes().replace(b"\r\n", b"\n")
            with sftp.file(remote_path, "wb") as remote_file:
                remote_file.write(payload)
            continue
        sftp.put(str(local_path), remote_path)


def restore_executable_permissions(ssh):
    command = "chmod 755 %s" % " ".join(
        shlex.quote(path) for path in EXECUTABLE_TARGETS
    )
    code, output, error = run_remote(ssh, command)
    if code != 0:
        raise RuntimeError(
            "failed to restore executable permissions: %s"
            % (output or error or "no output")
        )


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


def main():
    ensure_local_files()
    password = require_router_password()
    paramiko = load_paramiko()
    commands = build_remote_commands()

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

    sftp = ssh.open_sftp()
    try:
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
    finally:
        sftp.close()
        ssh.close()


if __name__ == "__main__":
    raise SystemExit(main())
