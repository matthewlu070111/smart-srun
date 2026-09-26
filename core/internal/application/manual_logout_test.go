package application

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

func TestManualLogoutConfirmsOfflineBeforeHotspotAndDoesNotReplayCredentials(t *testing.T) {
	for _, scenario := range []string{"success", "rejected", "unknown", "still online", "hotspot failure", "other account", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			p := newPortal(t)
			worker, _ := workerFor(t, p, &fakeBinder{})
			cfg := &worker.settings.(*fakeSettings).cfg
			cfg.STAIface, cfg.Selection.ActiveCampusID, cfg.Selection.ActiveHotspotID = "wwan", "c1", "h1"
			cfg.Failover.Enabled = true
			cfg.HotspotProfiles = []domain.HotspotProfile{{ID: "h1", SSID: "phone", Radio: "radio0", Encryption: "none"}}
			radio := &fakeWireless{}
			worker.wireless = radio
			probes := 0
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				probes++
				if p.count(logoutPath) != 1 || p.count(onlinePath) < 2 {
					t.Error("hotspot reached before logout confirmation")
				}
				w.WriteHeader(204)
			}))
			defer probe.Close()
			worker.probeURLs = []string{probe.URL}
			switch scenario {
			case "rejected":
				p.logoutBody = `{"error":"sign_error"}`
			case "unknown":
				p.onlineFails = true
			case "still online":
				p.keepSessionAfterLogout = true
			case "hotspot failure":
				radio.failApplyFor = map[string]error{"phone": domain.Errorf(domain.CodeBindingUnavailable, "unavailable")}
			case "other account":
				cfg.Selection.ActiveCampusID = "c2"
			case "disabled":
				cfg.Failover.Enabled = false
			}
			out := runWorker(t, worker, KindLogout)
			wantSuccess := scenario == "success" || scenario == "other account" || scenario == "disabled"
			if (out.State == StateSucceeded) != wantSuccess {
				t.Fatalf("outcome: %+v", out)
			}
			if scenario == "success" {
				if len(radio.moves()) != 1 || probes != 1 {
					t.Fatal("logout did not reach verified hotspot")
				}
			} else if len(radio.moves()) != 0 || probes != 0 {
				t.Fatal("unexpected hotspot move/probe")
			}
			if scenario == "hotspot failure" && (out.Observation == nil || out.Observation.Auth != domain.AuthOffline || !strings.Contains(out.Message, "已确认下线")) {
				t.Fatal("hotspot failure lost verified logout evidence")
			}
			if p.count(logoutPath) > 1 || p.count(portalPath) != 0 || p.count(challengePath) != 0 {
				t.Fatal("logout replayed credentials")
			}
		})
	}
}

func TestManualLogoutDoesNotQueryCampusOverAnotherWirelessNetwork(t *testing.T) {
	p := newPortal(t)
	worker, _ := workerFor(t, p, &fakeBinder{})
	cfg := &worker.settings.(*fakeSettings).cfg
	cfg.CampusAccounts[0].AccessMode = domain.AccessModeWiFi
	cfg.CampusAccounts[0].SSID, cfg.CampusAccounts[0].Radio = "campus", "radio0"
	worker.wireless = &fakeWireless{association: wifi.Association{SSID: "phone", BSSID: "02:00:5e:00:53:02", HasIPv4: true}}
	if out := runWorker(t, worker, KindLogout); out.State != StateFailed || out.Code != domain.CodeBindingUnavailable || len(p.seen()) != 0 {
		t.Fatalf("sent campus logout on a different network: %+v / %v", out, p.seen())
	}
}

func TestManualPauseLeavesOtherManagedAccountsRunningWithoutBusySpin(t *testing.T) {
	s := maintainWorld()
	s.cfg.MultiWANEnabled = true
	second := s.cfg.CampusAccounts[0]
	second.ID, second.WiredIface, second.AuthEnabled = "c2", "wan2", true
	s.cfg.CampusAccounts = append(s.cfg.CampusAccounts, second)
	paused := true
	sink := &recorder{}
	m := NewMaintainer(MaintainerOptions{Settings: s, Submit: sink.submit,
		ManuallyPaused: func(_ domain.Config, id string) bool { return paused && id == "c1" }})
	m.stateFor("c1").dueAt = maintainEpoch.Add(-time.Second)
	wake := m.tick(t.Context(), maintainEpoch)
	if len(sink.submitted) != 1 || sink.submitted[0].AccountID != "c2" || !wake.After(maintainEpoch) {
		t.Fatal("pause blocked another line or busy-spun")
	}
	paused = false
	m.tick(t.Context(), maintainEpoch)
	if len(sink.submitted) != 2 || sink.submitted[1].AccountID != "c1" {
		t.Fatal("manual resume did not allow maintenance")
	}
}

func TestManualLogoutCancelsOldMaintenanceAndRetainsTheSwitchBarrier(t *testing.T) {
	admissions := 0
	h := newHarness(t, func(o *Options) {
		o.Admit = func(r Request) error {
			if r.Kind == KindLogout {
				admissions++
			}
			return nil
		}
	})
	old := h.submit(KindMaintain, "c1", "maintain")
	h.awaitStart()
	h.mu.Lock()
	h.linger[old.ActionID] = true
	h.mu.Unlock()
	logout := h.submit(KindLogout, "c1", "logout")
	h.awaitCancelSeen(old.ActionID)
	h.submit(KindMaintain, "c2", "other-line")
	h.expectNoStart("logout must retain the old worker's cancellation barrier")
	h.succeed(old.ActionID)
	if got := h.awaitStart(); got.ID != logout.ActionID {
		t.Fatal("maintenance overtook logout")
	}
	h.expectNoStart("logout may reload the managed wireless uplink")
	h.succeed(logout.ActionID)
	h.awaitState(logout.ActionID, StateSucceeded)
	h.submit(KindLogout, "c1", "logout")
	if admissions != 1 {
		t.Fatal("duplicate logout re-applied its intent")
	}
}
