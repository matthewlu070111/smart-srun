package wireless

import (
	"context"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Store is the UCI a transaction reads and writes.
//
// Consumer-defined here, and deliberately narrow. Every method names a package
// and the exact options it touches, because a store that could write "the
// wireless configuration" is a store that could write the home access point --
// and spec 04 forbids that in a way no amount of care at the call site can
// guarantee. Making it inexpressible is stronger than remembering not to.
type Store interface {
	// Read returns the current value of each key, and whether it was present at
	// all. A present value is never empty: uci has no option holding the empty
	// string, so an implementation that reported one would be describing a
	// state no later call could restore or remove.
	Read(ctx context.Context, pkg string, keys []Key) (map[Key]Value, error)
	// SectionKeys is needed before deleting a section we created: newly added
	// third-party options must not disappear along with that section.
	SectionKeys(ctx context.Context, pkg, section string) ([]Key, error)
	// Stage writes the changes somewhere they are not yet live. The adapter
	// does this in an isolated UCI directory, so a failure between here and
	// Commit leaves the running configuration untouched.
	Stage(ctx context.Context, pkg string, changes []Change) error
	// Commit makes the staged changes live.
	Commit(ctx context.Context, pkg string) error
	// PendingChanges reports uncommitted changes that are already there. Spec
	// 04 refuses to start on top of somebody else's half-finished edit.
	PendingChanges(ctx context.Context, pkg string) ([]string, error)
	// Reload makes the live configuration take effect on the radio.
	Reload(ctx context.Context) error
}

// Value is one option as the store found it.
type Value struct {
	Text    string
	Present bool
	// IsList means Text is a JSON array, preserving order and scalar/list type.
	IsList bool
}

// Change is one option this transaction wants to write.
type Change struct {
	Key Key
	// Text is the new value, and must not be empty unless Delete is set. uci
	// has no such thing as an option holding the empty string: it does not
	// write one, does not report one, and cannot delete one. Measured on a
	// real device, 2026-09-15 -- see evidence/uci-behaviour-router2.
	//
	// Left expressible, "set this to nothing" would be accepted, do nothing,
	// and leave the journal claiming a value that was never written. See
	// Begin, which refuses it.
	Text   string
	IsList bool
	// Delete removes the option instead of setting it.
	Delete bool
}

// Plan is a transaction before it has touched anything.
type Plan struct {
	TaskID  string
	Package string
	Changes []Change
	// ConfigRevision is the configuration this change belongs to, so recovery
	// can tell a journal that describes the current world from one that does
	// not.
	ConfigRevision uint64
	// ConfirmWithin is how long the change has to be confirmed. Spec 04 gives
	// the wizard fifteen minutes; past it an unconfirmed change is rolled back
	// rather than left on a network nobody reported reaching.
	ConfirmWithin time.Duration
}

// Outcome is what a rollback or a recovery did, option by option.
//
// Per option rather than one verdict, because spec 04 requires the result to
// say which items were restored and which conflicted. "Recovery required" with
// no detail tells a person there is a problem and nothing about where.
type Outcome struct {
	Phase    Phase
	Restored []Key
	// Conflicts are options somebody else changed after this transaction wrote
	// them. They are left exactly as found.
	Conflicts []Key
	// Missing are options the backup has no value for, which means they were
	// absent before and have been deleted again.
	Removed []Key
}

// Transaction is one wireless change, from planned to committed or undone.
type Transaction struct {
	store   Store
	paths   Paths
	now     func() time.Time
	journal *Journal
}

// Begin records the plan and returns a transaction that has touched nothing.
//
// Refusing to start is a real outcome here. An existing journal means a
// previous change was never finished, and starting a second one on top would
// make the first unrecoverable -- its "before" values would be this one's
// "after". Uncommitted UCI changes mean somebody else is mid-edit, and spec 04
// refuses to build on that.
func Begin(ctx context.Context, store Store, paths Paths, plan Plan,
	now func() time.Time) (*Transaction, error) {

	if now == nil {
		now = time.Now
	}
	if plan.TaskID == "" {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"无线事务需要一个任务 ID")
	}
	if plan.Package == "" {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"无线事务需要指明配置包")
	}
	if len(plan.Changes) == 0 {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"无线事务没有要写入的内容")
	}
	if err := checkWritable(plan.Changes); err != nil {
		return nil, err
	}
	seen := map[Key]bool{}
	for _, change := range plan.Changes {
		if seen[change.Key] {
			return nil, domain.Errorf(domain.CodeInvalidArgument, "无线事务含重复配置项")
		}
		seen[change.Key] = true
	}

	// A journal left behind by a transaction that finished is cleaned up rather
	// than treated as one still in flight.
	//
	// The refusal exists to stop this building on an unfinished change, whose
	// "before" values would become this one's "after". A committed or
	// rolled_back journal is finished by definition and holds nothing to lose:
	// it is what a failed Clear leaves, and refusing on it would block every
	// wireless change until somebody restarted the service. recovery_required
	// is different and still refuses -- that one is waiting for a person.
	previous, present, err := paths.LoadJournal()
	if err != nil {
		return nil, err
	}
	if present {
		switch previous.Phase {
		case PhaseCommitted, PhaseRolledBack:
			if err := paths.Clear(); err != nil {
				return nil, err
			}
		case PhaseRecoveryRequired:
			return nil, domain.Errorf(domain.CodeRecoveryRequired,
				"上一次无线改动有需要人工确认的项，处理完再开始新的")
		default:
			return nil, domain.Errorf(domain.CodeConflict,
				"上一次无线改动停在 %s，还没有收尾，先处理它再开始新的",
				previous.Phase)
		}
	}

	pending, err := store.PendingChanges(ctx, plan.Package)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, domain.Errorf(domain.CodeConflict,
			"%s 里有 %d 项未应用的改动，不在别人改了一半的配置上继续",
			plan.Package, len(pending))
	}

	salt, err := newSalt()
	if err != nil {
		return nil, err
	}

	keys := make([]Key, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		keys = append(keys, change.Key)
	}
	before, err := store.Read(ctx, plan.Package, keys)
	if err != nil {
		return nil, err
	}

	started := now()
	journal := &Journal{
		Version:        JournalVersion,
		TaskID:         plan.TaskID,
		Phase:          PhasePlanned,
		Package:        plan.Package,
		ConfigRevision: plan.ConfigRevision,
		Salt:           salt,
		StartedAt:      started,
		ExpiresAt:      started.Add(plan.ConfirmWithin),
	}

	backup := &Backup{TaskID: plan.TaskID, Values: map[string]string{}}
	for _, change := range plan.Changes {
		previous := before[change.Key]
		if change.Key.IsSection() && previous.Present && (change.Delete || change.Text != previous.Text) {
			return nil, domain.Errorf(domain.CodeConflict, "不能删除或改变已有配置节的类型")
		}
		entry := Entry{
			Key:           change.Key,
			BeforePresent: previous.Present,
			AfterDeleted:  change.Delete,
			BeforeList:    previous.IsList,
			AfterList:     change.IsList,
		}
		if previous.Present {
			entry.BeforeHash = journal.Hash(previous.Text)
			backup.Values[keyString(change.Key)] = previous.Text
		}
		if !change.Delete {
			entry.AfterHash = journal.Hash(change.Text)
		}
		journal.Entries = append(journal.Entries, entry)
	}

	// The backup goes down before the journal names the phase it belongs to.
	// The other order leaves a window where the journal says values are
	// recoverable and the file holding them is not there yet.
	if err := paths.SaveBackup(backup); err != nil {
		return nil, err
	}
	journal.Phase = PhaseBackedUp
	if err := paths.SaveJournal(journal); err != nil {
		return nil, err
	}

	return &Transaction{store: store, paths: paths, now: now, journal: journal},
		nil
}

