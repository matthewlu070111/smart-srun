import os
import struct
import sys
import unittest
from unittest import mock


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")
THIS_DIR = os.path.dirname(os.path.abspath(__file__))

for path in (THIS_DIR, MODULE_ROOT):
    if path not in sys.path:
        sys.path.insert(0, path)


from _portal_urls import (  # noqa: E402
    CONNECTIVITY_PROBE_URL,
    DNS_SERVER_IP,
    PORTAL_ACID9_PAGE_URL,
    PORTAL_BARE_HOST,
    PORTAL_DNS_NAME,
    PORTAL_IPV4_HOST,
    PORTAL_IPV4_ORIGIN,
    PORTAL_IPV4_PORT,
    PORTAL_ORIGIN,
    PORTAL_PORT,
    WIRED_BIND_IP,
)
import network  # noqa: E402  (依赖上方 sys.path 注入，与其余测试文件一致)


class InternetConnectivityProbeTests(unittest.TestCase):
    """连通性探测：裸 socket 单次请求 + 真实 204 状态码判定。

    刻意不用 urllib/http.client（OpenWrt python3-light 缺 unicodedata 时
    idna 编解码器不可用，stdlib 对字符串主机名的 getaddrinfo 直接抛
    LookupError），也不落 wget/uclient-fetch 兜底链（被墙时串行拖满超时）。
    """

    def test_http_204_means_online(self):
        with mock.patch.object(
            network, "_probe_http_status", return_value=204
        ) as probe:
            ok, message = network.test_internet_connectivity(timeout=2)
        self.assertTrue(ok)
        self.assertEqual(message, "")
        # 第一个 URL 就返回 204，不应再探测后续 URL。
        self.assertEqual(probe.call_count, 1)

    def test_redirect_status_means_portal_hijack(self):
        with mock.patch.object(network, "_probe_http_status", return_value=302):
            ok, message = network.test_internet_connectivity(timeout=2)
        self.assertFalse(ok)
        self.assertIn("认证页面", message)

    def test_portal_page_200_means_portal_hijack(self):
        with mock.patch.object(network, "_probe_http_status", return_value=200):
            ok, message = network.test_internet_connectivity(timeout=2)
        self.assertFalse(ok)
        self.assertIn("认证页面", message)

    def test_all_urls_unreachable_reports_failure(self):
        with mock.patch.object(
            network, "_probe_http_status", side_effect=OSError("timed out")
        ) as probe:
            ok, message = network.test_internet_connectivity(timeout=2)
        self.assertFalse(ok)
        self.assertIn("无法访问", message)
        self.assertEqual(probe.call_count, len(network.CONNECTIVITY_CHECK_URLS))

    def test_probe_never_touches_stdlib_http_or_subprocess_fallback(self):
        # 探测既不能依赖 urllib/http.client（python3-light 缺 idna 编解码器时
        # 会 LookupError），也不能落入 http_get 的 wget/uclient-fetch 兜底链
        # （外网被墙时串行拖满多个硬超时，把登录后的终态校验拖死）。
        with (
            mock.patch.object(
                network, "_probe_http_status", side_effect=OSError("unreachable")
            ),
            mock.patch.object(network, "http_get") as legacy_get,
            mock.patch.object(network, "_http_get_via_stdlib") as stdlib_get,
        ):
            network.test_internet_connectivity(timeout=2)
        legacy_get.assert_not_called()
        stdlib_get.assert_not_called()

    def test_probe_resolves_hostname_as_bytes_to_bypass_idna_codec(self):
        # bytes 主机名走 C 解析器；str 主机名在缺 unicodedata 的设备上会
        # 因 idna 编解码器不可用抛 LookupError('unknown encoding: idna')。
        captured = {}

        def fake_getaddrinfo(host, port, *args, **kwargs):
            captured["host"] = host
            raise OSError("stop before real network IO")

        with mock.patch.object(network.socket, "getaddrinfo", fake_getaddrinfo):
            with self.assertRaises(OSError):
                network._probe_http_status(CONNECTIVITY_PROBE_URL, timeout=1)
        self.assertIsInstance(captured["host"], bytes)

    def test_dns_query_a_parses_compressed_answer(self):
        # 构造带压缩指针的标准 DNS 应答，验证裸 UDP 查询的解析器。
        captured = {}

        class FakeSock:
            def __init__(self, *args, **kwargs):
                pass

            def settimeout(self, timeout):
                pass

            def sendto(self, packet, addr):
                captured["txid"] = packet[:2]
                captured["addr"] = addr

            def recvfrom(self, size):
                resp = captured["txid"] + struct.pack(">HHHHH", 0x8180, 1, 1, 0, 0)
                resp += b"\x01a\x04test\x00" + struct.pack(">HH", 1, 1)  # question
                resp += b"\xc0\x0c" + struct.pack(">HHIH", 1, 1, 60, 4)
                resp += network.socket.inet_aton(PORTAL_IPV4_HOST)
                return resp, (DNS_SERVER_IP, 53)

            def close(self):
                pass

        with mock.patch.object(network.socket, "socket", FakeSock):
            ips = network._dns_query_a("a.test", DNS_SERVER_IP, timeout=1)

        self.assertEqual([PORTAL_IPV4_HOST], ips)
        self.assertEqual((DNS_SERVER_IP, 53), captured["addr"])

    def test_resolve_probe_ips_returns_ip_literal_directly(self):
        with mock.patch.object(network, "_uplink_dns_servers") as uplink:
            ips = network._resolve_probe_ips(PORTAL_BARE_HOST, timeout=2)
        self.assertEqual([PORTAL_BARE_HOST], ips)
        uplink.assert_not_called()

    def test_split_http_url_variants(self):
        self.assertEqual(
            (PORTAL_DNS_NAME, PORTAL_PORT, "/generate_204"),
            network._split_http_url(CONNECTIVITY_PROBE_URL),
        )
        self.assertEqual(
            (PORTAL_BARE_HOST, 8080, "/probe"),
            network._split_http_url("http://%s:8080/probe" % PORTAL_BARE_HOST),
        )
        self.assertEqual(
            (PORTAL_DNS_NAME, PORTAL_PORT, "/"),
            network._split_http_url(PORTAL_ORIGIN),
        )

    def test_strict_probe_binds_tcp_to_selected_source_and_device(self):
        sock = mock.Mock()
        sock.recv.return_value = b"HTTP/1.1 204 No Content\r\n"
        with (
            mock.patch.object(network, "validate_ip_device_binding"),
            mock.patch.object(network.socket, "socket", return_value=sock),
        ):
            status = network._probe_http_status(
                PORTAL_IPV4_ORIGIN + "/generate_204",
                2,
                bind_ip=WIRED_BIND_IP,
                bind_device="eth2",
                iface="wan2",
                strict=True,
            )
        self.assertEqual(status, 204)
        sock.bind.assert_called_once_with((WIRED_BIND_IP, 0))
        sock.setsockopt.assert_called_once_with(
            network.socket.SOL_SOCKET,
            getattr(network.socket, "SO_BINDTODEVICE", 25),
            b"eth2\0",
        )
        sock.connect.assert_called_once_with((PORTAL_IPV4_HOST, PORTAL_IPV4_PORT))

    def test_strict_dns_uses_selected_interface_and_cannot_fall_back_to_local_dns(self):
        with (
            mock.patch.object(
                network, "_uplink_dns_servers", return_value=[DNS_SERVER_IP]
            ) as servers,
            mock.patch.object(
                network, "_dns_query_a", side_effect=OSError("DNS blocked")
            ) as query,
            mock.patch.object(network.socket, "getaddrinfo") as fallback,
        ):
            with self.assertRaises(OSError):
                network._resolve_probe_ips(
                    PORTAL_DNS_NAME,
                    2,
                    bind_ip=WIRED_BIND_IP,
                    bind_device="eth2",
                    iface="wan2",
                    strict=True,
                )
        servers.assert_called_once_with(iface="wan2", strict=True)
        query.assert_called_once_with(
            PORTAL_DNS_NAME,
            DNS_SERVER_IP,
            1.0,
            bind_ip=WIRED_BIND_IP,
            bind_device="eth2",
            strict=True,
        )
        fallback.assert_not_called()

    def test_strict_dns_binding_failure_never_sends_unbound_packet(self):
        sock = mock.Mock()
        sock.setsockopt.side_effect = OSError("permission denied")
        with (
            mock.patch.object(network, "validate_ip_device_binding"),
            mock.patch.object(network.socket, "socket", return_value=sock),
        ):
            with self.assertRaises(OSError):
                network._dns_query_a(
                    PORTAL_DNS_NAME,
                    DNS_SERVER_IP,
                    1,
                    bind_ip=WIRED_BIND_IP,
                    bind_device="eth2",
                    strict=True,
                )
        sock.sendto.assert_not_called()
        sock.close.assert_called_once()

    def test_portal_probe_uses_full_strict_binding(self):
        cfg = {
            "base_url": PORTAL_IPV4_ORIGIN,
            "campus_access_mode": "wired",
            "wired_iface": "wan2",
            "_multi_wan_strict_bind": "1",
        }
        binding = {"bind_ip": WIRED_BIND_IP, "bind_device": "eth2", "strict": True}
        with (
            mock.patch.object(network, "resolve_http_binding", return_value=binding),
            mock.patch.object(network, "http_get", return_value="portal") as fetch,
        ):
            self.assertEqual(
                network.test_portal_reachability(cfg, timeout=2), (True, "")
            )
        fetch.assert_called_once_with(cfg["base_url"], timeout=2, **binding)


