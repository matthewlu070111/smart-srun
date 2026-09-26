package openwrt

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Tool is a system program this adapter runs.
type Tool string

const (
	ToolUCI    Tool = "uci"
	ToolUbus   Tool = "ubus"
	ToolIwinfo Tool = "iwinfo"
	ToolWifi   Tool = "wifi"
	ToolOpkg   Tool = "opkg"
	ToolAPK    Tool = "apk"
)

// probedTools is what Detect looks for. Keeping it a list rather than
// discovering on demand means a status page can say what is missing before the
// user tries the feature that needs it.
var probedTools = []Tool{ToolUCI, ToolUbus, ToolIwinfo, ToolWifi, ToolOpkg, ToolAPK}

// UbusObject is a service registered on the ubus bus.
//
// A binary on disk and an object on the bus are different capabilities, and
// this program needs the second one. An OpenWrt 24.10 image built without the
// iwinfo command still answers `ubus call iwinfo devices`, because the object
// comes from a library that netifd and rpcd link, not from the command. Asking
// whether the command exists would report wireless observation as unavailable
// on a system where it works.
type UbusObject string

const (
	// ObjectIwinfo answers the wireless observation calls this program makes.
	ObjectIwinfo UbusObject = "iwinfo"
	// ObjectNetworkWireless is netifd's view of the configured radios.
	ObjectNetworkWireless UbusObject = "network.wireless"
	// ObjectUCI is rpcd's uci plugin. Not used yet; not probed.
	ObjectUCI UbusObject = "uci"
)

var probedObjects = []UbusObject{ObjectIwinfo, ObjectNetworkWireless}

// PackageManager identifies which one actually manages packages here.
type PackageManager string

const (
	PackageManagerNone PackageManager = ""
	PackageManagerOpkg PackageManager = "opkg"
	PackageManagerAPK  PackageManager = "apk"
)

// Capabilities is what this firmware can actually do.
type Capabilities struct {
	// tools maps a tool to its absolute path. Absent means absent.
	tools map[Tool]string

	// objects is what is registered on the ubus bus.
	objects map[UbusObject]struct{}

	// PackageManager is decided by asking the binaries, never by reading the
	// firmware version. Kwrt calls itself 25.12-SNAPSHOT and ships opkg, while
	// official 25.12 ships apk; a release string is a label, and installing
	// with the wrong tool is not a recoverable mistake.
	PackageManager PackageManager

	// PackageArchitectures are the package architectures this system accepts,
	// most specific first. `all` is present but last: it is valid for a
	// LuCI-only file package and never for the compiled core.
	PackageArchitectures []string
}

// Has reports whether a tool is present.
func (c Capabilities) Has(tool Tool) bool {
	_, ok := c.tools[tool]
	return ok
}

// Path returns a tool's absolute path.
func (c Capabilities) Path(tool Tool) (string, bool) {
	path, ok := c.tools[tool]
	return path, ok
}

// HasUbusObject reports whether a service is registered on the bus.
func (c Capabilities) HasUbusObject(object UbusObject) bool {
	_, ok := c.objects[object]
	return ok
}

// RequireUbusObject reports a missing bus service as UnsupportedCapability.
//
// This is the check that belongs in front of a wireless observation, not a
// check for the iwinfo command: the calls go over ubus.
func (c Capabilities) RequireUbusObject(objects ...UbusObject) error {
	if !c.Has(ToolUbus) {
		return domain.Errorf(domain.CodeUnsupportedCapability,
			"系统缺少 ubus，该功能在此固件上不可用")
	}
	for _, object := range objects {
		if !c.HasUbusObject(object) {
			return domain.Errorf(domain.CodeUnsupportedCapability,
				"系统未提供 ubus 服务 %s，该功能在此固件上不可用", object)
		}
	}
	return nil
}

// Deliberately absent: a method listing everything that was found. The
// capabilities.get RPC asks Has and HasUbusObject for a fixed list instead, so
// the wire shape is decided by that handler rather than by whatever this scan
// happened to discover.

