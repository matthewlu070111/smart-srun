// Package transport carries requests out of one chosen line and nowhere else.
//
// Everything here is built around a domain.Binding: an observation of which
// device and address a line has right now. A connection is opened with that
// address as its source and that device pinned with SO_BINDTODEVICE, and if
// either fails the request is refused rather than sent. Spec 04 is explicit
// that a default route which happens to work is not an acceptable substitute:
// on a campus with two lines the credentials would authenticate the wrong one.
//
// The package deliberately owns no policy. It does not decide when to retry,
// which account to use, or what a response means; it decides only that bytes
// leave by the line they were supposed to.
package transport

import "time"

// The budgets spec 04 fixes. They are named constants rather than options
// because a caller that can raise them will, and the reason they are low is
// that a router with one slow line must not accumulate stuck requests.
const (
	// DialTimeout bounds one TCP connection attempt.
	DialTimeout = 5 * time.Second
	// DNSTimeout bounds one name lookup, including the TCP retry after a
	// truncated UDP answer.
	DNSTimeout = 2 * time.Second
	// TLSHandshakeTimeout bounds the handshake once connected.
	TLSHandshakeTimeout = 3 * time.Second
	// ResponseHeaderTimeout bounds the wait for the first response byte. A
	// captive portal that accepts a connection and then says nothing is a
	// common failure and must not hold a worker.
	ResponseHeaderTimeout = 3 * time.Second
	// RequestTimeout bounds one whole HTTP exchange.
	RequestTimeout = 5 * time.Second
	// AuthenticationTimeout bounds a complete authentication attempt across
	// its several requests. It is not the sum of the others: a sequence that
	// keeps just inside each individual budget must still terminate.
	AuthenticationTimeout = 30 * time.Second

	// IdleConnectionTimeout closes pooled connections that nothing is using.
	// Short, because holding one open across a DHCP change is how a request
	// leaves by an address the line no longer has.
	IdleConnectionTimeout = 20 * time.Second
)

// Body size limits, by what is being read. Spec 04 sets three, and they differ
// by an order of magnitude because the things behind them do.
const (
	// MaxAuthenticationBody is what a gateway's JSONP reply may be. Anything
	// larger is not an authentication response.
	MaxAuthenticationBody = 64 << 10
	// MaxPortalBody is what a captive portal page may be. Portals are real web
	// pages and are much larger than a reply.
	MaxPortalBody = 512 << 10
	// MaxPresetBody is what the school preset catalogue may be.
	MaxPresetBody = 2 << 20
)
