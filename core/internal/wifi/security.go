// Package wifi decides which access point a wireless account joins, and
// whether it is actually joined to it.
//
// Pure, like policy: every question here is answered from values, so the tests
// run on a machine with no wireless hardware -- which is every machine this
// project is developed on, and the guest it is tested on. Reading a radio is
// the adapter's job and sits deliberately on the other side of this boundary.
//
// The rules come from spec 04, and most of them are refusals: a scan result is
// not an association, a pinned access point that is not there is a failure
// rather than a reason to roam, and a strong open network does not win over the
// protected one whose name it is using.
package wifi

import "strings"

// Security is what an access point offers, as a set rather than a level.
//
// A set, because the mixed modes are real: `sae-mixed` accepts both WPA3 and
// WPA2-PSK clients, and asking "is this WPA3 or WPA2" has no single answer. Two
// sets are compatible when they share a member, which is the same question the
// supplicant asks.
//
// The zero value is the empty set and therefore matches nothing. That is the
// useful default: an encryption this program failed to recognise refuses to
// join rather than guessing, and a guess here means either a failed association
// or, worse, a credential sent to the wrong kind of network.
type Security uint8

const (
	// SecurityOpen is a network that needs no credential.
	SecurityOpen Security = 1 << iota
	// SecurityWEP is present so it can be named in a diagnosis. It is not
	// treated as equivalent to the WPA modes anywhere.
	SecurityWEP
	// SecurityPSK is WPA or WPA2 with a shared passphrase.
	SecurityPSK
	// SecuritySAE is WPA3.
	SecuritySAE
	// SecurityEnterprise is 802.1X, where the credential is per user rather
	// than a passphrase shared by everyone on the network.
	SecurityEnterprise
)

// SecurityUnknown is the empty set: an encryption nobody could classify.
const SecurityUnknown Security = 0

// Protected reports whether joining requires a credential.
//
// This is the distinction spec 04 turns on. An open network with the campus
// SSID is the ordinary way credentials get collected, and it is always the
// stronger signal, because it is the one in the room.
func (s Security) Protected() bool {
	return s&(SecurityWEP|SecurityPSK|SecuritySAE|SecurityEnterprise) != 0
}

// Accepts reports whether an account configured for s can join a network
// offering offered.
//
// Sharing one member is enough: a `sae-mixed` account joins a WPA2-PSK access
// point on the PSK member they share. An unknown set on either side shares
// nothing and is refused, which is the behaviour the zero value was chosen for.
func (s Security) Accepts(offered Security) bool {
	return s&offered != 0
}

// String names the set for a message a person reads.
func (s Security) String() string {
	if s == SecurityUnknown {
		return "未知加密"
	}
	var parts []string
	for _, named := range []struct {
		bit   Security
		label string
	}{
		{SecurityOpen, "开放"},
		{SecurityWEP, "WEP"},
		{SecurityPSK, "WPA/WPA2 密码"},
		{SecuritySAE, "WPA3"},
		{SecurityEnterprise, "802.1X 企业"},
	} {
		if s&named.bit != 0 {
			parts = append(parts, named.label)
		}
	}
	return strings.Join(parts, "/")
}

// ParseSecurity classifies a stored UCI encryption value.
//
// The input is the canonical form config already produced, so the several
// spellings of "open" were folded before this saw them and are not folded
// again here.
//
// The trap in this vocabulary is that `wpa2` is not the passphrase mode. In
// UCI, psk/psk2 are the shared-passphrase modes and wpa/wpa2/wpa3 are the
// 802.1X ones. Reading `wpa2` as WPA2-PSK would classify an enterprise campus
// network as a passphrase network, and then report a missing passphrase for a
// network that never wanted one.
func ParseSecurity(encryption string) Security {
	value := strings.ToLower(strings.TrimSpace(encryption))
	// A suffix after '+' names the cipher -- psk2+ccmp, psk2+tkip+ccmp -- and
	// every authentication mode accepts one. It says nothing about which
	// credential is needed.
	if cut := strings.IndexByte(value, '+'); cut >= 0 {
		value = value[:cut]
	}

	switch value {
	case "", "none":
		return SecurityOpen
	case "owe":
		// Opportunistic Wireless Encryption: encrypted, but with no credential
		// to present. For the question this type answers -- what must the
		// account supply -- it behaves as open.
		return SecurityOpen
	case "wep", "wep-open", "wep-shared":
		return SecurityWEP
	case "psk", "psk2", "psk-mixed":
		return SecurityPSK
	case "sae":
		return SecuritySAE
	case "sae-mixed":
		// WPA3 that still admits WPA2-PSK clients. Both members, because both
		// are true.
		return SecurityPSK | SecuritySAE
	case "wpa", "wpa2", "wpa-mixed":
		return SecurityEnterprise
	case "wpa3", "wpa3-mixed":
		return SecurityEnterprise
	default:
		return SecurityUnknown
	}
}

// ClassifyScan turns what a scan reported into the same set.
//
// The adapter passes what iwinfo gave it rather than a string it assembled,
// because iwinfo reports the authentication suites as a list and flattening
// that to one name is where a mixed-mode access point stops being matchable.
//
// enabled false is an open network whatever else is present: iwinfo leaves the
// suite lists populated with defaults on some drivers, and reading those as a
// requirement would make every open network look protected.
func ClassifyScan(enabled bool, authSuites []string, wpaVersions []int) Security {
	if !enabled {
		return SecurityOpen
	}

	var found Security
	for _, suite := range authSuites {
		switch strings.ToLower(strings.TrimSpace(suite)) {
		case "psk":
			found |= SecurityPSK
		case "sae":
			found |= SecuritySAE
		case "802.1x", "8021x", "eap":
			found |= SecurityEnterprise
		case "none", "open":
			found |= SecurityOpen
		case "owe":
			found |= SecurityOpen
		}
	}
	if found != SecurityUnknown {
		return found
	}

	// No suite this program knows. WEP announces itself by being encrypted
	// with no WPA version at all; anything else stays unknown rather than
	// being guessed into a mode.
	if len(wpaVersions) == 0 {
		return SecurityWEP
	}
	return SecurityUnknown
}
