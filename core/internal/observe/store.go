// Package observe is the read-only projection of what the daemon knows.
//
// Two rules shape it. A status read never touches the network -- spec 03
// requires status.get to answer from a cache, because the LuCI page polls it
// every few seconds and a poll that probed would turn an idle browser tab into
// continuous authentication traffic. And a result that arrives late never
// overwrites a newer one, which is the failure spec 04 calls out by name: a
// worker cancelled ten seconds ago finishing anyway and reporting the state of
// a line that has since been re-observed.
//
// The store is the one place a reader and the coordinator meet, so it is also
// the one place that needs a lock. Everything else in the scheduling path is
// single-writer.
package observe

import (
	"sort"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Observation is one worker's report about one account.
//
// The three state fields are kept apart because spec 04 requires it: a
// reachable gateway is not an authenticated session, and an authenticated
// session is not a working internet connection. Collapsing them into one
// "online" is how a user with a captive portal in front of them is told their
// password is wrong.
type Observation struct {
	AccountID string

	// Revision is the configuration revision the worker ran under, Generation
	// the binding generation it used, and Sequence the coordinator's dispatch
	// number. Together they answer "is this still about the current world?",
	// and they are three fields rather than one because the three ways of
	// becoming stale are worth telling apart when something goes wrong.
	Revision   uint64
	Generation uint64
	Sequence   uint64

	Link         domain.LinkState
	Auth         domain.AuthState
	Connectivity domain.Connectivity
	// Identity is whoever the gateway said is on the line. It may be another
	// account: that is reported, never acted on.
	Identity string

	// Line is how the account reached the network at this moment: the interface
	// the user selected, the device that actually carried layer 3, and the
	// address the socket was bound to.
	//
	// Observed, not configured. The interface an account is set to use and the
	// device that carried its last attempt are different facts, and a status
	// page that showed the first as the second would claim an observation
	// nobody made.
	Line LineView

	At time.Time
}

// LineView is the observed shape of one uplink.
type LineView struct {
	Iface   string `json:"iface,omitempty"`
	Device  string `json:"device,omitempty"`
	Address string `json:"address,omitempty"`
}

// Drop says why an observation was not recorded.
type Drop string

const (
	DropNone Drop = ""
	// DropNoAccount -- the observation names no account.
	DropNoAccount Drop = "no_account"
	// DropObsoleteRevision -- it ran under a configuration that has since been
	// replaced, so it is answering a question nobody is asking any more.
	DropObsoleteRevision Drop = "obsolete_revision"
	// DropObsoleteBinding -- the line was re-observed after this worker read
	// it. Its address or device may no longer exist.
	DropObsoleteBinding Drop = "obsolete_binding"
	// DropLate -- a newer dispatch has already reported.
	DropLate Drop = "late"
)

// Note is the last thing worth telling a user about an account.
//
// It survives routine observation and is cleared only when a new action starts.
// The baseline learned this the hard way: a daemon tick overwriting
// last_action_message meant the explanation of a failed login disappeared a few
// seconds after it appeared, while the user was still reading it.
type Note struct {
	ActionID string           `json:"action_id"`
	Kind     string           `json:"kind"`
	State    string           `json:"state"`
	Message  string           `json:"message,omitempty"`
	Code     domain.ErrorCode `json:"code,omitempty"`
	At       time.Time        `json:"at"`
}

// AccountView is one account as a reader sees it.
//
// The tags are part of the contract, not decoration: this value is half of
// status.get, and the page reads it by these names. Without them Go would
// publish its own field names into a document whose every other member is
// snake_case, and the page would quietly match nothing -- which is exactly what
// happened before they were added.
type AccountView struct {
	AccountID string `json:"account_id"`

	Link         domain.LinkState    `json:"link"`
	Auth         domain.AuthState    `json:"auth"`
	Connectivity domain.Connectivity `json:"connectivity"`
	Identity     string              `json:"identity,omitempty"`
	Line         LineView            `json:"line"`
	ObservedAt   time.Time           `json:"observed_at"`

	Revision   uint64 `json:"config_revision"`
	Generation uint64 `json:"generation"`
	Sequence   uint64 `json:"sequence"`

	// RunningAction is the action in flight for this account, if any.
	RunningAction string `json:"running_action,omitempty"`
	// Note is the last finished action's result. Nil when nothing has finished
	// since the last one started.
	Note *Note `json:"note,omitempty"`
}

// Snapshot is one combined read, which is what spec 03 requires of status.get:
// a single consistent picture rather than a field at a time.
type Snapshot struct {
	Revision uint64
	Accounts []AccountView
}

type entry struct {
	view AccountView
	note *Note
	// seen records that at least one observation has landed, so a first
	// observation is never mistaken for a late one.
	seen bool
}

// Store holds the projection.
type Store struct {
	mu       sync.RWMutex
	revision uint64
	accounts map[string]*entry
}

func New() *Store {
	return &Store{accounts: map[string]*entry{}}
}

// SetRevision records that the configuration has moved on.
//
// Observations from before it are refused from then on. Without this floor, a
// worker that started before a save and finished after it would report the old
// configuration's answer as the new one's.
func (s *Store) SetRevision(revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision > s.revision {
		s.revision = revision
	}
}

// ResetConfiguration drops observations and terminal notes about the previous
// configuration. The coordinator calls this only after every worker has exited.
func (s *Store) ResetConfiguration(revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision > s.revision {
		s.revision = revision
		s.accounts = map[string]*entry{}
	}
}

// Accept records an observation unless it is obsolete or late.
func (s *Store) Accept(observation Observation) (bool, Drop) {
	if observation.AccountID == "" {
		return false, DropNoAccount
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if observation.Revision < s.revision {
		return false, DropObsoleteRevision
	}
	current, known := s.accounts[observation.AccountID]
	if known && current.seen {
		if drop := staleness(current.view, observation); drop != DropNone {
			return false, drop
		}
	}
	if !known {
		current = &entry{}
		s.accounts[observation.AccountID] = current
	}

	// The note and the running action are not part of an observation and are
	// deliberately left alone: routine status must not replace the explanation
	// of the last thing a user asked for.
	current.seen = true
	current.view.AccountID = observation.AccountID
	current.view.Link = observation.Link
	current.view.Auth = observation.Auth
	current.view.Connectivity = observation.Connectivity
	current.view.Identity = observation.Identity
	current.view.Line = observation.Line
	current.view.ObservedAt = observation.At
	current.view.Revision = observation.Revision
	current.view.Generation = observation.Generation
	current.view.Sequence = observation.Sequence
	return true, DropNone
}

// staleness compares an incoming observation with what is already recorded.
//
// The comparison is lexicographic on (revision, generation, sequence) because
// that is the order the three change in: a configuration save bumps the
// revision, a new address bumps the generation, and every dispatch bumps the
// sequence. Comparing only the sequence would accept a worker that read a stale
// binding but happened to be dispatched later.
func staleness(current AccountView, incoming Observation) Drop {
	switch {
	case incoming.Revision < current.Revision:
		return DropObsoleteRevision
	case incoming.Revision > current.Revision:
		return DropNone
	case incoming.Generation < current.Generation:
		return DropObsoleteBinding
	case incoming.Generation > current.Generation:
		return DropNone
	case incoming.Sequence < current.Sequence:
		return DropLate
	default:
		return DropNone
	}
}

// ActionStarted records that an action is running and clears the previous
// note. A stale explanation sitting above a running action reads as the current
// state, which is worse than no explanation at all.
// Calling it repeatedly for the same action changes nothing, so a caller
// watching a stream of updates does not have to work out which one was the
// first: only a different action clears the note.
func (s *Store) ActionStarted(accountID, actionID string) {
	if accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entryFor(accountID)
	if current.view.RunningAction == actionID && actionID != "" {
		return
	}
	current.view.RunningAction = actionID
	current.note = nil
}

// ActionFinished records the note that stays until the next action starts.
//
// A note from an action that is no longer the running one is ignored: that is
// the same late-worker case as an observation, and letting it through would put
// a cancelled action's failure message above a login that is still going.
func (s *Store) ActionFinished(accountID string, note Note) {
	if accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entryFor(accountID)
	if current.view.RunningAction != "" && current.view.RunningAction != note.ActionID {
		return
	}
	current.view.RunningAction = ""
	kept := note
	current.note = &kept
}

// entryFor must be called with the lock held.
func (s *Store) entryFor(accountID string) *entry {
	current, known := s.accounts[accountID]
	if !known {
		current = &entry{}
		current.view.AccountID = accountID
		s.accounts[accountID] = current
	}
	return current
}

// Forget drops an account, for when one is deleted from the configuration.
func (s *Store) Forget(accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.accounts, accountID)
}

// Account reads one account.
func (s *Store) Account(accountID string) (AccountView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	current, known := s.accounts[accountID]
	if !known {
		return AccountView{}, false
	}
	return current.copy(), true
}

// Read returns the whole projection in one consistent picture.
func (s *Store) Read() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := Snapshot{Revision: s.revision,
		Accounts: make([]AccountView, 0, len(s.accounts))}
	for _, current := range s.accounts {
		snapshot.Accounts = append(snapshot.Accounts, current.copy())
	}
	// Map order is randomised, and a status page whose rows moved between polls
	// would be unreadable.
	sort.Slice(snapshot.Accounts, func(i, j int) bool {
		return snapshot.Accounts[i].AccountID < snapshot.Accounts[j].AccountID
	})
	return snapshot
}

// copy hands out a value with its own Note, so a reader cannot reach back
// through the pointer and change what the next reader sees.
func (e *entry) copy() AccountView {
	view := e.view
	if e.note != nil {
		note := *e.note
		view.Note = &note
	}
	return view
}
