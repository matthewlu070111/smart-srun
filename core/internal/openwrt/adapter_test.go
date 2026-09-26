package openwrt

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// recordingRunner answers with canned output and remembers every argv it was
// asked to run.
//
// This exists to check composition -- which tool, which arguments, how a
// failure is reported -- not to stand in for a process. What a process does is
// verified against real processes in command_test.go, and this package also
// runs the adapter against a real executable below.
type recordingRunner struct {
	responses map[string]Result
	errors    map[string]error
	calls     [][]string
	missing   []string
}

func (r *recordingRunner) key(program string, args []string) string {
	return strings.Join(append([]string{program}, args...), "\x00")
}

func (r *recordingRunner) Run(_ context.Context, program string, args ...string) (Result, error) {
	r.calls = append(r.calls, append([]string{program}, args...))
	if slices.Contains(r.missing, program) {
		return Result{Program: program}, domain.Errorf(
			domain.CodeUnsupportedCapability, "系统缺少 %s", program)
	}
	key := r.key(program, args)
	if err, ok := r.errors[key]; ok {
		return Result{Program: program}, err
	}
	if result, ok := r.responses[key]; ok {
		return result, nil
	}
	return Result{Program: program}, &ExitError{Program: program, Code: 1}
}

func (r *recordingRunner) Resolve(program string) (string, error) {
	if slices.Contains(r.missing, program) {
		return "", domain.Errorf(domain.CodeUnsupportedCapability,
			"系统缺少 %s", program)
	}
	return "/usr/bin/" + program, nil
}

func (r *recordingRunner) respond(stdout []byte, program string, args ...string) {
	if r.responses == nil {
		r.responses = map[string]Result{}
	}
	r.responses[r.key(program, args)] = Result{Program: program, Stdout: stdout}
}

func (r *recordingRunner) fail(err error, program string, args ...string) {
	if r.errors == nil {
		r.errors = map[string]error{}
	}
	r.errors[r.key(program, args)] = err
}

func staticFacts(index int, addresses ...string) func(string) (int, []netip.Addr, error) {
	parsed := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		parsed = append(parsed, netip.MustParseAddr(address))
	}
	return func(string) (int, []netip.Addr, error) { return index, parsed, nil }
}

// T12 -- the ordinary case, end to end, from a real interface status.
func TestResolvingAWorkingLineProducesItsBinding(t *testing.T) {
	runner := &recordingRunner{}
	runner.respond(testdata(t, "kwrt-25.12/iface-wwan-up.json"),
		"ubus", "call", "network.interface.wwan", "status")

	adapter := NewAdapter(runner)
	adapter.deviceFacts = staticFacts(7, "192.0.2.10")

	binding, err := adapter.ResolveBinding(t.Context(), "wwan", 42)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}

	if binding.LogicalIface != "wwan" || binding.L3Device != "phy1-sta0" {
		t.Errorf("binding = %+v", binding)
	}
	if binding.SourceIPv4 != netip.MustParseAddr("192.0.2.10") {
		t.Errorf("source = %v", binding.SourceIPv4)
	}
	if binding.IfIndex != 7 {
		t.Errorf("IfIndex = %d, want 7", binding.IfIndex)
	}
	if binding.Generation != 42 {
		t.Errorf("Generation = %d; it is the caller's and must be carried "+
			"through unchanged", binding.Generation)
	}
	if len(binding.DNSServers) != 2 {
		t.Errorf("DNS servers = %v; they belong to the line", binding.DNSServers)
	}
	if !binding.Ready() {
		t.Error("the binding is not usable")
	}
}

// T12 -- the cross-check spec 04 requires.
//
// netifd's status is a cache. When it reports an address the device does not
// actually hold -- because DHCP just moved it, or because two lines are on
// overlapping private subnets -- binding to it either fails or succeeds through
// the default route, authenticating the wrong line with these credentials.
func TestAnAddressThatIsNotOnTheDeviceIsRefused(t *testing.T) {
	runner := &recordingRunner{}
	runner.respond(testdata(t, "kwrt-25.12/iface-wwan-up.json"),
		"ubus", "call", "network.interface.wwan", "status")

	adapter := NewAdapter(runner)
	// The device really holds a different address: the lease changed.
	adapter.deviceFacts = staticFacts(7, "198.51.100.4")

	_, err := adapter.ResolveBinding(t.Context(), "wwan", 1)
	if err == nil {
		t.Fatal("bound to an address the device does not have")
	}
	if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
		t.Errorf("code = %s, want BindingUnavailable", code)
	}
}

