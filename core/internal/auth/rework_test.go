package auth

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// These lock the behaviour the independent review of d33528a found missing.
// They are regression tests, not the reviewer's diagnostics: those were
// deliberately temporary and live in the review snapshot.

// R03 -- HTTP and JSON both succeeding is not evidence about sessions.
//
// `{"error":"backend_busy"}` parses perfectly and names nobody. Reading that as
// "nobody is online" is what let a logout report success it never achieved, and
// sent the stale-session recovery down the clean-up branch on a line whose
// state was never established.
func TestABusinessErrorFromTheOnlineQueryIsNotProofOfOffline(t *testing.T) {
	for _, body := range []string{
		`{"error":"backend_busy","error_msg":"try later"}`,
		`{"error":"auth_server_error"}`,
		`{"res":"failed"}`,
	} {
		gateway := newFakeGateway(t)
		gateway.configure(func(g *fakeGateway) { g.onlineBody = body })
		transaction := transactionFor(t, gateway)

		identity, err := transaction.Online(t.Context(), "2020123456")
		if err == nil {
			t.Errorf("%s: became identity %+v instead of a failed query", body, identity)
			continue
		}
		if identity.Present {
			t.Errorf("%s: a failed query produced a present identity", body)
		}
	}
}

// And the confirmed answers still work, because refusing everything would be a
// different way of being useless.
func TestAConfirmedOfflineAnswerIsStillEvidence(t *testing.T) {
	for _, body := range []string{
		`{"error":"not_online_error"}`,
		`{"error":"ok"}`,
		`{"user_name":""}`,
	} {
		gateway := newFakeGateway(t)
		gateway.configure(func(g *fakeGateway) { g.onlineBody = body })
		transaction := transactionFor(t, gateway)

		identity, err := transaction.Online(t.Context(), "2020123456")
		if err != nil {
			t.Errorf("%s: %v", body, err)
			continue
		}
		if identity.Present {
			t.Errorf("%s: reported somebody online", body)
		}
		if identity.State() != domain.AuthOffline {
			t.Errorf("%s: state = %s, want Offline", body, identity.State())
		}
	}
}

// R04 -- two stated realms that disagree are two accounts.
//
// They share a student number and nothing else. Folding them together lets this
// program claim somebody else's session and, on a manual login, end it.
func TestTwoStatedRealmsThatDisagreeAreDifferentAccounts(t *testing.T) {
	cases := []struct {
		name     string
		reported string
		expected string
		same     bool
	}{
		{"identical", "alice@cmcc", "alice@cmcc", true},
		{"the gateway omits the realm", "alice", "alice@cmcc", true},
		{"the account omits the realm", "alice@cmcc", "alice", true},
		{"both stated and different", "alice@cmcc", "alice@ctcc", false},
		{"different accounts entirely", "bob@cmcc", "alice@cmcc", false},
		{"nobody", "", "alice@cmcc", false},
		{"expecting nobody", "alice@cmcc", "", false},
		// Only the realm is folded, and only because gateways echo it in
		// whatever case they please. The account part is a credential.
		{"the realm differs only in case", "alice@CMCC", "alice@cmcc", true},
		{"the account differs in case", "ALICE@cmcc", "alice@cmcc", false},
	}
	for _, testCase := range cases {
		if got := sameAccount(testCase.reported, testCase.expected); got != testCase.same {
			t.Errorf("%s: sameAccount(%q, %q) = %v, want %v", testCase.name,
				testCase.reported, testCase.expected, got, testCase.same)
		}
	}
}

