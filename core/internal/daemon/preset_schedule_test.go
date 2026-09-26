//go:build unix

package daemon

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
)

func TestPresetScheduleDailyFailureRestartAndClockRollback(t *testing.T) {
	cfg := config.Defaults()
	cfg.Revision = 7
	path := filepath.Join(t.TempDir(), "schedule.json")
	s := presetSchedule{}
	now := time.Date(2026, 9, 20, 0, 59, 0, 0, time.UTC) // 08:59 Beijing
	var requests []application.Request
	failure := errors.New("queue busy")
	submit := func(r application.Request) error { requests = append(requests, r); return failure }
	step := func(at time.Time) error { return s.step(at, cfg, "wwan", path, submit) }
	if err := step(now); err != nil || len(requests) != 0 {
		t.Fatal("ran before due time", err)
	}
	if err := step(now.Add(time.Minute)); !errors.Is(err, failure) || len(requests) != 1 {
		t.Fatal("did not attempt once", err)
	}
	if requests[0].Interface != "wwan" || !requests[0].CheckRevision || requests[0].ConfigRevision != 7 {
		t.Fatal(requests[0])
	}
	// Failure still consumes today's automatic attempt. A service restart and
	// unrelated settings saves cannot turn it into a repeated request loop.
	s = presetSchedule{}
	if exists, err := readRuntimeRecord(path, &s); !exists || err != nil {
		t.Fatal(exists, err)
	}
	cfg.Revision++
	for _, at := range []time.Time{now.Add(time.Hour), now.Add(14 * time.Hour), now.Add(-24 * time.Hour)} {
		if err := step(at); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 1 {
		t.Fatal("repeated same-day request", requests)
	}
	if err := step(now.Add(24*time.Hour + time.Minute)); !errors.Is(err, failure) || len(requests) != 2 {
		t.Fatal("next day missing", err)
	}
}

func TestPresetScheduleDisabledMissingLineAndUnavailableStorage(t *testing.T) {
	cfg := config.Defaults()
	path := filepath.Join(t.TempDir(), "schedule.json")
	s := presetSchedule{}
	now := time.Date(2026, 9, 20, 5, 0, 0, 0, time.UTC)
	calls := 0
	submit := func(application.Request) error { calls++; return nil }
	cfg.PresetUpdates.Enabled = false
	if err := s.step(now, cfg, "wan", path, submit); err != nil {
		t.Fatal(err)
	}
	cfg.PresetUpdates.Enabled = true
	if err := s.step(now, cfg, "", path, submit); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || s.Day != "" {
		t.Fatal("disabled/missing line consumed schedule")
	}
	badPath := filepath.Join(path, "missing", "schedule.json")
	if err := s.step(now, cfg, "wan", badPath, submit); err == nil {
		t.Fatal("storage error hidden")
	}
	if err := s.step(now.Add(time.Minute), cfg, "wan", badPath, submit); err != nil {
		t.Fatal("storage errors repeated", err)
	}
	if calls != 0 {
		t.Fatal("network started without retaining daily claim")
	}
}
