package application

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

// fakeWireless stands in for the transaction M10 will supply.
type fakeWireless struct {
	mu sync.Mutex

	association    wifi.Association
	associationErr error
	candidates     []wifi.Candidate
	scanErr        error
	// failApplyFor refuses to move to these SSIDs, which is how a failed
	// switch and a failed way back are produced independently.
	failApplyFor map[string]error

	scans     int
	applied   []WirelessPlan
	retired   int
	retireErr error
	onRetire  func()
}

func (w *fakeWireless) Retire(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.retired++
	if w.onRetire != nil {
		w.onRetire()
	}
	return w.retireErr
}

func (w *fakeWireless) Association(context.Context, string) (wifi.Association, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.association, w.associationErr
}

func (w *fakeWireless) Scan(context.Context, string) ([]wifi.Candidate, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.scans++
	if w.scanErr != nil {
		return nil, w.scanErr
	}
	return w.candidates, nil
}

func (w *fakeWireless) Apply(_ context.Context, plan WirelessPlan) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err, refused := w.failApplyFor[plan.SSID]; refused {
		return err
	}
	w.applied = append(w.applied, plan)
	w.association = wifi.Association{SSID: plan.SSID, BSSID: "02:00:5e:00:53:02", HasIPv4: true, Encrypted: plan.Encryption != "none"}
	return nil
}

func (w *fakeWireless) moves() []WirelessPlan {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]WirelessPlan(nil), w.applied...)
}

func (w *fakeWireless) scanCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.scans
}

// switchWorld is a router with one wireless campus account and one hotspot.
func switchWorld(failback bool) *fakeSettings {
	return &fakeSettings{
		revision: 3,
		cfg: domain.Config{
			Checks:    domain.ChecksConfig{Mode: domain.CheckPortal},
			STAIface:  "wwan",
			Selection: domain.Selection{ActiveCampusID: "c1"},
			Failover:  domain.FailoverConfig{HotspotFailbackEnabled: failback},
			CampusAccounts: []domain.CampusAccount{{
				ID:          "c1",
				Label:       "校园网",
				UserID:      "2020123456",
				Password:    "hunter2",
				AccessMode:  domain.AccessModeWiFi,
				SSID:        "jxnu_stu",
				Radio:       "radio0",
				Encryption:  "none",
				APSelection: domain.APSelectionAuto,
				BaseURL:     "http://10.0.0.1",
				ACID:        "12",
			}},
			HotspotProfiles: []domain.HotspotProfile{{
				ID:         "h1",
				Label:      "手机热点",
				SSID:       "phone",
				Radio:      "radio0",
				Encryption: "psk2",
				Key:        "letmein1",
			}},
		},
	}
}

func switcherFor(t *testing.T, settings *fakeSettings,
	radio *fakeWireless) *Authenticator {

	t.Helper()
	return NewAuthenticator(AuthenticatorOptions{
		Binder:   &fakeBinder{},
		Lines:    &fakeLines{client: &http.Client{Transport: hotspotProbeTransport{}}, source: steadyBinding().SourceIPv4},
		Settings: settings,
		Wireless: radio,
	})
}

type hotspotProbeTransport struct{}

func (hotspotProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: req}, nil
}

func switchAction() Action {
	return Action{
		ID: "a1",
		Request: Request{Kind: KindSwitchHotspot, HotspotID: "h1",
			IdempotencyKey: "k1"},
	}
}

// The ordinary case: the radio moves to the hotspot.
func TestSwitchingToAHotspotAppliesThePlan(t *testing.T) {
	radio := &fakeWireless{}
	worker := switcherFor(t, switchWorld(true), radio)

	outcome := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if outcome.State != StateSucceeded {
		t.Fatalf("outcome = %+v", outcome)
	}
	moves := radio.moves()
	if len(moves) != 1 {
		t.Fatalf("applied %d plans, want 1", len(moves))
	}
	if moves[0].SSID != "phone" || moves[0].Key != "letmein1" ||
		moves[0].Encryption != "psk2" {
		t.Errorf("plan = %+v", moves[0])
	}
	// A hotspot has no AP policy and no pinned BSSID. The form has never
	// offered them and this must not invent one.
	if moves[0].BSSID != "" {
		t.Errorf("a hotspot switch pinned %q", moves[0].BSSID)
	}
	if radio.scanCount() != 0 {
		t.Errorf("scanned %d times for a policy that pins nothing", radio.scanCount())
	}
}