// Phase is where this transaction has got to.
func (t *Transaction) Phase() Phase { return t.journal.Phase }

// TaskID identifies it.
func (t *Transaction) TaskID() string { return t.journal.TaskID }

// Apply stages the change, commits it and reloads the radio.
//
// The journal reaches applied *before* the commit, not after. Recording it
// afterwards would leave the one window that matters unrecoverable: a crash
// between the commit and the write would leave the new values live and nothing
// on disk saying they were ever this transaction's to undo.
func (t *Transaction) Apply(ctx context.Context, changes []Change) error {
	if t.journal.Phase != PhaseBackedUp {
		return domain.Errorf(domain.CodeConflict,
			"无线事务处于 %s，不能再应用一次", t.journal.Phase)
	}
	if len(changes) != len(t.journal.Entries) {
		return domain.Errorf(domain.CodeConflict, "应用内容与事务计划不符")
	}
	seen := map[Key]bool{}
	for _, change := range changes {
		matched := false
		for _, entry := range t.journal.Entries {
			if entry.Key == change.Key && entry.AfterDeleted == change.Delete && entry.AfterList == change.IsList &&
				(change.Delete || t.journal.Hash(change.Text) == entry.AfterHash) {
				matched = true
				break
			}
		}
		if !matched || seen[change.Key] {
			return domain.Errorf(domain.CodeConflict, "应用内容与事务计划不符")
		}
		seen[change.Key] = true
	}
	if err := t.store.Stage(ctx, t.journal.Package, changes); err != nil {
		return err
	}
	if err := t.setPhase(PhaseApplied); err != nil {
		return err
	}
	if err := t.store.Commit(ctx, t.journal.Package); err != nil {
		return err
	}
	return t.store.Reload(ctx)
}