class CaptivePortalProbeTests(unittest.TestCase):
    """强制门户探测：302 的 Location 就是本校认证页地址。

    守护进程的连通性探测只要状态码，每 tick 都跑；门户嗅探是一次性的、要多读
    到响应头结束。两者共用 socket 逻辑但不能共用读取预算，下面第一组测试就是
    钉住这条边界。
    """

    def _socket_returning(self, chunks):
        sock = mock.Mock()
        sock.recv.side_effect = list(chunks) + [b""]
        return sock

    def _probe(self, sock, **kwargs):
        with (
            mock.patch.object(network, "validate_ip_device_binding"),
            mock.patch.object(network.socket, "socket", return_value=sock),
        ):
            return network._probe_http_head(
                PORTAL_IPV4_ORIGIN + "/generate_204", 2, **kwargs
            )

    def test_status_only_probe_stops_at_the_first_crlf(self):
        # 守护进程热路径的读取预算不能因为新增门户嗅探而变大。
        sock = self._socket_returning([
            b"HTTP/1.1 204 No Content\r\n",
            b"Server: should-not-be-read\r\n\r\n",
        ])
        with (
            mock.patch.object(network, "validate_ip_device_binding"),
            mock.patch.object(network.socket, "socket", return_value=sock),
        ):
            status = network._probe_http_status(PORTAL_IPV4_ORIGIN + "/generate_204", 2)
        self.assertEqual(status, 204)
        self.assertEqual(sock.recv.call_count, 1)

    def test_header_probe_reads_until_headers_end_and_exposes_location(self):
        sock = self._socket_returning([
            b"HTTP/1.1 302 Found\r\n",
            b"Location: " + PORTAL_ACID9_PAGE_URL.encode("ascii") + b"\r\n",
            b"Content-Length: 0\r\n\r\n",
        ])
        status, headers = self._probe(sock, want_headers=True)
        self.assertEqual(status, 302)
        self.assertEqual(headers["location"], PORTAL_ACID9_PAGE_URL)
        self.assertEqual(headers["content-length"], "0")

    def test_header_keys_are_lowercased_and_malformed_status_still_raises(self):
        sock = self._socket_returning([b"HTTP/1.1 302 Found\r\nLOCATION: /x\r\n\r\n"])
        self.assertEqual(self._probe(sock, want_headers=True)[1]["location"], "/x")

        bad = self._socket_returning([b"not-a-status-line\r\n\r\n"])
        with self.assertRaises(ValueError):
            self._probe(bad, want_headers=True)

    def test_captive_probe_reports_online_without_a_portal_url(self):
        with mock.patch.object(network, "_probe_http_head", return_value=(204, {})):
            result = network.probe_captive_portal(timeout=2)
        self.assertEqual(result["state"], "online")
        self.assertEqual(result["location"], "")

    def test_captive_probe_hands_back_the_redirect_target(self):
        with mock.patch.object(
            network, "_probe_http_head",
            return_value=(302, {"location": PORTAL_ACID9_PAGE_URL}),
        ) as probe:
            result = network.probe_captive_portal(timeout=2)
        self.assertEqual(result["state"], "portal")
        self.assertEqual(result["location"], PORTAL_ACID9_PAGE_URL)
        self.assertEqual(result["status_code"], 302)
        self.assertTrue(probe.call_args.kwargs["want_headers"])

    def test_captive_probe_reports_intercepted_without_location(self):
        # 部分门户直接回 200 + HTML 跳转：仍算被拦截，交给上层读正文。
        with mock.patch.object(network, "_probe_http_head", return_value=(200, {})):
            result = network.probe_captive_portal(timeout=2)
        self.assertEqual(result["state"], "portal")
        self.assertEqual(result["location"], "")

    def test_captive_probe_reports_down_after_every_url_fails(self):
        with mock.patch.object(
            network, "_probe_http_head", side_effect=OSError("unreachable")
        ) as probe:
            result = network.probe_captive_portal(timeout=2)
        self.assertEqual(result["state"], "down")
        self.assertEqual(probe.call_count, len(network.CONNECTIVITY_CHECK_URLS))
        self.assertIn("unreachable", result["message"])


if __name__ == "__main__":
    unittest.main()
