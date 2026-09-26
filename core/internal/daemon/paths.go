// Package daemon assembles and runs the long-running service.
//
// It is the only place that knows the service is a process: the lock that makes
// it single-instance, the socket it answers on, the snapshot a reader can see
// while it is stopped, and the helper that starts it again. Everything it
// coordinates -- configuration, scheduling, the projection -- is built
// elsewhere and handed in, so this package can be tested against a temporary
// directory rather than a router.
package daemon

import (
	"io/fs"
	"path/filepath"

	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

// Modes fixed by spec 02. The runtime directory holds a socket that accepts
// privileged commands, so it is not world-anything.
const (
	RuntimeDirMode  fs.FileMode = 0o700
	RuntimeFileMode fs.FileMode = 0o600
)

// Paths locates everything the running service owns.
//
// A value rather than a set of constants, so a test can point the whole service
// at a temporary directory. The defaults are the ones spec 02 fixes, and the
// split between them matters: runtime state lives on tmpfs and is wiped by a
// reboot, configuration lives on flash and must survive one. Putting a
// scheduling artifact in the second is how a crash leaves something behind in
// the user's settings.
type Paths struct {
	// Runtime is /var/run/smart-srun: 0700, tmpfs, not configuration.
	Runtime string
	// Config is /etc/smart-srun: 0700, flash, the user's own data.
	Config string
	// Optional resource overrides; zero values use the device contract paths.
	BuiltinPresets string
	PresetsCache   string
	Log            string
}

func DefaultPaths() Paths {
	return Paths{Runtime: "/var/run/smart-srun", Config: "/etc/smart-srun"}
}

// Socket is the local control socket. Unix domain only: spec 03 forbids
// listening on TCP, because a management port on a router is a management port
// on the internet the first time somebody opens the firewall.
func (p Paths) Socket() string { return filepath.Join(p.Runtime, "control.sock") }

// Lock is the single-instance lock file.
func (p Paths) Lock() string { return filepath.Join(p.Runtime, "daemon.lock") }

// State is the snapshot a reader can see even while the service is stopped.
func (p Paths) State() string { return filepath.Join(p.Runtime, "state.json") }

func (p Paths) QuietResume() string    { return filepath.Join(p.Runtime, "quiet-uplink.json") }
func (p Paths) ManualPauses() string   { return filepath.Join(p.Runtime, "manual-pauses.json") }
func (p Paths) PresetSchedule() string { return filepath.Join(p.Runtime, "presets-schedule.json") }

// UpdateStatus is written by the update worker alone, and read by LuCI from
// this fixed path while the main service is down.
func (p Paths) UpdateStatus() string {
	return filepath.Join(p.Runtime, "update-status.json")
}

func (p Paths) ConfigFile() string  { return filepath.Join(p.Config, "config.json") }
func (p Paths) UserPresets() string { return filepath.Join(p.Config, "user-presets.json") }

func (p Paths) PresetFile() string {
	if p.BuiltinPresets != "" {
		return p.BuiltinPresets
	}
	return "/usr/share/smart-srun/school-presets.json"
}

// LogFile is the structured event log.
//
// Under /tmp rather than Runtime, and deliberately not on flash: spec 02 puts
// the log on tmpfs because it is the one file this service writes continuously,
// and a router's flash is the part most likely to wear out.
func (p Paths) LogFile() string {
	if p.Log != "" {
		return p.Log
	}
	return "/tmp/smart-srun/plugin.log"
}

func (p Paths) PresetCacheFile() string {
	if p.PresetsCache != "" {
		return p.PresetsCache
	}
	return "/tmp/smart-srun/presets-cache.json"
}

// Recovery holds the minimal journals that must survive a reboot -- an
// interrupted wireless transaction, an interrupted install. It is under Config
// rather than Runtime for exactly that reason.
func (p Paths) Recovery() string { return filepath.Join(p.Config, "recovery") }

func (p Paths) Update() update.Paths { return update.Paths{Runtime: p.Runtime, Config: p.Config} }

// WirelessStaging is where a wireless change is built before it is published.
//
// Under Runtime, and deliberately not under Recovery: it is a scratch copy of
// /etc/config/wireless that only means anything during one transaction, and a
// reboot wiping it is the correct outcome. The journal that does have to
// survive is the other one.
func (p Paths) WirelessStaging() string {
	return filepath.Join(p.Runtime, "wireless-staging")
}
