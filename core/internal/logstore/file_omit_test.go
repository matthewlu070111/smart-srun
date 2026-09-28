package logstore

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func tailText(store *Store) string {
	return strings.Join(store.Tail(TailQuery{}).Lines, "\n")
}

// log.file_omit_info: routine INFO stays in the page's live view and out of
// the file; warnings and errors still reach the file.
func TestFileOmitInfoKeepsInfoInTheTailOnly(t *testing.T) {
	store, path := newStore(t)
	store.SetFileOmitInfo(true)
	if !store.FileOmitInfo() {
		t.Fatal("setting not recorded")
	}
	store.Emit(noon, domain.LogInfo, EventMaintainQueued, "routine-info")
	store.Emit(noon, domain.LogWarn, EventRetryScheduled, "a-warning")
	store.Emit(noon, domain.LogError, EventInternalError, "an-error")

	file := readFile(t, path)
	if strings.Contains(file, "routine-info") {
		t.Errorf("INFO reached the file: %q", file)
	}
	for _, want := range []string{"a-warning", "an-error"} {
		if !strings.Contains(file, want) {
			t.Errorf("file = %q, missing %s", file, want)
		}
	}
	tail := tailText(store)
	for _, want := range []string{"routine-info", "a-warning", "an-error"} {
		if !strings.Contains(tail, want) {
			t.Errorf("tail = %q, missing %s", tail, want)
		}
	}
}

func TestFileOmitInfoKeepsTheLifecycleInTheFile(t *testing.T) {
	store, path := newStore(t)
	store.SetFileOmitInfo(true)
	for _, event := range []string{EventDaemonStart, EventDaemonStop, EventConfigApplied, EventLogCleared} {
		store.Emit(noon, domain.LogInfo, event, "")
	}
	store.Emit(noon, domain.LogInfo, EventConfigLoaded, "")
	file := readFile(t, path)
	for _, want := range []string{EventDaemonStart, EventDaemonStop, EventConfigApplied, EventLogCleared} {
		if !strings.Contains(file, want) {
			t.Errorf("lifecycle event %s missing from file %q", want, file)
		}
	}
	if strings.Contains(file, EventConfigLoaded) {
		t.Errorf("non-lifecycle INFO written: %q", file)
	}
}

func TestFileOmitInfoOffWritesInfo(t *testing.T) {
	store, path := newStore(t)
	store.SetFileOmitInfo(true)
	store.SetFileOmitInfo(false)
	store.Emit(noon, domain.LogInfo, EventMaintainQueued, "routine-info")
	if file := readFile(t, path); !strings.Contains(file, "routine-info") {
		t.Errorf("switch off but INFO missing from file: %q", file)
	}
}

// Somebody who raised the level to collect detail gets all of it in the file.
func TestFileOmitInfoIsIgnoredAtDebugAndAll(t *testing.T) {
	for _, level := range []domain.LogLevel{domain.LogDebug, domain.LogAll} {
		t.Run(string(level), func(t *testing.T) {
			store, path := newStore(t)
			store.SetFileOmitInfo(true)
			store.SetLevel(level)
			store.Emit(noon, domain.LogInfo, EventMaintainQueued, "routine-info")
			store.Emit(noon, domain.LogDebug, EventActionPhase, "a-debug")
			file := readFile(t, path)
			for _, want := range []string{"routine-info", "a-debug"} {
				if !strings.Contains(file, want) {
					t.Errorf("file = %q, missing %s", file, want)
				}
			}
		})
	}
}

func TestFileOmitInfoDefaultsOffInTheStore(t *testing.T) {
	store, path := newStore(t)
	if store.FileOmitInfo() {
		t.Fatal("a bare store omits INFO; the daemon applies the configured default")
	}
	store.Emit(noon, domain.LogInfo, EventMaintainQueued, "routine-info")
	if file := readFile(t, path); !strings.Contains(file, "routine-info") {
		t.Errorf("file = %q", file)
	}
}
