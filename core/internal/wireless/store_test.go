package wireless

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
)

// fakeUCI stands in for the uci binary, over real files.
//
// It is an emulation and is worth being plain about: it implements the five
// sub-commands this store uses, in terms of the -c and -t directories it is
// given, and nothing else. What it is not is evidence that the real uci behaves
// this way -- that is what the acceptance run on the OpenWrt guest is for, and
// this package's card says so.
//
// What it does buy is the half the store actually owns: which flags go where,
// that the candidate is built somewhere the system does not read, that the live
// file is compared before it is replaced, and that the replacement is atomic
// and keeps its mode. All of that happens on the real filesystem here.
//
// The files it keeps are in `uci show` format rather than /etc/config syntax.
// The store treats the package file as opaque bytes -- it copies it, compares
// it and publishes it, and never parses it -- so the format is the test's to
// choose, and choosing the one ParseUCIShow already reads means no second
// parser exists only for tests.
type fakeUCI struct {
	t *testing.T
	// calls is every argv, so a test can assert on what was not run as well as
	// on what was.
	calls  [][]string
	inputs []string
	// failOn makes one sub-command fail, to drive the error paths.
	failOn string
	// missing makes `show` and `changes` report no such package.
	missing bool
	// truncate makes the output arrive incomplete, as it does when a tool
	// produces more than the runner will hold.
	truncate bool
	// reloads counts the network service restarts.
	reloads     int
	ignoreBatch bool
}

func (f *fakeUCI) RunInput(ctx context.Context, program, input string, args ...string) (openwrt.Result, error) {
	f.calls = append(f.calls, append([]string{program}, args...))
	f.inputs = append(f.inputs, input)
	if f.ignoreBatch {
		return openwrt.Result{}, nil
	}
	count := len(f.calls)
	defer func() { f.calls = f.calls[:count] }() // Emulation below is not a process argv.
	flags, rest := splitFlags(args)
	if len(rest) != 1 || rest[0] != "batch" {
		f.t.Fatal("unexpected stdin command")
	}
	for _, line := range splitLines(input) {
		command, argument, _ := strings.Cut(line, " ")
		if command == "set" {
			name, _, _ := strings.Cut(argument, "=")
			pkg, rest, _ := strings.Cut(name, ".")
			sectionName, option, _ := strings.Cut(rest, ".")
			prefix := ""
			if option != "" {
				prefix = pkg + "." + sectionName + "=wifi-iface\n"
			}
			parsed, err := openwrt.ParseUCIShow(pkg, []byte(prefix+argument+"\n"))
			if err != nil {
				f.t.Fatal(err)
			}
			section, _ := parsed.Section(sectionName)
			value := section.Type
			if option != "" {
				value = section.Get(option)
			}
			argument = name + "=" + value
		}
		if _, err := f.Run(ctx, program, "-q", "-c", flags["-c"], "-t", flags["-t"], command, argument); err != nil {
			return openwrt.Result{}, err
		}
	}
	return openwrt.Result{}, nil
}

func (f *fakeUCI) Run(_ context.Context, program string, args ...string) (
	openwrt.Result, error) {

	f.calls = append(f.calls, append([]string{program}, args...))
	if program == networkInit {
		f.reloads++
		if f.failOn == "reload" {
			return openwrt.Result{}, &openwrt.ExitError{Program: program, Code: 1}
		}
		return openwrt.Result{}, nil
	}
	if program != "uci" {
		f.t.Fatalf("the store ran %q, which is not one of its tools", program)
	}

	flags, rest := splitFlags(args)
	if len(rest) == 0 {
		f.t.Fatalf("uci was run with no sub-command: %v", args)
	}
	command := rest[0]
	if f.failOn == command {
		return openwrt.Result{}, &openwrt.ExitError{Program: "uci", Code: 2}
	}

	config, delta := flags["-c"], flags["-t"]
	switch command {
	case "show", "export":
		if f.missing {
			return openwrt.Result{}, &openwrt.ExitError{Program: "uci", Code: 1}
		}
		data, err := os.ReadFile(filepath.Join(config, rest[1]))
		if err != nil {
			return openwrt.Result{}, &openwrt.ExitError{Program: "uci", Code: 1}
		}
		if command == "export" {
			parsed, err := openwrt.ParseUCIShow(rest[1], data)
			if err != nil {
				return openwrt.Result{}, err
			}
			var output strings.Builder
			output.WriteString("package " + rest[1] + "\n")
			for _, section := range parsed.Sections() {
				output.WriteString("config " + section.Type + " '" + section.Name + "'\n")
				for _, name := range section.OptionNames() {
					value, _ := section.Lookup(name)
					items, kind := []string{value.Text}, "option"
					if value.IsList {
						items, kind = value.List, "list"
					}
					for _, item := range items {
						quoted, err := quoteBatch(item)
						if err != nil {
							return openwrt.Result{}, err
						}
						output.WriteString(kind + " " + name + " " + quoted + "\n")
					}
				}
			}
			data = []byte(output.String())
		}
		return openwrt.Result{Stdout: data, StdoutTruncated: f.truncate}, nil

	case "changes":
		if f.missing {
			return openwrt.Result{}, &openwrt.ExitError{Program: "uci", Code: 1}
		}
		data, _ := os.ReadFile(filepath.Join(delta, rest[1]))
		return openwrt.Result{Stdout: data, StdoutTruncated: f.truncate}, nil

	case "set", "delete":
		line := rest[1] + "\n"
		pkg, name, _ := strings.Cut(rest[1], ".")
		if command == "delete" {
			// uci exits 1 for a delete it cannot find, quiet flag or not.
			// Measured on a real device; the fake said "fine" and so hid the
			// fact that every open network's plan -- which deletes `key` from
			// a section that may never have had one -- would have failed.
			if !f.holds(config, delta, pkg, name) {
				return openwrt.Result{}, &openwrt.ExitError{Program: "uci", Code: 1}
			}
			line = "-" + rest[1] + "\n"
		}
		f.append(filepath.Join(delta, pkg), line)
		return openwrt.Result{}, nil

	case "commit":
		f.commit(config, delta, rest[1])
		return openwrt.Result{}, nil
	}
	f.t.Fatalf("uci was run with an unexpected sub-command %q", command)
	return openwrt.Result{}, nil
}

