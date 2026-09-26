package daemon

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

// This is the other half of the assembly worker.go describes: application says
// what it needs of a radio, wireless knows how to change one safely, openwrt
// knows how to read the device, and none of the three may import the others.
// Putting them together is this package's job and nowhere else's.

// WirelessPackage is the uci package the client sections live in.
const WirelessPackage = "wireless"

// SettleWait is how long a change has to produce a real association and an
// address before it is undone.
//
// Spec 04: 最长60秒等待实际SSID和IPv4. It is a ceiling rather than a delay --
// the wait returns as soon as the line is up, which on a healthy network is a
// few seconds.
const SettleWait = 60 * time.Second

// SettlePoll is how often the wait looks.
const SettlePoll = 2 * time.Second

// deviceWireless is application.Wireless over a real router.
type deviceWireless struct {
	adapter     *openwrt.Adapter
	store       wireless.Store
	wizardStore wireless.Store // Test override; production also reloads firewall.
	paths       wireless.Paths
	settings    application.Settings
	clock       policy.Clock

	settleWait time.Duration
	settlePoll time.Duration

	// One wireless change at a time, for the whole process.
	//
	// The coordinator's scheduling key already serialises the actions that
	// touch a radio, and that is not the same guarantee: it is a queue for
	// actions, and recovery at startup is not an action. Spec 04 asks for a
	// global wireless transaction lock as well as the serial rule, and the
	// review of d33528a made a point of saying one does not replace the other.
	mu sync.Mutex

	// tasks numbers the transactions, so two changes in the same second do not
	// share a journal identity.
	tasks uint64
}

// wirelessOptions is what the daemon has to hand when it builds one.
type wirelessOptions struct {
	Adapter  *openwrt.Adapter
	Store    wireless.Store
	Paths    wireless.Paths
	Settings application.Settings
	Clock    policy.Clock
	// SettleWait and SettlePoll default to the constants above. Tests set them
	// so a sixty-second ceiling does not cost sixty seconds.
	SettleWait time.Duration
	SettlePoll time.Duration
}

func newDeviceWireless(options wirelessOptions) *deviceWireless {
	w := &deviceWireless{
		adapter:    options.Adapter,
		store:      options.Store,
		paths:      options.Paths,
		settings:   options.Settings,
		clock:      options.Clock,
		settleWait: options.SettleWait,
		settlePoll: options.SettlePoll,
	}
	if w.settleWait <= 0 {
		w.settleWait = SettleWait
	}
	if w.settlePoll <= 0 {
		w.settlePoll = SettlePoll
	}
	return w
}

// Association is what the managed client on one radio is doing right now.
//
// Two steps, because the radio name and the device name iwinfo answers to are
// not related. A client on radio1 might be phy1-sta0, and a disabled section
// has no device at all -- measured on a real router, where `iwinfo devices`
// listed only the access points because every station section was disabled. So
// the interface is looked up through netifd, which knows which UCI section
// produced which device.
func (w *deviceWireless) Association(ctx context.Context, radio string) (
	wifi.Association, error) {

	radios, err := w.adapter.WirelessStatus(ctx)
	if err != nil {
		return wifi.Association{}, err
	}
	found, known := openwrt.FindRadio(radios, radio)
	if !known {
		return wifi.Association{}, domain.FieldErrorf(domain.CodeInvalidConfig,
			"radio", "系统里没有无线电 %s", radio)
	}
	iface, running := found.FindInterface(stationSection(radio))
	if !found.Up || found.Pending || found.Disabled || found.RetrySetupFailed || !running || !iface.Station() || iface.IfName == "" {
		// The section is disabled, or netifd has not brought it up. Not being
		// associated is an answer; reporting it as a failure would make every
		// first connection look like a broken radio.
		return wifi.Association{}, nil
	}

	info, err := w.adapter.RadioInfo(ctx, iface.IfName)
	if err != nil {
		return wifi.Association{}, err
	}
	if !info.Associated() {
		return wifi.Association{}, nil
	}
	association := wifi.Association{SSID: info.SSID, BSSID: info.BSSID, Encrypted: info.Encrypted}
	if !association.Joined() {
		// No point asking netifd for an address on a client that joined
		// nothing, and an address left over from the previous network would
		// make one look usable.
		return association, nil
	}
	association.HasIPv4 = w.hasAddress(ctx, firstNetwork(iface.Network), iface.IfName)
	return association, nil
}

