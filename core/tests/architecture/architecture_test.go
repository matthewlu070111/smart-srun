// Package architecture checks the dependency rules that keep the packages
// separable.
//
// These are asserted rather than documented because a wrong import compiles
// fine and is only noticed much later, when the package it corrupted can no
// longer be tested without a router attached.
package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/matthewlu070111/smart-srun/core"

// coreRoot is the module root, two levels up from tests/architecture.
func coreRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve core root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("core root %q has no go.mod: %v", root, err)
	}
	return root
}

// packageImports maps each package's import path to the set of paths it imports.
// Test files are excluded: a test may reach for anything it needs to build a
// fixture, and holding tests to the production dependency rules would only
// push fixtures into production code.
func packageImports(t *testing.T) map[string][]string {
	t.Helper()
	root := coreRoot(t)
	fileSet := token.NewFileSet()
	result := map[string][]string{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		importPath := modulePath
		if relative != "." {
			importPath += "/" + filepath.ToSlash(relative)
		}

		file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			if !slices.Contains(result[importPath], imported) {
				result[importPath] = append(result[importPath], imported)
			}
		}
		if _, seen := result[importPath]; !seen {
			result[importPath] = nil
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("no packages found; the walk is looking in the wrong place")
	}
	return result
}

// domain is the vocabulary every other package shares. Giving it I/O would make
// every type that mentions it untestable without the thing it reached for.
func TestDomainHasNoIO(t *testing.T) {
	// "net" is forbidden but "net/netip" is not, and the difference is the
	// point: netip is value types with no dialing, listening or resolving in
	// it, so a Binding can name an address without the package that holds it
	// gaining the ability to open a socket.
	forbidden := []string{
		"os", "os/exec", "net", "net/http", "io/fs", "database/sql",
		"os/user", "syscall", "golang.org/x/sys/unix", "bufio",
	}
	imports := packageImports(t)[modulePath+"/internal/domain"]
	if imports == nil {
		t.Fatal("domain package not found")
	}
	for _, imported := range imports {
		if slices.Contains(forbidden, imported) {
			t.Errorf("domain imports %q; it must stay pure so every type that "+
				"mentions it can be tested without that dependency", imported)
		}
		if strings.HasPrefix(imported, modulePath) {
			t.Errorf("domain imports %q; the vocabulary package depends on "+
				"nothing inside the module", imported)
		}
	}
}

