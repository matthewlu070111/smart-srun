package policy

import (
	"math"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// MinimumInterval is the floor under every scheduled wait.
//
// Spec 04 requires it: a zero cooldown must not become a spin. A user who sets
// the cooldown to 0 is asking to retry as fast as is sensible, not for a router
// CPU pinned at 100% with the flash log filling up. The baseline returned 0
// here and relied on each call site to remember a floor; one of them did not.
const MinimumInterval = 100 * time.Millisecond

// MaxFailures is where the consecutive-failure counter stops counting.
//
// A managed WAN can be down for weeks, so the count has no natural bound. It is
// only ever used to choose a wait that saturates at the cap after a few dozen
// failures, so stopping here loses nothing and removes the overflow that would
// otherwise turn a long outage into a negative delay.
const MaxFailures Failures = math.MaxInt32

// Failures counts consecutive failures, saturating instead of wrapping.
type Failures int32

// Next returns the count after one more failure.
func (f Failures) Next() Failures {
	if f < 0 {
		// Not reachable through Next, but a caller can construct one. Treating
		// it as the first failure is the only answer that keeps the schedule
		// monotone.
		return 1
	}
	if f >= MaxFailures {
		return MaxFailures
	}
	return f + 1
}

// maxShift bounds the exponent so the doubling cannot leave float64's exact
// range. Base is validated below 2^30 seconds, so 2^30 * 2^62 = 2^92 is
// computed exactly and the minimum with the cap brings it straight back down.
const maxShift = 62

// Backoff is spec 04's schedule: min(cap, base*2^(index-1)), saturated before
// the exponentiation rather than after it.
//
// The order matters. Computing base*2^index first and clamping afterwards is
// the version that overflows: an account that has been failing for a day
// reaches an exponent where the multiplication is +Inf, and +Inf clamped to the
// cap looks correct right up until something converts it to a Duration.
type Backoff struct {
	Base domain.Seconds
	Cap  domain.Seconds
}

// Wait returns the delay before the retry that follows failure number index.
//
// index is 1-based: Wait(1) is the pause after the first failure and equals the
// base. Values below 1 are treated as 1, so a caller that has not yet counted a
// failure still gets a bounded, non-zero answer instead of a spin.
//
// A cap below the base yields the cap. That is what min() means, and config
// validation refuses the combination anyway; defining it here keeps the
// function total so no caller has to pre-check.
func (b Backoff) Wait(index Failures) time.Duration {
	shift := int(index) - 1
	if shift < 0 {
		shift = 0
	}
	if shift > maxShift {
		shift = maxShift
	}

	wait := float64(b.Base) * float64(uint64(1)<<uint(shift))
	if ceiling := float64(b.Cap); wait > ceiling {
		wait = ceiling
	}
	return clampInterval(time.Duration(wait * float64(time.Second)))
}

// clampInterval applies the scheduling floor.
func clampInterval(d time.Duration) time.Duration {
	if d < MinimumInterval {
		return MinimumInterval
	}
	return d
}
