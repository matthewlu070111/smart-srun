// Package policy holds the scheduling decisions as pure computation.
//
// Nothing here performs I/O and nothing here reads the wall clock directly:
// time arrives through Clock. That is what lets the tests run a hundred
// thousand consecutive failures, cross midnight in both directions, and move
// the clock backwards -- in microseconds, without a single sleep. Spec 05
// forbids sleep-driven unit tests, and this is the seam that makes obeying it
// possible rather than merely intended.
//
// The split from application is deliberate. Everything here can be checked by
// asking a question and comparing an answer; the coordinator next door has
// goroutines, cancellation and ordering, which need a different kind of test.
// Keeping the arithmetic out of the concurrency means a wrong backoff can be
// found without starting anything.
package policy

import "time"

// Clock is the scheduler's entire view of time.
//
// Two methods, because those are the two questions asked: what time is it, and
// wake me later. There is no Sleep: a wait that cannot be cancelled is how a
// user action ends up queued behind a sixty-second backoff, which spec 04
// rules out by requiring cancellation to propagate within 500ms.
type Clock interface {
	Now() time.Time
	// NewTimerAt returns a timer that fires once at an instant. A deadline that
	// has already passed fires immediately.
	//
	// Absolute rather than relative, and that is the whole point. A scheduler
	// reads the time, decides something is due in one second, and then arms a
	// one-second timer -- but if anything delayed it in between, the wait it
	// arms is measured from the wrong moment and it oversleeps. Passing the
	// deadline it computed makes that impossible: the timer either fires at the
	// instant that was meant or fires at once because it is late. A caller with
	// a genuinely relative wait writes clock.NewTimerAt(now.Add(d)) using the
	// `now` its decision was based on.
	NewTimerAt(deadline time.Time) Timer
}

// Timer is a single-shot, cancellable wait.
type Timer interface {
	C() <-chan time.Time
	// Stop reports whether it stopped the timer before it fired.
	Stop() bool
}

// SystemClock is the real clock. It is the only implementation that ships.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) NewTimerAt(deadline time.Time) Timer {
	// time.NewTimer with a non-positive duration fires immediately, which is
	// what a deadline in the past should do.
	return systemTimer{timer: time.NewTimer(time.Until(deadline))}
}

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.timer.C }
func (t systemTimer) Stop() bool          { return t.timer.Stop() }