// The same address on two devices is the case a reverse lookup gets wrong. The
// resolution is forward -- interface, then its device, then that device's
// addresses -- so the other device holding the same address changes nothing.
func TestTwoDevicesWithTheSameAddressResolveByInterfaceNotByAddress(t *testing.T) {
	runner := &recordingRunner{}
	runner.respond(testdata(t, "kwrt-25.12/iface-wwan-up.json"),
		"ubus", "call", "network.interface.wwan", "status")

	adapter := NewAdapter(runner)
	asked := ""
	adapter.deviceFacts = func(device string) (int, []netip.Addr, error) {
		asked = device
		// Both eth1 and phy1-sta0 hold 192.0.2.10 in this scenario; only the
		// one netifd named for this interface may be consulted.
		return 9, []netip.Addr{netip.MustParseAddr("192.0.2.10")}, nil
	}

	binding, err := adapter.ResolveBinding(t.Context(), "wwan", 1)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	if asked != "phy1-sta0" {
		t.Errorf("looked up device %q; resolution must follow the interface to "+
			"its own device, never search for whoever owns the address", asked)
	}
	if binding.L3Device != "phy1-sta0" {
		t.Errorf("L3Device = %q", binding.L3Device)
	}
}

// T12 -- each way a line can fail to be usable gets its own answer.
func TestEachKindOfUnusableLineIsReportedDifferently(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		iface   string
		want    domain.ErrorCode
		says    string
	}{
		{"the cable is out", "kwrt-25.12/iface-wan-down.json", "wan",
			domain.CodeBindingUnavailable, "未启用"},
		{"no device at all", "kwrt-25.12/iface-no-device.json", "wan6",
			domain.CodeBindingUnavailable, "NO_DEVICE"},
		{"still waiting for DHCP", "kwrt-25.12/iface-tunnel-no-address.json", "EasyTier",
			domain.CodeBindingUnavailable, "IPv4"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &recordingRunner{}
			runner.respond(testdata(t, testCase.fixture),
				"ubus", "call", "network.interface."+testCase.iface, "status")

			adapter := NewAdapter(runner)
			adapter.deviceFacts = staticFacts(3, "192.0.2.10")

			_, err := adapter.ResolveBinding(t.Context(), testCase.iface, 1)
			if err == nil {
				t.Fatal("an unusable line produced a binding")
			}
			if code := codeOf(t, err); code != testCase.want {
				t.Errorf("code = %s, want %s", code, testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.says) {
				t.Errorf("the message does not explain the problem: %v", err)
			}
		})
	}
}

// An interface the user selected and then deleted is a configuration mistake,
// not a transient failure, and NotFound is what tells the interface to send
// them back to the field rather than offer a retry.
func TestSelectingAnInterfaceThatDoesNotExistIsNotFound(t *testing.T) {
	runner := &recordingRunner{}
	runner.fail(&ExitError{Program: "ubus", Code: ubusStatusNotFound},
		"ubus", "call", "network.interface.wan9", "status")

	adapter := NewAdapter(runner)
	_, err := adapter.InterfaceStatus(t.Context(), "wan9")
	if err == nil {
		t.Fatal("an interface that does not exist was reported as readable")
	}
	if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound", code)
	}
}

// And resolving a binding for it says the same thing.
//
// Flattening this into "cannot read the interface status" would offer a retry
// for a problem no amount of waiting fixes; the user has to pick a different
// interface.
func TestResolvingADeletedInterfaceKeepsTheNotFoundAnswer(t *testing.T) {
	runner := &recordingRunner{}
	runner.fail(&ExitError{Program: "ubus", Code: ubusStatusNotFound},
		"ubus", "call", "network.interface.wan9", "status")

	adapter := NewAdapter(runner)
	_, err := adapter.ResolveBinding(t.Context(), "wan9", 1)
	if err == nil {
		t.Fatal("an interface that does not exist produced a binding")
	}
	if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound", code)
	}
	if !strings.Contains(err.Error(), "wan9") {
		t.Errorf("the message does not name the interface: %v", err)
	}
}

