//go:build linux

package transport

import (
	"net"
	"syscall"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Named so the table below reads as the conditions rather than as numbers.
const (
	syscallEPERM         = syscall.EPERM
	syscallEADDRNOTAVAIL = syscall.EADDRNOTAVAIL
	syscallENODEV        = syscall.ENODEV
	syscallEADDRINUSE    = syscall.EADDRINUSE
	syscallECONNREFUSED  = syscall.ECONNREFUSED
)

// The real setsockopt, not the injected stand-in.
//
// What this can show on an ordinary host is the refusal path. The success path
// needs a device matching the test host's routing. On WSL mirrored networking,
// TCP to 127.0.0.1 goes through loopback0; binding lo succeeds but times out.
// That is not a general Linux loopback limitation. The daemon's preset tests
// exercise real bound HTTP on the appropriate loopback device. A test expecting
// EPERM would also be asserting a privilege rule this kernel does not apply.
//
// The positive proof -- that a pinned socket leaves by that device and no
// other -- needs two real devices and a peer on each, which is what the network
// namespace test in the guest is for. This file covers what is true everywhere.

// A device that does not exist cannot be pinned, and the connection must not be
// made. This is the failure that matters: if it were ignored, the socket would
// leave by whatever route the table chose.
func TestPinningADeviceThatDoesNotExistFails(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	binding := loopbackBinding()
	binding.L3Device = "nosuchdev0"

	conn, err := NewDialer(binding).DialContext(
		t.Context(), "tcp", listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("a connection was made while pinned to a device that does " +
			"not exist; it cannot have been pinned at all")
	}
	if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
		t.Errorf("code = %s, want BindingUnavailable", code)
	}
	if got := err.Error(); !contains(got, "设备") {
		t.Errorf("the message does not point at the device: %s", got)
	}
}

// Every errno the binding path can produce has to map to a binding failure
// rather than falling through to "the gateway did not answer". The two are
// different problems with different advice, and only one of them is the user's
// network being absent.
func TestEveryBindingErrnoIsRecognised(t *testing.T) {
	cases := map[string]error{
		"no permission":  syscallEPERM,
		"address gone":   syscallEADDRNOTAVAIL,
		"device gone":    syscallENODEV,
		"address in use": syscallEADDRINUSE,
	}
	for name, err := range cases {
		if bindingFailure(err) == "" {
			t.Errorf("%s (%v) was not recognised as a binding failure", name, err)
		}
	}

	// And something that is not a binding problem must not be claimed as one.
	if reason := bindingFailure(syscallECONNREFUSED); reason != "" {
		t.Errorf("a refused connection was reported as a binding failure: %s",
			reason)
	}
}
