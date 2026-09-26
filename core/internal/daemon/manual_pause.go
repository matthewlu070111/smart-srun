package daemon

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type manualPauseRecord struct {
	SchemaVersion int      `json:"schema_version"`
	Revision      uint64   `json:"revision"`
	Accounts      []string `json:"accounts"`
}

// The coordinator is the sole writer. The scheduler and status projection take
// copies under the read lock; no observer event is needed to preserve intent.
type manualPauses struct {
	mu     sync.RWMutex
	paths  Paths
	record manualPauseRecord
}

func loadManualPauses(paths Paths, cfg domain.Config) (*manualPauses, error) {
	p := &manualPauses{paths: paths}
	exists, err := readRuntimeRecord(paths.ManualPauses(), &p.record)
	if err == nil && exists {
		if p.record.SchemaVersion != 1 || len(p.record.Accounts) > config.MaxCampusAccounts {
			err = runtimeRecordError(nil)
		}
		seen := map[string]bool{}
		for _, id := range p.record.Accounts {
			_, known := cfg.CampusAccountByID(id)
			if seen[id] || (p.record.Revision == cfg.Revision && !known) {
				err = runtimeRecordError(nil)
			}
			seen[id] = true
		}
	}
	if err != nil {
		// An unreadable pause record must not cause credential replay at boot.
		// Manual login or a settings save can restore a deliberate policy.
		p.record = manualPauseRecord{SchemaVersion: 1, Revision: cfg.Revision}
		for _, account := range cfg.CampusAccounts {
			p.record.Accounts = append(p.record.Accounts, account.ID)
		}
		// Retain this conservative state as a valid record. Otherwise saving
		// new settings only recovers until the next restart, when the same bad
		// bytes would pause every account again under the new revision.
		if saveErr := writeRuntimeRecord(paths.ManualPauses(), p.record); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}
	return p, err
}

func (p *manualPauses) accounts(cfg domain.Config) []string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.record.Revision != cfg.Revision {
		return nil
	}
	return append([]string(nil), p.record.Accounts...)
}

func (p *manualPauses) paused(cfg domain.Config, id string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.record.Revision == cfg.Revision && slices.Contains(p.record.Accounts, id)
}

func (p *manualPauses) set(cfg domain.Config, id string, paused bool) error {
	ids := p.accounts(cfg)
	if slices.Contains(ids, id) == paused {
		return nil
	}
	if paused {
		ids = append(ids, id)
	} else {
		ids = slices.DeleteFunc(ids, func(value string) bool { return value == id })
	}
	next := manualPauseRecord{SchemaVersion: 1, Revision: cfg.Revision, Accounts: ids}
	if err := writeRuntimeRecord(p.paths.ManualPauses(), next); err != nil {
		return err
	}
	p.mu.Lock()
	p.record = next
	p.mu.Unlock()
	return nil
}

func (d *Daemon) admitManualLogout(request application.Request) error {
	if request.Kind != application.KindLogout {
		return nil
	}
	cfg := d.config.Snapshot()
	if _, known := cfg.CampusAccountByID(request.AccountID); !known {
		return domain.Errorf(domain.CodeNotFound, "账号 %s 不存在", request.AccountID)
	}
	if cfg.Selection.ActiveCampusID == request.AccountID {
		if err := clearQuietResume(d.paths); err != nil {
			return err
		}
	}
	if err := d.manualPauses.set(cfg, request.AccountID, true); err != nil {
		return err
	}
	d.markDirty()
	return nil
}

type manualPauseRunner struct {
	actions application.Runner
	daemon  *Daemon
}

func (r manualPauseRunner) Run(ctx context.Context, action application.Action, report func(application.Phase)) application.Outcome {
	out := r.actions.Run(ctx, action, report)
	// Admission already retained this pause even when the gateway could not
	// confirm logout. Explain it alongside the failure so a user knows why
	// automatic authentication is no longer running for this account. Keep
	// this separate from Finalize, which commits only successful actions.
	if action.Request.Kind == application.KindLogout && r.daemon.manualPauses.paused(r.daemon.config.Snapshot(), action.Request.AccountID) {
		out.Message += "；该账号自动认证已暂停，可手动登录恢复"
	}
	return out
}

func (d *Daemon) finishUserAction(action application.Action, out application.Outcome) application.Outcome {
	out = d.finishSwitch(action, out)
	if out.State != application.StateSucceeded {
		return out
	}
	if kind := action.Request.Kind; kind == application.KindLogin || kind == application.KindRelogin || kind == application.KindSwitchCampus {
		if err := d.manualPauses.set(d.config.Snapshot(), action.Request.AccountID, false); err != nil {
			d.onError(err)
			out.State, out.Code = application.StateFailed, domain.CodeInternal
			out.Message += "；未能解除手动暂停，请检查临时状态目录"
		}
	}
	return out
}
