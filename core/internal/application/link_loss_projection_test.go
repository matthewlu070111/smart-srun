package application

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

func TestLinkLossReplacesPreviouslyVerifiedStatusAndRetiresItsTransport(t *testing.T) {
	for _, mode := range []string{"wired-down", "wireless-timeout", "association-unavailable", "manual-hotspot"} {
		t.Run(mode, func(t *testing.T) {
			portal := newPortal(t)
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(probe.Close)
			binder := &fakeBinder{linkState: domain.LinkDown}
			worker, direct := workerFor(t, portal, binder)
			settings := worker.settings.(*fakeSettings)
			settings.cfg.Revision = settings.revision
			settings.cfg.Checks.Mode = domain.CheckInternet
			worker.probeURLs = []string{probe.URL}
			lines := &pooledFixture{direct: direct, pool: transport.NewPool()}
			t.Cleanup(lines.pool.Close)
			worker.lines = lines
			radio := &fakeWireless{association: wifi.Association{
				SSID: "campus-test", BSSID: "02:00:5e:00:53:01", HasIPv4: true,
			}}
			if mode != "wired-down" {
				account := &settings.cfg.CampusAccounts[0]
				account.AccessMode, account.SSID, account.Radio = domain.AccessModeWiFi, "campus-test", "radio0"
				account.Encryption, account.APSelection = "none", domain.APSelectionAuto
				settings.cfg.STAIface = "wwan"
				settings.cfg.HotspotProfiles = []domain.HotspotProfile{{ID: "h1", SSID: "phone-test", Radio: "radio0"}}
				worker.wireless = radio
			}
			sequence := uint64(0)
			run := func() Outcome {
				sequence++
				return worker.Run(t.Context(), Action{Sequence: sequence,
					Request: Request{Kind: KindMaintain, AccountID: "c1"}}, func(Phase) {})
			}
			first := run()
			if first.State != StateSucceeded || first.Observation == nil || first.Observation.Connectivity != domain.ConnectivityInternetReachable {
				t.Fatalf("initial verified connection: %+v", first)
			}
			store := observe.New()
			store.Accept(*first.Observation)
			requestsBefore := len(portal.seen())
			switch mode {
			case "wired-down":
				binder.err = domain.Errorf(domain.CodeBindingUnavailable, "synthetic link loss")
			case "wireless-timeout":
				radio.association = wifi.Association{}
				radio.failApplyFor = map[string]error{"campus-test": domain.Errorf(domain.CodeDeadlineExceeded, "synthetic association timeout")}
			case "association-unavailable":
				radio.associationErr = domain.Errorf(domain.CodeBindingUnavailable, "synthetic radio query failure")
			case "manual-hotspot":
				radio.association.SSID = "phone-test"
			}
			for range 2 {
				lost := run()
				if lost.State != StateFailed || lost.Observation == nil {
					t.Fatalf("link loss left the old observation intact: %+v", lost)
				}
				if accepted, why := store.Accept(*lost.Observation); !accepted {
					t.Fatalf("new link loss was discarded as stale: %s", why)
				}
				view := store.Read().Accounts[0]
				if view.Auth != domain.AuthUnknown || view.Connectivity != domain.ConnectivityUnknown || view.Identity != "" || view.Line.Address != "" {
					t.Fatalf("status still claims the previous connection: %+v", view)
				}
				if lines.pool.Len() != 0 || len(portal.seen()) != requestsBefore {
					t.Fatal("unavailable campus line retained a bound transport or sent authentication traffic")
				}
				if mode == "manual-hotspot" && (!lost.MaintenanceDeferred || len(radio.moves()) != 0) {
					t.Fatal("status correction disturbed the manually selected hotspot")
				}
			}
			stale := store.Read().Accounts[0]
			binder.err, radio.associationErr, radio.failApplyFor = nil, nil, nil
			radio.association.SSID = "campus-test"
			radio.association.BSSID, radio.association.HasIPv4 = "02:00:5e:00:53:01", true
			recovered := run()
			if recovered.State != StateSucceeded || recovered.Observation.Generation <= stale.Generation {
				t.Fatalf("recovery reused an unverified generation: %+v", recovered)
			}
			if accepted, why := store.Accept(*recovered.Observation); !accepted {
				t.Fatal("verified recovery rejected", why)
			}
			if accepted, _ := store.Accept(*first.Observation); accepted {
				t.Fatal("late pre-outage success replaced the recovered connection")
			}
		})
	}
}