// The direction that matters: the layers that make decisions may use the layers
// that hold data, never the reverse. An adapter reaching back into the
// application is how a "small exception" becomes a cycle.
func TestDependencyDirection(t *testing.T) {
	// deciders are the layers that make decisions, plus the assembly above
	// them. Nothing below them may depend on them, which is most of what the
	// table below repeats.
	deciders := []string{"internal/policy", "internal/application",
		"internal/observe", "internal/daemon", "internal/wifi",
		"internal/wireless"}
	below := func(extra ...string) []string {
		return append(append([]string{}, deciders...), extra...)
	}

	rules := map[string][]string{
		"internal/domain": below("internal/config", "internal/protocol",
			"internal/control", "internal/cli", "internal/openwrt",
			"internal/transport", "internal/auth", "internal/strategy",
			"internal/presets", "cmd"),
		// presets decodes a published document into values and decides nothing
		// else. It may speak domain and nothing above it: a catalogue reader
		// that could reach the transport would fetch on its own, and then the
		// rules about which source wins and when a cache may be replaced would
		// need a network to test.
		"internal/presets": below("internal/config", "internal/protocol",
			"internal/control", "internal/cli", "internal/openwrt",
			"internal/transport", "internal/auth", "internal/strategy", "cmd"),
		"internal/protocol": below("internal/config", "internal/control",
			"internal/cli", "internal/openwrt", "internal/transport", "cmd"),
		// transport carries bytes out of one line. It takes a Binding as a
		// value and knows nothing about where that came from, so it does not
		// depend on the adapter that produced it -- which is what lets it be
		// tested with a hand-made binding and no router.
		"internal/transport": below("internal/config", "internal/control",
			"internal/cli", "internal/openwrt", "internal/auth", "cmd"),
		// auth performs one transaction over a line somebody else chose. It
		// must not read configuration or answer RPCs, and it must not reach the
		// adapter: which line to use is decided above it.
		"internal/auth": below("internal/config", "internal/control",
			"internal/cli", "internal/openwrt", "cmd"),
		// strategy is declarative. It describes a school's parameters and
		// extension points; it does not perform I/O of any kind.
		"internal/strategy": below("internal/config", "internal/control",
			"internal/cli", "internal/openwrt", "internal/transport", "cmd"),
		// The adapter is a leaf. It reads the router and speaks domain; it does
		// not read the user's configuration or answer RPCs. An adapter that
		// reaches back into the layers that use it is how a "small exception"
		// becomes a cycle, and it would also make every one of those layers
		// need a router to test.
		"internal/openwrt": below("internal/config", "internal/protocol",
			"internal/control", "internal/cli", "cmd"),
		"internal/config": below("internal/control", "internal/cli",
			"internal/openwrt", "cmd"),
		// policy is arithmetic over domain values and nothing else. Every
		// scheduling question it answers has to be answerable without a router,
		// a socket or a configuration file, which is what lets its tests run a
		// hundred thousand failures and cross midnight in microseconds.
		"internal/policy": {"internal/config", "internal/protocol",
			"internal/control", "internal/cli", "internal/openwrt",
			"internal/transport", "internal/auth", "internal/strategy",
			"internal/application", "internal/observe", "internal/daemon",
			"internal/wifi", "internal/wireless", "cmd"},
		// wifi is the same kind of package as policy, for the other half of the
		// scheduling problem: which access point, rather than when. It decides
		// from values only, so its tests run on a machine with no wireless
		// hardware -- which is every machine this is developed on. A wifi
		// package that could scan would need a radio to test, and the guest
		// this project tests on does not have one either.
		"internal/wifi": {"internal/config", "internal/protocol",
			"internal/control", "internal/cli", "internal/openwrt",
			"internal/transport", "internal/auth", "internal/strategy",
			"internal/application", "internal/observe", "internal/daemon",
			"internal/policy", "internal/wireless", "cmd"},
		// wireless performs the one change this program makes to a device it does
		// not own. It may read the adapter and ask wifi what to join, and it may
		// not reach the layers that schedule it: a transaction that could submit
		// an action could start itself, and the global lock spec 04 requires
		// would have nothing to protect.
		"internal/wireless": below("internal/config", "internal/protocol",
			"internal/control", "internal/cli", "internal/transport",
			"internal/auth", "internal/strategy", "cmd"),
		// observe is a projection. It may name what policy decided, but it does
		// not decide anything itself and it never reaches the network -- a
		// status poll that probed would turn an idle browser tab into
		// continuous authentication traffic.
		"internal/observe": {"internal/config", "internal/protocol",
			"internal/control", "internal/cli", "internal/openwrt",
			"internal/transport", "internal/auth", "internal/application",
			"internal/daemon", "internal/wifi", "cmd"},
		// application coordinates. It may use everything below it; what it may
		// not do is reach up into the transports that call it.
		// application declares the Wireless interface it needs and does not
		// import the package that implements it, for the same reason it does not
		// import openwrt: the assembly belongs to daemon, and a coordinator that
		// could reach the transaction could not be tested without one.
		"internal/application": {"internal/control", "internal/cli",
			"internal/openwrt", "internal/wireless", "internal/daemon", "cmd"},
		"internal/control": {"internal/cli", "internal/daemon", "cmd"},
		// daemon assembles the running service. It is allowed to know about
		// everything below it -- that is its job -- and nothing about the
		// command line above it: a service that reached into the CLI could not
		// be started any other way.
		"internal/daemon": {"internal/cli", "cmd"},
		"internal/cli":    {"cmd"},
	}

	imports := packageImports(t)
	for pkg, forbidden := range rules {
		full := modulePath + "/" + pkg
		for _, imported := range imports[full] {
			for _, banned := range forbidden {
				if strings.HasPrefix(imported, modulePath+"/"+banned) {
					t.Errorf("%s imports %s; dependencies point one way only",
						pkg, imported)
				}
			}
		}
	}
}

