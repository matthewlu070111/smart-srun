"""Temporary, rollback-capable Wi-Fi client setup for the LuCI wizard."""

import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import time

import wireless
import wireless_ap
from network import get_ipv4_from_network_interface


JOB_ROOT = "/var/run/smart_srun/wifi_setup"
CONFIG_ROOT = "/etc/config"
PACKAGES = ("wireless", "network", "firewall")
HOLD_SECONDS = 900
ENCRYPTIONS = ("none", "psk", "psk2", "psk-mixed", "sae", "sae-mixed")


def _path(job):
    if not re.fullmatch(r"[a-f0-9]{32}", str(job or "")):
        raise ValueError("无线连接任务编号无效")
    return os.path.join(JOB_ROOT, job)


def _write(path, data):
    temp = path + ".new"
    with open(temp, "w", encoding="utf-8") as handle:
        os.chmod(temp, 0o600)
        json.dump(data, handle, ensure_ascii=False)
    os.replace(temp, path)


def _read(path):
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def status(job):
    try:
        result = _read(os.path.join(_path(job), "status.json"))
        if result.get("state") == "connected" and time.time() > result.get("expires_at", 0):
            try:
                os.unlink(os.path.join(_path(job), "account.json"))
            except FileNotFoundError:
                pass
            result.update(ok=False, state="failed", message="连接检查已过期，请重新检测；原有网络保持不变")
        return result
    except FileNotFoundError:
        return {"ok": False, "state": "missing", "message": "尚未找到连接任务，请稍后重试"}


def start(payload):
    job = payload.get("job", "")
    path = _path(job)
    os.makedirs(JOB_ROOT, mode=0o700, exist_ok=True)
    try:
        os.mkdir(path, 0o700)
    except FileExistsError:
        return status(job)
    _write(os.path.join(path, "input.json"), payload)
    result = {"ok": True, "job": job, "state": "starting", "message": "正在查找校园网 Wi-Fi…"}
    _write(os.path.join(path, "status.json"), result)
    try:
        subprocess.Popen([sys.executable, os.path.abspath(__file__), job],
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL, start_new_session=True, close_fds=True)
    except OSError:
        os.unlink(os.path.join(path, "input.json"))
        result.update(ok=False, state="failed", message="无法启动无线连接任务")
        _write(os.path.join(path, "status.json"), result)
    return result


def control(job, command):
    if command not in ("cancel", "commit"):
        raise ValueError("无线连接操作无效")
    current = status(job)
    if current.get("state") == "connected":
        try:
            os.unlink(os.path.join(_path(job), "account.json"))
        except FileNotFoundError:
            pass
        return {"ok": True, "state": "done", "message": "原有连接保持不变"}
    if command == "commit" and current.get("state") != "ready":
        raise ValueError("无线连接尚未就绪或已超时，请返回第一步重新连接")
    _write(os.path.join(_path(job), "command.json"), {"command": command})
    return {"ok": True, "state": "finishing", "message": "正在保留连接" if command == "commit" else "正在恢复连接"}


def account_fields(job, ssid):
    current = status(job)
    if current.get("state") not in ("ready", "connected") or current.get("ssid") != ssid:
        raise ValueError("无线连接与账号不一致或连接已超时，请重新连接")
    # This private file is read only by the authenticated save endpoint.
    return _read(os.path.join(_path(job), "account.json"))


def _command(args, input_text=None, timeout=30, check=True):
    result = subprocess.run(args, input=input_text, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, timeout=timeout)
    if check and result.returncode:
        # UCI stderr may echo a password; never return command output on errors.
        raise RuntimeError("系统网络配置操作失败，请检查路由器网络服务")
    return result.stdout if result.returncode == 0 else ""


def _uci(directory, *args, input_text=None):
    return _command(["uci", "-q", "-c", directory, "-t", directory + "/delta"] + list(args), input_text)


def _sections(directory, package):
    result = {}
    for line in _uci(directory, "show", package).splitlines():
        key, _, raw = line.partition("=")
        parts = key.split(".")
        values = shlex.split(raw)
        if len(parts) == 2:
            result.setdefault(parts[1], {})[".type"] = values[0] if values else ""
        elif len(parts) == 3:
            result.setdefault(parts[1], {})[parts[2]] = values
    return result


def _quote(value):
    value = str(value)
    if any(ord(ch) < 32 for ch in value):
        raise ValueError("Wi-Fi 名称和密码不能含换行或控制字符")
    return "'" + value.replace("'", "'\\''") + "'"


