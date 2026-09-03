"""Interface identity must survive address lookup, HTTP and status probes."""

import json
import os
import sys
import time
import unittest
from unittest import mock


MODULE_ROOT = os.path.join(
    os.path.dirname(os.path.dirname(__file__)), "root", "usr", "lib", "smart_srun"
)
if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)

# Runtime modules use bare imports after the local source path is installed.
from _portal_urls import (  # noqa: E402
    BIND_IP,
    CLIENT_IP,
    PORTAL_BARE_ORIGIN,
    PORTAL_DNS_NAME,
    PORTAL_IPV4_HOST,
    PORTAL_IPV4_ORIGIN,
    WIRED_BIND_IP,
)
import network  # noqa: E402
import snapshot  # noqa: E402


class StrictNetworkBindingTests(unittest.TestCase):
    def setUp(self):
        self.cfg = {
            "campus_access_mode": "wired",
            "wired_iface": "wan2",
            "_multi_wan_strict_bind": "1",
        }

    def test_logical_interface_keeps_device_even_when_ip_is_shared(self):
        payload = {
            "up": True,
            "l3_device": "eth2",
            "ipv4-address": [{"address": WIRED_BIND_IP}],
        }
        with (
            mock.patch.object(
                network, "run_cmd", return_value=(True, json.dumps(payload))
            ) as command,
            mock.patch.object(
                network,
                "get_network_device_for_ip",
                side_effect=AssertionError("ambiguous"),
            ),
        ):
            binding = network.resolve_http_binding(PORTAL_IPV4_ORIGIN, self.cfg)
        self.assertEqual(
            binding,
            {
                "bind_ip": WIRED_BIND_IP,
                "bind_device": "eth2",
                "strict": True,
                "bind_iface": "wan2",
            },
        )
        command.assert_called_once_with(
            ["ubus", "call", "network.interface.wan2", "status"], timeout=5
        )

    def test_logical_interface_with_no_device_cannot_be_guessed_from_ip(self):
        payload = {"ipv4-address": [{"address": WIRED_BIND_IP}]}
        with mock.patch.object(
            network, "run_cmd", return_value=(True, json.dumps(payload))
        ):
            with self.assertRaisesRegex(RuntimeError, "L3"):
                network.resolve_http_binding(PORTAL_IPV4_ORIGIN, self.cfg)

    def test_literal_device_fallback_is_scoped_to_selected_device(self):
        cfg = dict(self.cfg, wired_iface="eth2")
        with mock.patch.object(
            network,
            "run_cmd",
            side_effect=[
                (False, "not a netifd interface"),
                (True, f"3: eth2@if5 inet {WIRED_BIND_IP}/24 scope global eth2"),
            ],
        ) as command:
            self.assertEqual(
                network.resolve_wired_binding(cfg), (WIRED_BIND_IP, "eth2")
            )
        self.assertEqual(
            command.call_args.args[0], ["ip", "-4", "-o", "addr", "show", "dev", "eth2"]
        )

    def test_strict_address_change_requires_new_transaction(self):
        with mock.patch.object(
            network, "resolve_wired_binding", return_value=(BIND_IP, "eth2")
        ):
            with self.assertRaisesRegex(RuntimeError, "IPv4"):
                network.resolve_http_binding(
                    PORTAL_IPV4_ORIGIN, self.cfg, bind_ip=WIRED_BIND_IP
                )

    def test_missing_selected_interface_never_uses_route_source(self):
        with (
            mock.patch.object(network, "run_cmd", return_value=(False, "missing")),
            mock.patch.object(network, "get_local_ip_for_target") as fallback,
        ):
            with self.assertRaises(RuntimeError):
                network.resolve_http_binding(PORTAL_IPV4_ORIGIN, self.cfg)
        fallback.assert_not_called()

    def test_strict_http_requires_explicit_device_before_any_fetch(self):
        with mock.patch.object(network, "_http_get_via_stdlib") as fetch:
            with self.assertRaisesRegex(RuntimeError, "L3"):
                network.http_get(PORTAL_IPV4_ORIGIN, bind_ip=WIRED_BIND_IP, strict=True)
        fetch.assert_not_called()

    def test_http_preserves_explicit_device_instead_of_reverse_lookup(self):
        with (
            mock.patch.object(network, "HAVE_URLLIB", True),
            mock.patch.object(network, "validate_ip_device_binding"),
            mock.patch.object(
                network,
                "get_network_device_for_ip",
                side_effect=AssertionError("wrong device"),
            ),
            mock.patch.object(
                network, "_http_get_via_stdlib", return_value=("bound", 200)
            ) as fetch,
        ):
            result = network.http_get(
                PORTAL_IPV4_ORIGIN,
                bind_ip=WIRED_BIND_IP,
                bind_device="eth2",
                strict=True,
            )
        self.assertEqual(result, "bound")
        fetch.assert_called_once_with(
            PORTAL_IPV4_ORIGIN,
            5,
            WIRED_BIND_IP,
            bind_device="eth2",
            strict=True,
            bind_iface=None,
        )

    def test_stale_source_owned_by_other_device_is_rejected_before_http(self):
        # Linux socket.bind accepts an address on any interface. WAN B has
        # changed DHCP address while WAN A still owns the cached old address.
        address = f"3: eth2 inet {BIND_IP}/24 scope global eth2"
        with (
            mock.patch.object(
                network, "run_cmd", return_value=(True, address)
            ) as command,
            mock.patch.object(network, "_http_get_via_stdlib") as fetch,
        ):
            with self.assertRaisesRegex(RuntimeError, "IPv4"):
                network.http_get(
                    PORTAL_IPV4_ORIGIN,
                    bind_ip=WIRED_BIND_IP,
                    bind_device="eth2",
                    strict=True,
                )
        command.assert_called_once_with(
            ["ip", "-4", "-o", "addr", "show", "dev", "eth2"], timeout=5
        )
        fetch.assert_not_called()

    def test_strict_http_dns_is_resolved_on_selected_interface(self):
        sock = mock.Mock()
        with (
            mock.patch.object(network, "validate_ip_device_binding"),
            mock.patch.object(
                network, "_resolve_probe_ips", return_value=[PORTAL_IPV4_HOST]
            ) as resolve,
            mock.patch.object(network.socket, "socket", return_value=sock),
            mock.patch.object(network.socket, "getaddrinfo") as global_dns,
        ):
            network._create_bound_connection(
                (PORTAL_DNS_NAME, 80),
                2,
                (WIRED_BIND_IP, 0),
                "eth2",
                strict=True,
                bind_iface="wan2",
            )
        resolve.assert_called_once_with(
            PORTAL_DNS_NAME,
            2,
            bind_ip=WIRED_BIND_IP,
            bind_device="eth2",
            iface="wan2",
            strict=True,
        )
        sock.connect.assert_called_once_with((PORTAL_IPV4_HOST, 80))
        global_dns.assert_not_called()

    def test_strict_http_without_interface_dns_cannot_use_global_resolver(self):
        with (
            mock.patch.object(network, "validate_ip_device_binding"),
            mock.patch.object(network, "_uplink_dns_servers", return_value=[]),
            mock.patch.object(network.socket, "getaddrinfo") as global_dns,
            mock.patch.object(network.socket, "socket") as socket_factory,
        ):
            with self.assertRaisesRegex(OSError, "DNS"):
                network._create_bound_connection(
                    (PORTAL_DNS_NAME, 80),
                    2,
                    (WIRED_BIND_IP, 0),
                    "eth2",
                    strict=True,
                    bind_iface="wan2",
                )
        global_dns.assert_not_called()
        socket_factory.assert_not_called()

    def test_socket_binding_error_prevents_connect_and_credentials(self):
        sock = mock.Mock()
        sock.setsockopt.side_effect = OSError("operation not permitted")
        with (
            mock.patch.object(network.socket, "socket", return_value=sock),
            mock.patch.object(
                network.socket,
                "getaddrinfo",
                return_value=[
                    (
                        network.socket.AF_INET,
                        network.socket.SOCK_STREAM,
                        6,
                        "",
                        (PORTAL_IPV4_HOST, 80),
                    )
                ],
            ),
        ):
            with self.assertRaises(OSError):
                network._create_bound_connection(
                    (PORTAL_IPV4_HOST, 80), 2, (WIRED_BIND_IP, 0), "eth2"
                )
        sock.connect.assert_not_called()
        sock.sendall.assert_not_called()
        sock.close.assert_called_once()

    def test_ip_only_lookup_rejects_two_owning_devices(self):
        addresses = (
            f"2: eth1 inet {WIRED_BIND_IP}/24 scope global eth1\n"
            f"3: eth2 inet {WIRED_BIND_IP}/24 scope global eth2\n"
        )
        with mock.patch.object(network, "run_cmd", return_value=(True, addresses)):
            self.assertIsNone(network.get_network_device_for_ip(WIRED_BIND_IP))

    def test_device_bound_http_without_stdlib_cannot_use_source_only_wget(self):
        with (
            mock.patch.object(network, "HAVE_URLLIB", False),
            mock.patch.object(
                network, "get_network_device_for_ip", return_value="eth2"
            ),
            mock.patch.object(network.os.path, "exists", return_value=True),
            mock.patch.object(network, "_wget_supports_bind", return_value=True),
            mock.patch.object(
                network.subprocess, "check_output", return_value=b"unsafe"
            ) as fetch,
        ):
            with self.assertRaises(RuntimeError):
                network.http_get(PORTAL_IPV4_ORIGIN, bind_ip=WIRED_BIND_IP)
        fetch.assert_not_called()


