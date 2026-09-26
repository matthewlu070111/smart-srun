package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func (d *Daemon) setupWifiStart(ctx context.Context, raw json.RawMessage) (any, error) {
	if d.wizard == nil {
		return nil, domain.Errorf(domain.CodeUnsupportedCapability, "当前服务不能建立临时无线连接")
	}
	var p WifiSetupParams
	if err := control.DecodeParams(raw, &p); err != nil {
		return nil, err
	}
	if err := validateWifiSetup(p); err != nil {
		return nil, err
	}
	m := d.wizard
	owner := probeOwner(p.Session)
	p.Session = "" // Raw LuCI session tokens never enter retained job input.
	body, _ := json.Marshal(p)
	digest := sha256.Sum256(body)
	m.mu.Lock()
	if existing := m.jobs[p.Job]; existing != nil {
		if existing.owner != owner || existing.digest != digest {
			m.mu.Unlock()
			return nil, domain.Errorf(domain.CodeConflict, "无线任务编号已被使用")
		}
		view := existing.view
		m.mu.Unlock()
		return view, nil
	}
	if m.blocked || m.active != "" {
		m.mu.Unlock()
		return nil, domain.Errorf(domain.CodeBusy, "请先完成或取消当前无线向导")
	}
	for len(m.order) >= 64 {
		delete(m.jobs, m.order[0])
		m.order = m.order[1:]
	}
	entry := &wifiJob{owner: owner, digest: digest, revision: d.config.Revision(), view: WifiSetupView{OK: true, Job: p.Job, State: "starting", Message: "无线连接任务已排队"}}
	m.jobs[p.Job], m.active = entry, p.Job
	m.order = append(m.order, p.Job)
	m.mu.Unlock()
	receipt, err := d.actions.Submit(ctx, application.Request{Kind: application.KindWifiSetupStart, SetupJob: p.Job, Owner: owner, PrivateJSON: string(body), IdempotencyKey: "wifi:" + p.Job, ConfigRevision: entry.revision, CheckRevision: true})
	if err != nil {
		m.publish(p.Job, "failed", err.Error(), true)
		return nil, err
	}
	m.mu.Lock()
	entry.actionID = receipt.ActionID
	cancelled := entry.cancelRequested
	view := entry.view
	m.mu.Unlock()
	if cancelled {
		_ = d.actions.Cancel(ctx, receipt.ActionID)
	}
	return view, nil
}

type wifiJobParams struct {
	Job     string `json:"job"`
	Session string `json:"session,omitempty"`
}

func (d *Daemon) setupWifiStatus(_ context.Context, raw json.RawMessage) (any, error) {
	var p wifiJobParams
	if err := control.DecodeParams(raw, &p); err != nil {
		return nil, err
	}
	if d.wizard == nil {
		return nil, domain.Errorf(domain.CodeNotFound, "没有当前会话的无线任务")
	}
	return d.wizard.view(p.Job, probeOwner(p.Session))
}
func (d *Daemon) setupWifiCancel(ctx context.Context, raw json.RawMessage) (any, error) {
	var p wifiJobParams
	if err := control.DecodeParams(raw, &p); err != nil {
		return nil, err
	}
	if d.wizard == nil {
		return nil, domain.Errorf(domain.CodeNotFound, "没有当前会话的无线任务")
	}
	return d.wizard.cancel(ctx, p.Job, probeOwner(p.Session))
}