// A radio already on the target network is left alone.
//
// Spec 04 allows reuse when the SSID matches and there is an address.
// Re-applying would cost a reassociation, a new lease and therefore a fresh
// authentication, to arrive exactly where the radio already is.
func TestARadioAlreadyOnTheTargetNetworkIsNotReapplied(t *testing.T) {
	radio := &fakeWireless{
		association: wifi.Association{SSID: "phone",
			BSSID: "aa:bb:cc:dd:ee:ff", HasIPv4: true, Encrypted: true},
	}
	worker := switcherFor(t, switchWorld(true), radio)

	outcome := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if outcome.State != StateSucceeded {
		t.Fatalf("outcome = %+v", outcome)
	}
	if moves := radio.moves(); len(moves) != 0 {
		t.Errorf("a radio already on the network was reconfigured: %+v", moves)
	}
	if radio.scanCount() != 0 {
		t.Errorf("a radio already on the network was made to scan")
	}
}

// An association without an address is not the network being there.
//
// The client joined and got nothing, which is precisely the state a switch has
// to repair rather than accept.
func TestAnAssociationWithoutAnAddressIsStillSwitched(t *testing.T) {
	radio := &fakeWireless{
		association: wifi.Association{SSID: "phone", BSSID: "aa:bb:cc:dd:ee:ff"},
	}
	worker := switcherFor(t, switchWorld(true), radio)

	worker.Run(t.Context(), switchAction(), func(Phase) {})
	if moves := radio.moves(); len(moves) != 1 {
		t.Fatalf("applied %d plans; an association with no address needs repair",
			len(moves))
	}
}

// T22: a failed switch goes back to the campus network, and says so.
func TestAFailedSwitchGoesBackToTheCampusNetwork(t *testing.T) {
	radio := &fakeWireless{
		failApplyFor: map[string]error{
			"phone": domain.Errorf(domain.CodeTransportFailure, "热点没有响应"),
		},
	}
	worker := switcherFor(t, switchWorld(true), radio)

	outcome := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if outcome.State != StateFailed {
		t.Fatalf("outcome = %+v, want a failure", outcome)
	}
	moves := radio.moves()
	if len(moves) != 1 || moves[0].SSID != "jxnu_stu" {
		t.Fatalf("moves = %+v, want the campus network restored", moves)
	}
	if !strings.Contains(outcome.Message, "回切") {
		t.Errorf("the message does not say it went back: %q", outcome.Message)
	}
}

// T22, the rule that matters: a failed way back is not reported as restored.
//
// A user told "the switch failed, you are back on the campus network" stops
// looking. If that is untrue the radio is on neither network and nobody is
// coming. The outcome has to say the connection needs a person.
func TestAFailedFailbackIsNotReportedAsRestored(t *testing.T) {
	radio := &fakeWireless{
		failApplyFor: map[string]error{
			"phone":    domain.Errorf(domain.CodeTransportFailure, "热点没有响应"),
			"jxnu_stu": domain.Errorf(domain.CodeTransportFailure, "校园网也没有响应"),
		},
	}
	worker := switcherFor(t, switchWorld(true), radio)

	outcome := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if outcome.State != StateFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Code != domain.CodeRecoveryRequired {
		t.Fatalf("Code = %s, want RecoveryRequired", outcome.Code)
	}
	if strings.Contains(outcome.Message, "已回切") {
		t.Fatalf("a failed failback claimed the old network was restored: %q",
			outcome.Message)
	}
	if !strings.Contains(outcome.Message, "人工") {
		t.Errorf("the message does not say a person is needed: %q", outcome.Message)
	}
	if moves := radio.moves(); len(moves) != 0 {
		t.Errorf("something was applied after both attempts failed: %+v", moves)
	}
}

