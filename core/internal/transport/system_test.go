package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestSystemProbeClientIgnoresProxiesAndRedirects(t *testing.T) {
	// A proxy variable that would fail every request if it were honoured.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/generate_204", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewSystemProbeClient()
	for path, want := range map[string]int{"/generate_204": 204, "/redirect": 302} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s: status %d, want %d", path, response.StatusCode, want)
		}
	}
}

func TestSystemProbeClientClassifiesFailures(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/", nil)
	if _, err := NewSystemProbeClient().Do(req); err == nil {
		t.Fatal("closed port answered")
	} else if code, _ := domain.CodeOf(err); code != domain.CodeTransportFailure {
		t.Fatalf("refused connection classified as %q", code)
	}

	dnsErr := &url.Error{Op: "Get", URL: "http://x.invalid/", Err: &net.DNSError{Name: "x.invalid", IsNotFound: true}}
	named, _ := http.NewRequest(http.MethodGet, "http://x.invalid/", nil)
	if code, _ := domain.CodeOf(classifySystem(named, dnsErr)); code != domain.CodeDNSFailure {
		t.Fatalf("DNS failure classified as %q", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.invalid/", nil)
	if code, _ := domain.CodeOf(classifySystem(cancelled, errors.New("canceled"))); code != domain.CodeDeadlineExceeded {
		t.Fatalf("cancelled request classified as %q", code)
	}
}
