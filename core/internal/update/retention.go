package update

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Keep two completed recovery sets, plus the active journal's set. Older
// installations without a receipt remain untouched: their ownership and
// terminal outcome cannot be inferred from directory names or modification time.
const retainedBackups = 2

func cleanupCompletedWorker(paths Paths, before os.FileInfo) error {
	if err := Guard(paths); err != nil {
		return err
	}
	current, err := os.Lstat(paths.Worker())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || before == nil || !current.Mode().IsRegular() ||
		!os.SameFile(before, current) || before.Size() != current.Size() ||
		!before.ModTime().Equal(current.ModTime()) {
		return storageError(err)
	}
	if err := os.Remove(paths.Worker()); err != nil {
		return storageError(err)
	}
	return syncDirectory(paths.Runtime)
}

type backupReceipt struct {
	SchemaVersion  int       `json:"schema_version"`
	JobID          string    `json:"job_id"`
	CompletedAt    time.Time `json:"completed_at"`
	BackupFiles    []string  `json:"backup_files"`
	TemporaryFiles []string  `json:"temporary_files"`
}

func receiptFor(task Task) backupReceipt {
	r := backupReceipt{SchemaVersion: 1, JobID: task.JobID, CompletedAt: time.Now().UTC(),
		BackupFiles: []string{"config.json", "user-presets.json"}}
	for _, a := range task.Recovery.Assets {
		r.BackupFiles = append(r.BackupFiles, a.ID+"."+a.Format)
	}
	for _, a := range task.Plan.Assets {
		r.TemporaryFiles = append(r.TemporaryFiles, a.ID+"."+a.Format)
	}
	return r
}

func validReceipt(r backupReceipt, id string) bool {
	if r.SchemaVersion != 1 || !validID(id) || r.JobID != id || r.CompletedAt.IsZero() ||
		len(r.BackupFiles) > 4 || len(r.TemporaryFiles) > 2 {
		return false
	}
	for _, names := range [][]string{r.BackupFiles, r.TemporaryFiles} {
		seen := make(map[string]bool)
		for _, name := range names {
			if seen[name] || (name != "config.json" && name != "user-presets.json" &&
				!validPackageFile(name)) {
				return false
			}
			seen[name] = true
		}
	}
	return true
}

func validPackageFile(name string) bool {
	ext := filepath.Ext(name)
	return (ext == ".ipk" || ext == ".apk") && namePattern.MatchString(strings.TrimSuffix(name, ext))
}

// Read a bounded list before deleting anything. These are flat, private task
// directories; unknown files, subdirectories and symlinks are never traversed.
func removeOwnedFiles(dir string, allowed []string, removeDir bool) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return storageError(err)
	}
	f, err := os.Open(dir)
	if err != nil {
		return storageError(err)
	}
	entries, err := f.ReadDir(9)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 8 {
		return storageError(err)
	}
	for _, entry := range entries {
		if !slices.Contains(allowed, entry.Name()) || !entry.Type().IsRegular() {
			return storageError(nil)
		}
	}
	// Preserve the receipt until last so an interrupted cleanup can resume.
	for _, entry := range entries {
		if entry.Name() != "result.json" {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return storageError(err)
			}
		}
	}
	if removeDir {
		if err := os.Remove(filepath.Join(dir, "result.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return storageError(err)
		}
		if err := os.Remove(dir); err != nil {
			return storageError(err)
		}
		return syncDirectory(filepath.Dir(dir))
	}
	return syncDirectory(dir)
}

// Caller holds the control lock. No cleanup is attempted while a journal
// exists, including a damaged one or a completed receipt before gate removal.
func pruneCompleted(paths Paths, protect string) error {
	if err := Guard(paths); err != nil {
		return err
	}
	dir := filepath.Dir(paths.Journal())
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storageError(err)
	}
	entries, err := f.ReadDir(129)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 128 {
		return storageError(err)
	}
	var receipts []backupReceipt
	for _, entry := range entries {
		id := strings.TrimPrefix(entry.Name(), "update-")
		if !strings.HasPrefix(entry.Name(), "update-") || !validID(id) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		var receipt backupReceipt
		if err := readState(filepath.Join(paths.Backup(id), "result.json"), &receipt); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil || !validReceipt(receipt, id) {
			return storageError(err)
		}
		receipts = append(receipts, receipt)
	}
	slices.SortFunc(receipts, func(a, b backupReceipt) int {
		if a.JobID == b.JobID {
			return 0
		}
		if a.JobID == protect {
			return -1
		}
		if b.JobID == protect {
			return 1
		}
		if cmp := b.CompletedAt.Compare(a.CompletedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.JobID, b.JobID)
	})
	for i, receipt := range receipts {
		// Downloaded new packages are no longer needed after a terminal result.
		if err := removeOwnedFiles(paths.Temporary(receipt.JobID), receipt.TemporaryFiles, true); err != nil {
			return err
		}
		if i >= retainedBackups {
			if err := removeOwnedFiles(paths.Backup(receipt.JobID), append(receipt.BackupFiles, "result.json"), true); err != nil {
				return err
			}
		}
	}
	return nil
}
