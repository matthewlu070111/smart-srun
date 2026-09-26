package transport

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// clientFor builds a client whose lookups answer with loopback and whose device
// binding is stubbed, so a real HTTP exchange can happen without privileges.
// The source address binding and everything above it are real.
func clientFor(t *testing.T, devices *[]string) *Client {
	t.Helper()
	binding := loopbackBinding()
	binding.DNSServers = []netip.Addr{netip.MustParseAddr("192.0.2.53")}

	dialer := NewDialer(binding)
	dialer.control = noControl(devices)

	// The real resolver, unmodified. No lookup actually happens: httptest
	// serves on 127.0.0.1 and LookupIPv4 returns a literal address without
	// asking anyone. The lookup path has its own tests against a real DNS
	// server; these are about the HTTP behaviour on top of it.
	resolver, err := NewResolver(binding)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	resolver.dialer = dialer

	return newClientWith(binding, dialer, resolver)
}

// T16 -- proxy environment variables must not change where an authentication
// request goes.
//
// http.ProxyFromEnvironment is the default for a Transport built by hand, and a
// router with HTTP_PROXY set in its environment would send credentials to that
// proxy instead of the gateway. The test sets the variables to an address
// nothing is listening on: if the client honoured them, the request could not
// possibly succeed.
func TestProxyEnvironmentVariablesDoNotChangeWhereTheRequestGoes(t *testing.T) {
	var reached atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			reached.Add(1)
			w.Write([]byte("ok"))
		}))
	defer server.Close()

	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, "http://127.0.0.1:1")
	}

	var devices []string
	client := clientFor(t, &devices)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("the request did not reach the server; a proxy variable was "+
			"honoured: %v", err)
	}
	defer DrainAndClose(response.Body)

	if reached.Load() != 1 {
		t.Errorf("the server was reached %d times, want 1", reached.Load())
	}
}

// T18 -- a redirect is reported, never followed.
//
// A gateway answering a credentialed request with a Location is pointing
// somewhere this program has not agreed to send credentials to, and following
// it would forward them to an origin nobody checked.
func TestARedirectIsReturnedRatherThanFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			http.Redirect(w, &http.Request{}, target.URL, http.StatusFound)
		}))
	defer server.Close()

	var devices []string
	client := clientFor(t, &devices)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		server.URL, nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer DrainAndClose(response.Body)

	if response.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the redirect itself", response.StatusCode)
	}
	if location := response.Header.Get("Location"); location == "" {
		t.Error("the Location was not handed back; it is what identifies a portal")
	}
	if elsewhere.Load() != 0 {
		t.Errorf("the redirect was followed %d times", elsewhere.Load())
	}
}

// T18 -- a body larger than its limit is refused, and refused by its size
// rather than by whatever the prefix happened to parse as.
func TestAnOversizedBodyIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write(bytes.Repeat([]byte("x"), 4096))
		}))
	defer server.Close()

	var devices []string
	client := clientFor(t, &devices)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		server.URL, nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer DrainAndClose(response.Body)

	if _, err := ReadBounded(response.Body, 1024, "认证响应"); err == nil {
		t.Fatal("a body four times its limit was accepted")
	} else if code := codeOf(t, err); code != domain.CodeProtocolInvalid {
		t.Errorf("code = %s, want ProtocolInvalid", code)
	}
}

// The limit is a limit, not a truncation point: a body of exactly the limit is
// fine and one byte more is not. Reading limit+1 is what tells them apart.
func TestTheBodyLimitIsExactlyABoundary(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024} {
		body, err := ReadBounded(bytes.NewReader(bytes.Repeat([]byte("y"), size)),
			1024, "认证响应")
		if err != nil {
			t.Errorf("%d bytes was refused against a 1024 limit: %v", size, err)
		}
		if len(body) != size {
			t.Errorf("read %d bytes, want %d", len(body), size)
		}
	}
	if _, err := ReadBounded(bytes.NewReader(bytes.Repeat([]byte("y"), 1025)),
		1024, "认证响应"); err == nil {
		t.Error("1025 bytes was accepted against a 1024 limit")
	}
}

