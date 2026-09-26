package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type fixtureSource struct {
	manifests  map[Version]Manifest
	candidates []Version
	fail       error
}

func (s *fixtureSource) Candidates(context.Context, Version, string) ([]Version, error) {
	return s.candidates, s.fail
}
func (s *fixtureSource) Manifest(_ context.Context, v Version) (Manifest, error) {
	m, ok := s.manifests[v]
	if !ok {
		return m, domain.Errorf(domain.CodeNotFound, "missing")
	}
	return m, nil
}
func (s *fixtureSource) Download(_ context.Context, a Asset, path string) error {
	if s.fail != nil {
		return s.fail
	}
	return os.WriteFile(path, []byte(a.PackageVersion), 0o600)
}

type fixtureDevice struct {
	inventory  Inventory
	installed  map[string]string
	install    func([]LocalPackage) error
	calls      int
	recoveries int
}

func (d *fixtureDevice) Inventory(context.Context, string) (Inventory, error) {
	return d.inventory, nil
}
func (d *fixtureDevice) InstalledVersions(context.Context) (map[string]string, error) {
	return maps.Clone(d.installed), nil
}
func (d *fixtureDevice) Verify(_ context.Context, file LocalPackage) error { return VerifyFile(file) }
func (d *fixtureDevice) Precheck(context.Context, []LocalPackage) error    { return nil }
func (d *fixtureDevice) Install(files []LocalPackage) error {
	d.calls++
	if d.install != nil {
		return d.install(files)
	}
	for _, f := range files {
		d.installed[f.Asset.PackageName()] = f.Asset.PackageVersion
	}
	return nil
}
func (d *fixtureDevice) Recover(files []LocalPackage) error {
	d.recoveries++
	for _, f := range files {
		d.installed[f.Asset.PackageName()] = f.Asset.PackageVersion
	}
	return nil
}

type fixtureService struct {
	starts, stops int
	healthErr     error
	running       bool
	version       string
}

func (s *fixtureService) Stop(context.Context) error  { s.stops++; s.running = false; return nil }
func (s *fixtureService) Start(context.Context) error { s.starts++; s.running = true; return nil }
func (s *fixtureService) Health(_ context.Context, version string) error {
	s.version = version
	return s.healthErr
}

