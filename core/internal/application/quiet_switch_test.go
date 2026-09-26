package application

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

func quietWorld(t *testing.T) *fakeSettings {
	s := maintainWorld()
	s.cfg.Failover.Enabled = true
	s.cfg.Quiet = domain.QuietConfig{Enabled: true, ForceLogout: true, Start: at(t, "20:00"), End: at(t, "21:00")}
	s.cfg.HotspotProfiles = []domain.HotspotProfile{{ID: "h1", SSID: "phone", Radio: "radio0"}}
	s.cfg.Selection.ActiveHotspotID = "h1"
	return s
}

func TestQuietFailoverWaitsForLogoutAndReturnsOnlyItsOwnSwitch(t *testing.T) {
	for _, reused := range []bool{false, true} {
		settings := quietWorld(t)
		clock := faketime.New(maintainEpoch)
		loop, sink := maintainerFor(t, settings, clock)
		loop.tick(t.Context(), clock.Now())
		if len(sink.submitted) != 1 || sink.submitted[0].Kind != KindForcedLogout {
			t.Fatalf("radio switch raced logout: %+v", sink.submitted)
		}
		loop.apply(finished(sink, 0, StateFailed), clock.Now())
		loop.tick(t.Context(), clock.Now())
		if len(sink.submitted) != 1 {
			t.Fatal("failed logout retried without a delay")
		}
		clock.Advance(time.Minute)
		loop.tick(t.Context(), clock.Now())
		if sink.submitted[1].Kind != KindForcedLogout {
			t.Fatal("failed logout was bypassed")
		}
		if sink.submitted[1].IdempotencyKey == sink.submitted[0].IdempotencyKey {
			t.Fatal("retry would only fetch the cached failed action")
		}
		loop.apply(finished(sink, 1, StateSucceeded), clock.Now())
		loop.tick(t.Context(), clock.Now())
		if len(sink.submitted) != 3 || sink.submitted[2].Kind != KindQuietHotspot || sink.submitted[2].HotspotID != "h1" {
			t.Fatalf("hotspot not queued after logout: %+v", sink.submitted)
		}
		result := finished(sink, 2, StateSucceeded)
		result.MaintenanceDeferred = reused
		loop.apply(result, clock.Now())
		clock.Advance(time.Hour - time.Minute)
		loop.tick(t.Context(), clock.Now())
		want := KindQuietCampus
		if reused {
			want = KindMaintain // worker observes the intentional hotspot and pauses
		}
		if len(sink.submitted) != 4 || sink.submitted[3].Kind != want {
			t.Fatalf("wrong ownership on quiet exit: reused=%v requests=%+v", reused, sink.submitted)
		}
	}
}

func TestQuietFailoverDisabledAndDaytimeManualChoiceAreRespected(t *testing.T) {
	settings := quietWorld(t)
	settings.cfg.Quiet.ForceLogout = false
	settings.cfg.Failover.Enabled = false
	loop, sink := maintainerFor(t, settings, faketime.New(maintainEpoch))
	loop.tick(t.Context(), maintainEpoch)
	if len(sink.submitted) != 0 {
		t.Fatal("disabled failover switched")
	}
	settings.cfg.Failover.Enabled = true
	loop.tick(t.Context(), maintainEpoch)
	loop.apply(finished(sink, 0, StateSucceeded), maintainEpoch)
	loop.apply(Action{Request: Request{Kind: KindSwitchHotspot}, StartedAt: maintainEpoch.Add(time.Hour), State: StateSucceeded}, maintainEpoch.Add(time.Hour))
	loop.tick(t.Context(), maintainEpoch.Add(time.Hour))
	for _, request := range sink.submitted[1:] {
		if request.Kind == KindQuietCampus {
			t.Fatal("schedule reclaimed a user-selected hotspot")
		}
	}
}

func TestDisabledOrExpiredQuietPolicyCannotLogOut(t *testing.T) {
	settings := quietWorld(t)
	settings.cfg.Enabled = false
	loop, sink := maintainerFor(t, settings, faketime.New(maintainEpoch))
	loop.tick(t.Context(), maintainEpoch)
	if len(sink.submitted) != 0 {
		t.Fatal("disabled daemon scheduled quiet network side effects")
	}
	p := newPortal(t)
	worker, _ := workerFor(t, p, &fakeBinder{})
	cfg := &worker.settings.(*fakeSettings).cfg
	cfg.Enabled = true
	cfg.Quiet = settings.cfg.Quiet
	worker.clock = faketime.New(maintainEpoch.Add(time.Hour))
	out := runWorker(t, worker, KindForcedLogout)
	if !out.MaintenanceDeferred || len(p.seen()) != 0 {
		t.Fatalf("queued logout executed after quiet window: %+v / %v", out, p.seen())
	}
}

