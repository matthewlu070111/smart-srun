package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

type updateArgs struct {
	command, planID, channel string
	background               bool
}

func parseUpdateArgs(args []string) (updateArgs, error) {
	var parsed updateArgs
	bad := func() (updateArgs, error) {
		return parsed, domain.Errorf(domain.CodeInvalidArgument, "用法：update check [--channel rc|stable] [--no-wait]；update run 计划ID [--background]；update status；update recover [--background]")
	}
	if len(args) == 0 {
		return bad()
	}
	parsed.command = args[0]
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
		case "--background", "--no-wait":
			parsed.background = true
		case "--channel":
			if parsed.command != "check" || parsed.channel != "" || i+1 >= len(args) {
				return bad()
			}
			i++
			parsed.channel = args[i]
			if parsed.channel != "stable" && parsed.channel != "rc" {
				return bad()
			}
		default:
			if parsed.command != "run" || parsed.planID != "" || len(args[i]) != 64 {
				return bad()
			}
			parsed.planID = args[i]
		}
	}
	switch parsed.command {
	case "check", "recover":
	case "prepare-local":
		if parsed.background {
			return bad()
		}
	case "status", "inventory":
		if parsed.background {
			return bad()
		}
	case "run":
		if parsed.planID == "" {
			return bad()
		}
	default:
		return bad()
	}
	return parsed, nil
}

func RunUpdate(ctx context.Context, args []string, stdout, stderr *os.File) int {
	parsed, err := parseUpdateArgs(args)
	if err != nil {
		return onlineError(stderr, err)
	}
	paths := daemon.DefaultPaths()
	if parsed.command == "inventory" {
		inventory, err := updateDevice(ctx).Inventory(ctx, Version)
		if err != nil {
			return onlineError(stderr, err)
		}
		if err := inventory.Validate(); err != nil {
			return onlineError(stderr, err)
		}
		return writeJSON(stdout, stderr, inventory)
	}
	if parsed.command == "prepare-local" {
		return prepareLocalUpdate(ctx, os.Stdin, stdout, stderr)
	}
	if parsed.command == "status" {
		status, err := update.ReadStatus(paths.Update())
		if err != nil {
			return onlineError(stderr, err)
		}
		return writeJSON(stdout, stderr, status)
	}
	if parsed.command == "check" {
		return runUpdateCheck(ctx, parsed, stdout, stderr)
	}
	executable, err := os.Executable()
	if err != nil {
		return onlineError(stderr, err)
	}
	var status update.Status
	if parsed.command == "recover" {
		task, err := update.QueueRecovery(paths.Update(), executable, launchUpdateWorker)
		if err != nil {
			return onlineError(stderr, err)
		}
		status = update.Status{SchemaVersion: 1, JobID: task.JobID, Running: true, OK: true, Phase: "queued", Message: "恢复任务已提交"}
	} else if defaultLifecycle().Running(ctx) {
		raw, err := (control.Client{Timeout: 15 * time.Second}).Call(ctx, "update.start", daemon.UpdateStartParams{PlanID: parsed.planID})
		if err != nil {
			return onlineError(stderr, err)
		}
		if json.Unmarshal(raw, &status) != nil {
			return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "更新任务响应无效"))
		}
	} else {
		status, err = beginOfflineUpdate(ctx, paths, parsed.planID, executable)
		if err != nil {
			return onlineError(stderr, err)
		}
	}
	if parsed.background {
		return writeJSON(stdout, stderr, status)
	}
	return waitUpdate(ctx, paths.Update(), status.JobID, stdout, stderr)
}

