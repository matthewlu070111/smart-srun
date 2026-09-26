package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	code, ok := domain.CodeOf(err)
	if !ok {
		t.Fatalf("error carries no code: %v", err)
	}
	return code
}

// loopbackBinding describes a line that really exists on every machine, so the
// source-address half of the binding can be exercised against real sockets
// without privileges. The device half needs CAP_NET_RAW and is covered by the
// guest run.
func loopbackBinding() domain.Binding {
	return domain.Binding{
		LogicalIface: "wan",
		L3Device:     "lo",
		IfIndex:      1,
		SourceIPv4:   netip.MustParseAddr("127.0.0.1"),
		Generation:   7,
	}
}

// noControl stands in for the device binding on a machine that will not allow
// it. It records what was asked for, so the test can still check that the right
// device would have been pinned.
func noControl(recorded *[]string) func(string) func(string, string, syscall.RawConn) error {
	return func(device string) func(string, string, syscall.RawConn) error {
		return func(string, string, syscall.RawConn) error {
			*recorded = append(*recorded, device)
			return nil
		}
	}
}

// T14 -- the source address is really applied to the socket.
//
// Not "the dialer was configured with it": the connection is made and the
// address the kernel reports is read back. A source address that is configured
// but not used is the failure this is for.
func TestTheConnectionLeavesFromTheBindingsAddress(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	var devices []string
	dialer := NewDialer(loopbackBinding())
	dialer.control = noControl(&devices)

	conn, err := dialer.DialContext(t.Context(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("local address is %T", conn.LocalAddr())
	}
	if !local.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("connected from %v, want the binding's address", local.IP)
	}
	if len(devices) != 1 || devices[0] != "lo" {
		t.Errorf("devices pinned = %v, want exactly [lo]", devices)
	}
}

// T14 -- both halves are required, so a device that cannot be pinned refuses
// the connection instead of sending it by whatever route exists.
func TestAFailureToPinTheDeviceRefusesTheConnection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	dialer := NewDialer(loopbackBinding())
	dialer.control = func(string) func(string, string, syscall.RawConn) error {
		return func(string, string, syscall.RawConn) error {
			return syscall.EPERM
		}
	}

	conn, err := dialer.DialContext(t.Context(), "tcp", listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("a connection was made without the device binding; it would " +
			"have left by the default route")
	}
	if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
		t.Errorf("code = %s, want BindingUnavailable", code)
	}
}

// A line that has not resolved is refused before a socket is created, so the
// caller is told why rather than getting a syscall error from deeper down.
func TestAnUnreadyBindingNeverOpensASocket(t *testing.T) {
	cases := map[string]domain.Binding{
		"nothing at all": {},
		"no device":      {LogicalIface: "wan", SourceIPv4: netip.MustParseAddr("127.0.0.1")},
		"no address":     {LogicalIface: "wan", L3Device: "lo"},
		"an IPv6 address": {LogicalIface: "wan", L3Device: "lo",
			SourceIPv4: netip.MustParseAddr("::1")},
	}
	for name, binding := range cases {
		t.Run(name, func(t *testing.T) {
			var devices []string
			dialer := NewDialer(binding)
			dialer.control = noControl(&devices)

			_, err := dialer.DialContext(t.Context(), "tcp", "127.0.0.1:9")
			if err == nil {
				t.Fatal("an unusable binding produced a connection")
			}
			if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
				t.Errorf("code = %s, want BindingUnavailable", code)
			}
			if len(devices) != 0 {
				t.Errorf("a socket was created for an unusable binding: %v", devices)
			}
		})
	}
}

// The binding describes an IPv4 line. Dialing v6 would leave by whatever route
// the system chose, which is the whole thing this package prevents.
func TestAnIPv6NetworkIsRefusedRatherThanLeavingUnbound(t *testing.T) {
	dialer := NewDialer(loopbackBinding())
	var devices []string
	dialer.control = noControl(&devices)

	for _, network := range []string{"tcp6", "udp6", "unix", "ip4:icmp"} {
		_, err := dialer.DialContext(t.Context(), network, "[::1]:9")
		if err == nil {
			t.Errorf("%s was dialed from an IPv4-only binding", network)
			continue
		}
		if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
			t.Errorf("%s: code = %s, want BindingUnavailable", network, code)
		}
	}
	if len(devices) != 0 {
		t.Errorf("a socket was created: %v", devices)
	}
}

// A gateway that does not answer is a transport failure, not a binding one.
// Telling the user their interface is misconfigured when they are simply not on
// the campus network sends them after the wrong thing.
func TestAnUnreachableGatewayIsNotABindingProblem(t *testing.T) {
	var devices []string
	dialer := NewDialer(loopbackBinding())
	dialer.control = noControl(&devices)

	// Port 9 on loopback: discard, and nothing is listening.
	_, err := dialer.DialContext(t.Context(), "tcp", "127.0.0.1:9")
	if err == nil {
		t.Skip("something is listening on the discard port here")
	}
	if code := codeOf(t, err); code != domain.CodeTransportFailure {
		t.Errorf("code = %s, want TransportFailure", code)
	}
}

// A failure message must not repeat the address it was given. Callers build
// those from configuration, and a query string appended to a base URL would
// travel with the error into a log.
func TestAFailureMessageCarriesOnlyTheHost(t *testing.T) {
	var devices []string
	dialer := NewDialer(loopbackBinding())
	dialer.control = noControl(&devices)

	_, err := dialer.DialContext(t.Context(), "tcp", "127.0.0.1:9")
	if err == nil {
		t.Skip("something is listening on the discard port here")
	}
	if got := err.Error(); !contains(got, "127.0.0.1") {
		t.Errorf("the message does not name the host: %s", got)
	}
	if got := err.Error(); contains(got, ":9") {
		t.Errorf("the message repeats the port and could repeat more: %s", got)
	}
}

func contains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Cancellation has to reach the dial, or a user action waits for the full
// connect timeout.
func TestCancellingStopsADialInProgress(t *testing.T) {
	var devices []string
	dialer := NewDialer(loopbackBinding())
	dialer.control = noControl(&devices)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:9")
	if err == nil {
		t.Fatal("a cancelled dial produced a connection")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; the cancellation should be in the chain", err)
	}
}
