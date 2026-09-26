package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
)

// SnapshotSchemaVersion is the shape of the runtime state file.
const SnapshotSchemaVersion = 1

// MaxSnapshotBytes bounds a read of the state file.
//
// A reader that trusted the file's length would be trusting whatever last wrote
// to /var/run, and LuCI reads this path while the daemon is stopped -- exactly
// when nothing is guarding it.
const MaxSnapshotBytes = 1 << 20

// ServiceState is whether the process is there, which spec 02 keeps strictly
// apart from whether automatic authentication is switched on. A user who
// stopped the service has not changed their mind about wanting to log in.
type ServiceState string

const (
	ServiceRunning ServiceState = "running"
	ServiceStopped ServiceState = "stopped"
)

// ActionView is one action as a reader sees it.
//
// A shape of its own rather than application.Action, because this one is a wire
// format: adding a field to the coordinator's record should not silently change
// what LuCI receives.
type ActionView struct {
	ID                 string                    `json:"id"`
	Kind               string                    `json:"kind"`
	AccountID          string                    `json:"account_id,omitempty"`
	HotspotID          string                    `json:"hotspot_id,omitempty"`
	Interface          string                    `json:"iface,omitempty"`
	State              string                    `json:"state"`
	Phase              string                    `json:"phase,omitempty"`
	Message            string                    `json:"message,omitempty"`
	Code               domain.ErrorCode          `json:"code,omitempty"`
	QueuedAt           time.Time                 `json:"queued_at"`
	StartedAt          *time.Time                `json:"started_at,omitempty"`
	EndedAt            *time.Time                `json:"ended_at,omitempty"`
	QueueMilliseconds  int64                     `json:"queue_ms"`
	WorkerMilliseconds int64                     `json:"worker_ms"`
	Timings            []application.PhaseTiming `json:"timings,omitempty"`
}

// Snapshot is the one combined picture spec 03 requires of status.get, and the
// same picture a reader gets from the file while the service is stopped.
type Snapshot struct {
	SchemaVersion int          `json:"schema_version"`
	Service       ServiceState `json:"service"`
	// PID is informational. Nothing decides anything from it; the lock decides.
	PID int `json:"pid,omitempty"`
	// Enabled is the user's automatic-authentication switch.
	Enabled bool `json:"enabled"`
	// Pause is why automatic authentication is currently suspended, empty when
	// it is not. It is kept apart from Enabled because they are different
	// facts: quiet hours pause a service the user has switched on, and showing
	// that as "off" would have them turning on a switch that is already on.
	Pause          []string  `json:"pause,omitempty"`
	ConfigRevision uint64    `json:"config_revision"`
	Version        string    `json:"version"`
	WrittenAt      time.Time `json:"written_at"`

	Accounts             []observe.AccountView `json:"accounts"`
	Wireless             *WirelessView         `json:"wireless,omitempty"`
	Actions              []ActionView          `json:"actions"`
	ManualPausedAccounts []string              `json:"manual_paused_accounts,omitempty"`
}

// ViewOf converts a coordinator action into the wire shape.
func ViewOf(action application.Action) ActionView {
	view := ActionView{
		ID:                 action.ID,
		Kind:               string(action.Request.Kind),
		AccountID:          action.Request.AccountID,
		HotspotID:          action.Request.HotspotID,
		Interface:          action.Request.Interface,
		State:              string(action.State),
		Phase:              string(action.Phase),
		Message:            action.Message,
		Code:               action.Code,
		QueuedAt:           action.QueuedAt,
		WorkerMilliseconds: action.WorkerMilliseconds,
		Timings:            append([]application.PhaseTiming(nil), action.Timings...),
	}
	if !action.StartedAt.IsZero() {
		started := action.StartedAt
		view.StartedAt = &started
		view.QueueMilliseconds = max(0, started.Sub(action.QueuedAt).Milliseconds())
	}
	if !action.EndedAt.IsZero() {
		ended := action.EndedAt
		view.EndedAt = &ended
	}
	return view
}

