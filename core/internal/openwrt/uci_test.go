package openwrt

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "openwrt", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	// The fixtures are byte-for-byte device output and .gitattributes pins them
	// to LF. A checkout that rewrote the line endings would change what is
	// being parsed, so the mismatch is reported here rather than as a puzzling
	// failure in a value comparison.
	if bytes.Contains(data, []byte("\r")) {
		t.Fatalf("%s contains CR; the fixture is no longer what the device "+
			"produced", name)
	}
	return data
}

// T28 -- the parser against real uci output, with real uci's own answers.
//
// The fixture came off a device: uci_roundtrip_probe.sh wrote values that are
// hard to quote into a sandbox configuration, captured `uci show`, and recorded
// each value separately through `uci get`, which does no quoting at all. So the
// expectation is uci's, not mine, and the test cannot pass by agreeing with my
// guess about the quoting rules.
func TestTheParserReproducesWhatRealUCIStored(t *testing.T) {
	config, err := ParseUCIShow("wireless",
		testdata(t, "uci-awkward/uci-show-wireless.txt"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	var expected struct {
		Options map[string]string `json:"options"`
	}
	if err := json.Unmarshal(testdata(t, "uci-awkward/expected.json"), &expected); err != nil {
		t.Fatalf("read expectations: %v", err)
	}
	if len(expected.Options) == 0 {
		t.Fatal("no expectations recorded; the fixture proves nothing")
	}

	for key, want := range expected.Options {
		sectionName, option, _ := cutLast(key, ".")
		section, ok := config.Section(sectionName)
		if !ok {
			t.Errorf("section %q is missing", sectionName)
			continue
		}
		if got := section.Get(option); got != want {
			t.Errorf("%s\n got %q\nwant %q", key, got, want)
		}
	}
}

func cutLast(text, separator string) (string, string, bool) {
	for index := len(text) - len(separator); index >= 0; index-- {
		if text[index:index+len(separator)] == separator {
			return text[:index], text[index+len(separator):], true
		}
	}
	return text, "", false
}

// T28 -- the specific failure a line-based reader has.
//
// uci prints a value containing a newline with that newline in it, so the
// second physical line of a wireless key can be shaped exactly like an option
// assignment. Reading line by line takes it as one, and an SSID the user never
// entered replaces the one they did -- decided by the contents of a key that
// nothing validates.
func TestAValueContainingANewlineDoesNotBecomeAnotherOption(t *testing.T) {
	config, err := ParseUCIShow("wireless",
		testdata(t, "uci-awkward/uci-show-wireless.txt"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	section, ok := config.Section("awkward")
	if !ok {
		t.Fatal("section awkward is missing")
	}
	if ssid := section.Get("ssid"); ssid != "Bob's Cafe" {
		t.Errorf("ssid = %q; the newline inside the key overwrote it", ssid)
	}
	if key := section.Get("key"); key != "line1\nwireless.awkward.ssid='HIJACKED'" {
		t.Errorf("key = %q; the value was truncated at the newline", key)
	}
	// And nothing invented a section or an option out of the second line.
	for _, name := range section.OptionNames() {
		if name != "device" && name != "mode" && name != "ssid" && name != "key" {
			t.Errorf("unexpected option %q parsed out of a multi-line value", name)
		}
	}
}

// A backslash inside uci's quoting is literal. It really does print
// 'ends-with-backslash\', so a parser that treats it as an escape swallows the
// closing quote and mis-reads the rest of the document.
func TestABackslashInsideQuotesIsNotAnEscape(t *testing.T) {
	config, err := ParseUCIShow("wireless",
		testdata(t, "uci-awkward/uci-show-wireless.txt"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	section, ok := config.Section("awkward2")
	if !ok {
		t.Fatal("section awkward2 is missing")
	}
	if ssid := section.Get("ssid"); ssid != `ends-with-backslash\` {
		t.Errorf("ssid = %q, want a trailing backslash", ssid)
	}
	// If the closing quote had been eaten, this option would be missing or
	// would have absorbed the following line.
	if got := section.Get("encryption"); got != "psk2" {
		t.Errorf("encryption = %q; the previous value ran past its quote", got)
	}
	if got := section.Get("key"); got != "$(id); `whoami` | tee /tmp/pwn" {
		t.Errorf("key = %q", got)
	}
}

// Lists are space-separated quoted tokens on one logical record, and a space
// inside a value is quoted -- so splitting on whitespace is wrong in exactly
// the case that matters.
func TestAListIsSplitOnUnquotedSpacesOnly(t *testing.T) {
	config, err := ParseUCIShow("wireless",
		testdata(t, "uci-awkward/uci-show-wireless.txt"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	section, ok := config.Section("listy")
	if !ok {
		t.Fatal("section listy is missing")
	}
	value, ok := section.Lookup("frequency")
	if !ok {
		t.Fatal("the list option is missing")
	}
	if !value.IsList {
		t.Fatalf("frequency was read as a string %q", value.Text)
	}
	// item0..item2 are the same three values written as separate scalars, so
	// the device confirmed each one independently.
	want := []string{section.Get("item0"), section.Get("item1"), section.Get("item2")}
	if !slices.Equal(value.List, want) {
		t.Errorf("list = %q, want %q", value.List, want)
	}

	// Asking for a list as a string answers with its first item, which is what
	// uci's own get does for the single-item case, rather than with a joined
	// string that could not be told apart from one value containing spaces.
	if got := section.Get("frequency"); got != want[0] {
		t.Errorf("Get on a list = %q, want its first item %q", got, want[0])
	}
	empty := UCISection{options: map[string]UCIValue{
		"nothing": {IsList: true},
	}}
	if got := empty.Get("nothing"); got != "" {
		t.Errorf("Get on an empty list = %q, want the empty string", got)
	}
}

// T26 -- radios and clients out of a real firmware's wireless configuration.
func TestARealWirelessConfigurationParsesIntoRadiosAndClients(t *testing.T) {
	config, err := ParseUCIShow("wireless",
		testdata(t, "kwrt-25.12/uci-show-wireless.txt"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	radios := Radios(config)
	if len(radios) != 2 {
		t.Fatalf("found %d radios, want 2: %+v", len(radios), radios)
	}
	if radios[0].Name != "radio0" || radios[0].Band != "2g" {
		t.Errorf("radio0 = %+v", radios[0])
	}
	if radios[1].Name != "radio1" || radios[1].Band != "5g" {
		t.Errorf("radio1 = %+v", radios[1])
	}

	stations := Stations(config)
	if len(stations) != 2 {
		t.Fatalf("found %d clients, want 2: %+v", len(stations), stations)
	}
	campus := stations[1]
	if campus.SSID != "jxnu_stu" || campus.Radio != "radio1" || campus.Disabled {
		t.Errorf("the campus client is wrong: %+v", campus)
	}
	if !campus.Managed {
		t.Error("the client carrying this project's marker was not recognised " +
			"as ours; a second one would be created beside it")
	}
	if campus.APSelection != "auto" {
		t.Errorf("AP selection = %q, want auto", campus.APSelection)
	}
}

// The reader is not wireless-specific: /etc/config/network is the other package
// this program has to understand, and it uses section types of its own.
func TestTheSameReaderHandlesAnotherPackage(t *testing.T) {
	config, err := ParseUCIShow("network",
		testdata(t, "kwrt-25.12/uci-show-network.txt"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	interfaces := config.SectionsOfType("interface")
	if len(interfaces) < 2 {
		t.Fatalf("found %d interface sections: %+v", len(interfaces), interfaces)
	}
	for _, section := range interfaces {
		// These names are what would be handed to ubus, so each has to be one
		// that could be.
		if !IsLogicalInterfaceName(section.Name) {
			t.Errorf("interface section %q is not a usable interface name",
				section.Name)
		}
	}
	lan, ok := config.Section("lan")
	if !ok {
		t.Fatal("the lan interface is missing")
	}
	if lan.Type != "interface" || lan.Get("proto") == "" {
		t.Errorf("lan = %+v with proto %q", lan, lan.Get("proto"))
	}
}

// The check that every committed fixture is read by something lives in
// tests/architecture, not here. It is an assertion about the repository, and
// this package's tests are cross-compiled and run inside a real OpenWrt guest
// where the repository is not present -- a test that needs the source tree
// would fail there for a reason that has nothing to do with the target.

// A section header prints its type unquoted, and the section name is not a hint
// about what it is.
func TestSectionsAreIdentifiedByTypeNotByName(t *testing.T) {
	// mtwifi names its radios after the chip. Selecting by name finds none.
	output := []byte(
		"wireless.MT7981_1_1=wifi-device\n" +
			"wireless.MT7981_1_1.band='2g'\n" +
			"wireless.radio_lookalike=wifi-iface\n" +
			"wireless.radio_lookalike.band='5g'\n" +
			"wireless.radio_lookalike.mode='ap'\n")

	config, err := ParseUCIShow("wireless", output)
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}
	radios := Radios(config)
	if len(radios) != 1 || radios[0].Name != "MT7981_1_1" {
		t.Fatalf("radios = %+v; a wifi-device whose name is not radioN must "+
			"still be found, and a wifi-iface with a band option must not", radios)
	}
	if radios[0].Band != "2g" {
		t.Errorf("band = %q, want 2g", radios[0].Band)
	}
}

// uci names a section the user never named, and the two spellings it uses have
// to be recognised so a rewrite does not treat one as a section to keep.
func TestAnonymousSectionsAreRecognised(t *testing.T) {
	cases := map[string]bool{
		"cfg01411c":       true,
		"cfg030f15":       true,
		"@wifi-iface[0]":  true,
		"radio0":          false,
		"cfg":             false,
		"cfgnothex":       false,
		"jxnu_sta_radio0": false,
		"default_radio1":  false,
	}
	for name, want := range cases {
		if got := isAnonymousSectionName(name); got != want {
			t.Errorf("isAnonymousSectionName(%q) = %v, want %v", name, got, want)
		}
	}
}

// T28 -- whether there are uncommitted changes decides whether this program may
// touch the wireless configuration at all, so the reader has to be exact.
func TestPendingChangesAreReadWithTheSameQuotingRules(t *testing.T) {
	// Captured shapes: a set, a new section, a multi-line value, a delete with
	// no value at all, and an append to a list.
	output := []byte("wireless.radio0.channel='11'\n" +
		"wireless.newsec='wifi-iface'\n" +
		"wireless.newsec.ssid='has'\\''quote and\nnewline'\n" +
		"-wireless.radio0.channel\n" +
		"wireless.newsec.freq+='2412'\n")

	changes, err := ParseUCIChanges(output)
	if err != nil {
		t.Fatalf("ParseUCIChanges: %v", err)
	}
	want := []UCIChange{
		{Kind: UCIChangeSet, Key: "wireless.radio0.channel", Value: "11"},
		{Kind: UCIChangeSet, Key: "wireless.newsec", Value: "wifi-iface"},
		{Kind: UCIChangeSet, Key: "wireless.newsec.ssid",
			Value: "has'quote and\nnewline"},
		{Kind: UCIChangeDelete, Key: "wireless.radio0.channel"},
		{Kind: UCIChangeAppend, Key: "wireless.newsec.freq", Value: "2412"},
	}
	if !slices.Equal(changes, want) {
		t.Errorf("changes =\n%+v\nwant\n%+v", changes, want)
	}
}

// A clean package reports nothing, which is the answer that grants permission
// to proceed -- so it must come from an empty document, not from a parse that
// gave up.
func TestNoPendingChangesIsAnEmptyResultAndNotAnError(t *testing.T) {
	for _, output := range []string{"", "\n", "   \n"} {
		changes, err := ParseUCIChanges([]byte(output))
		if err != nil {
			t.Fatalf("ParseUCIChanges(%q): %v", output, err)
		}
		if len(changes) != 0 {
			t.Errorf("ParseUCIChanges(%q) = %+v, want none", output, changes)
		}
	}
}

// Output the reader cannot account for must not come back as "no changes".
//
// The bare "=" case came from the fuzzer: it produced an empty change list and
// no error, so a document that plainly had something in it read as a clean
// package -- which is the answer that lets this program start rewriting the
// wireless configuration.
func TestUnreadableChangeOutputIsAnErrorRatherThanSilence(t *testing.T) {
	for _, output := range []string{
		"wireless.radio0.channel='unterminated\n",
		"this line is not a change at all\n",
		"=",
		"=value\n",
		"  =x\n",
		"+='2412'\n",
		"-\n",
	} {
		if _, err := ParseUCIChanges([]byte(output)); err == nil {
			t.Errorf("ParseUCIChanges(%q) reported no problem; an empty result "+
				"is read as permission to modify the configuration", output)
		}
	}
}

// Malformed show output is refused rather than half-read, for the same reason:
// a configuration that is missing the section it could not parse looks like a
// configuration that never had it.
func TestMalformedShowOutputIsRefused(t *testing.T) {
	cases := map[string]string{
		"unterminated quote": "wireless.radio0.band='2g\n",
		"no assignment":      "wireless.radio0\n",
		"another package":    "network.lan.proto='dhcp'\n",
		"empty section name": "wireless..band='2g'\n",
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseUCIShow("wireless", []byte(output)); err == nil {
				t.Error("accepted output it could not fully read")
			}
		})
	}
}

// An option that is absent and one set to the empty string are different: uci
// writes no line at all for the empty one, so "not configured" has to stay
// distinguishable from "configured as nothing".
func TestAbsentAndEmptyAreDistinguishable(t *testing.T) {
	config, err := ParseUCIShow("wireless", []byte(
		"wireless.sta=wifi-iface\nwireless.sta.ssid=''\n"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}
	section, _ := config.Section("sta")

	if value, ok := section.Lookup("ssid"); !ok || value.Text != "" {
		t.Errorf("an explicitly empty option was lost: %+v, present=%v", value, ok)
	}
	if _, ok := section.Lookup("key"); ok {
		t.Error("an option that was never written came back as present")
	}
}

// A parsed configuration handed out by value must not be a window onto the
// original. This is the aliasing that a struct copy does not prevent.
func TestASectionHandedOutCannotChangeTheConfiguration(t *testing.T) {
	config, err := ParseUCIShow("wireless",
		testdata(t, "kwrt-25.12/uci-show-wireless.txt"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	section, _ := config.Section("jxnu_sta_radio1")
	names := section.OptionNames()
	names[0] = "tampered"

	again, _ := config.Section("jxnu_sta_radio1")
	if again.OptionNames()[0] == "tampered" {
		t.Error("editing a returned option list changed the parsed configuration")
	}
}
