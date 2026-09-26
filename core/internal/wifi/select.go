package wifi

import (
	"cmp"
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// UnassociatedBSSID is what a radio reports when it is joined to nothing.
//
// Drivers differ on whether an unassociated client reports the SSID it is
// trying to reach, so the BSSID is the field that answers "are we actually on a
// network"; this is the value that means no.
const UnassociatedBSSID = "00:00:00:00:00:00"

// Candidate is one access point a scan reported.
type Candidate struct {
	SSID  string
	BSSID string
	// Signal is dBm and therefore negative. Zero means there was no usable
	// measurement -- not that the signal was excellent.
	Signal   int
	Security Security
	// Radio is the device that saw it. Two radios can see the same access
	// point, and which one is used decides which client section is written.
	Radio string
}

// Measured reports that this candidate carries a usable signal reading.
func (c Candidate) Measured() bool { return c.Signal < 0 }

// Target is what the account asked for.
type Target struct {
	// SSID is compared exactly. Configuration deliberately does not trim it:
	// an SSID may legitimately begin or end with a space, and two networks
	// differing only there are two networks.
	SSID string
	// Security is the set the account's configured encryption belongs to.
	Security Security
	Policy   domain.APSelection
	// PinnedBSSID is only meaningful when Policy is fixed.
	PinnedBSSID string
}

// Decision is the outcome of choosing.
type Decision struct {
	SSID string
	// BSSID is the access point to pin the client section to. Empty means do
	// not pin.
	BSSID string
	// Pinned separates "no BSSID because the policy is auto" from "no BSSID
	// because nothing was found", which would otherwise look identical.
	Pinned bool
	// Chosen is the scan result the decision came from. Zero when nothing was
	// pinned.
	Chosen Candidate
}

// Matches reports whether a candidate is the network this target wants.
//
// Both halves are required, and the second is the one spec 04 is about: an
// access point sharing the SSID but offering no encryption is not the protected
// network the account was configured for, however strong it is.
func (t Target) Matches(c Candidate) bool {
	return c.SSID == t.SSID && t.Security.Accepts(c.Security)
}

// NeedsScan reports whether choosing requires knowing what is on the air.
//
// Auto does not: it pins nothing, so there is nothing to choose and no reason
// to make the radio leave its channel. Asking before scanning rather than
// scanning and discarding the result is the difference between a switch that
// interrupts an associated client and one that does not.
func (t Target) NeedsScan() bool {
	return t.Policy != domain.APSelectionAuto
}

// Select picks the access point to join.
//
// The candidate list is whatever the caller scanned. For a fixed policy an
// empty list means the pinned access point was not seen, which is a failure --
// see below.
func Select(target Target, candidates []Candidate) (Decision, error) {
	if target.SSID == "" {
		return Decision{}, domain.FieldErrorf(domain.CodeInvalidConfig, "ssid",
			"无线账号没有填写 SSID，无法选择接入点")
	}
	if target.Security == SecurityUnknown {
		return Decision{}, domain.FieldErrorf(domain.CodeInvalidConfig,
			"encryption", "无法识别 %s 配置的加密方式，不会盲目尝试连接", target.SSID)
	}

	switch target.Policy {
	case domain.APSelectionAuto:
		// Auto pins nothing and needs no scan: the supplicant picks within the
		// SSID and may move between access points without this program being
		// involved. That is the point of offering it -- roaming handled by the
		// component that can do it without a reassociation this program then
		// has to re-authenticate.
		return Decision{SSID: target.SSID}, nil

	case domain.APSelectionStrongest:
		return selectStrongest(target, candidates)

	case domain.APSelectionFixed:
		return selectFixed(target, candidates)

	default:
		return Decision{}, domain.FieldErrorf(domain.CodeInvalidConfig,
			"ap_selection", "未知的接入点选择策略 %q", string(target.Policy))
	}
}

// selectStrongest takes the best measured candidate.
func selectStrongest(target Target, candidates []Candidate) (Decision, error) {
	matching := matches(target, candidates)
	if len(matching) == 0 {
		return Decision{}, noCandidate(target, candidates)
	}
	slices.SortFunc(matching, byStrength)
	best := matching[0]
	return Decision{SSID: target.SSID, BSSID: best.BSSID, Pinned: true,
		Chosen: best}, nil
}

// selectFixed requires the pinned access point to actually be there.
//
// Spec 04: an invalid or absent fixed BSSID fails and is never quietly turned
// back into auto. Someone who pinned an access point did it to stop the client
// wandering, and a silent downgrade delivers exactly the wandering they pinned
// it to prevent -- while the interface still says `fixed`.
func selectFixed(target Target, candidates []Candidate) (Decision, error) {
	pinned := strings.ToLower(strings.TrimSpace(target.PinnedBSSID))
	if pinned == "" || pinned == UnassociatedBSSID {
		return Decision{}, domain.FieldErrorf(domain.CodeInvalidConfig, "bssid",
			"接入点策略是「固定」，但没有指定有效的 BSSID")
	}

	for _, candidate := range matches(target, candidates) {
		if strings.EqualFold(candidate.BSSID, pinned) {
			return Decision{SSID: target.SSID, BSSID: pinned, Pinned: true,
				Chosen: candidate}, nil
		}
	}

	// Distinguish the two ways this fails, because they send the user to
	// different places: the access point is gone, or it is there but is not
	// the network it was pinned as.
	for _, candidate := range candidates {
		if !strings.EqualFold(candidate.BSSID, pinned) {
			continue
		}
		if candidate.SSID != target.SSID {
			return Decision{}, domain.Errorf(domain.CodeNotFound,
				"固定的接入点 %s 播发的是 %q，不是 %q",
				pinned, candidate.SSID, target.SSID)
		}
		return Decision{}, domain.Errorf(domain.CodeNotFound,
			"固定的接入点 %s 的加密方式是 %s，与该账号配置的 %s 不匹配",
			pinned, candidate.Security, target.Security)
	}
	return Decision{}, domain.Errorf(domain.CodeNotFound,
		"扫描结果里没有固定的接入点 %s", pinned)
}

// matches narrows a scan to the access points this target may join.
func matches(target Target, candidates []Candidate) []Candidate {
	var out []Candidate
	for _, candidate := range candidates {
		if target.Matches(candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

// noCandidate explains an empty result in terms of what was actually seen.
//
// "No access point found" is true and useless when the network is right there
// unencrypted, or there under a protected name the account is not configured
// for. Spec 04's rule produces this case deliberately, so the message has to
// say that is what happened rather than leaving the user to conclude the radio
// is broken.
func noCandidate(target Target, candidates []Candidate) error {
	var sameName []Candidate
	for _, candidate := range candidates {
		if candidate.SSID == target.SSID {
			sameName = append(sameName, candidate)
		}
	}
	if len(sameName) == 0 {
		return domain.Errorf(domain.CodeNotFound,
			"扫描结果里没有名为 %q 的无线网络", target.SSID)
	}

	offered := SecurityUnknown
	for _, candidate := range sameName {
		offered |= candidate.Security
	}
	if target.Security.Protected() && !offered.Protected() {
		return domain.Errorf(domain.CodeNotFound,
			"扫描到名为 %q 的网络，但它是开放网络，而该账号配置的是 %s；"+
				"不会把密码发给同名的开放网络", target.SSID, target.Security)
	}
	return domain.Errorf(domain.CodeNotFound,
		"扫描到名为 %q 的网络，但它提供的是 %s，与该账号配置的 %s 不匹配",
		target.SSID, offered, target.Security)
}

// byStrength orders candidates the way "strongest" means.
//
// It has to be explicit about the unmeasured ones. iwinfo reports an unusable
// reading as 0, and 0 is numerically greater than every real dBm value, so
// sorting on the raw number makes "this one could not be measured" the winner.
// They sort last instead, and are still usable if nothing else is there.
//
// Ties break on BSSID so that one scan produces one answer. Spec's T27 asks for
// that directly: a selection that depended on scan order would move the client
// between two equally strong access points on no new information, and each move
// costs a reassociation, a lease and an authentication.
func byStrength(a, b Candidate) int {
	if a.Measured() != b.Measured() {
		if a.Measured() {
			return -1
		}
		return 1
	}
	if a.Signal != b.Signal {
		// Less negative is stronger.
		return cmp.Compare(b.Signal, a.Signal)
	}
	return cmp.Compare(a.BSSID, b.BSSID)
}

// Association is what a radio reports about itself right now.
type Association struct {
	SSID      string
	BSSID     string
	Encrypted bool
	// HasIPv4 records that the line actually carries an address.
	HasIPv4 bool
}

// Joined reports that the radio is associated with some access point.
func (a Association) Joined() bool {
	return a.SSID != "" && a.BSSID != "" && !strings.EqualFold(a.BSSID, UnassociatedBSSID)
}

// Satisfied reports whether the radio is already on the network the target
// asked for, so the existing link can be reused instead of rebuilt.
//
// Spec 04: "仅有扫描结果不算关联成功；已真实关联匹配SSID与IP可直接复用". A scan
// result proves an access point exists, which is a different claim from being
// joined to it, and an association with no address is a client that joined and
// got nothing -- reusing that means authenticating over a line with no way out.
func (t Target) Satisfied(observed Association) bool {
	if !observed.Joined() || !observed.HasIPv4 {
		return false
	}
	if observed.SSID != t.SSID {
		return false
	}
	if t.Security.Protected() && !observed.Encrypted {
		return false
	}
	if t.Policy == domain.APSelectionFixed {
		return strings.EqualFold(observed.BSSID, strings.TrimSpace(t.PinnedBSSID))
	}
	return true
}

// ShouldReselect reports whether an access point needs choosing now.
//
// Spec 04: "strongest只在发起连接时选择，正常在线不每次tick漫游". A maintenance
// tick that re-ran the strongest search would hand the supplicant a new BSSID
// every time a neighbour's signal moved a decibel, and every change costs a
// reassociation, a new lease and therefore a fresh authentication. Being
// already on the right network is the whole answer; how strong it is, is not a
// question this asks.
func ShouldReselect(target Target, observed Association) bool {
	return !target.Satisfied(observed)
}
