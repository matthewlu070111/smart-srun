package openwrt

import (
	"slices"
	"testing"
)

func wirelessFrom(t *testing.T, fixture string) UCIConfig {
	t.Helper()
	config, err := ParseUCIShow("wireless", testdata(t, fixture))
	if err != nil {
		t.Fatalf("ParseUCIShow(%s): %v", fixture, err)
	}
	return config
}

// T26 -- the home access points must be visible and must be nobody's business
// but the owner's.
//
// Spec 04 forbids touching them. Proving that later needs a record of what they
// were, which is why they are read at all; the value of this test is that the
// sections this project owns and the ones it must not are separated by the
// marker and the SSID list, not by a name pattern that a user could collide
// with by naming their network the wrong thing.
func TestTheHomeAccessPointsAreReadButNeverClaimed(t *testing.T) {
	config := wirelessFrom(t, "kwrt-25.12/uci-show-wireless.txt")

	points := AccessPoints(config)
	if len(points) != 2 {
		t.Fatalf("found %d access points, want 2: %+v", len(points), points)
	}
	for _, point := range points {
		if point.SSID == "" || point.Encryption == "" {
			t.Errorf("access point read incompletely: %+v", point)
		}
	}

	// None of them may appear as something this project manages, even though
	// one of them shares a radio with a client that this project does own.
	managed := ManagedStations(config, "", []string{"jxnu_stu"})
	for _, station := range managed {
		for _, point := range points {
			if station.Section == point.Section {
				t.Errorf("access point %s was claimed as a managed client",
					point.Section)
			}
		}
	}
}

