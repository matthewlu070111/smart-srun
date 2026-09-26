package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func tempConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "smart-srun", "config.json")
}

// writeConfigFile puts a valid configuration on disk without going through the
// repository, so a test can set up a starting state it controls exactly.
func writeConfigFile(t *testing.T, path string, cfg domain.Config) {
	t.Helper()
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, FileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func validStartingConfig() domain.Config {
	cfg := Normalize(Defaults())
	cfg.CampusAccounts = []domain.CampusAccount{{
		ID: "c1", Label: "宿舍有线", UserID: "2020123456", Password: "pw123456",
		AccessMode: domain.AccessModeWired, WiredIface: "wan",
		BaseURL: "http://10.0.0.1", ACID: "1",
	}}
	cfg.Selection.ActiveCampusID = "c1"
	cfg.Selection.DefaultCampusID = "c1"
	return Normalize(cfg)
}

// T10 -- what a configuration written by this package must be.
func TestWriteAtomicProducesAReadableFileWithTheRightMode(t *testing.T) {
	path := tempConfigPath(t)
	cfg := validStartingConfig()
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := writeAtomic(path, data, nil); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}

	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("the file just written does not load: %v", err)
	}
	if len(loaded.CampusAccounts) != 1 || loaded.CampusAccounts[0].Password != "pw123456" {
		t.Fatalf("round trip lost data: %+v", loaded.CampusAccounts)
	}

	if runtime.GOOS == "windows" {
		return
	}

	// Compared against the literal, not against FileMode. Checking the file
	// against the constant that produced it is a test that cannot fail: widen
	// the constant and the expectation widens with it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v, want 0600; this file holds campus passwords "+
			"and wireless keys", info.Mode().Perm())
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("config directory mode %v, want 0700", dirInfo.Mode().Perm())
	}

	if FileMode != 0o600 || DirMode != 0o700 {
		t.Errorf("the declared modes are %v/%v, want 0600/0700", FileMode, DirMode)
	}
}

// T10 -- the fault injection that matters. A failure at any step must leave the
// previous configuration complete on disk, because the alternative is a router
// that comes back with no credentials after a bad flash write.
func TestAFailureAtAnyStepLeavesThePreviousConfigurationIntact(t *testing.T) {
	injected := errors.New("injected failure")

	steps := []struct {
		name  string
		build func(*writeHooks)
		// afterRename says the injected failure happens once the new bytes are
		// already what a reader sees.
		afterRename bool
	}{
		{"the write itself fails", func(h *writeHooks) { h.afterWrite = func() error { return injected } }, false},
		{"the flush to disk fails", func(h *writeHooks) { h.afterSync = func() error { return injected } }, false},
		{"the process stops before the rename", func(h *writeHooks) {
			h.beforeRename = func() error { return injected }
		}, false},
		{"the directory sync fails after the rename", func(h *writeHooks) {
			h.afterRename = func() error { return injected }
		}, true},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			path := tempConfigPath(t)
			previous := validStartingConfig()
			previous.Revision = 7
			writeConfigFile(t, path, previous)

			next := validStartingConfig()
			next.Revision = 8
			next.CampusAccounts[0].Password = "a-new-password"
			data, err := Marshal(next)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			hooks := &writeHooks{}
			step.build(hooks)
			state, writeErr := writeAtomic(path, data, hooks)
			if writeErr == nil {
				t.Fatal("the injected failure was not reported")
			}
			// The commit state has to match where the failure was injected.
			// Before the rename nothing is visible; after it, everything is,
			// and saying otherwise is what let the repository disagree with its
			// own file.
			if state.visible() != step.afterRename {
				t.Errorf("commit visible = %v, want %v for a failure at %s",
					state.visible(), step.afterRename, step.name)
			}

			// Whatever is on disk has to be a whole configuration -- never a
			// truncated one, and never the defaults.
			loaded, err := LoadFile(path)
			if err != nil {
				t.Fatalf("the file on disk no longer loads: %v", err)
			}
			if len(loaded.CampusAccounts) != 1 {
				t.Fatalf("the account is gone: %+v", loaded)
			}

			// Before the rename it must still be the old configuration. After
			// it, the new one -- the commit point is the rename, not the sync
			// that follows it.
			password := loaded.CampusAccounts[0].Password
			revision := loaded.Revision
			if step.name == "the directory sync fails after the rename" {
				if password != "a-new-password" || revision != 8 {
					t.Fatalf("after the rename the new configuration should be "+
						"visible, got revision %d", revision)
				}
			} else if password != "pw123456" || revision != 7 {
				t.Fatalf("the previous configuration was damaged: revision %d",
					revision)
			}

			assertNoLeftoverTempFiles(t, filepath.Dir(path))
		})
	}
}