func (m *wifiWizard) cancel(ctx context.Context, job, owner string) (WifiSetupView, error) {
	m.mu.Lock()
	entry := m.jobs[job]
	if entry == nil || entry.owner != owner {
		m.mu.Unlock()
		return WifiSetupView{}, domain.Errorf(domain.CodeNotFound, "没有当前会话的无线任务")
	}
	if m.active != job || entry.view.State == "cancelling" {
		view := entry.view
		m.mu.Unlock()
		return view, nil
	}
	if entry.view.State == "saving" {
		m.mu.Unlock()
		return WifiSetupView{}, domain.Errorf(domain.CodeBusy, "账号正在保存，请稍后重试")
	}
	entry.cancelRequested = true
	actionID, state := entry.actionID, entry.view.State
	if state == "ready" || state == "connected" {
		entry.view.State = "cancelling"
	}
	view := entry.view
	m.mu.Unlock()
	if state == "starting" || state == "connecting" {
		if actionID != "" {
			if err := m.daemon.actions.Cancel(ctx, actionID); err != nil {
				return WifiSetupView{}, err
			}
		}
		return view, nil
	}
	_, err := m.daemon.actions.Submit(ctx, application.Request{Kind: application.KindWifiSetupCancel, SetupJob: job, Owner: owner, IdempotencyKey: "wifi-cancel:" + job})
	if err != nil {
		m.mu.Lock()
		entry.view.State = state
		entry.cancelRequested = false
		m.mu.Unlock()
		return WifiSetupView{}, err
	}
	return view, nil
}

type wifiAccountParams struct {
	Job              string              `json:"job"`
	Session          string              `json:"session,omitempty"`
	ExpectedRevision *uint64             `json:"expected_revision"`
	Account          *config.CampusPatch `json:"account"`
}

func (d *Daemon) setupWifiAccount(ctx context.Context, raw json.RawMessage) (any, error) {
	var p wifiAccountParams
	if err := config.DecodePatch(raw, &p, "account.login.double_stack"); err != nil {
		return nil, err
	}
	if d.wizard == nil || p.Account == nil || p.ExpectedRevision == nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "保存无线向导需要任务、账号和配置版本")
	}
	m := d.wizard
	accountBody, _ := json.Marshal(p.Account)
	accountDigest := sha256.Sum256(accountBody)
	restore, err := m.prepareAccount(ctx, p.Job, probeOwner(p.Session))
	if err != nil {
		return nil, err
	}
	defer restore()
	var result ConfigWriteResult
	err = d.actions.ChangeConfiguration(ctx, func() error {
		m.device.mu.Lock()
		defer m.device.mu.Unlock()
		m.mu.Lock()
		entry := m.jobs[p.Job]
		if entry == nil || entry.owner != probeOwner(p.Session) {
			m.mu.Unlock()
			return domain.Errorf(domain.CodeNotFound, "没有当前会话的无线任务")
		}
		if entry.view.State == "done" && entry.savedID != "" {
			if entry.saveDigest != accountDigest {
				m.mu.Unlock()
				return domain.Errorf(domain.CodeConflict, "这个无线任务已经保存了不同的账号内容")
			}
			id := entry.savedID
			m.mu.Unlock()
			cfg := d.config.Snapshot()
			result = ConfigWriteResult{ID: id, Revision: cfg.Revision, Config: redact(cfg)}
			return nil
		}
		if m.blocked || m.active != p.Job || entry.cancelRequested || entry.view.State != "saving" || d.clock.Now().Unix() >= entry.view.ExpiresAt {
			m.mu.Unlock()
			return domain.Errorf(domain.CodeConflict, "无线连接已结束，请重新连接后保存")
		}
		connection, group, revision := entry.connection, entry.group, entry.revision
		m.mu.Unlock()
		before := d.config.Snapshot()
		if *p.ExpectedRevision != revision || before.Revision != revision {
			return domain.Errorf(domain.CodeConflict, "配置已变化，请重新开始无线向导")
		}
		// The settled connection is authoritative; stale or fabricated browser
		// fields cannot change which radio or wireless secret the account saves.
		patch := *p.Account
		if patch.ID != "" {
			return domain.Errorf(domain.CodeInvalidArgument, "无线向导用于新增账号")
		}
		mode, auto, empty := "wifi", "auto", ""
		patch.AccessMode, patch.SSID, patch.Radio, patch.Encryption, patch.Key = &mode, &connection.SSID, &connection.Radio, &connection.Encryption, &connection.Key
		patch.APSelection, patch.BSSID, patch.WiredIface = &auto, &empty, &empty
		candidate := config.CloneConfig(before)
		if err := config.UpsertCampus(patch)(&candidate); err != nil {
			return err
		}
		newID := candidate.CampusAccounts[len(candidate.CampusAccounts)-1].ID
		if err := config.SetDefaultCampus(newID)(&candidate); err != nil {
			return err
		}
		candidate.STAIface = connection.Iface
		candidate = config.Normalize(candidate)
		config.RepairSelection(&candidate)
		candidate.Revision = before.Revision + 1
		if err := config.Validate(candidate); err != nil {
			return err
		}
		body, err := config.Marshal(candidate)
		if err != nil {
			return err
		}
		if group != nil {
			if err := group.PrepareSave(candidate.Revision, body); err != nil {
				return err
			}
		}
		updated, saveErr := d.config.Update(before.Revision, func(cfg *domain.Config) error { *cfg = config.CloneConfig(candidate); return nil })
		visible := d.config.Revision() != before.Revision
		if visible {
			d.configChanged(d.config.Revision())
		}
		if saveErr != nil {
			if visible {
				m.mu.Lock()
				m.blocked = true
				m.mu.Unlock()
				m.publish(p.Job, "failed", "账号已写入，但尚未确认持久保存；请检查网络后重启服务完成恢复", true)
			}
			return saveErr
		}
		if group != nil {
			if err := group.Confirm(); err != nil {
				m.mu.Lock()
				m.blocked = true
				m.mu.Unlock()
				m.publish(p.Job, "failed", "账号已保存，无线事务仍需恢复确认", true)
				return domain.Errorf(domain.CodeRecoveryRequired, "账号已保存，无线事务仍需恢复确认").Wrap(err)
			}
		}
		id := updated.CampusAccounts[len(updated.CampusAccounts)-1].ID
		m.mu.Lock()
		entry.savedID = id
		entry.saveDigest = accountDigest
		m.mu.Unlock()
		m.publish(p.Job, "done", "账号已保存，无线连接已确认", true)
		result = ConfigWriteResult{ID: id, Revision: updated.Revision, Config: redact(updated)}
		return nil
	})
	return result, err
}