func workerFixture(t *testing.T, mode string, wasRunning bool) (Worker, Task, *fixtureDevice, *fixtureService, string) {
	t.Helper()
	paths := Paths{Runtime: filepath.Join(t.TempDir(), "run"), Config: filepath.Join(t.TempDir(), "config")}
	if err := privateDir(paths.Config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Config, "config.json"), []byte(`{"private":"original"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := fixtureManifest("opkg", "core", "luci", "bundle")
	recovery := fixtureManifest("opkg", "core", "luci", "bundle")
	recovery.Release = "2.0.0rc2"
	for i := range recovery.Assets {
		recovery.Assets[i].PackageVersion = "2.0.0~rc2-r1"
		recovery.Assets[i].URL = strings.Replace(recovery.Assets[i].URL, "2.0.0rc10/", "2.0.0rc2/", 1)
	}
	for _, m := range []*Manifest{&manifest, &recovery} {
		for i := range m.Assets {
			a := &m.Assets[i]
			digest := sha256.Sum256([]byte(a.PackageVersion))
			a.SHA256 = hex.EncodeToString(digest[:])
			a.Bytes = int64(len(a.PackageVersion))
		}
	}
	inventory := fixtureInventory("opkg", mode)
	plan, err := BuildPlan(manifest, inventory, "")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "worker")
	if err := os.WriteFile(executable, []byte("test-worker"), 0o700); err != nil {
		t.Fatal(err)
	}
	task, err := Begin(paths, Candidate{Plan: plan, Recovery: recovery}, wasRunning, executable, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	device := &fixtureDevice{inventory: inventory, installed: maps.Clone(inventory.Packages)}
	service := &fixtureService{running: wasRunning}
	return Worker{paths, &fixtureSource{}, device, service}, task, device, service, executable
}

func TestWorkerIgnoresCancellationAfterInstallBoundaryAndHonorsStop(t *testing.T) {
	w, task, device, service, _ := workerFixture(t, "split", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	device.install = func(files []LocalPackage) error {
		if Guard(w.Paths) == nil {
			t.Fatal("mutation gate open during install")
		}
		cancel()
		if err := RecordServiceIntent(w.Paths, false); err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			device.installed[file.Asset.PackageName()] = file.Asset.PackageVersion
		}
		return nil
	}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(w.Paths)
	if err != nil || !status.OK || status.Running || status.Phase != "completed" || status.JobID != task.JobID {
		t.Fatalf("status %+v %v", status, err)
	}
	if service.running || service.starts != 1 || service.stops != 2 || device.calls != 1 {
		t.Fatal("stop intent/cancellation altered native transaction")
	}
	if Guard(w.Paths) != nil {
		t.Fatal("gate not released after verification")
	}
}

func TestWorkerPartialSplitFailureReportsActualVersionsAndRecovers(t *testing.T) {
	w, task, device, service, executable := workerFixture(t, "split", true)
	device.install = func(files []LocalPackage) error {
		device.installed[files[0].Asset.PackageName()] = files[0].Asset.PackageVersion
		return domain.Errorf(domain.CodeInstallFailed, "synthetic second-package failure")
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("partial install succeeded")
	}
	status, err := ReadStatus(w.Paths)
	if err != nil || status.OK || status.Running || status.Installed["smart-srun"] != "2.0.0~rc10-r1" || status.Installed["luci-app-smart-srun"] != "2.0.0~rc2-r1" {
		t.Fatalf("partial status %+v %v", status, err)
	}
	if Guard(w.Paths) == nil || service.running {
		t.Fatal("unsafe service or mutation gate after partial install")
	}
	// An external admin edit after failure must never replace the old backup
	// or be silently overwritten by package recovery.
	configPath := filepath.Join(w.Paths.Config, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"private":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueRecovery(w.Paths, executable, func() error {
		queued, err := ReadStatus(w.Paths)
		if err != nil || !queued.Running || queued.Phase != "queued" || queued.JobID != task.JobID {
			t.Fatalf("recovery exposed previous terminal state: %+v %v", queued, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = ReadStatus(w.Paths)
	if err != nil || !status.OK || status.Phase != "recovered" || device.recoveries != 1 || !service.running || service.version != "2.0.0rc2" {
		t.Fatalf("recovery %+v %v", status, err)
	}
	data, _ := os.ReadFile(configPath)
	backup, _ := os.ReadFile(filepath.Join(w.Paths.Backup(task.JobID), "config.json"))
	if !strings.Contains(string(data), "changed") || !strings.Contains(string(backup), "original") {
		t.Fatal("configuration backup/recovery conflated")
	}
	if strings.Contains(status.Message, "private") {
		t.Fatal("private config leaked into status")
	}
}

func TestPreparationFailureAndOfflineOriginNeverInstall(t *testing.T) {
	w, _, device, service, executable := workerFixture(t, "bundle", false)
	w.Source = &fixtureSource{fail: domain.Errorf(domain.CodeTransportFailure, "download unavailable")}
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("download failure accepted")
	}
	if device.calls != 0 || service.starts != 0 || service.stops != 0 {
		t.Fatal("preparation touched service or installer")
	}
	if _, err := QueueRecovery(w.Paths, executable, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.running || service.starts != 1 || service.stops != 1 || device.recoveries != 0 {
		t.Fatal("original stopped state lost")
	}
}

func TestWorkerLockAndCorruptJournalFailClosed(t *testing.T) {
	w, _, device, _, executable := workerFixture(t, "core", true)
	lock, err := acquireLock(w.Paths.WorkerLock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := QueueRecovery(w.Paths, executable, func() error { return nil }); err == nil {
		t.Fatal("live worker interrupted")
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("second worker allowed")
	}
	lock.Close()
	if err := os.WriteFile(w.Paths.Journal(), []byte(`{"schema_version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if Guard(w.Paths) == nil {
		t.Fatal("corrupt journal opened mutation gate")
	}
	if err := w.Run(context.Background()); err == nil || device.calls != 0 {
		t.Fatal("corrupt journal installed")
	}
}

func TestReadStatusIsReadOnlyAndRejectsPartialSnapshot(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{filepath.Join(dir, "run"), filepath.Join(dir, "config")}
	status, err := ReadStatus(paths)
	if err != nil || status.Phase != "idle" {
		t.Fatalf("idle %v %v", status, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("status created files")
	}
	if err := privateDir(paths.Runtime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Status(), []byte(`{"schema_version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(paths); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	if err := os.Remove(paths.Status()); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestBrokenInstalledVersionIsNotReportedAsNoUpdate(t *testing.T) {
	inventory := fixtureInventory("opkg", "core")
	inventory.Packages["smart-srun"] = "2.0.0~rc10-r1"
	_, result, err := Check(context.Background(), &fixtureSource{}, inventory, "")
	if err == nil || result.OK {
		t.Fatal("inconsistent running/native versions looked up to date")
	}
}
