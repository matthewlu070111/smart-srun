package policy

import (
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// RetryPlan is the whole retry policy for one account.
//
// It is built from RetryConfig once and then asked questions; nothing reads the
// configuration again mid-round, so turning a knob cannot change the meaning of
// a round that is already half over.
type RetryPlan struct {
	// Enabled false is the baseline's "backoff off", which is not "no retries".
	// 1.6 with backoff_enable=0 still made exactly one more attempt after a
	// flat retry_cooldown_seconds wait (orchestrator.run_once_with_retry). That
	// is behaviour users rely on, so it is preserved rather than reinterpreted
	// as zero retries.
	Enabled bool
	// MaxRetries counts attempts *after* the first, matching the baseline's
	// retries counter and the "重试次数" label the UI still shows. 4 means at
	// most five attempts. 0 means no finite cap: the round is ended by
	// cancellation or policy instead.
	MaxRetries int
	Backoff    Backoff
}

// PlanFrom builds the plan from the persisted configuration.
func PlanFrom(cfg domain.RetryConfig) RetryPlan {
	return RetryPlan{
		Enabled:    cfg.Enabled,
		MaxRetries: cfg.MaxRetries,
		Backoff:    Backoff{Base: cfg.InitialSeconds, Cap: cfg.MaxSeconds},
	}
}

// flatRoundAttempts is the attempt count of a round with backoff switched off:
// the first attempt plus the single flat retry the baseline performed.
const flatRoundAttempts = 2

// RoundLimit is how many attempts one round allows, or 0 for no finite limit.
func (p RetryPlan) RoundLimit() int {
	if !p.Enabled {
		return flatRoundAttempts
	}
	if p.MaxRetries <= 0 {
		return 0
	}
	return p.MaxRetries + 1
}

// Wait returns the delay before the retry that follows `failures` consecutive
// failures. With backoff off the wait is flat, which is what the baseline did.
func (p RetryPlan) Wait(failures Failures) time.Duration {
	if !p.Enabled {
		return clampInterval(p.Backoff.Base.Duration())
	}
	return p.Backoff.Wait(failures)
}

// RoundExhausted reports whether this round has spent its attempts.
func (p RetryPlan) RoundExhausted(failures Failures) bool {
	limit := p.RoundLimit()
	return limit > 0 && int(failures) >= limit
}

// RoundCooldown is the bounded wait between rounds of long-running maintenance.
//
// Spec 04 is explicit that reaching the per-round retry limit must not stop a
// managed WAN permanently: the round ends, the account waits, and a new round
// begins. The baseline arrived at the same rule from the other direction -- its
// comment on the max_retries default records that an unlimited round trapped
// the daemon tick, so the state snapshot stopped refreshing and the recovery
// guard never ran. Ending the round is what lets the next one get another
// stale-session rebuild.
func (p RetryPlan) RoundCooldown() time.Duration {
	return clampInterval(p.Backoff.Cap.Duration())
}
