package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
)

// The locations are a contract, not an implementation detail.
//
// LuCI reads the update status from a fixed path while the daemon is down, the
// init script and the packaging install into these directories, and a keep.d
// entry preserves the configuration across a sysupgrade. A typo in any of them
// would be invisible until an upgrade quietly stopped preserving a user's
// settings.
func TestTheRuntimeAndConfigPathsAreTheOnesTheContractFixes(t *testing.T) {
	paths := DefaultPaths()

	cases := map[string]string{
		"runtime":         paths.Runtime,
		"socket":          paths.Socket(),
		"lock":            paths.Lock(),
		"state":           paths.State(),
		"update status":   paths.UpdateStatus(),
		"config dir":      paths.Config,
		"config file":     paths.ConfigFile(),
		"user presets":    paths.UserPresets(),
		"recovery":        paths.Recovery(),
		"builtin presets": paths.PresetFile(),
		"preset cache":    paths.PresetCacheFile(),
	}
	want := map[string]string{
		"runtime":         "/var/run/smart-srun",
		"socket":          "/var/run/smart-srun/control.sock",
		"lock":            "/var/run/smart-srun/daemon.lock",
		"state":           "/var/run/smart-srun/state.json",
		"update status":   "/var/run/smart-srun/update-status.json",
		"config dir":      "/etc/smart-srun",
		"config file":     "/etc/smart-srun/config.json",
		"user presets":    "/etc/smart-srun/user-presets.json",
		"recovery":        "/etc/smart-srun/recovery",
		"builtin presets": "/usr/share/smart-srun/school-presets.json",
		"preset cache":    "/tmp/smart-srun/presets-cache.json",
	}
	for name, got := range cases {
		if got != filepath.FromSlash(want[name]) {
			t.Errorf("%s = %q, want %q", name, got, want[name])
		}
	}

	// The control package fixes the socket location too, and the two must not
	// drift: the daemon listens on one and every client dials the other.
	if paths.Socket() != filepath.FromSlash(control.SocketPath) {
		t.Errorf("the daemon listens on %q while clients dial %q",
			paths.Socket(), control.SocketPath)
	}

	// Runtime state is not configuration. Spec 02 keeps them on different
	// filesystems on purpose: one is wiped by a reboot, the other must survive
	// one, and a scheduling artifact left in the second is a crash that changed
	// a user's settings.
	if paths.Runtime == paths.Config {
		t.Error("runtime state and configuration share a directory")
	}
}

// A runtime directory an older version left open is tightened rather than
// inherited. The socket inside it accepts privileged commands, and MkdirAll
// does nothing at all when the path already exists.
func TestTheRuntimeDirectoryIsTightenedIfItWasLeftOpen(t *testing.T) {
	paths := tempPaths(t)
	if err := os.MkdirAll(paths.Runtime, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// MkdirAll applies the umask, so set the loose mode explicitly.
	if err := os.Chmod(paths.Runtime, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := EnsureRuntimeDir(paths); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	info, err := os.Stat(paths.Runtime)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != RuntimeDirMode.Perm() {
		t.Errorf("mode = %#o, want %#o", got, RuntimeDirMode.Perm())
	}

	// Creating it fresh gives the same answer whatever the umask was.
	fresh := tempPaths(t)
	if err := EnsureRuntimeDir(fresh); err != nil {
		t.Fatalf("ensure fresh: %v", err)
	}
	info, err = os.Stat(fresh.Runtime)
	if err != nil {
		t.Fatalf("stat fresh: %v", err)
	}
	if got := info.Mode().Perm(); got != RuntimeDirMode.Perm() {
		t.Errorf("fresh mode = %#o, want %#o", got, RuntimeDirMode.Perm())
	}
}

// Something in the way is reported rather than worked around.
func TestARuntimePathThatIsNotADirectoryIsRefused(t *testing.T) {
	paths := tempPaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.Runtime), RuntimeDirMode); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(paths.Runtime, []byte("not a directory"),
		RuntimeFileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := EnsureRuntimeDir(paths); err == nil {
		t.Fatal("a file standing in for the runtime directory was accepted")
	}
}

// Wireless accounts share one scheduling key, and wired accounts get one per
// interface.
//
// Spec 04 gives wireless a single global transaction: two accounts cannot both
// be re-associating a radio, so they must not run at once. Wired accounts on
// different interfaces are genuinely independent and must. The keys are
// prefixed so an interface literally named "wireless" cannot collide with the
// wireless one.
func TestWirelessAccountsShareOneLineAndWiredOnesDoNot(t *testing.T) {
	paths := tempPaths(t)
	if err := os.MkdirAll(paths.Config, RuntimeDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"schema_version":2,
	  "campus_accounts":[
	    {"id":"w1","user_id":"u1","password":"p","operator_suffix":"",
	     "access_mode":"wifi","ssid":"campus","encryption":"none"},
	    {"id":"w2","user_id":"u2","password":"p","operator_suffix":"",
	     "access_mode":"wifi","ssid":"campus","encryption":"none"},
	    {"id":"e1","user_id":"u3","password":"p","operator_suffix":"",
	     "access_mode":"wired","wired_iface":"wan"},
	    {"id":"e2","user_id":"u4","password":"p","operator_suffix":"",
	     "access_mode":"wired","wired_iface":"wan2"}],
	  "selection":{"active_campus_id":"e1","default_campus_id":"e1"}}`
	if err := os.WriteFile(paths.ConfigFile(), []byte(seed), RuntimeFileMode); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	repository, err := config.Open(paths.ConfigFile())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	service := &Daemon{config: repository}

	line := func(id string) string {
		return service.lineOf(requestFor(id))
	}
	if line("w1") != line("w2") {
		t.Errorf("two wireless accounts got different lines %q and %q; they "+
			"cannot both hold the radio", line("w1"), line("w2"))
	}
	if line("e1") == line("e2") {
		t.Errorf("two wired accounts on different interfaces share line %q",
			line("e1"))
	}
	if line("e1") == line("w1") {
		t.Error("a wired account shares a line with a wireless one")
	}
	// An account that is gone gets a key of its own, so it cannot serialise
	// against an unrelated one on its way to failing.
	if line("gone") == line("e1") || line("gone") == line("w1") {
		t.Errorf("a missing account collided with a real line: %q", line("gone"))
	}
	if line("gone") == line("also-gone") {
		t.Error("two different missing accounts share a line")
	}
}
