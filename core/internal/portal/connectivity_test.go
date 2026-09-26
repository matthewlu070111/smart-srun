package portal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

func TestConnectivityContinuesAfterAnInterceptedOrFailedEndpoint(t *testing.T) {
	for _, status := range []int{0, 200, 302, 500} {
		calls := 0
		client := environmentFetcher(func(r *http.Request) (*http.Response, error) {
			calls++
			if _, ok := r.Context().Deadline(); !ok {
				t.Fatal("missing deadline")
			}
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("credentials on probe")
			}
			if calls == 1 {
				if status == 0 {
					return nil, errors.New("timeout")
				}
				return page(status, "<html>intercepted</html>", "http://other.invalid/?action=logout"), nil
			}
			return page(204, "", ""), nil
		})
		level, err := CheckConnectivity(t.Context(), client, []string{"http://first.invalid/204", "http://second.invalid/204"})
		if err != nil || level != domain.ConnectivityInternetReachable || calls != 2 {
			t.Fatalf("%s / %v / %d", level, err, calls)
		}
	}
}

type countedProbeBody struct {
	io.Reader
	n      int
	closed bool
}

func (b *countedProbeBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.n += n
	return n, err
}
func (b *countedProbeBody) Close() error { b.closed = true; return nil }

func TestConnectivityResponseAndEndpointCountAreBounded(t *testing.T) {
	for _, status := range []int{200, 204, 302} {
		var bodies []*countedProbeBody
		client := environmentFetcher(func(r *http.Request) (*http.Response, error) {
			body := &countedProbeBody{Reader: strings.NewReader(strings.Repeat("x", transport.MaxAuthenticationBody+900))}
			bodies = append(bodies, body)
			return &http.Response{StatusCode: status, Body: body}, nil
		})
		level, err := CheckConnectivity(t.Context(), client, []string{"http://a.invalid", "http://b.invalid", "http://c.invalid", "http://d.invalid"})
		if err != nil || level == domain.ConnectivityInternetReachable || len(bodies) != 3 {
			t.Fatalf("%s / %v / %d", level, err, len(bodies))
		}
		for _, body := range bodies {
			if !body.closed || body.n != transport.MaxAuthenticationBody+1 {
				t.Fatalf("unbounded/unclosed response: %+v", body)
			}
		}
	}
}

func TestConnectivityStopsOnLostBindingOrCancellation(t *testing.T) {
	for _, code := range []domain.ErrorCode{domain.CodeBindingUnavailable, domain.CodeBindingChanged, domain.CodeNotFound} {
		calls := 0
		client := environmentFetcher(func(*http.Request) (*http.Response, error) { calls++; return nil, domain.Errorf(code, "binding lost") })
		_, err := CheckConnectivity(t.Context(), client, []string{"http://a.invalid", "http://b.invalid"})
		got, _ := domain.CodeOf(err)
		if got != code || calls != 1 {
			t.Fatalf("%v / %d", err, calls)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	client := environmentFetcher(func(*http.Request) (*http.Response, error) { calls++; cancel(); return nil, ctx.Err() })
	_, err := CheckConnectivity(ctx, client, []string{"http://a.invalid", "http://b.invalid"})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("%v / %d", err, calls)
	}
}

func TestConnectivityRejectsCredentialedURLsWithoutSendingThem(t *testing.T) {
	client := environmentFetcher(func(*http.Request) (*http.Response, error) { t.Fatal("unsafe URL sent"); return nil, nil })
	level, err := CheckConnectivity(t.Context(), client, []string{"http://user:secret@host.invalid", "http://host.invalid/?password=secret", "http://host.invalid/?action=logout"})
	if err != nil || level != domain.ConnectivityUnknown {
		t.Fatalf("%s / %v", level, err)
	}
}
