package observe

import (
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

var moment = time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)

func observation(sequence uint64) Observation {
	return Observation{
		AccountID: "campus", Revision: 7, Generation: 3, Sequence: sequence,
		Link: domain.LinkReady, Auth: domain.AuthVerifiedSelf,
		Connectivity: domain.ConnectivityInternetReachable,
		Identity:     "2020123456", At: moment,
		Line: LineView{Iface: "wan", Device: "eth0.2", Address: "10.0.0.77"},
	}
}

func mustAccept(t *testing.T, store *Store, o Observation) {
	t.Helper()
	if ok, drop := store.Accept(o); !ok {
		t.Fatalf("observation %d was dropped as %s", o.Sequence, drop)
	}
}

func TestAnObservationBecomesTheView(t *testing.T) {
	store := New()
	mustAccept(t, store, observation(10))

	view, known := store.Account("campus")
	if !known {
		t.Fatal("the account was not recorded")
	}
	if view.Link != domain.LinkReady || view.Auth != domain.AuthVerifiedSelf ||
		view.Connectivity != domain.ConnectivityInternetReachable {
		t.Errorf("the three dimensions were not all kept: %+v", view)
	}
	if view.Identity != "2020123456" || !view.ObservedAt.Equal(moment) {
		t.Errorf("view = %+v", view)
	}
	// The line the observation was made on travels with it. A status page that
	// had to fall back to the configuration would be naming an interface
	// nobody had looked at.
	if view.Line.Iface != "wan" || view.Line.Device != "eth0.2" ||
		view.Line.Address != "10.0.0.77" {
		t.Errorf("Line = %+v, want what was observed", view.Line)
	}
	if _, known := store.Account("nobody"); known {
		t.Error("an account nobody observed exists")
	}
}

// T24 -- a worker that was cancelled and finished anyway does not overwrite the
// result of the one that replaced it.
//
// This is the failure spec 04 names. Without the check the display flips back
// to whatever the abandoned worker saw ten seconds ago, usually right after the
// user pressed the button that started the new one.
func TestALateResultDoesNotOverwriteANewerOne(t *testing.T) {
	store := New()
	mustAccept(t, store, observation(10))

	newer := observation(11)
	newer.Auth = domain.AuthRejected
	mustAccept(t, store, newer)

	stale := observation(10)
	stale.Auth = domain.AuthVerifiedSelf
	ok, drop := store.Accept(stale)
	if ok {
		t.Fatal("a result from an earlier dispatch was applied")
	}
	if drop != DropLate {
		t.Errorf("drop = %q, want %q", drop, DropLate)
	}

	view, _ := store.Account("campus")
	if view.Auth != domain.AuthRejected {
		t.Errorf("auth = %s; the late worker's answer won", view.Auth)
	}
	if view.Sequence != 11 {
		t.Errorf("sequence = %d, want the newer dispatch", view.Sequence)
	}
}

// A worker that read a binding which has since been replaced is answering about
// an address that may no longer exist.
func TestAnObservationFromAnOldBindingIsDropped(t *testing.T) {
	store := New()
	mustAccept(t, store, observation(10))

	current := observation(11)
	current.Generation = 4
	mustAccept(t, store, current)

	stale := observation(12) // later dispatch, older binding
	stale.Generation = 3
	ok, drop := store.Accept(stale)
	if ok || drop != DropObsoleteBinding {
		t.Errorf("accepted=%v drop=%q; a later dispatch that read an older "+
			"binding is still describing a line that has changed", ok, drop)
	}
}

// A worker that started before a configuration save and finished after it is
// answering a question about settings that no longer exist.
func TestAnObservationFromAnOldRevisionIsDropped(t *testing.T) {
	store := New()
	mustAccept(t, store, observation(10))

	store.SetRevision(8)
	ok, drop := store.Accept(observation(11))
	if ok || drop != DropObsoleteRevision {
		t.Errorf("accepted=%v drop=%q, want the revision floor to refuse it",
			ok, drop)
	}

	// The floor only moves forward: a stray older SetRevision must not reopen
	// the door to observations that were already refused.
	store.SetRevision(2)
	if ok, _ := store.Accept(observation(12)); ok {
		t.Error("the revision floor moved backwards")
	}

	current := observation(12)
	current.Revision = 8
	mustAccept(t, store, current)
}

// A newer revision wins even when the sequence looks older, because the three
// are compared in the order they change in.
func TestANewerRevisionOutranksAnOlderSequence(t *testing.T) {
	store := New()
	mustAccept(t, store, observation(50))

	fresh := observation(3)
	fresh.Revision = 9
	fresh.Generation = 1
	mustAccept(t, store, fresh)

	view, _ := store.Account("campus")
	if view.Revision != 9 {
		t.Errorf("revision = %d, want the newer configuration", view.Revision)
	}
}

func TestAnObservationWithNoAccountIsRefused(t *testing.T) {
	store := New()
	o := observation(1)
	o.AccountID = ""
	if ok, drop := store.Accept(o); ok || drop != DropNoAccount {
		t.Errorf("accepted=%v drop=%q", ok, drop)
	}
	if len(store.Read().Accounts) != 0 {
		t.Error("an anonymous observation created an account")
	}
}

