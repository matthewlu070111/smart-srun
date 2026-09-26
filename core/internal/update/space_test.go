package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedUpdateDoesNotReserveItsWorkerAndPackagesTwice(t *testing.T) {
	w, task, _, _, _ := workerFixture(t, "split", true)
	// A 128 MiB guest had approximately 31 MiB of tmpfs left after Begin.
	// Keep the real unpack/work budget; only already allocated inputs vanish
	// from the remaining requirement.
	task.Plan.InstalledBytes = 10 << 20
	task.Plan.DownloadBytes = 4 << 20
	task.Plan.Assets = []Asset{{ID: "new", Format: "apk", Bytes: 4 << 20}}
	task.Recovery.Assets = []Asset{{ID: "old", Format: "apk", Bytes: 4 << 20}}
	if err := privateDir(w.Paths.Temporary(task.JobID)); err != nil {
		t.Fatal(err)
	}
	for _, file := range append(packageFiles(w.Paths.Temporary(task.JobID), task.Plan.Assets), packageFiles(w.Paths.Backup(task.JobID), task.Recovery.Assets)...) {
		if err := os.WriteFile(file.Path, make([]byte, file.Asset.Bytes), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	free := func(path string) (uint64, error) {
		if path == w.Paths.Runtime {
			return 31 << 20, nil
		}
		return 15 << 20, nil
	}
	if err := checkSpaceWith(w.Paths, task, false, free); err == nil {
		t.Fatal("preparation without existing allocations should require more space")
	}
	if err := checkSpaceWith(w.Paths, task, true, free); err != nil {
		t.Fatalf("allocated files were charged twice: %v", err)
	}
	// Deleting a package means it must be downloaded again. An unrelated
	// file, a directory, or a truncated package cannot make that space free.
	path := packageFiles(w.Paths.Temporary(task.JobID), task.Plan.Assets)[0].Path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "unrelated.apk"), make([]byte, 4<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, shape := range []string{"missing", "truncated", "directory"} {
		switch shape {
		case "truncated":
			if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
				t.Fatal(err)
			}
		case "directory":
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := checkSpaceWith(w.Paths, task, true, free); err == nil {
			t.Fatalf("%s package incorrectly credited", shape)
		}
	}
}

func TestStagedUpdateStillReservesUnpackBuffers(t *testing.T) {
	w, task, _, _, _ := workerFixture(t, "bundle", true)
	task.Plan.InstalledBytes = 10 << 20
	free := func(string) (uint64, error) { return 27 << 20, nil }
	if err := checkSpaceWith(w.Paths, task, true, free); err == nil {
		t.Fatal("update allowed without unpack buffers and safety reserve")
	}
}
