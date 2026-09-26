package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
)

// requestFor is a minimal action request naming an account.
func requestFor(accountID string) application.Request {
	return application.Request{Kind: application.KindLogin,
		AccountID: accountID, IdempotencyKey: "k-" + accountID}
}

func tempPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	return Paths{
		Runtime: filepath.Join(root, "run"),
		Config:  filepath.Join(root, "etc"),
		// The log defaults to a fixed path under /tmp. A test that did not
		// override it would write into the machine's real log, and two tests
		// running in parallel would write into each other's.
		Log: filepath.Join(root, "log", "plugin.log"),
	}
}

func TestASnapshotRoundTrips(t *testing.T) {
	paths := tempPaths(t)
	written := Snapshot{
		Service:        ServiceRunning,
		PID:            4242,
		Enabled:        true,
		ConfigRevision: 9,
		Version:        "2.0.0rc1",
		Accounts: []observe.AccountView{{
			AccountID: "campus", Link: domain.LinkReady,
			Auth: domain.AuthVerifiedSelf,
			Note: &observe.Note{ActionID: "a1", Message: "认证完成"},
		}},
		Actions: []ActionView{{ID: "a1", Kind: "manual_login", State: "succeeded"}},
	}
	if err := WriteSnapshot(paths, written); err != nil {
		t.Fatalf("write: %v", err)
	}

	read, err := ReadSnapshot(paths)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Service != ServiceRunning || read.PID != 4242 || !read.Enabled {
		t.Errorf("read = %+v", read)
	}
	if read.ConfigRevision != 9 || read.Version != "2.0.0rc1" {
		t.Errorf("read = %+v", read)
	}
	if len(read.Accounts) != 1 || read.Accounts[0].Note == nil ||
		read.Accounts[0].Note.Message != "认证完成" {
		t.Errorf("accounts = %+v", read.Accounts)
	}
	if len(read.Actions) != 1 || read.Actions[0].ID != "a1" {
		t.Errorf("actions = %+v", read.Actions)
	}
	if read.WrittenAt.IsZero() {
		t.Error("the snapshot carries no timestamp, so a reader cannot tell " +
			"a fresh one from one left over from before a reboot")
	}
}

// The file is private and lands whole.
//
// LuCI polls it every few seconds while the daemon is stopped; a reader that
// could catch it half-written would eventually catch it half-written.
func TestTheSnapshotIsPrivateAndReplacedWhole(t *testing.T) {
	paths := tempPaths(t)
	if err := WriteSnapshot(paths, Snapshot{Service: ServiceRunning}); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(paths.State())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != RuntimeFileMode.Perm() {
		t.Errorf("mode = %#o, want %#o", got, RuntimeFileMode.Perm())
	}
	dir, err := os.Stat(paths.Runtime)
	if err != nil {
		t.Fatalf("stat runtime: %v", err)
	}
	if got := dir.Mode().Perm(); got != RuntimeDirMode.Perm() {
		t.Errorf("runtime dir mode = %#o, want %#o", got, RuntimeDirMode.Perm())
	}

	// Rewriting leaves no temporary file behind. One that stayed would
	// accumulate on tmpfs on a device with a few megabytes of it.
	for range 5 {
		if err := WriteSnapshot(paths, Snapshot{Service: ServiceRunning}); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
	}
	entries, err := os.ReadDir(paths.Runtime)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("left a temporary file behind: %s", entry.Name())
		}
	}
}

// A service that has never run since the last reboot has no file, and that is
// an answer rather than a fault.
func TestAMissingSnapshotReadsAsStopped(t *testing.T) {
	paths := tempPaths(t)
	snapshot, err := ReadSnapshot(paths)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if snapshot.Service != ServiceStopped {
		t.Errorf("service = %q, want stopped", snapshot.Service)
	}
}

// Everything else about the file is refused rather than guessed at. It lives in
// /var/run, which is writable by root and by anything that has already got
// there; a reader that trusted it would be trusting that.
func TestABadSnapshotIsRefused(t *testing.T) {
	cases := map[string]string{
		"not json":       "{",
		"wrong schema":   `{"schema_version":99,"service":"running"}`,
		"missing schema": `{"service":"running"}`,
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			paths := tempPaths(t)
			if err := os.MkdirAll(paths.Runtime, RuntimeDirMode); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(paths.State(), []byte(contents),
				RuntimeFileMode); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := ReadSnapshot(paths); err == nil {
				t.Fatal("a malformed snapshot was accepted")
			}
		})
	}

	// Exactly one byte over, and valid JSON.
	//
	// This is the only size at which the limit check is what refuses the file,
	// and getting there took two attempts. Padding with spaces does not work:
	// the decoder rejects whitespace on its own. Nor does "very large": the
	// read stops at limit+1, so anything bigger arrives truncated and fails to
	// parse whatever the limit says. At exactly limit+1 the document is intact
	// and would be accepted -- which is precisely what reading one byte past
	// the limit exists to catch.
	t.Run("one byte over the limit and well-formed", func(t *testing.T) {
		paths := tempPaths(t)
		if err := os.MkdirAll(paths.Runtime, RuntimeDirMode); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		prefix := `{"schema_version":1,"service":"stopped","pad":"`
		suffix := `"}`
		document := prefix +
			strings.Repeat("x", MaxSnapshotBytes+1-len(prefix)-len(suffix)) +
			suffix
		if len(document) != MaxSnapshotBytes+1 {
			t.Fatalf("the fixture is %d bytes; it has to be exactly %d",
				len(document), MaxSnapshotBytes+1)
		}
		var check Snapshot
		if err := json.Unmarshal([]byte(document), &check); err != nil {
			t.Fatalf("the fixture is not valid JSON, so this would pass for "+
				"the wrong reason: %v", err)
		}

		if err := os.WriteFile(paths.State(), []byte(document),
			RuntimeFileMode); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := ReadSnapshot(paths); err == nil {
			t.Fatal("a snapshot one byte over the limit was accepted; a reader " +
				"that trusted the length of a file in /var/run would be " +
				"trusting whatever last wrote there")
		}
	})

	// And a genuinely huge one is not read whole on the way to refusing it.
	t.Run("far over the limit", func(t *testing.T) {
		paths := tempPaths(t)
		if err := os.MkdirAll(paths.Runtime, RuntimeDirMode); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		document := `{"schema_version":1,"service":"stopped","pad":"` +
			strings.Repeat("x", 4*MaxSnapshotBytes) + `"}`
		if err := os.WriteFile(paths.State(), []byte(document),
			RuntimeFileMode); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := ReadSnapshot(paths); err == nil {
			t.Fatal("a four-megabyte snapshot was accepted")
		}
	})
}

