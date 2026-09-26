package openwrt

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// WirelessRadio is one radio as netifd has it running right now.
//
// The configured state is in UCI and says what should happen; this says what
// netifd made of it. The two disagree exactly when something went wrong, which
// is when this is worth asking.
type WirelessRadio struct {
	Name string
	// Up is netifd's own verdict on the radio, and Pending says it is still
	// working on it. A radio that is pending is neither up nor failed yet, and
	// reading it as either is how a wait ends early.
	Up      bool
	Pending bool
	// Disabled is the configuration's, not a fault.
	Disabled bool
	// RetrySetupFailed means netifd gave up bringing the radio up. Without it,
	// a failed radio looks exactly like one that is merely slow.
	RetrySetupFailed bool
	Interfaces       []WirelessInterface
}

// WirelessInterface is one wifi-iface on that radio.
//
// IfName is the field this exists for: it is the name iwinfo wants, and it
// cannot be derived from anything else. A station section on radio1 might be
// phy1-sta0, and a disabled one has no device at all -- measured on a real
// router, where `iwinfo devices` listed only the two access points because the
// station sections were disabled.
type WirelessInterface struct {
	// Section is the UCI section that produced it, which is how this program
	// finds the interface it owns among the household's.
	Section string
	IfName  string
	Mode    string
	SSID    string
	Network []string
}

// Station reports that this interface is a client rather than an access point.
func (i WirelessInterface) Station() bool {
	return strings.EqualFold(strings.TrimSpace(i.Mode), "sta")
}

// wirelessStatusJSON decodes only the fields above.
//
// Deliberately not a map[string]any, and deliberately not every field. The
// `config` object in this reply contains the wireless passphrase in clear --
// for the household's own access point as well as this program's client. A
// struct that took the whole object would carry that value into anything that
// logged, serialised or reported a radio, and the only reliable way to keep a
// secret out of a log is not to have read it.
type wirelessStatusJSON struct {
	Up               bool `json:"up"`
	Pending          bool `json:"pending"`
	Disabled         bool `json:"disabled"`
	RetrySetupFailed bool `json:"retry_setup_failed"`
	Interfaces       []struct {
		Section string `json:"section"`
		IfName  string `json:"ifname"`
		Config  struct {
			Mode    string   `json:"mode"`
			SSID    string   `json:"ssid"`
			Network []string `json:"network"`
		} `json:"config"`
	} `json:"interfaces"`
}

// ParseWirelessStatus reads `ubus call network.wireless status`.
//
// The reply is an object keyed by radio name, so the radios come back sorted
// rather than in whatever order the encoder used: a caller picking "the first
// radio" would otherwise pick a different one between calls.
func ParseWirelessStatus(data []byte) ([]WirelessRadio, error) {
	if len(data) == 0 {
		return nil, domain.Errorf(domain.CodeInternal,
			"无线状态查询没有返回内容")
	}
	var raw map[string]wirelessStatusJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法解析无线状态").Wrap(err)
	}

	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	radios := make([]WirelessRadio, 0, len(names))
	for _, name := range names {
		entry := raw[name]
		radio := WirelessRadio{
			Name:             name,
			Up:               entry.Up,
			Pending:          entry.Pending,
			Disabled:         entry.Disabled,
			RetrySetupFailed: entry.RetrySetupFailed,
		}
		for _, iface := range entry.Interfaces {
			radio.Interfaces = append(radio.Interfaces, WirelessInterface{
				Section: iface.Section,
				IfName:  iface.IfName,
				Mode:    iface.Config.Mode,
				SSID:    iface.Config.SSID,
				Network: iface.Config.Network,
			})
		}
		radios = append(radios, radio)
	}
	return radios, nil
}

// WirelessStatus asks netifd what it has made of the wireless configuration.
func (a *Adapter) WirelessStatus(ctx context.Context) ([]WirelessRadio, error) {
	result, err := a.runner.Run(ctx, "ubus", "call", "network.wireless", "status")
	if err != nil {
		return nil, err
	}
	if result.StdoutTruncated {
		// Half this reply is half the radios, and the caller uses it to find
		// the interface it owns. A short answer would read as "the interface is
		// not there", which is the one conclusion that must not be guessed.
		return nil, domain.Errorf(domain.CodeInternal,
			"无线状态过长，已截断")
	}
	return ParseWirelessStatus(result.Stdout)
}

// FindRadio returns one radio by name.
func FindRadio(radios []WirelessRadio, name string) (WirelessRadio, bool) {
	for _, radio := range radios {
		if radio.Name == name {
			return radio, true
		}
	}
	return WirelessRadio{}, false
}

// FindInterface returns the interface one UCI section produced.
//
// Absent is an ordinary answer: a disabled section produces no interface, and
// netifd simply does not list one. Reporting that as a failure would make a
// client this program has not enabled yet look like a broken radio.
func (r WirelessRadio) FindInterface(section string) (WirelessInterface, bool) {
	for _, iface := range r.Interfaces {
		if iface.Section == section {
			return iface, true
		}
	}
	return WirelessInterface{}, false
}

// AnyDevice returns a device name on this radio, for operations that need the
// radio rather than a particular client -- a scan, most of all.
//
// A scan needs some interface to be up on the radio, and the one this program
// manages is exactly the one that may not be. Preferring a station when there
// is one keeps the scan on the interface that will do the associating; falling
// back to the access point is what makes scanning work at all on a router whose
// client section is still disabled.
func (r WirelessRadio) AnyDevice() (string, bool) {
	for _, iface := range r.Interfaces {
		if iface.Station() && iface.IfName != "" {
			return iface.IfName, true
		}
	}
	for _, iface := range r.Interfaces {
		if iface.IfName != "" {
			return iface.IfName, true
		}
	}
	return "", false
}
