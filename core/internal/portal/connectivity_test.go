package portal

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestConnectivityReportNamesEachEndpointFailure(t *testing.T) {
	answers := []func() (*http.Response, error){
		func() (*http.Response, error) { return nil, domain.Errorf(domain.CodeDNSFailure, "no answer") },
		func() (*http.Response, error) { return nil, domain.Errorf(domain.CodeDeadlineExceeded, "slow") },
		func() (*http.Response, error) { return nil, errors.New("connection refused") },
	}
	calls := 0
	client := environmentFetcher(func(*http.Request) (*http.Response, error) {
		answer := answers[calls]
		calls++
		return answer()
	})
	report, err := CheckConnectivityReport(t.Context(), client,
		[]string{"http://a.invalid/generate_204", "http://b.invalid/generate_204", "http://c.invalid/generate_204"}, time.Second)
	if err != nil || report.Level != domain.ConnectivityUnknown {
		t.Fatalf("%+v / %v", report, err)
	}
	if got := report.Summary(); got != "a.invalid=dns,b.invalid=timeout,c.invalid=transport" {
		t.Fatalf("summary = %q", got)
	}
	if report.LastFailure() != FailureTransport {
		t.Fatalf("last = %q", report.LastFailure())
	}
}

func TestConnectivityReportKeepsStatusAndSuccess(t *testing.T) {
	calls := 0
	client := environmentFetcher(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return page(302, "", "http://portal.invalid/"), nil
		}
		return page(204, "", ""), nil
	})
	report, err := CheckConnectivityReport(t.Context(), client,
		[]string{"http://a.invalid/", "http://b.invalid/"}, 0)
	if err != nil || report.Level != domain.ConnectivityInternetReachable {
		t.Fatalf("%+v / %v", report, err)
	}
	if got := report.Summary(); got != "a.invalid=status_302,b.invalid=ok" {
		t.Fatalf("summary = %q", got)
	}
	if report.LastFailure() != FailureStatus {
		t.Fatalf("last = %q", report.LastFailure())
	}
	if (ConnectivityReport{}).Summary() != "no_endpoints" || (ConnectivityReport{}).LastFailure() != FailureNone {
		t.Fatal("empty report")
	}
}

func TestProbeErrorClassification(t *testing.T) {
	deadline := &net.OpError{Op: "dial", Err: timeoutError{}}
	cases := []struct {
		err      error
		timedOut bool
		want     FailureKind
	}{
		{&url.Error{Op: "Get", URL: "http://x", Err: &net.DNSError{Name: "x", IsTimeout: true}}, true, FailureDNS},
		{context.DeadlineExceeded, false, FailureTimeout},
		{errors.New("reset"), true, FailureTimeout},
		{deadline, false, FailureTimeout},
		{errors.New("reset"), false, FailureTransport},
	}
	for _, c := range cases {
		if got := classifyProbeError(c.err, c.timedOut); got != c.want {
			t.Errorf("%v -> %q, want %q", c.err, got, c.want)
		}
	}
	if endpointHost("://bad") != "invalid" {
		t.Error("bad endpoint host")
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
