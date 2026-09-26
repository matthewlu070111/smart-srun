package application

import (
	"context"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

func TestManualAndScheduledTerminalChecksWaitWithoutRepeatingTheMutation(t *testing.T) {
	for _, kind := range []Kind{KindLogin, KindLogout, KindQuietCampus, KindForcedLogout} {
		t.Run(string(kind), func(t *testing.T) {
			p := newPortal(t)
			p.keepSessionAfterLogout = true
			p.onlineAfter = func(count int) {
				if kind == KindLogin || kind == KindQuietCampus {
					p.onlineBody = offlineAnswer
					ready := 3
					if kind == KindQuietCampus {
						ready++ // automatic return checks the existing identity first
					}
					if count == ready {
						p.onlineBody = `{"error":"ok","user_name":"2020123456","online_ip":"10.0.0.77"}`
					}
				} else if count == 4 {
					p.onlineBody = offlineAnswer
				}
			}
			worker, _ := workerFor(t, p, &fakeBinder{})
			clock := faketime.New(maintainEpoch)
			worker.clock = clock
			settings := worker.settings.(*fakeSettings)
			settings.cfg.Checks.TerminalAttempts = 3
			settings.cfg.Checks.TerminalIntervalSeconds = 7
			settings.cfg.Enabled = true
			if kind == KindForcedLogout {
				settings.cfg.Quiet = quietWorld(t).cfg.Quiet
			}
			if kind == KindQuietCampus {
				settings.cfg.Failover.Enabled = true
				settings.cfg.Selection.ActiveCampusID = "c1"
				settings.cfg.HotspotProfiles = []domain.HotspotProfile{{ID: "h1", SSID: "phone", Radio: "radio0", Encryption: "none"}}
				worker.wireless = &fakeWireless{association: wifi.Association{SSID: "phone", BSSID: "02:00:5e:00:53:01", HasIPv4: true}}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result := make(chan Outcome, 1)
			go func() {
				result <- worker.Run(ctx, Action{Request: Request{Kind: kind, AccountID: "c1", HotspotID: "h1"}}, func(Phase) {})
			}()
			for range 2 {
				if err := clock.BlockUntilContext(ctx, 1); err != nil {
					t.Fatal(err)
				}
				clock.Advance(6 * time.Second)
				if clock.Waiters() != 1 {
					t.Fatal("ignored the configured seven-second interval")
				}
				clock.Advance(time.Second)
			}
			out := <-result
			if out.State != StateSucceeded {
				t.Fatalf("out = %+v", out)
			}
			if p.count(portalPath)+p.count(logoutPath) != 1 {
				t.Fatalf("repeated mutation: %v", p.seen())
			}
			if clock.Waiters() != 0 {
				t.Fatal("terminal timer leaked")
			}
		})
	}
}

func TestTerminalChecksStopAtTheConfiguredLimit(t *testing.T) {
	p := newPortal(t)
	p.onlineBody = offlineAnswer
	worker, _ := workerFor(t, p, &fakeBinder{})
	clock := faketime.New(time.Now())
	worker.clock = clock
	worker.settings.(*fakeSettings).cfg.Checks.TerminalAttempts = 2
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan Outcome, 1)
	go func() {
		result <- worker.Run(ctx, Action{Request: Request{Kind: KindLogin, AccountID: "c1"}}, func(Phase) {})
	}()
	if err := clock.BlockUntilContext(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	out := <-result
	if out.State != StateFailed || out.Observation.Auth != domain.AuthAccepted || p.count(onlinePath) != 2 || p.count(portalPath) != 1 || clock.Waiters() != 0 {
		t.Fatalf("%+v / %v", out, p.seen())
	}
}

func TestTerminalWaitCancelsWithoutSendingAnotherRequest(t *testing.T) {
	p := newPortal(t)
	p.onlineBody = offlineAnswer
	worker, _ := workerFor(t, p, &fakeBinder{})
	clock := faketime.New(time.Now())
	worker.clock = clock
	worker.settings.(*fakeSettings).cfg.Checks.TerminalAttempts = 20
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watchdog, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	result := make(chan Outcome, 1)
	go func() {
		result <- worker.Run(ctx, Action{Request: Request{Kind: KindLogin, AccountID: "c1"}}, func(Phase) {})
	}()
	if err := clock.BlockUntilContext(watchdog, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case out := <-result:
		if out.Code != domain.CodeCancelled || p.count(onlinePath) != 1 || p.count(portalPath) != 1 || clock.Waiters() != 0 {
			t.Fatalf("%+v / %v", out, p.seen())
		}
	case <-watchdog.Done():
		t.Fatal("cancel did not stop terminal wait")
	}
}

func TestMaintenanceDoesNotUseManualTerminalRetries(t *testing.T) {
	p := newPortal(t)
	p.onlineBody = offlineAnswer
	worker, _ := workerFor(t, p, &fakeBinder{})
	clock := faketime.New(time.Now())
	worker.clock = clock
	worker.settings.(*fakeSettings).cfg.Checks.TerminalAttempts = 20
	out := runWorker(t, worker, KindMaintain)
	if out.State != StateFailed || p.count(onlinePath) != 2 || p.count(portalPath) != 1 || clock.Waiters() != 0 {
		t.Fatalf("%+v / %v", out, p.seen())
	}
}