// Device verification happens before the coordinator's short configuration
// callback. The saving lease prevents a concurrent cancel or a new probe.
func (m *wifiWizard) prepareAccount(ctx context.Context, job, owner string) (func(), error) {
	m.mu.Lock()
	entry := m.jobs[job]
	if entry == nil || entry.owner != owner {
		m.mu.Unlock()
		return nil, domain.Errorf(domain.CodeNotFound, "没有当前会话的无线任务")
	}
	if entry.view.State == "done" {
		m.mu.Unlock()
		return func() {}, nil
	}
	if m.blocked || m.active != job || entry.cancelRequested || (entry.view.State != "ready" && entry.view.State != "connected") || m.daemon.clock.Now().Unix() >= entry.view.ExpiresAt {
		m.mu.Unlock()
		return nil, domain.Errorf(domain.CodeConflict, "无线连接尚未就绪或已经结束")
	}
	previous := entry.view.State
	entry.view.State = "saving"
	connection, group := entry.connection, entry.group
	m.mu.Unlock()
	restore := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if entry.view.State == "saving" {
			entry.view.State = previous
		}
	}
	m.device.mu.Lock()
	var err error
	if !m.device.wizardAssociated(ctx, connection) {
		err = domain.Errorf(domain.CodeConflict, "无线关联或 IPv4 已变化，请重新连接后保存")
	}
	if err == nil && group != nil {
		err = group.CheckApplied(ctx)
	}
	m.device.mu.Unlock()
	if err != nil {
		restore()
		return nil, err
	}
	return restore, nil
}

// commit is intentionally the same account-saving transaction: a disconnected
// browser cannot confirm the UCI change without also preserving its account.
func (d *Daemon) setupWifiCommit(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.setupWifiAccount(ctx, raw)
}