// Scan lists what the radio can see, as candidates the wifi package can choose
// between.
func (w *deviceWireless) Scan(ctx context.Context, radio string) (
	[]wifi.Candidate, error) {

	radios, err := w.adapter.WirelessStatus(ctx)
	if err != nil {
		return nil, err
	}
	found, known := openwrt.FindRadio(radios, radio)
	if !known {
		return nil, domain.FieldErrorf(domain.CodeInvalidConfig,
			"radio", "系统里没有无线电 %s", radio)
	}
	device, usable := found.AnyDevice()
	if !usable {
		// A radio with nothing up on it cannot scan. Saying so beats an empty
		// candidate list, which the selection would read as "the access point
		// you pinned is gone".
		return nil, domain.Errorf(domain.CodeUnsupportedCapability,
			"无线电 %s 上没有已启用的接口，无法扫描", radio)
	}

	results, err := w.adapter.Scan(ctx, device)
	if err != nil {
		return nil, err
	}
	return toCandidates(radio, results), nil
}

// toCandidates converts what the adapter saw into what wifi decides on.
//
// This lives here rather than in either package because neither may import the
// other: wifi decides from values and must be testable on a machine with no
// radio, so openwrt cannot reach it, and application cannot reach openwrt.
//
// The security classification is wifi's, not this function's. A mixed-mode
// access point advertising both psk and sae admits two kinds of client, and any
// judgement made here would be a second, quieter copy of a taxonomy that is
// already written down and tested.
func toCandidates(radio string, results []openwrt.ScanResult) []wifi.Candidate {
	candidates := make([]wifi.Candidate, 0, len(results))
	for _, result := range results {
		if !result.Joinable() {
			// Ad-hoc and mesh peers come back in the same array, with signal
			// readings and an SSID. Nothing further down would notice.
			continue
		}
		candidates = append(candidates, wifi.Candidate{
			SSID:   result.SSID,
			BSSID:  result.BSSID,
			Signal: result.Signal,
			Security: wifi.ClassifyScan(result.Encryption.Enabled,
				result.Encryption.Authentication, result.Encryption.WPA),
			Radio: radio,
		})
	}
	return candidates
}

// hasAddress reports whether the logical interface actually carries IPv4.
//
// A failure is "no", not an error: the caller is deciding whether an existing
// association can be reused, and "I could not tell" has to mean "do not reuse
// it". Getting that backwards would authenticate over a line with no way out.
func (w *deviceWireless) hasAddress(ctx context.Context, iface, device string) bool {
	if iface == "" {
		return false
	}
	status, err := w.adapter.InterfaceStatus(ctx, iface)
	if err != nil {
		return false
	}
	return status.Up && len(status.IPv4) > 0 && device != "" && status.L3Device == device
}

func firstNetwork(names []string) string {
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// stationSection is the UCI section this program manages on one radio.
//
// The spelling is 1.x's and stays that way. An installed router already has a
// section named like this, and choosing a new name would leave the old one
// behind for netifd to bring up beside the new one -- two clients on one radio,
// which is the ambiguity StationOnRadio refuses to resolve.
func stationSection(radio string) string {
	name := sectionUnsafe.ReplaceAllString(strings.TrimSpace(radio), "_")
	if name == "" {
		name = "sta"
	}
	return "jxnu_sta_" + name
}

var sectionUnsafe = regexp.MustCompile(`[^A-Za-z0-9_]+`)
