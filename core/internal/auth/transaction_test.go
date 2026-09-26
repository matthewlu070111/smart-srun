package auth

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/protocol/srun"
)

// T05 -- a successful login is Accepted, and only Accepted.
//
// The gateway saying "ok" is the gateway's claim about itself. Calling that
// VerifiedSelf would report a user as logged in without ever having asked whose
// session is on the line, which is the mistake spec 04 spends most of its words
// preventing.
func TestASuccessfulLoginIsAcceptedNotVerified(t *testing.T) {
	gateway := newFakeGateway(t)
	transaction := transactionFor(t, gateway)

	challenge, err := transaction.Challenge(t.Context(), "2020123456")
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	if challenge.Token != "token-abcdef" {
		t.Fatalf("token = %q", challenge.Token)
	}

	result, err := transaction.Login(t.Context(),
		Credentials{Username: "2020123456", Password: "pw12345678"},
		Shape{}, challenge)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.State != domain.AuthAccepted {
		t.Errorf("state = %s, want Accepted", result.State)
	}
	if result.State == domain.AuthVerifiedSelf {
		t.Error("the gateway's own answer was treated as a verified identity")
	}
}

// T05 -- the login carries every parameter spec 04 lists, with the wire
// password as the marked digest and the checksum over the bare one.
func TestTheLoginCarriesTheProtocolsParameters(t *testing.T) {
	gateway := newFakeGateway(t)
	transaction := transactionFor(t, gateway)

	challenge, _ := transaction.Challenge(t.Context(), "2020123456")
	if _, err := transaction.Login(t.Context(),
		Credentials{Username: "2020123456", Password: "pw12345678"},
		Shape{}, challenge); err != nil {
		t.Fatalf("Login: %v", err)
	}

	query := gateway.lastQuery(t, portalPath)
	for _, name := range []string{"action", "username", "password", "ac_id",
		"ip", "chksum", "info", "n", "type", "os", "name", "double_stack",
		"callback", "_"} {
		if query.Get(name) == "" {
			t.Errorf("the login is missing %s", name)
		}
	}
	if got := query.Get("action"); got != "login" {
		t.Errorf("action = %q", got)
	}

	digest := srun.HMACMD5Hex("token-abcdef", "pw12345678")
	if want := srun.WirePasswordPrefix + digest; query.Get("password") != want {
		t.Errorf("password = %q, want the {MD5}-marked digest", query.Get("password"))
	}
	// The checksum is over the bare digest. Feeding it the marked wire field is
	// an easy mistake and produces a signature the gateway rejects.
	want := srun.Checksum("token-abcdef", "2020123456", digest, "12",
		"10.0.0.77", "200", "1", query.Get("info"))
	if query.Get("chksum") != want {
		t.Error("the checksum was not computed over the bare digest")
	}
	if strings.Contains(query.Get("info"), "{{") {
		t.Errorf("the info prefix was applied twice: %q", query.Get("info"))
	}
}

// T05 -- a refused login is Rejected, and the gateway's own words are kept for
// the log without becoming this program's explanation.
func TestARefusedLoginIsRejected(t *testing.T) {
	gateway := newFakeGateway(t)
	gateway.configure(func(g *fakeGateway) {
		g.loginBody = `{"error":"login_error","error_msg":"E2531: invalid password"}`
	})
	transaction := transactionFor(t, gateway)

	challenge, _ := transaction.Challenge(t.Context(), "2020123456")
	result, err := transaction.Login(t.Context(),
		Credentials{Username: "2020123456", Password: "wrong"}, Shape{}, challenge)
	if err != nil {
		t.Fatalf("a refusal is an answer, not a transport failure: %v", err)
	}
	if result.State != domain.AuthRejected {
		t.Errorf("state = %s, want Rejected", result.State)
	}
	if result.GatewayCode != "login_error" {
		t.Errorf("the gateway's code was lost: %q", result.GatewayCode)
	}
	if result.AlreadyOnline {
		t.Error("a refusal was read as already-online")
	}
}

// T05 -- already-online is neither success nor failure. It is a question about
// whose session that is, and answering it optimistically is how a user gets
// told they are logged in on somebody else's.
func TestAlreadyOnlineIsNotTreatedAsSuccess(t *testing.T) {
	for _, body := range []string{
		`{"error":"ip_already_online_error"}`,
		`{"error":"login_error","error_msg":"ip already online"}`,
		`{"error":"already_online"}`,
	} {
		gateway := newFakeGateway(t)
		gateway.configure(func(g *fakeGateway) { g.loginBody = body })
		transaction := transactionFor(t, gateway)

		challenge, _ := transaction.Challenge(t.Context(), "2020123456")
		result, err := transaction.Login(t.Context(),
			Credentials{Username: "2020123456", Password: "pw"}, Shape{}, challenge)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if !result.AlreadyOnline {
			t.Errorf("%s was not recognised as already-online", body)
		}
		if result.State == domain.AuthAccepted {
			t.Errorf("%s was treated as a successful login", body)
		}
	}
}

