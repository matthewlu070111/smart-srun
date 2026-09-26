package config

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/strategy"
)

func openWith(t *testing.T, cfg domain.Config) (*Repository, string) {
	t.Helper()
	path := tempConfigPath(t)
	writeConfigFile(t, path, cfg)
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return repository, path
}

func stringPtr(value string) *string { return &value }
func boolPtr(value bool) *bool       { return &value }

// T11 -- the revision is what stops a stale page from undoing newer work.
func TestUpdateRequiresTheCurrentRevision(t *testing.T) {
	repository, _ := openWith(t, validStartingConfig())
	stale := repository.Revision()

	if _, err := repository.Update(stale, SetDefaultCampus("c1")); err != nil {
		t.Fatalf("the first save failed: %v", err)
	}
	if repository.Revision() != stale+1 {
		t.Fatalf("revision = %d, want %d", repository.Revision(), stale+1)
	}

	_, err := repository.Update(stale, SetDefaultCampus("c1"))
	if err == nil {
		t.Fatal("a save against a stale revision succeeded")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeConflict {
		t.Fatalf("code = %q, want Conflict", code)
	}
	// The user has to be able to see both numbers to understand what happened.
	if !containsAll(err.Error(), "0", "1") {
		t.Fatalf("the message does not name the revisions: %v", err)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		found := false
		for i := 0; i+len(part) <= len(text); i++ {
			if text[i:i+len(part)] == part {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// T11 -- two writers racing. Exactly one may win, and the loser must be told to
// re-read rather than have its write silently dropped or applied on top.
func TestConcurrentSavesLeaveExactlyOneWinner(t *testing.T) {
	repository, _ := openWith(t, validStartingConfig())
	start := repository.Revision()

	const writers = 8
	var wait sync.WaitGroup
	results := make([]error, writers)

	wait.Add(writers)
	for index := range writers {
		go func() {
			defer wait.Done()
			label := "writer"
			_, results[index] = repository.Update(start, UpsertCampus(CampusPatch{
				ID:    "c1",
				Label: &label,
			}))
		}()
	}
	wait.Wait()

	succeeded := 0
	for index, err := range results {
		switch {
		case err == nil:
			succeeded++
		default:
			if code, _ := domain.CodeOf(err); code != domain.CodeConflict {
				t.Errorf("writer %d failed with %q, want Conflict", index, code)
			}
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d writers succeeded against one revision, want exactly 1", succeeded)
	}
	if repository.Revision() != start+1 {
		t.Fatalf("revision = %d after one accepted save, want %d",
			repository.Revision(), start+1)
	}

	// And the file has to agree with memory.
	onDisk, err := LoadFile(repository.Path())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if onDisk.Revision != repository.Revision() {
		t.Fatalf("disk revision %d, memory %d", onDisk.Revision, repository.Revision())
	}
}

// A save that fails must leave the running daemon on the previous
// configuration, not on one that only ever existed in memory.
func TestAFailedCommitDoesNotPublishTheNewConfiguration(t *testing.T) {
	repository, path := openWith(t, validStartingConfig())
	before := repository.Snapshot()

	repository.hooks = &writeHooks{beforeRename: func() error {
		return errors.New("disk full")
	}}

	newLabel := "changed"
	_, err := repository.Update(repository.Revision(), UpsertCampus(CampusPatch{
		ID: "c1", Label: &newLabel,
	}))
	if err == nil {
		t.Fatal("the failed commit was reported as success")
	}

	after := repository.Snapshot()
	if after.Revision != before.Revision {
		t.Errorf("the revision moved on a failed save: %d -> %d",
			before.Revision, after.Revision)
	}
	if after.CampusAccounts[0].Label != before.CampusAccounts[0].Label {
		t.Error("the in-memory configuration was published before the disk commit")
	}

	onDisk, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if onDisk.CampusAccounts[0].Label != before.CampusAccounts[0].Label {
		t.Error("the file changed despite the failure")
	}
}

// A change that returns an error must leave nothing behind: no partial edit, no
// revision bump, no file write.
func TestARejectedChangeCommitsNothing(t *testing.T) {
	repository, path := openWith(t, validStartingConfig())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	_, err = repository.Update(repository.Revision(), func(cfg *domain.Config) error {
		cfg.CampusAccounts[0].Label = "half-applied"
		return errors.New("changed my mind")
	})
	if err == nil {
		t.Fatal("the change reported success")
	}
	if repository.Snapshot().CampusAccounts[0].Label == "half-applied" {
		t.Fatal("a rejected change was still applied to the snapshot")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("a rejected change wrote to the file")
	}
}

// A change that produces an invalid configuration is refused before anything
// reaches the disk, and every problem is reported at once.
func TestAnInvalidResultIsRefusedBeforeWriting(t *testing.T) {
	repository, path := openWith(t, validStartingConfig())
	before, _ := os.ReadFile(path)

	_, err := repository.Update(repository.Revision(), func(cfg *domain.Config) error {
		cfg.Checks.IntervalSeconds = 0
		cfg.Retry.MaxRetries = 9999
		return nil
	})
	if err == nil {
		t.Fatal("an invalid configuration was committed")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidConfig {
		t.Fatalf("code = %q, want InvalidConfig", code)
	}

	var problems *domain.Errors
	if !errors.As(err, &problems) || len(problems.Items) < 2 {
		t.Fatalf("only some problems were reported: %v", err)
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("an invalid configuration reached the file")
	}
}

// Snapshot has to hand out something the caller cannot use to reach into the
// repository -- including through the pointer inside LoginShape.
func TestSnapshotIsIndependentOfTheStoredConfiguration(t *testing.T) {
	// A declared field, so the value survives school_extra filtering and this
	// test still measures what it is about: whether the copy is deep.
	useTestStrategies(t, strategy.Strategy{ID: "deep-copy", Label: "深拷贝",
		Fields: []strategy.Field{{Key: "choices", Label: "选项", Kind: strategy.FieldMulti,
			Options: []strategy.Option{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}}}})

	start := validStartingConfig()
	enabled := true
	start.School = "deep-copy"
	start.CampusAccounts[0].Login.DoubleStack = &enabled
	start.SchoolExtra = map[string]any{"choices": []any{"a", "b"}}
	repository, _ := openWith(t, start)

	snapshot := repository.Snapshot()
	snapshot.CampusAccounts[0].Password = "stolen"
	snapshot.CampusAccounts[0].Label = "edited"
	*snapshot.CampusAccounts[0].Login.DoubleStack = false
	snapshot.SchoolExtra["choices"].([]any)[0] = "changed"
	snapshot.SchoolExtra["new"] = "added"

	stored := repository.Snapshot()
	if stored.CampusAccounts[0].Password != "pw123456" {
		t.Error("editing a snapshot changed the stored password")
	}
	if stored.CampusAccounts[0].Label != "宿舍有线" {
		t.Error("editing a snapshot changed the stored label")
	}
	if *stored.CampusAccounts[0].Login.DoubleStack != true {
		t.Error("writing through the snapshot's pointer changed the stored value")
	}
	if stored.SchoolExtra["choices"].([]any)[0] != "a" {
		t.Error("editing a snapshot's slice changed the stored one")
	}
	if _, added := stored.SchoolExtra["new"]; added {
		t.Error("adding to a snapshot's map changed the stored one")
	}
}

// The change function gets a copy too, so a change that fails halfway has not
// already edited what other readers see.
func TestAChangeCannotReachTheStoredConfigurationDirectly(t *testing.T) {
	repository, _ := openWith(t, validStartingConfig())

	var captured *domain.Config
	if _, err := repository.Update(repository.Revision(), func(cfg *domain.Config) error {
		captured = cfg
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	captured.CampusAccounts[0].Password = "stolen"
	if repository.Snapshot().CampusAccounts[0].Password != "pw123456" {
		t.Fatal("a change kept a reference into the stored configuration")
	}
}

// Reopening must produce what was saved: the file, not the defaults, is the
// source of truth after a restart.
func TestASavedConfigurationSurvivesReopening(t *testing.T) {
	repository, path := openWith(t, validStartingConfig())

	if _, err := repository.Update(repository.Revision(), UpsertCampus(CampusPatch{
		Label:      stringPtr("第二个"),
		UserID:     stringPtr("2020999999"),
		Password:   stringPtr("  spaces kept  "),
		AccessMode: stringPtr("wired"),
		WiredIface: stringPtr("wan.v2"),
	})); err != nil {
		t.Fatalf("Update: %v", err)
	}
	saved := repository.Snapshot()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.Persisted() {
		t.Error("a repository reading an existing file reported itself unpersisted")
	}
	if reopened.Revision() != saved.Revision {
		t.Fatalf("revision %d after reopen, saved %d", reopened.Revision(), saved.Revision)
	}

	after := reopened.Snapshot()
	if len(after.CampusAccounts) != 2 {
		t.Fatalf("%d accounts after reopen, want 2", len(after.CampusAccounts))
	}
	if after.CampusAccounts[1].Password != "  spaces kept  " {
		t.Errorf("password came back as %q; surrounding spaces are part of it",
			after.CampusAccounts[1].Password)
	}
}

func TestUpdateRefusesANilChange(t *testing.T) {
	repository, _ := openWith(t, validStartingConfig())
	if _, err := repository.Update(repository.Revision(), nil); err == nil {
		t.Fatal("a nil change was accepted")
	}
}