def _encryption(entry, requested="auto"):
    observed = entry.get("encryption") or {}
    if requested != "auto":
        return requested if wireless_ap._security_matches(requested, observed) else ""
    for mode in ("none", "psk2", "sae", "psk"):
        if wireless_ap._security_matches(mode, observed):
            return mode
    return ""


def _connected(section, iface, ssid):
    measured = wireless_ap.read_association(section)
    if measured.get("ssid") == ssid and wireless_ap.valid_bssid(measured.get("bssid")):
        return get_ipv4_from_network_interface(iface) or ""
    return ""


def _plan(payload):
    ssid = str(payload.get("ssid") or "")
    key = str(payload.get("key") or "")
    iface = str(payload.get("iface") or "")
    radio = str(payload.get("radio") or "")
    requested = str(payload.get("encryption") or "auto").strip().lower()
    if requested != "auto" and requested not in ENCRYPTIONS:
        raise ValueError("不支持所选加密方式，请选择开放网络、WPA-PSK、WPA2-PSK 或 WPA3-SAE")
    if requested == "none":
        key = ""
    if not ssid or len(ssid.encode("utf-8")) > 32:
        raise ValueError("请填写有效的校园网 Wi-Fi 名称（最多 32 字节）")
    _quote(ssid)
    _quote(key)
    for name in (iface, radio):
        if name and not re.fullmatch(r"[A-Za-z0-9_]+", name):
            raise ValueError("接口或无线电名称无效")
    data = wireless.parse_wireless_iface_data()
    candidates = []
    for section, opts in data.items():
        if opts.get("mode") != "sta" or (radio and opts.get("device") != radio):
            continue
        networks = str(opts.get("network") or "").split()
        if len(networks) != 1 or (iface and iface != networks[0]):
            continue
        current_encryption = str(opts.get("encryption") or "none").split("+", 1)[0]
        if (opts.get("ssid") == ssid and requested in ("auto", current_encryption)
                and (not key or key == opts.get("key", ""))):
            ip = _connected(section, networks[0], ssid)
            if ip:
                candidates.append(dict(section=section, radio=opts.get("device"), iface=networks[0],
                                       ssid=ssid, encryption=opts.get("encryption", "none"),
                                       key=opts.get("key", "") if current_encryption != "none" else "", ip=ip, unchanged=True))
    if len(candidates) > 1:
        raise ValueError("多个出口已连接此 Wi-Fi，请指定无线出口接口")
    if candidates:
        return candidates[0]
    status_data = wireless_ap._wireless_status() or {}
    found = []
    security_mismatch = False
    for device in sorted(status_data):
        if radio and device != radio:
            continue
        active = [(sec, opts) for sec, opts in data.items()
                  if opts.get("device") == device and opts.get("mode") == "sta"
                  and opts.get("disabled", "0") != "1"]
        if len(active) > 1:
            continue
        section, opts = active[0] if active else ("", {})
        if section and not (opts.get("ssid") == ssid or opts.get("jxnu_auto") == "1"
                            or (iface and str(opts.get("network", "")) == iface)):
            continue
        if iface and section and str(opts.get("network", "")) != iface:
            continue
        scan_device, _ = wireless_ap._scan_device(status_data, section, device)
        if not scan_device:
            continue
        scan = wireless_ap._iwinfo("scan", scan_device, timeout=20) or {}
        for entry in scan.get("results", []):
            if entry.get("ssid") != ssid or not wireless_ap.valid_bssid(entry.get("bssid")):
                continue
            encryption = _encryption(entry, requested)
            if not encryption:
                security_mismatch = True
                continue
            if encryption == "none" and key:
                security_mismatch = True
                continue
            existing_network = str(opts.get("network") or "").split()
            target_iface = iface or (existing_network[0] if len(existing_network) == 1 else "wwan")
            found.append(dict(section=section or "smart_srun_setup_" + device, radio=device,
                              iface=target_iface, ssid=ssid, encryption=encryption,
                              key=(key or (opts.get("key", "") if opts.get("ssid") == ssid else "")) if encryption != "none" else "",
                              signal=wireless_ap._signal(entry.get("signal")) or -127, unchanged=False))
    if not found:
        if security_mismatch:
            raise ValueError("未找到与所选加密方式及密码设置匹配的 Wi-Fi，请核对加密方式或选择自动识别；企业认证网络需在网络设置中配置")
        raise ValueError("未扫描到可连接的此 Wi-Fi。请核对名称并启用无线；企业认证 Wi-Fi 或被其它客户端占用的无线电需先在网络设置中配置")
    plan = sorted(found, key=lambda item: item["signal"], reverse=True)[0]
    if plan["encryption"] != "none" and not plan["key"]:
        raise ValueError("此 Wi-Fi 需要无线密码，请填写 Wi-Fi 密码后重新连接（不是学工号密码）")
    if plan["encryption"] in ("psk", "psk2", "psk-mixed", "sae-mixed") and not (
            8 <= len(plan["key"].encode("utf-8")) <= 63 or re.fullmatch(r"[a-fA-F0-9]{64}", plan["key"])):
        raise ValueError("Wi-Fi 密码应为 8–63 字节或 64 位十六进制密钥")
    return plan


