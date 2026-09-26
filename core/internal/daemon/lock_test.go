//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "daemon.lock")
}

func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	code, ok := domain.CodeOf(err)
	if !ok {
		t.Fatalf("error carries no code: %v", err)
	}
	return code
}

// T25 -- a second copy of the daemon does not start.
//
// Two daemons on one router would each believe they owned the account state,
// each write the snapshot, and each authenticate; the gateway would see two
// logins for one account and knock one of them off.
func TestASecondDaemonCannotTakeTheLock(t *testing.T) {
	path := lockPath(t)

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	_, err = Acquire(path)
	if err == nil {
		t.Fatal("a second daemon took the lock")
	}
	if code := codeOf(t, err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}
	// The message names who to look at. A refusal that just said "already
	// running" leaves the user with nothing to check.
	if holder, ok := Holder(path); !ok || holder != os.Getpid() {
		t.Errorf("holder = %d/%v, want this process %d", holder, ok, os.Getpid())
	}
}

// The lock is released when it is released, so restarting works.
func TestReleasingTheLockLetsTheNextDaemonIn(t *testing.T) {
	path := lockPath(t)

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	defer second.Release()

	// Releasing twice is not an error: a deferred Release after an explicit one
	// is the ordinary shape of this code.
	if err := first.Release(); err != nil {
		t.Errorf("second release: %v", err)
	}
}

// T25 -- a recycled pid does not look like a running daemon.
//
// This is why the lock is a flock and not a pidfile. A router that has just
// rebooted hands out low pids quickly, so the number a crashed daemon left
// behind will belong to something else within seconds. A pidfile has to guess
// whether that process is "really" the daemon; the kernel simply knows nobody
// holds this descriptor.
func TestAStalePIDDoesNotBlockTheLock(t *testing.T) {
	path := lockPath(t)

	// A file left by a daemon that died, naming a pid that is now somebody
	// else's -- or nobody's.
	if err := os.WriteFile(path, []byte("1\n"), RuntimeFileMode); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("a stale pid blocked the lock: %v", err)
	}
	defer lock.Release()

	if holder, _ := Holder(path); holder != os.Getpid() {
		t.Errorf("holder = %d, want the new owner %d", holder, os.Getpid())
	}
}

// Garbage in the lock file is a missing diagnostic, not a reason to refuse to
// start: the file is a note about who to look at, and nothing decides from it.
func TestUnreadableLockContentsDoNotPreventStarting(t *testing.T) {
	for name, contents := range map[string]string{
		"empty":        "",
		"not a number": "hello\n",
		"negative":     "-7\n",
		"zero":         "0\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := lockPath(t)
			if err := os.WriteFile(path, []byte(contents), RuntimeFileMode); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, ok := Holder(path); ok {
				t.Error("garbage was reported as a holder")
			}
			lock, err := Acquire(path)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			lock.Release()
		})
	}
	if _, ok := Holder(filepath.Join(t.TempDir(), "missing")); ok {
		t.Error("a missing lock file reported a holder")
	}
}

// The lock file is not world-readable: it lives in the runtime directory with
// everything else the daemon owns.
func TestTheLockFileIsPrivate(t *testing.T) {
	path := lockPath(t)
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != RuntimeFileMode.Perm() {
		t.Errorf("mode = %#o, want %#o", got, RuntimeFileMode.Perm())
	}
	if got := lock.Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
}