// WriteSnapshot replaces the state file atomically.
//
// Temporary file plus rename, so a reader never sees half a document -- LuCI
// polls this every few seconds and would otherwise eventually catch one.
//
// Deliberately no fsync, unlike the configuration write next door. This file
// lives on tmpfs: a reboot removes it whether it was flushed or not, so there
// is nothing durability could buy. Syncing it would mean an fsync per status
// change, which on a device whose flash is the thing most likely to fail is a
// cost with no benefit. The configuration path keeps its fsync because a power
// cut there loses a user's settings.
func WriteSnapshot(paths Paths, snapshot Snapshot) error {
	snapshot.SchemaVersion = SnapshotSchemaVersion
	if snapshot.WrittenAt.IsZero() {
		snapshot.WrittenAt = time.Now().UTC()
	}
	if snapshot.Accounts == nil {
		snapshot.Accounts = []observe.AccountView{}
	}
	if snapshot.Actions == nil {
		snapshot.Actions = []ActionView{}
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return domain.Errorf(domain.CodeInternal, "无法编码状态快照").Wrap(err)
	}
	if len(encoded) > MaxSnapshotBytes {
		return domain.Errorf(domain.CodeInternal,
			"状态快照 %d 字节，超过 %d 上限", len(encoded), MaxSnapshotBytes)
	}

	if err := os.MkdirAll(paths.Runtime, RuntimeDirMode); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法创建运行目录 %s", paths.Runtime).Wrap(err)
	}
	temp, err := os.CreateTemp(paths.Runtime, ".state-*.tmp")
	if err != nil {
		return domain.Errorf(domain.CodeInternal, "无法创建临时状态文件").Wrap(err)
	}
	name := temp.Name()
	defer os.Remove(name)

	if err := temp.Chmod(RuntimeFileMode); err != nil {
		temp.Close()
		return domain.Errorf(domain.CodeInternal, "无法设置状态文件权限").Wrap(err)
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return domain.Errorf(domain.CodeInternal, "无法写入状态文件").Wrap(err)
	}
	if err := temp.Close(); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法关闭临时状态文件").Wrap(err)
	}
	if err := os.Rename(name, paths.State()); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法发布状态文件").Wrap(err)
	}
	return nil
}

// ReadSnapshot reads the state file.
//
// A missing file is not an error: it means the service has never run since the
// last reboot, which is a state, not a fault. The caller gets a stopped
// snapshot and can say so.
func ReadSnapshot(paths Paths) (Snapshot, error) {
	file, err := os.Open(paths.State())
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{SchemaVersion: SnapshotSchemaVersion,
			Service: ServiceStopped}, nil
	}
	if err != nil {
		return Snapshot{}, domain.Errorf(domain.CodeInternal,
			"无法读取状态文件").Wrap(err)
	}
	defer file.Close()

	// limit+1, so an oversized file is detected by reading one byte past the
	// limit rather than by reading all of it first.
	data, err := io.ReadAll(io.LimitReader(file, MaxSnapshotBytes+1))
	if err != nil {
		return Snapshot{}, domain.Errorf(domain.CodeInternal,
			"无法读取状态文件").Wrap(err)
	}
	if len(data) > MaxSnapshotBytes {
		return Snapshot{}, domain.Errorf(domain.CodeInternal,
			"状态文件超过 %d 字节上限", MaxSnapshotBytes)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, domain.Errorf(domain.CodeInternal,
			"状态文件格式无效").Wrap(err)
	}
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return Snapshot{}, domain.Errorf(domain.CodeInternal,
			"状态文件版本 %d，本程序只认识 %d",
			snapshot.SchemaVersion, SnapshotSchemaVersion)
	}
	return snapshot, nil
}

// MarkStopped records that the service is no longer running.
//
// Spec 02 allows exactly one writer other than the daemon to touch this file,
// and only under the conditions the caller must have already established: the
// daemon has exited and this process holds the same exclusive lock. What is
// kept is what a stopped service can still answer -- the user's own switch and
// the configuration revision -- and what is dropped is everything that was only
// true while it was running.
func MarkStopped(paths Paths, enabled bool, revision uint64, version string) error {
	return WriteSnapshot(paths, Snapshot{
		Service:        ServiceStopped,
		Enabled:        enabled,
		ConfigRevision: revision,
		Version:        version,
	})
}

// EnsureRuntimeDir creates the runtime directory with the mode spec 02 fixes,
// and tightens it if an older version left it open.
func EnsureRuntimeDir(paths Paths) error {
	if err := os.MkdirAll(paths.Runtime, RuntimeDirMode); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法创建运行目录 %s", paths.Runtime).Wrap(err)
	}
	info, err := os.Stat(paths.Runtime)
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法检查运行目录 %s", paths.Runtime).Wrap(err)
	}
	if info.Mode().Perm() != RuntimeDirMode.Perm() {
		if err := os.Chmod(paths.Runtime, RuntimeDirMode); err != nil {
			return domain.Errorf(domain.CodeInternal,
				"无法收紧运行目录权限 %s", paths.Runtime).Wrap(err)
		}
	}
	return nil
}
