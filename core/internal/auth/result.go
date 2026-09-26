package auth

import (
	"encoding/json"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Result is what one request to the gateway produced.
//
// State is deliberately not a bool. A gateway that says "ok" has said something
// about itself, not about whose session is now on the line, and spec 04 keeps
// those apart all the way through.
type Result struct {
	State domain.AuthState
	// AlreadyOnline is the gateway's own "this line already has a session"
	// answer. It is not a success: the session may belong to someone else, and
	// which it is has to be asked separately.
	AlreadyOnline bool
	// GatewayCode and GatewayMessage are what the portal said, kept for the
	// log. They are the portal's words, not this program's, and are never
	// shown to a user as if they were an explanation this program wrote.
	GatewayCode    string
	GatewayMessage string
	// Identity is who the gateway named, when it named anyone.
	Identity string
	// ClientIP is the address the gateway associated with the session.
	ClientIP string
}

// Identity is the answer to "who is online on this line".
//
// There is no "unknown" value here on purpose. A query that did not establish
// anything returns an error instead, so every Identity that exists is evidence.
// The distinction matters because "nobody is online" is what a logout is
// checked against and what the stale-session recovery branches on: if a failed
// query could produce Present=false, both would act on a fact nobody
// established.
type Identity struct {
	// Present is false only when the gateway confirmed that nobody is
	// authenticated on this line.
	Present bool
	// Username as the gateway reported it, including a realm the gateway
	// reported separately.
	Username string
	// SessionUsername is user_name exactly as reported (without synthesizing a
	// separately returned realm). The signed DM logout uses this name, while
	// Username retains the realm for identity checks and observations.
	SessionUsername string
	ClientIP        string
	// MatchesExpected is whether this is the account that was asked about.
	// See sameAccount for what that means and what it deliberately refuses.
	MatchesExpected bool
}

// State turns an identity into the dimension spec 04 defines.
//
// An absent identity is Offline rather than Unknown, and it can be: readIdentity
// only produces one when the gateway positively said so. A query that failed
// never becomes an Identity at all.
func (i Identity) State() domain.AuthState {
	switch {
	case !i.Present:
		return domain.AuthOffline
	case i.MatchesExpected:
		return domain.AuthVerifiedSelf
	default:
		return domain.AuthVerifiedOther
	}
}

// gatewayAnswer is the shape SRun portals reply with. Fields vary between
// deployments, so everything is optional and nothing is required to parse.
type gatewayAnswer struct {
	Error    string          `json:"error"`
	ErrorMsg string          `json:"error_msg"`
	ECode    json.RawMessage `json:"ecode"`
	ClientIP string          `json:"client_ip"`
	OnlineIP string          `json:"online_ip"`
	UserName string          `json:"user_name"`
	// Domain is the realm reported as its own field rather than as a suffix on
	// user_name. Spec 04 requires it to take part in the identity comparison:
	// dropping it turns "alice on cmcc" into bare "alice", which then matches
	// an expectation of "alice@ctcc".
	Domain      string          `json:"domain"`
	SuccessMsg  string          `json:"suc_msg"`
	ResultValue json.RawMessage `json:"res"`
}

// code is the gateway's status, wherever it put it.
func (a gatewayAnswer) code() string {
	if trimmed := strings.TrimSpace(a.Error); trimmed != "" {
		return trimmed
	}
	return strings.Trim(string(a.ResultValue), `"`)
}

// identity is the account the answer names, with a separately reported realm
// folded in.
//
// Only when user_name carries no realm of its own: a gateway that reports both
// has already said which realm the session is on, and appending the other field
// would invent "alice@cmcc@ctcc".
func (a gatewayAnswer) identity() string {
	name := trimIdentity(a.UserName)
	realm := trimIdentity(a.Domain)
	if name == "" || realm == "" || strings.ContainsRune(name, '@') {
		return name
	}
	return name + "@" + realm
}

// interpret reads a login or logout reply.
//
// The three answers that matter are "ok", "already online", and everything
// else. Only the first two are not failures, and the second is not a success
// either -- it is a question about whose session that is.
//
// It deliberately takes no expected username. Deciding whose session is on the
// line is readIdentity's job, against the online endpoint; this reply is the
// gateway's answer about the request, not about the session. The parameter used
// to be here and was never read, which made the signature claim an identity
// check that the body did not perform.
func interpret(payload json.RawMessage) (Result, error) {
	var answer gatewayAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return Result{}, domain.Errorf(domain.CodeProtocolInvalid,
			"认证网关的响应无法解析").Wrap(err)
	}

	result := Result{
		GatewayCode:    answer.code(),
		GatewayMessage: strings.TrimSpace(answer.ErrorMsg),
		Identity:       answer.identity(),
		ClientIP:       firstNonEmpty(answer.ClientIP, answer.OnlineIP),
	}

	switch {
	case isOK(result.GatewayCode):
		result.State = domain.AuthAccepted
	case isAlreadyOnline(result.GatewayCode, result.GatewayMessage):
		result.AlreadyOnline = true
		// Not Accepted. Something is online; whether it is this account is a
		// separate question, and answering it optimistically here is how a
		// user gets told they are logged in on somebody else's session.
		result.State = domain.AuthUnknown
	default:
		result.State = domain.AuthRejected
	}
	return result, nil
}