// The protocol layer is bytes in, bytes out. Spec 04 requires timestamps and
// callback names to arrive as arguments: a package that read the clock or the
// network could only be tested against a live gateway, which is exactly what
// fixed protocol vectors exist to avoid.
func TestProtocolReadsNothing(t *testing.T) {
	forbidden := []string{
		"net", "net/http", "net/url", "os", "os/exec", "time", "math/rand",
		"math/rand/v2", "crypto/rand", "io/ioutil", "bufio",
	}

	imports := packageImports(t)
	found := false
	for pkg, imported := range imports {
		if !strings.HasPrefix(pkg, modulePath+"/internal/protocol") {
			continue
		}
		found = true
		for _, name := range imported {
			if slices.Contains(forbidden, name) {
				t.Errorf("%s imports %q; the protocol layer takes its inputs as "+
					"arguments so its output is decided entirely by them", pkg, name)
			}
		}
	}
	if !found {
		t.Fatal("no protocol package found")
	}
}

// Every system command goes through the one runner.
//
// That runner is what makes a command cancellable, bounded in output and time,
// reaped along with its children, and free of a shell. A second place that
// starts a process gets none of it, and the way that happens is somebody
// needing one more query and reaching for exec.Command because it is three
// lines. Restricting the import to the file that owns the guarantees makes the
// shortcut visible instead of easy.
func TestOnlyTheRunnerStartsProcesses(t *testing.T) {
	// command.go declares the runner; the platform files carry the syscalls it
	// needs for process groups.
	allowed := map[string]bool{
		"command.go": true, "platform_unix.go": true, "platform_other.go": true,
	}

	root := filepath.Join(coreRoot(t), "internal", "openwrt")
	fileSet := token.NewFileSet()
	found := false

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read the adapter directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		found = true
		file, err := parser.ParseFile(fileSet, filepath.Join(root, name), nil,
			parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			if (imported == "os/exec" || imported == "syscall") && !allowed[name] {
				t.Errorf("%s imports %q; system commands go through Runner, "+
					"which is what bounds their output and reaps their children",
					name, imported)
			}
		}
	}
	if !found {
		t.Fatal("no adapter sources found; the walk is looking in the wrong place")
	}
}