// AwaitConfirm marks the change live and waiting to be confirmed.
func (t *Transaction) AwaitConfirm() error {
	if t.journal.Phase != PhaseApplied {
		return domain.Errorf(domain.CodeConflict,
			"无线事务处于 %s，还没有可以等待确认的改动", t.journal.Phase)
	}
	return t.setPhase(PhaseAwaitingConfirm)
}

// Confirm accepts the change and deletes the record of how to undo it.
//
// Idempotent, because spec 04 requires it: a confirmation that arrives twice --
// a retried RPC, a user pressing the button again -- must not become an error
// the second time, and must not leave a passphrase copy behind either.
func (t *Transaction) Confirm() error {
	if t.journal.Phase == PhaseCommitted {
		return t.paths.Clear()
	}
	if t.journal.Phase != PhaseApplied && t.journal.Phase != PhaseAwaitingConfirm {
		return domain.Errorf(domain.CodeConflict,
			"无线事务处于 %s，没有可以确认的改动", t.journal.Phase)
	}
	// Committed reaches the disk before either file is deleted.
	//
	// Assigning it in memory and going straight to Clear leaves the phase
	// nowhere if the deletion fails -- and Clear removes the backup first, so
	// what it leaves is a journal still saying awaiting_confirm with no backup
	// beside it. The next start reads that as a change it cannot undo and
	// reports recovery required, for a change the user was already told had
	// worked. Spec 04 is explicit that a success somebody has been shown must
	// not be unwound behind them.
	//
	// Committed is terminal, so once it is on disk a failed Clear is harmless:
	// recovery clears it and does nothing else.
	if err := t.setPhase(PhaseCommitted); err != nil {
		return err
	}
	return t.paths.Clear()
}

// Rollback undoes what this transaction wrote, and only that.
func (t *Transaction) Rollback(ctx context.Context) (Outcome, error) {
	return rollback(ctx, t.store, t.paths, t.journal)
}

func (t *Transaction) setPhase(phase Phase) error {
	t.journal.Phase = phase
	return t.paths.SaveJournal(t.journal)
}

