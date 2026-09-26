package application

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

func TestMaintenanceDoesNotReauthenticateAnExistingSession(t *testing.T) {
	for _, kind := range []string{"self", "other", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			p := newPortal(t)
			if kind == "other" {
				p.onlineBody = `{"error":"ok","user_name":"another-person"}`
			}
			p.onlineFails = kind == "unknown"
			worker, _ := workerFor(t, p, &fakeBinder{})
			for range 3 {
				out := runWorker(t, worker, KindMaintain)
				if (out.State == StateSucceeded) != (kind == "self") {
					t.Fatalf("out = %+v", out)
				}
			}
			if p.count(onlinePath) != 3 || p.count(challengePath) != 0 || p.count(portalPath) != 0 || p.count(logoutPath) != 0 {
				t.Fatalf("maintenance disturbed an existing/unknown session: %v", p.seen())
			}
		})
	}
}

func TestInternetModeRequiresIndependentEvidenceWithoutErasingIdentity(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusOK, http.StatusFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("probe leaked authentication data")
				}
				w.WriteHeader(status)
			}))
			defer probe.Close()
			p := newPortal(t)
			worker, _ := workerFor(t, p, &fakeBinder{})
			worker.settings.(*fakeSettings).cfg.Checks.Mode = domain.CheckInternet
			worker.probeURLs = []string{probe.URL}
			for range 2 {
				out := runWorker(t, worker, KindMaintain)
				if (out.State == StateSucceeded) != (status == http.StatusNoContent) {
					t.Fatalf("out = %+v", out)
				}
				if out.Observation.Auth != domain.AuthVerifiedSelf {
					t.Fatalf("probe overwrote authentication: %+v", out.Observation)
				}
				if (out.Observation.Connectivity == domain.ConnectivityInternetReachable) != (status == http.StatusNoContent) {
					t.Fatal(out.Observation)
				}
				if status != http.StatusNoContent && out.Code == domain.CodeAuthRejected {
					t.Fatal("network fault blamed password")
				}
			}
			if p.count(portalPath) != 0 || p.count(logoutPath) != 0 {
				t.Fatalf("failed probe provoked login/unbind: %v", p.seen())
			}
		})
	}
}

func TestOnlineCheckRejectsABindingChangedDuringTheProbe(t *testing.T) {
	p := newPortal(t)
	moved := steadyBinding()
	moved.SourceIPv4 = netip.MustParseAddr("10.0.0.88")
	worker, _ := workerFor(t, p, &fakeBinder{bindings: []domain.Binding{steadyBinding(), moved}})
	out := runWorker(t, worker, KindMaintain)
	if out.State != StateFailed || out.Code != domain.CodeBindingChanged || out.Observation.Auth != domain.AuthUnknown {
		t.Fatalf("out = %+v", out)
	}
}

func TestWiredInternetSwitchKeepsTheHotspotUntilInternetIsConfirmed(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer probe.Close()
	p := newPortal(t)
	worker, _ := workerFor(t, p, &fakeBinder{})
	radio := &fakeWireless{}
	worker.wireless = radio
	worker.settings.(*fakeSettings).cfg.Checks.Mode = domain.CheckInternet
	worker.probeURLs = []string{probe.URL}
	out := runWorker(t, worker, KindSwitchCampus)
	if out.State != StateFailed || out.Observation.Auth != domain.AuthVerifiedSelf || radio.retired != 0 {
		t.Fatalf("failed Internet check retired working uplink: %+v / %d", out, radio.retired)
	}
}

func TestSSIDModeStillRequiresTheSelectedAssociationAndAddress(t *testing.T) {
	for _, name := range []string{"wired-ready", "wired-down", "wifi-correct", "wifi-wrong", "wifi-no-ip", "wifi-unavailable"} {
		t.Run(name, func(t *testing.T) {
			p := newPortal(t)
			binder := &fakeBinder{}
			worker, _ := workerFor(t, p, binder)
			cfg := &worker.settings.(*fakeSettings).cfg
			cfg.Checks.Mode = domain.CheckSSID
			if name == "wired-down" {
				binder.err = domain.Errorf(domain.CodeBindingUnavailable, "no address")
			}
			if name[:4] == "wifi" {
				cfg.STAIface = "wwan"
				cfg.CampusAccounts[0].AccessMode = domain.AccessModeWiFi
				cfg.CampusAccounts[0].SSID = "campus"
				cfg.CampusAccounts[0].Radio = "radio0"
				cfg.CampusAccounts[0].Encryption = "none"
				cfg.CampusAccounts[0].APSelection = domain.APSelectionAuto
				// Call the terminal check directly: changing association after
				// authentication must not be hidden by another moveTo transaction.
				if name != "wifi-unavailable" {
					worker.wireless = &fakeWireless{association: wifi.Association{SSID: "campus", BSSID: "02:00:00:00:00:01", HasIPv4: true}}
				}
				if name == "wifi-wrong" {
					worker.wireless.(*fakeWireless).association.SSID = "hotspot"
				}
				if name == "wifi-no-ip" {
					worker.wireless.(*fakeWireless).association.HasIPv4 = false
				}
			}
			prepared, out := worker.prepare(t.Context(), Action{Request: Request{AccountID: "c1"}}, func(Phase) {})
			if prepared != nil {
				out = worker.verifyConnectivity(t.Context(), prepared, "verified", "2020123456")
			}
			want := name == "wired-ready" || name == "wifi-correct"
			if (out.State == StateSucceeded) != want {
				t.Fatalf("out = %+v", out)
			}
		})
	}
}
