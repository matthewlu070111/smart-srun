package update

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const MaxStateBytes = 64 << 10

type Paths struct{ Runtime, Config string }

func (p Paths) Journal() string    { return filepath.Join(p.Config, "recovery", "update.json") }
func (p Paths) Status() string     { return filepath.Join(p.Runtime, "update-status.json") }
func (p Paths) Worker() string     { return filepath.Join(p.Runtime, "update-worker") }
func (p Paths) Inbox() string      { return filepath.Join(p.Runtime, "update-inbox") }
func (p Paths) Plan() string       { return filepath.Join(p.Runtime, "update-plan.json") }
func (p Paths) Intent() string     { return filepath.Join(p.Config, "recovery", "update-intent.json") }
func (p Paths) WorkerLock() string { return filepath.Join(p.Runtime, "update-worker.lock") }
func (p Paths) ControlLock() string {
	return filepath.Join(p.Config, "recovery", "update-control.lock")
}
func (p Paths) Temporary(id string) string { return filepath.Join(p.Runtime, "update-"+id) }
func (p Paths) Backup(id string) string    { return filepath.Join(p.Config, "recovery", "update-"+id) }

func WorkerActive(paths Paths) (bool, error) { return lockHeld(paths.WorkerLock()) }

type Task struct {
	SchemaVersion  int       `json:"schema_version"`
	JobID          string    `json:"job_id"`
	Plan           Plan      `json:"plan"`
	Recovery       Manifest  `json:"recovery"`
	Phase          string    `json:"phase"`
	CreatedAt      time.Time `json:"created_at"`
	WasRunning     bool      `json:"was_running"`
	RecoveryReady  bool      `json:"recovery_ready"`
	InstallStarted bool      `json:"install_started"`
	Recover        bool      `json:"recover"`
	Local          bool      `json:"local"`
}

