package policy

import (
	"math"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func seconds(value float64) domain.Seconds { return domain.Seconds(value) }

// T19 -- the documented schedule, term by term.
func TestTheBackoffDoublesUntilItReachesTheCap(t *testing.T) {
	backoff := Backoff{Base: seconds(10), Cap: seconds(60)}

	want := []time.Duration{
		10 * time.Second, 20 * time.Second, 40 * time.Second,
		60 * time.Second, 60 * time.Second, 60 * time.Second,
	}
	for index, expected := range want {
		if got := backoff.Wait(Failures(index + 1)); got != expected {
			t.Errorf("Wait(%d) = %v, want %v", index+1, got, expected)
		}
	}
}

// T19 -- a zero base is still a schedule, not a spin.
//
// The baseline returned 0 here and left each caller to remember a floor. One of
// them did; the wired retry path multiplied by max(..., 1). The others were one
// edit away from a router at 100% CPU writing a log line per microsecond.
func TestAZeroCooldownStillWaits(t *testing.T) {
	for _, backoff := range []Backoff{
		{Base: 0, Cap: 0},
		{Base: 0, Cap: seconds(60)},
		{Base: seconds(0.001), Cap: seconds(60)},
	} {
		for _, index := range []Failures{1, 2, 10} {
			if got := backoff.Wait(index); got < MinimumInterval {
				t.Errorf("%+v Wait(%d) = %v, below the %v floor",
					backoff, index, got, MinimumInterval)
			}
		}
	}
}

// T19 -- a hundred thousand consecutive failures.
//
// This is the shape spec 04 asks for and the reason the saturation happens
// before the exponentiation. The naive version computes base*2^(n-1) first: on
// a uint64 shift that silently becomes zero once the count passes 63, so an
// account that has been failing for a day gets the 100ms floor instead of the
// sixty-second cap and hammers the gateway exactly when it is least likely to
// help.
func TestAHundredThousandFailuresStayAtTheCap(t *testing.T) {
	backoff := Backoff{Base: seconds(10), Cap: seconds(60)}

	previous := time.Duration(0)
	for index := Failures(1); index <= 100000; index++ {
		wait := backoff.Wait(index)
		switch {
		case wait < MinimumInterval:
			t.Fatalf("Wait(%d) = %v, below the floor", index, wait)
		case wait > 60*time.Second:
			t.Fatalf("Wait(%d) = %v, above the cap", index, wait)
		case wait < previous:
			t.Fatalf("Wait(%d) = %v went backwards from %v", index, wait, previous)
		}
		previous = wait
	}
	if got := backoff.Wait(100000); got != 60*time.Second {
		t.Errorf("Wait(100000) = %v, want the cap", got)
	}
}

// The largest base the configuration allows, at the largest failure count, must
// still be a number. This is the overflow the ordering exists to avoid.
func TestTheLargestPermittedValuesDoNotOverflow(t *testing.T) {
	// Just under the 2^30 ceiling domain.Seconds enforces.
	backoff := Backoff{Base: seconds(1 << 29), Cap: seconds(1 << 29)}
	for _, index := range []Failures{1, 63, 64, 1000, MaxFailures} {
		wait := backoff.Wait(index)
		if wait <= 0 || wait > time.Duration(1<<29)*time.Second {
			t.Errorf("Wait(%d) = %v, outside the representable range", index, wait)
		}
	}
}

// A count that cannot grow further must not wrap into a negative one, which
// would turn the longest outage into the shortest wait.
func TestTheFailureCountSaturates(t *testing.T) {
	if got := MaxFailures.Next(); got != MaxFailures {
		t.Errorf("MaxFailures.Next() = %d, want it to stay put", got)
	}
	if got := (MaxFailures - 1).Next(); got != MaxFailures {
		t.Errorf("Next() at the boundary = %d, want %d", got, MaxFailures)
	}
	if MaxFailures != math.MaxInt32 {
		t.Errorf("MaxFailures = %d; it must fit the counter's own type", MaxFailures)
	}
	if got := Failures(-5).Next(); got != 1 {
		t.Errorf("Next() from a negative count = %d, want 1", got)
	}
	if got := Failures(0).Next(); got != 1 {
		t.Errorf("Next() from zero = %d, want 1", got)
	}
}

// Wait is total: it answers for indices a caller should not pass, rather than
// leaving a spin or a panic behind an unchecked precondition.
func TestWaitIsDefinedOutsideTheExpectedRange(t *testing.T) {
	backoff := Backoff{Base: seconds(10), Cap: seconds(60)}
	for _, index := range []Failures{-1, 0} {
		if got := backoff.Wait(index); got != 10*time.Second {
			t.Errorf("Wait(%d) = %v, want the base", index, got)
		}
	}
	// A cap below the base is refused by config validation, but min() still has
	// an answer and the function must not need the caller to pre-check.
	inverted := Backoff{Base: seconds(60), Cap: seconds(10)}
	if got := inverted.Wait(1); got != 10*time.Second {
		t.Errorf("an inverted pair gave %v, want the cap", got)
	}
}

// T19 -- backoff switched off is one flat retry, which is what 1.6 did.
//
// Reading it as "no retries" would quietly remove a retry users have today, and
// reading it as an unbounded flat loop would hammer the gateway. Neither is a
// change this rewrite is allowed to make by accident.
func TestBackoffOffIsOneFlatRetry(t *testing.T) {
	plan := PlanFrom(domain.RetryConfig{
		Enabled: false, MaxRetries: 4,
		InitialSeconds: seconds(10), MaxSeconds: seconds(60),
	})

	if got := plan.RoundLimit(); got != 2 {
		t.Errorf("RoundLimit = %d, want the first attempt plus one retry", got)
	}
	for _, failures := range []Failures{1, 2, 9} {
		if got := plan.Wait(failures); got != 10*time.Second {
			t.Errorf("Wait(%d) = %v, want the flat cooldown", failures, got)
		}
	}
	if !plan.RoundExhausted(2) {
		t.Error("the round did not end after the single retry")
	}
	if plan.RoundExhausted(1) {
		t.Error("the round ended before the retry happened")
	}
}

// T19 -- an unlimited round is unlimited, and the cooldown between rounds is
// still bounded.
//
// Long-running maintenance must not stop permanently because one round used its
// retries. The baseline's own comment on the max_retries default records the
// other side of it: a round that never ends traps the daemon tick, so the state
// snapshot stops refreshing and the recovery path never runs.
func TestMaintenanceIsUnboundedInRoundsButBoundedInWaiting(t *testing.T) {
	unlimited := PlanFrom(domain.RetryConfig{
		Enabled: true, MaxRetries: 0,
		InitialSeconds: seconds(10), MaxSeconds: seconds(60),
	})
	for _, failures := range []Failures{1, 5, 1000, MaxFailures} {
		if unlimited.RoundExhausted(failures) {
			t.Fatalf("an unlimited round ended after %d failures", failures)
		}
		if wait := unlimited.Wait(failures); wait > 60*time.Second {
			t.Fatalf("Wait(%d) = %v, above the cap", failures, wait)
		}
	}

	limited := PlanFrom(domain.RetryConfig{
		Enabled: true, MaxRetries: 4,
		InitialSeconds: seconds(10), MaxSeconds: seconds(60),
	})
	if !limited.RoundExhausted(5) {
		t.Error("four retries after the first attempt did not end the round")
	}
	if limited.RoundExhausted(4) {
		t.Error("the round ended one attempt early; max_retries counts retries, " +
			"not total attempts")
	}
	if got := limited.RoundCooldown(); got != 60*time.Second {
		t.Errorf("RoundCooldown = %v, want a bounded wait", got)
	}
	zero := RetryPlan{Enabled: true}
	if got := zero.RoundCooldown(); got < MinimumInterval {
		t.Errorf("RoundCooldown with no cap = %v, below the floor", got)
	}
}