func prepareLocalUpdate(ctx context.Context, stdin io.Reader, stdout, stderr *os.File) int {
	data, err := io.ReadAll(io.LimitReader(stdin, update.MaxStateBytes+1))
	if err != nil || len(data) > update.MaxStateBytes {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "本地更新清单过大或无法读取"))
	}
	var raw struct {
		Manifest json.RawMessage `json:"manifest"`
		Recovery json.RawMessage `json:"recovery"`
	}
	if err := config.DecodePatch(data, &raw); err != nil {
		return onlineError(stderr, err)
	}
	manifest, err := update.ParseManifest(raw.Manifest)
	if err != nil {
		return onlineError(stderr, err)
	}
	recovery, err := update.ParseManifest(raw.Recovery)
	if err != nil {
		return onlineError(stderr, err)
	}
	device := updateDevice(ctx)
	inventory, err := device.Inventory(ctx, Version)
	if err != nil {
		return onlineError(stderr, err)
	}
	candidate, err := update.LocalCandidate(manifest, recovery, inventory)
	if err != nil {
		return onlineError(stderr, err)
	}
	paths := daemon.DefaultPaths().Update()
	for _, file := range update.LocalInputs(paths, candidate) {
		if err := device.Verify(ctx, file); err != nil {
			return onlineError(stderr, err)
		}
	}
	if err := update.SaveCandidate(paths, candidate); err != nil {
		return onlineError(stderr, err)
	}
	return writeJSON(stdout, stderr, update.CheckResult{OK: true, UpdateAvailable: true, PlanID: candidate.Plan.ID,
		CurrentVersion: Version, LatestVersion: candidate.Plan.Release, LatestTag: candidate.Plan.Release,
		InstallMode: candidate.Plan.InstallMode, PackageFormat: candidate.Plan.Assets[0].Format, Message: "本地安装包已核验，等待启动更新"})
}

func updateDevice(ctx context.Context) openwrt.PackageDevice {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return openwrt.PackageDevice{Runner: openwrt.Runner{}, Capabilities: openwrt.Detect(probe, openwrt.Runner{})}
}

func launchUpdateWorker() error {
	_, err := (openwrt.Runner{Timeout: 10 * time.Second}).Run(context.Background(), "/etc/init.d/smart_srun_update", "start")
	return err
}

func beginOfflineUpdate(ctx context.Context, paths daemon.Paths, id, executable string) (update.Status, error) {
	if err := daemon.EnsureRuntimeDir(paths); err != nil {
		return update.Status{}, err
	}
	lock, err := daemon.Acquire(paths.Lock())
	if err != nil {
		return update.Status{}, err
	}
	defer lock.Release()
	candidate, err := update.ReadCandidate(paths.Update(), id)
	if err != nil {
		return update.Status{}, err
	}
	inventory, err := updateDevice(ctx).Inventory(ctx, Version)
	if err != nil {
		return update.Status{}, err
	}
	if !reflect.DeepEqual(inventory, candidate.Plan.Inventory) {
		return update.Status{}, domain.Errorf(domain.CodeConflict, "安装状态已变化，请重新检查更新")
	}
	task, err := update.Begin(paths.Update(), candidate, false, executable, launchUpdateWorker)
	return update.Status{SchemaVersion: 1, JobID: task.JobID, Running: true, OK: err == nil, Phase: "queued", Message: "更新任务已提交"}, err
}

func runUpdateCheck(ctx context.Context, args updateArgs, stdout, stderr *os.File) int {
	if defaultLifecycle().Running(ctx) {
		client := control.Client{}
		raw, err := client.Call(ctx, "update.check", daemon.UpdateCheckParams{Channel: args.channel})
		if err != nil {
			return onlineError(stderr, err)
		}
		var result update.CheckResult
		if json.Unmarshal(raw, &result) != nil {
			return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "更新检查响应无效"))
		}
		for result.Running && !args.background {
			if err := waitUpdateTick(ctx); err != nil {
				return onlineError(stderr, err)
			}
			raw, err = client.Call(ctx, "update.status", daemon.UpdateStatusParams{JobID: result.JobID})
			if err != nil {
				return onlineError(stderr, err)
			}
			if json.Unmarshal(raw, &result) != nil {
				return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "更新检查响应无效"))
			}
		}
		return printCheckResult(result, stdout, stderr)
	}
	// A stopped installation can be checked without starting authentication.
	source := update.NewSource()
	defer source.Close()
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	inventory, err := updateDevice(checkCtx).Inventory(checkCtx, Version)
	if err != nil {
		return onlineError(stderr, err)
	}
	candidate, result, err := update.Check(checkCtx, source, inventory, args.channel)
	if err == nil && result.UpdateAvailable {
		err = update.SaveCandidate(daemon.DefaultPaths().Update(), candidate)
	}
	if err != nil {
		return onlineError(stderr, err)
	}
	return printCheckResult(result, stdout, stderr)
}

