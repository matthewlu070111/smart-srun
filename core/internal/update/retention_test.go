package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func completedFixture(t *testing.T, paths Paths, number int) string {
	t.Helper()
	id := fmt.Sprintf("%032x", number)
	r := backupReceipt{SchemaVersion: 1, JobID: id, CompletedAt: time.Unix(int64(number), 0),
		BackupFiles: []string{"old.ipk", "config.json"}, TemporaryFiles: []string{"new.ipk"}}
	for _, dir := range []string{paths.Backup(id), paths.Temporary(id)} {
		if err := privateDir(dir); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(paths.Backup(id), "old.ipk"), filepath.Join(paths.Backup(id), "config.json"), filepath.Join(paths.Temporary(id), "new.ipk")} {
		if err := os.WriteFile(file, []byte("retained"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeState(filepath.Join(paths.Backup(id), "result.json"), r); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCompletedRetentionKeepsTwoBackupsAndProtectsLatestStatus(t *testing.T) {
	paths := Paths{Runtime: filepath.Join(t.TempDir(), "run"), Config: filepath.Join(t.TempDir(), "config")}
	// Protect the current status even when the clock moved backwards.
	current := completedFixture(t, paths, 1)
	for i := 2; i <= 12; i++ {
		completedFixture(t, paths, i)
	}
	legacy := paths.Backup(fmt.Sprintf("%032x", 99))
	if err := privateDir(legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pruneCompleted(paths, current); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("%032x", i)
		_, err := os.Stat(paths.Temporary(id))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("completed downloads retained", id, err)
		}
		_, err = os.Stat(filepath.Join(paths.Backup(id), "config.json"))
		if want := i == 1 || i == 12; (err == nil) != want {
			t.Fatal("wrong recovery retention", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(legacy, "config.json")); err != nil {
		t.Fatal("unmarked legacy evidence removed", err)
	}
	if err := pruneCompleted(paths, current); err != nil {
		t.Fatal("cleanup not repeatable", err)
	}
}

func TestRetentionNeverCleansActiveOrDamagedJournal(t *testing.T) {
	w, task, _, _, _ := workerFixture(t, "split", true)
	old := completedFixture(t, w.Paths, 10)
	if err := pruneCompleted(w.Paths, task.JobID); err == nil {
		t.Fatal("active journal ignored")
	}
	if err := os.WriteFile(w.Paths.Journal(), []byte("truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pruneCompleted(w.Paths, task.JobID); err == nil {
		t.Fatal("damaged journal ignored")
	}
	if _, err := os.Stat(filepath.Join(w.Paths.Temporary(old), "new.ipk")); err != nil {
		t.Fatal("active recovery evidence cleaned", err)
	}
}

func TestRetentionRefusesForeignFilesAndSymlinksBeforeRemoval(t *testing.T) {
	for _, kind := range []string{"unknown", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "owned")
			if err := privateDir(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "old.ipk"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "unknown":
				err = os.WriteFile(filepath.Join(dir, "foreign"), []byte("keep"), 0o600)
			case "symlink":
				err = os.Symlink(outside, filepath.Join(dir, "config.json"))
			case "directory":
				err = os.Mkdir(filepath.Join(dir, "config.json"), 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := removeOwnedFiles(dir, []string{"old.ipk", "config.json"}, true); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "old.ipk")); err != nil {
				t.Fatal("deleted before full validation", err)
			}
			if data, _ := os.ReadFile(outside); string(data) != "untouched" {
				t.Fatal("followed a link")
			}
		})
	}
}

func TestWorkerCompletesWithBoundedRecoveryAndNoDownloadResidue(t *testing.T) {
	w, task, _, _, executable := workerFixture(t, "split", true)
	for i := 1; i <= 6; i++ {
		completedFixture(t, w.Paths, i)
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Paths.Temporary(task.JobID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new packages retained", err)
	}
	if _, err := os.Stat(filepath.Join(w.Paths.Backup(task.JobID), "config.json")); err != nil {
		t.Fatal("latest backup lost", err)
	}
	// A previous worker may still own its lock just after releasing the journal.
	lock, err := acquireLock(w.Paths.WorkerLock())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := Begin(w.Paths, Candidate{Plan: task.Plan, Recovery: task.Recovery}, true, executable, func() error { t.Fatal("overlapping worker launched"); return nil }); err == nil {
		t.Fatal("active worker overwritten")
	}
}

func TestBeginFailureBeforeJournalDoesNotAccumulateEmptyTaskDirectories(t *testing.T) {
	w, task, _, _, _ := workerFixture(t, "split", false)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		_, err := Begin(w.Paths, Candidate{Plan: task.Plan, Recovery: task.Recovery}, false,
			filepath.Join(t.TempDir(), "missing-worker"), func() error { t.Fatal("invalid worker launched"); return nil })
		if err == nil {
			t.Fatal("missing executable accepted")
		}
	}
	entries, err := os.ReadDir(filepath.Dir(w.Paths.Journal()))
	if err != nil {
		t.Fatal(err)
	}
	var directories int
	for _, entry := range entries {
		if entry.IsDir() {
			directories++
		}
	}
	if directories != 1 {
		t.Fatalf("failed preparations left %d directories", directories)
	}
	if err := Guard(w.Paths); err != nil {
		t.Fatal("failed pre-journal preparation closed the gate", err)
	}
}

func TestSuccessfulInstallWithUnsafeOldCleanupRemainsSuccessful(t *testing.T) {
	w, task, device, _, _ := workerFixture(t, "split", true)
	old := completedFixture(t, w.Paths, 1)
	if err := os.WriteFile(filepath.Join(w.Paths.Temporary(old), "foreign"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal("cleanup error misreported as installation failure", err)
	}
	status, err := ReadStatus(w.Paths)
	if err != nil || !status.OK || status.Phase != "completed" || device.calls != 1 {
		t.Fatal("wrong terminal result", status, err)
	}
	if status.JobID != task.JobID || Guard(w.Paths) != nil {
		t.Fatal("cleanup failed to release successful install gate")
	}
	if _, err := os.Stat(filepath.Join(w.Paths.Temporary(old), "new.ipk")); err != nil {
		t.Fatal("unknown contents deleted", err)
	}
}
