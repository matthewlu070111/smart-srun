package policy

import (
	"slices"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func wired(id, iface string, managed bool) domain.CampusAccount {
	return domain.CampusAccount{
		ID: id, AccessMode: domain.AccessModeWired,
		WiredIface: iface, AuthEnabled: managed,
	}
}

func wireless(id, ssid string) domain.CampusAccount {
	return domain.CampusAccount{
		ID: id, AccessMode: domain.AccessModeWiFi, SSID: ssid,
	}
}

func idsOf(targets []Target) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.AccountID)
	}
	return out
}

// T21 -- the forced-logout set is the union, with the account that is in both
// halves appearing once.
//
// The active campus account is very often also a managed one. Logging it out
// twice makes the second attempt fail -- it is already gone -- and the sweep
// then reports a failure and retries it for the rest of the window.
func TestTheForcedLogoutSetDeduplicatesTheActiveAccount(t *testing.T) {
	config := &domain.Config{
		MultiWANEnabled: true,
		CampusAccounts: []domain.CampusAccount{
			wired("wan-a", "wan", true),
			wired("wan-b", "wan2", true),
			wired("wan-c", "wan3", false),
		},
		Selection: domain.Selection{ActiveCampusID: "wan-a"},
	}

	targets := ForcedLogoutTargets(config)
	if got := idsOf(targets); !slices.Equal(got, []string{"wan-a", "wan-b"}) {
		t.Fatalf("targets = %v, want the two managed accounts once each", got)
	}
	for _, target := range targets {
		if target.AccountID == "wan-a" && !target.Active {
			t.Error("the active account was not marked active")
		}
		if !target.Managed {
			t.Errorf("%s should be marked managed", target.AccountID)
		}
	}
}

// T21 -- an empty managed set still has to log the active account out.
//
// This is the configuration most users have: multi-WAN off, one account. A
// sweep that skipped it whenever the managed set was empty would mean quiet
// hours did nothing at all for them.
func TestAnEmptyManagedSetStillHandlesTheActiveAccount(t *testing.T) {
	config := &domain.Config{
		MultiWANEnabled: false,
		CampusAccounts: []domain.CampusAccount{
			wired("only", "wan", true), // opted in, but the global switch is off
		},
		Selection: domain.Selection{ActiveCampusID: "only"},
	}

	if managed := Managed(config); len(managed) != 0 {
		t.Fatalf("managed = %v, want none while the global switch is off",
			idsOf(managed))
	}
	targets := ForcedLogoutTargets(config)
	if got := idsOf(targets); !slices.Equal(got, []string{"only"}) {
		t.Fatalf("targets = %v, want the active account", got)
	}
	if targets[0].Managed {
		t.Error("an account was reported as managed while multi-WAN is off")
	}
}

// T21 -- a wireless active account joins the set, and is marked as wireless
// rather than being given somebody else's line.
func TestAWirelessActiveAccountJoinsTheWiredOnes(t *testing.T) {
	config := &domain.Config{
		MultiWANEnabled: true,
		CampusAccounts: []domain.CampusAccount{
			wired("wan-a", "wan", true),
			wireless("wifi-a", "campus"),
		},
		Selection: domain.Selection{ActiveCampusID: "wifi-a"},
	}

	targets := ForcedLogoutTargets(config)
	if got := idsOf(targets); !slices.Equal(got, []string{"wan-a", "wifi-a"}) {
		t.Fatalf("targets = %v", got)
	}
	last := targets[len(targets)-1]
	if !last.Wireless || last.Line != "" {
		t.Errorf("the wireless account got line %q, wireless=%v; its line is "+
			"whatever the STA associated to, which configuration does not know",
			last.Line, last.Wireless)
	}
}

// A selection pointing at an account that no longer exists is not a target.
// Config normalization repairs the pointer; producing a target for a missing
// account would make the sweep fail forever on something nobody can see.
func TestADanglingActiveSelectionIsNotATarget(t *testing.T) {
	config := &domain.Config{
		Selection: domain.Selection{ActiveCampusID: "deleted"},
	}
	if targets := ForcedLogoutTargets(config); len(targets) != 0 {
		t.Errorf("targets = %v, want none", idsOf(targets))
	}
}

