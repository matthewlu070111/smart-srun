"""Package update helper for SMART SRun."""

import errno
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time
import zipfile

try:
    from urllib import request as urlrequest
except ImportError:  # OpenWrt python3-light may omit urllib.
    urlrequest = None

import version_info

try:
    from config import log
except ImportError:  # pragma: no cover - updater can still run in probe mode
    def log(level, event, message="", **fields):
        del level, event, message, fields


OWNER = "matthewlu070111"
REPO = "smart-srun"
LATEST_RELEASE_API = "https://api.github.com/repos/%s/%s/releases/latest" % (
    OWNER,
    REPO,
)
RELEASES_PAGE_URL = "https://github.com/%s/%s/releases" % (OWNER, REPO)
DOWNLOADS_BRANCH_URL = "https://raw.githubusercontent.com/%s/%s/downloads" % (
    OWNER,
    REPO,
)
STATUS_FILE = "/var/run/smart_srun/update_status.json"
LOG_FILE = "/var/log/smart_srun_update.log"
LOCK_FILE = "/var/run/smart_srun/update.lock"
WORK_DIR = "/tmp/smart_srun_update"
MODULE_DIR = os.path.dirname(os.path.abspath(__file__))
CLIENT_PATH = os.path.join(MODULE_DIR, "client.py")


def _ensure_parent(path):
    parent = os.path.dirname(path)
    if parent and not os.path.exists(parent):
        os.makedirs(parent)


def _write_json(path, payload):
    _ensure_parent(path)
    tmp_path = path + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")
    os.replace(tmp_path, path)


def _read_json(path):
    try:
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
        return data if isinstance(data, dict) else {}
    except (OSError, TypeError, ValueError):
        return {}


def _append_log(message):
    _ensure_parent(LOG_FILE)
    line = "%s %s\n" % (time.strftime("%Y-%m-%d %H:%M:%S"), str(message))
    with open(LOG_FILE, "a", encoding="utf-8") as handle:
        handle.write(line)


def _set_status(phase, message, **fields):
    payload = get_status()
    payload.update(fields)
    payload["phase"] = phase
    payload["message"] = str(message or "")
    payload["updated_at"] = int(time.time())
    _write_json(STATUS_FILE, payload)
    _append_log("%s: %s" % (phase, message))
    log("INFO", "update_status", str(message or ""), phase=phase)
    return payload


def _status_fields(payload):
    return {
        key: value
        for key, value in dict(payload or {}).items()
        if key not in ("ok", "running", "phase", "message")
    }


def get_status():
    payload = _read_json(STATUS_FILE)
    if not payload:
        payload = {
            "ok": True,
            "running": False,
            "phase": "idle",
            "message": "未开始更新",
        }
    return payload


def package_manager():
    if shutil.which("apk") or os.path.exists("/sbin/apk"):
        return "apk"
    return "opkg"


def package_format():
    return "apk" if package_manager() == "apk" else "ipk"


def install_mode(package_name=None):
    name = package_name or version_info.detect_installed_package_name()
    if name == "luci-app-smart-srun-bundle":
        return "bundle"
    if name == "luci-app-smart-srun":
        return "split"
    return "core"


def _release_version(tag_name):
    text = str(tag_name or "").strip()
    if text.startswith("v"):
        text = text[1:]
    return text


def _version_tuple(value):
    text = _release_version(str(value or "").strip())
    text = text.split("-r", 1)[0]
    nums = []
    for part in re.split(r"[^0-9]+", text):
        if part != "":
            nums.append(int(part))
    return tuple(nums or [0])


def _version_sort_key(value):
    """把版本串解析为可比较的 key，正确处理预发布语义。

    预发布（-bN / -betaN / -rcN / _betaN 等，含字母标记）排在同号正式版之前，
    因此同一主版本下 “无预发布 > 有预发布”。修复 beta 用户永远收不到同号
    正式版更新的问题（旧实现把 -b2 的 2 当成第 4 段版本号，反而判成更高）。
    """
    text = _release_version(str(value or "").strip())
    text = text.split("-r", 1)[0]
    match = re.match(r"^(\d+(?:\.\d+)*)(.*)$", text)
    if not match:
        return ((0,), 1, ())
    release = tuple(int(p) for p in match.group(1).split("."))
    rest = match.group(2)
    is_prerelease = bool(re.search(r"[A-Za-z]", rest))
    if is_prerelease:
        pre_nums = tuple(int(n) for n in re.findall(r"\d+", rest)) or (0,)
        return (release, 0, pre_nums)
    return (release, 1, ())