// Failback is a setting, and switching it off means the radio stays where the
// failed switch left it rather than being moved again.
func TestFailbackIsSkippedWhenItIsSwitchedOff(t *testing.T) {
	radio := &fakeWireless{
		failApplyFor: map[string]error{
			"phone": domain.Errorf(domain.CodeTransportFailure, "热点没有响应"),
		},
	}
	worker := switcherFor(t, switchWorld(false), radio)

	outcome := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if outcome.State != StateFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if moves := radio.moves(); len(moves) != 0 {
		t.Fatalf("failback is off and the radio was still moved: %+v", moves)
	}
	if strings.Contains(outcome.Message, "回切") {
		t.Errorf("the message mentions a failback that did not happen: %q",
			outcome.Message)
	}
}

// A build without the wireless transaction refuses rather than pretending.
//
// Spec 07 will not let a real radio be modified before M10 passes, and
// reporting a switch that did not happen is worse than the command not
// existing: the user reads "switched" and the uplink is unchanged.
func TestSwitchingWithoutAWirelessTransactionIsRefused(t *testing.T) {
	worker := NewAuthenticator(AuthenticatorOptions{
		Binder:   &fakeBinder{},
		Lines:    &fakeLines{},
		Settings: switchWorld(true),
	})

	outcome := worker.Run(t.Context(), switchAction(), func(Phase) {})
	if outcome.State != StateFailed {
		t.Fatalf("outcome = %+v, want a refusal", outcome)
	}
	if outcome.Code != domain.CodeUnsupportedCapability {
		t.Errorf("Code = %s, want UnsupportedCapability", outcome.Code)
	}
}

// A hotspot that is not in the configuration is NotFound.
func TestAnUnknownHotspotIsNotFound(t *testing.T) {
	radio := &fakeWireless{}
	worker := switcherFor(t, switchWorld(true), radio)

	action := switchAction()
	action.Request.HotspotID = "gone"
	outcome := worker.Run(t.Context(), action, func(Phase) {})
	if outcome.Code != domain.CodeNotFound {
		t.Fatalf("outcome = %+v, want NotFound", outcome)
	}
	if moves := radio.moves(); len(moves) != 0 {
		t.Errorf("an unknown hotspot still moved the radio: %+v", moves)
	}
}

// A pinned campus account scans, and the pin reaches the plan.
func TestAPinnedCampusAccountScansAndPinsWhatItFound(t *testing.T) {
	settings := switchWorld(true)
	account := &settings.cfg.CampusAccounts[0]
	account.APSelection = domain.APSelectionFixed
	account.BSSID = "02:00:5e:00:53:02"

	radio := &fakeWireless{
		candidates: []wifi.Candidate{
			{SSID: "jxnu_stu", BSSID: "02:00:5e:00:53:02", Signal: -60,
				Security: wifi.SecurityOpen},
		},
	}
	worker := switcherFor(t, settings, radio)

	outcome := worker.Run(t.Context(), Action{
		ID:      "a2",
		Request: Request{Kind: KindLogin, AccountID: "c1", IdempotencyKey: "k2"},
	}, func(Phase) {})

	if radio.scanCount() != 1 {
		t.Errorf("a pinned policy scanned %d times, want 1", radio.scanCount())
	}
	moves := radio.moves()
	if len(moves) != 1 || moves[0].BSSID != "02:00:5e:00:53:02" {
		t.Fatalf("moves = %+v, want the pinned access point", moves)
	}
	// The authentication behind it fails -- there is no portal in this test --
	// but the radio work happened first and is what is under test here.
	if outcome.State != StateFailed {
		t.Logf("authentication outcome (not under test here): %+v", outcome)
	}
}