// Require reports the first missing tool as UnsupportedCapability.
//
// This is the difference between "the scan found nothing" and "this firmware
// has no iwinfo, so scanning is not available here". The second is not a
// failure to retry, and telling the user to try again is telling them to wait
// for something that will not happen.
func (c Capabilities) Require(tools ...Tool) error {
	for _, tool := range tools {
		if !c.Has(tool) {
			return domain.Errorf(domain.CodeUnsupportedCapability,
				"系统缺少 %s，该功能在此固件上不可用", tool)
		}
	}
	return nil
}

// Detect probes the system.
//
// Presence is not capability: a binary that exists but cannot answer a trivial
// query is not a working package manager, and spec 09 requires the real
// architecture list rather than one derived from the CPU. So the package
// manager has to answer a question before it counts.
func Detect(ctx context.Context, runner commandRunner) Capabilities {
	capabilities := Capabilities{
		tools:   map[Tool]string{},
		objects: map[UbusObject]struct{}{},
	}
	for _, tool := range probedTools {
		if path, err := runner.Resolve(string(tool)); err == nil {
			capabilities.tools[tool] = path
		}
	}

	if capabilities.Has(ToolUbus) {
		for _, object := range listUbusObjects(ctx, runner) {
			capabilities.objects[object] = struct{}{}
		}
	}

	if capabilities.Has(ToolOpkg) {
		if architectures, err := opkgArchitectures(ctx, runner); err == nil {
			capabilities.PackageManager = PackageManagerOpkg
			capabilities.PackageArchitectures = architectures
		}
	}
	if capabilities.PackageManager == PackageManagerNone && capabilities.Has(ToolAPK) {
		if _, err := runner.Run(ctx, string(ToolAPK), "--version"); err == nil {
			capabilities.PackageManager = PackageManagerAPK
			capabilities.PackageArchitectures = apkArchitectures(ctx, runner)
		}
	}
	return capabilities
}

func apkArchitectures(ctx context.Context, runner commandRunner) []string {
	result, err := runner.Run(ctx, string(ToolAPK), "--print-arch")
	if err != nil || result.StdoutTruncated {
		return nil
	}
	fields := strings.Fields(string(result.Stdout))
	if len(fields) != 1 || len(fields[0]) > 128 || fields[0] == "all" || fields[0] == "noarch" {
		return nil
	}
	for _, c := range fields[0] {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '_' && c != '-' && c != '.' {
			return nil
		}
	}
	return []string{fields[0], "noarch"}
}

// listUbusObjects reads `ubus list`, one object name per line.
//
// Only the objects this program actually calls are recorded. Keeping every name
// would put the whole bus -- which includes per-interface and per-radio objects
// naming the user's networks -- into a structure that a status response or a
// log line might later render.
func listUbusObjects(ctx context.Context, runner commandRunner) []UbusObject {
	result, err := runner.Run(ctx, string(ToolUbus), "list")
	if err != nil {
		return nil
	}
	wanted := make(map[string]UbusObject, len(probedObjects))
	for _, object := range probedObjects {
		wanted[string(object)] = object
	}

	var found []UbusObject
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		if object, ok := wanted[strings.TrimSpace(line)]; ok {
			found = append(found, object)
		}
	}
	return found
}

// opkgArchitectures reads `opkg print-architecture`.
//
// The output is one "arch <name> <priority>" line per accepted architecture,
// and the priority is what orders them -- higher is more specific. Sorting by
// it rather than trusting the print order is what keeps aarch64_cortex-a53
// ahead of `all` when a release happens to list them the other way round.
func opkgArchitectures(ctx context.Context, runner commandRunner) ([]string, error) {
	result, err := runner.Run(ctx, string(ToolOpkg), "print-architecture")
	if err != nil {
		return nil, err
	}

	type entry struct {
		name     string
		priority int
		order    int
	}
	var entries []entry
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "arch" {
			continue
		}
		priority, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		entries = append(entries, entry{fields[1], priority, len(entries)})
	}
	if len(entries) == 0 {
		return nil, domain.Errorf(domain.CodeUnsupportedCapability,
			"opkg 没有报告任何可用的软件包架构")
	}

	slices.SortStableFunc(entries, func(a, b entry) int {
		if a.priority != b.priority {
			return b.priority - a.priority
		}
		return a.order - b.order
	})
	out := make([]string, len(entries))
	for index, item := range entries {
		out[index] = item.name
	}
	return out, nil
}
