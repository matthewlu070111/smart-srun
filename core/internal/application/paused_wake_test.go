package application

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func TestPausedMaintenanceDoesNotSpinOnExpiredAccountDeadline(t *testing.T) {
	for _, reason := range []string{"disabled", "quiet"} {
		t.Run(reason, func(t *testing.T) {
			settings := maintainWorld()
			loop, sink := maintainerFor(t, settings, faketime.New(maintainEpoch))
			loop.tick(t.Context(), maintainEpoch)
			loop.apply(Action{ID: loop.stateFor("c1").inFlight, Request: sink.submitted[0], State: StateSucceeded}, maintainEpoch)
			if reason == "disabled" {
				settings.cfg.Enabled = false
			} else {
				settings.cfg.Quiet = domain.QuietConfig{Enabled: true, Start: at(t, "20:00"), End: at(t, "22:00")}
			}
			now := maintainEpoch.Add(2 * time.Minute)
			wake := loop.tick(t.Context(), now)
			if !wake.Equal(now.Add(time.Minute)) || len(sink.submitted) != 1 {
				t.Fatalf("paused loop spins or queues work: wake=%v requests=%+v", wake, sink.submitted)
			}
			settings.cfg.Enabled = true
			settings.cfg.Quiet.Enabled = false
			loop.tick(t.Context(), wake)
			if len(sink.submitted) != 2 {
				t.Fatal("resuming lost the overdue check")
			}
		})
	}
}
