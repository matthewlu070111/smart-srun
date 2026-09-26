// Package packaging checks the files that ship to the device but are not Go.
//
// The init script is a contract the Go side depends on and cannot verify from
// inside itself: it decides what gets started, how it gets stopped, and what
// happens when it dies. Nothing in `go test ./internal/...` would notice it
// drifting, so it is asserted here.
package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is three levels up from core/tests/packaging.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "root", "etc", "init.d")); err != nil {
		t.Fatalf("repository root %q has no device tree: %v", root, err)
	}
	return root
}

func initScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "root", "etc", "init.d", "smart_srun")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the init script: %v", err)
	}
	return string(data)
}

// commands is the script with its comments removed.
//
// The checks below are about what the script does, not what it says about
// itself. Several of them exist because of a specific thing the Python version
// did, and the comment explaining that history names it -- which would fail a
// scan of the raw text. Splitting on an unquoted `#` is enough here because
// this script has none inside a string; if one ever appears, this helper is
// where to notice.
func commands(script string) string {
	var kept []string
	for line := range strings.SplitSeq(script, "\n") {
		if index := strings.Index(line, "#"); index >= 0 {
			line = line[:index]
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// T25 -- procd starts the Go binary, in the foreground, as a supervised
// instance.
//
// Anything that forked, wrote its own pidfile or ran a wrapper would be a
// process procd could not supervise, respawn or stop, which is most of what
// procd is for.
func TestProcdStartsTheGoDaemonDirectly(t *testing.T) {
	raw := initScript(t)
	if !strings.HasPrefix(raw, "#!/bin/sh /etc/rc.common\n") {
		t.Error("the init script does not start with OpenWrt's rc.common " +
			"interpreter line, so rc.common's START and the procd helpers are " +
			"not in scope")
	}

	script := commands(raw)
	for _, required := range []string{
		"USE_PROCD=1",
		"/usr/bin/srunnet",
		"procd_set_param command",
		`"$PROG" daemon`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("the init script does not contain %q", required)
		}
	}
	if !strings.Contains(script, "procd_open_instance") ||
		!strings.Contains(script, "procd_close_instance") {
		t.Error("the service is not declared as a procd instance")
	}
}

// T25 -- the daemon respawns without a budget that can be exhausted.
//
// The Python version used the default finite budget and the manual actions that
// restarted the service burned through it; after that the daemon stayed dead
// until the router was rebooted. `respawn <threshold> <timeout> 0` is procd's
// unlimited form.
func TestTheDaemonRespawnsWithoutAFiniteBudget(t *testing.T) {
	script := commands(initScript(t))

	var respawn string
	for line := range strings.SplitSeq(script, "\n") {
		if strings.Contains(line, "procd_set_param respawn") {
			respawn = strings.TrimSpace(line)
		}
	}
	if respawn == "" {
		t.Fatal("the service does not ask procd to respawn it at all")
	}
	fields := strings.Fields(respawn)
	if len(fields) != 5 {
		t.Fatalf("respawn = %q, want threshold, timeout and retries", respawn)
	}
	if fields[4] != "0" {
		t.Errorf("respawn retries = %q, want 0 (unlimited); a finite budget is "+
			"exhausted by ordinary restarts and then the daemon stays dead",
			fields[4])
	}
}

// T25 -- stopping this service stops this service, and does not hunt for
// processes that look like it.
//
// The Python version had to scan /proc because the daemon, the CLI and the
// update worker were all the same script; a blunt stop killed installs halfway
// through and left the update status stuck. The Go update worker runs under its
// own procd service with its own copy of the binary, so procd's own instance
// tracking is exact -- and a process hunt reintroduced here would silently
// undo that.
func TestStoppingDoesNotHuntForProcesses(t *testing.T) {
	script := commands(initScript(t))

	for _, forbidden := range []string{
		"/proc/[0-9]",
		"kill -TERM",
		"kill -KILL",
		"killall",
		"pgrep",
		"pkill",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("the init script contains %q; procd knows which process it "+
				"started, and a hunt catches the update worker too", forbidden)
		}
	}
}

// The daemon is given long enough to shut down cleanly.
//
// It cancels its work, writes its last snapshot and releases its lock on
// SIGTERM, and allows ten seconds for a worker that ignores cancellation.
// procd's default five seconds would SIGKILL a shutdown that was about to
// finish, which is how a state file ends up saying "running" for a process that
// is gone.
func TestTheDaemonIsGivenTimeToShutDown(t *testing.T) {
	script := commands(initScript(t))
	if !strings.Contains(script, "procd_set_param term_timeout") {
		t.Error("no term_timeout; procd's default is shorter than the daemon's " +
			"own shutdown grace")
	}
}

// The device package does not invoke Python from its service definition.
//
// The legacy Python runtime is retained in Git history only. The source audit
// checks the tree; this assertion checks what procd actually starts.
func TestTheServiceDefinitionInvokesNoPython(t *testing.T) {
	script := commands(initScript(t))
	for _, marker := range []string{"python", "client.py", "smart_srun/"} {
		if strings.Contains(script, marker) {
			t.Errorf("the init script mentions %q; the service procd starts is "+
				"the Go binary", marker)
		}
	}
}
