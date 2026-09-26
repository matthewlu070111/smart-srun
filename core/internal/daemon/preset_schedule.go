package daemon

import (
	"context"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// A calendar-day claim is retained on tmpfs across daemon restarts, without
// writing flash every day. Rebooting the device clears it along with the cache.
// Claim before submission: failure or a crash must not produce a request loop.
type presetSchedule struct {
	SchemaVersion int    `json:"schema_version"`
	Day           string `json:"day"`
}

func (s *presetSchedule) step(now time.Time, cfg domain.Config, iface, path string,
	submit func(application.Request) error) error {
	local := now.In(domain.Beijing)
	day := local.Format("2006-01-02")
	if !cfg.PresetUpdates.Enabled || iface == "" || local.Year() < 2024 ||
		local.Hour()*60+local.Minute() < cfg.PresetUpdates.Time.Minutes() || day <= s.Day {
		return nil
	}
	// Keep the in-memory claim even if storage is unavailable: report once,
	// fail closed, and leave manual refresh available.
	s.SchemaVersion, s.Day = 1, day
	if err := writeRuntimeRecord(path, s); err != nil {
		return err
	}
	return submit(application.Request{Kind: application.KindPresetsRefresh,
		Interface: iface, IdempotencyKey: "presets-daily-" + day,
		CheckRevision: true, ConfigRevision: cfg.Revision})
}

func (d *Daemon) schedulePresets(ctx context.Context) {
	var schedule presetSchedule
	exists, err := readRuntimeRecord(d.paths.PresetSchedule(), &schedule)
	if err == nil && exists {
		_, parseErr := time.Parse("2006-01-02", schedule.Day)
		if schedule.SchemaVersion != 1 || parseErr != nil {
			err = runtimeRecordError(parseErr)
		}
	}
	if err != nil {
		d.onError(err)
		return // unreadable deduplication state cannot grant another auto request
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		cfg := d.config.Snapshot()
		iface := cfg.STAIface
		if a, ok := cfg.CampusAccountByID(cfg.Selection.ActiveCampusID); ok && a.IsWired() {
			iface = a.WiredIface
		}
		// An observed hotspot/STA takes precedence over the configured campus
		// account, which may still describe the previous uplink after failover.
		if view := d.wirelessState.read(cfg.Revision); view != nil && view.State == "associated" && view.Address != "" {
			iface = view.Interface
		}
		err := schedule.step(d.clock.Now(), cfg, iface, d.paths.PresetSchedule(), func(request application.Request) error {
			_, err := d.actions.Submit(ctx, request)
			return err
		})
		if err != nil && ctx.Err() == nil {
			d.onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
