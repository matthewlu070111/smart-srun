package application

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestSwitchDrainsWorkersCommitsBeforeNextDispatchAndRejectsStaleQueue(t *testing.T) {
	var revision uint64 = 1
	h := newHarness(t, func(o *Options) {
		o.Check = func(r Request) error {
			if r.CheckRevision && r.ConfigRevision != revision {
				return domain.Errorf(domain.CodeConflict, "stale")
			}
			return nil
		}
		o.Finalize = func(a Action, out Outcome) Outcome {
			if a.Request.Kind.switches() {
				revision++
			}
			return out
		}
	})
	first := h.submit(KindMaintain, "c1", "first")
	h.awaitStart()
	move := h.submit(KindSwitchCampus, "c2", "switch")
	queued, err := h.Submit(t.Context(), Request{Kind: KindMaintain, AccountID: "c3",
		IdempotencyKey: "old-maintenance", CheckRevision: true, ConfigRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.expectNoStart("a switch must drain existing workers")
	h.succeed(first.ActionID)
	if got := h.awaitStart(); got.ID != move.ActionID {
		t.Fatal("maintenance overtook the switch")
	}
	h.expectNoStart("a switch owns the dispatch slot")
	h.succeed(move.ActionID)
	h.awaitState(move.ActionID, StateSucceeded)
	if got := h.awaitState(queued.ActionID, StateFailed); got.Code != domain.CodeConflict {
		t.Fatalf("stale queued work: %+v", got)
	}
	h.expectNoStart("old selection must not reach a worker")
}

func TestCancelledOrOverdueSwitchDoesNotCommit(t *testing.T) {
	for _, overdue := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "overdue"}[overdue], func(t *testing.T) {
			h := newHarness(t, func(o *Options) {
				o.ActionBudget = time.Second
				o.Finalize = func(_ Action, out Outcome) Outcome { t.Error("late result committed"); return out }
			})
			r := h.submit(KindSwitchCampus, "c1", "switch")
			h.awaitStart()
			h.mu.Lock()
			h.linger[r.ActionID] = true
			h.mu.Unlock()
			if overdue {
				h.clock.Advance(time.Second)
			} else {
				h.cancel(r.ActionID)
			}
			h.awaitCancelSeen(r.ActionID)
			h.succeed(r.ActionID)
			if overdue {
				h.awaitState(r.ActionID, StateFailed)
			} else {
				h.awaitState(r.ActionID, StateCancelled)
			}
		})
	}
}
