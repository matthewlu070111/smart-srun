#!/usr/bin/env python3
"""Exercise real Linux interface binding against isolated mock campus networks.

Run as root on a disposable Linux development host with iproute2 installed:
    sudo python3 scripts/test_network_namespaces.py --output-dir /tmp/srun-netns-results

Only task-owned network namespaces and veth devices are created. All addresses
are documentation addresses; host routes and existing interfaces are untouched.
The HTTP/DNS servers accept fake test traffic only. Results and request logs
remain in output-dir; a finally block cleans up the created namespaces and processes.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit


SERVER_IP = "192.0.2.1"
SHARED_IP = "192.0.2.10"
CHANGED_IP = "192.0.2.11"
HTTP_PORT = 18080


def run(*command, check=True, env=None):
    return subprocess.run(command, check=check, text=True, capture_output=True, timeout=15, env=env)


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def serve(args):
    lock = threading.Lock()

    def record(value):
        value["server"] = args.server
        value["at_ns"] = time.monotonic_ns()
        with lock, Path(args.request_log).open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(value) + "\n")
            handle.flush()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            query = parse_qs(urlsplit(self.path).query)
            record({"protocol": "http", "case": query.get("case", [""])[0],
                    "peer": self.client_address[0], "path": self.path})
            payload = json.dumps({"server": args.server, "peer": self.client_address[0]}).encode("utf-8")
            self.send_response(204 if self.path.startswith("/generate_204") else 200)
            self.send_header("Content-Length", "0" if self.path.startswith("/generate_204") else str(len(payload)))
            self.end_headers()
            if not self.path.startswith("/generate_204"):
                self.wfile.write(payload)

        def log_message(self, *_args):
            pass

    dns = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    dns.bind((SERVER_IP, 53))

    def serve_dns():
        while True:
            packet, peer = dns.recvfrom(4096)
            offset, labels = 12, []
            while packet[offset]:
                size = packet[offset]
                labels.append(packet[offset + 1:offset + size + 1].decode("ascii"))
                offset += size + 1
            record({"protocol": "dns", "case": ".".join(labels), "peer": peer[0]})
            question = packet[12:offset + 5]
            response = packet[:2] + struct.pack(">HHHHH", 0x8180, 1, 1, 0, 0)
            response += question + b"\xc0\x0c" + struct.pack(">HHIH", 1, 1, 60, 4)
            dns.sendto(response + socket.inet_aton(SERVER_IP), peer)

    threading.Thread(target=serve_dns, daemon=True).start()
    server = HTTPServer((SERVER_IP, HTTP_PORT), Handler)
    Path(args.ready_file).write_text("ready", encoding="ascii")
    server.serve_forever()


def client(args):
    sys.path.insert(0, str(Path(args.repo_root) / "root/usr/lib/smart_srun"))
    import logger
    import network

    logger.LOG_FILE = str(Path(args.output_dir) / "runtime.log")
    url = "http://%s:%d/srun_portal" % (args.host or SERVER_IP, HTTP_PORT)
    cfg = {"campus_access_mode": "wired", "wired_iface": args.device,
           "_multi_wan_strict_bind": "1", "base_url": url}
    try:
        if args.client == "ip_lookup":
            result = network.get_network_device_for_ip(SHARED_IP)
        elif args.client == "source_only":
            result = json.loads(network.http_get(url, params={"case": args.case}, timeout=1, bind_ip=SHARED_IP))
        else:
            if args.client == "explicit_http":
                binding = {"bind_ip": args.bind_ip, "bind_device": args.device, "strict": True}
            else:
                binding = network.resolve_http_binding(url, cfg, bind_ip=args.bind_ip)
            if args.client == "binding":
                result = binding
            elif args.client == "probe":
                probe_binding = dict(binding)
                probe_iface = probe_binding.pop("bind_iface", args.device)
                result = network._probe_http_status(
                    "http://%s:%d/generate_204?case=%s" % (SERVER_IP, HTTP_PORT, args.case),
                    1, iface=probe_iface, **probe_binding,
                )
            elif args.client == "dns":
                dns_binding = dict(binding)
                dns_binding.pop("bind_iface", None)
                result = network._dns_query_a(args.case + ".example", SERVER_IP, 1, **dns_binding)
            elif args.client == "portal":
                result = network.test_portal_reachability(cfg, timeout=1, **binding)
            else:
                result = json.loads(network.http_get(
                    url, params={"case": args.case, "username": "fake-campus-account"}, timeout=1, **binding
                ))
        print(json.dumps({"ok": True, "value": result}))
    except Exception as exc:
        print(json.dumps({"ok": False, "error": str(exc), "exception": type(exc).__name__}))


def integration(args):
    if sys.platform != "linux" or os.geteuid() != 0:
        raise SystemExit("Run this integration test as root on Linux; no changes were made.")
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=True)
    run_id = uuid.uuid4().hex[:8]
    prefix = "ssrn-codex-" + run_id
    namespaces = {role: prefix + "-" + role for role in ("client", "a", "b")}
    request_logs = {label: output / ("requests-%s-%s.jsonl" % (label, run_id)) for label in ("a", "b")}
    created_namespaces, created_host_links, servers, handles, results = [], [], [], [], []
    script = str(Path(__file__).resolve())
    repo = str(Path(args.repo_root).resolve())
    # Generic Linux has no netifd. This read-only metadata fixture exposes the
    # actual namespace device addresses with OpenWrt's ubus status shape. The
    # production resolver and every DNS/HTTP socket still execute unchanged.
    fixture_dir = output / ("netifd-" + run_id)
    fixture_dir.mkdir()
    fixture = fixture_dir / "ubus"
    fixture.write_text("#!" + sys.executable + "\n" + """import json, os, subprocess, sys
