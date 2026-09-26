package wireless

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// fakeStore is a UCI package held in a map, with a failure injectable at every
// step.
//
// Spec 07 asks for an exception at each phase, and a store that could only
// succeed would let the state machine look right while being unrecoverable at
// exactly the moments it exists for.
type fakeStore struct {
	values  map[Key]Value
	pending []string

	failRead    error
	failStage   error
	failCommit  error
	failReload  error
	stageCalls  int
	commitCalls int
	reloadCalls int

	// staged holds what Stage was given but Commit has not yet made live, so a
	// test can see that a failure before the commit left the live values alone.
	staged []Change
}

func newStore(values map[Key]Value) *fakeStore {
	if values == nil {
		values = map[Key]Value{}
	}
	return &fakeStore{values: values}
}

func (s *fakeStore) Read(_ context.Context, _ string, keys []Key) (map[Key]Value, error) {
	if s.failRead != nil {
		return nil, s.failRead
	}
	out := map[Key]Value{}
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

func (s *fakeStore) Stage(_ context.Context, _ string, changes []Change) error {
	s.stageCalls++
	if s.failStage != nil {
		return s.failStage
	}
	s.staged = append(s.staged, changes...)
	return nil
}

func (s *fakeStore) Commit(context.Context, string) error {
	s.commitCalls++
	if s.failCommit != nil {
		return s.failCommit
	}
	for _, change := range s.staged {
		if change.Delete {
			delete(s.values, change.Key)
			continue
		}
		s.values[change.Key] = Value{Text: change.Text, Present: true, IsList: change.IsList}
	}
	s.staged = nil
	return nil
}

func (s *fakeStore) PendingChanges(context.Context, string) ([]string, error) {
	return s.pending, nil
}

func (s *fakeStore) SectionKeys(_ context.Context, _, section string) ([]Key, error) {
	var keys []Key
	for key, value := range s.values {
		if key.Section == section && !key.IsSection() && value.Present {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *fakeStore) Reload(context.Context) error {
	s.reloadCalls++
	return s.failReload
}

var (
	ssid = Key{Section: "sta0", Option: "ssid"}
	key  = Key{Section: "sta0", Option: "key"}
	enc  = Key{Section: "sta0", Option: "encryption"}
	// home is the household's own access point. Nothing in a plan ever names
	// it; it is here so a test can prove it was not touched.
	home = Key{Section: "ap0", Option: "ssid"}
)

const passphrase = "hunter2-hunter2"

func homeStore() *fakeStore {
	return newStore(map[Key]Value{
		ssid: {Text: "old-network", Present: true},
		enc:  {Text: "none", Present: true},
		home: {Text: "HomeNet", Present: true},
	})
}

func campusPlan() Plan {
	return Plan{
		TaskID:         "task-1",
		Package:        "wireless",
		ConfigRevision: 7,
		ConfirmWithin:  15 * time.Minute,
		Changes: []Change{
			{Key: ssid, Text: "jxnu_stu"},
			{Key: enc, Text: "psk2"},
			// Absent before: rolling this back means deleting it, not blanking
			// it, and uci treats those differently.
			{Key: key, Text: passphrase},
		},
	}
}

func paths(t *testing.T) Paths {
	t.Helper()
	return Paths{Dir: t.TempDir()}
}

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

var epoch = time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)

// applied runs a transaction through to applied and returns everything.
func applied(t *testing.T, store *fakeStore, where Paths) *Transaction {
	t.Helper()
	plan := campusPlan()
	transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return transaction
}

// A transaction will not start on top of somebody else's half-finished edit.
func TestBeginRefusesWhenSomebodyElseHasUncommittedChanges(t *testing.T) {
	store := homeStore()
	store.pending = []string{"wireless.ap0.ssid='SomethingElse'"}

	_, err := Begin(t.Context(), store, paths(t), campusPlan(), fixedClock(epoch))
	if err == nil {
		t.Fatal("started on top of uncommitted changes")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}
}

// Nor on top of its own unfinished one.
//
// Starting a second transaction would make the first unrecoverable: its
// "before" values would become this one's "after".
func TestBeginRefusesWhileAnEarlierTransactionIsOpen(t *testing.T) {
	where := paths(t)
	store := homeStore()
	applied(t, store, where)

	_, err := Begin(t.Context(), store, where, campusPlan(), fixedClock(epoch))
	if err == nil {
		t.Fatal("a second transaction started while the first was open")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}
}

// The journal reaches applied before the commit, not after.
//
// The other order leaves the one window that matters unrecoverable: a crash
// between the commit and the write would leave the new values live with nothing
// on disk saying they were this transaction's to undo.
func TestTheJournalRecordsAppliedBeforeTheCommit(t *testing.T) {
	where := paths(t)
	store := homeStore()
	store.failCommit = errors.New("injected commit failure")

	plan := campusPlan()
	transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err == nil {
		t.Fatal("the injected commit failure was not reported")
	}

	journal, present, err := where.LoadJournal()
	if err != nil || !present {
		t.Fatalf("journal: %v present=%v", err, present)
	}
	if journal.Phase != PhaseApplied {
		t.Errorf("phase = %s, want applied -- a crash here must be recoverable",
			journal.Phase)
	}
}

// A failure before the commit leaves the live configuration untouched.
func TestAFailureBeforeTheCommitLeavesTheLiveConfigurationAlone(t *testing.T) {
	where := paths(t)
	store := homeStore()
	store.failStage = errors.New("injected staging failure")

	plan := campusPlan()
	transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err == nil {
		t.Fatal("the injected staging failure was not reported")
	}
	if store.commitCalls != 0 {
		t.Error("a failed staging still committed")
	}
	if got := store.values[ssid].Text; got != "old-network" {
		t.Errorf("ssid = %q, want it untouched", got)
	}
}

// And undoing that failure is a tidy-up, not a conflict report.
//
// A transaction that has only been backed up has written nothing live, so
// every option still holds the value it held before. Checking those against
// what this transaction *would* have written finds them all different, which is
// the conflict test's answer to a question nobody asked -- and it comes back as
// "somebody else changed these, a person has to look", on a router where
// nothing happened at all.
//
// The likely way to reach it is not an injected failure but a power cut: the
// journal is on disk from Begin, the process dies before Apply, and the next
// start recovers a change that was never made.
func TestUndoingAChangeThatWasNeverAppliedIsNotAConflict(t *testing.T) {
	for name, undo := range map[string]func(*testing.T, *fakeStore, Paths) Outcome{
		"rolled back in the same process": func(t *testing.T, store *fakeStore,
			where Paths) Outcome {

			plan := campusPlan()
			transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			store.failStage = errors.New("injected staging failure")
			if err := transaction.Apply(t.Context(), plan.Changes); err == nil {
				t.Fatal("the injected staging failure was not reported")
			}
			outcome, err := transaction.Rollback(t.Context())
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			return outcome
		},
		"recovered after a restart": func(t *testing.T, store *fakeStore,
			where Paths) Outcome {

			plan := campusPlan()
			if _, err := Begin(t.Context(), store, where, plan, fixedClock(epoch)); err != nil {
				t.Fatalf("Begin: %v", err)
			}
			// Nothing else. The journal is on disk at backed_up and the process
			// is gone.
			outcome, acted, err := Recover(t.Context(), store, where,
				plan.ConfigRevision, fixedClock(epoch))
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if !acted {
				t.Fatal("Recover found no journal to act on")
			}
			return outcome
		},
	} {
		store := homeStore()
		where := paths(t)
		outcome := undo(t, store, where)

		if len(outcome.Conflicts) != 0 {
			t.Errorf("%s: %d options reported as somebody else's change, on a "+
				"transaction that never wrote one: %v",
				name, len(outcome.Conflicts), outcome.Conflicts)
		}
		if outcome.Phase != PhaseRolledBack {
			t.Errorf("%s: phase = %s, want rolled back", name, outcome.Phase)
		}
		if got := store.values[ssid].Text; got != "old-network" {
			t.Errorf("%s: ssid = %q, want it untouched", name, got)
		}
		// And the journal is gone, so the next start does not examine it again.
		if _, present, err := where.LoadJournal(); err != nil || present {
			t.Errorf("%s: the journal survived a clean undo (present=%v, err=%v)",
				name, present, err)
		}
	}
}

// The same when the configuration moved on in the meantime.
//
// The revision rule holds a change back from being undone because the account
// save that went with it succeeded and the user was told so. A change that
// never reached the configuration was never reported as anything, so the rule
// does not apply -- and applying it anyway leaves the journal on disk, which
// Begin refuses to start on top of. A power cut in the wrong second would then
// block every future wireless change until somebody found the file.
func TestARecordOfAChangeThatNeverHappenedIsClearedEvenAfterAConfigChange(t *testing.T) {
	store := homeStore()
	where := paths(t)
	plan := campusPlan()
	if _, err := Begin(t.Context(), store, where, plan, fixedClock(epoch)); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	outcome, acted, err := Recover(t.Context(), store, where,
		plan.ConfigRevision+1, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !acted || outcome.Phase != PhaseRolledBack {
		t.Fatalf("outcome = %+v, acted = %v", outcome, acted)
	}
	if _, present, _ := where.LoadJournal(); present {
		t.Error("the journal was left behind, and Begin will refuse to start on it")
	}

	// And the proof that it is not merely deleted: a new transaction can start.
	if _, err := Begin(t.Context(), store, where, plan, fixedClock(epoch)); err != nil {
		t.Errorf("a later change was blocked by a record of one that never happened: %v", err)
	}
}

// Rolling back restores what this transaction wrote, and removes what it added.
func TestRollbackRestoresWhatWasThereAndRemovesWhatWasNot(t *testing.T) {
	where := paths(t)
	store := homeStore()
	transaction := applied(t, store, where)

	outcome, err := transaction.Rollback(t.Context())
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Fatalf("phase = %s, want rolled_back", outcome.Phase)
	}

	if got := store.values[ssid].Text; got != "old-network" {
		t.Errorf("ssid = %q, want the previous value back", got)
	}
	if got := store.values[enc].Text; got != "none" {
		t.Errorf("encryption = %q, want the previous value back", got)
	}
	// The key was absent before. Putting it back means removing it -- setting
	// it to an empty string is a different thing to uci and would leave a
	// half-configured client behind.
	if value, present := store.values[key]; present {
		t.Errorf("key is still present as %q; it was absent before", value.Text)
	}
	if len(outcome.Removed) != 1 || outcome.Removed[0] != key {
		t.Errorf("Removed = %v, want just the key", outcome.Removed)
	}

	// And the files are gone.
	if _, present, _ := where.LoadJournal(); present {
		t.Error("the journal survived a clean rollback")
	}
}

// The rule the package is arranged around: an option somebody else changed is
// left exactly as found.
//
// Writing the old configuration back unconditionally would undo whatever
// happened in between, and "in between" includes the user editing the home
// access point from LuCI while this was running.
func TestRollbackLeavesAloneWhatSomebodyElseChanged(t *testing.T) {
	where := paths(t)
	store := homeStore()
	transaction := applied(t, store, where)

	// Somebody edits the SSID this transaction wrote, after it was written.
	store.values[ssid] = Value{Text: "somebody-elses-choice", Present: true}

	outcome, err := transaction.Rollback(t.Context())
	if err == nil {
		t.Fatal("a conflicting rollback reported success")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeRecoveryRequired {
		t.Errorf("code = %s, want RecoveryRequired", code)
	}
	if outcome.Phase != PhaseRecoveryRequired {
		t.Errorf("phase = %s, want recovery_required", outcome.Phase)
	}

	if got := store.values[ssid].Text; got != "somebody-elses-choice" {
		t.Errorf("ssid = %q; the other edit was overwritten", got)
	}
	if len(outcome.Conflicts) != 1 || outcome.Conflicts[0] != ssid {
		t.Errorf("Conflicts = %v, want just the ssid", outcome.Conflicts)
	}
	// The options nobody else touched were still put back: a conflict on one
	// option does not abandon the rest.
	if got := store.values[enc].Text; got != "none" {
		t.Errorf("encryption = %q, want it restored", got)
	}

	// And the journal stays, because a person has to look at it.
	if _, present, _ := where.LoadJournal(); !present {
		t.Error("the journal was deleted despite an unresolved conflict")
	}
}

// Nothing a plan does not name is ever written.
func TestATransactionTouchesOnlyWhatItNamed(t *testing.T) {
	where := paths(t)
	store := homeStore()
	transaction := applied(t, store, where)

	if got := store.values[home].Text; got != "HomeNet" {
		t.Fatalf("the home access point became %q", got)
	}
	if _, err := transaction.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := store.values[home].Text; got != "HomeNet" {
		t.Errorf("the rollback changed the home access point to %q", got)
	}
}

// Confirming deletes the record, and doing it twice is not an error.
//
// Spec 04 asks for an idempotent commit: a retried RPC or a second click must
// not become a failure, and must not leave the passphrase copy behind either.
func TestConfirmIsIdempotentAndRemovesThePassphraseCopy(t *testing.T) {
	where := paths(t)
	transaction := applied(t, homeStore(), where)

	if err := transaction.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, present, _ := where.LoadBackup(); present {
		t.Error("the backup holding the passphrase survived confirmation")
	}
	if _, present, _ := where.LoadJournal(); present {
		t.Error("the journal survived confirmation")
	}
	if err := transaction.Confirm(); err != nil {
		t.Errorf("a second Confirm failed: %v", err)
	}
}

// The journal never holds the passphrase.
//
// It outlives the transaction and it is the file a support request would be
// asked to attach. A bare hash would not be enough either: an eight-character
// wireless key is a dictionary away from plaintext, which is why the hash is
// salted with a value generated per transaction.
func TestTheJournalNeverHoldsThePassphrase(t *testing.T) {
	where := paths(t)
	applied(t, homeStore(), where)

	raw, err := os.ReadFile(where.journal())
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(raw), passphrase) {
		t.Fatal("the journal contains the wireless passphrase in clear")
	}

	var journal Journal
	if err := json.Unmarshal(raw, &journal); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if journal.Salt == "" {
		t.Fatal("the journal has no salt, so its hashes are a dictionary away")
	}

	// Two transactions over the same value must not produce the same hash, or
	// the salt is doing nothing.
	other := Journal{Salt: "a-different-salt"}
	if other.Hash(passphrase) == journal.Hash(passphrase) {
		t.Error("the salt does not take part in the hash")
	}
}

// Recovery: nothing to do when there is no journal.
func TestRecoveryOnACleanStartDoesNothing(t *testing.T) {
	outcome, found, err := Recover(t.Context(), homeStore(), paths(t), 7,
		fixedClock(epoch))
	if err != nil || found {
		t.Fatalf("found = %v, err = %v, want a clean start", found, err)
	}
	if outcome.Phase != "" {
		t.Errorf("phase = %s, want nothing", outcome.Phase)
	}
}

// Recovery rolls back a change that was applied and never confirmed.
func TestRecoveryRollsBackAnAbandonedChange(t *testing.T) {
	where := paths(t)
	store := homeStore()
	applied(t, store, where)

	outcome, found, err := Recover(t.Context(), store, where, 7, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !found || outcome.Phase != PhaseRolledBack {
		t.Fatalf("found = %v, phase = %s", found, outcome.Phase)
	}
	if got := store.values[ssid].Text; got != "old-network" {
		t.Errorf("ssid = %q, want it restored", got)
	}
}

// But not one that is still inside its confirmation window.
func TestRecoveryLeavesAChangeStillWaitingToBeConfirmed(t *testing.T) {
	where := paths(t)
	store := homeStore()
	transaction := applied(t, store, where)
	if err := transaction.AwaitConfirm(); err != nil {
		t.Fatalf("AwaitConfirm: %v", err)
	}

	// Five minutes into a fifteen-minute window.
	outcome, found, err := Recover(t.Context(), store, where, 7,
		fixedClock(epoch.Add(5*time.Minute)))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !found || outcome.Phase != PhaseAwaitingConfirm {
		t.Fatalf("phase = %s, want it left alone", outcome.Phase)
	}
	if got := store.values[ssid].Text; got != "jxnu_stu" {
		t.Errorf("ssid = %q; a change still inside its window was rolled back", got)
	}
}

// And it does roll one back once the window has passed.
func TestRecoveryRollsBackOnceTheConfirmationWindowHasPassed(t *testing.T) {
	where := paths(t)
	store := homeStore()
	transaction := applied(t, store, where)
	if err := transaction.AwaitConfirm(); err != nil {
		t.Fatalf("AwaitConfirm: %v", err)
	}

	outcome, _, err := Recover(t.Context(), store, where, 7,
		fixedClock(epoch.Add(20*time.Minute)))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Fatalf("phase = %s, want rolled_back", outcome.Phase)
	}
	if got := store.values[ssid].Text; got != "old-network" {
		t.Errorf("ssid = %q, want it restored", got)
	}
}

// A journal from another configuration revision is not rolled back.
//
// Spec 04: the account save that goes with the change succeeded, so the user
// has been told it worked. Unconditionally reverting that is the failure the
// rule names -- the interface said yes and the device quietly said no.
func TestRecoveryWillNotRevertAChangeTheUserWasToldSucceeded(t *testing.T) {
	where := paths(t)
	store := homeStore()
	applied(t, store, where)

	// The configuration moved on: the account was saved.
	outcome, _, err := Recover(t.Context(), store, where, 8, fixedClock(epoch))
	if err == nil {
		t.Fatal("a change from another revision was silently rolled back")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeRecoveryRequired {
		t.Errorf("code = %s, want RecoveryRequired", code)
	}
	if outcome.Phase != PhaseRecoveryRequired {
		t.Errorf("phase = %s", outcome.Phase)
	}
	if got := store.values[ssid].Text; got != "jxnu_stu" {
		t.Errorf("ssid = %q; the change was reverted anyway", got)
	}
}

// A journal whose backup is gone is reported, not guessed at.
func TestRecoveryWithoutTheBackupAsksForHelpRatherThanGuessing(t *testing.T) {
	where := paths(t)
	store := homeStore()
	applied(t, store, where)
	if err := os.Remove(where.backup()); err != nil {
		t.Fatalf("remove backup: %v", err)
	}

	_, _, err := Recover(t.Context(), store, where, 7, fixedClock(epoch))
	if err == nil {
		t.Fatal("a rollback without the values to restore reported success")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeRecoveryRequired {
		t.Errorf("code = %s, want RecoveryRequired", code)
	}
}

// A journal this build does not understand is left alone.
func TestAJournalFromAnotherVersionIsLeftAlone(t *testing.T) {
	where := paths(t)
	if err := writePrivate(where.journal(), map[string]any{
		"version": JournalVersion + 1, "task_id": "from-the-future",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, err := Recover(t.Context(), homeStore(), where, 7, fixedClock(epoch))
	if err == nil {
		t.Fatal("a journal from another version was acted on")
	}
	if _, statErr := os.Stat(where.journal()); statErr != nil {
		t.Error("the unreadable journal was deleted rather than kept for a person")
	}
}

// A transaction that was only planned has nothing to undo.
func TestRecoveryOfAPlannedTransactionJustClearsIt(t *testing.T) {
	where := paths(t)
	store := homeStore()
	journal := &Journal{
		Version: JournalVersion, TaskID: "t", Phase: PhasePlanned,
		Package: "wireless", ConfigRevision: 7, Salt: "s",
	}
	if err := where.SaveJournal(journal); err != nil {
		t.Fatalf("save: %v", err)
	}

	outcome, _, err := Recover(t.Context(), store, where, 7, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Errorf("phase = %s", outcome.Phase)
	}
	if store.stageCalls != 0 {
		t.Error("a planned transaction wrote something while being recovered")
	}
	if _, present, _ := where.LoadJournal(); present {
		t.Error("the journal was kept for a transaction that changed nothing")
	}
}

// An empty plan is refused rather than producing a transaction that does
// nothing but has to be cleaned up.
func TestAnEmptyPlanIsRefused(t *testing.T) {
	plan := campusPlan()
	plan.Changes = nil
	if _, err := Begin(t.Context(), homeStore(), paths(t), plan,
		fixedClock(epoch)); err == nil {
		t.Fatal("an empty plan started a transaction")
	}
}

// An option this transaction deleted, which somebody has since put back, is
// not deleted again.
//
// The "is it still ours" question has a second shape for a deletion: we left
// nothing there, so it is still ours only while there is still nothing there.
// Deleting whatever appeared since would throw away somebody else's work with
// no record that it existed.
func TestADeletionIsOnlyUndoneWhileNobodyHasPutTheOptionBack(t *testing.T) {
	where := paths(t)
	store := newStore(map[Key]Value{
		ssid: {Text: "old-network", Present: true},
		key:  {Text: "old-key", Present: true},
	})

	plan := campusPlan()
	plan.Changes = []Change{
		{Key: ssid, Text: "jxnu_stu"},
		// An open network: the key is removed rather than blanked.
		{Key: key, Delete: true},
	}
	transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, present := store.values[key]; present {
		t.Fatal("the key was not deleted")
	}

	// Somebody sets a key on that section afterwards.
	store.values[key] = Value{Text: "somebody-elses-key", Present: true}

	outcome, err := transaction.Rollback(t.Context())
	if err == nil {
		t.Fatal("the rollback reported success over somebody else's value")
	}
	if len(outcome.Conflicts) != 1 || outcome.Conflicts[0] != key {
		t.Fatalf("Conflicts = %v, want the key", outcome.Conflicts)
	}
	if got := store.values[key].Text; got != "somebody-elses-key" {
		t.Errorf("key = %q; the value somebody else set was removed", got)
	}
	// The SSID was untouched by anybody else, so it still goes back.
	if got := store.values[ssid].Text; got != "old-network" {
		t.Errorf("ssid = %q, want it restored", got)
	}
}

// An option this transaction wrote, which has since been removed entirely, is
// also a conflict.
func TestAnOptionRemovedBySomebodyElseIsAConflict(t *testing.T) {
	where := paths(t)
	store := homeStore()
	transaction := applied(t, store, where)

	delete(store.values, ssid)

	outcome, err := transaction.Rollback(t.Context())
	if err == nil {
		t.Fatal("a removed option was silently rewritten")
	}
	if len(outcome.Conflicts) != 1 || outcome.Conflicts[0] != ssid {
		t.Errorf("Conflicts = %v, want the ssid", outcome.Conflicts)
	}
	if _, present := store.values[ssid]; present {
		t.Error("the option somebody removed was put back")
	}
}

// The phases are a state machine, and the transitions that are not allowed say
// so rather than half-happening.
func TestThePhasesRefuseTheTransitionsTheyDoNotHave(t *testing.T) {
	where := paths(t)
	store := homeStore()
	plan := campusPlan()

	transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if transaction.TaskID() != "task-1" {
		t.Errorf("TaskID = %q", transaction.TaskID())
	}
	if transaction.Phase() != PhaseBackedUp {
		t.Fatalf("phase = %s, want backed_up", transaction.Phase())
	}

	// Nothing is live yet, so there is nothing to wait for or confirm.
	if err := transaction.AwaitConfirm(); err == nil {
		t.Error("AwaitConfirm was accepted before anything was applied")
	}
	if err := transaction.Confirm(); err == nil {
		t.Error("Confirm was accepted before anything was applied")
	}

	if err := transaction.Apply(t.Context(), plan.Changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if transaction.Phase() != PhaseApplied {
		t.Fatalf("phase = %s, want applied", transaction.Phase())
	}
	// Applying twice would write the second change's "before" values over the
	// first's, which is how a transaction stops being undoable.
	if err := transaction.Apply(t.Context(), plan.Changes); err == nil {
		t.Error("a second Apply was accepted")
	}
}

// A reload that fails is reported, and the change is still recorded as applied.
//
// The values are live at that point whatever the radio did with them, so the
// journal has to say so or recovery would not know there was anything to undo.
func TestAFailedReloadIsReportedAndStillCountsAsApplied(t *testing.T) {
	where := paths(t)
	store := homeStore()
	store.failReload = errors.New("injected reload failure")

	plan := campusPlan()
	transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err == nil {
		t.Fatal("the injected reload failure was not reported")
	}

	journal, present, err := where.LoadJournal()
	if err != nil || !present {
		t.Fatalf("journal: %v present=%v", err, present)
	}
	if journal.Phase != PhaseApplied {
		t.Errorf("phase = %s, want applied", journal.Phase)
	}
	// And recovery can undo it once the fault clears -- which is the realistic
	// shape: the reload failed, the daemon restarted, and the undo happens on
	// a device where wifi reload works again.
	store.failReload = nil
	outcome, _, err := Recover(t.Context(), store, where, 7, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Errorf("phase = %s, want rolled_back", outcome.Phase)
	}
}

// A rollback that finished its writes and then died is finished, not a
// conflict.
//
// rollback writes `rolling_back` to disk before it touches anything, and only
// assigns `rolled_back` in memory on the way out -- so the window between the
// last store write and Clear() succeeding leaves a journal saying rolling_back
// over options that already hold their old values. Two ordinary ways in: Clear
// failing on a full or failing flash, and a power cut, which is likelier here
// than it sounds because the step before it is `/etc/init.d/network reload`.
//
// Resuming it then compares each option against what this transaction *wrote*,
// finds the old value instead, and calls every one of them somebody else's
// edit. The journal stays on disk and Begin refuses to start on top of it, so
// every later wireless change is blocked until a person deletes the file.
func TestAResumedRollbackWhoseWritesAlreadyLandedIsDone(t *testing.T) {
	store := homeStore()
	where := paths(t)
	applied(t, store, where)

	// The writes landed: every option is back at its "before" value. This is
	// what the store looks like the instant before Clear() would have run.
	store.values[ssid] = Value{Text: "old-network", Present: true}
	store.values[enc] = Value{Text: "none", Present: true}
	delete(store.values, key)

	journal, _, err := where.LoadJournal()
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	journal.Phase = PhaseRollingBack
	if err := where.SaveJournal(journal); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	outcome, acted, err := Recover(t.Context(), store, where, 7, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !acted {
		t.Fatal("Recover found no journal")
	}
	if len(outcome.Conflicts) != 0 {
		t.Errorf("%d options reported as somebody else's change, on a rollback "+
			"that had already put them back: %v", len(outcome.Conflicts),
			outcome.Conflicts)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Errorf("phase = %s, want rolled back", outcome.Phase)
	}
	if _, present, _ := where.LoadJournal(); present {
		t.Error("the journal was left behind, and Begin will refuse to start on it")
	}
}

// A commit that failed wrote nothing, so undoing it is a tidy-up too.
//
// The journal reaches `applied` before the commit on purpose -- that is what
// makes a crash between them recoverable -- which means `applied` does not
// promise the values were published. When the commit then fails, the options
// still hold their old values, and the same comparison calls all of them
// conflicts.
func TestUndoingAFailedCommitIsNotAConflict(t *testing.T) {
	store := homeStore()
	where := paths(t)
	store.failCommit = errors.New("injected commit failure")

	plan := campusPlan()
	transaction, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err == nil {
		t.Fatal("the injected commit failure was not reported")
	}
	store.failCommit = nil

	outcome, err := transaction.Rollback(t.Context())
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(outcome.Conflicts) != 0 {
		t.Errorf("%d conflicts after a commit that published nothing: %v",
			len(outcome.Conflicts), outcome.Conflicts)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Errorf("phase = %s, want rolled back", outcome.Phase)
	}
}

// A confirmed change stays confirmed even if clearing its record fails.
//
// Confirm assigns `committed` in memory and then deletes both files; the phase
// never reaches the disk. Clear removes the backup first, so a failure between
// the two leaves a journal still saying awaiting_confirm with no backup beside
// it -- and the next start reads that as a change it cannot undo and reports
// recovery required, for a change the user was already told had worked. Spec 04
// is explicit that a success the user has been shown must not be rolled back
// behind their back.
func TestAConfirmedChangeSurvivesAFailureToClearItsRecord(t *testing.T) {
	store := homeStore()
	where := paths(t)
	transaction := applied(t, store, where)
	if err := transaction.AwaitConfirm(); err != nil {
		t.Fatalf("AwaitConfirm: %v", err)
	}

	// Make the deletion fail. A non-empty directory where the backup file was:
	// os.Remove answers ENOTEMPTY for it whatever the process's uid, which a
	// permission bit would not -- some of the containers this is built in run
	// the tests as root, where 0000 stops nothing.
	//
	// Clear removes the backup first, so it stops there and never reaches the
	// journal. Whatever the journal says at that point is what the next start
	// will read.
	if err := os.Remove(where.backup()); err != nil {
		t.Fatalf("remove the backup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(where.backup(), "occupied"), 0o700); err != nil {
		t.Fatalf("block the backup path: %v", err)
	}

	if err := transaction.Confirm(); err == nil {
		t.Fatal("Confirm reported success while its record could not be deleted")
	}

	journal, present, err := where.LoadJournal()
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	if !present {
		t.Fatal("the journal is gone, so this test is checking nothing")
	}
	if journal.Phase != PhaseCommitted {
		t.Fatalf("the journal says %s; a confirmed change has to be recorded as "+
			"committed before its record is deleted, or the next start reads it "+
			"as one to undo", journal.Phase)
	}

	// Which is the point: the next start leaves it alone.
	if !journal.Phase.Terminal() {
		t.Error("committed is not terminal, so Recover would try to undo it")
	}
}

// A journal left by a transaction that finished does not block the next one.
//
// Begin refuses while a journal is on disk, which is right for one still in
// flight: its "before" values would become the new transaction's "after". A
// committed or rolled_back journal is what a failed Clear leaves behind, and
// refusing on it blocks every wireless change until somebody restarts the
// service. The one that still refuses is recovery_required -- that one is
// waiting for a person, and starting over it would bury what they have to look
// at.
func TestAFinishedTransactionsLeftoverJournalDoesNotBlockTheNextOne(t *testing.T) {
	for phase, wantStart := range map[Phase]bool{
		PhaseCommitted:        true,
		PhaseRolledBack:       true,
		PhaseRecoveryRequired: false,
		PhaseAwaitingConfirm:  false,
		PhaseRollingBack:      false,
	} {
		store := homeStore()
		where := paths(t)
		if err := where.SaveJournal(&Journal{
			Version: JournalVersion, TaskID: "old", Phase: phase,
			Package: "wireless", Salt: "abcd", StartedAt: epoch,
		}); err != nil {
			t.Fatalf("%s: SaveJournal: %v", phase, err)
		}

		_, err := Begin(t.Context(), store, where, campusPlan(), fixedClock(epoch))
		if wantStart {
			if err != nil {
				t.Errorf("%s: a finished transaction's leftovers blocked a new "+
					"change: %v", phase, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: a new change started over an unfinished one", phase)
		}
	}
}

// Clearing a finished transaction's leftovers takes its passphrase copy with
// them.
//
// A committed journal with a backup still beside it is what a half-completed
// Confirm leaves: Clear removes the backup first, so the pair can only be in
// that state if it failed on the journal -- or, as here, the other way about.
// The backup holds a wireless passphrase in clear, and spec 04 says it must not
// outlive the change it existed for.
//
// Begin overwrites both files on its way to backed_up, so the only window where
// this is visible is a Begin that refuses after noticing the leftovers -- which
// is exactly the case where nothing else will tidy up either.
func TestStartingOverRemovesThePreviousTransactionsPassphraseCopy(t *testing.T) {
	store := homeStore()
	where := paths(t)
	if err := where.SaveBackup(&Backup{
		TaskID: "old", Values: map[string]string{"sta0.key": passphrase},
	}); err != nil {
		t.Fatalf("SaveBackup: %v", err)
	}
	if err := where.SaveJournal(&Journal{
		Version: JournalVersion, TaskID: "old", Phase: PhaseCommitted,
		Package: "wireless", Salt: "abcd", StartedAt: epoch,
	}); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	// Somebody else is mid-edit, so this Begin refuses after it has dealt with
	// the leftovers and before it writes anything of its own.
	store.pending = []string{"wireless.ap0.ssid='SomethingElse'"}
	if _, err := Begin(t.Context(), store, where, campusPlan(),
		fixedClock(epoch)); err == nil {
		t.Fatal("Begin started on top of somebody else's uncommitted changes")
	}

	if _, present, _ := where.LoadBackup(); present {
		t.Error("the previous transaction's backup survived, passphrase and all")
	}
	if _, present, _ := where.LoadJournal(); present {
		t.Error("the previous transaction's journal survived")
	}
}

// And the phase a refusal names is the one on disk.
//
// "The last change is not finished" tells somebody to go and look without
// saying where. Whether it stopped waiting for a confirmation or needs a person
// are different situations with different next steps.
func TestARefusalNamesWhyTheLastChangeIsStillOpen(t *testing.T) {
	store := homeStore()
	where := paths(t)
	if err := where.SaveJournal(&Journal{
		Version: JournalVersion, TaskID: "old", Phase: PhaseRecoveryRequired,
		Package: "wireless", Salt: "abcd", StartedAt: epoch,
	}); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	_, err := Begin(t.Context(), store, where, campusPlan(), fixedClock(epoch))
	if code, _ := domain.CodeOf(err); code != domain.CodeRecoveryRequired {
		t.Fatalf("Begin = %v (code %s), want RecoveryRequired", err, code)
	}

	// And the record is still there. Refusing is only half of it: the whole
	// point of recovery_required is that somebody has to look at what was left
	// alone, and a refusal that tidied the evidence away on its way out would
	// leave them nothing to look at.
	journal, present, err := where.LoadJournal()
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	if !present || journal.Phase != PhaseRecoveryRequired {
		t.Errorf("the record a person has to read was removed by the refusal "+
			"(present=%v)", present)
	}
}

// A change that was never applied needs no backup to undo.
//
// Nothing was written, so there is nothing to restore and the file holding the
// old values is beside the point. That matters because the backup is the one
// that holds a passphrase in clear: spec 04 says it must not outlive the change
// it exists for, so it is the file most likely to be gone -- deleted early, or
// missing from a restored configuration. Reading its absence as "this cannot be
// undone" would report recovery required for a change that never happened.
// Both entry points, because they take different routes to the same answer:
// Recover decides it before it calls rollback, and Rollback has to decide it
// inside. A test that only drove Recover would leave the half inside rollback
// uncovered -- which it did, until this grew its second case.
func TestUndoingAChangeThatWasNeverAppliedNeedsNoBackup(t *testing.T) {
	for name, undo := range map[string]func(*testing.T, *fakeStore, Paths, *Transaction) (Outcome, error){
		"recovered after a restart": func(t *testing.T, store *fakeStore,
			where Paths, _ *Transaction) (Outcome, error) {

			outcome, acted, err := Recover(t.Context(), store, where, 7,
				fixedClock(epoch))
			if !acted && err == nil {
				t.Fatal("Recover found no journal to act on")
			}
			return outcome, err
		},
		"rolled back in the same process": func(t *testing.T, _ *fakeStore,
			_ Paths, transaction *Transaction) (Outcome, error) {

			return transaction.Rollback(t.Context())
		},
	} {
		store := homeStore()
		where := paths(t)
		transaction, err := Begin(t.Context(), store, where, campusPlan(),
			fixedClock(epoch))
		if err != nil {
			t.Fatalf("%s: Begin: %v", name, err)
		}
		if err := os.Remove(where.backup()); err != nil {
			t.Fatalf("%s: remove the backup: %v", name, err)
		}

		outcome, err := undo(t, store, where, transaction)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if outcome.Phase != PhaseRolledBack {
			t.Errorf("%s: phase = %s, want a clean tidy-up", name, outcome.Phase)
		}
		if _, present, _ := where.LoadJournal(); present {
			t.Errorf("%s: the journal was left behind, and Begin will refuse to "+
				"start on it", name)
		}
	}
}

// A rollback interrupted partway is resumable, which is why rolling_back is a
// recorded phase rather than a moment inside a function.
func TestAnInterruptedRollbackIsResumed(t *testing.T) {
	where := paths(t)
	store := homeStore()
	applied(t, store, where)

	// Simulate a crash during the rollback: the phase is on disk, the values
	// are still the ones this transaction wrote.
	journal, _, err := where.LoadJournal()
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	journal.Phase = PhaseRollingBack
	if err := where.SaveJournal(journal); err != nil {
		t.Fatalf("save: %v", err)
	}

	outcome, found, err := Recover(t.Context(), store, where, 7, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !found || outcome.Phase != PhaseRolledBack {
		t.Fatalf("phase = %s, want the rollback finished", outcome.Phase)
	}
	if got := store.values[ssid].Text; got != "old-network" {
		t.Errorf("ssid = %q, want it restored", got)
	}
}

// A terminal journal left behind is cleared rather than examined forever.
func TestATerminalJournalIsClearedAtStartup(t *testing.T) {
	for _, phase := range []Phase{PhaseCommitted, PhaseRolledBack} {
		where := paths(t)
		if err := where.SaveJournal(&Journal{
			Version: JournalVersion, TaskID: "t", Phase: phase,
			Package: "wireless", ConfigRevision: 7, Salt: "s",
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
		outcome, found, err := Recover(t.Context(), homeStore(), where, 7,
			fixedClock(epoch))
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		if !found || outcome.Phase != phase {
			t.Errorf("%s: found=%v phase=%s", phase, found, outcome.Phase)
		}
		if _, present, _ := where.LoadJournal(); present {
			t.Errorf("%s: a finished transaction was kept", phase)
		}
		if !phase.Terminal() {
			t.Errorf("%s should be terminal", phase)
		}
	}
	for _, phase := range []Phase{PhasePlanned, PhaseBackedUp, PhaseApplied,
		PhaseAwaitingConfirm, PhaseRollingBack} {
		if phase.Terminal() {
			t.Errorf("%s should not be terminal", phase)
		}
	}
}

// A store that cannot be read stops the transaction before it writes anything.
func TestAStoreThatCannotBeReadStopsBeforeWriting(t *testing.T) {
	store := homeStore()
	store.failRead = errors.New("injected read failure")

	if _, err := Begin(t.Context(), store, paths(t), campusPlan(),
		fixedClock(epoch)); err == nil {
		t.Fatal("a transaction started without reading the previous values")
	}
	if store.stageCalls != 0 {
		t.Error("something was staged without a backup")
	}
}
