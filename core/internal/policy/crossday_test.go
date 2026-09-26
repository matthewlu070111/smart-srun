// This file is package policy_test rather than package policy because it uses
// the fake clock, and the fake clock imports policy. Everything it touches is
// exported, which is the point: a week of quiet hours is answerable from
// outside the package.
package policy_test

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func mustClock(t *testing.T, text string) domain.ClockTime {
	t.Helper()
	parsed, err := domain.ParseClockTime(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return parsed
}

// T20 -- a week of nights, ten minutes at a time, on a clock that only moves
// when this test moves it.
//
// The single-instant tests each check one rule. This checks that the rules
// compose over time, which is where quiet hours actually go wrong: the window
// opens 42 ticks before it closes, and a sweep that forgot what it had already
// done would log every managed account out 42 times a night. Seven nights also
// catches the opposite mistake -- a sweep that remembers too well and never
// runs again after the first one.
func TestAWeekOfNightsSweepsEachAccountOncePerNight(t *testing.T) {
	config := domain.QuietConfig{
		Enabled: true, ForceLogout: true,
		Start: mustClock(t, "23:00"), End: mustClock(t, "06:00"),
	}
	targets := []policy.Target{
		{AccountID: "wan-a", Line: "wan"},
		{AccountID: "wan-b", Line: "wan2"},
	}

	clock := faketime.New(time.Date(2026, 3, 1, 12, 0, 0, 0, domain.Beijing))
	var sweep policy.Sweep

	logouts := map[string]int{}
	var nights []string
	const step = 10 * time.Minute
	for range int(7 * 24 * time.Hour / step) {
		state := policy.EvaluateQuiet(config, clock.Now())
		if state.Active && state.ForceLogout {
			if _, seen := logouts[state.Occurrence]; !seen {
				nights = append(nights, state.Occurrence)
				logouts[state.Occurrence] = 0
			}
			for _, target := range sweep.Pending(state.Occurrence, targets) {
				sweep.Succeeded(state.Occurrence, target.AccountID)
				logouts[state.Occurrence]++
			}
		}
		clock.Advance(step)
	}

	if len(nights) != 7 {
		t.Fatalf("saw %d nights in a week: %v", len(nights), nights)
	}
	for _, night := range nights {
		if logouts[night] != len(targets) {
			t.Errorf("%s logged out %d times, want %d -- once per account",
				night, logouts[night], len(targets))
		}
	}
}

// T20 -- an NTP correction in the middle of a window does not start the night
// over.
//
// A router with no clock battery boots in 1970 and jumps forward the moment NTP
// answers, and it is just as likely to jump backwards after a bad reading. If
// either one looked like a new window, every managed account would be logged
// out again.
func TestAClockCorrectionMidWindowDoesNotSweepTwice(t *testing.T) {
	config := domain.QuietConfig{
		Enabled: true, ForceLogout: true,
		Start: mustClock(t, "23:00"), End: mustClock(t, "06:00"),
	}
	targets := []policy.Target{{AccountID: "wan-a", Line: "wan"}}

	clock := faketime.New(time.Date(2026, 3, 1, 23, 30, 0, 0, domain.Beijing))
	var sweep policy.Sweep
	swept := 0

	run := func() {
		state := policy.EvaluateQuiet(config, clock.Now())
		if !state.Active || !state.ForceLogout {
			return
		}
		for _, target := range sweep.Pending(state.Occurrence, targets) {
			sweep.Succeeded(state.Occurrence, target.AccountID)
			swept++
		}
	}

	run()
	if swept != 1 {
		t.Fatalf("the first pass swept %d times, want 1", swept)
	}

	// Forward past midnight, backward before it, forward again -- all inside
	// the same night.
	for _, at := range []time.Time{
		time.Date(2026, 3, 2, 1, 0, 0, 0, domain.Beijing),
		time.Date(2026, 3, 1, 23, 45, 0, 0, domain.Beijing),
		time.Date(2026, 3, 2, 5, 0, 0, 0, domain.Beijing),
	} {
		clock.Set(at)
		run()
	}
	if swept != 1 {
		t.Errorf("swept %d times across one night's clock corrections, want 1", swept)
	}

	// The next night is a different night, correction or not.
	clock.Set(time.Date(2026, 3, 2, 23, 30, 0, 0, domain.Beijing))
	run()
	if swept != 2 {
		t.Errorf("swept %d times over two nights, want 2", swept)
	}
}
