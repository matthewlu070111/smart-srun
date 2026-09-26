package policy

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func clockAt(t *testing.T, text string) domain.ClockTime {
	t.Helper()
	parsed, err := domain.ParseClockTime(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return parsed
}

func quietConfig(t *testing.T, start, end string) domain.QuietConfig {
	t.Helper()
	return domain.QuietConfig{
		Enabled: true, ForceLogout: true,
		Start: clockAt(t, start), End: clockAt(t, end),
	}
}

// beijing builds an instant in the fixed zone the window is defined in.
func beijing(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, domain.Beijing)
}

// T20 -- a same-day window is half-open: the start is inside, the end is not.
//
// Both edges matter. An inclusive end would log an account out at exactly the
// moment the window closes and then immediately log it back in.
func TestASameDayWindowIsHalfOpen(t *testing.T) {
	config := quietConfig(t, "01:00", "05:00")
	cases := []struct {
		at   time.Time
		want bool
	}{
		{beijing(2026, 3, 5, 0, 59), false},
		{beijing(2026, 3, 5, 1, 0), true},
		{beijing(2026, 3, 5, 3, 0), true},
		{beijing(2026, 3, 5, 4, 59), true},
		{beijing(2026, 3, 5, 5, 0), false},
		{beijing(2026, 3, 5, 23, 59), false},
	}
	for _, testCase := range cases {
		if got := EvaluateQuiet(config, testCase.at).Active; got != testCase.want {
			t.Errorf("%s: active = %v, want %v",
				testCase.at.Format(time.RFC3339), got, testCase.want)
		}
	}
}

// T20 -- a window that crosses midnight is the union of two pieces, and the
// piece after midnight belongs to the occurrence that began the day before.
//
// This is the real configuration: the user's own quiet hours are 00:09 to
// 05:40, and the campus network is physically off for most of it.
func TestAWindowCrossingMidnightIsOneOccurrence(t *testing.T) {
	config := quietConfig(t, "23:00", "06:00")

	for _, at := range []time.Time{
		beijing(2026, 3, 5, 23, 0),
		beijing(2026, 3, 5, 23, 59),
		beijing(2026, 3, 6, 0, 0),
		beijing(2026, 3, 6, 5, 59),
	} {
		if !EvaluateQuiet(config, at).Active {
			t.Errorf("%s should be inside the window", at.Format(time.RFC3339))
		}
	}
	for _, at := range []time.Time{
		beijing(2026, 3, 5, 22, 59),
		beijing(2026, 3, 6, 6, 0),
		beijing(2026, 3, 6, 12, 0),
	} {
		if EvaluateQuiet(config, at).Active {
			t.Errorf("%s should be outside the window", at.Format(time.RFC3339))
		}
	}

	// Before and after midnight are the same visit, so the forced logout that
	// ran at 23:05 does not run again at 00:05.
	before := EvaluateQuiet(config, beijing(2026, 3, 5, 23, 5)).Occurrence
	after := EvaluateQuiet(config, beijing(2026, 3, 6, 0, 5)).Occurrence
	if before == "" || before != after {
		t.Errorf("occurrence changed across midnight: %q then %q", before, after)
	}
	tomorrow := EvaluateQuiet(config, beijing(2026, 3, 6, 23, 5)).Occurrence
	if tomorrow == before {
		t.Error("the next night reported the same occurrence as the last one")
	}
}

// T20 -- equal endpoints mean an empty window.
//
// Reading it as all-day would log every account out forever the moment somebody
// typed the same value into both boxes, and the interface offers no clue that
// this is what happened.
func TestEqualEndpointsAreAnEmptyWindow(t *testing.T) {
	config := quietConfig(t, "03:00", "03:00")
	for _, at := range []time.Time{
		beijing(2026, 3, 5, 2, 59),
		beijing(2026, 3, 5, 3, 0),
		beijing(2026, 3, 5, 3, 1),
		beijing(2026, 3, 5, 15, 0),
	} {
		state := EvaluateQuiet(config, at)
		if state.Active {
			t.Errorf("%s was inside an empty window", at.Format(time.RFC3339))
		}
		if state.HasNext {
			t.Error("an empty window reported a boundary to wait for")
		}
	}
}

// A disabled window is not a window. Nothing waits for its edges either.
func TestADisabledWindowIsInert(t *testing.T) {
	config := quietConfig(t, "01:00", "05:00")
	config.Enabled = false
	state := EvaluateQuiet(config, beijing(2026, 3, 5, 3, 0))
	if state.Active || state.HasNext || state.ForceLogout {
		t.Errorf("a disabled window produced %+v", state)
	}
}

// T20 -- the window is in Beijing time whatever the router's clock zone is.
//
// Reading the local zone would move the window when somebody fixes the
// timezone, which on a router that boots with the wrong one is the first thing
// they do.
func TestTheWindowIsEvaluatedInBeijingTime(t *testing.T) {
	config := quietConfig(t, "01:00", "05:00")

	// 18:00 UTC on the 4th is 02:00 Beijing on the 5th.
	inside := time.Date(2026, 3, 4, 18, 0, 0, 0, time.UTC)
	if !EvaluateQuiet(config, inside).Active {
		t.Error("a UTC instant inside the Beijing window was read as outside")
	}
	// The same wall-clock reading in a different zone is a different instant.
	outside := time.Date(2026, 3, 5, 2, 0, 0, 0, time.UTC)
	if EvaluateQuiet(config, outside).Active {
		t.Error("02:00 UTC was treated as 02:00 Beijing")
	}
}