def is_remote_newer(current_version, latest_tag):
    return _version_sort_key(latest_tag) > _version_sort_key(current_version)


def _fetch_json(url, timeout=12):
    text = _fetch_text(url, timeout=timeout, accept="application/vnd.github+json")
    return json.loads(text)


def _fetch_text(url, timeout=12, accept="*/*"):
    if urlrequest is not None:
        req = urlrequest.Request(
            url,
            headers={
                "Accept": accept,
                "User-Agent": "smart-srun-updater/1",
            },
        )
        with urlrequest.urlopen(req, timeout=timeout) as response:
            return response.read().decode("utf-8", "replace")
    return _fetch_via_system_client(url, timeout).decode("utf-8", "replace")


def _fetch_via_system_client(url, timeout=30):
    last_error = None
    for command in ("uclient-fetch", "wget"):
        binary = shutil.which(command)
        if not binary:
            continue
        args = [binary, "-q", "-O", "-", url]
        try:
            return subprocess.check_output(
                args, stderr=subprocess.STDOUT, timeout=timeout
            )
        except (OSError, subprocess.SubprocessError) as exc:
            last_error = exc
    raise RuntimeError("no usable HTTP client: %s" % last_error)


def _fetch_binary(url, timeout=30):
    if urlrequest is not None:
        req = urlrequest.Request(url, headers={"User-Agent": "smart-srun-updater/1"})
        with urlrequest.urlopen(req, timeout=timeout) as response:
            return response.read()
    return _fetch_via_system_client(url, timeout)


def fetch_latest_release(timeout=12):
    data = _fetch_json(LATEST_RELEASE_API, timeout=timeout)
    if not isinstance(data, dict) or not data.get("tag_name"):
        raise RuntimeError("invalid GitHub latest release response")
    return data


def _asset_candidates(release, suffix):
    assets = release.get("assets")
    if not isinstance(assets, list):
        return []
    out = []
    for asset in assets:
        if not isinstance(asset, dict):
            continue
        name = str(asset.get("name") or "")
        url = str(asset.get("browser_download_url") or "")
        if name.endswith(suffix) and url:
            out.append(asset)
    return out


def _select_bundle_asset(release, fmt):
    candidates = _asset_candidates(release, "." + fmt)
    if fmt == "apk":
        prefix = "luci-app-smart-srun-bundle-"
    else:
        prefix = "luci-app-smart-srun-bundle_"
    for asset in candidates:
        if str(asset.get("name") or "").startswith(prefix):
            return asset
    return None


def _split_zip_names(version, fmt):
    if fmt == "apk":
        return [
            "smart-srun-split-packages-%s-apk.zip" % version,
            "smart-srun-split-packages-%s.zip" % version,
        ]
    return ["smart-srun-split-packages-%s.zip" % version]


def _split_zip_urls(version, fmt):
    return [
        "%s/%s/%s" % (DOWNLOADS_BRANCH_URL, version, name)
        for name in _split_zip_names(version, fmt)
    ]


def build_update_plan(release=None):
    release = release or fetch_latest_release()
    package_name = version_info.detect_installed_package_name()
    current_version = version_info.get_display_version(package_name=package_name)
    fmt = package_format()
    mode = install_mode(package_name)
    latest_tag = str(release.get("tag_name") or "")
    latest_version = _release_version(latest_tag)
    plan = {
        "package_manager": package_manager(),
        "package_format": fmt,
        "package_name": package_name,
        "install_mode": mode,
        "current_version": current_version,
        "latest_tag": latest_tag,
        "latest_version": latest_version,
        "update_available": is_remote_newer(current_version, latest_tag),
        "release_page": RELEASES_PAGE_URL,
    }
    if mode == "bundle":
        asset = _select_bundle_asset(release, fmt)
        if asset:
            plan.update(
                {
                    "download_kind": "release_asset",
                    "asset_name": str(asset.get("name") or ""),
                    "download_url": str(asset.get("browser_download_url") or ""),
                    "asset_digest": str(asset.get("digest") or ""),
                }
            )
        else:
            plan["error"] = "latest release has no matching bundle %s asset" % fmt
    else:
        plan.update(
            {
                "download_kind": "split_zip",
                "asset_name": _split_zip_names(latest_version, fmt)[0],
                "download_urls": _split_zip_urls(latest_version, fmt),
            }
        )
    return plan