// A write that cannot land leaves nothing behind.
//
// The happy path never exercises this: a successful rename moves the temporary
// file, so there is nothing to clean up. Only a failure between creating it and
// renaming it can leave one, and on a tmpfs those accumulate.
func TestAFailedWriteLeavesNoTemporaryFile(t *testing.T) {
	paths := tempPaths(t)
	if err := os.MkdirAll(paths.Runtime, RuntimeDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A directory where the state file should be: the rename cannot succeed.
	if err := os.MkdirAll(paths.State(), RuntimeDirMode); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	if err := WriteSnapshot(paths, Snapshot{Service: ServiceRunning}); err == nil {
		t.Fatal("a write that could not land reported success")
	}

	entries, err := os.ReadDir(paths.Runtime)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a failed write left %s behind", entry.Name())
		}
	}
}

// T25 -- recording a stop does not touch the automatic-authentication switch.
//
// The switch is the user's, it lives in the configuration, and a force stop is
// not a change of mind about wanting to log in. What the stopped record keeps
// is what the running daemon last published.
func TestMarkingStoppedPreservesTheUsersSwitch(t *testing.T) {
	paths := tempPaths(t)
	if err := WriteSnapshot(paths, Snapshot{
		Service: ServiceRunning, PID: 100, Enabled: true, ConfigRevision: 12,
		Version: "2.0.0rc1",
		Actions: []ActionView{{ID: "a1", State: "running"}},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := MarkStopped(paths, true, 12, "2.0.0rc1"); err != nil {
		t.Fatalf("mark stopped: %v", err)
	}
	after, err := ReadSnapshot(paths)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if after.Service != ServiceStopped {
		t.Errorf("service = %q", after.Service)
	}
	if !after.Enabled {
		t.Error("stopping the service switched automatic authentication off")
	}
	if after.ConfigRevision != 12 {
		t.Errorf("revision = %d, want it preserved", after.ConfigRevision)
	}
	if after.PID != 0 {
		t.Errorf("pid = %d, want none: nothing is running", after.PID)
	}
	if len(after.Actions) != 0 {
		t.Errorf("actions = %+v, want none: they belonged to a process that "+
			"has gone", after.Actions)
	}
}

// An empty projection serialises as an empty list, not null. A reader iterating
// it should not have to special-case one shape of "nothing".
func TestEmptyCollectionsAreListsNotNull(t *testing.T) {
	paths := tempPaths(t)
	if err := WriteSnapshot(paths, Snapshot{Service: ServiceStopped}); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(paths.State())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"accounts", "actions"} {
		if string(raw[key]) != "[]" {
			t.Errorf("%s = %s, want []", key, raw[key])
		}
	}
}

// The wire shape of an action is this package's, not the coordinator's, so a
// field added to the scheduler does not silently change what LuCI receives.
func TestActionViewCarriesWhatAReaderNeeds(t *testing.T) {
	ended := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	view := ViewOf(application.Action{
		ID: "a7", State: application.StateFailed, Phase: application.PhaseLogin,
		Message: "密码错误", Code: domain.CodeAuthRejected,
		Request: application.Request{Kind: application.KindLogin,
			AccountID: "campus", IdempotencyKey: "click-1"},
		EndedAt: ended,
	})

	if view.ID != "a7" || view.Kind != "manual_login" || view.AccountID != "campus" {
		t.Errorf("view = %+v", view)
	}
	if view.State != "failed" || view.Code != domain.CodeAuthRejected {
		t.Errorf("view = %+v", view)
	}
	if view.EndedAt == nil || !view.EndedAt.Equal(ended) {
		t.Errorf("ended = %v", view.EndedAt)
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The idempotency key is a caller's own identifier and has no business on a
	// status page that anybody with the UI open can read.
	if strings.Contains(string(encoded), "click-1") {
		t.Errorf("the view carries the caller's key: %s", encoded)
	}

	running := ViewOf(application.Action{ID: "a8", State: application.StateRunning})
	if running.EndedAt != nil {
		t.Error("an action that has not ended carries an end time")
	}
}
