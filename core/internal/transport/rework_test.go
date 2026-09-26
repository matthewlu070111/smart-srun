package transport

import (
	"net/netip"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// R07 -- a gateway addressed by IP needs no DNS, so a line without one must
// still be able to reach it.
//
// Requiring a resolver at construction refused the request over a capability it
// never used: a campus portal configured as http://192.0.2.1 on an interface
// whose DHCP offered no usable resolver could not be authenticated against at
// all, and the diagnosis pointed at DNS.
func TestALineWithNoDNSStillBuildsAClientForALiteralGateway(t *testing.T) {
	binding := domain.Binding{
		LogicalIface: "wan",
		L3Device:     "eth0",
		IfIndex:      3,
		SourceIPv4:   netip.MustParseAddr("192.0.2.10"),
		// No DNS servers at all.
		Generation: 1,
	}

	client, err := NewClient(binding)
	if err != nil {
		t.Fatalf("NewClient refused a line with no DNS: %v", err)
	}
	if client == nil {
		t.Fatal("NewClient returned no client and no error")
	}
	if client.resolver != nil {
		t.Error("a line with no usable servers was given a resolver anyway")
	}
	if client.resolverErr == nil {
		t.Error("the reason there is no resolver was not kept for the diagnosis")
	}

	// And the dial actually gets past the resolver. Checking only that NewClient
	// returned would leave the requirement in place one layer down, where it
	// would refuse the request just the same -- which is what the first version
	// of this test missed.
	_, dialErr := client.dialResolved(t.Context(), "tcp", "192.0.2.1:80")
	if dialErr == nil {
		// Reaching TEST-NET-1 from a source address this machine does not have
		// is not expected to succeed; what matters is why it failed.
		t.Log("the dial unexpectedly succeeded, which still proves DNS was not required")
		return
	}
	if code, _ := domain.CodeOf(dialErr); code == domain.CodeDNSFailure {
		t.Errorf("a literal address was refused for want of DNS: %v", dialErr)
	}
}

// The requirement moves to the point a name actually has to be resolved, and it
// keeps the diagnosis that says which kind of DNS problem this is.
func TestResolvingANameOnALineWithNoDNSStillFails(t *testing.T) {
	cases := map[string][]netip.Addr{
		"no servers at all": nil,
		"only loopback":     {netip.MustParseAddr("127.0.0.1")},
	}
	for name, servers := range cases {
		binding := domain.Binding{
			LogicalIface: "wan",
			L3Device:     "eth0",
			IfIndex:      3,
			SourceIPv4:   netip.MustParseAddr("192.0.2.10"),
			DNSServers:   servers,
			Generation:   1,
		}
		client, err := NewClient(binding)
		if err != nil {
			t.Fatalf("%s: NewClient: %v", name, err)
		}

		_, dialErr := client.dialResolved(t.Context(), "tcp", "portal.example:80")
		if dialErr == nil {
			t.Errorf("%s: a name was resolved on a line that cannot resolve", name)
			continue
		}
		if code, _ := domain.CodeOf(dialErr); code != domain.CodeDNSFailure {
			t.Errorf("%s: code = %s, want DNSFailure", name, code)
		}
		// And it must not have quietly used the system resolver to get there.
		if client.resolver != nil {
			t.Errorf("%s: a resolver was built from servers this line cannot use", name)
		}
	}
}

// An IPv6 literal is still refused: this line carries IPv4 only, and dialing a
// v6 address over it would leave by a route the binding does not describe.
func TestAnIPv6LiteralIsRefusedRatherThanDialed(t *testing.T) {
	binding := domain.Binding{
		LogicalIface: "wan",
		L3Device:     "eth0",
		IfIndex:      3,
		SourceIPv4:   netip.MustParseAddr("192.0.2.10"),
		Generation:   1,
	}
	client, err := NewClient(binding)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, dialErr := client.dialResolved(t.Context(), "tcp", "[2001:db8::1]:80")
	if dialErr == nil {
		t.Fatal("an IPv6 literal was dialed over an IPv4-only line")
	}
	if code, _ := domain.CodeOf(dialErr); code != domain.CodeDNSFailure {
		t.Errorf("code = %s, want DNSFailure", code)
	}
}
