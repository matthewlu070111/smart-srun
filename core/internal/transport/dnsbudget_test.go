package transport

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// #64: a resolver that never answers -- a DNS hijack redirecting the bound
// query to a loopback resolver whose reply never comes back -- must surface as
// a DNS failure inside a short probe budget, not as a bare deadline.
func TestASilentResolverIsReportedAsDNSInsideAShortBudget(t *testing.T) {
	server := newTestDNS(t)
	server.configure(func(s *testDNS) { s.silentUDP = true })
	var devices []string
	resolver := resolverFor(t, server, &devices)
	client := newClientWith(resolver.dialer.Binding, resolver.dialer, resolver)

	ctx, cancel := context.WithTimeout(t.Context(), 800*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://probe.example./generate_204", nil)
	started := time.Now()
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("a silent resolver produced an answer")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeDNSFailure {
		t.Fatalf("error = %v (code %q), want DNSFailure", err, code)
	}
	if elapsed := time.Since(started); elapsed >= 800*time.Millisecond {
		t.Fatalf("lookup used the whole budget (%v)", elapsed)
	}
}
