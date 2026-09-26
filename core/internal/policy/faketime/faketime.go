// Package faketime is a controllable policy.Clock for tests.
//
// It is a package of its own rather than a file in policy so that "test only"
// is a fact instead of a comment: nothing the daemon links imports it, and the
// architecture test enforces that only _test.go files may.
//
// The point of it is spec 05's ban on sleep-driven unit tests. A backoff that
// reaches a sixty-second cap cannot be tested by waiting sixty seconds, and a
// quiet window that crosses midnight cannot be tested at all without moving
// time. With this, both are arithmetic: Advance, then assert.
package faketime

import (
	"context"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/policy"
)

// Clock is a policy.Clock whose time only moves when a test moves it.
type Clock struct {
	mu      sync.Mutex
	changed *sync.Cond
	now     time.Time
	waiting []*Timer
	// leaked counts timers that fired with nobody listening. A scheduler that
	// abandons a timer without stopping it leaks one goroutine's worth of state
	// per retry on a real clock; here it is countable.
	fired int
}

// New returns a clock reading `at`.
func New(at time.Time) *Clock {
	clock := &Clock{now: at}
	clock.changed = sync.NewCond(&clock.mu)
	return clock
}

// Now reports the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimerAt registers a wait. A deadline that has already passed fires
// immediately, which is what makes the absolute form race-free: a test that
// advanced the clock between the caller reading Now and arming the timer still
// gets the wake-up rather than a wait measured from the wrong moment.
func (c *Clock) NewTimerAt(deadline time.Time) policy.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()

	timer := &Timer{clock: c, deadline: deadline, signal: make(chan time.Time, 1)}
	if deadline.After(c.now) {
		c.waiting = append(c.waiting, timer)
		c.changed.Broadcast()
		return timer
	}
	timer.fire(c.now)
	c.fired++
	return timer
}

// Advance moves time forward and fires everything that comes due.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(c.now.Add(d))
}

// Set jumps the clock to an absolute instant.
//
// Backward jumps -- an NTP correction, or a router whose clock was wrong until
// the first sync -- fire nothing and reschedule nothing, which is what the real
// runtime does: Go's timers run on the monotonic clock and a wall-clock
// correction does not move them. The code under test is expected to notice the
// jump by re-reading Now, which is what spec 04 requires of the quiet window.
//
// That needs no special case. Every waiting timer's deadline is ahead of the
// clock by construction -- NewTimerAt fires anything else immediately -- so
// moving the clock back leaves all of them still ahead of it. An earlier
// version passed a "should this fire anything" flag down from here; a mutation
// proved the flag decided nothing, because the deadline comparison below had
// already decided it.
func (c *Clock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(at)
}

func (c *Clock) setLocked(at time.Time) {
	c.now = at
	remaining := c.waiting[:0]
	for _, timer := range c.waiting {
		if timer.deadline.After(c.now) {
			remaining = append(remaining, timer)
			continue
		}
		timer.fire(c.now)
		c.fired++
	}
	c.waiting = remaining
	c.changed.Broadcast()
}

// Waiters is how many timers are pending.
func (c *Clock) Waiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiting)
}

// Fired is how many timers have gone off.
func (c *Clock) Fired() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fired
}

// BlockUntil waits for at least n pending timers.
//
// This is the synchronisation a fake clock needs and a real one does not. A
// test that advances the clock before the code under test has created its timer
// advances past nothing, and then waits forever for an event that will now
// never come -- a flake that only appears on a loaded machine. Waiting for the
// timer to exist first makes the sequence deterministic.
func (c *Clock) BlockUntil(n int) {
	_ = c.BlockUntilContext(context.Background(), n)
}

// BlockUntilContext is BlockUntil with a way out.
//
// A test that drives the clock in a loop -- wait for a timer, advance past it,
// repeat -- has no way to know the code under test has stopped making them, so
// the last iteration would block forever. Cancelling is how that driver stops
// instead of leaking a goroutine for the rest of the run.
func (c *Clock) BlockUntilContext(ctx context.Context, n int) error {
	// sync.Cond has no deadline, so cancellation has to arrive as a broadcast.
	stop := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.changed.Broadcast()
	})
	defer stop()

	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.waiting) < n {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.changed.Wait()
	}
	return nil
}

// Timer is one pending wait.
type Timer struct {
	clock    *Clock
	deadline time.Time
	signal   chan time.Time
	done     bool
}

func (t *Timer) C() <-chan time.Time { return t.signal }

// Stop reports whether it stopped the timer before it fired, matching
// time.Timer.Stop.
func (t *Timer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.done {
		return false
	}
	t.done = true
	for index, pending := range t.clock.waiting {
		if pending == t {
			t.clock.waiting = append(t.clock.waiting[:index], t.clock.waiting[index+1:]...)
			t.clock.changed.Broadcast()
			return true
		}
	}
	return false
}

// fire must be called with the clock locked.
func (t *Timer) fire(at time.Time) {
	if t.done {
		return
	}
	t.done = true
	select {
	case t.signal <- at:
	default:
	}
}
