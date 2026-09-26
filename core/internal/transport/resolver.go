package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// DNSPort is where a resolver listens. Not configurable: the servers come from
// the line's own DHCP lease, and a non-standard port there would mean something
// has gone wrong rather than that someone chose it.
const DNSPort = 53

// Resolver looks names up through one line's own resolvers, over that line.
//
// Two things spec 04 requires and the standard library will not do on its own:
//
//   - the servers are the ones the selected line reported, not whatever is in
//     /etc/resolv.conf. On a router with two lines, resolv.conf holds the
//     default line's resolvers, and a name resolved through those can point at
//     the wrong gateway entirely;
//   - both the UDP query and the TCP retry after a truncated answer go out
//     bound to the line. Resolving over the default route and then connecting
//     over the bound one still leaks the lookup, and a captive portal that
//     answers DNS differently per line is the normal case, not an exotic one.
type Resolver struct {
	dialer  *Dialer
	servers []netip.Addr
	inner   *net.Resolver

	// port is DNSPort in production. It is a field only so a test can run a
	// real resolver on an unprivileged port; nothing configures it.
	port int

	// The dial hook is called from the standard library, possibly from more
	// than one goroutine for one lookup, so everything it touches is guarded.
	mu sync.Mutex
	// attempts counts server rotations, which is what gives a second server a
	// turn when the first does not answer.
	attempts int
	// lastDialFailure keeps the error the dialer produced.
	//
	// net.DNSError has no Unwrap and keeps only a string, so a binding failure
	// handed to the standard library does not survive the trip back: the
	// caller would be told the name could not be resolved when the real
	// problem is that this line cannot send anything at all. Those two send a
	// user to completely different places, so the error is kept here where it
	// is still whole.
	lastDialFailure error
}

// NewResolver builds a resolver for one binding.
//
// It refuses a line whose only resolvers are on this machine. Spec 04 is
// explicit: a query to 127.0.0.1 leaves by whichever line the local resolver
// chose, so an answer from it is not evidence about this line. The honest
// response is to say so and ask for a resolver reachable on the line, rather
// than to resolve through the default route and present the result as if it
// had come from here.
// literalIPv4 reports whether a host is already an address, and which one.
//
// One place decides what counts as a literal, because two callers need the
// answer: the resolver, which must not query for something already numeric, and
// the client, which must be able to reach such a gateway on a line that has no
// usable DNS at all. The second bool separates "not a literal" from "a literal
// this line cannot use".
func literalIPv4(host string) (netip.Addr, bool, error) {
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, false, nil
	}
	if !address.Is4() {
		return netip.Addr{}, true, domain.Errorf(domain.CodeDNSFailure,
			"%s 不是 IPv4 地址，本线路只支持 IPv4", host)
	}
	return address, true, nil
}

func NewResolver(binding domain.Binding) (*Resolver, error) {
	usable := make([]netip.Addr, 0, len(binding.DNSServers))
	for _, server := range binding.DNSServers {
		if server.Is4() && !server.IsLoopback() && !server.IsUnspecified() {
			usable = append(usable, server)
		}
	}

	if len(usable) == 0 {
		if binding.DNSIsLoopbackOnly() {
			return nil, domain.FieldErrorf(domain.CodeDNSFailure, "wired_iface",
				"线路 %s 只提供了本机 DNS，无法证明查询从这条线路发出；"+
					"请为该接口配置一个线路上可达的 DNS 服务器",
				binding.LogicalIface)
		}
		return nil, domain.FieldErrorf(domain.CodeDNSFailure, "wired_iface",
			"线路 %s 没有可用的 DNS 服务器", binding.LogicalIface)
	}

	resolver := &Resolver{
		dialer:  NewDialer(binding),
		servers: usable,
		port:    DNSPort,
	}
	resolver.inner = &net.Resolver{
		// The pure Go resolver, so the lookup goes through the Dial hook below.
		// The cgo resolver would call the system's, which knows nothing about
		// this line.
		PreferGo:     true,
		StrictErrors: true,
		Dial:         resolver.dialToServer,
	}
	return resolver, nil
}