// Every committed fixture is read by something.
//
// A fixture nobody opens suggests coverage that does not exist: it looks like
// the case is covered when nothing asserts anything about it. These files are
// captured from real devices and can be regenerated from the raw capture, so
// the honest state is to commit the ones in use.
//
// It lives here rather than beside the tests that read them because it is an
// assertion about the repository, and the adapter's own tests are cross-
// compiled and run inside a real OpenWrt guest, where there is no source tree.
func TestEveryCommittedFixtureIsUsed(t *testing.T) {
	root := coreRoot(t)
	fixtures := filepath.Join(root, "testdata", "openwrt")
	tests := filepath.Join(root, "internal", "openwrt")

	sources := map[string]string{}
	entries, err := os.ReadDir(tests)
	if err != nil {
		t.Fatalf("read the adapter directory: %v", err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tests, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		sources[entry.Name()] = string(data)
	}
	if len(sources) == 0 {
		t.Fatal("no test sources found; this check would pass vacuously")
	}

	found := 0
	err = filepath.WalkDir(fixtures, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(fixtures, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		found++
		for _, source := range sources {
			if strings.Contains(source, relative) {
				return nil
			}
		}
		t.Errorf("testdata/openwrt/%s is committed but no test reads it", relative)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if found < 10 {
		t.Fatalf("only %d fixtures found; the walk is looking in the wrong place",
			found)
	}
}

// There is one HTTP client, and it is the bound one.
//
// The M06 card says a school strategy may not carry its own; the same applies
// to the authentication transaction. A second client would have its own pool,
// its own timeouts and -- the part that matters -- its own idea of which line
// to leave by, which is the whole thing the transport package exists to fix. A
// strategy that needed a request nobody anticipated is a reason to widen the
// interface it is given, not to open a socket.
func TestOnlyTheTransportBuildsHTTPClients(t *testing.T) {
	// The transport package is where the one client lives. Everything else
	// that could plausibly want a request of its own is listed here.
	restricted := []string{"internal/auth", "internal/strategy",
		"internal/discovery", "internal/presets", "internal/update",
		"internal/policy", "internal/observe", "internal/application",
		"internal/wifi", "internal/wireless"}
	// presets was already named here before the package existed, which is what
	// that list is for -- it names the packages that must never make their own
	// way onto the network, whether or not they have been written yet.

	// Imports that mean "I am about to make my own way onto the network".
	// net/http is allowed: a Request has to be built somewhere. net/netip is
	// values only.
	forbiddenImports := []string{"net", "crypto/tls", "net/http/httputil"}
	// And the constructions that would build a client out of net/http alone.
	forbiddenText := []string{"http.DefaultClient", "http.DefaultTransport",
		"http.Client{", "http.Transport{", "&http.Client", "&http.Transport"}

	root := coreRoot(t)
	fileSet := token.NewFileSet()
	checked := 0

	for _, relative := range restricted {
		directory := filepath.Join(root, filepath.FromSlash(relative))
		entries, err := os.ReadDir(directory)
		if err != nil {
			// The package does not exist yet. The rule applies when it does.
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
				strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(directory, name)
			checked++

			file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, spec := range file.Imports {
				imported := strings.Trim(spec.Path.Value, `"`)
				if slices.Contains(forbiddenImports, imported) {
					t.Errorf("%s/%s imports %q; the network is reached through "+
						"the bound transport, not directly", relative, name, imported)
				}
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, marker := range forbiddenText {
				if strings.Contains(string(data), marker) {
					t.Errorf("%s/%s contains %q; there is one HTTP client and "+
						"it is the one bound to the line", relative, name, marker)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no restricted packages found; this check would pass vacuously")
	}
}

// The fake clock is a test tool, and it has to stay one.
//
// It exists so scheduling can be tested without waiting: a sixty-second cap and
// a window that crosses midnight are both unreachable in a unit test on a real
// clock. What it must never become is a way for production code to control
// time, because then the thing being tested is no longer the thing that ships.
// Keeping it in its own package makes the rule checkable rather than hopeful.
func TestTheFakeClockIsTestOnly(t *testing.T) {
	fake := modulePath + "/internal/policy/faketime"

	for pkg, imports := range packageImports(t) {
		if pkg == fake {
			continue
		}
		if slices.Contains(imports, fake) {
			t.Errorf("%s imports the fake clock outside a test; production code "+
				"takes a policy.Clock and the daemon passes the real one", pkg)
		}
	}

	// And something has to use it, or the rule above passes because the package
	// is dead rather than because it is contained.
	root := coreRoot(t)
	users := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), fake) {
			users++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if users == 0 {
		t.Error("no test imports the fake clock; either it is unused or this " +
			"check is looking in the wrong place")
	}
}

// cmd wires things together and exits. Business logic there would be reachable
// only by running the binary.
func TestCommandPackageOnlyWires(t *testing.T) {
	root := coreRoot(t)
	path := filepath.Join(root, "cmd", "srunnet", "main.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	lines := strings.Count(string(data), "\n")
	if lines > 120 {
		t.Errorf("cmd/srunnet/main.go is %d lines; it should construct "+
			"dependencies and exit, not carry logic", lines)
	}
}

// Third-party dependencies are a supply-chain decision, so each one gets
// recorded deliberately rather than arriving with a convenient helper.
func TestNoUnreviewedThirdPartyDependencies(t *testing.T) {
	// Reviewed and allowed. Spec 02 permits golang.org/x/sys/unix for Linux
	// syscalls and x/net/html plus its x/text charset support for portal
	// parsing. Nothing else may appear without being added here and to the
	// dependency record.
	allowed := []string{
		"golang.org/x/sys/unix",
		"golang.org/x/net/html",
		"golang.org/x/net/html/charset",
		"golang.org/x/text/encoding",
		"golang.org/x/text/encoding/htmlindex",
		"golang.org/x/text/transform",
	}

	for pkg, imports := range packageImports(t) {
		for _, imported := range imports {
			if strings.HasPrefix(imported, modulePath) {
				continue
			}
			// A standard library path has no dot in its first element.
			first, _, _ := strings.Cut(imported, "/")
			if !strings.Contains(first, ".") {
				continue
			}
			if !slices.Contains(allowed, imported) {
				t.Errorf("%s imports the unreviewed third-party package %q",
					pkg, imported)
			}
		}
	}
}

// A shared mutable bag is how ownership stops being traceable: any package can
// write any key, and nothing can be tested in isolation afterwards.
func TestNoGlobalServiceLocator(t *testing.T) {
	root := coreRoot(t)
	banned := []string{"ServiceLocator", "CoreAPI", "GlobalContext", "AppContext"}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(data)
		for _, name := range banned {
			if strings.Contains(text, "type "+name) {
				relative, _ := filepath.Rel(root, path)
				t.Errorf("%s declares %s; dependencies are passed explicitly",
					filepath.ToSlash(relative), name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