func printCheckResult(result update.CheckResult, stdout, stderr *os.File) int {
	code := writeJSON(stdout, stderr, result)
	if code == ExitOK && !result.OK {
		return ExitCodeFor(domain.Errorf(result.Code, "更新检查失败"))
	}
	return code
}

func waitUpdateTick(ctx context.Context) error {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return domain.Errorf(domain.CodeCancelled, "已停止等待，后台安装任务继续执行")
	}
}

func waitUpdate(ctx context.Context, paths update.Paths, id string, stdout, stderr *os.File) int {
	for {
		status, err := update.ReadStatus(paths)
		if err != nil {
			return onlineError(stderr, err)
		}
		if status.JobID != id {
			return onlineError(stderr, domain.Errorf(domain.CodeConflict, "更新任务状态已变化"))
		}
		if !status.Running {
			code := writeJSON(stdout, stderr, status)
			if code == ExitOK && !status.OK {
				return ExitCodeFor(domain.Errorf(status.Code, "更新失败"))
			}
			return code
		}
		if err := waitUpdateTick(ctx); err != nil {
			return onlineError(stderr, err)
		}
	}
}

type updateService struct{ paths daemon.Paths }

func (s updateService) Stop(ctx context.Context) error {
	_, err := (openwrt.Runner{Timeout: 25 * time.Second}).Run(ctx, daemon.DefaultInitScript, "stop_for_update")
	if err != nil {
		return domain.Errorf(domain.CodeInstallFailed, "无法停止主服务").Wrap(err)
	}
	// The same lock used by the daemon proves shutdown has completed, without
	// searching processes or trusting a stale PID.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		lock, err := daemon.Acquire(s.paths.Lock())
		if err == nil {
			lock.Release()
			return nil
		}
		if err := waitUpdateTick(ctx); err != nil {
			return err
		}
	}
	return domain.Errorf(domain.CodeInstallFailed, "主服务没有及时停止")
}
func (s updateService) Start(ctx context.Context) error {
	_, err := (openwrt.Runner{Timeout: 10 * time.Second}).Run(ctx, daemon.DefaultInitScript, "start")
	if err != nil {
		return domain.Errorf(domain.CodeInstallFailed, "安装后无法启动服务").Wrap(err)
	}
	return nil
}
func (s updateService) Health(ctx context.Context, version string) error {
	client := control.Client{Path: s.paths.Socket()}
	for {
		raw, err := client.Call(ctx, "version.get", nil)
		if err == nil {
			var result daemon.VersionResult
			if json.Unmarshal(raw, &result) != nil || result.Version != version || result.RPCVersion != control.Version {
				return domain.Errorf(domain.CodeInstallFailed, "安装后服务版本或 RPC 不匹配")
			}
			if _, err := client.Call(ctx, "status.get", nil); err == nil {
				return nil
			}
		}
		if err := waitUpdateTick(ctx); err != nil {
			return domain.Errorf(domain.CodeInstallFailed, "安装后服务健康检查未通过").Wrap(err)
		}
	}
}

func RunUpdateWorker(ctx context.Context, args []string, stdout, stderr *os.File) int {
	if len(args) != 0 {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "更新 worker 不接受外部路径或命令"))
	}
	paths := daemon.DefaultPaths()
	source := update.NewSource()
	defer source.Close()
	worker := update.Worker{Paths: paths.Update(), Source: source, Device: updateDevice(ctx), Service: updateService{paths}}
	if err := worker.Run(ctx); err != nil {
		return onlineError(stderr, err)
	}
	return ExitOK
}

func UpdateWorkerActive() int {
	held, err := update.WorkerActive(daemon.DefaultPaths().Update())
	if held || err != nil {
		return ExitOK
	}
	return ExitServiceStopped
}

func RunUpdateIntent(args []string, stderr *os.File) int {
	if len(args) != 1 || args[0] != "stopped" {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "无效的服务意图"))
	}
	if err := update.RecordServiceIntent(daemon.DefaultPaths().Update(), false); err != nil {
		return onlineError(stderr, err)
	}
	return ExitOK
}