// dialToServer sends the query to one of the line's resolvers instead of
// wherever the standard library was going to send it.
//
// The address the resolver passes in comes from /etc/resolv.conf and is
// discarded. Rotating on each call is what gives a second server a turn when
// the first does not answer, because the standard library counts one Dial per
// attempt.
func (r *Resolver) dialToServer(ctx context.Context, network, _ string) (net.Conn, error) {
	r.mu.Lock()
	server := r.servers[r.attempts%len(r.servers)]
	r.attempts++
	port := r.port
	r.mu.Unlock()

	if port == 0 {
		port = DNSPort
	}

	// The resolver asks for "udp" first and "tcp" for the retry after a
	// truncated answer. Both go through the same bound dialer, which is the
	// property T13 is about.
	conn, err := r.dialer.DialContext(ctx, network,
		net.JoinHostPort(server.String(), strconv.Itoa(port)))
	if err != nil {
		r.mu.Lock()
		r.lastDialFailure = err
		r.mu.Unlock()
	}
	return conn, err
}

// takeDialFailure returns and clears the last dial error, so one lookup's
// failure cannot be reported against the next.
func (r *Resolver) takeDialFailure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.lastDialFailure
	r.lastDialFailure = nil
	return err
}

// LookupIPv4 resolves a host to the addresses this line can reach.
//
// A host that is already an address is returned as it is. Campus gateways are
// routinely configured by address, and sending that to a resolver would turn a
// working configuration into a lookup that can fail.
func (r *Resolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, literal, err := literalIPv4(host); literal {
		if err != nil {
			return nil, err
		}
		return []netip.Addr{address}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, DNSTimeout)
	defer cancel()
	r.takeDialFailure()

	// ip4 only: an AAAA answer would be dialed over a route this binding does
	// not describe.
	addresses, err := r.inner.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return nil, r.classify(host, err)
	}

	out := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if unmapped := address.Unmap(); unmapped.Is4() {
			out = append(out, unmapped)
		}
	}
	if len(out) == 0 {
		return nil, domain.Errorf(domain.CodeDNSFailure,
			"%s 没有解析到 IPv4 地址", host)
	}
	return out, nil
}

// classify keeps a binding problem from being reported as a name that does not
// exist. The two send the user to completely different places.
func (r *Resolver) classify(host string, err error) error {
	// Checked first, and taken from the dial hook rather than from the chain:
	// the standard library's DNSError does not carry the cause, so by the time
	// the error arrives here the binding failure exists only in the copy kept
	// aside. Without this, a line that cannot send anything reports the name
	// as unresolvable and the user goes looking at their DNS settings.
	if dialFailure := r.takeDialFailure(); dialFailure != nil {
		if code, ok := domain.CodeOf(dialFailure); ok &&
			code == domain.CodeBindingUnavailable {
			return dialFailure
		}
	}
	if code, ok := domain.CodeOf(err); ok && code == domain.CodeBindingUnavailable {
		return err
	}
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		switch {
		case dnsErr.IsNotFound:
			return domain.Errorf(domain.CodeDNSFailure,
				"无法解析 %s：该域名不存在", host).Wrap(err)
		case dnsErr.IsTimeout:
			return domain.Errorf(domain.CodeDNSFailure,
				"解析 %s 超时；线路 %s 的 DNS 服务器没有应答",
				host, r.dialer.Binding.LogicalIface).Wrap(err)
		}
	}
	return domain.Errorf(domain.CodeDNSFailure,
		"无法通过线路 %s 解析 %s", r.dialer.Binding.LogicalIface, host).Wrap(err)
}

// Servers reports the resolvers in use, for a diagnosis that can say which ones
// were tried.
func (r *Resolver) Servers() []netip.Addr {
	out := make([]netip.Addr, len(r.servers))
	copy(out, r.servers)
	return out
}