interface = next((value.split('network.interface.', 1)[1] for value in sys.argv if 'network.interface.' in value), '')
if interface not in ('wan-a', 'wan-b'):
    sys.exit(1)
result = subprocess.run(['ip', '-j', '-4', 'addr', 'show', 'dev', interface], capture_output=True, text=True)
if result.returncode:
    sys.exit(1)
device = json.loads(result.stdout)[0]
print(json.dumps({'up': 'UP' in device['flags'], 'l3_device': interface,
                  'ipv4-address': [{'address': item['local']} for item in device.get('addr_info', []) if item['family'] == 'inet'],
                  'dns-server': [] if os.environ.get('SRUN_NETNS_NO_DNS') else ['192.0.2.1']}))
""", encoding="utf-8")
    fixture.chmod(0o755)
    host_routes_before = run("ip", "-j", "route", "show", "default").stdout
    host_links_before = {item["ifname"] for item in json.loads(run("ip", "-j", "link", "show").stdout)}
    client_default = ""
    completed = False

    def ip(role, *command):
        return run("ip", "-n", namespaces[role], *command)

    def request(operation, device="wan-b", case="", bind_ip=None, host=None, no_dns=False):
        command = ["ip", "netns", "exec", namespaces["client"], sys.executable, script,
                   "--client", operation, "--repo-root", repo, "--output-dir", str(output),
                   "--device", device, "--case", case]
        if bind_ip:
            command.extend(["--bind-ip", bind_ip])
        if host:
            command.extend(["--host", host])
        env = dict(os.environ, PATH=str(fixture_dir) + os.pathsep + os.environ.get("PATH", ""))
        if no_dns:
            env["SRUN_NETNS_NO_DNS"] = "1"
        return json.loads(run(*command, env=env).stdout)

    def records():
        values = []
        for label in ("a", "b"):
            path = request_logs[label]
            if path.exists():
                values.extend(json.loads(line) for line in path.read_text(encoding="utf-8").splitlines())
        return sorted(values, key=lambda item: item["at_ns"])

    def check(name, callback):
        try:
            details = callback()
            result = {"case": name, "passed": True, "details": details}
        except Exception as exc:
            result = {"case": name, "passed": False, "error": str(exc)}
        results.append(result)
        print(json.dumps(result, ensure_ascii=False), flush=True)

    def expect_http(case, device, expected_server, peer=SHARED_IP):
        response = request("http", device, case)
        assert response.get("ok"), response
        assert response["value"] == {"server": expected_server, "peer": peer}, response
        return response["value"]

    def expect_failure(case, operation="http", device="wan-b", bind_ip=None):
        before = len(records())
        response = request(operation, device, case, bind_ip)
        assert not response.get("ok"), response
        assert len(records()) == before, "Failed request reached a mock campus server"
        return response

    try:
        for namespace in namespaces.values():
            run("ip", "netns", "add", namespace)
            created_namespaces.append(namespace)
            run("ip", "-n", namespace, "link", "set", "lo", "up")
        for label in ("a", "b"):
            host_client = "ssc" + run_id + label
            host_server = "sss" + run_id + label
            run("ip", "link", "add", host_client, "type", "veth", "peer", "name", host_server)
            created_host_links.extend([host_client, host_server])
            run("ip", "link", "set", host_client, "netns", namespaces["client"])
            run("ip", "link", "set", host_server, "netns", namespaces[label])
            ip("client", "link", "set", host_client, "name", "wan-" + label)
            ip(label, "link", "set", host_server, "name", "campus")
            ip("client", "addr", "add", SHARED_IP + "/24", "dev", "wan-" + label)
            ip(label, "addr", "add", SERVER_IP + "/24", "dev", "campus")
            ip("client", "link", "set", "wan-" + label, "up")
            ip(label, "link", "set", "campus", "up")
            # Disable reverse-path rejection only inside these new namespaces;
            # the test needs two equal connected prefixes, as campus WANs may have.
            for namespace in (namespaces["client"], namespaces[label]):
                run("ip", "netns", "exec", namespace, "sysctl", "-qw", "net.ipv4.conf.all.rp_filter=0")
            ready = output / ("ready-" + label + "-" + run_id)
            handle = (output / ("server-%s-%s.log" % (label, run_id))).open("a", encoding="utf-8")
            handles.append(handle)
            process = subprocess.Popen(
                ["ip", "netns", "exec", namespaces[label], sys.executable, script, "--server", label,
                 "--request-log", str(request_logs[label]), "--ready-file", str(ready)],
                stdin=subprocess.DEVNULL, stdout=handle, stderr=handle,
            )
            servers.append(process)
            deadline = time.monotonic() + 5
            while not ready.exists() and process.poll() is None and time.monotonic() < deadline:
                time.sleep(0.05)
            assert ready.exists(), "Mock server %s did not start" % label
        ip("client", "route", "add", "default", "via", SERVER_IP, "dev", "wan-a")
        client_default = ip("client", "-j", "route", "show", "default").stdout
        write_json(output / "topology.json", {"namespaces": namespaces, "client_ip": SHARED_IP,
                   "campus_ip": SERVER_IP, "default_device": "wan-a", "repo": repo})

        def duplicate_lookup():
            result = request("ip_lookup")
            assert result == {"ok": True, "value": None}, result
            return result

        def source_control():
            result = request("source_only", case="source-only-control")
            assert result.get("ok") and result["value"]["server"] == "a", result
            return result

        check("duplicate IP lookup is ambiguous", duplicate_lookup)
        check("source-only control follows WAN A", source_control)
        for label in ("a", "b", "a", "b"):
            check("strict HTTP through WAN " + label, lambda label=label: expect_http("strict-" + label, "wan-" + label, label))
        for label in ("a", "b"):
            def probe(label=label):
                case = "probe-" + label
                result = request("probe", "wan-" + label, case)
                assert result == {"ok": True, "value": 204}, result
                assert any(item["server"] == label and item["case"] == case for item in records())
                return result

            def dns(label=label):
                case = "dns-" + label
                result = request("dns", "wan-" + label, case)
                assert result == {"ok": True, "value": [SERVER_IP]}, result
                assert any(item["server"] == label and item["case"] == case + ".example" for item in records())
                return result

            check("strict 204 probe through WAN " + label, probe)
            check("strict DNS through WAN " + label, dns)
            def domain_http(label=label):
                before = len(records())
                result = request("http", "wan-" + label, "domain-" + label, host="auth-" + label + ".example")
                assert result.get("ok") and result["value"]["server"] == label, result
                new = records()[before:]
                dns_records = [item for item in new if item["protocol"] == "dns"]
                assert dns_records and all(item["server"] == label for item in dns_records), new
                return result

            def domain_portal(label=label):
                result = request("portal", "wan-" + label, host="portal-" + label + ".example")
                assert result == {"ok": True, "value": [True, ""]}, result
                return result

            check("strict HTTP domain uses WAN " + label + " DNS", domain_http)
            check("strict portal probe on WAN " + label, domain_portal)

        def no_interface_dns():
            before = len(records())
            result = request("http", host="no-dns.example", no_dns=True)
            assert not result.get("ok") and "DNS" in result.get("error", ""), result
            assert len(records()) == before, "A DNS-less request reached a mock server"
            return result

        check("strict domain without interface DNS fails closed", no_interface_dns)
        ip("client", "link", "set", "wan-b", "down")
        check("WAN B link down cannot use WAN A", lambda: expect_failure("b-link-down"))
        check("WAN A survives WAN B interruption", lambda: expect_http("a-survives", "wan-a", "a"))
        ip("client", "link", "set", "wan-b", "up")
        check("WAN B recovers", lambda: expect_http("b-recovers", "wan-b", "b"))
        ip("client", "addr", "del", SHARED_IP + "/24", "dev", "wan-b")
        check("WAN B without IPv4 fails before HTTP", lambda: expect_failure("b-no-address"))
        check("stale source IP on another device fails closed", lambda: expect_failure(
            "b-stale-explicit", operation="explicit_http", bind_ip=SHARED_IP))
        ip("client", "addr", "add", CHANGED_IP + "/24", "dev", "wan-b")
        check("WAN B reads changed DHCP address", lambda: expect_http("b-new-ip", "wan-b", "b", CHANGED_IP))
        check("old transaction IP is rejected", lambda: expect_failure("b-old-transaction", bind_ip=SHARED_IP))
        check("cached old source IP fails after DHCP replacement", lambda: expect_failure(
            "b-stale-after-dhcp", operation="explicit_http", bind_ip=SHARED_IP))
        ip("client", "link", "delete", "wan-b")
        check("removed device cannot use WAN A", lambda: expect_failure(
            "b-removed", operation="explicit_http", bind_ip=CHANGED_IP))
        check("WAN A remains usable", lambda: expect_http("a-final", "wan-a", "a"))
        after_default = ip("client", "-j", "route", "show", "default").stdout

        def unchanged_client_default():
            assert after_default == client_default, "default route changed"
            return {"default_device": "wan-a"}

        check("authentication never rewrites client default route", unchanged_client_default)
        completed = True
    finally:
        for process in servers:
            process.terminate()
        for process in servers:
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        for handle in handles:
            handle.close()
        for namespace in reversed(created_namespaces):
            run("ip", "netns", "delete", namespace, check=False)
        for link in created_host_links:
            run("ip", "link", "delete", link, check=False)
        host_routes_after = run("ip", "-j", "route", "show", "default").stdout
        host_links_after = {item["ifname"] for item in json.loads(run("ip", "-j", "link", "show").stdout)}
        namespace_names = {line.split()[0] for line in run("ip", "netns", "list").stdout.splitlines()}
        cleanup_ok = not (set(created_namespaces) & namespace_names) and not (set(created_host_links) & host_links_after)
        host_unchanged = host_routes_before == host_routes_after and host_links_before == host_links_after
        report = {"passed": completed and all(item["passed"] for item in results) and cleanup_ok and host_unchanged,
                  "completed": completed,
                  "cases": results, "cleanup_ok": cleanup_ok, "host_network_unchanged": host_unchanged,
                  "host_default_route_sha256": hashlib.sha256(host_routes_after.encode()).hexdigest(),
                  "requests": records(), "namespaces": namespaces}
        write_json(output / "results.json", report)
        print(json.dumps({"report": str(output / "results.json"), "passed": report["passed"],
                          "cleanup_ok": cleanup_ok, "host_network_unchanged": host_unchanged}), flush=True)
    return 0 if report["passed"] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--output-dir", default="/tmp/srun-netns-results")
    parser.add_argument("--server", choices=("a", "b"), help=argparse.SUPPRESS)
    parser.add_argument("--request-log", help=argparse.SUPPRESS)
    parser.add_argument("--ready-file", help=argparse.SUPPRESS)
    parser.add_argument("--client", help=argparse.SUPPRESS)
    parser.add_argument("--device", default="wan-b", help=argparse.SUPPRESS)
    parser.add_argument("--case", default="", help=argparse.SUPPRESS)
    parser.add_argument("--bind-ip", help=argparse.SUPPRESS)
    parser.add_argument("--host", help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.server:
        serve(args)
        return 0
    if args.client:
        client(args)
        return 0
    return integration(args)


if __name__ == "__main__":
    sys.exit(main())