// T20 -- a clock correction inside the window does not start a second visit.
//
// A router with no RTC boots in 1970 and jumps forward when NTP answers. If the
// occupancy were counted per tick, that jump would look like a new window and
// log every account out again.
func TestAClockCorrectionInsideTheWindowKeepsTheOccurrence(t *testing.T) {
	config := quietConfig(t, "01:00", "05:00")
	first := EvaluateQuiet(config, beijing(2026, 3, 5, 2, 0))

	for _, corrected := range []time.Time{
		beijing(2026, 3, 5, 1, 30), // the clock was running fast
		beijing(2026, 3, 5, 4, 45), // it was running slow
	} {
		state := EvaluateQuiet(config, corrected)
		if !state.Active {
			t.Fatalf("%s left the window", corrected.Format(time.RFC3339))
		}
		if state.Occurrence != first.Occurrence {
			t.Errorf("a correction to %s produced a new occurrence %q, was %q",
				corrected.Format(time.RFC3339), state.Occurrence, first.Occurrence)
		}
	}

	// A jump right out of the window is a different matter: it really is over.
	if EvaluateQuiet(config, beijing(2026, 3, 5, 6, 0)).Active {
		t.Error("a jump past the end left the window active")
	}
}

// T20 -- the next boundary is what the scheduler waits for, so it has to be the
// next actual change and never in the past.
func TestTheNextBoundaryIsTheNextChange(t *testing.T) {
	config := quietConfig(t, "01:00", "05:00")
	cases := []struct {
		at   time.Time
		want time.Time
	}{
		{beijing(2026, 3, 5, 0, 30), beijing(2026, 3, 5, 1, 0)},
		{beijing(2026, 3, 5, 2, 0), beijing(2026, 3, 5, 5, 0)},
		{beijing(2026, 3, 5, 6, 0), beijing(2026, 3, 6, 1, 0)},
		{beijing(2026, 3, 5, 23, 59), beijing(2026, 3, 6, 1, 0)},
	}
	for _, testCase := range cases {
		state := EvaluateQuiet(config, testCase.at)
		if !state.HasNext {
			t.Fatalf("%s: no boundary", testCase.at.Format(time.RFC3339))
		}
		if !state.Next.Equal(testCase.want) {
			t.Errorf("%s: next = %s, want %s", testCase.at.Format(time.RFC3339),
				state.Next.Format(time.RFC3339), testCase.want.Format(time.RFC3339))
		}
		if !state.Next.After(testCase.at) {
			t.Errorf("%s: the boundary is not in the future",
				testCase.at.Format(time.RFC3339))
		}
	}
}

// Forced logout is a property of being inside the window, not of the settings
// alone: outside it there is nothing to sweep.
func TestForcedLogoutIsOnlyReportedInsideTheWindow(t *testing.T) {
	config := quietConfig(t, "01:00", "05:00")
	if !EvaluateQuiet(config, beijing(2026, 3, 5, 2, 0)).ForceLogout {
		t.Error("forced logout was not reported inside the window")
	}
	if EvaluateQuiet(config, beijing(2026, 3, 5, 12, 0)).ForceLogout {
		t.Error("forced logout was reported outside the window")
	}
	config.ForceLogout = false
	if EvaluateQuiet(config, beijing(2026, 3, 5, 2, 0)).ForceLogout {
		t.Error("forced logout was reported although it is switched off")
	}
}

// T20/T22 -- quiet hours suspend the automatic loop, not the user.
func TestQuietHoursDoNotBlockAUserAction(t *testing.T) {
	active := EvaluateQuiet(quietConfig(t, "01:00", "05:00"), beijing(2026, 3, 5, 2, 0))

	if QuietPermits(active, PriorityMaintenance, false) {
		t.Error("maintenance ran inside quiet hours")
	}
	if QuietPermits(active, PriorityPeriodic, false) {
		t.Error("a periodic observation ran inside quiet hours")
	}
	if !QuietPermits(active, PriorityUserAction, false) {
		t.Error("a user's own action was blocked by quiet hours")
	}
	if !QuietPermits(active, PriorityFreeze, false) {
		t.Error("a service stop was blocked by quiet hours")
	}
	// The single-action override lifts it for that action only; the state it
	// was evaluated against is unchanged, so the loop behind it is still
	// suspended.
	if !QuietPermits(active, PriorityMaintenance, true) {
		t.Error("the explicit override did not apply")
	}
	if QuietPermits(active, PriorityMaintenance, false) {
		t.Error("the override leaked into the next decision")
	}

	inactive := EvaluateQuiet(quietConfig(t, "01:00", "05:00"), beijing(2026, 3, 5, 12, 0))
	if !QuietPermits(inactive, PriorityPeriodic, false) {
		t.Error("everything was blocked outside the window")
	}
}
