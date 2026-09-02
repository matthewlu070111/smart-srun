import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock


THIS_DIR = os.path.dirname(os.path.abspath(__file__))
WORKTREE_ROOT = os.path.dirname(THIS_DIR)
MODULE_DIR = os.path.join(WORKTREE_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_DIR not in sys.path:
    sys.path.insert(0, MODULE_DIR)


from _portal_urls import (
    BIND_IP,
    PORTAL_BARE_HOST,
    PORTAL_HOST,
    PORTAL_LOGIN_URL,
    PORTAL_PING_URL,
    WIRED_BIND_IP,
)
import config
import daemon
import network
import srun_auth
import wireless


class NetworkBindIpTests(unittest.TestCase):
    def test_network_device_is_resolved_from_bound_dhcp_ip(self):
        output = (
            "33: wan.v2    inet " + WIRED_BIND_IP + "/22 brd 192.0.2.255 "
            "scope global wan.v2"
        )
        with mock.patch.object(network, "run_cmd", return_value=(True, output)):
            device = network.get_network_device_for_ip(WIRED_BIND_IP)

        self.assertEqual(device, "wan.v2")

    def test_bound_socket_uses_source_ip_and_so_bindtodevice(self):
        fake_socket = mock.Mock()
        address = (PORTAL_BARE_HOST, 80)
        addrinfo = [
            (network.socket.AF_INET, network.socket.SOCK_STREAM, 6, "", address)
        ]
        with (
            mock.patch.object(
                network.socket, "getaddrinfo", return_value=addrinfo
            ) as getaddrinfo,
            mock.patch.object(network.socket, "socket", return_value=fake_socket),
        ):
            result = network._create_bound_connection(
                address,
                5,
                (WIRED_BIND_IP, 0),
                "wan.v2",
            )

        self.assertIs(result, fake_socket)
        getaddrinfo.assert_called_once_with(
            PORTAL_BARE_HOST.encode("ascii"), 80, 0, network.socket.SOCK_STREAM
        )
        fake_socket.settimeout.assert_called_once_with(5)
        fake_socket.setsockopt.assert_called_once_with(
            network.socket.SOL_SOCKET,
            getattr(network.socket, "SO_BINDTODEVICE", 25),
            b"wan.v2\0",
        )
        fake_socket.bind.assert_called_once_with((WIRED_BIND_IP, 0))
        fake_socket.connect.assert_called_once_with(address)

    def test_init_ip_falls_back_to_explicit_dhcp_bind_ip(self):
        with (
            mock.patch.object(srun_auth, "http_get", return_value="<html></html>"),
            mock.patch.object(
                srun_auth,
                "get_local_ip_for_target",
                side_effect=AssertionError("default route must not replace bind IP"),
            ),
        ):
            ip = srun_auth.init_getip(PORTAL_LOGIN_URL, bind_ip=WIRED_BIND_IP)

        self.assertEqual(ip, WIRED_BIND_IP)

    def test_wired_bind_ip_comes_from_selected_virtual_wan(self):
        cfg = {"campus_access_mode": "wired", "wired_iface": "wan.v2"}
        with (
            mock.patch.object(network, "get_ipv4_from_network_interface", return_value=WIRED_BIND_IP) as get_ip,
            mock.patch.object(
                network,
                "get_local_ip_for_target",
                side_effect=AssertionError("route-selected address must not win"),
            ),
        ):
            bind_ip = network.resolve_bind_ip(PORTAL_LOGIN_URL, cfg)

        self.assertEqual(bind_ip, WIRED_BIND_IP)
        get_ip.assert_called_once_with("wan.v2")

    def test_wired_bind_ip_fails_closed_when_selected_interface_has_no_ip(self):
        cfg = {
            "campus_access_mode": "wired",
            "wired_iface": "wan.v2",
            "_multi_wan_strict_bind": "1",
        }
        with mock.patch.object(
            network, "get_ipv4_from_network_interface", return_value=None
        ):
            with self.assertRaisesRegex(RuntimeError, "wan.v2"):
                network.resolve_bind_ip(PORTAL_LOGIN_URL, cfg)

    def test_legacy_wired_config_falls_back_to_route_when_iface_has_no_ip(self):
        cfg = {"campus_access_mode": "wired", "wired_iface": "wan"}
        with (
            mock.patch.object(
                network, "get_ipv4_from_network_interface", return_value=None
            ),
            mock.patch.object(
                network, "get_local_ip_for_target", return_value=BIND_IP
            ) as route_ip,
        ):
            bind_ip = network.resolve_bind_ip(PORTAL_LOGIN_URL, cfg)

        self.assertEqual(BIND_IP, bind_ip)
        route_ip.assert_called_once_with(PORTAL_HOST)

    def test_get_ipv4_falls_back_to_ip_addr_when_ubus_json_is_invalid(self):
        calls = []

        def fake_run(cmd):
            calls.append(cmd)
            if cmd[:3] == ["ubus", "call", "network.interface.wan"]:
                return True, "{not-json"
            return True, "2: eth0    inet 10.0.0.9/24 brd 10.0.0.255 scope global eth0"

        with mock.patch.object(network, "run_cmd", side_effect=fake_run):
            ip = network.get_ipv4_from_network_interface("wan")

        self.assertEqual(ip, "10.0.0.9")
        self.assertEqual(
            calls[-1],
            ["ip", "-4", "-o", "addr", "show", "dev", "wan"],
        )

    def test_get_ipv4_does_not_swallow_unexpected_parse_errors(self):
        with (
            mock.patch.object(
                network, "run_cmd", return_value=(True, '{"ipv4-address": []}')
            ),
            mock.patch.object(
                network,
                "_parse_network_interface_status",
                side_effect=RuntimeError("unexpected"),
            ),
        ):
            with self.assertRaisesRegex(RuntimeError, "unexpected"):
                network.get_ipv4_from_network_interface("wan")

    def test_http_get_binds_source_ip_via_stdlib_when_bind_ip_is_explicit(self):
        fake_resp = mock.Mock()
        fake_resp.read.return_value = b"bound-response"
        fake_resp.status = 200
        fake_conn = mock.Mock()
        fake_conn.getresponse.return_value = fake_resp

        with (
            mock.patch.object(network, "HAVE_URLLIB", True),
            mock.patch.object(
                network, "get_network_device_for_ip", return_value="wan.v2"
            ),
            mock.patch.object(
                network.http_client, "HTTPConnection", return_value=fake_conn
            ) as http_conn,
            mock.patch.object(
                network.urllib_request,
                "urlopen",
                side_effect=AssertionError("urlopen path should be skipped"),
            ) as urlopen,
            mock.patch.object(
                network.subprocess,
                "check_output",
                side_effect=AssertionError("wget path should be skipped"),
            ) as check_output,
        ):
            body = network.http_get(
                PORTAL_PING_URL, timeout=7, bind_ip=BIND_IP
            )

        self.assertEqual(body, "bound-response")
        urlopen.assert_not_called()
        check_output.assert_not_called()
        self.assertEqual(http_conn.call_args.args[0], PORTAL_HOST)
        self.assertEqual(http_conn.call_args.kwargs.get("timeout"), 7)
        self.assertEqual(
            http_conn.call_args.kwargs.get("source_address"), (BIND_IP, 0)
        )
        self.assertTrue(callable(fake_conn._create_connection))
        fake_conn.request.assert_called_once_with(
            "GET", "/ping", headers=network.HEADER
        )

    def test_http_get_falls_back_to_wget_when_stdlib_bind_fails(self):
        with (
            mock.patch.object(network, "HAVE_URLLIB", True),
            mock.patch.object(
                network.http_client,
                "HTTPConnection",
                side_effect=OSError("Cannot assign requested address"),
            ),
            mock.patch.object(
                network.os,
                "path",
                wraps=network.os.path,
            ) as mock_path,
            mock.patch.object(
                network.subprocess,
                "check_output",
                return_value=b"wget-response",
            ) as check_output,
        ):
            mock_path.exists.side_effect = lambda path: path == "/usr/bin/wget"

            body = network.http_get(
                PORTAL_PING_URL, timeout=7, bind_ip=BIND_IP
            )

        self.assertEqual(body, "wget-response")
        self.assertEqual(
            check_output.call_args[0][0],
            [
                "/usr/bin/wget",
                "-q",
                "-O",
                "-",
                "--timeout=7",
                "--bind-address=" + BIND_IP,
                PORTAL_PING_URL,
            ],
        )

    def test_http_get_does_not_fall_back_to_source_only_wget_for_multi_wan(self):
        with (
            mock.patch.object(network, "HAVE_URLLIB", True),
            mock.patch.object(
                network, "get_network_device_for_ip", return_value="wan.v2"
            ),
            mock.patch.object(
                network,
                "_http_get_via_stdlib",
                side_effect=OSError("bound socket failed"),
            ),
            mock.patch.object(
                network.subprocess,
                "check_output",
                side_effect=AssertionError("unsafe wget fallback must not run"),
            ),
        ):
            with self.assertRaisesRegex(RuntimeError, "wan.v2"):
                network.http_get(PORTAL_PING_URL, timeout=7, bind_ip=BIND_IP)

    def test_http_get_rejects_uclient_fetch_when_bind_ip_is_required(self):
        with (
            mock.patch.object(network, "HAVE_URLLIB", False),
            mock.patch.object(
                network.os,
                "path",
                wraps=network.os.path,
            ) as mock_path,
            mock.patch.object(network.subprocess, "check_output") as check_output,
        ):
            mock_path.exists.side_effect = lambda path: path == "/usr/bin/uclient-fetch"

            with self.assertRaisesRegex(RuntimeError, "bind_ip"):
                network.http_get(
                    PORTAL_PING_URL,
                    timeout=7,
                    bind_ip=BIND_IP,
                )

        check_output.assert_not_called()

    def test_http_get_logs_redacted_url_when_query_contains_sensitive_params(self):
        with (
            mock.patch.object(network, "HAVE_URLLIB", False),
            mock.patch.object(
                network.os,
                "path",
                wraps=network.os.path,
            ) as mock_path,
            mock.patch.object(
                network.subprocess,
                "check_output",
                return_value=b"ok",
            ) as check_output,
            mock.patch.object(network, "log") as log_fn,
        ):
            mock_path.exists.side_effect = lambda path: path == "/usr/bin/wget"

            body = network.http_get(
                PORTAL_LOGIN_URL,
                params={
                    "username": "alice",
                    "password": "{MD5}secret",
                    "info": "opaque-info",
                    "chksum": "deadbeef",
                },
                timeout=7,
            )

        self.assertEqual(body, "ok")
        request_url = check_output.call_args[0][0][-1]
        self.assertIn("password=", request_url)
        self.assertIn("info=", request_url)
        self.assertIn("chksum=", request_url)

        logged_urls = [call.kwargs.get("url", "") for call in log_fn.call_args_list]
        self.assertIn(PORTAL_LOGIN_URL, logged_urls)
        for value in logged_urls:
            self.assertNotIn("password=", value)
            self.assertNotIn("info=", value)
            self.assertNotIn("chksum=", value)


class ConfigLockingTests(unittest.TestCase):
    def setUp(self):
        self.tmp_dir = tempfile.mkdtemp(prefix="remaining-review-config-")
        self.config_path = os.path.join(self.tmp_dir, "config.json")
        self.original_json_config_file = config.JSON_CONFIG_FILE
        config.JSON_CONFIG_FILE = self.config_path

    def tearDown(self):
        config.JSON_CONFIG_FILE = self.original_json_config_file
        shutil.rmtree(self.tmp_dir)

    def test_update_json_raw_config_mutates_and_persists_under_shared_lock(self):
        config.save_json_raw_config({"enabled": "0", "school": "jxnu"})

        updated = config.update_json_raw_config(
            lambda raw: raw.update({"enabled": "1", "school": "custom"})
        )

        self.assertEqual("1", updated["enabled"])
        self.assertEqual("custom", updated["school"])
        self.assertEqual("1", config.load_json_raw_config()["enabled"])
        self.assertEqual("custom", config.load_json_raw_config()["school"])

    def test_set_json_scalar_config_routes_through_locked_updater(self):
        with mock.patch.object(
            config, "update_json_raw_config", create=True
        ) as updater:
            config.set_json_scalar_config("enabled", "1")

        updater.assert_called_once()

    def test_daemon_config_set_routes_through_locked_updater(self):
        with (
            mock.patch.object(daemon, "load_json_raw_config", return_value={}),
            mock.patch.object(config, "update_json_raw_config", create=True) as updater,
            mock.patch("builtins.print"),
        ):
            daemon._config_set(["enabled=1"])

        updater.assert_called_once()

    def test_invalid_json_config_still_falls_back_to_empty_payload(self):
        with open(self.config_path, "w", encoding="utf-8") as wf:
            wf.write("{not-json")

        self.assertEqual(config.load_json_raw_config(), {})
        self.assertEqual(config.load_json_file(self.config_path), {})

    def test_json_config_read_does_not_swallow_unexpected_open_errors(self):
        with mock.patch("builtins.open", side_effect=RuntimeError("unexpected")):
            with self.assertRaisesRegex(RuntimeError, "unexpected"):
                config.load_json_raw_config()


class WirelessSanitizationTests(unittest.TestCase):
    def test_set_sta_profile_uci_strips_newlines_from_ssid_and_key(self):
        calls = []

        def fake_run(cmd):
            calls.append(cmd)
            return True, ""

        with mock.patch.object(wireless, "run_cmd", side_effect=fake_run):
            ok, message = wireless._set_sta_profile_uci(
                "sta0",
                {
                    "ssid": "campus\nssid",
                    "bssid": "",
                    "encryption": "psk2",
                    "key": "pa\r\nss",
                },
            )

        self.assertTrue(ok)
        self.assertEqual(message, "")
        self.assertIn(["uci", "set", "wireless.sta0.ssid=campusssid"], calls)
        self.assertIn(["uci", "set", "wireless.sta0.key=pass"], calls)

    def test_create_sta_on_radio_strips_newlines_from_written_profile_values(self):
        calls = []

        def fake_run(cmd):
            calls.append(cmd)
            if cmd == ["uci", "add", "wireless", "wifi-iface"]:
                return True, "sta0"
            return True, ""

        with mock.patch.object(wireless, "run_cmd", side_effect=fake_run):
            section, message = wireless.create_sta_on_radio(
                "radio0",
                "wwan",
                {
                    "ssid": "guest\nnet",
                    "bssid": "aa:bb\n:cc:dd:ee:ff",
                    "encryption": "psk2",
                    "key": "to\x00ken\n42",
                },
            )

        self.assertEqual(section, "sta0")
        self.assertEqual(message, "")
        self.assertIn(["uci", "set", "wireless.sta0.ssid=guestnet"], calls)
        self.assertIn(["uci", "set", "wireless.sta0.bssid=aa:bb:cc:dd:ee:ff"], calls)
        self.assertIn(["uci", "set", "wireless.sta0.key=token42"], calls)


if __name__ == "__main__":
    unittest.main()