// splitFlags separates the -c/-t pairs and the bare -q from the sub-command.
func splitFlags(args []string) (map[string]string, []string) {
	flags := map[string]string{}
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-c", "-t":
			flags[args[index]] = args[index+1]
			index++
		case "-q", "-n":
		default:
			return flags, args[index:]
		}
	}
	return flags, nil
}

// holds reports whether the package currently has a section or option, taking
// the pending delta into account the way uci does.
func (f *fakeUCI) holds(config, delta, pkg, name string) bool {
	live, err := os.ReadFile(filepath.Join(config, pkg))
	if err != nil {
		return false
	}
	lines := splitLines(string(live))
	staged, _ := os.ReadFile(filepath.Join(delta, pkg))
	for _, change := range splitLines(string(staged)) {
		if key, _, isSet := strings.Cut(change, "="); isSet {
			lines = replaceOrAppend(lines, key, change)
		} else {
			removed := strings.TrimPrefix(change, "-")
			lines = slices.DeleteFunc(lines, func(line string) bool {
				key, _, _ := strings.Cut(line, "=")
				return key == removed || strings.HasPrefix(key, removed+".")
			})
		}
	}
	for _, line := range lines {
		if key, _, _ := strings.Cut(line, "="); key == pkg+"."+name {
			return true
		}
	}
	return false
}

func (f *fakeUCI) append(path, line string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatalf("delta directory: %v", err)
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		f.t.Fatalf("delta: %v", err)
	}
	defer handle.Close()
	if _, err := handle.WriteString(line); err != nil {
		f.t.Fatalf("delta: %v", err)
	}
}

// commit folds the delta into the package file and empties it, which is what
// uci commit does.
//
// Empties rather than removes: measured on a real device, the delta file is
// still there afterwards, truncated to zero bytes. It makes no difference to
// the store -- Stage clears the directory and PendingChanges reads no changes
// out of an empty file either way -- but a fake that removed it would be
// asserting something about uci that is not true.
func (f *fakeUCI) commit(config, delta, pkg string) {
	f.t.Helper()
	staged, err := os.ReadFile(filepath.Join(delta, pkg))
	if err != nil {
		return
	}
	live, err := os.ReadFile(filepath.Join(config, pkg))
	if err != nil {
		f.t.Fatalf("commit onto a package that is not there: %v", err)
	}

	lines := splitLines(string(live))
	for _, change := range splitLines(string(staged)) {
		name, _, isSet := strings.Cut(change, "=")
		if isSet {
			lines = replaceOrAppend(lines, name, change)
			continue
		}
		name = strings.TrimPrefix(change, "-")
		lines = slices.DeleteFunc(lines, func(line string) bool {
			key, _, _ := strings.Cut(line, "=")
			// Deleting a section takes its options with it, which is what uci
			// does and what makes rolling back a section this transaction
			// created a single operation.
			return key == name || strings.HasPrefix(key, name+".")
		})
	}

	text := strings.Join(lines, "\n")
	if text != "" {
		text += "\n"
	}
	if err := os.WriteFile(filepath.Join(config, pkg), []byte(text), 0o600); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(delta, pkg), nil, 0o600); err != nil {
		f.t.Fatalf("clear the delta: %v", err)
	}
}

func splitLines(text string) []string {
	trimmed := strings.TrimSuffix(text, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func replaceOrAppend(lines []string, name, line string) []string {
	for index, existing := range lines {
		if key, _, _ := strings.Cut(existing, "="); key == name {
			lines[index] = line
			return lines
		}
	}
	return append(lines, line)
}

func (f *fakeUCI) ran(command string) bool {
	for _, input := range f.inputs {
		for _, line := range splitLines(input) {
			if strings.HasPrefix(line, command+" ") {
				return true
			}
		}
	}
	for _, call := range f.calls {
		if slices.Contains(call, command) {
			return true
		}
	}
	return false
}

// liveWireless is one campus station and the household access point beside it,
// which is the shape every one of these tests needs.
const liveWireless = `wireless.ap0=wifi-iface
wireless.ap0.ssid='HomeNet'
wireless.ap0.key='home-secret'
wireless.sta0=wifi-iface
wireless.sta0.ssid='old-network'
wireless.sta0.encryption='none'
`

// storeFixture is a store over a temporary /etc/config, a temporary delta and a
// temporary staging directory.
type storeFixture struct {
	store  *UCIStore
	uci    *fakeUCI
	config string
	delta  string
	live   string
}

func newFixture(t *testing.T) *storeFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &storeFixture{
		uci:    &fakeUCI{t: t},
		config: filepath.Join(root, "config"),
		delta:  filepath.Join(root, "delta"),
	}
	fixture.live = filepath.Join(fixture.config, "wireless")
	for _, dir := range []string{fixture.config, fixture.delta} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	if err := os.WriteFile(fixture.live, []byte(liveWireless), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	store, err := NewUCIStore(fixture.uci, StoreOptions{
		Staging:   filepath.Join(root, "staging"),
		ConfigDir: fixture.config,
		DeltaDir:  fixture.delta,
	})
	if err != nil {
		t.Fatalf("NewUCIStore: %v", err)
	}
	fixture.store = store
	return fixture
}

func (f *storeFixture) liveText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.live)
	if err != nil {
		t.Fatalf("read the live configuration: %v", err)
	}
	return string(data)
}

