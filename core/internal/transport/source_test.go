package transport

import (
	"net"
	"net/netip"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The source address is really bound, not merely configured.
//
// Loopback alone cannot show this: a connection to 127.0.0.1 sources from
// 127.0.0.1 whether or not anything asked, so a test built on that passes even
// when the binding is dropped. Two other ideas also failed to discriminate --
// binding a non-loopback source and reaching loopback works on Linux 6.6, and
// so does connecting to the machine's own address from 127.0.0.1, because both
// go through the loopback path internally.
//
// What does discriminate is an address the machine does not have. bind() to it
// fails with EADDRNOTAVAIL every time, and a wildcard source succeeds every
// time, so the two are never confusable. It is also the realistic case: an
// address the host no longer holds is exactly what a binding looks like after
// DHCP has moved on.
func TestTheSourceAddressIsAppliedAndNotJustConfigured(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	binding := loopbackBinding()
	// TEST-NET-1. Reserved for documentation, so it is not on this machine and
	// will not become so.
	binding.SourceIPv4 = netip.MustParseAddr("192.0.2.99")

	var devices []string
	dialer := NewDialer(binding)
	dialer.control = noControl(&devices)

	conn, err := dialer.DialContext(t.Context(), "tcp", listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("a socket sourced from an address this machine does not have " +
			"connected anyway; the source was not applied and the request " +
			"left with a wildcard")
	}
	// The refusal above is the guarantee and holds everywhere. Which code it
	// carries depends on the platform's errno numbers, which are only mapped
	// for the target.
	if !bindingErrnosClassified {
		return
	}
	if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
		t.Errorf("code = %s, want BindingUnavailable: an address the host has "+
			"given up is a binding problem, not an unreachable gateway", code)
	}
}

// And the right source still connects, so the test above is not passing merely
// because everything fails.
func TestTheCorrectSourceStillConnects(t *testing.T) {
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
		t.Fatalf("the matching source could not connect: %v", err)
	}
	conn.Close()
}