// T21/T20 -- a success inside one occurrence stays recorded, and only failures
// come back.
func TestTheSweepRetriesOnlyWhatFailed(t *testing.T) {
	config := &domain.Config{
		MultiWANEnabled: true,
		CampusAccounts: []domain.CampusAccount{
			wired("wan-a", "wan", true),
			wired("wan-b", "wan2", true),
		},
	}
	targets := ForcedLogoutTargets(config)

	var sweep Sweep
	const tonight = "20260305T0100+0800"

	if got := idsOf(sweep.Pending(tonight, targets)); len(got) != 2 {
		t.Fatalf("first pass = %v, want both", got)
	}
	sweep.Succeeded(tonight, "wan-a")

	// wan-b failed, so it is still pending; wan-a is not.
	if got := idsOf(sweep.Pending(tonight, targets)); !slices.Equal(got, []string{"wan-b"}) {
		t.Fatalf("second pass = %v, want only the one that failed", got)
	}
	sweep.Succeeded(tonight, "wan-b")
	if got := sweep.Pending(tonight, targets); len(got) != 0 {
		t.Fatalf("third pass = %v, want nothing left", idsOf(got))
	}

	// The next night is a new occurrence and starts over.
	const tomorrow = "20260306T0100+0800"
	if got := idsOf(sweep.Pending(tomorrow, targets)); len(got) != 2 {
		t.Errorf("the next occurrence = %v, want both again", got)
	}
}

// T21 -- switching forced logout on while the window is already open applies to
// the accounts that have not been handled yet.
//
// It falls out of recomputing the target list every pass instead of capturing
// it when the window opened, which is the point: the alternative needs its own
// branch and the branch is what gets forgotten.
func TestTurningForcedLogoutOnMidWindowCatchesTheRest(t *testing.T) {
	config := &domain.Config{
		MultiWANEnabled: true,
		CampusAccounts: []domain.CampusAccount{
			wired("wan-a", "wan", true),
		},
	}
	var sweep Sweep
	const tonight = "20260305T0100+0800"

	sweep.Succeeded(tonight, "wan-a")
	if got := sweep.Pending(tonight, ForcedLogoutTargets(config)); len(got) != 0 {
		t.Fatalf("pending = %v, want nothing", idsOf(got))
	}

	// The user adds a second managed account while the window is open.
	config.CampusAccounts = append(config.CampusAccounts, wired("wan-b", "wan2", true))
	got := idsOf(sweep.Pending(tonight, ForcedLogoutTargets(config)))
	if !slices.Equal(got, []string{"wan-b"}) {
		t.Errorf("pending = %v, want the account that has not been handled", got)
	}
}

// Two accounts on one resolved line is refused rather than guessed at: they
// cannot both be online, so each login knocks the other off and the pair take
// turns doing it forever.
func TestTwoAccountsOnOneLineAreReportedAsAConflict(t *testing.T) {
	targets := []Target{
		{AccountID: "a", Line: "wan"},
		{AccountID: "b", Line: "wan.v2"},
		{AccountID: "c", Line: "wan2"},
		{AccountID: "d", Line: ""},
	}
	// Different logical names, same L3 device underneath. Comparing the
	// configured names would miss it.
	resolve := func(target Target) string {
		switch target.Line {
		case "wan", "wan.v2":
			return "eth0"
		case "wan2":
			return "eth1"
		default:
			return ""
		}
	}

	conflicts := Conflicts(targets, resolve)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly one", conflicts)
	}
	if conflicts[0].Line != "eth0" {
		t.Errorf("line = %q, want the resolved device", conflicts[0].Line)
	}
	if !slices.Equal(conflicts[0].AccountIDs, []string{"a", "b"}) {
		t.Errorf("accounts = %v", conflicts[0].AccountIDs)
	}

	// An unresolved line is not a conflict: not knowing is not the same as
	// clashing, and refusing on it would block an account whose interface is
	// merely still coming up.
	unknown := []Target{{AccountID: "a"}, {AccountID: "b"}}
	if got := Conflicts(unknown, func(Target) string { return "" }); len(got) != 0 {
		t.Errorf("unresolved lines produced %+v", got)
	}
}

// The ceilings are the ones spec 02 fixes. Raising one silently would change
// how much memory the daemon can use on a router that has none to spare.
func TestTheConcurrencyCeilingsAreTheDocumentedOnes(t *testing.T) {
	if MaxManagedWired != 8 {
		t.Errorf("MaxManagedWired = %d, want 8", MaxManagedWired)
	}
	if MaxConcurrentLines != 4 {
		t.Errorf("MaxConcurrentLines = %d, want 4", MaxConcurrentLines)
	}
}
