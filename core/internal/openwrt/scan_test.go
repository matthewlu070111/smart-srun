package openwrt

import (
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// A real scan from a real client radio on a real campus.
//
// The numbers below are not chosen: they are what the device returned, and the
// fixture is that output with the BSSIDs and the neighbours' network names
// renamed. Nothing here asserts a value this test invented.
func TestARealScanIsReadCorrectly(t *testing.T) {
	results, err := ParseScanResults("phy1-sta0",
		testdata(t, "kwrt-25.12/iwinfo-scan.json"))
	if err != nil {
		t.Fatalf("ParseScanResults: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("read %d results, want the 10 the device reported", len(results))
	}

	first := results[0]
	if first.SSID != "jxnu_stu" {
		t.Errorf("SSID = %q, want jxnu_stu", first.SSID)
	}
	// Lower-cased here. iwinfo prints it upper-case and UCI stores it
	// lower-case, and comparing the two forms is how a pinned access point
	// looks missing while it is sitting right there.
	if first.BSSID != "02:00:5e:00:53:02" {
		t.Errorf("BSSID = %q, want it lower-cased", first.BSSID)
	}
	if first.Signal != -45 || first.Channel != 52 || first.Frequency != 5260 {
		t.Errorf("first result = %+v", first)
	}
	if first.Encryption.Enabled {
		t.Error("the campus network is open and was read as encrypted")
	}
	if !first.Joinable() {
		t.Error("a Master-mode entry is joinable")
	}
}

// The campus network really is open, and there really are several of them.
//
// This is the case the AP-selection policy exists for, and it is worth pinning
// down from real data rather than from a fixture somebody designed: eight
// access points share one SSID at signals from -45 to -90 dBm, so "which one"
// is a question that has to be answered every time this radio connects.
func TestTheRealScanHasManyAccessPointsSharingOneSSID(t *testing.T) {
	results, err := ParseScanResults("phy1-sta0",
		testdata(t, "kwrt-25.12/iwinfo-scan.json"))
	if err != nil {
		t.Fatalf("ParseScanResults: %v", err)
	}

	campus := 0
	seen := map[string]bool{}
	for _, result := range results {
		if result.SSID != "jxnu_stu" {
			continue
		}
		campus++
		if seen[result.BSSID] {
			t.Errorf("BSSID %s appeared twice", result.BSSID)
		}
		seen[result.BSSID] = true
		if result.Encryption.Enabled {
			t.Errorf("%s reported the campus network as encrypted", result.BSSID)
		}
	}
	if campus < 2 {
		t.Fatalf("found %d campus access points; this fixture exists because "+
			"there are several", campus)
	}
}

// A protected neighbour is read with its suites intact.
//
// The authentication list is what decides whether an account's credentials fit,
// so it has to survive parsing as a list. Collapsing it to a single name is
// where a mixed-mode access point stops being matchable by one of the two kinds
// of client it admits.
func TestAProtectedNeighbourKeepsItsSuites(t *testing.T) {
	results, err := ParseScanResults("phy1-sta0",
		testdata(t, "kwrt-25.12/iwinfo-scan.json"))
	if err != nil {
		t.Fatalf("ParseScanResults: %v", err)
	}

	var protected *ScanResult
	for index := range results {
		if results[index].Encryption.Enabled {
			protected = &results[index]
			break
		}
	}
	if protected == nil {
		t.Fatal("the fixture has no encrypted network; it had one when captured")
	}
	if len(protected.Encryption.Authentication) == 0 {
		t.Fatal("an encrypted access point came back with no authentication suites")
	}
	if protected.Encryption.Authentication[0] != "psk" {
		t.Errorf("authentication = %v, want the psk the device reported",
			protected.Encryption.Authentication)
	}
	if len(protected.Encryption.WPA) != 2 {
		t.Errorf("wpa = %v, want the two versions the device reported",
			protected.Encryption.WPA)
	}
}

// Output this cannot read is not an empty neighbourhood.
//
// "No access points found" is a fact with consequences -- it is the answer that
// tells a pinned account its access point is gone -- and a parser must not
// produce it for input it failed to understand. The M04 lesson, applied to a
// different parser: an object that is simply not a scan reply unmarshals
// happily into a struct of optional fields.
func TestUnreadableOutputIsNotAnEmptyNeighbourhood(t *testing.T) {
	for name, input := range map[string]string{
		"empty":              ``,
		"not json":           `<html>gateway login</html>`,
		"another method":     `{"devices":["phy0-ap0"]}`,
		"an error object":    `{"error":{"code":-32000}}`,
		"an unrelated array": `[{"ssid":"x"}]`,
	} {
		results, err := ParseScanResults("phy1-sta0", []byte(input))
		if err == nil {
			t.Errorf("%s: parsed into %d results instead of failing",
				name, len(results))
			continue
		}
		if code, _ := domain.CodeOf(err); code != domain.CodeInternal {
			t.Errorf("%s: code = %s, want Internal", name, code)
		}
	}
}

// An empty list, on the other hand, is a real answer and must survive as one.
func TestAnEmptyScanIsAnAnswerRatherThanAnError(t *testing.T) {
	results, err := ParseScanResults("phy1-sta0", []byte(`{"results":[]}`))
	if err != nil {
		t.Fatalf("an empty scan was reported as a failure: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("read %d results from an empty scan", len(results))
	}
}

// Only an infrastructure access point can be joined by a client.
//
// iwinfo lists ad-hoc and mesh peers in the same array, with an SSID and a
// signal reading, so nothing downstream would notice them. The captured scan
// contains only Master entries -- this radio saw no mesh -- so the other modes
// are checked here against the rule rather than against the device.
func TestOnlyMasterModeEntriesAreJoinable(t *testing.T) {
	for mode, joinable := range map[string]bool{
		"Master":  true,
		"master":  true,
		"Ad-Hoc":  false,
		"Mesh":    false,
		"Client":  false,
		"Monitor": false,
		"":        false,
	} {
		if got := (ScanResult{Mode: mode}).Joinable(); got != joinable {
			t.Errorf("Joinable(%q) = %v, want %v", mode, got, joinable)
		}
	}
}

// respondTruncated is a reply the output limit cut short.
func (r *recordingRunner) respondTruncated(stdout []byte, program string, args ...string) {
	r.respond(stdout, program, args...)
	key := r.key(program, args)
	result := r.responses[key]
	result.StdoutTruncated = true
	r.responses[key] = result
}

const scanRequest = `{"device":"phy1-sta0"}`

// A truncated scan is refused, not used.
//
// This is the one that would be invisible. A cut-off reply is still valid JSON
// only by accident, but even when it parses it is a shorter list of access
// points -- and a shorter list is indistinguishable from a quieter
// neighbourhood. The access point that got cut off is then "gone", which for a
// pinned account is a failure with a confident and wrong explanation.
func TestATruncatedScanIsRefusedRatherThanShortened(t *testing.T) {
	runner := &recordingRunner{}
	// Deliberately still-parseable: the point is that truncation is refused on
	// its own evidence, not because the remains happened not to parse.
	runner.respondTruncated([]byte(`{"results":[{"ssid":"jxnu_stu","mode":"Master"}]}`),
		"ubus", "call", "iwinfo", "scan", scanRequest)

	results, err := NewAdapter(runner).Scan(t.Context(), "phy1-sta0")
	if err == nil {
		t.Fatalf("a truncated scan produced %d results", len(results))
	}
	if code := codeOf(t, err); code != domain.CodeInternal {
		t.Errorf("code = %s, want Internal", code)
	}
}

// A name that is not a device never reaches ubus.
func TestScanRefusesANameThatIsNotADevice(t *testing.T) {
	runner := &recordingRunner{}
	adapter := NewAdapter(runner)

	for _, device := range []string{"", "phy1 sta0", `phy1","x":"`, "$(id)",
		"phy1\nsta0"} {
		if _, err := adapter.Scan(t.Context(), device); err == nil {
			t.Errorf("Scan(%q) succeeded", device)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("a name that is not a device was passed to ubus: %v", runner.calls)
	}
}

// The request is one encoded argument, built by the JSON encoder rather than by
// pasting the device name into a string.
func TestTheScanRequestIsOneEncodedArgument(t *testing.T) {
	runner := &recordingRunner{}
	runner.respond(testdata(t, "kwrt-25.12/iwinfo-scan.json"),
		"ubus", "call", "iwinfo", "scan", scanRequest)

	results, err := NewAdapter(runner).Scan(t.Context(), "phy1-sta0")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(results) != 10 {
		t.Errorf("read %d results through the adapter", len(results))
	}
	if len(runner.calls) != 1 || len(runner.calls[0]) != 5 {
		t.Fatalf("calls = %v", runner.calls)
	}
	if got := runner.calls[0][4]; got != scanRequest {
		t.Errorf("request = %s", got)
	}
}

// The signal in a scan goes through the same correction as the one in an info
// call, because it is the same signed char printed by the same encoders.
func TestAScanSignalPrintedAsUnsignedIsReadAsNegative(t *testing.T) {
	results, err := ParseScanResults("phy1-sta0",
		[]byte(`{"results":[{"ssid":"x","bssid":"AA:BB:CC:DD:EE:FF","mode":"Master","signal":4294967244}]}`))
	if err != nil {
		t.Fatalf("ParseScanResults: %v", err)
	}
	if results[0].Signal != -52 {
		t.Errorf("Signal = %d, want -52", results[0].Signal)
	}
	if results[0].BSSID != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("BSSID = %q, want it lower-cased", results[0].BSSID)
	}
}
