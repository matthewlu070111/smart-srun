//go:build unix

package daemon

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// awaitPause polls the status until the pause set matches, and says what it saw
// if it never does.
func awaitPause(t *testing.T, service *running, want func([]string) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(patience)
	var snapshot Snapshot
	for time.Now().Before(deadline) {
		snapshot = service.status()
		if want(snapshot.Pause) {
			return snapshot
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the pause set stayed %v", snapshot.Pause)
	return snapshot
}

// "Switched off" and "paused" are different facts, and the page has to be able
// to tell them apart: quiet hours suspend a service the user has switched on,
// and showing that as "off" invites them to turn on a switch that is already
// on.
func TestTheStatusSaysWhyAutomaticAuthenticationIsPaused(t *testing.T) {
	service := start(t, nil)

	// Automatic authentication ships off, so the loop suspends itself at once.
	paused := awaitPause(t, service, func(reasons []string) bool {
		return len(reasons) > 0
	})
	if !slices.Contains(paused.Pause, "UserDisabled") {
		t.Errorf("pause = %v, want the user's own switch named", paused.Pause)
	}
	if paused.Enabled {
		t.Error("enabled = true, but the configuration ships with it off")
	}

	// Switching it on -- with quiet hours off, so the answer does not depend on
	// what time the test runs -- clears the reason rather than adding another.
	service.call("config.apply", json.RawMessage(
		`{"expected_revision":0,"settings":{"enabled":true,"quiet":{"enabled":false}}}`))
	resumed := awaitPause(t, service, func(reasons []string) bool {
		return len(reasons) == 0
	})
	if !resumed.Enabled {
		t.Error("enabled = false after switching it on")
	}
}