// T24 -- the explanation of the last action stays until the next one.
//
// The baseline overwrote last_action_message from the daemon tick, so the
// reason a login failed disappeared a couple of seconds after it appeared,
// while the user was still reading it.
func TestATerminalNoteSurvivesRoutineObservation(t *testing.T) {
	store := New()
	store.ActionStarted("campus", "a1")
	store.ActionFinished("campus", Note{
		ActionID: "a1", Kind: "manual_login", State: "failed",
		Message: "密码错误", Code: domain.CodeAuthRejected, At: moment,
	})

	for sequence := uint64(10); sequence < 20; sequence++ {
		mustAccept(t, store, observation(sequence))
	}

	view, _ := store.Account("campus")
	if view.Note == nil {
		t.Fatal("the note was cleared by routine observation")
	}
	if view.Note.Message != "密码错误" || view.Note.Code != domain.CodeAuthRejected {
		t.Errorf("note = %+v", view.Note)
	}
	if view.RunningAction != "" {
		t.Errorf("running = %q after the action finished", view.RunningAction)
	}

	// A new action clears it: a stale explanation sitting above a running
	// action reads as the current state.
	store.ActionStarted("campus", "a2")
	view, _ = store.Account("campus")
	if view.Note != nil {
		t.Errorf("the previous note survived a new action: %+v", view.Note)
	}
	if view.RunningAction != "a2" {
		t.Errorf("running = %q, want a2", view.RunningAction)
	}
}

// A note from an action that is no longer the running one is the same late
// worker again, and it must not land on top of the one in progress.
func TestANoteFromASupersededActionIsIgnored(t *testing.T) {
	store := New()
	store.ActionStarted("campus", "a1")
	store.ActionStarted("campus", "a2")

	store.ActionFinished("campus", Note{ActionID: "a1", Message: "旧结果"})
	view, _ := store.Account("campus")
	if view.Note != nil {
		t.Fatalf("a superseded action's note landed: %+v", view.Note)
	}
	if view.RunningAction != "a2" {
		t.Errorf("running = %q; the superseded note cleared the live action",
			view.RunningAction)
	}

	store.ActionFinished("campus", Note{ActionID: "a2", Message: "新结果"})
	view, _ = store.Account("campus")
	if view.Note == nil || view.Note.Message != "新结果" {
		t.Errorf("note = %+v", view.Note)
	}
}

// A reader gets a copy. Handing out the stored pointer would let one caller
// change what the next one sees, which is the sort of thing that shows up as an
// impossible status page and never as a test failure.
func TestReadersCannotReachBackIntoTheStore(t *testing.T) {
	store := New()
	mustAccept(t, store, observation(1))
	store.ActionFinished("campus", Note{ActionID: "a1", Message: "原文"})

	first, _ := store.Account("campus")
	first.Note.Message = "被改过"

	second, _ := store.Account("campus")
	if second.Note.Message != "原文" {
		t.Errorf("the note was changed through a reader's copy: %q",
			second.Note.Message)
	}

	// Read hands out its own copies too, so one caller iterating a snapshot
	// cannot change what the next poll shows.
	snapshot := store.Read()
	snapshot.Accounts[0].Note.Message = "又被改过"
	if again := store.Read(); again.Accounts[0].Note.Message != "原文" {
		t.Errorf("the snapshot shares its notes with the store: %q",
			again.Accounts[0].Note.Message)
	}
}

func TestTheSnapshotIsOrderedAndComplete(t *testing.T) {
	store := New()
	store.SetRevision(7)
	for _, id := range []string{"zulu", "alpha", "mike"} {
		o := observation(1)
		o.AccountID = id
		mustAccept(t, store, o)
	}

	snapshot := store.Read()
	if snapshot.Revision != 7 {
		t.Errorf("revision = %d", snapshot.Revision)
	}
	var ids []string
	for _, account := range snapshot.Accounts {
		ids = append(ids, account.AccountID)
	}
	want := []string{"alpha", "mike", "zulu"}
	for index := range want {
		if ids[index] != want[index] {
			t.Fatalf("order = %v, want %v; a status page whose rows moved "+
				"between polls would look like something was changing", ids, want)
		}
	}

	store.Forget("mike")
	if _, known := store.Account("mike"); known {
		t.Error("a deleted account is still in the projection")
	}
	if len(store.Read().Accounts) != 2 {
		t.Errorf("accounts = %d after forgetting one", len(store.Read().Accounts))
	}
}

// The store is where the coordinator and the readers meet, so it is the one
// place that has to survive concurrent use. Run with -race, this is the check
// that the lock covers everything it needs to.
func TestConcurrentReadersAndOneWriter(t *testing.T) {
	store := New()
	var group sync.WaitGroup

	group.Go(func() {
		for sequence := uint64(1); sequence <= 500; sequence++ {
			store.Accept(observation(sequence))
			if sequence%50 == 0 {
				store.ActionStarted("campus", "a1")
				store.ActionFinished("campus", Note{ActionID: "a1", Message: "结果"})
			}
		}
	})

	for range 4 {
		group.Go(func() {
			for range 500 {
				snapshot := store.Read()
				for _, account := range snapshot.Accounts {
					if account.Note != nil {
						_ = account.Note.Message
					}
				}
				store.Account("campus")
			}
		})
	}
	group.Wait()

	view, known := store.Account("campus")
	if !known || view.Sequence != 500 {
		t.Errorf("final sequence = %d, want the last one written", view.Sequence)
	}
}