// A message must not be assembled by splicing a configured value into a format
// string. Today's validators reject a percent sign, which is exactly the kind
// of thing that stops being true later, and the result would be a user-facing
// message with %!s(MISSING) in it.
func TestADiagnosisDoesNotTreatTheInterfaceNameAsAFormat(t *testing.T) {
	err := unavailable("wan%s%d", "接口尚未获取到 IPv4 地址")
	if got := err.Error(); !strings.Contains(got, "wan%s%d") ||
		strings.Contains(got, "%!") {
		t.Errorf("message = %q; the name is data, not a format", got)
	}
}

// A router with no wireless hardware has no wireless configuration at all.
//
// uci answers "Entry not found" with status 1, and reporting that as a generic
// failure would have the interface offer a retry for a system that has nothing
// to configure. Seen on a real OpenWrt 24.10.8 guest with no radio.
func TestAConfigurationPackageThatDoesNotExistIsNotFound(t *testing.T) {
	runner := &recordingRunner{}
	runner.fail(&ExitError{Program: "uci", Code: 1}, "uci", "show", "wireless")
	runner.fail(&ExitError{Program: "uci", Code: 1}, "uci", "changes", "wireless")

	adapter := NewAdapter(runner)

	_, err := adapter.UCI(t.Context(), "wireless")
	if err == nil {
		t.Fatal("a package that does not exist was read successfully")
	}
	if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound", code)
	}
	if !strings.Contains(err.Error(), "wireless") {
		t.Errorf("the message does not name the package: %v", err)
	}

	// The same for the change list, which must not read as "nothing staged"
	// either: there is no package, which is a different fact.
	changes, err := adapter.PendingChanges(t.Context(), "wireless")
	if err == nil {
		t.Fatalf("changes = %+v, want an error for a package that is absent",
			changes)
	}
	if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound", code)
	}
}

// A uci failure that is not "entry not found" keeps its own answer, so a broken
// uci does not read as a system with no configuration.
func TestAnotherUCIFailureIsNotReportedAsAMissingPackage(t *testing.T) {
	runner := &recordingRunner{}
	runner.fail(&ExitError{Program: "uci", Code: 255}, "uci", "show", "network")

	adapter := NewAdapter(runner)
	_, err := adapter.UCI(t.Context(), "network")
	if err == nil {
		t.Fatal("expected a failure")
	}
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 255 {
		t.Errorf("err = %v, want the original exit status", err)
	}
	if code, ok := domain.CodeOf(err); ok && code == domain.CodeNotFound {
		t.Error("an unexplained uci failure was reported as a missing package")
	}
}

// A name that is not a uci section name cannot be a netifd interface, so it is
// taken as a Linux device -- which is how "wan.v2" works. ubus must not be
// asked about it at all.
func TestAVLANDeviceIsResolvedWithoutAskingNetifd(t *testing.T) {
	runner := &recordingRunner{}
	adapter := NewAdapter(runner)
	adapter.deviceFacts = staticFacts(11, "192.0.2.30")

	binding, err := adapter.ResolveBinding(t.Context(), "wan.v2", 5)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("asked netifd about a name it cannot know: %v", runner.calls)
	}
	if binding.L3Device != "wan.v2" || binding.IfIndex != 11 {
		t.Errorf("binding = %+v", binding)
	}
	if binding.SourceIPv4 != netip.MustParseAddr("192.0.2.30") {
		t.Errorf("source = %v", binding.SourceIPv4)
	}
	if len(binding.DNSServers) != 0 {
		t.Error("a device has no interface to take resolvers from; inventing " +
			"some would resolve through the default route")
	}
}