// A wireless account whose network is missing never reaches the gateway.
//
// The pinned access point is gone, so there is nothing to authenticate over,
// and the message has to be about the radio rather than about the portal.
func TestAWirelessAccountWithNoNetworkFailsBeforeAuthenticating(t *testing.T) {
	settings := switchWorld(true)
	account := &settings.cfg.CampusAccounts[0]
	account.APSelection = domain.APSelectionFixed
	account.BSSID = "02:00:5e:00:53:02"

	radio := &fakeWireless{} // nothing on the air
	worker := switcherFor(t, settings, radio)

	outcome := worker.Run(t.Context(), Action{
		ID:      "a3",
		Request: Request{Kind: KindMaintain, AccountID: "c1", IdempotencyKey: "k3"},
	}, func(Phase) {})

	if outcome.State != StateFailed || outcome.Code != domain.CodeNotFound {
		t.Fatalf("outcome = %+v, want NotFound", outcome)
	}
	if !strings.Contains(outcome.Message, "校园网") {
		t.Errorf("the message does not say which network: %q", outcome.Message)
	}
}

// T27 at this level: a maintenance tick on an associated client does not scan.
//
// Scanning takes the radio off its channel. Doing it every tick is a periodic
// interruption of a working connection, and if the strongest policy then acted
// on what it found it would be a periodic reassociation as well.
func TestAMaintenanceTickDoesNotScanAnAssociatedClient(t *testing.T) {
	settings := switchWorld(true)
	settings.cfg.CampusAccounts[0].APSelection = domain.APSelectionStrongest

	radio := &fakeWireless{
		association: wifi.Association{SSID: "jxnu_stu",
			BSSID: "02:00:5e:00:53:02", HasIPv4: true},
		candidates: []wifi.Candidate{
			// Much stronger, and deliberately so: a policy that re-evaluated
			// would move to it.
			{SSID: "jxnu_stu", BSSID: "02:00:5e:00:53:09", Signal: -20,
				Security: wifi.SecurityOpen},
		},
	}
	worker := switcherFor(t, settings, radio)

	worker.Run(t.Context(), Action{
		ID:      "a4",
		Request: Request{Kind: KindMaintain, AccountID: "c1", IdempotencyKey: "k4"},
	}, func(Phase) {})

	if radio.scanCount() != 0 {
		t.Errorf("an associated client was scanned %d times on a maintenance tick",
			radio.scanCount())
	}
	if moves := radio.moves(); len(moves) != 0 {
		t.Errorf("an associated client was moved on a maintenance tick: %+v", moves)
	}
}

// A wired account skips the radio path and goes on to authenticate.
//
// Both halves matter, and the second is the one that is easy to leave out. A
// guard that sent a wired account into the wireless path would fail it with
// "this account is wired, there is no wireless network to switch to" -- the
// radio would still be untouched, so a test that only counted scans and moves
// would pass while every wired router had stopped logging in.
func TestAWiredAccountSkipsTheRadioAndStillAuthenticates(t *testing.T) {
	p := newPortal(t)
	p.onlineAfter = func(count int) {
		if count == 1 {
			p.onlineBody = offlineAnswer
		} else {
			p.onlineBody = `{"error":"ok","user_name":"2020123456","online_ip":"10.0.0.77"}`
		}
	}
	settings := switchWorld(true)
	account := &settings.cfg.CampusAccounts[0]
	account.AccessMode = domain.AccessModeWired
	account.WiredIface = "wan"
	account.BaseURL = p.server.URL

	radio := &fakeWireless{}
	worker := NewAuthenticator(AuthenticatorOptions{
		Binder: &fakeBinder{},
		Lines: &fakeLines{client: p.server.Client(),
			source: netip.MustParseAddr("10.0.0.77")},
		Settings: settings,
		Wireless: radio,
	})

	outcome := worker.Run(t.Context(), Action{
		ID:      "a5",
		Request: Request{Kind: KindMaintain, AccountID: "c1", IdempotencyKey: "k5"},
	}, func(Phase) {})

	if radio.scanCount() != 0 || len(radio.moves()) != 0 {
		t.Errorf("a wired account reached the radio: scans=%d moves=%+v",
			radio.scanCount(), radio.moves())
	}
	if outcome.State != StateSucceeded {
		t.Fatalf("a wired account did not authenticate: %+v", outcome)
	}
	if p.count(challengePath) == 0 {
		t.Error("a wired account never reached the gateway")
	}
}
