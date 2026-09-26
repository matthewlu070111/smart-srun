package application

import (
	"context"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/auth"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func (a *Authenticator) verifySession(ctx context.Context, tx *auth.Transaction, p *attempt, report func(Phase), known *auth.Identity) Outcome {
	return a.terminalChecks(ctx, p, func() (Outcome, bool) {
		out := a.verifyOnce(ctx, tx, p, report, known)
		known = nil // every later check asks whose session is on the line now
		return out, retryableTerminal(out)
	})
}

func retryableTerminal(out Outcome) bool {
	if out.State != StateFailed {
		return false
	}
	switch out.Code {
	case domain.CodeTransportFailure, domain.CodeDNSFailure, domain.CodeDeadlineExceeded:
		return true
	default:
		return false
	}
}

func (a *Authenticator) verifyLogout(ctx context.Context, tx *auth.Transaction, p *attempt, identity string, report func(Phase)) Outcome {
	return a.terminalChecks(ctx, p, func() (Outcome, bool) {
		report(PhaseVerify)
		after, err := tx.Online(ctx, p.username)
		if changed := a.confirmUnchanged(ctx, p); changed != nil {
			return p.failed(a, changed, domain.AuthUnknown, ""), false
		}
		if err != nil {
			out := p.unconfirmed(a, "已发送登出请求，但无法确认这条线路是否已下线："+userMessage(err), identity)
			code, _ := domain.CodeOf(err)
			return out, code == domain.CodeTransportFailure || code == domain.CodeDNSFailure || code == domain.CodeDeadlineExceeded
		}
		if !after.Present {
			return p.wentOffline(a, "已登出"), false
		}
		out := p.failed(a, domain.Errorf(domain.CodeConflict, "网关接受了登出请求，但这条线路上仍有在线会话"), after.State(), after.Username)
		return out, after.Username == identity
	})
}

// Only confirmation is retried: this loop cannot send a password or unbind.
// Scheduled transitions use the same bounded confirmation as manual actions;
// an eventually consistent portal must not delay a switch by an entire normal
// maintenance interval. Ordinary maintenance retains its scheduler/backoff.
func (a *Authenticator) terminalChecks(ctx context.Context, p *attempt, check func() (Outcome, bool)) Outcome {
	count := 1
	if p.intent == auth.IntentManual || p.confirmTerminal {
		count = max(1, min(config.MaxTerminalAttempts, p.checks.TerminalAttempts))
	}
	interval := time.Duration(max(1, min(config.MaxTerminalIntervalSeconds, p.checks.TerminalIntervalSeconds))) * time.Second
	for index := 0; ; index++ {
		if err := ctx.Err(); err != nil {
			code, message := domain.CodeCancelled, "终态检查已取消"
			if err == context.DeadlineExceeded {
				code, message = domain.CodeDeadlineExceeded, "终态检查已超时"
			}
			return p.failed(a, domain.Errorf(code, "%s", message).Wrap(err), domain.AuthUnknown, "")
		}
		if index > 0 {
			if err := a.confirmUnchanged(ctx, p); err != nil {
				return p.failed(a, err, domain.AuthUnknown, "")
			}
		}
		out, retry := check()
		if !retry || index+1 >= count {
			return out
		}
		timer := a.clock.NewTimerAt(a.clock.Now().Add(interval))
		select {
		case <-timer.C():
		case <-ctx.Done():
		}
		timer.Stop()
	}
}
