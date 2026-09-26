package application

import (
	"context"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
)

// QuietResume contains no credentials or network identifiers. The daemon may
// retain it on tmpfs across service restarts; a router reboot clears it.
type QuietResume struct {
	Revision   uint64    `json:"revision"`
	Occurrence string    `json:"occurrence"`
	AccountID  string    `json:"account_id"`
	HotspotID  string    `json:"hotspot_id"`
	StartedAt  time.Time `json:"started_at"`
	// A manual hotspot choice inside the quiet window retains the upcoming
	// return, but does not prove that managed wired accounts were logged out.
	SweepPending bool `json:"sweep_pending,omitempty"`
}

func (r QuietResume) Matches(cfg domain.Config, now time.Time) bool {
	if r.Revision != cfg.Revision || !cfg.Enabled || !cfg.Failover.Enabled ||
		r.AccountID != cfg.Selection.ActiveCampusID || now.Before(r.StartedAt) {
		return false
	}
	if _, ok := cfg.CampusAccountByID(r.AccountID); !ok {
		return false
	}
	if _, ok := cfg.HotspotByID(r.HotspotID); !ok {
		return false
	}
	then := policy.EvaluateQuiet(cfg.Quiet, r.StartedAt)
	current := policy.EvaluateQuiet(cfg.Quiet, now)
	return then.Active && then.HasNext && then.Occurrence == r.Occurrence &&
		now.Before(then.Next.Add(24*time.Hour)) && (!current.Active || current.Occurrence == r.Occurrence)
}

// Ownership records the next scheduled return, including an already selected
// hotspot observed inside the quiet window. Outside that window manual choices
// remain in effect. Configuration changes invalidate retained scheduling intent.
type quietSwitchState struct {
	occurrence string
	hotspotID  string
	inFlight   string
	done       bool
	owned      bool
	dueAt      time.Time
}

func (m *Maintainer) scheduleQuietSwitch(ctx context.Context, cfg *domain.Config, quiet policy.QuietState, now time.Time) {
	s := &m.quietSwitch
	if m.manuallyPaused(*cfg, cfg.Selection.ActiveCampusID) {
		s.owned, s.done = false, true
		return
	}
	if !cfg.Enabled || !cfg.Failover.Enabled {
		s.owned = false
		return
	}
	if quiet.Active && s.occurrence != quiet.Occurrence {
		*s = quietSwitchState{occurrence: quiet.Occurrence}
	}
	if s.inFlight != "" || now.Before(s.dueAt) {
		return
	}
	if _, ok := cfg.CampusAccountByID(cfg.Selection.ActiveCampusID); !ok {
		return
	}
	kind := KindQuietCampus
	if quiet.Active {
		if s.done {
			return
		}
		if quiet.ForceLogout {
			for _, target := range m.sweep.Pending(quiet.Occurrence, policy.ForcedLogoutTargets(cfg)) {
				if !m.manuallyPaused(*cfg, target.AccountID) {
					return // finish the configured logout sweep before moving its radio
				}
			}
		}
		s.hotspotID = cfg.Selection.ActiveHotspotID
		if s.hotspotID == "" {
			s.hotspotID = cfg.Selection.DefaultHotspotID
		}
		kind = KindQuietHotspot
	} else if !s.owned {
		return
	}
	if _, ok := cfg.HotspotByID(s.hotspotID); !ok {
		return
	}
	receipt, err := m.submit(ctx, Request{Kind: kind, AccountID: cfg.Selection.ActiveCampusID,
		HotspotID: s.hotspotID, CheckRevision: true, ConfigRevision: cfg.Revision,
		IdempotencyKey: m.key(string(kind), cfg.Selection.ActiveCampusID)})
	if err == nil {
		s.inFlight = receipt.ActionID
	}
}

func (m *Maintainer) applyQuietSwitch(action Action, cfg domain.Config, now time.Time) bool {
	s := &m.quietSwitch
	if action.Request.Kind == KindSwitchCampus || action.Request.Kind == KindSwitchHotspot {
		if action.State == StateSucceeded {
			s.owned, s.done = false, true
			quiet := policy.EvaluateQuiet(cfg.Quiet, action.StartedAt)
			if action.Request.Kind == KindSwitchHotspot && cfg.Enabled && cfg.Failover.Enabled && quiet.Active {
				*s = quietSwitchState{occurrence: quiet.Occurrence, hotspotID: action.Request.HotspotID,
					done: true, owned: true}
			}
		}
		return false
	}
	if action.Request.Kind != KindQuietHotspot && action.Request.Kind != KindQuietCampus {
		return false
	}
	if action.ID != s.inFlight {
		return true
	}
	s.inFlight = ""
	s.dueAt = now.Add(max(30*time.Second, checkInterval(&cfg)))
	if action.State == StateCancelled || action.State == StateInterrupted {
		s.done, s.owned = true, false
	} else if action.State == StateSucceeded {
		s.done = true
		s.owned = action.Request.Kind == KindQuietHotspot && !action.MaintenanceDeferred
		s.dueAt = time.Time{}
	}
	return true
}