type Status struct {
	SchemaVersion     int               `json:"schema_version"`
	JobID             string            `json:"job_id,omitempty"`
	Running           bool              `json:"running"`
	OK                bool              `json:"ok"`
	Phase             string            `json:"phase"`
	Message           string            `json:"message"`
	Code              domain.ErrorCode  `json:"code,omitempty"`
	CurrentVersion    string            `json:"current_version,omitempty"`
	LatestVersion     string            `json:"latest_version,omitempty"`
	InstallMode       string            `json:"install_mode,omitempty"`
	PackageFormat     string            `json:"package_format,omitempty"`
	Installed         map[string]string `json:"installed,omitempty"`
	RecoveryDirectory string            `json:"recovery_directory,omitempty"`
	ConfigBackup      string            `json:"config_backup,omitempty"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

func newID() string { var id [16]byte; _, _ = rand.Read(id[:]); return hex.EncodeToString(id[:]) }
func validID(id string) bool {
	data, err := hex.DecodeString(id)
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == id
}

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return storageError(err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return storageError(err)
	}
	return os.Chmod(dir, 0o700)
}

func storageError(err error) error {
	return domain.Errorf(domain.CodeRecoveryRequired, "无法可靠读取或保存更新状态，请检查恢复目录").Wrap(err)
}

func writeState(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > MaxStateBytes {
		return storageError(err)
	}
	return writePrivate(path, data, 0o600)
}

func writePrivate(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := privateDir(dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".update-*")
	if err != nil {
		return storageError(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return storageError(err)
	}
	if _, err := file.Write(data); err != nil {
		return storageError(err)
	}
	if err := file.Sync(); err != nil {
		return storageError(err)
	}
	if err := file.Close(); err != nil {
		return storageError(err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return storageError(err)
	}
	return syncDirectory(dir)
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return storageError(err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return storageError(err)
	}
	return nil
}

func readPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > limit {
		return nil, storageError(nil)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, storageError(err)
	}
	return data, nil
}

func readState(path string, value any) error {
	data, err := readPrivate(path, MaxStateBytes)
	if err != nil {
		return err
	}
	if err := config.DecodePatch(data, value); err != nil {
		return storageError(err)
	}
	return nil
}

func ValidatePlan(plan Plan) error {
	version, err := ParseVersion(plan.Release)
	if err != nil {
		return err
	}
	rebuilt, err := BuildPlan(Manifest{SchemaVersion: 1, Release: plan.Release, Channel: version.Channel(),
		SourceCommit: plan.SourceCommit, Assets: plan.Assets}, plan.Inventory, "rc")
	if err != nil || !reflect.DeepEqual(rebuilt, plan) {
		return invalidManifest()
	}
	return nil
}

func ReadTask(paths Paths) (Task, error) {
	var task Task
	if err := readState(paths.Journal(), &task); err != nil {
		return task, err
	}
	if task.SchemaVersion != 1 || !validID(task.JobID) || task.CreatedAt.IsZero() {
		return Task{}, storageError(nil)
	}
	if err := ValidatePlan(task.Plan); err != nil {
		return Task{}, storageError(err)
	}
	if _, err := RecoveryAssets(task.Recovery, task.Plan.Inventory); err != nil {
		return Task{}, storageError(err)
	}
	switch task.Phase {
	case "queued", "preparing", "installing", "verifying", "recovery_required", "recovering":
	default:
		return Task{}, storageError(nil)
	}
	return task, nil
}

// Guard is read-only and fails closed on a damaged journal. A reboot loses
// tmpfs but cannot silently reopen authentication after a partial install.
func Guard(paths Paths) error {
	_, err := ReadTask(paths)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storageError(err)
	}
	return domain.Errorf(domain.CodeBusy, "更新正在进行或等待恢复，暂不接受配置和网络变更")
}

func ReadStatus(paths Paths) (Status, error) {
	var status Status
	err := readState(paths.Status(), &status)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, storageError(err)
	}
	task, taskErr := ReadTask(paths)
	if taskErr != nil && !errors.Is(taskErr, os.ErrNotExist) {
		return Status{}, storageError(taskErr)
	}
	if err != nil {
		status = Status{SchemaVersion: 1, OK: true, Phase: "idle", Message: "没有进行中的更新"}
	}
	if status.SchemaVersion != 1 || (status.JobID != "" && !validID(status.JobID)) || len(status.Message) > 1024 {
		return Status{}, storageError(nil)
	}
	if taskErr == nil {
		if status.JobID != task.JobID {
			status = taskStatus(task, "queued", "更新任务已提交")
		}
		held, err := lockHeld(paths.WorkerLock())
		if err != nil {
			return Status{}, err
		}
		if !held && time.Since(task.CreatedAt) > 10*time.Second {
			status.Running, status.OK, status.Phase, status.Code = false, false, "recovery_required", domain.CodeRecoveryRequired
			status.Message = "更新进程已停止；请运行 srunnet update recover 检查并恢复"
		}
	}
	return status, nil
}

func taskStatus(task Task, phase, message string) Status {
	return Status{SchemaVersion: 1, JobID: task.JobID, Running: true, Phase: phase, Message: message,
		CurrentVersion: task.Plan.Inventory.DisplayVersion, LatestVersion: task.Plan.Release,
		InstallMode: task.Plan.InstallMode, PackageFormat: task.Plan.Assets[0].Format, UpdatedAt: time.Now().UTC()}
}

type serviceIntent struct {
	JobID   string `json:"job_id"`
	Running bool   `json:"running"`
}

// RecordServiceIntent changes only the task's final service state. Stopping the
// main daemon must not cancel the independently supervised installer.
func RecordServiceIntent(paths Paths, running bool) error {
	if _, err := os.Lstat(paths.Journal()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	lock, err := acquireLock(paths.ControlLock())
	if err != nil {
		return err
	}
	defer lock.Close()
	task, err := ReadTask(paths)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeState(paths.Intent(), serviceIntent{task.JobID, running})
}