func TestQuietWorkerReusesSelectedHotspotAndRetainsTheScheduledReturn(t *testing.T) {
	settings := switchWorld(true)
	settings.cfg.Enabled, settings.cfg.Failover.Enabled = true, true
	settings.cfg.Quiet = domain.QuietConfig{Enabled: true, Start: at(t, "20:00"), End: at(t, "21:00")}
	radio := &fakeWireless{association: wifi.Association{SSID: "phone", BSSID: "aa:bb:cc:dd:ee:ff", HasIPv4: true, Encrypted: true}}
	worker := switcherFor(t, settings, radio)
	clock := faketime.New(maintainEpoch)
	worker.clock = clock
	action := Action{Request: Request{Kind: KindQuietHotspot, AccountID: "c1", HotspotID: "h1"}}
	out := worker.Run(t.Context(), action, func(Phase) {})
	if out.State != StateSucceeded || out.MaintenanceDeferred || len(radio.moves()) != 0 || radio.scanCount() != 0 {
		t.Fatalf("schedule must retain its return without changing an existing hotspot: %+v", out)
	}
	clock.Advance(time.Hour)
	out = worker.Run(t.Context(), action, func(Phase) {})
	if !out.MaintenanceDeferred || len(radio.moves()) != 0 {
		t.Fatalf("stale entry action changed network after window: %+v", out)
	}
	action.Request.Kind = KindQuietCampus
	radio.association.SSID = "user-selected-network"
	out = worker.Run(t.Context(), action, func(Phase) {})
	if !out.MaintenanceDeferred || len(radio.moves()) != 0 {
		t.Fatalf("return action overwrote another network choice: %+v", out)
	}
}

func TestManualHotspotDuringQuietWindowStillReturnsAtTheDeadline(t *testing.T) {
	settings := quietWorld(t)
	loop, sink := maintainerFor(t, settings, faketime.New(maintainEpoch))
	loop.apply(Action{Request: Request{Kind: KindSwitchHotspot, HotspotID: "h1"},
		StartedAt: maintainEpoch, State: StateSucceeded}, maintainEpoch)
	loop.tick(t.Context(), maintainEpoch.Add(time.Hour))
	if len(sink.submitted) != 1 || sink.submitted[0].Kind != KindQuietCampus || sink.submitted[0].HotspotID != "h1" {
		t.Fatalf("manual hotspot erased the configured morning deadline: %+v", sink.submitted)
	}
}

func TestQuietReturnUsesAutomaticIdentityRulesBeforeRetiringHotspot(t *testing.T) {
	for _, other := range []bool{false, true} {
		p := newPortal(t)
		if other {
			p.onlineBody = `{"error":"ok","user_name":"someone-else","online_ip":"10.0.0.77"}`
		}
		worker, _ := workerFor(t, p, &fakeBinder{})
		cfg := &worker.settings.(*fakeSettings).cfg
		cfg.Enabled, cfg.Failover.Enabled = true, true
		cfg.Selection.ActiveCampusID = "c1"
		cfg.HotspotProfiles = []domain.HotspotProfile{{ID: "h1", SSID: "phone", Radio: "radio0", Encryption: "none"}}
		radio := &fakeWireless{association: wifi.Association{SSID: "phone", BSSID: "aa:bb:cc:dd:ee:ff", HasIPv4: true}}
		worker.wireless = radio
		out := worker.Run(t.Context(), Action{Request: Request{Kind: KindQuietCampus, AccountID: "c1", HotspotID: "h1"}}, func(Phase) {})
		if p.count(portalPath) != 0 || p.count(logoutPath) != 0 || p.count(challengePath) != 0 {
			t.Fatalf("automatic return reauthenticated or kicked another session: %v", p.seen())
		}
		if other {
			if out.Code != domain.CodeOnlineIdentityMismatch || radio.retired != 0 {
				t.Fatalf("unsafe campus return: %+v / retired=%d", out, radio.retired)
			}
		} else if out.State != StateSucceeded || radio.retired != 1 {
			t.Fatalf("did not reuse verified campus session: %+v", out)
		}
	}
}

func TestExplicitSwitchSupersedesScheduledSwitchAndWaitsForItsWorker(t *testing.T) {
	h := newHarness(t, nil)
	quiet, err := h.Submit(t.Context(), Request{Kind: KindQuietCampus, AccountID: "c1", HotspotID: "h1", IdempotencyKey: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	h.lingerOn(quiet.ActionID)
	h.awaitStart()
	manual := h.submit(KindSwitchCampus, "c1", "manual")
	h.awaitState(quiet.ActionID, StateCancelled)
	h.awaitCancelSeen(quiet.ActionID)
	h.expectNoStart("manual switch must wait for cancelled radio worker")
	h.finish(quiet.ActionID, Outcome{State: StateSucceeded})
	if next := h.awaitStart(); next.ID != manual.ActionID {
		t.Fatal("manual switch did not replace scheduled return")
	}
}
