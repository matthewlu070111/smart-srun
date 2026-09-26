package openwrt

import (
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// A real client radio, associated to the campus network.
func TestARealAssociatedClientIsReadCorrectly(t *testing.T) {
	info, err := ParseRadioInfo("phy1-sta0", testdata(t, "kwrt-25.12/iwinfo-sta-open.json"))
	if err != nil {
		t.Fatalf("ParseRadioInfo: %v", err)
	}

	if info.SSID != "jxnu_stu" || info.Mode != "Client" {
		t.Errorf("info = %+v", info)
	}
	if info.Signal != -52 {
		t.Errorf("Signal = %d dBm, want -52", info.Signal)
	}
	if info.Encrypted {
		t.Error("the campus network is open and was read as encrypted")
	}
	if !info.Associated() {
		t.Error("a client with an SSID and a BSSID is associated")
	}
	if info.Channel != 52 || info.Frequency != 5260 {
		t.Errorf("channel/frequency = %d/%d", info.Channel, info.Frequency)
	}
}

// The signedness bug, which is real: some ubus JSON encoders print iwinfo's
// signed char as an unsigned 32-bit number.
//
// Left alone, -52 dBm arrives as 4294967244 and becomes the strongest signal
// ever measured -- so an AP-selection policy that picks the strongest candidate
// would always choose whichever radio the encoder mangled. The baseline hit
// this and corrected it the same way.
func TestASignalPrintedAsUnsignedIsReadAsNegative(t *testing.T) {
	cases := map[float64]int{
		-52:        -52,
		4294967244: -52, // 2^32 - 52
		4294967169: -127,
		-127:       -127,
		-128:       0, // outside what a radio reports
		0:          0,
		12:         0, // a positive dBm reading is not a measurement
		4294967296: 0, // 2^32 exactly: not the wrapped form of anything
		2147483647: 0,
	}
	for raw, want := range cases {
		if got := normalizeSignal(raw); got != want {
			t.Errorf("normalizeSignal(%v) = %d, want %d", raw, got, want)
		}
	}
}

// An unassociated client still reports the SSID it is trying to reach on some
// drivers, so the SSID alone proves nothing.
func TestAClientWithNoPeerIsNotAssociated(t *testing.T) {
	cases := map[string]RadioInfo{
		"no BSSID at all":  {SSID: "jxnu_stu"},
		"the null BSSID":   {SSID: "jxnu_stu", BSSID: "00:00:00:00:00:00"},
		"no SSID either":   {BSSID: "02:00:5e:00:53:00"},
		"nothing reported": {},
	}
	for name, info := range cases {
		if info.Associated() {
			t.Errorf("%s: reported as associated", name)
		}
	}
}

// Output that is not a status must not become an empty one, which would read as
// a radio that is present but idle.
func TestUnreadableRadioInfoIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"empty":          "",
		"a ubus refusal": "Command failed: Not found",
		"a fragment":     `{"ssid": `,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRadioInfo("phy1-sta0", []byte(body)); err == nil {
				t.Error("unreadable output parsed successfully")
			}
		})
	}
}

// The device name reaches ubus inside a JSON object, so it is checked against
// the kernel's character set before it can become one.
func TestAnImpossibleDeviceNameNeverReachesUbus(t *testing.T) {
	runner := &recordingRunner{}
	adapter := NewAdapter(runner)

	for _, device := range []string{"", "phy1 sta0", `phy1","x":"`, "$(id)",
		"phy1\nsta0"} {
		if _, err := adapter.RadioInfo(t.Context(), device); err == nil {
			t.Errorf("RadioInfo(%q) succeeded", device)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("a name that is not a device was passed to ubus: %v", runner.calls)
	}
}

// The request really is one JSON argument, built by the encoder rather than by
// string concatenation.
func TestTheIwinfoRequestIsOneEncodedArgument(t *testing.T) {
	runner := &recordingRunner{}
	runner.respond(testdata(t, "kwrt-25.12/iwinfo-sta-open.json"),
		"ubus", "call", "iwinfo", "info", `{"device":"phy1-sta0"}`)

	adapter := NewAdapter(runner)
	if _, err := adapter.RadioInfo(t.Context(), "phy1-sta0"); err != nil {
		t.Fatalf("RadioInfo: %v", err)
	}
	if len(runner.calls) != 1 || len(runner.calls[0]) != 5 {
		t.Fatalf("calls = %v", runner.calls)
	}
	if got := runner.calls[0][4]; got != `{"device":"phy1-sta0"}` {
		t.Errorf("request = %s", got)
	}
}

// An access point radio is read too, because a change has to be checked against
// what the home network looked like before it.
func TestAnAccessPointRadioIsAlsoReadable(t *testing.T) {
	info, err := ParseRadioInfo("phy0-ap0", testdata(t, "kwrt-25.12/iwinfo-ap.json"))
	if err != nil {
		t.Fatalf("ParseRadioInfo: %v", err)
	}
	if info.Mode != "Master" {
		t.Errorf("Mode = %q, want Master for an access point", info.Mode)
	}
	if !info.Encrypted {
		t.Error("the home access point is protected and was read as open")
	}
}

// A missing iwinfo is a capability this firmware does not have.
func TestScanningWithoutIwinfoIsUnsupported(t *testing.T) {
	capabilities := Detect(t.Context(),
		&recordingRunner{missing: []string{"iwinfo", "apk", "opkg"}})

	err := capabilities.Require(ToolIwinfo)
	if err == nil {
		t.Fatal("Require accepted a missing iwinfo")
	}
	if code := codeOf(t, err); code != domain.CodeUnsupportedCapability {
		t.Errorf("code = %s, want UnsupportedCapability", code)
	}
}