def _stage(directory, plan):
    iface, section, radio = plan["iface"], plan["section"], plan["radio"]
    network = _sections(directory, "network")
    existing = network.get(iface, {})
    if existing and (existing.get(".type") != "interface" or existing.get("proto") != ["dhcp"]
                     or existing.get("device") or existing.get("ifname")):
        raise ValueError("所选出口已用于其它网络，请在高级选项填写一个新的无线接口名")
    for name, opts in _sections(directory, "wireless").items():
        if name == section and (opts.get(".type") != "wifi-iface" or opts.get("mode") != ["sta"]):
            raise ValueError("目标无线配置名称已被其它接口使用，请选择其它无线电")
        if name != section and iface in opts.get("network", []):
            raise ValueError("所选出口仍被其它无线接口使用，请填写新的无线接口名")
    batch = []
    def put(path, value):
        batch.append("set " + path + "=" + _quote(value))
    put("network." + iface, "interface")
    put("network." + iface + ".proto", "dhcp")
    put("wireless." + section, "wifi-iface")
    for option, value in dict(device=radio, mode="sta", network=iface, ssid=plan["ssid"],
                              encryption=plan["encryption"], disabled="0", jxnu_auto="1",
                              smart_srun_ap_selection="auto").items():
        put("wireless." + section + "." + option, value)
    batch.append("delete wireless." + section + ".bssid")
    if plan["encryption"] == "none":
        batch.append("delete wireless." + section + ".key")
    else:
        put("wireless." + section + ".key", plan["key"])
    firewall = _sections(directory, "firewall")
    zones = [(name, opts) for name, opts in firewall.items() if opts.get(".type") == "zone"]
    attached = [(name, opts) for name, opts in zones if iface in opts.get("network", [])]
    if attached and (len(attached) != 1 or attached[0][1].get("input") != ["REJECT"]
                     or attached[0][1].get("masq") != ["1"]):
        raise ValueError("所选出口的防火墙不是常规 WAN 区域，请先调整防火墙或填写新的无线接口名")
    if not attached:
        wan = [(name, opts) for name, opts in zones if opts.get("name") == ["wan"]
               and opts.get("input") == ["REJECT"] and opts.get("masq") == ["1"]]
        if len(wan) != 1:
            raise ValueError("未找到启用 NAT 的 WAN 防火墙区域，请先在网络设置中配置")
        attached = wan
        batch.append("add_list firewall." + wan[0][0] + ".network=" + _quote(iface))
    zone_name = attached[0][1].get("name", [""])[0]
    if not any(opts.get(".type") == "forwarding" and opts.get("src") == ["lan"]
               and opts.get("dest") == [zone_name] for opts in firewall.values()):
        raise ValueError("防火墙未允许 LAN 转发到所选出口，请先在网络设置中配置")
    _uci(directory, "batch", input_text="\n".join(batch) + "\n")
    for package in PACKAGES:
        _uci(directory, "commit", package)


def _reload():
    _command(["/etc/init.d/network", "reload"], timeout=45)
    _command(["/etc/init.d/firewall", "reload"], timeout=30)


def _replace_file(path, content):
    temp = path + ".smart-srun-setup"
    with open(temp, "wb") as handle:
        os.chmod(temp, 0o600)
        handle.write(content)
    os.replace(temp, path)