// A device that is not there at all -- removed, renamed, never existed.
func TestADeviceThatDoesNotExistFailsClosed(t *testing.T) {
	adapter := NewAdapter(&recordingRunner{})
	adapter.deviceFacts = func(string) (int, []netip.Addr, error) {
		return 0, nil, os.ErrNotExist
	}

	_, err := adapter.ResolveBinding(t.Context(), "wan.v2", 1)
	if err == nil {
		t.Fatal("a missing device produced a binding")
	}
	if code := codeOf(t, err); code != domain.CodeBindingUnavailable {
		t.Errorf("code = %s, want BindingUnavailable", code)
	}
}

// A name that is neither shape is refused before it can reach an argv.
func TestANameThatIsNeitherInterfaceNorDeviceIsRefused(t *testing.T) {
	adapter := NewAdapter(&recordingRunner{})
	for _, name := range []string{"", "wan; reboot", "$(id)", "wan\nwan2",
		strings.Repeat("e", 40)} {
		_, err := adapter.ResolveBinding(t.Context(), name, 1)
		if err == nil {
			t.Errorf("ResolveBinding(%q) succeeded", name)
			continue
		}
		if code := codeOf(t, err); code != domain.CodeInvalidArgument &&
			code != domain.CodeBindingUnavailable {
			t.Errorf("ResolveBinding(%q): code = %s", name, code)
		}
	}
}

// The device is up but the kernel has no address on it yet. Reported as a
// binding problem rather than as a device problem, so the caller waits.
func TestADeviceWithNoAddressIsNotBound(t *testing.T) {
	adapter := NewAdapter(&recordingRunner{})
	adapter.deviceFacts = staticFacts(4)

	_, err := adapter.ResolveBinding(t.Context(), "wan.v2", 1)
	if err == nil {
		t.Fatal("a device with no address produced a binding")
	}
	if !strings.Contains(err.Error(), "IPv4") {
		t.Errorf("the message does not say what is missing: %v", err)
	}
}

// The adapter names the tool and the arguments; nothing is assembled into one
// string for something else to split.
func TestTheAdapterCallsToolsWithSeparateArguments(t *testing.T) {
	runner := &recordingRunner{}
	runner.respond(testdata(t, "kwrt-25.12/iface-wan-down.json"),
		"ubus", "call", "network.interface.wan", "status")
	runner.respond(testdata(t, "kwrt-25.12/uci-show-wireless.txt"),
		"uci", "show", "wireless")

	adapter := NewAdapter(runner)
	if _, err := adapter.InterfaceStatus(t.Context(), "wan"); err != nil {
		t.Fatalf("InterfaceStatus: %v", err)
	}
	if _, err := adapter.UCI(t.Context(), "wireless"); err != nil {
		t.Fatalf("UCI: %v", err)
	}

	want := [][]string{
		{"ubus", "call", "network.interface.wan", "status"},
		{"uci", "show", "wireless"},
	}
	if !slices.EqualFunc(runner.calls, want, slices.Equal) {
		t.Errorf("calls = %v, want %v", runner.calls, want)
	}
	for _, call := range runner.calls {
		for _, argument := range call {
			if strings.ContainsAny(argument, " \t\n;|&$`") {
				t.Errorf("argument %q looks like a command line rather than an "+
					"argument", argument)
			}
		}
	}
}