// T05 -- the online query is what turns an acceptance into an identity, and it
// distinguishes this account from anybody else's.
func TestTheOnlineQueryDistinguishesSelfFromOther(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		expected string
		want     domain.AuthState
	}{
		{"this account", `{"user_name":"2020123456","online_ip":"10.0.0.77"}`,
			"2020123456", domain.AuthVerifiedSelf},
		{"the same account with its suffix",
			`{"user_name":"2020123456@cmcc","online_ip":"10.0.0.77"}`,
			"2020123456", domain.AuthVerifiedSelf},
		{"the same account, suffix expected",
			`{"user_name":"2020123456","online_ip":"10.0.0.77"}`,
			"2020123456@cmcc", domain.AuthVerifiedSelf},
		{"somebody else", `{"user_name":"2020999999","online_ip":"10.0.0.77"}`,
			"2020123456", domain.AuthVerifiedOther},
		// Offline rather than Unknown, and the difference is the point. The
		// gateway said nobody is here, which is evidence; Unknown is for having
		// asked nothing. A logout may only report success against the first.
		{"nobody", `{"error":"not_online_error"}`,
			"2020123456", domain.AuthOffline},
		{"an empty identity", `{"user_name":"","online_ip":""}`,
			"2020123456", domain.AuthOffline},
		// Two realms both stated and not the same account. Bare names agreeing
		// is not enough: these are two carriers' subscribers who happen to share
		// a student number.
		{"a different realm", `{"user_name":"2020123456@ctcc","online_ip":"10.0.0.77"}`,
			"2020123456@cmcc", domain.AuthVerifiedOther},
		// The realm reported as its own field takes part the same way.
		{"a realm in its own field",
			`{"user_name":"2020123456","domain":"ctcc","online_ip":"10.0.0.77"}`,
			"2020123456@cmcc", domain.AuthVerifiedOther},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := newFakeGateway(t)
			gateway.configure(func(g *fakeGateway) { g.onlineBody = testCase.body })
			transaction := transactionFor(t, gateway)

			identity, err := transaction.Online(t.Context(), testCase.expected)
			if err != nil {
				t.Fatalf("Online: %v", err)
			}
			if identity.State() != testCase.want {
				t.Errorf("state = %s, want %s (reported %q, expected %q)",
					identity.State(), testCase.want, identity.Username,
					testCase.expected)
			}
		})
	}
}

// Two genuinely different accounts must never compare equal, or this program
// would claim somebody else's session as its own.
func TestTwoDifferentAccountsNeverMatch(t *testing.T) {
	cases := [][2]string{
		{"2020123456", "2020123457"},
		{"2020123456@cmcc", "2020999999@cmcc"},
		{"2020123456", ""},
		{"", "2020123456"},
		{"", ""},
		{"alice", "alicia"},
	}
	for _, pair := range cases {
		if sameAccount(pair[0], pair[1]) {
			t.Errorf("%q and %q were treated as the same account", pair[0], pair[1])
		}
	}
}

// T06 -- the signed logout goes to rad_user_dm, with seconds, and the same
// value goes into the signature.
//
// The endpoint is half the contract and this test used to assert the other
// half only. Spec 04 names rad_user_dm and the baseline posts there
// (srun_auth.logout takes rad_user_dm_api); sending the signed form to
// srun_portal means a gateway that implements this endpoint never receives the
// unbind, so the session stays up while this program reports it gone. Signing
// with milliseconds produces a signature the gateway refuses without saying
// why, which is the other half.
func TestTheSignedLogoutGoesToRadUserDMWithSeconds(t *testing.T) {
	gateway := newFakeGateway(t)
	transaction := transactionFor(t, gateway)

	if _, err := transaction.Logout(t.Context(), "2020123456", "10.0.0.77"); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	for _, seen := range gateway.seen() {
		if seen.path == portalPath {
			t.Fatalf("the signed logout was sent to %s", portalPath)
		}
	}
	query := gateway.lastQuery(t, logoutPath)
	if got := query.Get("time"); got != "1700000000" {
		t.Errorf("time = %q, want the fixed clock's seconds", got)
	}
	if got := query.Get("unbind"); got != "1" {
		t.Errorf("unbind = %q, want 1", got)
	}
	if got := query.Get("username"); got != "2020123456" {
		t.Errorf("username = %q", got)
	}
	want := srun.LogoutSign(1700000000, "2020123456", "10.0.0.77")
	if query.Get("sign") != want {
		t.Errorf("sign = %q, want %q", query.Get("sign"), want)
	}
	// The baseline's build_logout_params carries callback, time, unbind, ip,
	// username and sign -- and nothing else. action and ac_id belong to the
	// portal form of the request; carrying them here would be a third shape
	// that neither the baseline nor the spec describes.
	for _, unwanted := range []string{"action", "ac_id"} {
		if query.Has(unwanted) {
			t.Errorf("the signed logout carries %q, which belongs to the portal form",
				unwanted)
		}
	}
}