// T26 -- what makes a client this project's.
//
// The marker is what this program wrote. The SSID list adopts a client the user
// built by hand for a network the configuration names, which is what stops a
// second client being created beside a working one and the two fighting over
// the radio. Everything else on the radio belongs to somebody else.
func TestOnlyMarkedOrConfiguredClientsAreManaged(t *testing.T) {
	config, err := ParseUCIShow("wireless", []byte(
		"wireless.radio0=wifi-device\n"+
			"wireless.ours=wifi-iface\n"+
			"wireless.ours.device='radio0'\n"+
			"wireless.ours.mode='sta'\n"+
			"wireless.ours.ssid='campus'\n"+
			"wireless.ours.jxnu_auto='1'\n"+
			"wireless.byhand=wifi-iface\n"+
			"wireless.byhand.device='radio0'\n"+
			"wireless.byhand.mode='sta'\n"+
			"wireless.byhand.ssid='campus-other'\n"+
			"wireless.neighbour=wifi-iface\n"+
			"wireless.neighbour.device='radio0'\n"+
			"wireless.neighbour.mode='sta'\n"+
			"wireless.neighbour.ssid='someone-elses-uplink'\n"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	managed := ManagedStations(config, "", []string{"campus-other"})
	var sections []string
	for _, station := range managed {
		sections = append(sections, station.Section)
	}
	slices.Sort(sections)

	want := []string{"byhand", "ours"}
	if !slices.Equal(sections, want) {
		t.Errorf("managed = %v, want %v; the neighbour's client must never be "+
			"adopted", sections, want)
	}
}

// The explicitly chosen section counts as ours even when it carries no marker
// and its SSID is not in the configuration -- that is what sta_iface is for.
func TestAnExplicitlyChosenSectionIsManaged(t *testing.T) {
	config, err := ParseUCIShow("wireless", []byte(
		"wireless.chosen=wifi-iface\n"+
			"wireless.chosen.device='radio0'\n"+
			"wireless.chosen.mode='sta'\n"+
			"wireless.chosen.ssid='unlisted'\n"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	if managed := ManagedStations(config, "chosen", nil); len(managed) != 1 {
		t.Fatalf("managed = %+v, want the chosen section", managed)
	}
	if managed := ManagedStations(config, "", nil); len(managed) != 0 {
		t.Errorf("managed = %+v with nothing chosen, want none", managed)
	}
}

// T26 -- two managed clients on one radio is an ambiguity, not a tie.
//
// It means an earlier run left one behind. Picking either could disable the one
// that is actually carrying traffic, so the caller is told to resolve it.
func TestTwoManagedClientsOnOneRadioAreReportedAsAmbiguous(t *testing.T) {
	// The February firmware really has three client sections on radio1, two of
	// them carrying this project's marker.
	config := wirelessFrom(t, "kwrt-25.12-feb/uci-show-wireless.txt")
	managed := ManagedStations(config, "", []string{"jxnu_stu"})

	station, ambiguous := StationOnRadio(managed, "radio1")
	if len(ambiguous) < 2 {
		t.Fatalf("expected an ambiguity on radio1, got station %+v and %d "+
			"candidates", station, len(ambiguous))
	}
	if station.Section != "" {
		t.Error("a client was chosen despite the ambiguity")
	}

	// The answer must be the same every time, so a report does not reorder
	// between two runs on the same configuration.
	var first []string
	for _, candidate := range ambiguous {
		first = append(first, candidate.Section)
	}
	for range 5 {
		_, again := StationOnRadio(managed, "radio1")
		var names []string
		for _, candidate := range again {
			names = append(names, candidate.Section)
		}
		if !slices.Equal(names, first) {
			t.Fatalf("candidate order changed between runs: %v then %v",
				first, names)
		}
	}
}

// One client on a radio is answered directly.
func TestASingleClientOnARadioIsChosen(t *testing.T) {
	config := wirelessFrom(t, "kwrt-25.12/uci-show-wireless.txt")
	managed := ManagedStations(config, "", nil)

	station, ambiguous := StationOnRadio(managed, "radio1")
	if len(ambiguous) != 0 {
		t.Fatalf("unexpected ambiguity: %+v", ambiguous)
	}
	if station.Section != "jxnu_sta_radio1" || station.SSID != "jxnu_stu" {
		t.Errorf("station = %+v", station)
	}
}

// A radio with nothing of ours on it answers with nothing, rather than with the
// first client it finds.
func TestARadioWithNoManagedClientAnswersEmpty(t *testing.T) {
	config := wirelessFrom(t, "kwrt-25.12/uci-show-wireless.txt")
	managed := ManagedStations(config, "", nil)

	station, ambiguous := StationOnRadio(managed, "radio_that_does_not_exist")
	if station.Section != "" || len(ambiguous) != 0 {
		t.Errorf("station = %+v, candidates = %+v", station, ambiguous)
	}
}

// The older firmwares wrote hwmode instead of band, and a radio with no band at
// all must not be silently called 2.4 GHz.
func TestTheBandFallsBackToHWModeAndOtherwiseStaysUnknown(t *testing.T) {
	config, err := ParseUCIShow("wireless", []byte(
		"wireless.old5=wifi-device\nwireless.old5.hwmode='11a'\n"+
			"wireless.old2=wifi-device\nwireless.old2.hwmode='11g'\n"+
			"wireless.blank=wifi-device\n"+
			"wireless.both=wifi-device\nwireless.both.band='6g'\n"+
			"wireless.both.hwmode='11a'\n"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}

	bands := map[string]string{}
	for _, radio := range Radios(config) {
		bands[radio.Name] = radio.Band
	}
	want := map[string]string{"old5": "5g", "old2": "2g", "blank": "", "both": "6g"}
	for name, expected := range want {
		if bands[name] != expected {
			t.Errorf("%s band = %q, want %q", name, bands[name], expected)
		}
	}
}

// A client's disabled flag decides whether it is doing anything, and it is
// exactly "1" -- not any non-empty value, which would make disabled='0' mean
// disabled.
func TestTheDisabledFlagIsReadExactly(t *testing.T) {
	config := wirelessFrom(t, "kwrt-25.12/uci-show-wireless.txt")
	states := map[string]bool{}
	for _, station := range Stations(config) {
		states[station.Section] = station.Disabled
	}
	if !states["jxnu_sta_radio0"] {
		t.Error("jxnu_sta_radio0 has disabled='1' and must read as disabled")
	}
	if states["jxnu_sta_radio1"] {
		t.Error("jxnu_sta_radio1 has disabled='0' and must read as enabled")
	}
}

// A section's mode decides what it is, and the comparison is case-insensitive
// because both spellings occur in the wild.
func TestModeIsMatchedWithoutRegardToCase(t *testing.T) {
	config, err := ParseUCIShow("wireless", []byte(
		"wireless.a=wifi-iface\nwireless.a.mode='STA'\nwireless.a.ssid='x'\n"+
			"wireless.b=wifi-iface\nwireless.b.mode=' Ap '\nwireless.b.ssid='y'\n"+
			"wireless.c=wifi-iface\nwireless.c.mode='monitor'\n"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}
	if stations := Stations(config); len(stations) != 1 || stations[0].Section != "a" {
		t.Errorf("stations = %+v", stations)
	}
	if points := AccessPoints(config); len(points) != 1 || points[0].Section != "b" {
		t.Errorf("access points = %+v", points)
	}
}

// A network option may list several names; the first is the one the interface
// is on.
func TestTheFirstNetworkNameIsTaken(t *testing.T) {
	config, err := ParseUCIShow("wireless", []byte(
		"wireless.s=wifi-iface\nwireless.s.mode='sta'\n"+
			"wireless.s.network='wwan lan'\n"))
	if err != nil {
		t.Fatalf("ParseUCIShow: %v", err)
	}
	if network := Stations(config)[0].Network; network != "wwan" {
		t.Errorf("network = %q, want wwan", network)
	}
}