def worker(job):
    import fcntl
    import daemon

    path = _path(job)
    stopped = False
    committed = False
    before, applied = {}, {}
    lock = open(os.path.join(JOB_ROOT, "lock"), "a")
    def report(state, message, **extra):
        value = dict(ok=state not in ("failed", "cancelled"), job=job, state=state, message=message)
        value.update(extra)
        _write(os.path.join(path, "status.json"), value)
    def command():
        try:
            return _read(os.path.join(path, "command.json")).get("command", "")
        except FileNotFoundError:
            return ""
    try:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            raise RuntimeError("另一个无线配置正在进行，请完成或关闭那个向导后再试") from None
        payload = _read(os.path.join(path, "input.json"))
        os.unlink(os.path.join(path, "input.json"))
        plan = _plan(payload)
        if command() == "cancel":
            raise RuntimeError("已取消无线连接")
        _write(os.path.join(path, "account.json"), {
            "ssid": plan["ssid"], "radio": plan["radio"], "encryption": plan["encryption"], "key": plan["key"]})
        if plan["unchanged"]:
            report("connected", "此 Wi-Fi 已连接，无需修改网络配置", iface=plan["iface"],
                   ssid=plan["ssid"], radio=plan["radio"], encryption=plan["encryption"], ip=plan["ip"],
                   expires_at=time.time() + HOLD_SECONDS)
            return
        for package in PACKAGES:
            if _command(["uci", "-q", "changes", package]).strip():
                raise ValueError("网络设置中有尚未应用的修改，请先保存或撤销后再连接")
        staging = os.path.join(path, "staging")
        os.mkdir(staging, 0o700)
        os.mkdir(staging + "/delta", 0o700)
        for package in PACKAGES:
            target = os.path.join(CONFIG_ROOT, package)
            with open(target, "rb") as handle:
                before[target] = handle.read()
            _replace_file(os.path.join(staging, package), before[target])
        _stage(staging, plan)
        for package in PACKAGES:
            target = os.path.join(CONFIG_ROOT, package)
            with open(target, "rb") as handle:
                if handle.read() != before[target]:
                    raise RuntimeError("网络配置同时被其它操作修改，请重试")
        stopped = daemon.daemon_is_alive()
        if stopped:
            _command(["/etc/init.d/smart_srun", "stop"])
        report("connecting", "正在切换无线客户端并获取地址，页面短暂断线后会自动继续…")
        for package in PACKAGES:
            target = os.path.join(CONFIG_ROOT, package)
            with open(os.path.join(staging, package), "rb") as handle:
                content = handle.read()
            if content != before[target]:
                applied[target] = content
                _replace_file(target, content)
        _reload()
        deadline = time.monotonic() + 60
        ip = ""
        while time.monotonic() < deadline and command() != "cancel":
            ip = _connected(plan["section"], plan["iface"], plan["ssid"])
            if ip:
                break
            time.sleep(2)
        if not ip:
            raise RuntimeError("未能连接此 Wi-Fi 并获取地址，请检查信号和 Wi-Fi 密码")
        report("ready", "已连接校园网 Wi-Fi。请在 15 分钟内保存账号；关闭向导或超时会恢复原网络",
               ssid=plan["ssid"], iface=plan["iface"], radio=plan["radio"], encryption=plan["encryption"], ip=ip)
        deadline = time.monotonic() + HOLD_SECONDS
        while time.monotonic() < deadline and not command():
            time.sleep(1)
        committed = command() == "commit"
        if committed:
            report("done", "已保留无线连接")
        else:
            raise RuntimeError("已取消或配置超时")
    except Exception as exc:
        restored = True
        for target, content in applied.items():
            try:
                with open(target, "rb") as handle:
                    current = handle.read()
                if current == content:
                    _replace_file(target, before[target])
                elif current != before[target]:
                    restored = False
            except OSError:
                restored = False
        if applied:
            try:
                _reload()
            except Exception:
                restored = False
        message = str(exc)
        if applied:
            message += "；已恢复原网络" if restored else "；网络恢复未完成，请检查网络设置"
        report("cancelled" if command() == "cancel" else "failed", message)
    finally:
        for filename in ("input.json", "account.json"):
            if filename == "account.json" and status(job).get("state") == "connected":
                continue
            try:
                os.unlink(os.path.join(path, filename))
            except FileNotFoundError:
                pass
        shutil.rmtree(os.path.join(path, "staging"), ignore_errors=True)
        if stopped or committed:
            _command(["/etc/init.d/smart_srun", "start"], check=False)
        lock.close()


if __name__ == "__main__":
    worker(sys.argv[1])