// readIdentity reads the online-status reply.
//
// Three outcomes, and keeping them apart is the whole job. The query can say
// who is online, it can confirm that nobody is, or it can fail -- and a failure
// is not the second one. HTTP and JSON both succeeding is not evidence about
// sessions: `{"error":"backend_busy"}` parses perfectly and names nobody, and
// reading that as "nobody is online" makes a logout report success it did not
// achieve and sends the stale-session recovery down a branch it should not be
// on.
func readIdentity(payload json.RawMessage, expected string) (Identity, error) {
	var answer gatewayAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return Identity{}, domain.Errorf(domain.CodeProtocolInvalid,
			"在线状态无法解析").Wrap(err)
	}

	code := answer.code()
	username := answer.identity()

	switch {
	case isConfirmedOffline(code, answer.ErrorMsg):
		return Identity{Present: false}, nil

	case username != "":
		// A named session, whether or not the gateway also said "ok". Some
		// deployments answer the record with no error field at all, and
		// requiring one would make this program blind on those.
		return Identity{
			Present:         true,
			Username:        username,
			SessionUsername: trimIdentity(answer.UserName),
			ClientIP:        firstNonEmpty(answer.ClientIP, answer.OnlineIP),
			MatchesExpected: sameAccount(username, expected),
		}, nil

	case isOK(code) || code == "":
		// The gateway answered successfully and named nobody. The baseline
		// reads this as offline and so does this.
		return Identity{Present: false}, nil

	default:
		// Anything else is the query itself failing. Say so, with the gateway's
		// own words in the log and Unknown as the state the caller is left in.
		return Identity{}, domain.Errorf(domain.CodeProtocolInvalid,
			"在线状态查询失败：%s", firstNonEmpty(strings.TrimSpace(answer.ErrorMsg), code))
	}
}

// sameAccount compares two account names, allowing for a realm one side omits
// and refusing two realms that disagree.
//
// The permissive half is necessary: a gateway may report "2020123456" for a
// login sent as "2020123456@cmcc", and treating those as different users would
// make this program log itself out and try again forever. The baseline compares
// bare accounts for exactly that reason.
//
// The restrictive half is what the baseline is missing. "alice@ctcc" and
// "alice@cmcc" are two accounts on two carriers that happen to share a student
// number, and folding them together lets this program claim somebody else's
// session -- and, on a manual login, end it. So the bare accounts agreeing is
// necessary but not sufficient: if both sides state a realm, the realms have to
// agree too. Only when at least one side is silent about the realm is there no
// contradiction to act on.
func sameAccount(reported, expected string) bool {
	reported = trimIdentity(reported)
	expected = trimIdentity(expected)
	if reported == "" || expected == "" {
		return false
	}
	if reported == expected {
		return true
	}
	if bareAccount(reported) != bareAccount(expected) {
		return false
	}

	reportedRealm, expectedRealm := realmOf(reported), realmOf(expected)
	if reportedRealm == "" || expectedRealm == "" {
		// One side did not say. Nothing contradicts anything.
		return true
	}
	// Both said, and they are not byte-identical or the check above would have
	// returned. Only the realm is compared case-insensitively, and only here:
	// realms are domain-shaped and gateways echo them in whatever case they
	// like, while the account part is a credential and is never folded.
	return strings.EqualFold(reportedRealm, expectedRealm)
}

// bareAccount drops an operator suffix.
func bareAccount(value string) string {
	if at := strings.IndexByte(value, '@'); at > 0 {
		return value[:at]
	}
	return value
}

// realmOf is the suffix, or "" when the name states none.
func realmOf(value string) string {
	if at := strings.IndexByte(value, '@'); at > 0 {
		return value[at+1:]
	}
	return ""
}

// isOK recognises the gateway's success code.
func isOK(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), "ok")
}

// alreadyOnlineMarkers are the ways a gateway says "this line already has a
// session".
//
// The list is closed and every entry is one the baseline acts on:
// orchestrator.py and portal_detect.py both key off "e2620", "already online"
// and the Chinese "已在线". E2620 in particular is the stuck-session case the
// controlled STA rebuild exists for -- classified as a plain rejection it looks
// like a bad password, so the recovery never runs and the account stays locked
// out until somebody reboots something.
//
// It is a list rather than a prefix rule on purpose. "Every code starting with
// E is a session we may end" would hand this program permission to log people
// out over a typo in a password.
var alreadyOnlineMarkers = []string{
	"ip_already_online_error",
	"already online",
	"already_online",
	"e2620",
	"已在线",
}

// isAlreadyOnline recognises the "this line already has a session" answer.
//
// The message is checked as well as the code: some portals put the marker only
// there, and the baseline searches the whole message for the same reason.
func isAlreadyOnline(code, message string) bool {
	haystack := strings.ToLower(code + " " + message)
	for _, marker := range alreadyOnlineMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

// offlineMarkers are the answers that positively establish an empty line.
//
// Closed, for the same reason as above but pointing the other way: an
// unrecognised failure must not become "nobody is online", because that is the
// fact a logout reports success on.
var offlineMarkers = []string{
	"not_online_error",
	"not_online",
	"未在线",
	"不在线",
}

func isConfirmedOffline(code, message string) bool {
	haystack := strings.ToLower(strings.TrimSpace(code) + " " + strings.TrimSpace(message))
	for _, marker := range offlineMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
