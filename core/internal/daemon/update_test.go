//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

type checkSource struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *checkSource) Candidates(ctx context.Context, _ update.Version, _ string) ([]update.Version, error) {
	s.calls.Add(1)
	close(s.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return nil, nil
	}
}
func (*checkSource) Manifest(context.Context, update.Version) (update.Manifest, error) {
	panic("unexpected manifest")
}
func (*checkSource) Download(context.Context, update.Asset, string) error {
	panic("unexpected download")
}

type checkDevice struct{}

func (checkDevice) Inventory(_ context.Context, version string) (update.Inventory, error) {
	return update.Inventory{PackageManager: "opkg", Architecture: "x86_64", FirmwareFamily: "24.10", DisplayVersion: version,
		Packages: map[string]string{"smart-srun": "2.0.0~rc1-r1"}}, nil
}
func (checkDevice) InstalledVersions(context.Context) (map[string]string, error) {
	panic("unexpected installed versions")
}
func (checkDevice) Verify(context.Context, update.LocalPackage) error { panic("unexpected verify") }
func (checkDevice) Precheck(context.Context, []update.LocalPackage) error {
	panic("unexpected precheck")
}
func (checkDevice) Install([]update.LocalPackage) error { panic("unexpected install") }
func (checkDevice) Recover([]update.LocalPackage) error { panic("unexpected recovery") }

func TestUpdateCheckRunsIndependentlyOfStatusAndStopsWithDaemon(t *testing.T) {
	source := &checkSource{entered: make(chan struct{}), release: make(chan struct{})}
	r := start(t, func(o *Options) { o.UpdateSource = source; o.UpdateDevice = checkDevice{} })
	var check update.CheckResult
	json.Unmarshal(r.call("update.check", UpdateCheckParams{}), &check)
	if !check.Running || check.JobID == "" {
		t.Fatalf("check %+v", check)
	}
	<-source.entered
	r.status()
	var duplicate update.CheckResult
	json.Unmarshal(r.call("update.check", UpdateCheckParams{}), &duplicate)
	if check.JobID != duplicate.JobID || source.calls.Load() != 1 {
		t.Fatal("duplicate check spawned more work")
	}
	var status update.CheckResult
	json.Unmarshal(r.call("update.status", UpdateStatusParams{JobID: check.JobID}), &status)
	if !status.Running {
		t.Fatal("check disappeared while still running")
	}
	r.stop()
	if err := r.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateJournalBlocksConfigurationAndActionsButNotStatus(t *testing.T) {
	r := setupSwitchRPC(t)
	if err := os.MkdirAll(r.paths.Recovery(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.paths.Update().Journal(), []byte(`{"schema_version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := r.callExpectingError("action.submit", SubmitParams{Kind: "switch_campus", AccountID: "c2", IdempotencyKey: "during-update"})
	if codeOf(t, err) != domain.CodeRecoveryRequired {
		t.Fatalf("action: %v", err)
	}
	revision := uint64(4)
	err = r.callExpectingError("config.apply", ConfigApplyParams{ExpectedRevision: &revision, Settings: json.RawMessage(`{"enabled":true}`)})
	if codeOf(t, err) != domain.CodeRecoveryRequired {
		t.Fatalf("config: %v", err)
	}
	status := r.status()
	if status.Enabled || status.ConfigRevision != 4 {
		t.Fatal("blocked mutation changed config")
	}
}
