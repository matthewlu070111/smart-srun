package application

import (
	"context"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func TestConfigurationWaitsForCancelledWorkerToActuallyExit(t *testing.T) {
	h := newHarness(t, nil)
	receipt := h.submit(KindLogin, "c1", "login")
	h.awaitStart()
	h.mu.Lock()
	h.linger[receipt.ActionID] = true
	h.mu.Unlock()
	h.cancel(receipt.ActionID)
	h.awaitCancelSeen(receipt.ActionID)
	wrote := false
	change := func() error { wrote = true; return nil }
	err := h.ChangeConfiguration(t.Context(), change)
	if codeOf(t, err) != domain.CodeBusy || wrote {
		t.Fatalf("save during cancellation: wrote=%v error=%v", wrote, err)
	}
	h.succeed(receipt.ActionID)
	deadline := time.Now().Add(patience)
	for {
		err = h.ChangeConfiguration(t.Context(), change)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("save stayed busy after the worker exited")
		}
	}
	if !wrote {
		t.Fatal("idle save did not run")
	}
}

func TestConfigurationChangeResetsMaintenanceBackoffAndRejectsOldResults(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.Revision = 1
	loop, sink := maintainerFor(t, settings, faketime.New(maintainEpoch))
	loop.tick(t.Context(), maintainEpoch)
	old := sink.submitted[0]
	settings.cfg.Revision = 2
	settings.cfg.CampusAccounts[0].UserID = "replacement"
	loop.tick(t.Context(), maintainEpoch)
	if len(sink.submitted) != 2 || sink.submitted[1].ConfigRevision != 2 {
		t.Fatal("new configuration inherited the old in-flight/backoff state")
	}
	state := loop.stateFor("c1")
	newFlight := state.inFlight
	loop.apply(Action{ID: newFlight, Request: old, State: StateFailed}, maintainEpoch)
	if state.inFlight != newFlight {
		t.Fatal("a completion from an old revision changed the current maintenance round")
	}
}

func TestConfigurationRejectsStaleSubmissionAndCancelledSave(t *testing.T) {
	var revision uint64
	h := newHarness(t, func(o *Options) {
		o.Check = func(r Request) error {
			if r.CheckRevision && r.ConfigRevision != revision {
				return domain.Errorf(domain.CodeConflict, "stale")
			}
			return nil
		}
	})
	if err := h.ChangeConfiguration(t.Context(), func() error { revision++; return nil }); err != nil {
		t.Fatal(err)
	}
	err := h.submitExpectingError(Request{Kind: KindLogin, AccountID: "c1", IdempotencyKey: "old-page", CheckRevision: true})
	if codeOf(t, err) != domain.CodeConflict {
		t.Fatal(err)
	}
	h.expectNoStart("stale account configuration")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = h.ChangeConfiguration(ctx, func() error { t.Error("cancelled save ran"); return nil })
	if codeOf(t, err) != domain.CodeCancelled {
		t.Fatal(err)
	}
}
