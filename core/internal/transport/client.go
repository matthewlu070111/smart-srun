package transport

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Client is an HTTP client that can reach the network only through one line.
//
// Everything about it is the opposite of a convenient default:
//
//   - no proxy, ever. http.ProxyFromEnvironment would send an authentication
//     request to whatever HTTP_PROXY happens to hold, which is neither the
//     gateway nor on this line. Spec 04 says Proxy is nil and this is where
//     that is enforced.
//   - no http.DefaultClient and no http.DefaultTransport. Both are shared
//     process-wide, so a connection pooled for one line would be handed to
//     another, and a timeout set for one would apply to all.
//   - no redirects followed. A gateway that answers a credentialed request
//     with a redirect is pointing somewhere this program has not agreed to
//     send credentials.
//   - names resolved through the line's own resolvers, not the system's.
type Client struct {
	binding   domain.Binding
	dialer    *Dialer
	resolver  *Resolver
	transport *http.Transport
	inner     *http.Client
	// resolverErr is why this line has no resolver, kept so the diagnosis can be
	// given at the moment a name actually has to be resolved rather than at
	// construction, where it would also refuse requests that need no names.
	resolverErr error

	// onClose lets the pool's tests see that a retirement closed the client
	// rather than merely forgetting it. Unexported and nil in production. The
	// distinction is not cosmetic: forgetting a client leaves its idle
	// connections open until they time out, holding a source address the line
	// may no longer have, and nothing observable from outside would show it.
	onClose func()
}

// NewClient builds a client for one binding.
//
// A line with no usable DNS still gets a client. It cannot resolve a name, and
// it is not allowed to borrow anybody else's resolver to try -- but a gateway
// configured as http://192.0.2.1 never needed one, and refusing to build the
// client made a perfectly reachable portal unreachable over a capability the
// request does not use. The failure is carried instead, and raised at the point
// a name actually has to be resolved.
func NewClient(binding domain.Binding) (*Client, error) {
	resolver, err := NewResolver(binding)
	if err != nil {
		client := newClientWith(binding, NewDialer(binding), nil)
		client.resolverErr = err
		return client, nil
	}
	return newClientWith(binding, NewDialer(binding), resolver), nil
}

func newClientWith(binding domain.Binding, dialer *Dialer, resolver *Resolver) *Client {
	client := &Client{binding: binding, dialer: dialer, resolver: resolver}

	client.transport = &http.Transport{
		// Nil, not http.ProxyFromEnvironment. This is the line spec 04 names.
		Proxy:       nil,
		DialContext: client.dialResolved,
		// The handshake happens on a connection that is already bound, so the
		// only thing left to fix is how long it may take.
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ResponseHeaderTimeout: ResponseHeaderTimeout,
		IdleConnTimeout:       IdleConnectionTimeout,
		// A router has a handful of gateways, not a browser's worth. Keeping
		// the pool small bounds what a line can hold open.
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 2,
		MaxConnsPerHost:     4,
		ForceAttemptHTTP2:   false,
		// The certificate is checked against the name the user configured.
		// Pinning the address does not change who we think we are talking to,
		// and turning verification off because a portal presents its own
		// certificate would accept any certificate anywhere.
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	client.inner = &http.Client{
		Transport: client.transport,
		Timeout:   RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Stop and hand the response back, rather than erroring: the
			// Location of a captive portal is useful information, and the
			// caller decides what to do with it. What must not happen is this
			// client following it with the credentials still attached.
			return http.ErrUseLastResponse
		},
	}
	return client
}

// dialResolved turns a host into an address using the line's resolvers, then
// connects to it over the line.
//
// The standard library would resolve the name itself, through the system
// resolver, before ever calling the dialer. Doing it here is what keeps the
// lookup on the line too.
func (c *Client) dialResolved(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法解析目标地址").Wrap(err)
	}

	// A literal address is dialed as it stands. This is the case that must work
	// on a line with no DNS: the request never needed a name resolved, so a
	// missing resolver is not its problem.
	if literal, isLiteral, literalErr := literalIPv4(host); isLiteral {
		if literalErr != nil {
			return nil, literalErr
		}
		return c.dialer.DialContext(ctx, network,
			net.JoinHostPort(literal.String(), port))
	}

	if c.resolver == nil {
		// A name, and nothing on this line can resolve it. The diagnosis is the
		// one NewResolver produced, which says whether the line has no servers
		// at all or only loopback ones -- two different things to fix.
		return nil, c.resolverErr
	}

	addresses, err := c.resolver.LookupIPv4(ctx, host)
	if err != nil {
		return nil, err
	}

	// Every answer is tried, so one dead address in a round-robin does not
	// fail the request. The first success wins and the last failure is
	// reported, because that is the one the caller can act on.
	var last error
	for _, candidate := range addresses {
		conn, dialErr := c.dialer.DialContext(ctx, network,
			net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		last = dialErr
		if code, ok := domain.CodeOf(dialErr); ok &&
			code == domain.CodeBindingUnavailable {
			// The line itself is unusable; another address will not help.
			return nil, dialErr
		}
	}
	return nil, last
}

// Do sends one request over the line.
//
// The caller owns the response body and must close it. Nothing here reads it:
// how much may be read depends on what was asked for, and spec 04 sets three
// different limits for three different things.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if !c.binding.Ready() {
		return nil, domain.FieldErrorf(domain.CodeBindingUnavailable,
			"wired_iface", "线路 %s 尚未就绪", c.binding.LogicalIface)
	}
	response, err := c.inner.Do(req)
	if err != nil {
		return nil, c.classify(req, err)
	}
	return response, nil
}

func (c *Client) classify(req *http.Request, err error) error {
	if code, ok := domain.CodeOf(err); ok {
		switch code {
		case domain.CodeBindingUnavailable, domain.CodeDNSFailure:
			return err
		}
	}
	if req.Context().Err() != nil {
		return domain.Errorf(domain.CodeDeadlineExceeded,
			"请求 %s 超时或已取消", req.URL.Host).Wrap(err)
	}
	return domain.Errorf(domain.CodeTransportFailure,
		"请求 %s 失败", req.URL.Host).Wrap(err)
}

// Close releases the connections this client is holding.
//
// Called when the binding it was built for stops being current. Spec 04
// requires the idle connections to go with it: one kept open across a DHCP
// change carries the old source address, and the gateway sees a request from
// an address the line no longer has.
func (c *Client) Close() {
	c.transport.CloseIdleConnections()
	if c.onClose != nil {
		c.onClose()
	}
}

// Binding reports what this client was built for, so a caller holding one can
// check it is still the current generation before sending credentials.
func (c *Client) Binding() domain.Binding { return c.binding }

// SourceAddr is the address requests leave from, which SRun's login parameters
// have to carry.
func (c *Client) SourceAddr() netip.Addr { return c.binding.SourceIPv4 }