// A failed save that leaves a temporary file behind fills the flash of a device
// that has very little of it.
func assertNoLeftoverTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config-") {
			t.Errorf("left a temporary file behind: %s", entry.Name())
		}
	}
}

// The commit point is the rename. Until it happens the target must still be the
// old file, not a partially written new one.
func TestTheTargetIsNeverAPartiallyWrittenFile(t *testing.T) {
	path := tempConfigPath(t)
	previous := validStartingConfig()
	writeConfigFile(t, path, previous)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	huge := validStartingConfig()
	huge.CampusAccounts[0].Label = strings.Repeat("x", 100)
	data, err := Marshal(huge)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	hooks := &writeHooks{beforeRename: func() error {
		// At this moment the whole new document has been written and flushed,
		// but nothing points at it yet.
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Errorf("read during write: %v", readErr)
		}
		if string(current) != string(before) {
			t.Error("the target changed before the rename")
		}
		return errors.New("stop here")
	}}

	if _, err := writeAtomic(path, data, hooks); err == nil {
		t.Fatal("expected the injected failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("the target was modified by a write that failed")
	}
}

// T07/T10 -- a file that cannot be loaded is reported and left alone. It still
// holds the settings the user would otherwise have to remember.
func TestLoadingABadFileNeverTouchesIt(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"truncated", `{"schema_version":2,"enabled":`},
		{"not JSON", `nonsense`},
		{"a 1.x configuration", `{"backoff_enable":"1","quiet_hours_enabled":"1"}`},
		{"an unknown key", `{"schema_version":2,"enabled":true,"mystery":1}`},
		{"an invalid value", `{"schema_version":2,"checks":{"interval_seconds":0}}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := tempConfigPath(t)
			if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(testCase.content), FileMode); err != nil {
				t.Fatalf("write: %v", err)
			}

			if _, err := LoadFile(path); err == nil {
				t.Fatal("a bad configuration loaded successfully")
			}
			if _, err := Open(path); err == nil {
				t.Fatal("Open accepted a bad configuration")
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(after) != testCase.content {
				t.Fatalf("the file was modified:\n got %s\nwant %s", after, testCase.content)
			}
		})
	}
}

// A 1.x file has its own message, because "unknown key" would send the user
// looking for a typo that is not there.
func TestALegacyFileIsNamedAsSuch(t *testing.T) {
	path := tempConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"enabled":"1","quiet_hours_enabled":"1","interval":"60"}`
	if err := os.WriteFile(path, []byte(legacy), FileMode); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Open(path)
	if !errors.Is(err, ErrLegacyConfig) {
		t.Fatalf("err = %v, want ErrLegacyConfig", err)
	}
	if !strings.Contains(err.Error(), "1.x") {
		t.Fatalf("the message does not say what the file is: %v", err)
	}
}

// A missing file is not a failure: a fresh install has none, and the daemon has
// to start anyway with defaults it has not yet saved.
func TestAMissingFileStartsFromDefaultsWithoutWritingOne(t *testing.T) {
	path := tempConfigPath(t)

	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if repository.Persisted() {
		t.Error("a repository with no file reported itself as persisted")
	}
	if repository.Revision() != 0 {
		t.Errorf("revision = %d, want 0", repository.Revision())
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Open created the file; nothing should be written until a save")
	}

	snapshot := repository.Snapshot()
	if err := Validate(snapshot); err != nil {
		t.Fatalf("the starting defaults do not validate: %v", err)
	}
}

// Two saves of the same configuration must produce the same bytes, or every
// backup diff is noise.
func TestMarshalIsDeterministic(t *testing.T) {
	cfg := validStartingConfig()
	cfg.SchoolExtra = map[string]any{
		"zebra": "last", "alpha": "first", "middle": true,
	}

	first, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for range 20 {
		again, err := Marshal(cfg)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatal("two marshals of the same configuration differ")
		}
	}

	// And it has to survive the strict decoder, which is what actually reads it
	// back on the next start.
	if _, err := Parse(first); err != nil {
		t.Fatalf("what Marshal writes, Parse rejects: %v", err)
	}
}
