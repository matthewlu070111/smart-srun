package update

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type Device interface {
	Inventory(context.Context, string) (Inventory, error)
	InstalledVersions(context.Context) (map[string]string, error)
	Verify(context.Context, LocalPackage) error
	Precheck(context.Context, []LocalPackage) error
	Install([]LocalPackage) error
	Recover([]LocalPackage) error
}

type Service interface {
	Stop(context.Context) error
	Start(context.Context) error
	Health(context.Context, string) error
}

// Begin is called inside the daemon's configuration barrier (or while an
// offline CLI holds the daemon lock). No authentication can slip between the
// idle check and the durable update gate.
func Begin(paths Paths, candidate Candidate, wasRunning bool, executable string, launch func() error) (Task, error) {
	lock, err := acquireLock(paths.ControlLock())
	if err != nil {
		return Task{}, err
	}
	defer lock.Close()
	if err := Guard(paths); err != nil {
		return Task{}, err
	}
	if held, err := WorkerActive(paths); err != nil || held {
		return Task{}, domain.Errorf(domain.CodeBusy, "上一更新进程仍在收尾，请稍后再试").Wrap(err)
	}
	if err := ValidatePlan(candidate.Plan); err != nil {
		return Task{}, err
	}
	assets, err := RecoveryAssets(candidate.Recovery, candidate.Plan.Inventory)
	if err != nil {
		return Task{}, err
	}
	candidate.Recovery.Assets = assets
	previous, err := ReadStatus(paths)
	if err != nil {
		return Task{}, err
	}
	if err := pruneCompleted(paths, previous.JobID); err != nil {
		return Task{}, err
	}
	// Older successful workers left their executable on tmpfs. The control
	// lock, absent journal and idle worker prove it is no longer needed; drop
	// it before reserving space for the new atomic copy. Otherwise the first
	// update after upgrading from such a version can still count two workers.
	if stale, err := os.Lstat(paths.Worker()); err == nil {
		if err := cleanupCompletedWorker(paths, stale); err != nil {
			return Task{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Task{}, storageError(err)
	}
	task := Task{SchemaVersion: 1, JobID: newID(), Plan: candidate.Plan, Recovery: candidate.Recovery,
		Phase: "queued", CreatedAt: time.Now().UTC(), WasRunning: wasRunning, Local: candidate.Local}
	journalWritten := false
	defer func() {
		if !journalWritten {
			r := receiptFor(task)
			// Preparation failures before a durable journal have not installed
			// anything. Delete only this fresh task's explicitly owned files.
			_ = removeOwnedFiles(paths.Temporary(task.JobID), r.TemporaryFiles, true)
			_ = removeOwnedFiles(paths.Backup(task.JobID), r.BackupFiles, true)
		}
	}()
	if err := privateDir(paths.Runtime); err != nil {
		return Task{}, err
	}
	if err := privateDir(paths.Backup(task.JobID)); err != nil {
		return Task{}, err
	}
	if err := checkSpace(paths, task, false); err != nil {
		return Task{}, err
	}
	if err := copyWorker(executable, paths.Worker()); err != nil {
		return Task{}, err
	}
	if candidate.Local {
		if err := stageLocal(paths, task); err != nil {
			return Task{}, err
		}
	}
	if err := writeState(paths.Intent(), serviceIntent{task.JobID, wasRunning}); err != nil {
		return Task{}, err
	}
	if err := writeState(paths.Journal(), task); err != nil {
		// Rename may have succeeded before directory fsync failed. Never
		// remove recovery inputs when even a damaged journal remains.
		_, statErr := os.Lstat(paths.Journal())
		journalWritten = !errors.Is(statErr, os.ErrNotExist)
		return Task{}, err
	}
	journalWritten = true
	if err := launch(); err != nil {
		return task, domain.Errorf(domain.CodeRecoveryRequired, "独立更新服务未启动；原安装未改动，请运行 srunnet update recover").Wrap(err)
	}
	return task, nil
}

func copyWorker(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return storageError(err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxPayloadBytes {
		return storageError(err)
	}
	output, err := os.CreateTemp(filepath.Dir(destination), ".worker-*")
	if err != nil {
		return storageError(err)
	}
	defer output.Close()
	defer os.Remove(output.Name())
	n, err := io.Copy(output, io.LimitReader(input, MaxPayloadBytes+1))
	if err != nil || n != info.Size() {
		return storageError(err)
	}
	if err := output.Chmod(0o700); err != nil {
		return storageError(err)
	}
	if err := output.Sync(); err != nil {
		return storageError(err)
	}
	if err := output.Close(); err != nil {
		return storageError(err)
	}
	if err := os.Rename(output.Name(), destination); err != nil {
		return storageError(err)
	}
	return syncDirectory(filepath.Dir(destination))
}

func checkSpace(paths Paths, task Task, staged bool) error {
	return checkSpaceWith(paths, task, staged, FreeBytes)
}

func checkSpaceWith(paths Paths, task Task, staged bool, freeBytes func(string) (uint64, error)) error {
	var oldBytes int64
	for _, asset := range task.Recovery.Assets {
		oldBytes += asset.Bytes
	}
	// Keep compressed recovery packages and private config backups on flash.
	// tmpfs needs the copied worker, new packages and two unpack/work buffers.
	tmpNeed := int64(MaxPayloadBytes) + task.Plan.DownloadBytes + 2*task.Plan.InstalledBytes + (8 << 20)
	flashNeed := oldBytes + task.Plan.InstalledBytes + (4 << 20)
	if staged {
		// Begin has already copied the independent worker. Existing package
		// files also already consume the free space reported by statfs. Reserve
		// only the remaining allocations, retaining both unpack/work buffers.
		// This accounting grants no trust: VerifyFile and native verification
		// still run for every package before any installation starts.
		tmpNeed -= int64(MaxPayloadBytes)
		tmpNeed -= stagedBytes(packageFiles(paths.Temporary(task.JobID), task.Plan.Assets))
		flashNeed -= stagedBytes(packageFiles(paths.Backup(task.JobID), task.Recovery.Assets))
	}
	for _, requirement := range []struct {
		path  string
		bytes int64
	}{{paths.Runtime, tmpNeed}, {paths.Config, flashNeed}} {
		free, err := freeBytes(requirement.path)
		if err != nil {
			return err
		}
		if free < uint64(requirement.bytes) {
			return domain.Errorf(domain.CodePackageIncompatible, "更新所需的临时空间或持久恢复空间不足")
		}
	}
	return nil
}

func stagedBytes(files []LocalPackage) int64 {
	var bytes int64
	for _, file := range files {
		info, err := os.Lstat(file.Path)
		if err == nil && info.Mode().IsRegular() && info.Size() == file.Asset.Bytes {
			bytes += file.Asset.Bytes
		}
	}
	return bytes
}

type Worker struct {
	Paths   Paths
	Source  ReleaseSource
	Device  Device
	Service Service
}

func (w Worker) Run(ctx context.Context) (result error) {
	lock, err := acquireLock(w.Paths.WorkerLock())
	if err != nil {
		return err
	}
	defer lock.Close()
	workerInfo, _ := os.Lstat(w.Paths.Worker())
	defer func() {
		if result == nil {
			// The executable must remain available throughout a failed install
			// and recovery. A completed task has no journal and no more work for
			// this private copy. Unlink before releasing the worker lock so a
			// subsequent task cannot replace it between identification and cleanup.
			if err := cleanupCompletedWorker(w.Paths, workerInfo); err != nil {
				if status, readErr := ReadStatus(w.Paths); readErr == nil {
					status.Message += "；临时更新程序未能清理"
					_ = writeState(w.Paths.Status(), status)
				}
			}
		}
	}()
	task, err := ReadTask(w.Paths)
	if err != nil {
		return storageError(err)
	}
	if task.Phase != "queued" {
		return w.fail(task, domain.Errorf(domain.CodeRecoveryRequired, "上次更新未完成；请先运行 srunnet update recover"))
	}
	if task.Recover {
		return w.recover(task)
	}
	if err := w.progress(&task, "preparing", "正在核验安装环境与恢复包"); err != nil {
		return err
	}
	if err := w.prepare(ctx, &task); err != nil {
		return w.fail(task, err)
	}
	// Context cancellation is safe only before this durable boundary. Past it,
	// worker shutdown requests never kill or abandon a native package install.
	if err := ctx.Err(); err != nil {
		return w.fail(task, domain.Errorf(domain.CodeCancelled, "安装前已取消更新").Wrap(err))
	}
	task.InstallStarted = true
	if err := w.progress(&task, "installing", "正在安装，请勿断电"); err != nil {
		return err
	}
	if err := w.Service.Stop(context.Background()); err != nil {
		return w.fail(task, err)
	}
	files := packageFiles(w.Paths.Temporary(task.JobID), task.Plan.Assets)
	// Recheck after stopping the service; no unverified cached file reaches a
	// package manager, even when it was pre-staged by an explicit local install.
	for _, file := range files {
		if err := w.Device.Verify(context.Background(), file); err != nil {
			return w.fail(task, err)
		}
	}
	if err := w.Device.Install(files); err != nil {
		return w.fail(task, err)
	}
	return w.finish(task, task.Plan.Release, task.Plan.Assets, "completed", "更新完成")
}

func packageFiles(directory string, assets []Asset) []LocalPackage {
	files := make([]LocalPackage, 0, len(assets))
	for _, asset := range assets {
		files = append(files, LocalPackage{asset, filepath.Join(directory, asset.ID+"."+asset.Format)})
	}
	return files
}

func (w Worker) prepare(ctx context.Context, task *Task) error {
	inventory, err := w.Device.Inventory(ctx, task.Plan.Inventory.DisplayVersion)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(inventory, task.Plan.Inventory) {
		return domain.Errorf(domain.CodeConflict, "本机包版本或环境已变化，请重新检查更新")
	}
	if err := checkSpace(w.Paths, *task, true); err != nil {
		return err
	}
	if err := privateDir(w.Paths.Temporary(task.JobID)); err != nil {
		return err
	}
	old := packageFiles(w.Paths.Backup(task.JobID), task.Recovery.Assets)
	current := packageFiles(w.Paths.Temporary(task.JobID), task.Plan.Assets)
	for _, file := range append(old, current...) {
		if _, err := os.Lstat(file.Path); errors.Is(err, os.ErrNotExist) {
			if task.Local {
				return domain.Errorf(domain.CodePackageIncompatible, "本地更新缺少已校验的安装包，请重新上传")
			}
			if err := w.Source.Download(ctx, file.Asset, file.Path); err != nil {
				return err
			}
		} else if err != nil {
			return storageError(err)
		}
		if err := w.Device.Verify(ctx, file); err != nil {
			return err
		}
	}
	if err := w.backupConfig(*task); err != nil {
		return err
	}
	if err := syncDirectory(w.Paths.Backup(task.JobID)); err != nil {
		return err
	}
	task.RecoveryReady = true
	if err := writeState(w.Paths.Journal(), task); err != nil {
		return err
	}
	return w.Device.Precheck(ctx, current)
}

func (w Worker) backupConfig(task Task) error {
	for _, name := range []string{"config.json", "user-presets.json"} {
		data, err := readPrivate(filepath.Join(w.Paths.Config, name), 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return storageError(err)
		}
		// This task gets a fresh ID, so an existing backup indicates an
		// interrupted preparation. Never replace an earlier backup with new data.
		path := filepath.Join(w.Paths.Backup(task.JobID), name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return storageError(err)
		}
		_, writeErr := file.Write(data)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return storageError(writeErr)
		}
		if closeErr != nil {
			return storageError(closeErr)
		}
	}
	return nil
}

func (w Worker) progress(task *Task, phase, message string) error {
	task.Phase = phase
	if err := writeState(w.Paths.Journal(), task); err != nil {
		return err
	}
	return writeState(w.Paths.Status(), taskStatus(*task, phase, message))
}

func (w Worker) fail(task Task, cause error) error {
	task.Phase = "recovery_required"
	status := taskStatus(task, task.Phase, "")
	status.Running, status.OK = false, false
	status.Code, status.Message = ErrorStatus(cause)
	status.Message += "；运行 srunnet update recover 查看并恢复"
	status.RecoveryDirectory = w.Paths.Backup(task.JobID)
	if task.RecoveryReady {
		status.ConfigBackup = w.Paths.Backup(task.JobID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	status.Installed, _ = w.Device.InstalledVersions(ctx)
	cancel()
	if err := writeState(w.Paths.Journal(), task); err != nil {
		return err
	}
	if err := writeState(w.Paths.Status(), status); err != nil {
		return err
	}
	return cause
}

func (w Worker) finish(task Task, version string, assets []Asset, phase, message string) error {
	if err := w.progress(&task, "verifying", "正在核验安装版本与服务健康"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	versions, err := w.Device.InstalledVersions(ctx)
	expected := make(map[string]string)
	for _, asset := range assets {
		expected[asset.PackageName()] = asset.PackageVersion
	}
	if err != nil || !maps.Equal(versions, expected) {
		return w.fail(task, domain.Errorf(domain.CodeInstallFailed, "安装后的实际包版本不一致").Wrap(err))
	}
	if err := w.Service.Start(ctx); err != nil {
		return w.fail(task, err)
	}
	if err := w.Service.Health(ctx, version); err != nil {
		return w.fail(task, err)
	}
	lock, err := acquireLock(w.Paths.ControlLock())
	if err != nil {
		return w.fail(task, err)
	}
	defer lock.Close()
	var intent serviceIntent
	if err := readState(w.Paths.Intent(), &intent); err != nil || intent.JobID != task.JobID {
		return w.fail(task, storageError(err))
	}
	if !intent.Running {
		if err := w.Service.Stop(ctx); err != nil {
			return w.fail(task, err)
		}
	}
	status := taskStatus(task, phase, message)
	status.Running, status.OK, status.Installed = false, true, versions
	status.RecoveryDirectory = w.Paths.Backup(task.JobID)
	if task.RecoveryReady {
		status.ConfigBackup = w.Paths.Backup(task.JobID)
	}
	if err := writeState(w.Paths.Status(), status); err != nil {
		return err
	}
	if err := writeState(filepath.Join(w.Paths.Backup(task.JobID), "result.json"), receiptFor(task)); err != nil {
		return w.fail(task, err)
	}
	if err := os.Remove(w.Paths.Journal()); err != nil {
		return storageError(err)
	}
	if err := syncDirectory(filepath.Dir(w.Paths.Journal())); err != nil {
		return err
	}
	if err := pruneCompleted(w.Paths, task.JobID); err != nil {
		// Installed versions and health have passed. Cleanup failure is not
		// an installation failure; retain evidence and retry before next Begin.
		status.Message += "；旧更新文件清理未完成，下次更新前会重试"
		return writeState(w.Paths.Status(), status)
	}
	return nil
}

func QueueRecovery(paths Paths, executable string, launch func() error) (Task, error) {
	lock, err := acquireLock(paths.ControlLock())
	if err != nil {
		return Task{}, err
	}
	defer lock.Close()
	held, err := lockHeld(paths.WorkerLock())
	if err != nil {
		return Task{}, err
	}
	if held {
		return Task{}, domain.Errorf(domain.CodeBusy, "安装仍在进行，不能中途恢复")
	}
	task, err := ReadTask(paths)
	if err != nil {
		return Task{}, storageError(err)
	}
	if err := copyWorker(executable, paths.Worker()); err != nil {
		return Task{}, err
	}
	task.Recover, task.Phase, task.CreatedAt = true, "queued", time.Now().UTC()
	if err := writeState(paths.Journal(), task); err != nil {
		return Task{}, err
	}
	// Recovery reuses the job ID. Replace its previous failed snapshot before
	// launching, otherwise CLI/LuCI can mistake that terminal state for the
	// result of the newly queued recovery and stop waiting immediately.
	if err := writeState(paths.Status(), taskStatus(task, "queued", "恢复任务已提交")); err != nil {
		return Task{}, err
	}
	return task, launch()
}

func (w Worker) recover(task Task) error {
	if !task.InstallStarted {
		// Preparation did not cross the install boundary. Release the gate only
		// after proving that the original package database is still intact.
		return w.finish(task, task.Plan.Inventory.DisplayVersion, task.Recovery.Assets, "cancelled", "安装尚未开始，已保留原版本")
	}
	if !task.RecoveryReady {
		return w.fail(task, domain.Errorf(domain.CodeRecoveryRequired, "恢复包尚未就绪，请使用保留的清单进行人工恢复"))
	}
	if err := w.progress(&task, "recovering", "正在恢复原版本安装包；配置备份保持独立"); err != nil {
		return err
	}
	files := packageFiles(w.Paths.Backup(task.JobID), task.Recovery.Assets)
	for _, file := range files {
		if err := w.Device.Verify(context.Background(), file); err != nil {
			return w.fail(task, err)
		}
	}
	if err := w.Service.Stop(context.Background()); err != nil {
		return w.fail(task, err)
	}
	if err := w.Device.Recover(files); err != nil {
		return w.fail(task, err)
	}
	// Package recovery does not overwrite current user data with a backup.
	return w.finish(task, task.Plan.Inventory.DisplayVersion, task.Recovery.Assets, "recovered", "已恢复原安装包，配置未回写；备份保留在恢复目录")
}