// T06 -- logging out needs to know who. An empty account is a programming
// mistake, not a request to log out whoever happens to be there.
func TestLoggingOutWithNoAccountIsRefused(t *testing.T) {
	gateway := newFakeGateway(t)
	transaction := transactionFor(t, gateway)

	if _, err := transaction.Logout(t.Context(), "", "10.0.0.77"); err == nil {
		t.Fatal("a logout with no account was sent")
	}
	if len(gateway.seen()) != 0 {
		t.Errorf("a request was made anyway: %+v", gateway.seen())
	}
}

// A portal intercepting the request answers with a page, and that has its own
// code so the interface can say what is happening rather than "malformed".
func TestAnHTMLAnswerIsReportedAsAPortalInterception(t *testing.T) {
	gateway := newFakeGateway(t)
	gateway.configure(func(g *fakeGateway) {
		g.rawBody = "<!DOCTYPE html><html><body>Please sign in</body></html>"
	})
	transaction := transactionFor(t, gateway)

	_, err := transaction.Challenge(t.Context(), "2020123456")
	if err == nil {
		t.Fatal("a web page was accepted as a challenge")
	}
	if code := codeOf(t, err); code != domain.CodePortalHTMLResponse {
		t.Errorf("code = %s, want PortalHTMLResponse", code)
	}
}

// A login cannot be built without a challenge: the token is what the whole
// request is signed with, and sending an unsigned one wastes an attempt and
// teaches the user nothing.
func TestALoginWithoutAChallengeIsRefusedBeforeSending(t *testing.T) {
	gateway := newFakeGateway(t)
	transaction := transactionFor(t, gateway)

	_, err := transaction.Login(t.Context(),
		Credentials{Username: "2020123456", Password: "pw"}, Shape{}, Challenge{})
	if err == nil {
		t.Fatal("a login was built with no token")
	}
	if len(gateway.seen()) != 0 {
		t.Errorf("a request was sent anyway: %+v", gateway.seen())
	}
}

// The account's own protocol shape is used, and the defaults fill only what it
// left unset.
func TestTheAccountsShapeOverridesTheDefaults(t *testing.T) {
	gateway := newFakeGateway(t)
	transaction := transactionFor(t, gateway)

	challenge, _ := transaction.Challenge(t.Context(), "2020123456")
	if _, err := transaction.Login(t.Context(),
		Credentials{Username: "2020123456", Password: "pw"},
		Shape{N: "201", Type: "3", DoubleStack: true}, challenge); err != nil {
		t.Fatalf("Login: %v", err)
	}

	query := gateway.lastQuery(t, portalPath)
	if query.Get("n") != "201" || query.Get("type") != "3" {
		t.Errorf("n/type = %q/%q, want the account's own values",
			query.Get("n"), query.Get("type"))
	}
	if query.Get("double_stack") != "1" {
		t.Errorf("double_stack = %q, want the protocol's 1", query.Get("double_stack"))
	}
	// Unset fields still get the protocol's defaults.
	if query.Get("os") != "Windows 10" || query.Get("name") != "Windows" {
		t.Errorf("os/name = %q/%q", query.Get("os"), query.Get("name"))
	}
}

// double_stack is "0" or "1" on the wire, never "true".
func TestDoubleStackIsTheProtocolsSpelling(t *testing.T) {
	if boolWire(true) != "1" || boolWire(false) != "0" {
		t.Errorf("boolWire gives %q/%q, want 1/0", boolWire(true), boolWire(false))
	}
}

// The gateway's view of the client address is used when it gives one, because
// behind NAT it differs from the bound source and it is what the session will
// be recorded against.
func TestTheGatewaysClientAddressIsPreferredForTheLogin(t *testing.T) {
	gateway := newFakeGateway(t)
	gateway.configure(func(g *fakeGateway) { g.clientIP = "203.0.113.9" })
	transaction := transactionFor(t, gateway)

	challenge, _ := transaction.Challenge(t.Context(), "2020123456")
	if challenge.ClientIP != "203.0.113.9" {
		t.Fatalf("client_ip = %q", challenge.ClientIP)
	}
	if _, err := transaction.Login(t.Context(),
		Credentials{Username: "2020123456", Password: "pw"}, Shape{},
		challenge); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := gateway.lastQuery(t, portalPath).Get("ip"); got != "203.0.113.9" {
		t.Errorf("ip = %q, want the address the gateway reported", got)
	}
}

// With no reported address the bound source is used: it is what the packets
// actually carry, and inventing something else would be worse.
func TestWithoutAReportedAddressTheBoundSourceIsUsed(t *testing.T) {
	gateway := newFakeGateway(t)
	gateway.configure(func(g *fakeGateway) { g.clientIP = "" })
	transaction := transactionFor(t, gateway)

	challenge, _ := transaction.Challenge(t.Context(), "2020123456")
	if _, err := transaction.Login(t.Context(),
		Credentials{Username: "2020123456", Password: "pw"}, Shape{},
		challenge); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := gateway.lastQuery(t, portalPath).Get("ip"); got != "10.0.0.77" {
		t.Errorf("ip = %q, want the bound source address", got)
	}
}