// rollback restores every option whose current value is still the one this
// transaction wrote.
//
// This is the rule the whole package is arranged around. Writing the old
// configuration back unconditionally would undo whatever happened in between,
// and "in between" includes the user editing the home access point from LuCI
// while this was running. An option somebody else has since changed is left
// exactly as found and reported as a conflict, because the alternative is this
// program quietly reverting somebody's work.
func rollback(ctx context.Context, store Store, paths Paths, journal *Journal) (Outcome, error) {

	if journal.Phase.Terminal() {
		return Outcome{Phase: journal.Phase}, nil
	}

	// Nothing has been published yet, so there is nothing to undo and nothing
	// to compare against.
	//
	// Apply stages into an isolated directory and only reaches `applied` before
	// it commits, so a journal still saying planned or backed_up describes a
	// live configuration that was never touched. Running the conflict check on
	// one finds every option holding its original value, decides that is not
	// what this transaction wrote -- which is true and irrelevant -- and
	// reports the lot as somebody else's edit needing a person to look at. On a
	// router where nothing happened.
	//
	// The way to get here is not an injected failure. It is a power cut between
	// Begin writing the journal and Apply staging anything.
	if journal.Phase == PhasePlanned || journal.Phase == PhaseBackedUp {
		journal.Phase = PhaseRolledBack
		return Outcome{Phase: PhaseRolledBack}, paths.Clear()
	}

	journal.Phase = PhaseRollingBack
	if err := paths.SaveJournal(journal); err != nil {
		return Outcome{}, err
	}

	backup, present, err := paths.LoadBackup()
	if err != nil {
		return Outcome{}, err
	}
	if !present {
		// The journal says there is something to undo and the values needed to
		// undo it are gone. Guessing is worse than saying so.
		journal.Phase = PhaseRecoveryRequired
		_ = paths.SaveJournal(journal)
		return Outcome{Phase: PhaseRecoveryRequired}, domain.Errorf(
			domain.CodeRecoveryRequired,
			"无线事务 %s 的备份文件不存在，无法自动还原", journal.TaskID)
	}

	keys := make([]Key, 0, len(journal.Entries))
	for _, entry := range journal.Entries {
		keys = append(keys, entry.Key)
	}
	current, err := store.Read(ctx, journal.Package, keys)
	if err != nil {
		return Outcome{}, err
	}
	// Never remove a newly created section that now contains somebody else's
	// option, or whose owned options have been changed by somebody else.
	blockedSections := map[string]bool{}
	for _, entry := range journal.Entries {
		if !entry.Key.IsSection() || entry.BeforePresent || !current[entry.Key].Present {
			continue
		}
		children, err := store.SectionKeys(ctx, journal.Package, entry.Key.Section)
		if err != nil {
			return Outcome{}, err
		}
		owned := map[Key]Entry{}
		for _, item := range journal.Entries {
			owned[item.Key] = item
		}
		for _, child := range children {
			item, known := owned[child]
			if !known || (!stillOurs(journal, item, current[child]) && !alreadyBefore(journal, item, current[child])) {
				blockedSections[entry.Key.Section] = true
			}
		}
	}

	outcome := Outcome{}
	var restore []Change
	for _, entry := range journal.Entries {
		if entry.Key.IsSection() && blockedSections[entry.Key.Section] {
			outcome.Conflicts = append(outcome.Conflicts, entry.Key)
			continue
		}
		if alreadyBefore(journal, entry, current[entry.Key]) {
			// Nothing to do, and specifically not a conflict. Two ordinary ways
			// to arrive here: a rollback whose writes landed and then failed to
			// clear its journal, and an Apply whose commit failed -- the journal
			// reaches `applied` before the commit on purpose, so `applied` does
			// not promise anything was published.
			//
			// Asked first, because the option genuinely no longer holds what
			// this transaction wrote, so stillOurs answers no and means it. The
			// question it is answering is just not the one that matters when
			// the value is already where the undo would put it.
			outcome.Restored = append(outcome.Restored, entry.Key)
			continue
		}
		if !stillOurs(journal, entry, current[entry.Key]) {
			outcome.Conflicts = append(outcome.Conflicts, entry.Key)
			continue
		}
		if entry.BeforePresent {
			previous, exists := backup.Values[keyString(entry.Key)]
			if backup.TaskID != journal.TaskID || !exists || journal.Hash(previous) != entry.BeforeHash {
				return Outcome{}, domain.Errorf(domain.CodeRecoveryRequired, "无线事务备份与日志不符，已保留原文件")
			}
			restore = append(restore, Change{
				Key:    entry.Key,
				Text:   backup.Values[keyString(entry.Key)],
				IsList: entry.BeforeList,
			})
			outcome.Restored = append(outcome.Restored, entry.Key)
			continue
		}
		// It was not there before, so putting it back means removing it.
		restore = append(restore, Change{Key: entry.Key, Delete: true})
		outcome.Removed = append(outcome.Removed, entry.Key)
	}

	if len(restore) > 0 {
		if err := store.Stage(ctx, journal.Package, restore); err != nil {
			return Outcome{}, err
		}
		if err := store.Commit(ctx, journal.Package); err != nil {
			return Outcome{}, err
		}
		if err := store.Reload(ctx); err != nil {
			return Outcome{}, err
		}
	}

	if len(outcome.Conflicts) > 0 {
		// Partially undone, and the part that was not is somebody else's. The
		// journal stays on disk: a person has to look at it.
		outcome.Phase = PhaseRecoveryRequired
		journal.Phase = PhaseRecoveryRequired
		if err := paths.SaveJournal(journal); err != nil {
			return outcome, err
		}
		return outcome, domain.Errorf(domain.CodeRecoveryRequired,
			"无线事务 %s 有 %d 项已被其他地方修改，未予还原；需要人工确认",
			journal.TaskID, len(outcome.Conflicts))
	}

	outcome.Phase = PhaseRolledBack
	journal.Phase = PhaseRolledBack
	// Deliberately not saved before Clear, unlike Confirm.
	//
	// It was, briefly, and no test could be made to fail for removing it --
	// which is this project's definition of dead code, so it went. The reason
	// it is dead is alreadyBefore: if Clear fails here, the journal on disk
	// still says rolling_back, and the next start reads options that already
	// hold their old values, finds every entry already where the undo would put
	// it, and finishes cleanly. Writing rolled_back first would only reach the
	// same place by a shorter route.
	//
	// Confirm is different and does save first, because there is no equivalent
	// of alreadyBefore for it: nothing about the live configuration
	// distinguishes a confirmed change from one still awaiting confirmation.
	if err := paths.Clear(); err != nil {
		return outcome, err
	}
	return outcome, nil
}

