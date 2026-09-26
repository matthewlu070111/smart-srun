package faketime

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/policy"
)

// A fake clock that is subtly wrong makes every test that uses it wrong in a
// way that looks like a bug in the code under test, so it gets its own tests.

var start = time.Date(2026, 3, 5, 1, 0, 0, 0, time.UTC)

// in arms a timer d from the clock's current reading, which is what a caller
// with a genuinely relative wait writes.
func in(clock *Clock, d time.Duration) policy.Timer {
	return clock.NewTimerAt(clock.Now().Add(d))
}

func TestTimeOnlyMovesWhenItIsMoved(t *testing.T) {
	clock := New(start)
	if !clock.Now().Equal(start) {
		t.Fatalf("now = %v, want %v", clock.Now(), start)
	}
	clock.Advance(90 * time.Second)
	if want := start.Add(90 * time.Second); !clock.Now().Equal(want) {
		t.Errorf("now = %v, want %v", clock.Now(), want)
	}
}

func TestATimerFiresWhenItsDeadlinePasses(t *testing.T) {
	clock := New(start)
	timer := in(clock, 10*time.Second)

	if clock.Waiters() != 1 {
		t.Fatalf("waiters = %d, want 1", clock.Waiters())
	}
	select {
	case at := <-timer.C():
		t.Fatalf("the timer fired at %v before its deadline", at)
	default:
	}

	clock.Advance(9 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("the timer fired one second early")
	default:
	}

	clock.Advance(time.Second)
	select {
	case at := <-timer.C():
		if !at.Equal(start.Add(10 * time.Second)) {
			t.Errorf("fired reporting %v, want the deadline", at)
		}
	default:
		t.Fatal("the timer did not fire at its deadline")
	}
	if clock.Waiters() != 0 {
		t.Errorf("waiters = %d after firing, want 0", clock.Waiters())
	}
}

// A deadline that has already passed fires immediately.
//
// This is what makes the absolute form race-free. A caller that reads the time,
// decides something is due in a second, and is then delayed before arming its
// timer would otherwise sleep a full second measured from the wrong moment.
// Here it wakes at once, because the deadline it computed is already behind it.
func TestADeadlineInThePastFiresImmediately(t *testing.T) {
	clock := New(start)
	for _, d := range []time.Duration{0, -time.Second, -time.Hour} {
		timer := in(clock, d)
		select {
		case <-timer.C():
		default:
			t.Errorf("a deadline %v away did not fire", d)
		}
	}
	if clock.Waiters() != 0 {
		t.Errorf("waiters = %d, want none pending", clock.Waiters())
	}

	// The same thing said the way a delayed scheduler would say it: read the
	// clock, have it move underneath you, then arm.
	stale := clock.Now()
	clock.Advance(time.Hour)
	timer := clock.NewTimerAt(stale.Add(time.Second))
	select {
	case <-timer.C():
	default:
		t.Error("a timer armed from a stale reading slept instead of firing")
	}
}

// Stop reports whether it got there first, which is what time.Timer.Stop
// promises and what a scheduler uses to decide whether to drain the channel.
func TestStopReportsWhetherItWonTheRace(t *testing.T) {
	clock := New(start)

	timer := in(clock, time.Minute)
	if !timer.Stop() {
		t.Error("Stop before firing returned false")
	}
	if timer.Stop() {
		t.Error("a second Stop claimed to have stopped it again")
	}
	clock.Advance(time.Hour)
	select {
	case <-timer.C():
		t.Error("a stopped timer fired anyway")
	default:
	}

	fired := in(clock, time.Minute)
	clock.Advance(time.Minute)
	if fired.Stop() {
		t.Error("Stop after firing returned true")
	}
}

// A backward jump fires nothing. Go's runtime timers run on the monotonic
// clock, so an NTP correction does not move them, and a fake that fired on a
// correction would test behaviour the real one does not have.
func TestABackwardJumpFiresNothing(t *testing.T) {
	clock := New(start)
	timer := in(clock, 10*time.Second)

	clock.Set(start.Add(-time.Hour))
	if !clock.Now().Equal(start.Add(-time.Hour)) {
		t.Fatalf("now = %v, the clock did not move back", clock.Now())
	}
	select {
	case <-timer.C():
		t.Fatal("a backward jump fired a timer")
	default:
	}
	if clock.Waiters() != 1 {
		t.Errorf("waiters = %d, want the timer still pending", clock.Waiters())
	}

	// It keeps its absolute deadline, so moving forward past it still fires.
	clock.Set(start.Add(10 * time.Second))
	select {
	case <-timer.C():
	default:
		t.Error("the timer did not fire after the clock came back")
	}
}

// BlockUntil is the synchronisation a fake clock needs: advancing before the
// code under test has created its timer advances past nothing, and the test
// then waits forever for an event that will never come.
func TestBlockUntilWaitsForATimerToExist(t *testing.T) {
	clock := New(start)
	created := make(chan struct{})

	go func() {
		timer := in(clock, time.Minute)
		<-timer.C()
		close(created)
	}()

	clock.BlockUntil(1)
	clock.Advance(time.Minute)

	select {
	case <-created:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never woke; BlockUntil returned too early")
	}
}

// Several timers due at once all fire, and one that is not due does not.
func TestAdvancingPastSeveralDeadlinesFiresAllOfThem(t *testing.T) {
	clock := New(start)
	near := in(clock, time.Second)
	also := in(clock, 2*time.Second)
	far := in(clock, time.Hour)

	clock.Advance(5 * time.Second)
	for name, timer := range map[string]interface{ C() <-chan time.Time }{
		"near": near, "also": also,
	} {
		select {
		case <-timer.C():
		default:
			t.Errorf("%s did not fire", name)
		}
	}
	select {
	case <-far.C():
		t.Error("a timer an hour out fired after five seconds")
	default:
	}
	if got := clock.Fired(); got != 2 {
		t.Errorf("Fired() = %d, want 2", got)
	}
	if clock.Waiters() != 1 {
		t.Errorf("waiters = %d, want the far one", clock.Waiters())
	}
}