class SnapshotBindingTests(unittest.TestCase):
    def test_connectivity_cache_cannot_cross_accounts_with_the_same_ip(self):
        cfg = {
            "campus_access_mode": "wired",
            "wired_iface": "wan2",
            "active_campus_id": "account2",
            "username": "user2",
        }
        previous = {
            "current_ip": WIRED_BIND_IP,
            "current_iface": "wan1",
            "active_campus_id": "account1",
            "connectivity_level": "online",
            "connectivity": "互联网可达",
            "connectivity_checked_at": int(time.time()),
        }
        runtime = mock.Mock()
        runtime.runtime_api_version = 1
        runtime.query_online_identity.return_value = (False, "", {})
        with (
            mock.patch.object(
                snapshot, "build_app_context", return_value={"runtime": runtime}
            ),
            mock.patch.object(snapshot, "parse_wireless_iface_data", return_value={}),
            mock.patch.object(snapshot, "get_runtime_sta_section", return_value=None),
            mock.patch.object(
                snapshot, "get_ipv4_from_network_interface", return_value=WIRED_BIND_IP
            ),
            mock.patch.object(
                snapshot, "test_internet_connectivity", return_value=(False, "offline")
            ) as probe,
            mock.patch.object(
                snapshot, "test_portal_reachability", return_value=(False, "offline")
            ),
        ):
            result = snapshot.build_runtime_snapshot(cfg, previous)
        probe.assert_called_once()
        self.assertEqual(result["connectivity_level"], "limited")

    def test_strict_snapshot_scopes_both_probes_and_online_identity(self):
        cfg = {
            "campus_access_mode": "wired",
            "wired_iface": "wan2",
            "base_url": PORTAL_IPV4_ORIGIN,
            "active_campus_id": "account2",
            "username": "user2",
            "_multi_wan_strict_bind": "1",
        }
        binding = {"bind_ip": WIRED_BIND_IP, "bind_device": "eth2", "strict": True}
        runtime = mock.Mock(runtime_api_version=1)
        runtime.query_online_identity.return_value = (False, "", {})
        app = {"runtime": runtime}
        with (
            mock.patch.object(snapshot, "build_app_context", return_value=app),
            mock.patch.object(snapshot, "parse_wireless_iface_data", return_value={}),
            mock.patch.object(snapshot, "get_runtime_sta_section", return_value=None),
            mock.patch.object(
                snapshot, "get_ipv4_from_network_interface", return_value=WIRED_BIND_IP
            ),
            mock.patch.object(snapshot, "resolve_http_binding", return_value=binding),
            mock.patch.object(
                snapshot, "test_internet_connectivity", return_value=(False, "portal")
            ) as internet,
            mock.patch.object(
                snapshot, "test_portal_reachability", return_value=(True, "")
            ) as portal,
        ):
            state = snapshot.build_runtime_snapshot(cfg, {})
            cached = snapshot.build_runtime_snapshot(cfg, state)
        internet.assert_called_once_with(timeout=2, iface="wan2", **binding)
        portal.assert_called_once_with(cfg, timeout=2, **binding)
        self.assertEqual(runtime.query_online_identity.call_count, 2)
        self.assertTrue(
            all(
                call.kwargs.get("bind_ip") == WIRED_BIND_IP
                for call in runtime.query_online_identity.call_args_list
            )
        )
        self.assertEqual(app["_http_binding"], binding)
        self.assertEqual(cached["connectivity_level"], "portal")
        self.assertTrue(snapshot.cached_connectivity_matches(cfg, state))
        for field, value in [
            ("active_campus_id", "other"),
            ("username", "other"),
            ("wired_iface", "wan3"),
            ("base_url", PORTAL_BARE_ORIGIN),
        ]:
            with self.subTest(field=field):
                self.assertFalse(
                    snapshot.cached_connectivity_matches(
                        dict(cfg, **{field: value}), state
                    )
                )
        self.assertFalse(
            snapshot.cached_connectivity_matches(
                cfg, state, now=state["connectivity_checked_at"] + 999
            )
        )

    def test_strict_wired_failure_does_not_probe_existing_wifi(self):
        cfg = {
            "campus_access_mode": "wired",
            "wired_iface": "wan2",
            "username": "user2",
            "_multi_wan_strict_bind": "1",
        }
        runtime = mock.Mock(runtime_api_version=1)
        with (
            mock.patch.object(
                snapshot, "build_app_context", return_value={"runtime": runtime}
            ),
            mock.patch.object(snapshot, "parse_wireless_iface_data", return_value={}),
            mock.patch.object(snapshot, "get_runtime_sta_section", return_value="sta1"),
            mock.patch.object(
                snapshot, "get_sta_profile_from_section", return_value={"ssid": "other"}
            ),
            mock.patch.object(
                snapshot, "get_network_interface_from_sta_section", return_value="wwan"
            ),
            mock.patch.object(
                snapshot, "get_ipv4_from_network_interface", return_value=CLIENT_IP
            ),
            mock.patch.object(
                snapshot,
                "resolve_http_binding",
                side_effect=RuntimeError("wan2 missing"),
            ),
            mock.patch.object(snapshot, "test_internet_connectivity") as internet,
            mock.patch.object(snapshot, "test_portal_reachability") as portal,
        ):
            state = snapshot.build_runtime_snapshot(cfg, {})
        self.assertEqual(state["current_ip"], "")
        self.assertEqual(state["current_iface"], "wan2")
        self.assertEqual(state["connectivity_level"], "offline")
        internet.assert_not_called()
        portal.assert_not_called()
        runtime.query_online_identity.assert_not_called()


if __name__ == "__main__":
    unittest.main()