// R04 -- a realm in its own field participates too.
//
// Spec 04 lists "响应单独 domain" as one of the four cases to test. Dropping the
// field turns "alice on cmcc" into bare "alice", which then matches an
// expectation of "alice@ctcc".
func TestARealmReportedInItsOwnFieldIsPartOfTheIdentity(t *testing.T) {
	gateway := newFakeGateway(t)
	gateway.configure(func(g *fakeGateway) {
		g.onlineBody = `{"error":"ok","user_name":"alice","domain":"ctcc","online_ip":"10.0.0.77"}`
	})
	transaction := transactionFor(t, gateway)

	identity, err := transaction.Online(t.Context(), "alice@cmcc")
	if err != nil {
		t.Fatalf("Online: %v", err)
	}
	if identity.Username != "alice@ctcc" {
		t.Errorf("Username = %q, want the separately reported realm folded in",
			identity.Username)
	}
	if identity.MatchesExpected {
		t.Error("an account on another carrier was accepted as this one")
	}
	if identity.SessionUsername != "alice" {
		t.Fatalf("DM logout must keep the reported session name: %q", identity.SessionUsername)
	}

	// And it is not appended twice when the name already carries one.
	if got := (gatewayAnswer{UserName: "alice@cmcc", Domain: "ctcc"}).identity(); got != "alice@cmcc" {
		t.Errorf("identity = %q, want the name's own realm left alone", got)
	}
}

// R10 -- E2620 is the stuck-session case, not a bad password.
//
// The baseline keys its controlled STA rebuild off exactly this marker
// (orchestrator.py, portal_detect.py). Classified as a plain rejection it looks
// like wrong credentials, so the recovery never runs and the account stays
// locked out until somebody reboots something.
func TestE2620IsRecognisedAsAnExistingSession(t *testing.T) {
	for _, body := range []string{
		`{"error":"login_error","error_msg":"E2620"}`,
		`{"error":"E2620"}`,
		`{"error":"login_error","error_msg":"e2620: ip already online"}`,
		`{"error":"login_error","error_msg":"该账号已在线"}`,
	} {
		gateway := newFakeGateway(t)
		gateway.configure(func(g *fakeGateway) { g.loginBody = body })
		transaction := transactionFor(t, gateway)

		result, err := transaction.Login(t.Context(),
			Credentials{Username: "2020123456", Password: "hunter2"},
			Shape{}, Challenge{Token: "token-abcdef", ClientIP: "10.0.0.77"})
		if err != nil {
			t.Errorf("%s: %v", body, err)
			continue
		}
		if !result.AlreadyOnline {
			t.Errorf("%s: not recognised as an existing session", body)
		}
		// Still not a success. The session may be somebody else's, and that is
		// a separate question from whether one exists.
		if result.State == domain.AuthAccepted {
			t.Errorf("%s: an existing session was reported as an accepted login", body)
		}
	}
}

// And an ordinary refusal is still an ordinary refusal. "Every code beginning
// with E is a session we may end" would hand this program permission to log
// people out over a typo.
func TestAnOrdinaryRefusalIsNotMistakenForAnExistingSession(t *testing.T) {
	for _, body := range []string{
		`{"error":"login_error","error_msg":"E2531: user not found"}`,
		`{"error":"login_error","error_msg":"Password is incorrect"}`,
		`{"error":"login_error","error_msg":"E0001"}`,
	} {
		gateway := newFakeGateway(t)
		gateway.configure(func(g *fakeGateway) { g.loginBody = body })
		transaction := transactionFor(t, gateway)

		result, err := transaction.Login(t.Context(),
			Credentials{Username: "2020123456", Password: "hunter2"},
			Shape{}, Challenge{Token: "token-abcdef", ClientIP: "10.0.0.77"})
		if err != nil {
			t.Errorf("%s: %v", body, err)
			continue
		}
		if result.AlreadyOnline {
			t.Errorf("%s: a refusal was read as an existing session", body)
		}
		if result.State != domain.AuthRejected {
			t.Errorf("%s: state = %s, want Rejected", body, result.State)
		}
	}
}

// R01 -- the endpoint is part of the protocol, and the two requests are not
// interchangeable.
func TestTheLogoutAndLoginEndpointsAreDistinct(t *testing.T) {
	if logoutPath == portalPath {
		t.Fatal("the signed logout shares the portal's path")
	}
	if !strings.HasSuffix(logoutPath, "rad_user_dm") {
		t.Errorf("logoutPath = %q, want the rad_user_dm endpoint spec 04 names",
			logoutPath)
	}
}
