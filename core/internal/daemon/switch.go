package daemon

import (
	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// finishSwitch runs on the coordinator, after the network worker has stopped
// and before success is visible. Switches own an exclusive dispatch slot, so
// publishing a new config cannot invalidate somebody else's running auth.
func (d *Daemon) finishSwitch(action application.Action, outcome application.Outcome) application.Outcome {
	request := action.Request
	if request.Kind == application.KindQuietHotspot || request.Kind == application.KindQuietCampus {
		// Temporary scheduling never changes the user's selected profiles.
		outcome = d.finishQuietRecord(action, outcome)
		if !outcome.MaintenanceDeferred {
			d.configChanged(d.config.Revision())
		}
		return outcome
	}
	if request.Kind != application.KindSwitchCampus && request.Kind != application.KindSwitchHotspot {
		return outcome
	}
	before := d.config.Snapshot()
	if request.CheckRevision && request.ConfigRevision != before.Revision {
		return switchSaveFailed(outcome)
	}
	if outcome = d.finishQuietRecord(action, outcome); outcome.State != application.StateSucceeded {
		return outcome
	}
	selection := before.Selection
	if request.Kind == application.KindSwitchCampus {
		if _, ok := before.CampusAccountByID(request.AccountID); !ok {
			return switchSaveFailed(outcome)
		}
		selection.ActiveCampusID = request.AccountID
	} else {
		if _, ok := before.HotspotByID(request.HotspotID); !ok {
			return switchSaveFailed(outcome)
		}
		selection.ActiveHotspotID = request.HotspotID
	}
	if selection == before.Selection {
		d.configChanged(before.Revision)                  // invalidate observations from the old line
		return d.retainManualQuietReturn(action, outcome) // no persistent config write
	}
	updated, err := d.config.Update(before.Revision, func(cfg *domain.Config) error {
		cfg.Selection = selection
		return nil
	})
	revision := d.config.Revision()
	if revision != before.Revision {
		d.configChanged(revision)
	}
	if err != nil {
		// The line may already have changed; do not claim a config write error
		// undid it. In particular rename can be visible even if fsync failed.
		d.onError(err)
		return switchSaveFailed(outcome)
	}
	if outcome.Observation != nil && outcome.Observation.Revision == before.Revision {
		observed := *outcome.Observation
		observed.Revision = updated.Revision
		outcome.Observation = &observed
	}
	return d.retainManualQuietReturn(action, outcome)
}

func switchSaveFailed(outcome application.Outcome) application.Outcome {
	outcome.State = application.StateFailed
	outcome.Code = domain.CodeRecoveryRequired
	outcome.Message = "线路切换已执行，但未能确认保存所选账号或热点，请检查当前连接后重试"
	return outcome
}
