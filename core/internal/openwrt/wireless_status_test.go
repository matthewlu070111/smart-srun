package openwrt

import (
	"context"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// A real router's wireless status, read as it came off the device.
//
// The router this came from has two radios, an access point on each, and three
// station sections -- all of them disabled. That last part is why this fixture
// is worth having: netifd lists no interface at all for a disabled section, so
// the interface this program manages is exactly the one that is usually not
// there, and code that expected to find it would fail on the ordinary case.
func TestARealWirelessStatusIsReadCorrectly(t *testing.T) {
	radios, err := ParseWirelessStatus(
		testdata(t, "kwrt-25.12-feb/wireless-status.json"))
	if err != nil {
		t.Fatalf("ParseWirelessStatus: %v", err)
	}
	if len(radios) != 2 {
		t.Fatalf("read %d radios, want the 2 the device reported", len(radios))
	}

	// Sorted, so "the first radio" is the same radio twice running. A JSON
	// object has no order and Go's map has less.
	if radios[0].Name != "radio0" || radios[1].Name != "radio1" {
		t.Errorf("radios = %q, %q; want them sorted", radios[0].Name, radios[1].Name)
	}

	first := radios[0]
	if !first.Up || first.Pending || first.Disabled || first.RetrySetupFailed {
		t.Errorf("radio0 = %+v, want up and settled", first)
	}
	if len(first.Interfaces) != 1 {
		t.Fatalf("radio0 has %d interfaces, want 1", len(first.Interfaces))
	}

	iface := first.Interfaces[0]
	if iface.Section != "default_radio0" {
		t.Errorf("section = %q", iface.Section)
	}
	// The field this type exists for. It cannot be derived from the radio name
	// or the section name, and iwinfo will not answer to anything else.
	if iface.IfName != "phy0-ap0" {
		t.Errorf("ifname = %q, want phy0-ap0", iface.IfName)
	}
	if iface.Mode != "ap" || iface.Station() {
		t.Errorf("mode = %q; an access point is not a station", iface.Mode)
	}
	if len(iface.Network) != 1 || iface.Network[0] != "lan" {
		t.Errorf("network = %v, want [lan]", iface.Network)
	}
}

// A section that produced no interface is absent, not an error.
//
// Every station section on the device this came from is disabled, so netifd
// lists none of them. A build that treated "not found" as a fault would refuse
// to work on the state every router is in before this program has enabled
// anything.
func TestASectionWithNoInterfaceIsSimplyAbsent(t *testing.T) {
	radios, err := ParseWirelessStatus(
		testdata(t, "kwrt-25.12-feb/wireless-status.json"))
	if err != nil {
		t.Fatalf("ParseWirelessStatus: %v", err)
	}
	radio, ok := FindRadio(radios, "radio1")
	if !ok {
		t.Fatal("radio1 is not in the status")
	}
	if _, found := radio.FindInterface("jxnu_sta_radio1"); found {
		t.Error("a disabled section was reported as having an interface")
	}

	// And a scan can still happen, because the access point on that radio is
	// up. Falling back is what makes choosing an access point possible before
	// the client exists to choose one with.
	device, ok := radio.AnyDevice()
	if !ok || device != "phy1-ap0" {
		t.Errorf("AnyDevice = %q, %v; want the access point on that radio",
			device, ok)
	}
}

// The passphrase in the reply is never read, so it cannot be leaked.
//
// `ubus call network.wireless status` reports every interface's configuration,
// key included, for the household's own access point as well as this program's
// client. Keeping it out of the struct is stronger than remembering not to log
// it: there is nothing to log. The fixture's own value is redacted, so this
// checks the decoded value rather than the input -- a struct that took the
// whole config object would carry "redacted-key" here just as faithfully as it
// would have carried the real one.
func TestTheStatusDecoderNeverReadsThePassphrase(t *testing.T) {
	raw := testdata(t, "kwrt-25.12-feb/wireless-status.json")
	if !strings.Contains(string(raw), "redacted-key") {
		t.Fatal("the fixture no longer carries a key field; this test is checking nothing")
	}

	radios, err := ParseWirelessStatus(raw)
	if err != nil {
		t.Fatalf("ParseWirelessStatus: %v", err)
	}
	for _, radio := range radios {
		for _, iface := range radio.Interfaces {
			if strings.Contains(formatInterface(iface), "redacted-key") {
				t.Errorf("%s carries the key field: %+v", iface.Section, iface)
			}
		}
	}
}

// formatInterface renders everything the type holds, so the check above sees
// any field that was added later as well as the ones there today.
func formatInterface(iface WirelessInterface) string {
	var builder strings.Builder
	builder.WriteString(iface.Section)
	builder.WriteString(iface.IfName)
	builder.WriteString(iface.Mode)
	builder.WriteString(iface.SSID)
	builder.WriteString(strings.Join(iface.Network, ","))
	return builder.String()
}

// A station on the radio is preferred over the access point beside it.
//
// Constructed rather than captured: every station section on the real device is
// disabled, so no capture of it has a station interface. The shape is the
// device's -- the same fields, in the same places -- and what is being checked
// is this package's choice between two interfaces, not netifd's output.
func TestAStationOnTheRadioIsPreferredForItsDevice(t *testing.T) {
	radios, err := ParseWirelessStatus([]byte(`{
	  "radio1": {
	    "up": true, "pending": false, "disabled": false,
	    "retry_setup_failed": false,
	    "interfaces": [
	      {"section": "default_radio1", "ifname": "phy1-ap0",
	       "config": {"mode": "ap", "ssid": "HomeNet-5", "network": ["lan"]}},
	      {"section": "jxnu_sta_radio1", "ifname": "phy1-sta0",
	       "config": {"mode": "sta", "ssid": "jxnu_stu", "network": ["wwan"]}}
	    ]
	  }
	}`))
	if err != nil {
		t.Fatalf("ParseWirelessStatus: %v", err)
	}
	radio := radios[0]

	iface, found := radio.FindInterface("jxnu_sta_radio1")
	if !found {
		t.Fatal("the station section's interface was not found")
	}
	if iface.IfName != "phy1-sta0" || !iface.Station() {
		t.Errorf("interface = %+v", iface)
	}

	// The access point is listed first; the station still wins, because it is
	// the interface that will do the associating.
	if device, _ := radio.AnyDevice(); device != "phy1-sta0" {
		t.Errorf("AnyDevice = %q, want the station", device)
	}
}

// A radio netifd gave up on is distinguishable from one it is still working on.
//
// Both are "not up". A wait that could not tell them apart would either give up
// on a radio that was about to come good, or sit out its whole timeout on one
// that never will.
func TestPendingAndFailedAreNotTheSameAsDown(t *testing.T) {
	radios, err := ParseWirelessStatus([]byte(`{
	  "radio0": {"up": false, "pending": true,  "disabled": false,
	             "retry_setup_failed": false, "interfaces": []},
	  "radio1": {"up": false, "pending": false, "disabled": false,
	             "retry_setup_failed": true,  "interfaces": []},
	  "radio2": {"up": false, "pending": false, "disabled": true,
	             "retry_setup_failed": false, "interfaces": []}
	}`))
	if err != nil {
		t.Fatalf("ParseWirelessStatus: %v", err)
	}
	if !radios[0].Pending || radios[0].RetrySetupFailed {
		t.Errorf("radio0 = %+v, want pending", radios[0])
	}
	if radios[1].Pending || !radios[1].RetrySetupFailed {
		t.Errorf("radio1 = %+v, want failed", radios[1])
	}
	if !radios[2].Disabled || radios[2].RetrySetupFailed {
		t.Errorf("radio2 = %+v, want disabled and not failed", radios[2])
	}
}

// Output this cannot read is an error, not an empty list of radios.
//
// The M04 lesson, in the one place it would hurt most: an empty answer here
// means "this router has no wireless", and a caller acting on that would decide
// the interface it manages is gone.
func TestStatusOutputThatCannotBeReadIsRefused(t *testing.T) {
	for name, input := range map[string][]byte{
		"nothing at all": nil,
		"not json":       []byte("ubus call failed\n"),
		"an array":       []byte("[]"),
	} {
		if _, err := ParseWirelessStatus(input); err == nil {
			t.Errorf("%s: parsed without complaint", name)
		}
	}

	// An empty object is a real answer: a router with no radios configured.
	radios, err := ParseWirelessStatus([]byte("{}"))
	if err != nil {
		t.Fatalf("an empty status is not a failure: %v", err)
	}
	if len(radios) != 0 {
		t.Errorf("read %d radios out of nothing", len(radios))
	}
}

// A truncated status is refused rather than read as fewer radios.
//
// Half this reply is half the radios, and the caller uses it to find the
// interface it owns. A short answer reads as "the interface is not there".
func TestATruncatedStatusIsRefused(t *testing.T) {
	runner := &recordingRunner{}
	runner.responses = map[string]Result{
		runner.key("ubus", []string{"call", "network.wireless", "status"}): {
			Stdout: []byte("{}"), StdoutTruncated: true},
	}

	_, err := NewAdapter(runner).WirelessStatus(context.Background())
	if code, _ := domain.CodeOf(err); code != domain.CodeInternal {
		t.Fatalf("WirelessStatus = %v (code %s), want Internal", err, code)
	}
}

// And the adapter asks ubus for the object it means.
func TestWirelessStatusAsksNetifd(t *testing.T) {
	runner := &recordingRunner{}
	runner.respond(testdata(t, "kwrt-25.12-feb/wireless-status.json"),
		"ubus", "call", "network.wireless", "status")

	radios, err := NewAdapter(runner).WirelessStatus(context.Background())
	if err != nil {
		t.Fatalf("WirelessStatus: %v", err)
	}
	if len(radios) != 2 {
		t.Errorf("read %d radios", len(radios))
	}
	if len(runner.calls) != 1 {
		t.Fatalf("ran %d commands, want one", len(runner.calls))
	}
	want := []string{"ubus", "call", "network.wireless", "status"}
	if got := strings.Join(runner.calls[0], " "); got != strings.Join(want, " ") {
		t.Errorf("ran %q, want %q", got, strings.Join(want, " "))
	}
}
