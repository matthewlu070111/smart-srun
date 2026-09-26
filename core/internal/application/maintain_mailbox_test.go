package application

import (
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func TestTerminalBurstCannotStrandAutomaticMaintenance(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	m, sink := maintainerFor(t, maintainWorld(), clock)
	m.tick(t.Context(), clock.Now())
	for i := range 1000 {
		m.Observe(Action{ID: formatID(uint64(i + 2)), Request: Request{Kind: KindLogin, AccountID: "c1"}, State: StateSucceeded})
	}
	m.Observe(finished(sink, 0, StateSucceeded))
	for _, result := range m.takeResults() {
		m.apply(result, clock.Now())
	}
	clock.Advance(time.Minute)
	m.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 2 {
		t.Fatal("terminal burst left completed maintenance permanently in flight")
	}
}

func TestMaintenanceMailboxKeepsNewResultOverLateCancellation(t *testing.T) {
	m, _ := maintainerFor(t, maintainWorld(), faketime.New(maintainEpoch))
	newer := Action{ID: "a2", ordinal: 2, Request: Request{Kind: KindMaintain, AccountID: "c1"}, State: StateSucceeded}
	m.Observe(newer)
	m.Observe(Action{ID: "a1", ordinal: 1, Request: newer.Request, State: StateCancelled})
	got := m.takeResults()
	if len(got) != 1 || got[0].ID != newer.ID {
		t.Fatal("old worker replaced newer completion")
	}
}

func TestMaintenanceMailboxIsBoundedAcrossConfigChurnAndManualClicks(t *testing.T) {
	settings := maintainWorld()
	m, _ := maintainerFor(t, settings, faketime.New(maintainEpoch))
	for i := range 1000 {
		id := formatID(uint64(i + 1))
		settings.cfg.CampusAccounts[0].ID = id
		m.Observe(Action{ID: id, ordinal: uint64(i + 1), Request: Request{Kind: KindMaintain, AccountID: id}, State: StateSucceeded})
		m.Observe(Action{ID: "manual-" + id, ordinal: uint64(i + 1001), Request: Request{Kind: KindSwitchHotspot, HotspotID: "h1"}, State: StateSucceeded})
		if len(m.results) > 2 {
			t.Fatal("mailbox retained removed accounts or per-click history")
		}
	}
	if len(m.takeResults()) != 2 || len(m.takeResults()) != 0 {
		t.Fatal("mailbox did not drain its bounded state")
	}
}

func TestMaintenanceMailboxRetainsManualChoiceAfterScheduledSuccess(t *testing.T) {
	m, _ := maintainerFor(t, maintainWorld(), faketime.New(maintainEpoch))
	m.quietSwitch.inFlight = "a1"
	m.Observe(Action{ID: "a1", ordinal: 1, Request: Request{Kind: KindQuietHotspot, AccountID: "c1", HotspotID: "h1"}, State: StateSucceeded})
	m.Observe(Action{ID: "a2", ordinal: 2, Request: Request{Kind: KindSwitchHotspot, HotspotID: "h1"}, State: StateSucceeded})
	for _, action := range m.takeResults() {
		m.apply(action, maintainEpoch)
	}
	if m.quietSwitch.owned || !m.quietSwitch.done || m.quietSwitch.inFlight != "" {
		t.Fatal("unordered delivery reclaimed a user's hotspot choice")
	}
}