// Truncated output must not be parsed.
//
// The fragments here are deliberately well formed. Output cut mid-token fails
// to parse on its own, so a test built from one passes whether or not the
// truncation is noticed -- which is what the first version of this test did.
// A configuration cut at a line boundary parses perfectly and is simply missing
// its later sections, and a router missing the section this program manages is
// indistinguishable from a router that never had it: the next step would be to
// create a second wireless client beside the working one.
func TestTruncatedOutputIsRefusedRatherThanParsed(t *testing.T) {
	full := testdata(t, "kwrt-25.12/uci-show-wireless.txt")
	cleanCut := full[:bytes.Index(full, []byte("wireless.radio1="))]

	// Both fragments have to be valid on their own, or this proves nothing.
	if _, err := ParseUCIShow("wireless", cleanCut); err != nil {
		t.Fatalf("the fragment must parse for this test to mean anything: %v", err)
	}
	shortStatus := []byte(`{"up": true, "available": true, "l3_device": "eth1"}`)
	if _, err := ParseInterfaceStatus("wan", shortStatus); err != nil {
		t.Fatalf("the fragment must parse for this test to mean anything: %v", err)
	}

	runner := &recordingRunner{responses: map[string]Result{}}
	runner.responses[runner.key("uci", []string{"show", "wireless"})] = Result{
		Program: "uci", Stdout: cleanCut, StdoutTruncated: true,
	}
	runner.responses[runner.key("ubus", []string{"call", "network.interface.wan", "status"})] = Result{
		Program: "ubus", Stdout: shortStatus, StdoutTruncated: true,
	}
	runner.responses[runner.key("uci", []string{"changes", "wireless"})] = Result{
		Program: "uci", Stdout: []byte("wireless.radio0.channel='11'\n"),
		StdoutTruncated: true,
	}

	adapter := NewAdapter(runner)
	if _, err := adapter.UCI(t.Context(), "wireless"); err == nil {
		t.Error("a truncated configuration was parsed as a whole one")
	}
	if _, err := adapter.InterfaceStatus(t.Context(), "wan"); err == nil {
		t.Error("a truncated status was parsed as a whole one")
	}
	if _, err := adapter.PendingChanges(t.Context(), "wireless"); err == nil {
		t.Error("a truncated change list was read as the whole list; the " +
			"changes it did not show would be committed under our name")
	}
}

// The kernel path, with no injection at all.
//
// This is what actually runs on the router: no subprocess, no output to parse,
// just the interface table. Loopback is the one device every machine has.
func TestTheDefaultDeviceLookupReadsTheRealKernel(t *testing.T) {
	name := "lo"
	if runtime.GOOS == "windows" {
		t.Skip("no loopback device of that name here")
	}

	index, addresses, err := kernelDeviceFacts(name)
	if err != nil {
		t.Fatalf("kernelDeviceFacts(%q): %v", name, err)
	}
	if index <= 0 {
		t.Errorf("index = %d, want a real interface index", index)
	}
	if !slices.Contains(addresses, netip.MustParseAddr("127.0.0.1")) {
		t.Errorf("addresses = %v, want to include 127.0.0.1", addresses)
	}

	if _, _, err := kernelDeviceFacts("nosuchdev0"); err == nil {
		t.Error("a device that does not exist was looked up successfully")
	}
}

// A real executable, on a real search path, producing real bytes.
//
// The tests above drive the adapter through a seam so they can check which
// arguments it builds. This one closes the gap: the same code path, with an
// actual program on disk, so the seam cannot be hiding a mismatch between what
// the adapter asks for and what a process would give it.
func TestTheAdapterWorksAgainstARealExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "status.json")
	if err := os.WriteFile(fixture,
		testdata(t, "kwrt-25.12/iface-wwan-up.json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Named ubus, and run by path with its own interpreter. The runner refuses
	// to start a shell by name; a program that happens to be a script is a
	// program.
	stub := "#!/bin/sh\n" +
		"if [ \"$1\" = call ] && [ \"$2\" = network.interface.wwan ] && [ \"$3\" = status ]; then\n" +
		"  cat " + fixture + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'Command failed: Not found' >&2\n" +
		"exit 4\n"
	if err := os.WriteFile(filepath.Join(dir, "ubus"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	adapter := NewAdapter(Runner{SearchPath: []string{dir}})
	adapter.deviceFacts = staticFacts(2, "192.0.2.10")

	binding, err := adapter.ResolveBinding(t.Context(), "wwan", 3)
	if err != nil {
		t.Fatalf("ResolveBinding through a real process: %v", err)
	}
	if binding.L3Device != "phy1-sta0" {
		t.Errorf("L3Device = %q", binding.L3Device)
	}

	// And the not-found path, with the real exit status ubus uses.
	if _, err := adapter.InterfaceStatus(t.Context(), "wan9"); err == nil {
		t.Error("a missing interface succeeded")
	} else if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound", code)
	}
}
