package policy

import (
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// QuietState is everything the scheduler needs to know about quiet hours at one
// instant.
//
// It is a value computed from the configuration and a timestamp, with no memory
// of its own. A scheduler that remembered "we are in quiet hours" would keep
// believing it after the clock was corrected; recomputing from the instant is
// what makes a clock jump self-healing.
type QuietState struct {
	// Active is whether authentication is suspended right now.
	Active bool
	// Occurrence identifies this single visit to the window, derived from the
	// instant the window opened. A clock correction inside the window maps to
	// the same occurrence, so the forced-logout sweep does not run twice.
	// Empty when Active is false.
	Occurrence string
	// ForceLogout is whether this occurrence must log the managed accounts out.
	ForceLogout bool
	// Next is the instant membership changes, and HasNext says whether there is
	// one. The scheduler waits until then instead of polling, and re-derives it
	// after any clock jump.
	Next    time.Time
	HasNext bool
}

// EvaluateQuiet answers the quiet-hours question for one instant.
//
// A disabled window has no boundary to wake for. An empty window
// (start == end) is empty rather than all-day -- the difference between a user
// typing the same value twice and every account being logged out forever -- but
// that rule is domain.QuietWindow's, and it is not repeated here: an empty
// window contains no instant and has no next boundary, so everything below
// already answers correctly for it. Deciding it twice would be two places to
// keep in agreement.
func EvaluateQuiet(cfg domain.QuietConfig, at time.Time) QuietState {
	if !cfg.Enabled {
		return QuietState{}
	}

	window := cfg.Window()
	state := QuietState{ForceLogout: cfg.ForceLogout}
	state.Occurrence, state.Active = window.OccurrenceID(at)
	state.Next, state.HasNext = window.NextBoundary(at)
	if !state.Active {
		state.ForceLogout = false
	}
	return state
}

// QuietPermits reports whether an action may run under this quiet state.
//
// Automatic maintenance is what quiet hours suspend. An explicit user action
// still runs: spec 04 allows a single-action override, and a user who opens the
// page at 02:00 and presses 登录 has said what they want more clearly than the
// schedule has. The override applies to that action only -- it does not lift
// the window for the maintenance loop behind it.
func QuietPermits(state QuietState, priority Priority, override bool) bool {
	if !state.Active {
		return true
	}
	if override {
		return true
	}
	return priority >= PriorityUserAction
}
