package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

type updateController struct {
	mu        sync.Mutex
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	closing   bool
	check     update.CheckResult
	source    update.ReleaseSource
	inventory func(context.Context, string) (update.Inventory, error)
	launch    func() error
}

type UpdateCheckParams struct {
	Channel string `json:"channel,omitempty"`
}
type UpdateStartParams struct {
	PlanID string `json:"plan_id"`
}
type UpdateStatusParams struct {
	JobID string `json:"job_id,omitempty"`
}

func (d *Daemon) initUpdater(ctx context.Context, options Options) {
	source := options.UpdateSource
	if source == nil {
		source = update.NewSource()
	}
	inventory := func(ctx context.Context, version string) (update.Inventory, error) {
		return openwrt.CurrentPackageInventory(ctx, openwrt.Runner{}, version)
	}
	if options.UpdateDevice != nil {
		inventory = options.UpdateDevice.Inventory
	}
	background, cancel := context.WithCancel(ctx)
	d.updater = &updateController{ctx: background, cancel: cancel, source: source, inventory: inventory, launch: options.UpdateLaunch}
	if d.updater.launch == nil {
		d.updater.launch = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := (openwrt.Runner{Timeout: 10 * time.Second}).Run(ctx, "/etc/init.d/smart_srun_update", "start")
			return err
		}
	}
}

func (u *updateController) close() {
	u.mu.Lock()
	u.closing = true
	u.cancel()
	u.mu.Unlock()
	u.wg.Wait()
	if source, ok := u.source.(interface{ Close() }); ok {
		source.Close()
	}
}

func (d *Daemon) updateCheck(_ context.Context, raw json.RawMessage) (any, error) {
	var params UpdateCheckParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	if params.Channel != "" && params.Channel != "stable" && params.Channel != "rc" {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "更新通道无效")
	}
	u := d.updater
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closing {
		return nil, domain.Errorf(domain.CodeServiceStopped, "服务正在停止")
	}
	if u.check.Running {
		return u.check, nil
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	u.check = update.CheckResult{OK: true, Running: true, JobID: hex.EncodeToString(id[:]), CurrentVersion: d.version, Message: "正在检查兼容更新"}
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		ctx, cancel := context.WithTimeout(u.ctx, 3*time.Minute)
		defer cancel()
		inventory, err := u.inventory(ctx, d.version)
		result := update.CheckResult{CurrentVersion: d.version}
		if err == nil {
			var candidate update.Candidate
			candidate, result, err = update.Check(ctx, u.source, inventory, params.Channel)
			if err == nil && result.UpdateAvailable {
				err = update.SaveCandidate(d.paths.Update(), candidate)
			}
		}
		if err != nil {
			result.OK = false
			result.Code, result.Message = update.ErrorStatus(err)
		}
		u.mu.Lock()
		result.JobID = u.check.JobID
		u.check = result
		u.mu.Unlock()
	}()
	return u.check, nil
}

func (d *Daemon) updateStatus(_ context.Context, raw json.RawMessage) (any, error) {
	var params UpdateStatusParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	if params.JobID != "" {
		u := d.updater
		u.mu.Lock()
		result := u.check
		u.mu.Unlock()
		if result.JobID == params.JobID {
			return result, nil
		}
	}
	status, err := update.ReadStatus(d.paths.Update())
	if err != nil {
		return nil, err
	}
	if params.JobID != "" && params.JobID != status.JobID {
		return nil, domain.Errorf(domain.CodeNotFound, "没有该更新任务")
	}
	return status, nil
}

func (d *Daemon) updateStart(ctx context.Context, raw json.RawMessage) (any, error) {
	var params UpdateStartParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	// A lost RPC reply may be retried; it must not start a second installer.
	if task, err := update.ReadTask(d.paths.Update()); err == nil && task.Plan.ID == params.PlanID {
		return update.ReadStatus(d.paths.Update())
	}
	candidate, err := update.ReadCandidate(d.paths.Update(), params.PlanID)
	if err != nil {
		return nil, err
	}
	inventory, err := d.updater.inventory(ctx, d.version)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(inventory, candidate.Plan.Inventory) {
		return nil, domain.Errorf(domain.CodeConflict, "本机安装状态已变化，请重新检查更新")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	var task update.Task
	err = d.actions.ChangeConfiguration(ctx, func() error {
		if d.wizard.configBlocked() {
			return domain.Errorf(domain.CodeBusy, "无线向导尚未完成，请先保存或取消")
		}
		var err error
		task, err = update.Begin(d.paths.Update(), candidate, true, executable, d.updater.launch)
		return err
	})
	if err != nil {
		return nil, err
	}
	return update.Status{SchemaVersion: 1, JobID: task.JobID, Running: true, OK: true, Phase: "queued", Message: "更新任务已提交"}, nil
}
