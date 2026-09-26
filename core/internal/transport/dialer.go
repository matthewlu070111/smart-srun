package transport

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Dialer opens connections that can leave only by one line.
//
// Two things are applied to every socket, and spec 04 requires both to succeed
// before anything is sent:
//
//   - the line's address as the source, so the packet carries the identity the
//     gateway will authenticate;
//   - SO_BINDTODEVICE on the line's device, so the routing table cannot send it
//     somewhere else.
//
// Either alone is insufficient. A source address without the device binding
// still follows the default route when the routing table prefers another line,
// which on a campus with overlapping private subnets silently authenticates the
// wrong one. A device binding without the source address sends packets out of
// the right interface carrying an address that does not belong to it.
type Dialer struct {
	Binding domain.Binding

	// Timeout bounds one connection attempt. Zero means DialTimeout.
	Timeout time.Duration

	// control is the hook that pins the device. It is a field so a test can
	// observe what was asked for, and so the failure path can be exercised on a
	// machine where the real call needs privileges it does not have.
	control func(device string) func(network, address string, c syscall.RawConn) error
}

// NewDialer builds a dialer for one binding.
func NewDialer(binding domain.Binding) *Dialer {
	return &Dialer{Binding: binding, control: bindToDevice}
}

func (d *Dialer) timeout() time.Duration {
	if d.Timeout <= 0 {
		return DialTimeout
	}
	return d.Timeout
}

// DialContext opens a connection from this binding.
//
// A binding that is not usable is refused here rather than at connect time, so
// the caller gets BindingUnavailable with the reason instead of a syscall error
// from somewhere deeper.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !d.Binding.Ready() {
		return nil, domain.FieldErrorf(domain.CodeBindingUnavailable,
			"wired_iface", "线路 %s 尚未就绪，拒绝发出请求",
			d.Binding.LogicalIface)
	}

	local, err := d.localAddr(network)
	if err != nil {
		return nil, err
	}

	control := d.control
	if control == nil {
		control = bindToDevice
	}

	dialer := &net.Dialer{
		LocalAddr: local,
		Timeout:   d.timeout(),
		Control:   control(d.Binding.L3Device),
		// The keep-alive is left at the Go default rather than disabled: a
		// half-open connection to a gateway that vanished is otherwise only
		// noticed by a timeout on the next request.
	}

	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, d.classify(err, address)
	}
	return conn, nil
}

// localAddr is the source address, in the shape the network wants.
func (d *Dialer) localAddr(network string) (net.Addr, error) {
	source := net.IP(d.Binding.SourceIPv4.AsSlice())
	switch network {
	case "tcp", "tcp4":
		return &net.TCPAddr{IP: source}, nil
	case "udp", "udp4":
		return &net.UDPAddr{IP: source}, nil
	default:
		// Including tcp6 and udp6: this binding describes an IPv4 line, and
		// silently dialing v6 would leave by whatever route the system picked.
		return nil, domain.Errorf(domain.CodeBindingUnavailable,
			"线路 %s 只提供 IPv4，不能用于 %s", d.Binding.LogicalIface, network)
	}
}

// classify turns a dial failure into an answer the interface can act on.
//
// The distinction that matters is between "this line cannot carry the request"
// and "the far end did not answer". The first is a local problem the user may
// be able to fix; the second usually means they are not on the campus network
// yet, and telling them to check their interface configuration would send them
// after the wrong thing.
func (d *Dialer) classify(err error, address string) error {
	if binding := bindingFailure(err); binding != "" {
		return domain.FieldErrorf(domain.CodeBindingUnavailable, "wired_iface",
			"线路 %s 无法用于发送请求：%s", d.Binding.LogicalIface, binding).Wrap(err)
	}
	return domain.Errorf(domain.CodeTransportFailure,
		"无法连接认证网关 %s", hostOnly(address)).Wrap(err)
}

// hostOnly drops the port, and never returns the whole address string, so a
// message cannot carry a query that a caller appended to it.
func hostOnly(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}

// SourceAddr reports the address this dialer binds to, for the callers that
// have to put it on the wire -- SRun's login parameters include the client
// address, and it has to be the one the socket actually used.
func (d *Dialer) SourceAddr() netip.Addr { return d.Binding.SourceIPv4 }
