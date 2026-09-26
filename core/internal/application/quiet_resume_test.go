package application

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func TestServiceRestartResumesOnlyARecordedQuietTransition(t *testing.T) {
	settings := quietWorld(t)
	quiet := policy.EvaluateQuiet(settings.cfg.Quiet, maintainEpoch)
	resume := QuietResume{Revision: settings.cfg.Revision, Occurrence: quiet.Occurrence,
		AccountID: "c1", HotspotID: "h1", StartedAt: maintainEpoch}
	for _, now := range []time.Time{maintainEpoch.Add(time.Minute), maintainEpoch.Add(time.Hour)} {
		sink := &recorder{}
		loop := NewMaintainer(MaintainerOptions{Settings: settings, Clock: faketime.New(now), Submit: sink.submit, ResumeQuiet: &resume})
		loop.tick(t.Context(), now)
		if now.Before(quiet.Next) {
			if len(sink.submitted) != 0 {
				t.Fatal("restart repeated logout over the hotspot")
			}
		} else if len(sink.submitted) != 1 || sink.submitted[0].Kind != KindQuietCampus {
			t.Fatalf("restart lost the scheduled campus return: %+v", sink.submitted)
		}
	}
}

func TestQuietResumeRejectsChangedConfigClockAndOwnership(t *testing.T) {
	settings := quietWorld(t)
	quiet := policy.EvaluateQuiet(settings.cfg.Quiet, maintainEpoch)
	base := QuietResume{Revision: settings.cfg.Revision, Occurrence: quiet.Occurrence,
		AccountID: "c1", HotspotID: "h1", StartedAt: maintainEpoch}
	for name, change := range map[string]func(*QuietResume, *domain.Config, *time.Time){
		"revision":          func(r *QuietResume, _ *domain.Config, _ *time.Time) { r.Revision++ },
		"account":           func(r *QuietResume, _ *domain.Config, _ *time.Time) { r.AccountID = "other" },
		"hotspot":           func(r *QuietResume, _ *domain.Config, _ *time.Time) { r.HotspotID = "missing" },
		"disabled":          func(_ *QuietResume, c *domain.Config, _ *time.Time) { c.Enabled = false },
		"failover disabled": func(_ *QuietResume, c *domain.Config, _ *time.Time) { c.Failover.Enabled = false },
		"wrong occurrence":  func(r *QuietResume, _ *domain.Config, _ *time.Time) { r.Occurrence = "unknown" },
		"clock rewind":      func(_ *QuietResume, _ *domain.Config, now *time.Time) { *now = maintainEpoch.Add(-time.Minute) },
		"next window":       func(_ *QuietResume, _ *domain.Config, now *time.Time) { *now = maintainEpoch.Add(24 * time.Hour) },
		"expired":           func(_ *QuietResume, _ *domain.Config, now *time.Time) { *now = maintainEpoch.Add(30 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			resume, cfg, now := base, settings.cfg, maintainEpoch.Add(time.Hour)
			change(&resume, &cfg, &now)
			if resume.Matches(cfg, now) {
				t.Fatal("stale ownership permitted a network switch")
			}
		})
	}
}

func TestManualQuietResumeDoesNotSkipManagedWiredLogout(t *testing.T) {
	settings := quietWorld(t)
	quiet := policy.EvaluateQuiet(settings.cfg.Quiet, maintainEpoch)
	resume := QuietResume{Revision: settings.cfg.Revision, Occurrence: quiet.Occurrence,
		AccountID: "c1", HotspotID: "h1", StartedAt: maintainEpoch, SweepPending: true}
	sink := &recorder{}
	loop := NewMaintainer(MaintainerOptions{Settings: settings, Clock: faketime.New(maintainEpoch), Submit: sink.submit, ResumeQuiet: &resume})
	loop.tick(t.Context(), maintainEpoch)
	if len(sink.submitted) != 1 || sink.submitted[0].Kind != KindForcedLogout {
		t.Fatalf("manual hotspot was incorrectly treated as a completed logout sweep: %+v", sink.submitted)
	}
}
