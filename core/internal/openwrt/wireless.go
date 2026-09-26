package openwrt

import (
	"cmp"
	"slices"
	"strings"
)

// UCI section types. These are what identifies a radio or a client, not the
// section's name.
const (
	SectionWifiDevice = "wifi-device"
	SectionWifiIface  = "wifi-iface"
)

// ManagedMarker is the option this project writes on the wireless sections it
// owns. The spelling is deliberately unchanged from 1.x: an installed router
// already has sections marked this way, and renaming the marker would make this
// program stop recognising its own client and create a second one beside it.
const ManagedMarker = "jxnu_auto"

// APSelectionOption records the AP-selection policy on a managed client.
const APSelectionOption = "smart_srun_ap_selection"

// Radio is one wifi-device: the physical radio a client can be attached to.
type Radio struct {
	Name    string
	Band    string
	Channel string
	HTMode  string
	Path    string
}

// Station is one wifi-iface in client mode.
type Station struct {
	Section    string
	Radio      string
	SSID       string
	BSSID      string
	Encryption string
	Network    string
	Disabled   bool
	Anonymous  bool

	// Managed reports that this section belongs to this project. Everything
	// else on the radio is somebody's home network and must come back
	// unchanged.
	Managed     bool
	APSelection string
}

// AccessPoint is one wifi-iface in AP mode -- a network this router serves.
//
// This program never configures one. They are read so that a change can be
// checked against them afterwards: spec 04 forbids touching the home AP, and
// proving that requires knowing what it looked like before.
type AccessPoint struct {
	Section    string
	Radio      string
	SSID       string
	Encryption string
	Network    string
	Disabled   bool
}

// Radios lists the radios in a parsed wireless configuration.
//
// Selection is by section type. mac80211 names them radio0 and radio1, but
// MediaTek's closed mtwifi driver on MT798x boards names them MT7981_1_1 and
// MT7981_1_2, and matching on the name finds no radios there at all. The band
// comes from `band`, or from the older `hwmode` when a firmware still writes
// that -- and only from a section that really is a wifi-device, so a wifi-iface
// with an option of the same name cannot invent a radio.
func Radios(config UCIConfig) []Radio {
	sections := config.SectionsOfType(SectionWifiDevice)
	radios := make([]Radio, 0, len(sections))
	for _, section := range sections {
		radio := Radio{
			Name:    section.Name,
			Band:    strings.ToLower(section.Get("band")),
			Channel: section.Get("channel"),
			HTMode:  section.Get("htmode"),
			Path:    section.Get("path"),
		}
		if radio.Band == "" {
			radio.Band = bandFromHWMode(section.Get("hwmode"))
		}
		radios = append(radios, radio)
	}
	return radios
}

// bandFromHWMode maps the pre-band spelling. "11a" and "11na" are 5 GHz;
// everything else that firmware wrote was 2.4.
func bandFromHWMode(hwmode string) string {
	mode := strings.ToLower(strings.TrimSpace(hwmode))
	if mode == "" {
		return ""
	}
	if strings.Contains(mode, "a") {
		return "5g"
	}
	return "2g"
}

// Stations lists the client sections, in file order.
func Stations(config UCIConfig) []Station {
	var stations []Station
	for _, section := range config.SectionsOfType(SectionWifiIface) {
		if !strings.EqualFold(strings.TrimSpace(section.Get("mode")), "sta") {
			continue
		}
		stations = append(stations, Station{
			Section:     section.Name,
			Radio:       section.Get("device"),
			SSID:        section.Get("ssid"),
			BSSID:       strings.ToLower(section.Get("bssid")),
			Encryption:  section.Get("encryption"),
			Network:     firstNetwork(section.Get("network")),
			Disabled:    section.Get("disabled") == "1",
			Anonymous:   section.Anonymous,
			Managed:     section.Get(ManagedMarker) == "1",
			APSelection: section.Get(APSelectionOption),
		})
	}
	return stations
}

// AccessPoints lists the AP sections, in file order.
func AccessPoints(config UCIConfig) []AccessPoint {
	var points []AccessPoint
	for _, section := range config.SectionsOfType(SectionWifiIface) {
		if !strings.EqualFold(strings.TrimSpace(section.Get("mode")), "ap") {
			continue
		}
		points = append(points, AccessPoint{
			Section:    section.Name,
			Radio:      section.Get("device"),
			SSID:       section.Get("ssid"),
			Encryption: section.Get("encryption"),
			Network:    firstNetwork(section.Get("network")),
			Disabled:   section.Get("disabled") == "1",
		})
	}
	return points
}

// firstNetwork takes the first name from a `network` option, which UCI allows
// to list several.
func firstNetwork(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// ManagedStations narrows to the sections this project owns.
//
// A section counts as owned when it carries the marker, or when its SSID is one
// the configuration names. The SSID rule is what adopts a client the user
// created by hand for the campus network before installing this, instead of
// building a second one beside it and having two clients fight over the radio.
// It is also the rule that must not be loosened: a home network whose SSID is
// not in the configuration is never adopted.
func ManagedStations(config UCIConfig, preferredSection string, knownSSIDs []string) []Station {
	known := make(map[string]struct{}, len(knownSSIDs))
	for _, ssid := range knownSSIDs {
		if trimmed := strings.TrimSpace(ssid); trimmed != "" {
			known[trimmed] = struct{}{}
		}
	}

	var managed []Station
	for _, station := range Stations(config) {
		_, namedInConfig := known[strings.TrimSpace(station.SSID)]
		if station.Managed || namedInConfig || station.Section == preferredSection {
			station.Managed = true
			managed = append(managed, station)
		}
	}
	return managed
}

// StationOnRadio picks the managed client attached to one radio.
//
// More than one is an ambiguity, not a tie to break: two managed clients on the
// same radio mean an earlier run left one behind, and picking either could
// disable a working uplink. The caller is told to resolve it. The result is
// otherwise deterministic -- sorted by section name -- so the same
// configuration always produces the same answer.
func StationOnRadio(stations []Station, radio string) (Station, []Station) {
	var candidates []Station
	for _, station := range stations {
		if station.Radio == radio {
			candidates = append(candidates, station)
		}
	}
	slices.SortFunc(candidates, func(a, b Station) int {
		return cmp.Compare(a.Section, b.Section)
	})
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return Station{}, candidates
}

// Deliberately absent: matching a configured encryption against what a scan
// reports. Spec 07 puts "加密匹配" in M09 along with AP selection and the
// association check, and the useful part of that rule -- never joining an open
// network because it shares a name with the protected one -- needs the scan
// results M09 introduces to mean anything. Writing the taxonomy here would put
// a guess in the tree that M09 would inherit as if it had been verified.
