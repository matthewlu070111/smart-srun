//go:build unix

package logstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// A log line names accounts and interfaces. It is not public, and the mode is
// the only thing that makes that true on a device where every process runs as
// root's neighbour.
func TestTheLogAndItsDirectoryAreNotWorldReadable(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	store := New(filepath.Join(directory, "plugin.log"))
	store.OnError(func(err error) { t.Errorf("unexpected log fault: %v", err) })
	store.Emit(noon, domain.LogInfo, EventDaemonStart, "")

	info, err := os.Stat(filepath.Join(directory, "plugin.log"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != FileMode.Perm() {
		t.Errorf("file mode = %v, want %v", info.Mode().Perm(), FileMode.Perm())
	}
	folder, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if folder.Mode().Perm() != DirMode.Perm() {
		t.Errorf("directory mode = %v, want %v", folder.Mode().Perm(), DirMode.Perm())
	}
}

// Rotation republishes the file. The mode has to survive it, or a log that has
// been running long enough to rotate becomes readable by anyone.
func TestRotationKeepsTheMode(t *testing.T) {
	store, path := newStore(t)
	message := string(make([]byte, 0, MaxMessageBytes))
	for range MaxMessageBytes {
		message += "z"
	}
	for index := range 700 {
		store.Emit(noon, domain.LogInfo, EventActionResult, message,
			F("n", string(rune('a'+index%26))))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > MaxBytes {
		t.Fatalf("file is %d bytes, want it rotated below %d", info.Size(), MaxBytes)
	}
	if info.Mode().Perm() != FileMode.Perm() {
		t.Errorf("mode after rotation = %v, want %v", info.Mode().Perm(), FileMode.Perm())
	}
}
