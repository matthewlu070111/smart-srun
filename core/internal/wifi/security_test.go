package wifi

import "testing"

// An encryption nobody classified joins nothing.
//
// The zero value was chosen to be the empty set for this: a Security this
// program could not read must not fall through to "probably a passphrase
// network", because the next step after that guess is sending the passphrase.
func TestAnUnrecognisedEncryptionMatchesNothing(t *testing.T) {
	unknown := ParseSecurity("some-future-mode")
	if unknown != SecurityUnknown {
		t.Fatalf("ParseSecurity(future mode) = %v, want the empty set", unknown)
	}

	for _, offered := range []Security{SecurityOpen, SecurityWEP, SecurityPSK,
		SecuritySAE, SecurityEnterprise, SecurityPSK | SecuritySAE} {
		if unknown.Accepts(offered) {
			t.Errorf("an unknown configuration accepted %v", offered)
		}
		if offered.Accepts(unknown) {
			t.Errorf("%v accepted an unknown access point", offered)
		}
	}
	if unknown.Protected() {
		t.Error("an unknown encryption reported itself as protected")
	}
}

// In UCI, `wpa2` is 802.1X and `psk2` is the passphrase mode.
//
// Getting these the wrong way round is not a cosmetic error: an enterprise
// campus network classified as a passphrase network is then reported as missing
// a passphrase it never wanted, which sends the user to look for a password
// that does not exist.
func TestWPA2IsEnterpriseAndPSK2IsThePassphraseMode(t *testing.T) {
	if got := ParseSecurity("wpa2"); got != SecurityEnterprise {
		t.Errorf("ParseSecurity(wpa2) = %v, want 802.1X", got)
	}
	if got := ParseSecurity("psk2"); got != SecurityPSK {
		t.Errorf("ParseSecurity(psk2) = %v, want the passphrase mode", got)
	}
	// The pair must stay distinguishable, which is the property that actually
	// matters: they are different credentials.
	if ParseSecurity("wpa2").Accepts(ParseSecurity("psk2")) {
		t.Error("an 802.1X configuration accepted a passphrase network")
	}
}

// A mixed access point carries both members, because both are true of it.
//
// Folding `sae-mixed` to one name is where a WPA3-capable network stops being
// joinable by the WPA2 client it explicitly admits.
func TestMixedModesCarryEveryMemberTheyAdmit(t *testing.T) {
	mixed := ParseSecurity("sae-mixed")
	if mixed != SecurityPSK|SecuritySAE {
		t.Fatalf("ParseSecurity(sae-mixed) = %v, want PSK and SAE", mixed)
	}
	if !mixed.Accepts(ParseSecurity("psk2")) {
		t.Error("a sae-mixed account could not join a WPA2-PSK access point")
	}
	if !mixed.Accepts(ParseSecurity("sae")) {
		t.Error("a sae-mixed account could not join a WPA3 access point")
	}
	// And a plain WPA3 account still cannot join a WPA2-only access point:
	// sharing a member is the rule, not "mixed matches everything".
	if ParseSecurity("sae").Accepts(ParseSecurity("psk2")) {
		t.Error("a WPA3-only account accepted a WPA2-PSK access point")
	}
}

// The part after '+' is the cipher, and every mode accepts one.
//
// Without this, `psk2+ccmp` -- which is what a router actually writes -- falls
// through to unknown and the account stops matching its own network.
func TestACipherSuffixDoesNotChangeTheAuthentication(t *testing.T) {
	for _, spelling := range []string{"psk2+ccmp", "psk2+tkip+ccmp", "psk2+aes"} {
		if got := ParseSecurity(spelling); got != SecurityPSK {
			t.Errorf("ParseSecurity(%q) = %v, want the passphrase mode",
				spelling, got)
		}
	}
	if got := ParseSecurity("sae-mixed+ccmp"); got != SecurityPSK|SecuritySAE {
		t.Errorf("ParseSecurity(sae-mixed+ccmp) = %v, want PSK and SAE", got)
	}
}

// An open network needs no credential however it is spelled, and OWE is open
// for the purpose of "what must the account supply".
func TestTheCredentiallessModesAreOpen(t *testing.T) {
	for _, spelling := range []string{"", "none", "owe"} {
		got := ParseSecurity(spelling)
		if got != SecurityOpen {
			t.Errorf("ParseSecurity(%q) = %v, want open", spelling, got)
		}
		if got.Protected() {
			t.Errorf("ParseSecurity(%q) reported itself as protected", spelling)
		}
	}
	if !ParseSecurity("psk2").Protected() {
		t.Error("a passphrase network did not report itself as protected")
	}
}

// encryption.enabled false settles it, whatever the suite lists contain.
//
// Some drivers leave the suite lists populated with defaults on an open
// network. Reading those as a requirement makes every open network look
// protected, which would defeat the one comparison spec 04 relies on.
func TestAnOpenScanResultIsOpenWhateverElseItLists(t *testing.T) {
	got := ClassifyScan(false, []string{"psk", "sae"}, []int{2, 3})
	if got != SecurityOpen {
		t.Fatalf("ClassifyScan(disabled, ...) = %v, want open", got)
	}
	if got.Protected() {
		t.Error("a scan result with encryption disabled reported as protected")
	}
}

// Every advertised suite is kept, so a mixed access point stays matchable by
// either kind of client.
func TestClassifyScanKeepsEveryAdvertisedSuite(t *testing.T) {
	got := ClassifyScan(true, []string{"psk", "sae"}, []int{2, 3})
	if got != SecurityPSK|SecuritySAE {
		t.Fatalf("ClassifyScan(psk+sae) = %v, want both", got)
	}
	if !ParseSecurity("psk2").Accepts(got) {
		t.Error("a WPA2-PSK account could not join an access point offering PSK")
	}
	if got := ClassifyScan(true, []string{"802.1X"}, []int{2}); got != SecurityEnterprise {
		t.Errorf("ClassifyScan(802.1X) = %v, want enterprise", got)
	}
}

// Encrypted, but with no WPA version at all, is WEP.
//
// The alternative is to call it unknown and refuse, which would be safe but
// would also refuse to explain itself. Naming it lets the message say why this
// network is not being joined.
func TestEncryptedWithNoWPAVersionIsWEP(t *testing.T) {
	if got := ClassifyScan(true, nil, nil); got != SecurityWEP {
		t.Errorf("ClassifyScan(encrypted, no suites, no wpa) = %v, want WEP", got)
	}
	// An unreadable suite on a WPA access point stays unknown rather than
	// being demoted to WEP, because that would be a guess about a network
	// that announced a version this program simply does not handle.
	if got := ClassifyScan(true, []string{"future"}, []int{4}); got != SecurityUnknown {
		t.Errorf("ClassifyScan(unknown suite, wpa4) = %v, want unknown", got)
	}
}
