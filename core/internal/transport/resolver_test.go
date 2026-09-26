package transport

import (
	"net"
	"net/netip"
	"slices"
	"syscall"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// resolverFor builds a resolver whose queries go to the test server, with the
// device binding stubbed out so the test can run without privileges. The source
// address binding is real.
func resolverFor(t *testing.T, server *testDNS, devices *[]string) *Resolver {
	t.Helper()
	binding := loopbackBinding()
	binding.DNSServers = []netip.Addr{netip.MustParseAddr("127.0.0.1")}

	// NewResolver refuses loopback resolvers on purpose, so the test reaches
	// past it and installs the server directly -- what is under test here is
	// the query path, and the refusal has its own test below.
	resolver := &Resolver{
		dialer:  NewDialer(binding),
		servers: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		port:    server.port(),
	}
	resolver.dialer.control = noControl(devices)
	resolver.inner = &net.Resolver{
		PreferGo:     true,
		StrictErrors: true,
		Dial:         resolver.dialToServer,
	}
	return resolver
}

// T13 -- the ordinary lookup, over the line.
func TestALookupGoesToTheLinesOwnResolver(t *testing.T) {
	server := newTestDNS(t)
	var devices []string
	resolver := resolverFor(t, server, &devices)

	addresses, err := resolver.LookupIPv4(t.Context(), "gateway.example.")
	if err != nil {
		t.Fatalf("LookupIPv4: %v", err)
	}
	want := netip.MustParseAddr("203.0.113.7")
	if !slices.Contains(addresses, want) {
		t.Errorf("addresses = %v, want to include %v", addresses, want)
	}
	if seen := server.seen(); len(seen) == 0 || seen[0] != "udp" {
		t.Errorf("transports = %v, want udp first", seen)
	}
	if len(devices) == 0 {
		t.Error("the query was not sent through the bound dialer")
	}
}

// T13 -- the case the card names: a truncated UDP answer must be retried over
// TCP, and that retry must leave by the same line.
//
// This is where a resolver quietly falls back to the system's: the standard
// library would open the TCP retry through the default route unless the same
// dial hook handles it.
func TestATruncatedAnswerIsRetriedOverTCPOnTheSameLine(t *testing.T) {
	server := newTestDNS(t)
	server.configure(func(s *testDNS) { s.truncateUDP = true })

	var devices []string
	resolver := resolverFor(t, server, &devices)

	addresses, err := resolver.LookupIPv4(t.Context(), "gateway.example.")
	if err != nil {
		t.Fatalf("LookupIPv4: %v", err)
	}
	if want := netip.MustParseAddr("203.0.113.7"); !slices.Contains(addresses, want) {
		t.Errorf("addresses = %v, want %v from the TCP retry", addresses, want)
	}

	seen := server.seen()
	if !slices.Contains(seen, "udp") || !slices.Contains(seen, "tcp") {
		t.Fatalf("transports = %v, want the UDP attempt and the TCP retry", seen)
	}
	// Every dial, UDP and TCP alike, went through the bound dialer.
	if len(devices) < 2 {
		t.Errorf("bound dials = %d for transports %v; the TCP retry did not "+
			"go through the binding", len(devices), seen)
	}
	for _, device := range devices {
		if device != "lo" {
			t.Errorf("a query was pinned to %q, not the line's device", device)
		}
	}
}

// A binding whose only resolvers are on this machine is refused with a reason.
// Spec 04 will not accept an answer from 127.0.0.1 as evidence about a line,
// because the query leaves by whichever line the local resolver chose.
func TestALineWithOnlyALocalResolverIsRefusedWithAReason(t *testing.T) {
	binding := loopbackBinding()
	binding.DNSServers = []netip.Addr{
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("127.0.0.53"),
	}

	_, err := NewResolver(binding)
	if err == nil {
		t.Fatal("a line with only a local resolver was accepted")
	}
	if code := codeOf(t, err); code != domain.CodeDNSFailure {
		t.Errorf("code = %s, want DNSFailure", code)
	}
	if got := err.Error(); !contains(got, "本机 DNS") {
		t.Errorf("the message does not explain the problem: %s", got)
	}
}

// A line with no resolvers at all is a different problem and says so.
func TestALineWithNoResolversSaysSo(t *testing.T) {
	binding := loopbackBinding()
	binding.DNSServers = nil

	_, err := NewResolver(binding)
	if err == nil {
		t.Fatal("a line with no resolvers was accepted")
	}
	if got := err.Error(); contains(got, "本机 DNS") {
		t.Errorf("no resolvers is not the same as a local-only resolver: %s", got)
	}
}

// A resolver on the line is accepted, and only the usable ones are kept.
func TestOnlyUsableResolversAreKept(t *testing.T) {
	binding := loopbackBinding()
	binding.DNSServers = []netip.Addr{
		netip.MustParseAddr("127.0.0.1"),   // this machine
		netip.MustParseAddr("0.0.0.0"),     // not a server
		netip.MustParseAddr("2001:db8::1"), // not this line's family
		netip.MustParseAddr("192.0.2.53"),  // usable
	}

	resolver, err := NewResolver(binding)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	want := []netip.Addr{netip.MustParseAddr("192.0.2.53")}
	if !slices.Equal(resolver.Servers(), want) {
		t.Errorf("servers = %v, want %v", resolver.Servers(), want)
	}
}

// An address is not a name. Campus gateways are routinely configured by
// address, and sending that to a resolver turns a working configuration into a
// lookup that can fail.
func TestAnAddressIsReturnedWithoutALookup(t *testing.T) {
	server := newTestDNS(t)
	var devices []string
	resolver := resolverFor(t, server, &devices)

	addresses, err := resolver.LookupIPv4(t.Context(), "10.0.0.1")
	if err != nil {
		t.Fatalf("LookupIPv4: %v", err)
	}
	if len(addresses) != 1 || addresses[0].String() != "10.0.0.1" {
		t.Errorf("addresses = %v", addresses)
	}
	if seen := server.seen(); len(seen) != 0 {
		t.Errorf("a query was sent for a literal address: %v", seen)
	}
}

// An IPv6 literal cannot be reached over this binding, and saying so is better
// than dialing it by some other route.
func TestAnIPv6LiteralIsRefused(t *testing.T) {
	server := newTestDNS(t)
	var devices []string
	resolver := resolverFor(t, server, &devices)

	if _, err := resolver.LookupIPv4(t.Context(), "2001:db8::1"); err == nil {
		t.Fatal("an IPv6 literal was accepted for an IPv4-only line")
	}
}

// A resolver that does not answer is a DNS failure naming the line, not a
// mysterious one -- the user has to know which line's resolver is silent.
func TestASilentResolverTimesOutWithTheLineNamed(t *testing.T) {
	server := newTestDNS(t)
	server.configure(func(s *testDNS) { s.silentUDP = true })

	var devices []string
	resolver := resolverFor(t, server, &devices)
	// The TCP side would answer, so close it: this is the "nothing responds"
	// case, not the truncation one.
	server.tcp.Close()

	_, err := resolver.LookupIPv4(t.Context(), "gateway.example.")
	if err == nil {
		t.Fatal("a silent resolver produced an answer")
	}
	if code := codeOf(t, err); code != domain.CodeDNSFailure {
		t.Errorf("code = %s, want DNSFailure", code)
	}
	if got := err.Error(); !contains(got, "wan") {
		t.Errorf("the message does not name the line: %s", got)
	}
}

// A binding that cannot be used keeps its own answer through the resolver: the
// problem is the line, not the name.
func TestABindingFailureIsNotReportedAsANameProblem(t *testing.T) {
	server := newTestDNS(t)
	var devices []string
	resolver := resolverFor(t, server, &devices)
	resolver.dialer.control = func(string) func(string, string, syscall.RawConn) error {
		return func(string, string, syscall.RawConn) error { return syscall.EPERM }
	}

	_, err := resolver.LookupIPv4(t.Context(), "gateway.example.")
	if err == nil {
		t.Fatal("a lookup succeeded with an unusable binding")
	}
	if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
		t.Errorf("code = %s, want BindingUnavailable; the line is the problem, "+
			"not the name", code)
	}
}
