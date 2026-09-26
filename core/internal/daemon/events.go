package daemon

import (
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
)

// The service's own reporting.
//
// Everything a user can read about what the daemon did passes through here, so
// there is one place that knows which events exist, what level each is, and
// which fields they carry. Spreading Emit calls through the packages that do
// the work would put the log's contract wherever somebody happened to need a
// line.

// log emits a catalogued event at its declared level.
func (d *Daemon) log(event, message string, fields ...logstore.Field) {
	d.logAt(levelOf(event), event, message, fields...)
}

// logAt emits at a level the caller decides.
//
// A result is INFO when it succeeded and WARN when it did not, and the
// catalogue cannot know which; nothing else should need this.
func (d *Daemon) logAt(level domain.LogLevel, event, message string,
	fields ...logstore.Field) {
	if d.events == nil {
		return
	}
	d.events.Emit(d.clock.Now(), level, event, message, fields...)
}

// levelOf is the catalogue's answer. An unknown name can only come from a
// literal somebody typed instead of a constant; it is logged at INFO rather
// than dropped, so the mistake is visible in the place it happened.
func levelOf(event string) domain.LogLevel {
	if declared, found := logstore.Lookup(event); found {
		return declared.Level
	}
	return domain.LogInfo
}

// actionFields are the identifiers every line about an action carries, so a
// reader can follow one action through the log.
func actionFields(action application.Action) []logstore.Field {
	fields := []logstore.Field{
		logstore.F("action_id", action.ID),
		logstore.F("kind", string(action.Request.Kind)),
	}
	if action.Request.AccountID != "" {
		fields = append(fields, logstore.F("account_id", action.Request.AccountID))
	}
	if action.Request.HotspotID != "" {
		fields = append(fields, logstore.F("hotspot_id", action.Request.HotspotID))
	}
	if action.Request.Interface != "" {
		fields = append(fields, logstore.F("iface", action.Request.Interface))
	}
	return fields
}

// logAction records one action transition, once.
//
// The coordinator republishes an action that is still running when a
// cancellation arrives, which is a real change to it but not a new phase; the
// signature check is what keeps that from writing a second "started" line.
func (d *Daemon) logAction(action application.Action) {
	signature := string(action.State) + "/" + string(action.Phase)
	previous := d.published[action.ID]
	if previous == signature {
		return
	}
	d.published[action.ID] = signature

	running := string(application.StateRunning) + "/"
	switch {
	case action.State == application.StateQueued:
		d.log(logstore.EventActionQueued, "", actionFields(action)...)
	// The first running publication already carries a phase: dispatch sets
	// waiting_link before it announces the action. Keying "started" off an
	// empty phase would mean an action that ran was only ever logged as
	// queued and then finished.
	case action.State == application.StateRunning && !strings.HasPrefix(previous, running):
		d.log(logstore.EventActionStart, "",
			append(actionFields(action), logstore.F("phase", string(action.Phase)))...)
	case action.State == application.StateRunning:
		d.log(logstore.EventActionPhase, "",
			append(actionFields(action), logstore.F("phase", string(action.Phase)))...)
	case action.State.Terminal():
		// The record survives the action: this is the line a user reads after a
		// login failed, so it carries the message and the code rather than
		// leaving them in a status field that the next action clears.
		fields := append(actionFields(action), logstore.F("state", string(action.State)))
		if action.Code != "" {
			fields = append(fields, logstore.F("code", string(action.Code)))
		}
		level := domain.LogInfo
		if action.State != application.StateSucceeded {
			level = domain.LogWarn
		}
		d.logAt(level, logstore.EventActionResult, action.Message, fields...)
		// Terminal states are irreversible, so nothing more will be published
		// about this action and the signature can go. Without this the map
		// would grow for as long as the service runs.
		delete(d.published, action.ID)
	}
}

// logMaintenance records what the automatic loop decided.
//
// The loop reports events rather than formatting lines, so the mapping from its
// decisions to the catalogue lives here, next to the other log rules.
func (d *Daemon) logMaintenance(event application.MaintenanceEvent) {
	fields := []logstore.Field{}
	if event.AccountID != "" {
		fields = append(fields, logstore.F("account_id", event.AccountID))
	}

	switch event.Kind {
	case application.EventPauseChanged:
		// The primary reason, not the whole set: the message already says what
		// happened, and a bitmask in a log line is for the program, not the
		// person reading it.
		reason, paused := event.Pause.Primary()
		state := "resumed"
		if paused {
			state = string(reason)
		}
		d.log(logstore.EventPauseChanged, event.Message,
			append(fields, logstore.F("pause", state))...)
	case application.EventMaintainQueued:
		d.log(logstore.EventMaintainQueued, event.Message, fields...)
	case application.EventSweepQueued:
		d.log(logstore.EventQuietLogout, event.Message, fields...)
	case application.EventBackoff:
		d.log(logstore.EventRetryScheduled, event.Message, fields...)
	case application.EventRoundCooldown:
		d.log(logstore.EventRetryCycleEnd, event.Message, fields...)
	case application.EventLineConflict:
		d.log(logstore.EventLineConflict, event.Message, fields...)
	}
}