// A caller that forgets the limit gets an error rather than an unbounded read,
// and the error blames the caller rather than the server.
//
// Checking only that something failed was not enough: with the guard removed,
// a limit of zero still errors -- as "the body exceeds 0 bytes", which is a
// ProtocolInvalid pointing at the gateway for a mistake in this program. The
// code is what distinguishes a bug here from a misbehaving server, and that is
// what a reader of the log will act on.
func TestReadingWithNoLimitIsRefusedAsAProgrammingError(t *testing.T) {
	for _, limit := range []int{0, -1} {
		_, err := ReadBounded(strings.NewReader("x"), limit, "认证响应")
		if err == nil {
			t.Fatalf("an unbounded read was allowed for limit %d", limit)
		}
		if code := codeOf(t, err); code != domain.CodeInternal {
			t.Errorf("limit %d: code = %s, want Internal; a missing limit is "+
				"this program's mistake, not the gateway's", limit, code)
		}
	}
}

// The three limits spec 04 sets are different numbers for different things, and
// collapsing them would either truncate a portal page or accept a reply the
// size of one.
func TestTheThreeBodyLimitsAreDistinct(t *testing.T) {
	if !(MaxAuthenticationBody < MaxPortalBody && MaxPortalBody < MaxPresetBody) {
		t.Errorf("limits are %d/%d/%d; spec 04 orders them auth < portal < preset",
			MaxAuthenticationBody, MaxPortalBody, MaxPresetBody)
	}
	if MaxAuthenticationBody != 64<<10 {
		t.Errorf("authentication limit is %d, spec 04 says 64 KiB",
			MaxAuthenticationBody)
	}
}

// A client for a binding that is not ready refuses before opening anything.
func TestAClientForAnUnreadyBindingRefuses(t *testing.T) {
	client := newClientWith(domain.Binding{LogicalIface: "wan"},
		NewDialer(domain.Binding{}), nil)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://127.0.0.1:1/", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("an unready binding sent a request")
	} else if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
		t.Errorf("code = %s, want BindingUnavailable", code)
	}
}

// Closing the client releases what it was holding. A connection kept across a
// DHCP change carries the old source address.
func TestClosingReleasesIdleConnections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer server.Close()

	var devices []string
	client := clientFor(t, &devices)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		server.URL, nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()

	// Nothing observable to assert beyond it not panicking and the next
	// request still working: the pool is internal. What matters is that the
	// call exists and is wired to the transport rather than to the shared
	// default one.
	client.Close()
	if _, err := client.Do(req.Clone(t.Context())); err != nil {
		t.Errorf("the client stopped working after Close: %v", err)
	}
}

// The client must not be the shared default one, or a connection pooled for one
// line would be handed to another.
func TestTheClientIsNotTheProcessWideDefault(t *testing.T) {
	var devices []string
	client := clientFor(t, &devices)

	if client.inner == http.DefaultClient {
		t.Error("the shared default client is in use")
	}
	if any(client.transport) == any(http.DefaultTransport) {
		t.Error("the shared default transport is in use")
	}
	if client.transport.Proxy != nil {
		t.Error("Proxy is set; spec 04 requires it to be nil")
	}
}

// Every request leaves through the bound dialer, which is what makes the line
// guarantee hold for HTTP and not only for a raw socket.
func TestEveryRequestGoesThroughTheBoundDialer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer server.Close()

	var devices []string
	client := clientFor(t, &devices)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		server.URL, nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	DrainAndClose(response.Body)

	if len(devices) == 0 {
		t.Fatal("no connection went through the bound dialer")
	}
	for _, device := range devices {
		if device != "lo" {
			t.Errorf("a connection was pinned to %q", device)
		}
	}
}

// Draining before closing keeps the connection reusable. Without it every poll
// pays for a new handshake, which on a router is the difference between a quiet
// background task and a visible one.
func TestDrainAndCloseHandlesNil(t *testing.T) {
	DrainAndClose(nil)

	body := io.NopCloser(strings.NewReader(strings.Repeat("z", 100)))
	DrainAndClose(body)
}