def check_update():
    try:
        plan = build_update_plan()
    except Exception as exc:
        return {
            "ok": False,
            "running": False,
            "phase": "check_failed",
            "message": "检查更新失败: %s" % exc,
        }
    if plan.get("error"):
        return dict(
            plan,
            ok=False,
            running=False,
            phase="missing_asset",
            message=plan["error"],
        )
    message = (
        "发现新版本 %s" % plan["latest_tag"]
        if plan.get("update_available")
        else "当前已是最新版本"
    )
    return dict(plan, ok=True, running=False, phase="checked", message=message)


def _download_url(url, target, timeout=30):
    data = _fetch_binary(url, timeout=timeout)
    with open(target, "wb") as handle:
        handle.write(data)
    if os.path.getsize(target) <= 0:
        raise RuntimeError("downloaded file is empty")


def _download_first(urls, target):
    last_error = None
    for url in urls:
        try:
            _download_url(url, target)
            return url
        except Exception as exc:
            last_error = exc
    raise RuntimeError("download failed: %s" % last_error)


def _verify_digest(path, digest):
    text = str(digest or "").strip()
    if not text:
        return True
    if text.startswith("sha256:"):
        expected = text.split(":", 1)[1].strip().lower()
        sha = hashlib.sha256()
        with open(path, "rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 128), b""):
                sha.update(chunk)
        if sha.hexdigest().lower() != expected:
            raise RuntimeError("sha256 digest mismatch")
    return True


def _parse_sha256(text):
    for token in str(text or "").split():
        token = token.strip().lower()
        if re.match(r"^[0-9a-f]{64}$", token):
            return token
    return ""


def _verify_split_zip(path, source_url):
    """校验 split zip 的 sha256，旁注来自 downloads 分支同目录的 <zip>.sha256。"""
    if not source_url:
        return
    try:
        text = _fetch_text(source_url + ".sha256", timeout=12)
    except Exception:
        # 旧版本的 downloads 目录没有 .sha256 旁注，保持向后兼容直接跳过。
        _append_log("split zip sha256 sidecar missing, skipping verification")
        return
    expected = _parse_sha256(text)
    if not expected:
        _append_log("split zip sha256 sidecar unreadable, skipping verification")
        return
    _verify_digest(path, "sha256:" + expected)


def _is_safe_zip_member(name):
    text = str(name or "")
    if not text or text.startswith("/") or text.startswith("\\"):
        return False
    parts = re.split(r"[\\/]+", text)
    return all(part not in ("", ".", "..") for part in parts)


def _extract_split_zip(zip_path, extract_dir, fmt, mode):
    with zipfile.ZipFile(zip_path, "r") as archive:
        for member in archive.namelist():
            if not _is_safe_zip_member(member):
                raise RuntimeError("unsafe split zip member: %s" % member)
        archive.extractall(extract_dir)

    package_paths = []
    for root, _, files in os.walk(extract_dir):
        for name in files:
            if not name.endswith("." + fmt):
                continue
            if fmt == "apk":
                is_core = name.startswith("smart-srun-")
                is_luci = name.startswith("luci-app-smart-srun-") and not name.startswith(
                    "luci-app-smart-srun-bundle-"
                )
            else:
                is_core = name.startswith("smart-srun_")
                is_luci = name.startswith("luci-app-smart-srun_")
            if mode == "core" and is_core:
                package_paths.append(os.path.join(root, name))
            elif mode == "split" and (is_core or is_luci):
                package_paths.append(os.path.join(root, name))

    package_paths = sorted(package_paths)
    if mode == "split" and len(package_paths) < 2:
        raise RuntimeError("split zip missing smart-srun/luci-app-smart-srun packages")
    if mode == "core" and not package_paths:
        raise RuntimeError("split zip missing smart-srun package")
    return package_paths


def _run_command(args, timeout=120):
    proc = subprocess.Popen(
        args,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        universal_newlines=True,
    )
    try:
        out, _ = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        out, _ = proc.communicate()
        raise RuntimeError("command timed out: %s" % " ".join(args))
    if out:
        for line in out.splitlines():
            _append_log("  " + line)
    if proc.returncode != 0:
        raise RuntimeError("command failed (%d): %s" % (proc.returncode, " ".join(args)))
    return out


def _preinstall_command(paths, manager):
    if manager == "apk":
        return [
            "apk",
            "add",
            "-s",
            "-q",
            "--force-overwrite",
            "--clean-protected",
            "--allow-untrusted",
        ] + paths
    return ["opkg", "install", "--noaction"] + paths


def _install_command(paths, manager):
    if manager == "apk":
        return [
            "apk",
            "add",
            "-q",
            "--force-overwrite",
            "--clean-protected",
            "--allow-untrusted",
        ] + paths
    return ["opkg", "install"] + paths


def _restart_services():
    for command in (
        ["/etc/init.d/smart_srun", "restart"],
        ["/etc/init.d/uwsgi", "restart"],
    ):
        if os.path.exists(command[0]):
            try:
                _run_command(command, timeout=30)
            except Exception as exc:
                _append_log("restart skipped: %s" % exc)


def _pid_alive(pid):
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except OSError as exc:
        # EPERM 说明进程存在但无权发信号，仍视为存活。
        return exc.errno == errno.EPERM
    return True


def _read_lock_pid():
    try:
        with open(LOCK_FILE, "r", encoding="ascii", errors="ignore") as handle:
            return int((handle.read() or "0").strip() or 0)
    except (OSError, ValueError):
        return 0


def _write_lock_pid(pid):
    _ensure_parent(LOCK_FILE)
    with open(LOCK_FILE, "w", encoding="ascii") as handle:
        handle.write(str(int(pid)))


def _acquire_lock():
    _ensure_parent(LOCK_FILE)
    try:
        fd = os.open(LOCK_FILE, os.O_CREAT | os.O_EXCL | os.O_WRONLY)
    except OSError:
        # 锁文件已存在：若持有者进程已退出（崩溃 / 被服务重启杀掉）则视为陈旧锁，
        # 清理后重试，避免一次失败的更新把后续更新永久挡在门外。
        holder = _read_lock_pid()
        if holder and _pid_alive(holder):
            raise RuntimeError("已有更新任务正在运行")
        _append_log("clearing stale update lock (pid=%s)" % (holder or "?"))
        try:
            os.remove(LOCK_FILE)
            fd = os.open(LOCK_FILE, os.O_CREAT | os.O_EXCL | os.O_WRONLY)
        except OSError:
            raise RuntimeError("已有更新任务正在运行")
    os.write(fd, str(os.getpid()).encode("ascii", "ignore"))
    os.close(fd)


def _release_lock():
    try:
        os.remove(LOCK_FILE)
    except OSError:
        pass


# Standalone worker: must NOT run as client.py. Package install can restart
# smart_srun/uwsgi; init.d used to kill every client.py process (including the
# updater). This script lives under /tmp, uses only stdlib, and finalizes status.
_FINISH_UPDATE_SCRIPT = r"""#!/usr/bin/env python3
# smart-srun update finish worker (generated; do not edit)
import json
import os
import subprocess
import sys
import time


def _append_log(log_file, message):
    parent = os.path.dirname(log_file)
    if parent and not os.path.exists(parent):
        os.makedirs(parent)
    line = "%s %s\n" % (time.strftime("%Y-%m-%d %H:%M:%S"), str(message))
    with open(log_file, "a", encoding="utf-8") as handle:
        handle.write(line)


def _write_json(path, payload):
    parent = os.path.dirname(path)
    if parent and not os.path.exists(parent):
        os.makedirs(parent)
    tmp_path = path + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")
    os.replace(tmp_path, path)


def _read_json(path):
    try:
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
        return data if isinstance(data, dict) else {}
    except (OSError, TypeError, ValueError):
        return {}


def _set_status(status_file, log_file, phase, message, **fields):
    payload = _read_json(status_file)
    payload.update(fields)
    payload["phase"] = phase
    payload["message"] = str(message or "")
    payload["updated_at"] = int(time.time())
    _write_json(status_file, payload)
    _append_log(log_file, "%s: %s" % (phase, message))
    return payload


def _run_command(args, log_file, timeout=240):
    proc = subprocess.Popen(
        args,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        universal_newlines=True,
    )
    try:
        out, _ = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        out, _ = proc.communicate()
        raise RuntimeError("command timed out: %s" % " ".join(args))
    if out:
        for line in out.splitlines():
            _append_log(log_file, "  " + line)
    if proc.returncode != 0:
        raise RuntimeError(
            "command failed (%d): %s" % (proc.returncode, " ".join(args))
        )
    return out


def _install_command(paths, manager):
    if manager == "apk":
        return [
            "apk",
            "add",
            "-q",
            "--force-overwrite",
            "--clean-protected",
            "--allow-untrusted",
        ] + list(paths)
    return ["opkg", "install"] + list(paths)


def _restart_services(log_file):
    for command in (
        ["/etc/init.d/smart_srun", "restart"],
        ["/etc/init.d/uwsgi", "restart"],
    ):
        if not os.path.exists(command[0]):
            continue
        try:
            _run_command(command, log_file, timeout=30)
        except Exception as exc:
            _append_log(log_file, "restart skipped: %s" % exc)


def main(argv):
    if len(argv) != 2:
        raise SystemExit("usage: finish_update.py <plan.json>")
    plan_path = argv[1]
    with open(plan_path, "r", encoding="utf-8") as handle:
        plan = json.load(handle)

    status_file = str(plan["status_file"])
    log_file = str(plan["log_file"])
    lock_file = str(plan["lock_file"])
    manager = str(plan["manager"])
    package_paths = list(plan.get("package_paths") or [])
    fields = dict(plan.get("status_fields") or {})

    # Claim lock ownership for this worker.
    parent = os.path.dirname(lock_file)
    if parent and not os.path.exists(parent):
        os.makedirs(parent)
    with open(lock_file, "w", encoding="ascii") as handle:
        handle.write(str(os.getpid()))

    try:
        _set_status(
            status_file,
            log_file,
            "installing",
            "正在安装更新包",
            ok=True,
            running=True,
            **fields
        )
        _run_command(_install_command(package_paths, manager), log_file, timeout=240)

        _set_status(
            status_file,
            log_file,
            "restarting",
            "正在重启服务",
            ok=True,
            running=True,
            **fields
        )
        _restart_services(log_file)

        _set_status(
            status_file,
            log_file,
            "complete",
            "更新完成",
            ok=True,
            running=False,
            **fields
        )
        return 0
    except Exception as exc:
        _set_status(
            status_file,
            log_file,
            "failed",
            "更新失败: %s" % exc,
            ok=False,
            running=False,
            **fields
        )
        return 1
    finally:
        try:
            os.remove(lock_file)
        except OSError:
            pass


if __name__ == "__main__":
    sys.exit(main(sys.argv))
"""


def _finish_script_path():
    return os.path.join(WORK_DIR, "finish_update.py")


def _finish_plan_path():
    return os.path.join(WORK_DIR, "finish_plan.json")


def _write_finish_worker(package_paths, manager, status_fields):
    """Write /tmp finish worker + plan. Worker is not client.py (survives restarts)."""
    plan = {
        "package_paths": list(package_paths),
        "manager": str(manager),
        "status_fields": dict(status_fields or {}),
        "status_file": STATUS_FILE,
        "log_file": LOG_FILE,
        "lock_file": LOCK_FILE,
    }
    _write_json(_finish_plan_path(), plan)
    script_path = _finish_script_path()
    with open(script_path, "w", encoding="utf-8") as handle:
        handle.write(_FINISH_UPDATE_SCRIPT)
    try:
        os.chmod(script_path, 0o755)
    except OSError:
        pass
    return script_path, _finish_plan_path()


def _spawn_finish_worker(script_path, plan_path):
    """Detach install/restart/finalize so package self-replace + service restart
    cannot kill the process that writes the terminal update status.

    Applies to both opkg and apk: both replace shipped files under
    /usr/lib/smart_srun and both are followed by smart_srun/uwsgi restart.
    """
    _ensure_parent(LOG_FILE)
    log_handle = open(LOG_FILE, "a", encoding="utf-8")
    try:
        proc = subprocess.Popen(
            [sys.executable or "python3", "-B", script_path, plan_path],
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            close_fds=True,
            start_new_session=True,
            cwd=WORK_DIR,
        )
    finally:
        log_handle.close()
    # Transfer lock ownership before parent returns/exits.
    _write_lock_pid(proc.pid)
    _append_log("handed off install/restart to finish worker pid=%s" % proc.pid)
    return proc


def run_update():
    _acquire_lock()
    handed_off = False
    try:
        if os.path.exists(WORK_DIR):
            shutil.rmtree(WORK_DIR)
        os.makedirs(WORK_DIR)
        _set_status("checking", "正在检查最新版本", ok=True, running=True)
        result = check_update()
        if not result.get("ok"):
            _set_status(
                result.get("phase") or "check_failed",
                result.get("message") or "检查更新失败",
                ok=False,
                running=False,
                **_status_fields(result)
            )
            return get_status()
        if not result.get("update_available"):
            _set_status(
                "complete",
                "当前已是最新版本",
                ok=True,
                running=False,
                **_status_fields(result)
            )
            return get_status()

        manager = result["package_manager"]
        fmt = result["package_format"]
        mode = result["install_mode"]
        _set_status(
            "downloading",
            "正在下载更新包",
            ok=True,
            running=True,
            **_status_fields(result)
        )

        if result.get("download_kind") == "release_asset":
            package_path = os.path.join(WORK_DIR, result["asset_name"])
            _download_url(result["download_url"], package_path)
            _verify_digest(package_path, result.get("asset_digest"))
            package_paths = [package_path]
            downloaded_from = result["download_url"]
        else:
            zip_path = os.path.join(WORK_DIR, result["asset_name"])
            downloaded_from = _download_first(result.get("download_urls") or [], zip_path)
            _verify_split_zip(zip_path, downloaded_from)
            extract_dir = os.path.join(WORK_DIR, "packages")
            os.makedirs(extract_dir)
            package_paths = _extract_split_zip(zip_path, extract_dir, fmt, mode)

        status_fields = dict(
            _status_fields(result),
            downloaded_from=downloaded_from,
            package_paths=package_paths,
        )
        _set_status(
            "preinstall",
            "正在执行预安装测试",
            ok=True,
            running=True,
            **status_fields
        )
        _run_command(_preinstall_command(package_paths, manager), timeout=180)

        # Hand off install + service restart + final status to a /tmp worker.
        # In-process install used to die when smart_srun restart killed client.py
        # (and package file replace is hostile to a live interpreter under
        # /usr/lib/smart_srun). Same risk for apk and opkg.
        script_path, plan_path = _write_finish_worker(
            package_paths, manager, status_fields
        )
        _set_status(
            "installing",
            "正在安装更新包",
            ok=True,
            running=True,
            **status_fields
        )
        _spawn_finish_worker(script_path, plan_path)
        handed_off = True
        return get_status()
    except Exception as exc:
        _set_status("failed", "更新失败: %s" % exc, ok=False, running=False)
        return get_status()
    finally:
        if not handed_off:
            _release_lock()


def start_background_update():
    status = get_status()
    holder = _read_lock_pid()
    if status.get("running") and holder and _pid_alive(holder):
        return dict(status, ok=False, message="已有更新任务正在运行")
    if status.get("running"):
        # 状态残留 running 但持有进程已死（被 kill/OOM/服务重启杀掉）：视为陈旧，
        # 清理后继续；否则 LuCI「立即更新」会被永久挡住，只能重启路由器恢复。
        _append_log("clearing stale running status (holder pid=%s)" % (holder or "?"))
    _set_status("queued", "已提交后台更新任务", ok=True, running=True)
    cmd = [
        sys.executable or "python3",
        "-B",
        CLIENT_PATH,
        "update",
        "run",
        "--foreground",
    ]
    _ensure_parent(LOG_FILE)
    log_handle = open(LOG_FILE, "a", encoding="utf-8")
    # start_new_session 让更新进程脱离 uwsgi 的会话/进程组，否则更新末尾重启 uwsgi
    # 时会把正在收尾的更新进程一起杀掉，导致状态卡在 running 并残留锁文件。
    subprocess.Popen(
        cmd,
        stdout=log_handle,
        stderr=subprocess.STDOUT,
        close_fds=True,
        start_new_session=True,
    )
    log_handle.close()
    return get_status()

