package wifi

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	code, ok := domain.CodeOf(err)
	if !ok {
		t.Fatalf("error carries no code: %v", err)
	}
	return code
}

// campusTarget is a protected campus network on the strongest policy.
func campusTarget() Target {
	return Target{
		SSID:     "campus",
		Security: ParseSecurity("psk2"),
		Policy:   domain.APSelectionStrongest,
	}
}

// The rule spec 04 exists for: a strong open network does not win over the
// protected one whose name it is using.
//
// This is the one that has to hold even when every other signal says otherwise.
// The impostor here is 40 dB stronger, because that is the realistic case -- it
// is the one in the room and the real network is down the corridor -- and it is
// listed first, so neither signal order nor scan order can be what rejects it.
func TestAStrongOpenNetworkDoesNotWinOverTheProtectedOneItIsNamedAfter(t *testing.T) {
	target := campusTarget()
	decision, err := Select(target, []Candidate{
		{SSID: "campus", BSSID: "aa:aa:aa:aa:aa:aa", Signal: -20, Security: SecurityOpen},
		{SSID: "campus", BSSID: "bb:bb:bb:bb:bb:bb", Signal: -60, Security: SecurityPSK},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.BSSID != "bb:bb:bb:bb:bb:bb" {
		t.Fatalf("joined %s, want the protected access point despite its weaker signal",
			decision.BSSID)
	}
}

// And when the impostor is the only thing there, the answer is a refusal that
// says so rather than a join.
func TestAnOpenImpostorAloneIsRefusedWithAnExplanation(t *testing.T) {
	_, err := Select(campusTarget(), []Candidate{
		{SSID: "campus", BSSID: "aa:aa:aa:aa:aa:aa", Signal: -20, Security: SecurityOpen},
	})
	if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Fatalf("code = %s, want NotFound", code)
	}
	// The message has to distinguish this from "no such network": the network
	// is right there, and a user told it is missing will go looking at the
	// radio instead of at the access point pretending to be it.
	if !strings.Contains(err.Error(), "开放网络") {
		t.Errorf("message does not say the network found was open: %v", err)
	}
}

// Auto pins nothing and does not need a scan at all.
//
// That is the reason to offer it: roaming stays with the supplicant, which can
// move between access points without the reassociation and fresh lease that
// this program would have to re-authenticate after.
func TestAutoPinsNothingAndNeedsNoScan(t *testing.T) {
	target := campusTarget()
	target.Policy = domain.APSelectionAuto

	decision, err := Select(target, nil)
	if err != nil {
		t.Fatalf("Select with no scan results: %v", err)
	}
	if decision.SSID != "campus" {
		t.Errorf("SSID = %q, want campus", decision.SSID)
	}
	if decision.BSSID != "" || decision.Pinned {
		t.Errorf("auto pinned %q (pinned=%v); it must leave the choice to the supplicant",
			decision.BSSID, decision.Pinned)
	}
}

// Strongest means the strongest, and dBm is negative.
func TestStrongestPicksTheLeastNegativeSignal(t *testing.T) {
	decision, err := Select(campusTarget(), []Candidate{
		{SSID: "campus", BSSID: "11:11:11:11:11:11", Signal: -80, Security: SecurityPSK},
		{SSID: "campus", BSSID: "22:22:22:22:22:22", Signal: -45, Security: SecurityPSK},
		{SSID: "campus", BSSID: "33:33:33:33:33:33", Signal: -67, Security: SecurityPSK},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.BSSID != "22:22:22:22:22:22" {
		t.Fatalf("chose %s, want the -45 dBm access point", decision.BSSID)
	}
	if !decision.Pinned || decision.Chosen.Signal != -45 {
		t.Errorf("decision does not carry the candidate it chose: %+v", decision)
	}
}

// An unmeasurable reading must not become the best one.
//
// iwinfo reports an unusable measurement as 0, and 0 is numerically greater
// than every real dBm value. Sorting on the raw number makes "this one could
// not be measured" beat a genuinely strong access point -- and it wins every
// time, so the client would settle on it permanently.
func TestAnUnmeasurableSignalDoesNotBecomeTheStrongest(t *testing.T) {
	decision, err := Select(campusTarget(), []Candidate{
		{SSID: "campus", BSSID: "00:00:00:00:00:01", Signal: 0, Security: SecurityPSK},
		{SSID: "campus", BSSID: "22:22:22:22:22:22", Signal: -70, Security: SecurityPSK},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.BSSID != "22:22:22:22:22:22" {
		t.Fatalf("chose %s, want the one with a real measurement", decision.BSSID)
	}

	// It is still usable when it is all there is: unmeasured is not unusable.
	decision, err = Select(campusTarget(), []Candidate{
		{SSID: "campus", BSSID: "00:00:00:00:00:01", Signal: 0, Security: SecurityPSK},
	})
	if err != nil {
		t.Fatalf("Select with only an unmeasured candidate: %v", err)
	}
	if decision.BSSID != "00:00:00:00:00:01" {
		t.Errorf("chose %q, want the only candidate there was", decision.BSSID)
	}
}

// Equally strong candidates resolve the same way every time.
//
// T27 asks for this directly. A selection that depended on scan order would
// move the client between two equally strong access points on no new
// information, and each move costs a reassociation, a lease and an
// authentication. The two orderings below are reverses of each other, so an
// implementation that returned "whichever came first" fails.
func TestEquallyStrongCandidatesResolveTheSameWayEveryTime(t *testing.T) {
	forwards := []Candidate{
		{SSID: "campus", BSSID: "aa:00:00:00:00:01", Signal: -50, Security: SecurityPSK},
		{SSID: "campus", BSSID: "aa:00:00:00:00:02", Signal: -50, Security: SecurityPSK},
		{SSID: "campus", BSSID: "aa:00:00:00:00:03", Signal: -50, Security: SecurityPSK},
	}
	backwards := []Candidate{forwards[2], forwards[1], forwards[0]}

	first, err := Select(campusTarget(), forwards)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	second, err := Select(campusTarget(), backwards)
	if err != nil {
		t.Fatalf("Select reversed: %v", err)
	}
	if first.BSSID != second.BSSID {
		t.Fatalf("scan order changed the answer: %s then %s",
			first.BSSID, second.BSSID)
	}
	if first.BSSID != "aa:00:00:00:00:01" {
		t.Errorf("tie resolved to %s, want the lowest BSSID", first.BSSID)
	}
}

// A pinned access point that is not there fails. It never becomes auto.
//
// Silently downgrading delivers exactly the wandering the user pinned it to
// prevent, while the interface still reads `fixed` -- so the setting lies and
// the behaviour is the one it was set to avoid.
func TestAMissingPinnedAccessPointFailsInsteadOfRoaming(t *testing.T) {
	target := campusTarget()
	target.Policy = domain.APSelectionFixed
	target.PinnedBSSID = "de:ad:be:ef:00:01"

	decision, err := Select(target, []Candidate{
		{SSID: "campus", BSSID: "aa:aa:aa:aa:aa:aa", Signal: -30, Security: SecurityPSK},
	})
	if err == nil {
		t.Fatalf("a missing pinned access point produced a decision: %+v", decision)
	}
	if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Fatalf("code = %s, want NotFound", code)
	}
	// The available access point is strong, protected and on the right SSID --
	// everything auto or strongest would have accepted. Fixed still refuses.
	if decision.Pinned || decision.BSSID != "" {
		t.Errorf("a failed fixed selection still returned something: %+v", decision)
	}
}

// Fixed with nothing pinned is refused rather than treated as auto.
func TestFixedWithNoBSSIDIsRefused(t *testing.T) {
	target := campusTarget()
	target.Policy = domain.APSelectionFixed

	for _, pinned := range []string{"", "   ", UnassociatedBSSID} {
		target.PinnedBSSID = pinned
		_, err := Select(target, []Candidate{
			{SSID: "campus", BSSID: "aa:aa:aa:aa:aa:aa", Signal: -30, Security: SecurityPSK},
		})
		if code := codeOf(t, err); code != domain.CodeInvalidConfig {
			t.Errorf("pinned %q: code = %s, want InvalidConfig", pinned, code)
		}
	}
}

// The two ways a fixed selection fails send the user to different places, so
// they must not collapse into one message.
func TestFixedSaysWhetherTheAPIsGoneOrIsADifferentNetwork(t *testing.T) {
	target := campusTarget()
	target.Policy = domain.APSelectionFixed
	target.PinnedBSSID = "aa:aa:aa:aa:aa:aa"

	// There, but broadcasting another SSID: somebody re-used the hardware.
	_, err := Select(target, []Candidate{
		{SSID: "somebody-else", BSSID: "aa:aa:aa:aa:aa:aa", Signal: -30, Security: SecurityPSK},
	})
	if !strings.Contains(err.Error(), "somebody-else") {
		t.Errorf("message does not name the SSID actually found: %v", err)
	}

	// There, right SSID, but now open: the pinned access point was replaced by
	// one the account must not hand a password to.
	_, err = Select(target, []Candidate{
		{SSID: "campus", BSSID: "aa:aa:aa:aa:aa:aa", Signal: -30, Security: SecurityOpen},
	})
	if !strings.Contains(err.Error(), "加密") {
		t.Errorf("message does not say the encryption did not match: %v", err)
	}

	// Not there at all.
	_, err = Select(target, nil)
	if !strings.Contains(err.Error(), "没有") {
		t.Errorf("message does not say the access point was absent: %v", err)
	}
}

// A configuration this program cannot classify does not get to try.
func TestAnUnknownConfiguredEncryptionRefusesToSelect(t *testing.T) {
	target := campusTarget()
	target.Security = ParseSecurity("some-future-mode")

	_, err := Select(target, []Candidate{
		{SSID: "campus", BSSID: "aa:aa:aa:aa:aa:aa", Signal: -30, Security: SecurityPSK},
	})
	if code := codeOf(t, err); code != domain.CodeInvalidConfig {
		t.Fatalf("code = %s, want InvalidConfig", code)
	}
}

// An account with no SSID cannot select, and says which field is missing.
func TestAnAccountWithNoSSIDCannotSelect(t *testing.T) {
	target := campusTarget()
	target.SSID = ""

	_, err := Select(target, nil)
	if code := codeOf(t, err); code != domain.CodeInvalidConfig {
		t.Fatalf("code = %s, want InvalidConfig", code)
	}
}

// A scan result is not an association.
//
// Spec 04 is explicit. An access point existing and this radio being joined to
// it are different claims, and only the second one means the link can be
// reused.
func TestAScanResultIsNotAnAssociation(t *testing.T) {
	target := campusTarget()

	for name, observed := range map[string]Association{
		"nothing at all":    {},
		"ssid but no bssid": {SSID: "campus", HasIPv4: true},
		"placeholder bssid": {SSID: "campus", BSSID: UnassociatedBSSID, HasIPv4: true},
		"another network":   {SSID: "somebody-else", BSSID: "aa:aa:aa:aa:aa:aa", HasIPv4: true},
	} {
		if target.Satisfied(observed) {
			t.Errorf("%s counted as being on the network", name)
		}
		if !ShouldReselect(target, observed) {
			t.Errorf("%s did not trigger a reselection", name)
		}
	}
}

// An association with no address is a client that joined and got nothing.
//
// Reusing it means authenticating over a line with no way out, and the failure
// that produces points at the gateway rather than at the lease.
func TestAnAssociationWithNoAddressIsNotReusable(t *testing.T) {
	target := campusTarget()
	joined := Association{SSID: "campus", BSSID: "bb:bb:bb:bb:bb:bb", Encrypted: true}

	if target.Satisfied(joined) {
		t.Fatal("an association with no IPv4 was accepted as usable")
	}
	joined.HasIPv4 = true
	if !target.Satisfied(joined) {
		t.Fatal("a real association with an address was rejected")
	}
}

// An online client is not reselected on every tick.
//
// Spec 04: strongest chooses when the connection is made, not continuously. A
// maintenance tick that re-ran the search would hand the supplicant a new BSSID
// whenever a neighbour's signal moved, and each change costs a reassociation, a
// lease and an authentication. The candidate below is much stronger than the
// one in use precisely so that a policy which re-evaluated would move.
func TestAnOnlineClientIsNotReselectedEveryTick(t *testing.T) {
	target := campusTarget()
	online := Association{SSID: "campus", BSSID: "bb:bb:bb:bb:bb:bb", HasIPv4: true, Encrypted: true}

	if ShouldReselect(target, online) {
		t.Fatal("a working association asked to be reselected")
	}
	// Being already on the right network is the whole answer; how strong it is
	// is not a question this asks.
	if !target.Satisfied(online) {
		t.Fatal("a working association was not satisfied")
	}
}

// Fixed is the one policy where which access point you are on still matters
// while online: being on the wrong one is the thing pinning forbids.
func TestFixedRequiresTheAssociationToBeThePinnedAP(t *testing.T) {
	target := campusTarget()
	target.Policy = domain.APSelectionFixed
	target.PinnedBSSID = "bb:bb:bb:bb:bb:bb"

	wrong := Association{SSID: "campus", BSSID: "cc:cc:cc:cc:cc:cc", HasIPv4: true, Encrypted: true}
	if target.Satisfied(wrong) {
		t.Error("a pinned account accepted an association with another access point")
	}
	if !ShouldReselect(target, wrong) {
		t.Error("a pinned account on the wrong access point did not reselect")
	}

	right := Association{SSID: "campus", BSSID: "BB:BB:BB:BB:BB:BB", HasIPv4: true, Encrypted: true}
	if !target.Satisfied(right) {
		t.Error("a pinned account rejected its own access point over letter case")
	}
}

func TestProtectedTargetNeverReusesAnOpenAssociation(t *testing.T) {
	target := campusTarget()
	observed := Association{SSID: target.SSID, BSSID: "02:00:00:00:00:01", HasIPv4: true}
	if target.Satisfied(observed) {
		t.Fatal("open same-name AP reused for protected target")
	}
	observed.Encrypted = true
	if !target.Satisfied(observed) {
		t.Fatal("protected association not reusable")
	}
}