// alreadyBefore reports that an option is already where the undo would put it.
//
// Compared against the journal's hash of the old value rather than the backup's
// copy of it, so this still answers when the backup is gone -- and for the same
// reason the journal holds hashes at all: one of these values is a passphrase.
//
// This does not weaken the conflict rule. If somebody else really did change an
// option to exactly the value this transaction found there, restoring it is a
// no-op and calling that a conflict would ask a person to look at nothing.
func alreadyBefore(journal *Journal, entry Entry, value Value) bool {
	if !entry.BeforePresent {
		return !value.Present
	}
	return value.Present && value.IsList == entry.BeforeList && journal.Hash(value.Text) == entry.BeforeHash
}

// checkWritable refuses a change uci would accept and then not perform.
//
// `uci set x.y.z=` writes nothing at all: the option ends up absent, while the
// journal records it as present holding the empty string. The rollback then
// reads an absent option where it expected its own value, decides somebody
// else removed it, and reports a conflict -- for an option nobody touched and
// that needed no undoing. The path is not hypothetical: an open network has no
// passphrase, so the plan for one carries key="".
//
// Refused rather than translated into a deletion. Translating would write the
// right thing and record the wrong one, which is the same false conflict with
// the cause hidden one layer deeper.
func checkWritable(changes []Change) error {
	for _, change := range changes {
		if err := checkKey(change.Key); err != nil {
			return err
		}
		if change.IsList {
			if change.Delete || change.Key.IsSection() {
				return domain.Errorf(domain.CodeInvalidArgument, "列表必须是有效的配置选项")
			}
			if _, err := listItems(change.Text); err != nil {
				return err
			}
		} else if !change.Delete {
			if _, err := quoteBatch(change.Text); err != nil {
				return err
			}
		}
		if !change.Delete && change.Text == "" {
			return domain.Errorf(domain.CodeInvalidArgument,
				"%s.%s 要写入空值；uci 没有空选项这种东西，请改用删除",
				change.Key.Section, change.Key.Option)
		}
	}
	return nil
}