var campusChanges = []Change{
	{Key: ssid, Text: "jxnu_stu"},
	{Key: enc, Text: "psk2"},
	{Key: key, Text: passphrase},
}

func TestBatchSecretsNeverEnterProcessArguments(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatal(err)
	}
	for _, args := range f.uci.calls {
		if strings.Contains(strings.Join(args, " "), passphrase) {
			t.Fatal("secret in process arguments")
		}
	}
	if !strings.Contains(strings.Join(f.uci.inputs, ""), passphrase) {
		t.Fatal("secret never reached stdin")
	}
}

func TestFailedReplacementInvalidatesPreviousCandidate(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Stage(t.Context(), "wireless", []Change{{Key: key, Text: "bad\ninput"}}); err == nil {
		t.Fatal("accepted newline")
	}
	if err := f.store.Commit(t.Context(), "wireless"); err == nil {
		t.Fatal("published stale candidate")
	}
	if f.liveText(t) != liveWireless {
		t.Fatal("changed live configuration")
	}
}

func TestSuccessfulBatchExitWithoutAppliedChangesIsRefused(t *testing.T) {
	f := newFixture(t)
	f.uci.ignoreBatch = true
	if err := f.store.Stage(t.Context(), "wireless", campusChanges); err == nil {
		t.Fatal("trusted exit status without effect")
	}
	if err := f.store.Commit(t.Context(), "wireless"); err == nil {
		t.Fatal("published incomplete candidate")
	}
}

// Nothing the system reads has moved when the candidate is ready.
//
// This is what makes every failure before the publish harmless: spec 04 builds
// the candidate "在独立UCI目录", and a store that staged into the system's own
// delta would leave a half-written change for the next person who runs `uci
// commit` -- under this program's name and without its journal.
func TestTheCandidateIsBuiltWithoutTouchingTheLiveConfiguration(t *testing.T) {
	fixture := newFixture(t)

	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if got := fixture.liveText(t); got != liveWireless {
		t.Errorf("the live configuration changed during staging:\n%s", got)
	}
	if entries, _ := os.ReadDir(fixture.delta); len(entries) != 0 {
		t.Errorf("the system delta has %d entries; staging used it", len(entries))
	}
}

func TestApplyingPublishesTheCandidate(t *testing.T) {
	fixture := newFixture(t)

	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	live := fixture.liveText(t)
	for _, want := range []string{
		"wireless.sta0.ssid=jxnu_stu",
		"wireless.sta0.encryption=psk2",
		"wireless.sta0.key=" + passphrase,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("the published configuration is missing %q:\n%s", want, live)
		}
	}
	// And the household's own access point is exactly as it was.
	if !strings.Contains(live, "wireless.ap0.ssid='HomeNet'") {
		t.Errorf("the home access point was changed:\n%s", live)
	}
}

