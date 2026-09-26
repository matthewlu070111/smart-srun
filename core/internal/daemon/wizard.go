package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

type WifiSetupView struct {
	OK         bool   `json:"ok"`
	Job        string `json:"job"`
	State      string `json:"state"`
	Message    string `json:"message"`
	Iface      string `json:"iface,omitempty"`
	Radio      string `json:"radio,omitempty"`
	Encryption string `json:"encryption,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

type wifiJob struct {
	view            WifiSetupView
	owner           string
	digest          [32]byte
	revision        uint64
	actionID        string
	cancelRequested bool
	connection      wizardConnection
	group           *wireless.Group
	savedID         string
	saveDigest      [32]byte
}

// The coordinator drains all workers before start/undo; the lease remains while
// the browser verifies the draft. mu protects only short status/lease changes.
// Network I/O takes device.mu, never this status mutex.
type wifiWizard struct {
	mu      sync.Mutex
	device  *deviceWireless
	daemon  *Daemon
	jobs    map[string]*wifiJob
	order   []string
	active  string
	blocked bool
	paths   wireless.Paths
}

type wizardStore struct {
	wireless.Store
	runner openwrt.Runner
}

func (s wizardStore) Reload(ctx context.Context) error {
	// Firewall first, so a new uplink never comes up before its WAN policy.
	if _, err := s.runner.Run(ctx, "/etc/init.d/firewall", "reload"); err != nil {
		return domain.Errorf(domain.CodeInternal, "重载防火墙配置失败").Wrap(err)
	}
	return s.Store.Reload(ctx)
}

func newWifiWizard(d *Daemon, device *deviceWireless) *wifiWizard {
	return &wifiWizard{daemon: d, device: device, jobs: map[string]*wifiJob{}, paths: wireless.Paths{Dir: filepath.Join(d.paths.Recovery(), "setup-wifi")}}
}

func (m *wifiWizard) transactionStore() wireless.Store {
	if m.device.wizardStore != nil {
		return m.device.wizardStore
	}
	return wizardStore{Store: m.device.store}
}

func (m *wifiWizard) recover(ctx context.Context) error {
	m.device.mu.Lock()
	defer m.device.mu.Unlock()
	g, found, err := wireless.LoadGroup(m.transactionStore(), m.paths, m.daemon.clock.Now)
	if err == nil && found {
		cfg := m.daemon.config.Snapshot()
		data, _ := config.Marshal(cfg)
		err = g.Recover(ctx, cfg.Revision, data)
	}
	m.mu.Lock()
	m.blocked = err != nil
	m.mu.Unlock()
	return err
}

func (m *wifiWizard) check(request application.Request) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blocked {
		return domain.Errorf(domain.CodeRecoveryRequired, "上次无线向导需要恢复，暂不执行网络操作")
	}
	if m.active == "" {
		return nil
	}
	job := m.jobs[m.active]
	if request.Kind.WifiSetup() && request.SetupJob == m.active && request.Owner == job.owner {
		return nil
	}
	if (job.view.State == "ready" || job.view.State == "connected") && request.Kind.Discovery() && request.Owner == job.owner && request.Interface == job.connection.Iface && request.ProbeSSID == job.connection.SSID && m.daemon.clock.Now().Unix() < job.view.ExpiresAt {
		return nil
	}
	return domain.Errorf(domain.CodeBusy, "无线向导正在使用网络，请先完成或取消向导")
}

func (m *wifiWizard) configBlocked() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blocked || m.active != ""
}

func (m *wifiWizard) view(job, owner string) (WifiSetupView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.jobs[job]
	if entry == nil || entry.owner != owner {
		return WifiSetupView{}, domain.Errorf(domain.CodeNotFound, "没有当前会话的无线任务")
	}
	return entry.view, nil
}

func (m *wifiWizard) publish(job, state, message string, release bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.jobs[job]
	entry.view.State, entry.view.Message = state, message
	entry.view.OK = state != "failed"
	if release {
		entry.connection.Key = ""
		entry.group = nil
		if m.active == job {
			m.active = ""
		}
	}
}

func (m *wifiWizard) observe(action application.Action) {
	if m == nil || action.Request.Kind != application.KindWifiSetupStart || !action.State.Terminal() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.jobs[action.Request.SetupJob]
	if entry != nil && entry.view.State == "starting" {
		entry.view.State, entry.view.Message, entry.view.OK = "failed", action.Message, false
		if action.State == application.StateCancelled || action.State == application.StateInterrupted {
			entry.view.State = "cancelled"
			entry.view.OK = true
		}
		if m.active == entry.view.Job {
			m.active = ""
		}
	}
}

type wifiRoutingRunner struct {
	actions application.Runner
	wizard  *wifiWizard
}

func (r wifiRoutingRunner) Run(ctx context.Context, action application.Action, report func(application.Phase)) application.Outcome {
	if !action.Request.Kind.WifiSetup() {
		return r.actions.Run(ctx, action, report)
	}
	// Mounted even without a wizard, so this answers instead of the worker's
	// "not implemented" fallback. A build on a router with no manageable radio
	// has a capability problem, not a missing feature, and telling a user the
	// wizard was never written sends them to the wrong place.
	if r.wizard == nil {
		return application.Outcome{State: application.StateFailed,
			Code:    domain.CodeUnsupportedCapability,
			Message: "这台设备没有可管理的无线客户端，无法使用无线向导"}
	}
	var err error
	if action.Request.Kind == application.KindWifiSetupStart {
		err = r.wizard.start(ctx, action)
	} else {
		err = r.wizard.undo(ctx, action.Request.SetupJob)
	}
	if err != nil {
		code, _ := domain.CodeOf(err)
		return application.Outcome{State: application.StateFailed, Code: code, Message: err.Error()}
	}
	return application.Outcome{State: application.StateSucceeded, Message: "无线向导操作已完成"}
}

func (m *wifiWizard) start(ctx context.Context, action application.Action) error {
	ctx, cancelWork := context.WithTimeout(ctx, 180*time.Second)
	defer cancelWork()
	var params WifiSetupParams
	if err := json.Unmarshal([]byte(action.Request.PrivateJSON), &params); err != nil {
		return domain.Errorf(domain.CodeInvalidArgument, "无线连接参数无效")
	}
	w := m.device
	w.mu.Lock()
	defer w.mu.Unlock()
	m.mu.Lock()
	entry := m.jobs[params.Job]
	if entry == nil || m.active != params.Job || entry.cancelRequested {
		m.mu.Unlock()
		if entry != nil {
			m.publish(params.Job, "cancelled", "无线连接已取消", true)
		}
		return domain.Errorf(domain.CodeCancelled, "无线连接已取消")
	}
	entry.view.State, entry.view.Message = "connecting", "正在连接所选无线网络"
	m.mu.Unlock()
	connection, plans, err := w.wizardPlan(ctx, params)
	if err != nil {
		m.publish(params.Job, "failed", err.Error(), true)
		return err
	}
	var group *wireless.Group
	if len(plans) > 0 {
		group, err = wireless.BeginGroup(ctx, m.transactionStore(), m.paths, wireless.GroupPlan{TaskID: params.Job, ConfigRevision: entry.revision, ConfirmWithin: ConfirmWithin, Packages: plans}, m.daemon.clock.Now)
		if err == nil {
			err = group.Apply(ctx)
		}
	}
	if err == nil {
		settleCtx, cancelSettle := context.WithTimeout(ctx, w.settleWait)
		defer cancelSettle()
		deadline := m.daemon.clock.Now().Add(w.settleWait)
		for !w.wizardAssociated(settleCtx, connection) {
			if ctx.Err() != nil {
				err = domain.Errorf(domain.CodeCancelled, "无线连接已取消")
				break
			}
			if settleCtx.Err() != nil || !m.daemon.clock.Now().Before(deadline) {
				err = domain.Errorf(domain.CodeDeadlineExceeded, "无线网络未在限时内关联并取得 IPv4 地址")
				break
			}
			timer := m.daemon.clock.NewTimerAt(m.daemon.clock.Now().Add(w.settlePoll))
			select {
			case <-settleCtx.Done():
				timer.Stop()
			case <-timer.C():
			}
		}
	}
	if ctx.Err() != nil && err == nil {
		err = domain.Errorf(domain.CodeCancelled, "无线连接已取消")
	}
	if err != nil {
		undoCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), UndoBudget)
		defer cancel()
		if group == nil {
			group, _, _ = wireless.LoadGroup(m.transactionStore(), m.paths, m.daemon.clock.Now)
		}
		var undoErr error
		if group != nil {
			undoErr = group.Rollback(undoCtx)
		}
		if undoErr != nil {
			m.mu.Lock()
			m.blocked = true
			m.mu.Unlock()
			err = domain.Errorf(domain.CodeRecoveryRequired, "无线连接失败，恢复尚未完成，请检查网络状态").Wrap(errors.Join(err, undoErr))
		}
		state := "failed"
		if undoErr == nil && ctx.Err() == context.Canceled {
			state = "cancelled"
		}
		m.publish(params.Job, state, err.Error(), true)
		return err
	}
	m.mu.Lock()
	entry.connection, entry.group = connection, group
	entry.view.Iface, entry.view.Radio, entry.view.Encryption = connection.Iface, connection.Radio, connection.Encryption
	entry.view.ExpiresAt = m.daemon.clock.Now().Add(ConfirmWithin).Unix()
	if group != nil {
		entry.view.ExpiresAt = group.ExpiresAt().Unix()
	}
	entry.view.State, entry.view.Message = "ready", "已连接，等待完成向导保存"
	if connection.Reused {
		entry.view.State, entry.view.Message = "connected", "已复用当前无线连接"
	}
	cancelRequested := entry.cancelRequested
	m.mu.Unlock()
	if cancelRequested {
		return m.undoLocked(context.WithoutCancel(ctx), params.Job)
	}
	return nil
}

func (m *wifiWizard) undo(ctx context.Context, job string) error {
	m.device.mu.Lock()
	defer m.device.mu.Unlock()
	return m.undoLocked(ctx, job)
}

func (m *wifiWizard) undoLocked(ctx context.Context, job string) error {
	m.mu.Lock()
	entry := m.jobs[job]
	if entry == nil || m.active != job {
		m.mu.Unlock()
		return nil
	}
	group := entry.group
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), UndoBudget)
	defer cancel()
	if group != nil {
		if err := group.Rollback(ctx); err != nil {
			m.mu.Lock()
			m.blocked = true
			m.mu.Unlock()
			m.publish(job, "failed", "无线连接恢复尚未完成，请检查网络状态", true)
			return domain.Errorf(domain.CodeRecoveryRequired, "无线连接恢复尚未完成").Wrap(err)
		}
	}
	m.publish(job, "cancelled", "已取消临时连接并恢复原配置", true)
	return nil
}

func (m *wifiWizard) runExpiry(ctx context.Context) {
	for {
		timer := m.daemon.clock.NewTimerAt(m.daemon.clock.Now().Add(time.Second))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
		m.mu.Lock()
		entry := m.jobs[m.active]
		var job, owner string
		if entry != nil && (entry.view.State == "ready" || entry.view.State == "connected") && m.daemon.clock.Now().Unix() >= entry.view.ExpiresAt {
			job, owner = entry.view.Job, entry.owner
		}
		m.mu.Unlock()
		if job != "" {
			_, err := m.cancel(ctx, job, owner)
			if err != nil && ctx.Err() == nil {
				m.daemon.onError(err)
			}
		}
	}
}

func (m *wifiWizard) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), UndoBudget)
	defer cancel()
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active != "" {
		if err := m.undo(ctx, active); err != nil {
			m.daemon.onError(err)
		}
	}
}
