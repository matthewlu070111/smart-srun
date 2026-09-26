//go:build linux

package transport

import (
	"errors"
	"os"
	"syscall"
)

// bindToDevice pins a socket to one network device before it connects.
//
// This is the half of the binding that the routing table cannot override.
// Setting only a source address leaves the kernel free to send the packet out
// of whichever interface the route for the destination selects, and on a campus
// with two lines on overlapping private subnets that is routinely the wrong
// one -- the request then authenticates a line the user did not choose, with
// credentials meant for the other.
//
// It needs CAP_NET_RAW. The daemon runs as root on the router; anywhere else
// this fails with EPERM, which is reported as a binding failure rather than
// silently skipped, because a socket that is not pinned is not bound.
func bindToDevice(device string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var inner error
		if err := c.Control(func(fd uintptr) {
			inner = syscall.SetsockoptString(
				int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, device)
		}); err != nil {
			return err
		}
		return inner
	}
}

// bindingErrnosClassified says the errno table below is the platform's real
// one. Off Linux the numbers differ -- Windows reports WSAEADDRNOTAVAIL rather
// than EADDRNOTAVAIL, for instance -- so a local failure there falls through to
// the generic answer. The tests assert the precise classification only where it
// is implemented, rather than pretending it holds everywhere.
const bindingErrnosClassified = true

// bindingFailure names the local reasons a connection could not be made, as
// opposed to the far end not answering.
//
// EPERM is the privilege case, EADDRNOTAVAIL is the address no longer being on
// the device -- which is what a DHCP change looks like from here -- and
// ENODEV is the device having gone away.
func bindingFailure(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EPERM):
		return "没有绑定网络设备的权限"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "源地址已不在该设备上，线路可能刚刚变化"
	case errors.Is(err, syscall.ENODEV):
		return "设备已不存在"
	case errors.Is(err, syscall.EADDRINUSE):
		return "源地址已被占用"
	default:
		return ""
	}
}
