package application

import (
	"net/netip"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

func TestQuietCampusSwitchNeedsExplicitOverrideBeforeTouchingRadio(t *testing.T) {
	settings := switchWorld(true)
	settings.cfg.Quiet = config.Defaults().Quiet
	radio := &fakeWireless{}
	worker := switcherFor(t, settings, radio)
	worker.clock = faketime.New(time.Date(2026, 3, 5, 20, 0, 0, 0, time.UTC)) // 04:00 UTC+8
	request := Request{Kind: KindSwitchCampus, AccountID: "c1"}
	if out := worker.Run(t.Context(), Action{Request: request}, func(Phase) {}); out.Code != domain.CodeBusy {
		t.Fatalf("quiet switch was allowed: %+v", out)
	}
	if len(radio.moves()) != 0 || radio.scanCount() != 0 {
		t.Fatal("quiet refusal happened after radio changes")
	}
	request.IgnoreQuiet = true
	worker.Run(t.Context(), Action{Request: request}, func(Phase) {})
	if len(radio.moves()) != 1 {
		t.Fatal("explicit single-action override was ignored")
	}
	if !settings.cfg.Quiet.Enabled {
		t.Fatal("override persisted a change to quiet preference")
	}
}

func TestHotspotMaintenanceSurvivesFreshWorkerWithoutReassociation(t *testing.T) {
	settings := switchWorld(true)
	settings.cfg.Enabled = true
	radio := &fakeWireless{association: wifi.Association{SSID: "phone", BSSID: "aa:bb:cc:dd:ee:ff", HasIPv4: true}}
	for range 2 {
		worker := switcherFor(t, settings, radio) // no remembered process state
		out := worker.Run(t.Context(), Action{Request: Request{Kind: KindMaintain, AccountID: "c1"}}, func(Phase) {})
		if !out.MaintenanceDeferred || out.State != StateFailed || out.Code != domain.CodeBusy {
			t.Fatalf("hotspot was not treated as a pause: %+v", out)
		}
	}
	if !settings.cfg.Enabled || radio.scanCount() != 0 || len(radio.moves()) != 0 {
		t.Fatal("maintenance changed preference or moved the hotspot")
	}
	worker := switcherFor(t, settings, radio)
	settings.cfg.Quiet = domain.QuietConfig{Enabled: true, ForceLogout: true, Start: at(t, "20:00"), End: at(t, "21:00")}
	worker.clock = faketime.New(maintainEpoch)
	out := worker.Run(t.Context(), Action{Request: Request{Kind: KindForcedLogout, AccountID: "c1"}}, func(Phase) {})
	if out.State != StateSucceeded || out.MaintenanceDeferred {
		t.Fatalf("an absent campus wireless path must not block the timetable: %+v", out)
	}
	out = worker.Run(t.Context(), Action{Request: Request{Kind: KindLogout, AccountID: "c1"}}, func(Phase) {})
	if out.Code != domain.CodeBindingUnavailable {
		t.Fatalf("manual logout must still refuse the wrong line: %+v", out)
	}
	// An explicit campus action remains allowed even while maintenance is paused.
	settings.cfg.Quiet.Enabled = false
	worker.Run(t.Context(), Action{Request: Request{Kind: KindSwitchCampus, AccountID: "c1"}}, func(Phase) {})
	if len(radio.moves()) != 1 || radio.moves()[0].SSID != "jxnu_stu" {
		t.Fatal("manual return to campus was suppressed")
	}
}

func TestHotspotPauseDoesNotSpendAuthenticationRetryBudget(t *testing.T) {
	settings := maintainWorld()
	loop, sink := maintainerFor(t, settings, faketime.New(maintainEpoch))
	loop.tick(t.Context(), maintainEpoch)
	state := loop.stateFor("c1")
	loop.apply(Action{ID: state.inFlight, Request: sink.submitted[0], State: StateFailed,
		Code: domain.CodeBusy, MaintenanceDeferred: true}, maintainEpoch)
	if state.failures != 0 || state.inFlight != "" || !state.dueAt.Equal(maintainEpoch.Add(checkInterval(&settings.cfg))) {
		t.Fatalf("pause became backoff: %+v", state)
	}
}

func TestWiredSwitchRetiresWirelessOnlyAfterSuccessfulAuthentication(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[reject], func(t *testing.T) {
			p := newPortal(t)
			settings := switchWorld(true)
			account := &settings.cfg.CampusAccounts[0]
			account.AccessMode, account.WiredIface, account.BaseURL = domain.AccessModeWired, "wan", p.server.URL
			radio := &fakeWireless{}
			worker := NewAuthenticator(AuthenticatorOptions{Binder: &fakeBinder{},
				Lines: &fakeLines{client: p.server.Client(), source: netip.MustParseAddr("10.0.0.77")}, Settings: settings, Wireless: radio})
			if reject {
				account.WiredIface = ""
			} // cannot bind: must preserve old uplink
			out := worker.Run(t.Context(), Action{Request: Request{Kind: KindSwitchCampus, AccountID: "c1"}}, func(Phase) {})
			if reject {
				if out.State != StateFailed || radio.retired != 0 {
					t.Fatal("failed target retired the old link")
				}
			} else if out.State != StateSucceeded || radio.retired != 1 || p.count(challengePath) == 0 {
				t.Fatalf("wired switch did not authenticate then retire: %+v", out)
			}
		})
	}
}

func TestWiredSwitchDoesNotReuseAuthenticationAcrossRetirementAddressChange(t *testing.T) {
	p := newPortal(t)
	settings := switchWorld(true)
	account := &settings.cfg.CampusAccounts[0]
	account.AccessMode, account.WiredIface, account.BaseURL = domain.AccessModeWired, "wan", p.server.URL
	binder := &fakeBinder{}
	radio := &fakeWireless{onRetire: func() {
		if p.count(portalPath) == 0 {
			t.Error("old uplink retired before target authenticated")
		}
		moved := steadyBinding()
		moved.SourceIPv4 = netip.MustParseAddr("10.0.0.99")
		binder.mu.Lock()
		binder.bindings = []domain.Binding{moved}
		binder.mu.Unlock()
	}}
	worker := NewAuthenticator(AuthenticatorOptions{Binder: binder, Lines: &fakeLines{client: p.server.Client(), source: netip.MustParseAddr("10.0.0.77")}, Settings: settings, Wireless: radio})
	out := worker.Run(t.Context(), Action{Request: Request{Kind: KindSwitchCampus, AccountID: "c1"}}, func(Phase) {})
	if out.State != StateFailed || out.Code != domain.CodeBindingChanged || out.Observation.Auth != domain.AuthUnknown {
		t.Fatalf("reported old-IP authentication as current: %+v", out)
	}
}
