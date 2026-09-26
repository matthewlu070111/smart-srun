package config

import (
	"errors"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// R06 -- a failure after the rename must not leave the repository behind its
// own file.
//
// The rename is the commit: from that instant every reader sees the new
// configuration. Treating a later failure as "nothing happened" left the
// running daemon serving values that no longer existed on disk, a restart
// reading different ones, and the next compare-and-swap judging against a
// revision the file had already moved past -- so the save after this one could
// silently overwrite it.
func TestAFailureAfterTheRenameKeepsTheRepositoryWithTheFile(t *testing.T) {
	path := tempConfigPath(t)
	writeConfigFile(t, path, validStartingConfig())

	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	before := repository.Revision()

	repository.hooks = &writeHooks{
		afterRename: func() error { return errors.New("injected failure") },
	}
	_, err = repository.Update(before, func(cfg *domain.Config) error {
		cfg.CampusAccounts[0].Label = "renamed"
		return nil
	})
	if err == nil {
		t.Fatal("a failure after the rename was reported as a clean save")
	}

	// The file holds the new configuration, so the repository must too.
	onDisk, loadErr := LoadFile(path)
	if loadErr != nil {
		t.Fatalf("the file is unreadable after the failure: %v", loadErr)
	}
	live := repository.Snapshot()

	if onDisk.Revision != live.Revision {
		t.Errorf("revision: disk %d, memory %d -- the two have split",
			onDisk.Revision, live.Revision)
	}
	if onDisk.CampusAccounts[0].Label != live.CampusAccounts[0].Label {
		t.Errorf("label: disk %q, memory %q -- the two have split",
			onDisk.CampusAccounts[0].Label, live.CampusAccounts[0].Label)
	}

	// And once the fault clears, the next save is judged against the revision
	// that is really current -- so it neither overwrites blindly nor refuses
	// forever. Before the fix this used the stale in-memory revision and
	// accepted a save that had not seen what the file already held.
	repository.hooks = nil
	if _, err := repository.Update(live.Revision, func(cfg *domain.Config) error {
		cfg.CampusAccounts[0].Label = "again"
		return nil
	}); err != nil {
		t.Errorf("the save after the failure was refused: %v", err)
	}
}

// A failure before the rename still means nothing happened, which is the other
// half of the same rule.
func TestAFailureBeforeTheRenameLeavesBothUntouched(t *testing.T) {
	path := tempConfigPath(t)
	writeConfigFile(t, path, validStartingConfig())

	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	before := repository.Snapshot()

	repository.hooks = &writeHooks{
		beforeRename: func() error { return errors.New("injected failure") },
	}
	if _, err := repository.Update(before.Revision, func(cfg *domain.Config) error {
		cfg.CampusAccounts[0].Label = "renamed"
		return nil
	}); err == nil {
		t.Fatal("the injected failure was not reported")
	}

	onDisk, loadErr := LoadFile(path)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if onDisk.Revision != before.Revision {
		t.Errorf("disk revision = %d, want the previous %d",
			onDisk.Revision, before.Revision)
	}
	if repository.Snapshot().Revision != before.Revision {
		t.Errorf("memory revision = %d, want the previous %d",
			repository.Snapshot().Revision, before.Revision)
	}
}
