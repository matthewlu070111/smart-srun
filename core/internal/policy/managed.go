package policy

import (
	"slices"
	"sort"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Ceilings from spec 02. They are constants rather than configuration because
// they bound resource use on a router with 128MB of RAM, and a user who raises
// them has no way to know what they are trading away.
const (
	// MaxManagedWired is how many wired accounts may be maintained at once.
	MaxManagedWired = 8
	// MaxConcurrentLines is how many lines may have I/O in flight at once.
	MaxConcurrentLines = 4
)

// Target is one account the scheduler has something to do with, together with
// the line it authenticates through.
type Target struct {
	AccountID string
	// Line is the logical interface for a wired account. Empty for wireless:
	// the wireless account's line is whatever the STA is associated to, which
	// is not known from configuration alone.
	Line string
	// Wireless says which of the two the account is, so a caller does not have
	// to infer it from Line being empty.
	Wireless bool
	// Managed says the account is in the multi-WAN maintained set. The active
	// campus account may appear without being managed.
	Managed bool
	// Active says this is the currently selected campus account.
	Active bool
}

// Managed lists the accounts the automatic loop maintains.
//
// Both gates matter and they are separate on purpose: the global multi-WAN
// switch and the account's own opt-in. An account that opted in while the
// global switch is off is not managed, and turning the global switch on must
// not silently enrol accounts that never opted in.
func Managed(cfg *domain.Config) []Target {
	var out []Target
	for _, account := range cfg.ManagedWiredAccounts() {
		out = append(out, Target{
			AccountID: account.ID,
			Line:      account.WiredIface,
			Managed:   true,
			Active:    account.ID == cfg.Selection.ActiveCampusID,
		})
	}
	return out
}

// ForcedLogoutTargets is the set a quiet-hours sweep must log out: the managed
// wired accounts together with the active campus account, deduplicated.
//
// Spec 04 fixes both halves. An empty managed set still has to handle the
// active account -- the baseline bug this replaces was skipping the forced
// logout entirely whenever multi-WAN was off, which is the configuration most
// users have. And the active account is frequently also managed, so the union
// has to deduplicate or the sweep logs the same session out twice and reports
// the second attempt as a failure.
//
// The order is stable: managed accounts in configuration order, then the active
// account if it was not already among them. A sweep that reordered itself
// between ticks would make the "which of these did we already do" record
// useless.
func ForcedLogoutTargets(cfg *domain.Config) []Target {
	targets := Managed(cfg)

	active := cfg.Selection.ActiveCampusID
	if active == "" {
		return targets
	}
	if slices.ContainsFunc(targets, func(t Target) bool { return t.AccountID == active }) {
		return targets
	}
	account, ok := cfg.CampusAccountByID(active)
	if !ok {
		// The pointer names an account that is gone. Config normalization
		// repairs that; reporting a target for it here would make the sweep
		// fail forever on an account nobody can see.
		return targets
	}
	line := account.WiredIface
	if !account.IsWired() {
		line = ""
	}
	return append(targets, Target{
		AccountID: account.ID,
		Line:      line,
		Wireless:  !account.IsWired(),
		Active:    true,
	})
}

// Sweep records which accounts one quiet-hours occurrence has already logged
// out.
//
// Spec 04: a success recorded in the current window stays recorded, and only
// the failures are retried. Without that the scheduler would log every managed
// account out on every tick for six hours, which is both pointless traffic and
// a log nobody can read.
//
// The occurrence is the key, so a new window -- or the same window tomorrow --
// starts empty, while a clock correction inside the window does not.
type Sweep struct {
	occurrence string
	done       map[string]bool
}

// Pending returns the targets this occurrence has not yet finished, resetting
// the record when the occurrence changes.
//
// Recomputing from the live target list each time is what makes spec 04's
// "turning forced logout on mid-window applies to the accounts not yet handled"
// fall out rather than needing its own branch.
func (s *Sweep) Pending(occurrence string, targets []Target) []Target {
	if s.occurrence != occurrence {
		s.occurrence = occurrence
		s.done = map[string]bool{}
	}
	var out []Target
	for _, target := range targets {
		if !s.done[target.AccountID] {
			out = append(out, target)
		}
	}
	return out
}

// Succeeded records one account as done for this occurrence. A failure is
// deliberately not recorded: it comes back on the next tick.
func (s *Sweep) Succeeded(occurrence, accountID string) {
	if s.occurrence != occurrence {
		s.occurrence = occurrence
		s.done = map[string]bool{}
	}
	s.done[accountID] = true
}

// LineConflict is two enabled accounts that resolved to the same line.
type LineConflict struct {
	Line       string
	AccountIDs []string
}

// Conflicts finds accounts that would authenticate through the same line.
//
// Spec 02 refuses this in the first version rather than guessing. Two accounts
// on one line cannot both be online, so whichever authenticates second knocks
// the first off, and the pair then take turns doing it to each other forever.
// The check runs against resolved lines, not logical names: "wan" and a device
// name that resolves to the same L3 device are the same line even though the
// strings differ.
//
// resolve maps a target to the line it actually uses. Targets whose line cannot
// be resolved yet are skipped: not knowing is not a conflict.
func Conflicts(targets []Target, resolve func(Target) string) []LineConflict {
	byLine := map[string][]string{}
	for _, target := range targets {
		line := resolve(target)
		if line == "" {
			continue
		}
		byLine[line] = append(byLine[line], target.AccountID)
	}

	var out []LineConflict
	for line, accounts := range byLine {
		if len(accounts) < 2 {
			continue
		}
		out = append(out, LineConflict{Line: line, AccountIDs: accounts})
	}
	// Map iteration order is randomised, and a diagnostic that reorders itself
	// between runs cannot be compared against a previous one.
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}