// stillOurs reports that an option still holds what this transaction wrote.
func stillOurs(journal *Journal, entry Entry, value Value) bool {
	if entry.AfterDeleted {
		// We deleted it; it is still ours as long as nobody has put it back.
		return !value.Present
	}
	if !value.Present {
		// We wrote a value and it is gone. Somebody removed it.
		return false
	}
	return value.IsList == entry.AfterList && journal.Hash(value.Text) == entry.AfterHash
}

// Recover finishes whatever a previous run left open.
//
// Called at startup. Spec 04 is specific about the two ways this goes wrong:
// rolling back a change the user was already told had succeeded, and leaving a
// router on an unconfirmed network forever. The configuration revision is what
// tells them apart -- a journal from the configuration that is still current
// describes a change that is still meaningful.
func Recover(ctx context.Context, store Store, paths Paths,
	currentRevision uint64, now func() time.Time) (Outcome, bool, error) {

	if now == nil {
		now = time.Now
	}
	journal, present, err := paths.LoadJournal()
	if err != nil {
		return Outcome{}, present, err
	}
	if !present {
		return Outcome{}, false, nil
	}

	switch {
	case journal.Phase == PhaseRecoveryRequired:
		return Outcome{Phase: PhaseRecoveryRequired}, true, domain.Errorf(domain.CodeRecoveryRequired,
			"上次无线改动仍需人工确认，已保留事务和备份")
	case journal.Phase.Terminal():
		// Nothing to do. Clearing it here is what stops a finished transaction
		// being examined at every startup forever.
		return Outcome{Phase: journal.Phase}, true, paths.Clear()

	case journal.Phase == PhasePlanned || journal.Phase == PhaseBackedUp:
		// Nothing was written. Only the record exists.
		//
		// Before the revision check, and that order is the point. The revision
		// rule exists because the account save that goes with an applied change
		// succeeded, so the user has been told it worked -- but a change that
		// never reached the configuration cannot have been reported as
		// anything. Leaving it for a person would also leave the journal on
		// disk, and Begin refuses to start while one is there: a power cut in
		// the wrong second would block every future wireless change until
		// somebody found the file and deleted it.
		return Outcome{Phase: PhaseRolledBack}, true, paths.Clear()

	case journal.ConfigRevision != currentRevision:
		// The configuration moved on. The account save that goes with this
		// change succeeded, so the user has been told it worked -- spec 04
		// forbids rolling that back unconditionally. It is left for a person.
		journal.Phase = PhaseRecoveryRequired
		if err := paths.SaveJournal(journal); err != nil {
			return Outcome{}, true, err
		}
		return Outcome{Phase: PhaseRecoveryRequired}, true, domain.Errorf(
			domain.CodeRecoveryRequired,
			"无线事务 %s 属于配置版本 %d，当前是 %d；不擅自回滚一个用户可能已经被告知成功的改动",
			journal.TaskID, journal.ConfigRevision, currentRevision)

	case journal.Phase == PhaseAwaitingConfirm && now().Before(journal.ExpiresAt):
		// Still within the window. Whoever is waiting may still confirm it.
		return Outcome{Phase: journal.Phase}, true, nil
	}

	// backed_up, applied, rolling_back, or an expired awaiting_confirm: undo it.
	return rollbackAndReport(ctx, store, paths, journal, now)
}

func rollbackAndReport(ctx context.Context, store Store, paths Paths,
	journal *Journal, now func() time.Time) (Outcome, bool, error) {

	outcome, err := rollback(ctx, store, paths, journal)
	return outcome, true, err
}
