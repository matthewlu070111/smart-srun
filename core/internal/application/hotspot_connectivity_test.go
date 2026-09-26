package application

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestHotspotWithoutInternetHonorsFailbackSetting(t *testing.T) {
	for _, failback := range []bool{false, true} {
		radio := &fakeWireless{}
		worker := switcherFor(t, switchWorld(failback), radio)
		probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html>login required</html>"))
		}))
		defer probe.Close()
		worker.lines = &fakeLines{client: probe.Client(), source: steadyBinding().SourceIPv4}
		worker.probeURLs = []string{probe.URL}
		out := worker.Run(t.Context(), switchAction(), func(Phase) {})
		moves := radio.moves()
		if failback {
			if out.State != StateFailed || len(moves) != 2 || moves[1].SSID != "jxnu_stu" {
				t.Fatalf("no failback: %+v / %+v", out, moves)
			}
		} else if out.State != StateSucceeded || len(moves) != 1 || !strings.Contains(out.Message, "尚未确认互联网") {
			t.Fatalf("ignored disabled failback or claimed Internet: %+v / %+v", out, moves)
		}
	}
}

func TestHotspotProbeCannotBorrowTheWorkingDefaultRoute(t *testing.T) {
	requests := 0
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(204) }))
	defer probe.Close()
	radio := &fakeWireless{}
	worker := switcherFor(t, switchWorld(false), radio)
	worker.lines = &fakeLines{client: probe.Client(), source: steadyBinding().SourceIPv4}
	worker.binder = &fakeBinder{err: domain.Errorf(domain.CodeBindingUnavailable, "selected STA has no address")}
	worker.probeURLs = []string{probe.URL}
	out := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if out.State != StateFailed || requests != 0 {
		t.Fatalf("used a working route without STA binding: %+v / %d", out, requests)
	}
}

func TestHotspotProbeRejectsAChangedLeaseEvenAfter204(t *testing.T) {
	radio := &fakeWireless{}
	worker := switcherFor(t, switchWorld(false), radio)
	moved := steadyBinding()
	moved.SourceIPv4 = netip.MustParseAddr("10.0.0.99")
	worker.binder = &fakeBinder{bindings: []domain.Binding{steadyBinding(), moved}}
	out := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if out.State != StateFailed || out.Code != domain.CodeBindingChanged {
		t.Fatalf("credited old lease: %+v", out)
	}
}