// A configuration somebody else changed between the copy and the publish is not
// overwritten by a file that never contained their change.
func TestAConfigurationThatMovedUnderneathIsNotOverwritten(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	elsewhere := liveWireless + "wireless.ap0.channel='11'\n"
	if err := os.WriteFile(fixture.live, []byte(elsewhere), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := fixture.store.Commit(t.Context(), "wireless")
	if code, _ := domain.CodeOf(err); code != domain.CodeConflict {
		t.Fatalf("Commit = %v (code %s), want a conflict", err, code)
	}
	if got := fixture.liveText(t); got != elsewhere {
		t.Errorf("the other change was overwritten:\n%s", got)
	}
}

// Committing what was never staged is this program calling itself out of order,
// and it must not publish whatever the staging directory happens to hold.
//
// The empty case is the one that matters and the one an "err != nil" assertion
// misses. A store that skipped the check would fall through to comparing the
// live file against the nil it never recorded -- which differs from a real
// configuration, so it would still refuse, for the wrong reason -- but matches
// an empty one, and then publishes a candidate from an abandoned run. So the
// reason is asserted, and the empty file is exercised.
func TestCommitWithoutStageIsRefused(t *testing.T) {
	for name, live := range map[string]string{
		"a configured radio": liveWireless,
		"an empty file":      "",
	} {
		fixture := newFixture(t)
		if err := os.WriteFile(fixture.live, []byte(live), 0o600); err != nil {
			t.Fatalf("%s: write: %v", name, err)
		}

		// A candidate from an earlier, abandoned attempt.
		if err := os.MkdirAll(fixture.store.staging, 0o700); err != nil {
			t.Fatalf("%s: staging: %v", name, err)
		}
		stale := filepath.Join(fixture.store.staging, "wireless")
		if err := os.WriteFile(stale, []byte("wireless.sta0.ssid=stale\n"), 0o600); err != nil {
			t.Fatalf("%s: staging: %v", name, err)
		}

		err := fixture.store.Commit(t.Context(), "wireless")
		if code, _ := domain.CodeOf(err); code != domain.CodeInternal {
			t.Errorf("%s: Commit = %v (code %s), want Internal -- refused because "+
				"nothing was staged, not because the file moved", name, err, code)
		}
		if got := fixture.liveText(t); got != live {
			t.Errorf("%s: a stale candidate was published:\n%s", name, got)
		}
	}
}

// A Commit that published nothing can be tried again.
//
// The candidate belongs to the Stage that built it, not to the first Commit
// that looked at it. A refusal leaves the live file exactly as Stage found it,
// so the next call is a retry of one operation rather than a second one -- and
// a store that forgot the candidate on the way out would answer "nothing was
// staged" to a caller whose staging is sitting right there, which sends
// somebody looking for the wrong problem.
//
// Driven through the conflict refusal because that one is reachable: the live
// file moves, Commit refuses, the file moves back, and the same candidate
// publishes. replaceFile failing is the other half of the same rule and cannot
// be reached without giving the store a filesystem seam.
func TestACommitThatPublishedNothingCanBeRetried(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	// Somebody else edits the package between the staging and the publish.
	elsewhere := liveWireless + "wireless.ap0.channel='11'\n"
	if err := os.WriteFile(fixture.live, []byte(elsewhere), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := fixture.store.Commit(t.Context(), "wireless")
	if code, _ := domain.CodeOf(err); code != domain.CodeConflict {
		t.Fatalf("Commit = %v (code %s), want a conflict", err, code)
	}

	// They put it back. The candidate is still the right one to publish.
	if err := os.WriteFile(fixture.live, []byte(liveWireless), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("the retry was refused: %v", err)
	}
	if live := fixture.liveText(t); !strings.Contains(live, "wireless.sta0.ssid=jxnu_stu") {
		t.Errorf("the retry published nothing:\n%s", live)
	}
}

// And once it has published, the candidate is spent.
//
// The pair matters: a store that never dropped it would let a candidate built
// against one state be published again much later against another, which is
// the thing the before/after comparison exists to prevent.
func TestAPublishedCandidateIsNotPublishedTwice(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	err := fixture.store.Commit(t.Context(), "wireless")
	if code, _ := domain.CodeOf(err); code != domain.CodeInternal {
		t.Fatalf("the second Commit = %v (code %s), want Internal", err, code)
	}
}

// A delta left by an interrupted attempt is not replayed into this one.
//
// uci commit applies everything in the delta directory, not everything this
// transaction wrote, so the leftovers of a previous run would ride along --
// options that are in no journal and that the rollback therefore cannot undo.
func TestALeftoverDeltaIsNotReplayed(t *testing.T) {
	fixture := newFixture(t)
	delta := filepath.Join(fixture.store.staging, "delta")
	if err := os.MkdirAll(delta, 0o700); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if err := os.WriteFile(filepath.Join(delta, "wireless"),
		[]byte("wireless.ap0.ssid=hijacked\n"), 0o600); err != nil {
		t.Fatalf("staging: %v", err)
	}

	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if live := fixture.liveText(t); strings.Contains(live, "hijacked") {
		t.Errorf("a leftover change was applied:\n%s", live)
	}
}

// The live file keeps the mode it had.
//
// Asserted both ways round on purpose: a store that forced 0600 would pass a
// test that only checked a 0600 file, and would still be changing something it
// was not asked to change on a system that ships 0644.
func TestThePublishedFileKeepsTheModeItHad(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o644} {
		fixture := newFixture(t)
		if err := os.Chmod(fixture.live, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}

		if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		info, err := os.Stat(fixture.live)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != mode {
			t.Errorf("mode = %04o, want %04o", got, mode)
		}
	}
}

// An option that is not there, and one uci shows as empty, both read as absent.
//
// The second half is measured rather than assumed. A hand-written
// `option blank ”` is not listed by `uci show`, not returned by `uci get`, and
// `uci delete` on it answers "Entry not found" -- uci has no empty option at
// all. Reporting one as present would put a value in the journal that no later
// uci call could restore or remove, and the rollback would then read the
// absence uci caused as somebody else's edit.
func TestReadReportsAnEmptyOptionAsAbsent(t *testing.T) {
	fixture := newFixture(t)
	if err := os.WriteFile(fixture.live,
		[]byte(liveWireless+"wireless.sta0.bssid=''\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	bssid := Key{Section: "sta0", Option: "bssid"}
	values, err := fixture.store.Read(t.Context(), "wireless", []Key{ssid, key, bssid})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if want := (Value{Text: "old-network", Present: true}); values[ssid] != want {
		t.Errorf("ssid = %+v, want %+v", values[ssid], want)
	}
	if values[key].Present {
		t.Errorf("an option that is not in the file was reported present: %+v", values[key])
	}
	if values[bssid].Present {
		t.Errorf("bssid = %+v; an option uci shows as empty is one uci cannot act on",
			values[bssid])
	}
}

// A change that writes the empty string is refused rather than silently doing
// nothing.
//
// `uci set x.y.z=` writes no option at all. Accepted, it would leave the
// journal saying the option holds "" while the option is absent -- and the
// rollback reads that absence as somebody else's removal and reports a
// conflict, for an option nobody touched. An open network is exactly this
// case: no passphrase means key="".
func TestWritingAnEmptyValueIsRefused(t *testing.T) {
	fixture := newFixture(t)
	open := []Change{{Key: ssid, Text: "jxnu_open"}, {Key: key, Text: ""}}

	err := fixture.store.Stage(t.Context(), "wireless", open)
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
		t.Fatalf("Stage = %v (code %s), want InvalidArgument", err, code)
	}
	if !strings.Contains(err.Error(), "sta0.key") {
		t.Errorf("the refusal does not say which option: %v", err)
	}
	if fixture.uci.ran("set") {
		t.Error("options were staged before the empty one was noticed")
	}

	// And the transaction refuses to start at all, so no journal is written
	// against a plan that could not be undone.
	plan := campusPlan()
	plan.Changes = open
	if _, err := Begin(t.Context(), fixture.store, paths(t), plan,
		fixedClock(epoch)); err == nil {
		t.Error("Begin accepted a plan with an unwritable value")
	}
}

// A list is not a string, and reading one as its first item would record a
// "before" value that cannot restore what was there.
func TestReadPreservesAListOption(t *testing.T) {
	fixture := newFixture(t)
	if err := os.WriteFile(fixture.live,
		[]byte(liveWireless+"wireless.sta0.ifname='wlan0' 'wlan1'\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	values, err := fixture.store.Read(t.Context(), "wireless",
		[]Key{{Section: "sta0", Option: "ifname"}})
	if err != nil {
		t.Fatal(err)
	}
	value := values[Key{Section: "sta0", Option: "ifname"}]
	if !value.IsList || value.Text != `["wlan0","wlan1"]` {
		t.Fatal("lost list members or type")
	}
}

// A name that is not a uci name never reaches a command line.
//
// uci's own names are letters, digits and underscores. A section called
// "sta0.key" is not something uci produced; it is a value that arrived from
// somewhere it should not have, and spliced into `wireless.<section>.<option>`
// it would address a different option than the one asked for.
func TestAnImpossibleNameIsRefusedBeforeAnythingRuns(t *testing.T) {
	forged := []Key{
		{Section: "sta0.key", Option: "ssid"},
		{Section: "sta0", Option: "ssid='x' wireless.ap0.ssid"},
		{Section: "", Option: "ssid"},
	}
	for _, bad := range forged {
		fixture := newFixture(t)
		err := fixture.store.Stage(t.Context(), "wireless",
			[]Change{{Key: bad, Text: "jxnu_stu"}})
		if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
			t.Errorf("%+v: Stage = %v (code %s), want InvalidArgument", bad, err, code)
		}
		if fixture.uci.ran("set") {
			t.Errorf("%+v: a forged name reached a uci command line", bad)
		}
	}
}

// The refusal names the keys and not the values.
//
// A pending change is somebody's half-finished edit of the wireless page, so
// its value can be the passphrase they just typed -- and this list exists to be
// put in front of a user as the reason the change was refused.
func TestPendingChangesReportsKeysWithoutValues(t *testing.T) {
	fixture := newFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.delta, "wireless"),
		[]byte("wireless.ap0.key='home-secret'\n-wireless.ap0.bssid\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	pending, err := fixture.store.PendingChanges(t.Context(), "wireless")
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want two entries", pending)
	}
	for _, entry := range pending {
		if strings.Contains(entry, "home-secret") {
			t.Errorf("a pending value was returned to be displayed: %q", entry)
		}
	}
	if !strings.Contains(pending[0], "wireless.ap0.key") {
		t.Errorf("pending[0] = %q, does not name the key", pending[0])
	}
}

// A router with no radio has no wireless configuration at all, and that needs a
// different answer from "the uci command failed".
func TestNoWirelessConfigurationIsNotFoundRatherThanAFailure(t *testing.T) {
	fixture := newFixture(t)
	fixture.uci.missing = true

	_, readErr := fixture.store.Read(t.Context(), "wireless", []Key{ssid})
	_, pendingErr := fixture.store.PendingChanges(t.Context(), "wireless")
	for name, err := range map[string]error{"Read": readErr, "PendingChanges": pendingErr} {
		if code, _ := domain.CodeOf(err); code != domain.CodeNotFound {
			t.Errorf("%s = %v (code %s), want NotFound", name, err, code)
		}
	}
}

func TestReloadRunsTheNetworkService(t *testing.T) {
	fixture := newFixture(t)

	if err := fixture.store.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if fixture.uci.reloads != 1 {
		t.Errorf("the network service was reloaded %d times, want 1", fixture.uci.reloads)
	}

	fixture.uci.failOn = "reload"
	if err := fixture.store.Reload(t.Context()); err == nil {
		t.Error("a failed reload was reported as success")
	}
}

// Staging that fails part-way leaves the live configuration alone -- and leaves
// nothing behind that a later commit would publish.
func TestAFailureWhileStagingPublishesNothing(t *testing.T) {
	fixture := newFixture(t)
	fixture.uci.failOn = "commit"

	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err == nil {
		t.Fatal("Stage reported success after uci refused")
	}
	if got := fixture.liveText(t); got != liveWireless {
		t.Errorf("the live configuration changed:\n%s", got)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err == nil {
		t.Error("Commit published a candidate whose staging failed")
	}
}

// Neither the UCI command line nor the failure report carries the value.
func TestAStagingFailureDoesNotQuoteThePassphrase(t *testing.T) {
	fixture := newFixture(t)
	fixture.uci.failOn = "set"

	err := fixture.store.Stage(t.Context(), "wireless",
		[]Change{{Key: key, Text: passphrase}})
	if err == nil {
		t.Fatal("Stage reported success after uci refused")
	}
	if strings.Contains(err.Error(), passphrase) {
		t.Errorf("the failure quotes the passphrase: %v", err)
	}
	if !strings.Contains(err.Error(), "sta0.key") {
		t.Errorf("the failure does not say which option: %v", err)
	}
}

// A store with nowhere private to stage is refused at construction, not at the
// first wireless change a user asks for.
func TestAStoreWithoutAStagingDirectoryIsRefused(t *testing.T) {
	for name, build := range map[string]func() (*UCIStore, error){
		"no staging directory": func() (*UCIStore, error) {
			return NewUCIStore(&fakeUCI{t: t}, StoreOptions{})
		},
		"no runner": func() (*UCIStore, error) {
			return NewUCIStore(nil, StoreOptions{Staging: t.TempDir()})
		},
	} {
		store, err := build()
		if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
			t.Errorf("%s: err = %v (code %s), want InvalidArgument", name, err, code)
		}
		if store != nil {
			t.Errorf("%s: a store was returned anyway", name)
		}
	}
}

// The defaults are uci's own, so a daemon that names only its staging directory
// still reads and writes the system configuration.
func TestTheDefaultDirectoriesAreTheSystemOnes(t *testing.T) {
	store, err := NewUCIStore(&fakeUCI{t: t}, StoreOptions{Staging: t.TempDir()})
	if err != nil {
		t.Fatalf("NewUCIStore: %v", err)
	}
	if store.configDir != "/etc/config" || store.deltaDir != "/tmp/.uci" {
		t.Errorf("directories = %q and %q, want uci's own defaults",
			store.configDir, store.deltaDir)
	}
}

// And the whole transaction runs over this store, not just over the fake one
// the state machine's own tests use.
func TestATransactionOverTheRealStoreAppliesAndRollsBack(t *testing.T) {
	fixture := newFixture(t)
	where := paths(t)
	plan := campusPlan()

	transaction, err := Begin(t.Context(), fixture.store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if live := fixture.liveText(t); !strings.Contains(live, "wireless.sta0.ssid=jxnu_stu") {
		t.Fatalf("the change was not applied:\n%s", live)
	}

	outcome, err := transaction.Rollback(t.Context())
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Fatalf("phase = %s, want rolled back", outcome.Phase)
	}

	live := fixture.liveText(t)
	if !strings.Contains(live, "wireless.sta0.ssid=old-network") {
		t.Errorf("the previous name was not restored:\n%s", live)
	}
	// key was absent before, so undoing it means the option is gone rather than
	// present and empty.
	if strings.Contains(live, "wireless.sta0.key") {
		t.Errorf("an option that was not there before was left behind:\n%s", live)
	}
}

// Every uci invocation says where it is reading and writing.
//
// Leaving either flag off means uci falls back to /etc/config and /tmp/.uci --
// which is right on a router and wrong everywhere else, and is the difference
// between a test that exercises the store and one that edits the machine it is
// running on.
func TestEveryInvocationNamesItsDirectories(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.store.Read(t.Context(), "wireless", []Key{ssid}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := fixture.store.PendingChanges(t.Context(), "wireless"); err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if len(fixture.uci.calls) == 0 {
		t.Fatal("nothing ran")
	}
	for _, call := range fixture.uci.calls {
		if call[0] != "uci" {
			continue
		}
		for _, flag := range []string{"-c", "-t"} {
			if !slices.Contains(call, flag) {
				t.Errorf("%v: no %s, so uci would use the system default", call, flag)
			}
		}
	}
}

// A package name that is not a uci name is refused by every entry point.
//
// Checked on all four rather than on one, because they are four separate
// checks in the source and a table that only exercised Read would let any of
// the other three splice a name straight onto a command line.
func TestEveryEntryPointChecksThePackageName(t *testing.T) {
	fixture := newFixture(t)
	forged := "wireless.ap0"

	_, readErr := fixture.store.Read(t.Context(), forged, []Key{ssid})
	stageErr := fixture.store.Stage(t.Context(), forged, campusChanges)
	commitErr := fixture.store.Commit(t.Context(), forged)
	_, pendingErr := fixture.store.PendingChanges(t.Context(), forged)

	for name, err := range map[string]error{
		"Read": readErr, "Stage": stageErr,
		"Commit": commitErr, "PendingChanges": pendingErr,
	} {
		if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
			t.Errorf("%s = %v (code %s), want InvalidArgument", name, err, code)
		}
	}
	if len(fixture.uci.calls) != 0 {
		t.Errorf("a forged package name reached %d command lines", len(fixture.uci.calls))
	}
}

// Staging a package that is not on the system says so, rather than staging an
// empty file and publishing it over nothing.
func TestStagingAPackageThatIsNotThereIsNotFound(t *testing.T) {
	fixture := newFixture(t)
	if err := os.Remove(fixture.live); err != nil {
		t.Fatalf("remove: %v", err)
	}

	err := fixture.store.Stage(t.Context(), "wireless", campusChanges)
	if code, _ := domain.CodeOf(err); code != domain.CodeNotFound {
		t.Fatalf("Stage = %v (code %s), want NotFound", err, code)
	}
	if fixture.uci.ran("set") {
		t.Error("options were staged against a package that does not exist")
	}
}

// A staging directory that cannot be made is reported, not worked around.
func TestAStagingDirectoryThatCannotBeCreatedStopsTheChange(t *testing.T) {
	fixture := newFixture(t)
	// A file where the directory has to go. Blunter than a permission bit and
	// it behaves the same whether or not the tests run as root -- which they do
	// in some of the containers this is built in, where 0000 stops nothing.
	if err := os.WriteFile(fixture.store.staging, []byte("in the way"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err == nil {
		t.Fatal("Stage reported success with nowhere to stage")
	}
	if got := fixture.liveText(t); got != liveWireless {
		t.Errorf("the live configuration changed anyway:\n%s", got)
	}
}

// A candidate that vanished between staging and publishing is a failure, not an
// empty file to write over the live one.
func TestACandidateThatVanishedIsNotPublishedAsNothing(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.Stage(t.Context(), "wireless", campusChanges); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.store.staging, "wireless")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if err := fixture.store.Commit(t.Context(), "wireless"); err == nil {
		t.Fatal("Commit reported success with no candidate")
	}
	if got := fixture.liveText(t); got != liveWireless {
		t.Errorf("the live configuration was replaced:\n%s", got)
	}
}

// Output uci could not finish is not a shorter answer.
//
// Both of these decide whether this program may touch the wireless
// configuration at all: a truncated `show` loses the option whose previous
// value the rollback needs, and a truncated `changes` turns somebody else's
// half-finished edit into permission to proceed.
func TestTruncatedOutputIsRefusedRatherThanRead(t *testing.T) {
	fixture := newFixture(t)
	fixture.uci.truncate = true

	_, readErr := fixture.store.Read(t.Context(), "wireless", []Key{ssid})
	_, pendingErr := fixture.store.PendingChanges(t.Context(), "wireless")
	for name, err := range map[string]error{"Read": readErr, "PendingChanges": pendingErr} {
		if code, _ := domain.CodeOf(err); code != domain.CodeInternal {
			t.Errorf("%s = %v (code %s), want Internal", name, err, code)
		}
	}
}

// Nothing to stage is a caller mistake, not an empty transaction that commits.
func TestStagingNothingIsRefused(t *testing.T) {
	fixture := newFixture(t)

	err := fixture.store.Stage(t.Context(), "wireless", nil)
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
		t.Fatalf("Stage = %v (code %s), want InvalidArgument", err, code)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err == nil {
		t.Error("Commit published something after an empty staging")
	}
}

// A candidate identical to the live file is not written over it.
//
// Not an optimisation: publishing is a rename, so it gives the file a new
// inode, and anything watching /etc/config for changes -- netifd's own reload,
// a backup, the next transaction's "has this moved underneath me" -- sees a
// change that did not happen.
//
// Identity rather than timestamps, because a rename and the stat around it can
// land inside one filesystem timestamp tick and then prove nothing.
func TestAnIdenticalCandidateIsNotRepublished(t *testing.T) {
	fixture := newFixture(t)
	// A change that sets the options to what they already are.
	unchanged := []Change{{Key: ssid, Text: "old-network"}}
	if err := fixture.store.Stage(t.Context(), "wireless", unchanged); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	before, err := os.Stat(fixture.live)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	after, err := os.Stat(fixture.live)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Errorf("an identical candidate was written anyway; the live file was replaced")
	}
	if got := fixture.liveText(t); got != liveWireless {
		t.Errorf("the configuration changed:\n%s", got)
	}
}

// A section this program has to create is part of the change, and comes back
// out again.
//
// uci will not set an option in a section that does not exist, and the client
// section this manages usually does not: it is the thing being created. So the
// creation is in the plan, in the journal, and in the rollback -- an undo that
// left an empty section behind would leave netifd a client with no network to
// join.
func TestASectionCanBeCreatedAndUndone(t *testing.T) {
	fixture := newFixture(t)
	where := paths(t)
	station := Key{Section: "jxnu_sta_radio1"}
	plan := Plan{
		TaskID: "task-9", Package: "wireless", ConfigRevision: 3,
		ConfirmWithin: 15 * time.Minute,
		Changes: []Change{
			{Key: station, Text: "wifi-iface"},
			{Key: Key{Section: station.Section, Option: "ssid"}, Text: "jxnu_stu"},
			{Key: Key{Section: station.Section, Option: "mode"}, Text: "sta"},
		},
	}

	transaction, err := Begin(t.Context(), fixture.store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := transaction.Apply(t.Context(), plan.Changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	live := fixture.liveText(t)
	for _, want := range []string{
		"wireless.jxnu_sta_radio1=wifi-iface",
		"wireless.jxnu_sta_radio1.ssid=jxnu_stu",
	} {
		if !strings.Contains(live, want) {
			t.Fatalf("missing %q after the change:\n%s", want, live)
		}
	}

	outcome, err := transaction.Rollback(t.Context())
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if outcome.Phase != PhaseRolledBack {
		t.Fatalf("phase = %s, want rolled back", outcome.Phase)
	}
	if got := fixture.liveText(t); strings.Contains(got, "jxnu_sta_radio1") {
		t.Errorf("the section this transaction created survived its undo:\n%s", got)
	}
}

// The section is created before its options are set, and removed after they
// are cleared.
//
// A plan listing them the other way round would have uci refuse: there is
// nothing to set an option in, and nothing to clear one out of. The order is
// the store's to get right because the rollback builds its changes from a
// journal whose order is the plan's, reversed in meaning but not in sequence.
func TestSectionsAreCreatedFirstAndRemovedLast(t *testing.T) {
	option := Change{Key: Key{Section: "sta9", Option: "ssid"}, Text: "x"}
	create := Change{Key: Key{Section: "sta9"}, Text: "wifi-iface"}
	remove := Change{Key: Key{Section: "sta9"}, Delete: true}

	got := ordered([]Change{option, remove, create})
	if len(got) != 3 {
		t.Fatalf("ordered dropped a change: %+v", got)
	}
	if !got[0].Key.IsSection() || got[0].Delete {
		t.Errorf("first = %+v, want the creation", got[0])
	}
	if got[1].Key.IsSection() {
		t.Errorf("second = %+v, want the option", got[1])
	}
	if !got[2].Key.IsSection() || !got[2].Delete {
		t.Errorf("last = %+v, want the removal", got[2])
	}
}

// Deleting a section takes its options with it, in the staged commands as well
// as in the file.
func TestRemovingASectionRemovesWhatWasInIt(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.Stage(t.Context(), "wireless",
		[]Change{{Key: Key{Section: "sta0"}, Delete: true}}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	live := fixture.liveText(t)
	if strings.Contains(live, "sta0") {
		t.Errorf("the section or its options survived:\n%s", live)
	}
	// And the household's access point is untouched, which is the whole point
	// of naming one section rather than rewriting the package.
	if !strings.Contains(live, "wireless.ap0.ssid='HomeNet'") {
		t.Errorf("the home access point was removed too:\n%s", live)
	}
}

// Removing an option that was never there is the state already holding, not a
// failure.
//
// uci exits 1 for a delete it cannot find, with or without -q. An open network
// has no passphrase, so its plan deletes `key` from a section that may never
// have had one -- which means treating that exit as a failure would make every
// open network fail to stage. Skipped rather than attempted-and-forgiven, so a
// delete that really does fail still fails.
func TestDeletingAnOptionThatWasNeverThereIsNotAFailure(t *testing.T) {
	fixture := newFixture(t)
	// sta0 in the fixture has ssid and encryption, and no key.
	open := []Change{
		{Key: ssid, Text: "jxnu_open"},
		{Key: key, Delete: true},
		{Key: Key{Section: "sta0", Option: "bssid"}, Delete: true},
	}

	if err := fixture.store.Stage(t.Context(), "wireless", open); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	live := fixture.liveText(t)
	if !strings.Contains(live, "wireless.sta0.ssid=jxnu_open") {
		t.Errorf("the change was not applied:\n%s", live)
	}
	if strings.Contains(live, "wireless.sta0.key") {
		t.Errorf("a key appeared from nowhere:\n%s", live)
	}
}

// And an option that is there really is removed.
//
// The pair matters: a store that skipped every deletion would pass the test
// above and quietly leave the previous network's passphrase in the client
// section.
func TestDeletingAnOptionThatIsThereRemovesIt(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.Stage(t.Context(), "wireless",
		[]Change{{Key: enc, Delete: true}}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := fixture.store.Commit(t.Context(), "wireless"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if live := fixture.liveText(t); strings.Contains(live, "wireless.sta0.encryption") {
		t.Errorf("the option survived its deletion:\n%s", live)
	}
}

// Reading a section reports its type, and whether it is there at all.
func TestReadingASectionReportsItsType(t *testing.T) {
	fixture := newFixture(t)

	values, err := fixture.store.Read(t.Context(), "wireless", []Key{
		{Section: "sta0"}, {Section: "not_there"},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := (Value{Text: "wifi-iface", Present: true}); values[Key{Section: "sta0"}] != want {
		t.Errorf("sta0 = %+v, want %+v", values[Key{Section: "sta0"}], want)
	}
	if values[Key{Section: "not_there"}].Present {
		t.Error("a section that does not exist was reported present")
	}
}

// An exit status uci uses for something else is not turned into NotFound.
func TestAnOrdinaryFailureIsNotReportedAsAMissingPackage(t *testing.T) {
	fixture := newFixture(t)
	fixture.uci.failOn = "export"

	_, err := fixture.store.Read(t.Context(), "wireless", []Key{ssid})
	if err == nil {
		t.Fatal("Read reported success after uci refused")
	}
	if code, ok := domain.CodeOf(err); ok && code == domain.CodeNotFound {
		t.Errorf("a status-2 failure was reported as a missing package: %v", err)
	}
	var exit *openwrt.ExitError
	if !errors.As(err, &exit) {
		t.Errorf("the underlying failure was dropped: %v", err)
	}
}
