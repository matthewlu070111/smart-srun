package logstore

import (
	"fmt"
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The event catalogue.
//
// Spec 02 puts the event codes and their fields here, and leaves the Chinese
// wording to the page that displays them. That split only works if the two
// halves cannot drift: every name below has a translation in the LuCI
// controller's event_zh table, and a test checks it. Adding an event means
// adding both.
//
// Names are reused from the baseline wherever the baseline had one for the same
// thing, so a user who has read their log before recognises it.
const (
	// EventDaemonStart and EventDaemonStop bracket a run. Between them, the
	// absence of lines means nothing happened -- without them it could equally
	// mean the service was not running.
	EventDaemonStart = "daemon_start"
	EventDaemonStop  = "daemon_stop"
	// EventConfigLoaded records the configuration a run started with.
	EventConfigLoaded = "config_loaded"
	// EventConfigApplied records a save, by revision. It is what makes "the
	// setting did not take effect" answerable after the fact.
	EventConfigApplied = "config_applied"

	// EventActionQueued through EventActionResult are one action's timeline.
	EventActionQueued = "config_action_queued"
	EventActionStart  = "action_started"
	EventActionPhase  = "action_phase"
	EventActionResult = "action_result"

	// EventMaintainQueued is the automatic loop deciding to authenticate.
	EventMaintainQueued = "maintain_queued"
	// EventRetryScheduled is a failure's cooldown, EventRetryCycleEnd the end
	// of a round's budget.
	EventRetryScheduled = "retry_scheduled"
	EventRetryCycleEnd  = "retry_cycle_end"
	// EventPauseChanged is automatic authentication being suspended or resumed
	// -- switched off, quiet hours, or an uplink that is deliberately a hotspot.
	EventPauseChanged = "pause_changed"
	// EventQuietLogout is the quiet-hours sweep queueing a logout.
	EventQuietLogout = "quiet_logout_queued"
	// EventLineConflict is two accounts wanting one line.
	EventLineConflict = "line_conflict"

	// EventPresetsRefresh is a catalogue refresh, which touches the network but
	// authenticates nothing.
	EventPresetsRefresh = "presets_refresh"
	// EventDetectProbe is one read-only look at a gateway's own pages. It
	// records the line and how many addresses were visited, never a credential:
	// discovery does not have one.
	EventDetectProbe = "detect_probe"

	// EventInternalError is a fault with nobody to return it to: a snapshot
	// that could not be written, a connection that failed mid-answer.
	EventInternalError = "internal_error"
	// EventLogCleared marks the boundary a user created by clearing the log, so
	// an empty log and a cleared one are not the same page.
	EventLogCleared = "log_cleared"
)

// Event describes one name in the catalogue.
type Event struct {
	Name  string
	Level domain.LogLevel
	// Network marks an event about the line rather than about the service, so
	// the network channel can be assembled here instead of in the page.
	Network bool
	// Note is the constraint worth knowing at the call site.
	Note string
}

var catalogue = []Event{
	{EventDaemonStart, domain.LogInfo, false, ""},
	{EventDaemonStop, domain.LogInfo, false, ""},
	{EventConfigLoaded, domain.LogInfo, false, "不记录账号凭据，只记录版本与数量"},
	{EventConfigApplied, domain.LogInfo, false, ""},

	{EventActionQueued, domain.LogInfo, true, ""},
	{EventActionStart, domain.LogInfo, true, ""},
	{EventActionPhase, domain.LogDebug, true, "阶段进度，默认等级下不写"},
	{EventActionResult, domain.LogInfo, true, "失败时由调用方改用 WARN"},

	{EventMaintainQueued, domain.LogInfo, true, ""},
	{EventRetryScheduled, domain.LogWarn, true, ""},
	{EventRetryCycleEnd, domain.LogWarn, true, ""},
	{EventPauseChanged, domain.LogInfo, false, ""},
	{EventQuietLogout, domain.LogInfo, true, ""},
	{EventLineConflict, domain.LogWarn, true, ""},

	{EventPresetsRefresh, domain.LogInfo, true, ""},
	{EventDetectProbe, domain.LogInfo, true, "只读探测，永远没有凭据字段"},

	{EventInternalError, domain.LogError, false, ""},
	{EventLogCleared, domain.LogInfo, false, ""},
}

var byName = func() map[string]Event {
	out := make(map[string]Event, len(catalogue))
	for _, event := range catalogue {
		out[event.Name] = event
	}
	return out
}()

// Catalogue returns every declared event, sorted by name.
func Catalogue() []Event {
	out := slices.Clone(catalogue)
	slices.SortFunc(out, func(a, b Event) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Lookup reports the declared event with this name.
func Lookup(name string) (Event, bool) {
	event, ok := byName[name]
	return event, ok
}

// NetworkEvents is the set the network channel keeps.
func NetworkEvents() map[string]bool {
	out := make(map[string]bool, len(catalogue))
	for _, event := range catalogue {
		if event.Network {
			out[event.Name] = true
		}
	}
	return out
}

// validateCatalogue reports the first malformed or duplicated entry. It runs at
// init for the same reason the RPC catalogue's does: a log that cannot describe
// its own events is not something to discover at the first failure.
func validateCatalogue(events []Event) error {
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event.Name == "" || strings.ContainsAny(event.Name, " \t\r\n=|") {
			return fmt.Errorf("logstore: event name %q is not usable in a line", event.Name)
		}
		if !event.Level.Valid() || event.Level == domain.LogAll {
			return fmt.Errorf("logstore: event %q has no usable level", event.Name)
		}
		if _, duplicate := seen[event.Name]; duplicate {
			return fmt.Errorf("logstore: duplicate event in catalogue: %s", event.Name)
		}
		seen[event.Name] = struct{}{}
	}
	return nil
}

func init() {
	if err := validateCatalogue(catalogue); err != nil {
		panic(err.Error())
	}
}
