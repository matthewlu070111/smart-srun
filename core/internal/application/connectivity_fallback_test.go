package application

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// systemFetcher stands in for the unbound system-route client.
type systemFetcher struct {
	status int
	err    error
	calls  atomic.Int32
}

func (f *systemFetcher) Do(r *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.RawQuery != "" {
		panic("system probe carried authentication data")
	}
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{StatusCode: f.status, Body: http.NoBody}, nil
}

// internetWorker is a worker in Internet mode whose bound probe answers with
// boundStatus, the way a line behind a DNS-hijacking local proxy answers
// nothing useful.
func internetWorker(t *testing.T, boundStatus int, system *systemFetcher) *Authenticator {
	t.Helper()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(boundStatus)
	}))
	t.Cleanup(probe.Close)
	worker, _ := workerFor(t, newPortal(t), &fakeBinder{})
	worker.settings.(*fakeSettings).cfg.Checks.Mode = domain.CheckInternet
	worker.probeURLs = []string{probe.URL}
	if system != nil {
		worker.systemProbe = system
	}
	return worker
}

const unconfirmedInternet = "已确认本账号在线，但尚未确认互联网连通，请检查线路或更换在线判定模式"

func TestBoundProbeFailureIsConfirmedByTheSystemRoute(t *testing.T) {
	for _, kind := range []Kind{KindMaintain, KindLogin} {
		t.Run(string(kind), func(t *testing.T) {
			system := &systemFetcher{status: http.StatusNoContent}
			worker := internetWorker(t, http.StatusServiceUnavailable, system)
			out := runWorker(t, worker, kind)
			if out.State != StateSucceeded || out.Code != "" {
				t.Fatalf("system route 204 was not accepted: %+v", out)
			}
			if out.Observation.Auth != domain.AuthVerifiedSelf ||
				out.Observation.Connectivity != domain.ConnectivityInternetReachable {
				t.Fatalf("observation = %+v", out.Observation)
			}
			d := out.Connectivity
			if d == nil || d.Via != viaSystem || d.Fallback != fallbackUsed ||
				!strings.Contains(d.Bound, "=status_503") || !strings.HasSuffix(d.System, "=ok") ||
				d.BoundLast != "status" {
				t.Fatalf("diagnosis = %+v", d)
			}
			if system.calls.Load() != 1 {
				t.Fatalf("system probe calls = %d", system.calls.Load())
			}
		})
	}
}

func TestBoundSuccessNeverAsksTheSystemRoute(t *testing.T) {
	system := &systemFetcher{status: http.StatusNoContent}
	worker := internetWorker(t, http.StatusNoContent, system)
	out := runWorker(t, worker, KindMaintain)
	if out.State != StateSucceeded || out.Connectivity == nil || out.Connectivity.Via != viaBound {
		t.Fatalf("out = %+v / %+v", out, out.Connectivity)
	}
	if system.calls.Load() != 0 {
		t.Fatal("system route consulted although the bound line proved access")
	}
}

func TestMultiWANDisablesTheSystemRouteFallback(t *testing.T) {
	system := &systemFetcher{status: http.StatusNoContent}
	worker := internetWorker(t, http.StatusServiceUnavailable, system)
	worker.settings.(*fakeSettings).cfg.MultiWANEnabled = true
	out := runWorker(t, worker, KindMaintain)
	if out.State != StateFailed || out.Code != domain.CodeTransportFailure || out.Message != unconfirmedInternet {
		t.Fatalf("out = %+v", out)
	}
	if system.calls.Load() != 0 || out.Connectivity == nil || out.Connectivity.Fallback != fallbackMultiWAN {
		t.Fatalf("multi-WAN consulted the default route: calls=%d diag=%+v", system.calls.Load(), out.Connectivity)
	}
	if out.Observation.Auth != domain.AuthVerifiedSelf {
		t.Fatalf("identity erased: %+v", out.Observation)
	}
}

func TestSwitchingToCampusNeverTrustsTheSystemRoute(t *testing.T) {
	system := &systemFetcher{status: http.StatusNoContent}
	worker := internetWorker(t, http.StatusServiceUnavailable, system)
	radio := &fakeWireless{}
	worker.wireless = radio
	out := runWorker(t, worker, KindSwitchCampus)
	if out.State != StateFailed || radio.retired != 0 || system.calls.Load() != 0 {
		t.Fatalf("hotspot route validated a campus switch: %+v calls=%d retired=%d", out, system.calls.Load(), radio.retired)
	}
	if out.Connectivity == nil || out.Connectivity.Fallback != fallbackSwitching {
		t.Fatalf("switch did not reach the Internet check: %+v", out.Connectivity)
	}
}

func TestBothRoutesFailingStaysATransportFailure(t *testing.T) {
	for name, system := range map[string]*systemFetcher{
		"status":    {status: http.StatusOK},
		"transport": {err: domain.Errorf(domain.CodeTransportFailure, "refused")},
		"dns":       {err: domain.Errorf(domain.CodeDNSFailure, "no answer")},
	} {
		t.Run(name, func(t *testing.T) {
			worker := internetWorker(t, http.StatusServiceUnavailable, system)
			out := runWorker(t, worker, KindMaintain)
			if out.State != StateFailed || out.Code != domain.CodeTransportFailure || out.Message != unconfirmedInternet {
				t.Fatalf("out = %+v", out)
			}
			if out.Observation.Auth != domain.AuthVerifiedSelf || out.Observation.Connectivity == domain.ConnectivityInternetReachable {
				t.Fatalf("observation = %+v", out.Observation)
			}
			d := out.Connectivity
			if d == nil || d.Via != "" || d.Fallback != fallbackUsed || d.System == "" || system.calls.Load() != 1 {
				t.Fatalf("diagnosis = %+v calls=%d", d, system.calls.Load())
			}
			if name == "dns" && !strings.HasSuffix(d.System, "=dns") {
				t.Fatalf("DNS failure not named: %+v", d)
			}
		})
	}
}

func TestNoSystemClientKeepsTheOldVerdict(t *testing.T) {
	worker := internetWorker(t, http.StatusServiceUnavailable, nil)
	out := runWorker(t, worker, KindMaintain)
	if out.State != StateFailed || out.Code != domain.CodeTransportFailure || out.Connectivity.Fallback != fallbackNoClient {
		t.Fatalf("out = %+v / %+v", out, out.Connectivity)
	}
}
