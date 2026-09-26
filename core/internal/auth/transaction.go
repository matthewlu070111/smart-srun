package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/protocol/srun"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// srunLogoutUnbind is the value the logout signature and URL both carry.
const srunLogoutUnbind = srun.LogoutUnbind

// Intent says how much this program may disturb.
//
// Spec 04 draws a hard line here. Automatic maintenance may look at an online
// session that belongs to somebody else and report it, and nothing more --
// knocking a stranger off a shared line because a timer fired is not a repair.
// An explicit action by the user is different: they asked for this account on
// this line, so the session currently on this line may be cleared first. The
// two paths are separate because collapsing them is how an unattended daemon
// starts fighting over a gateway.
type Intent string

const (
	// IntentAutomatic is maintenance. It never ends a session it did not
	// establish.
	IntentAutomatic Intent = "automatic"
	// IntentManual is a user asking for this account now. It may clear the
	// session this line currently holds, once.
	IntentManual Intent = "manual"
)

// Credentials identify the user on the wire.
//
// Username is already assembled, suffix included: deciding whether to append
// an operator suffix belongs to configuration, and doing it here would give two
// places an opinion about it.
type Credentials struct {
	Username string
	Password string
}

// Shape is the protocol variation this account needs.
//
// These vary by school and sometimes by account within a school, which is why
// they travel with the request rather than living in a strategy. Empty fields
// mean "use the protocol's default", resolved once by configuration.
type Shape struct {
	N           string
	Type        string
	Enc         string
	InfoPrefix  string
	OS          string
	Name        string
	DoubleStack bool
	// Alphabet is the gateway's Base64 table. Empty means the one almost every
	// SRun deployment uses; a school with its own table sets it per account.
	//
	// Carried as the table rather than a parsed encoder because a Shape is
	// assembled by the coordinator from resolved configuration, and that layer
	// has nowhere to report a malformed table. Login is where it can fail
	// usefully, so that is where it is parsed.
	Alphabet string
}

// Line is the part of a bound transport this package uses.
//
// Declared here, by the consumer, as spec 02 requires. It is deliberately tiny:
// two methods is all an authentication attempt needs, and a package that could
// reach more of the transport could start making decisions about connections
// that belong to the transport. *transport.Client satisfies it; nothing in this
// package can construct one, which is the point -- the line is chosen by
// whoever owns the binding, not by the code sending credentials over it.
type Line interface {
	Do(req *http.Request) (*http.Response, error)
	// SourceAddr is the address requests leave from. SRun's login parameters
	// carry the client address, and it has to be the one the socket used.
	SourceAddr() netip.Addr
}

// Transaction performs one authentication attempt over one bound line.
//
// It holds no state between calls beyond what it was constructed with. A
// Transaction is made for one attempt and discarded, so there is nowhere for a
// stale token or a previous answer to survive into the next one.
type Transaction struct {
	client  Line
	gateway Gateway
	// now and callback are injected so the request bytes are decided entirely
	// by the caller. The protocol layer refuses to read a clock for the same
	// reason, and a transaction that generated its own would be untestable
	// against a fixed expectation.
	now      func() time.Time
	callback func() string
}

// NewTransaction builds one attempt.
func NewTransaction(client Line, gateway Gateway) *Transaction {
	return &Transaction{
		client:   client,
		gateway:  gateway,
		now:      time.Now,
		callback: defaultCallback,
	}
}

// defaultCallback produces a name the response parser will unwrap.
//
// jQuery-shaped because that is what the portals expect and what the baseline
// sent; the digits come from the clock so two requests in flight cannot be
// confused by a caching proxy on the path.
func defaultCallback() string {
	return "jQuery" + strconv.FormatInt(time.Now().UnixNano()%1e12, 10)
}

// Challenge is the token a login must be signed with, and the address the
// gateway believes the client has.
type Challenge struct {
	Token string
	// ClientIP is what the gateway reported. Spec 04 keeps this separate from
	// the binding's source address on purpose: behind NAT they differ, and
	// binding a socket to the address the far end reported would fail. This
	// value goes into the login parameters; it is never dialed.
	ClientIP string
}

// Challenge fetches a token.
func (t *Transaction) Challenge(ctx context.Context, username string) (Challenge, error) {
	milliseconds := t.now().UnixMilli()
	callback := t.callback()

	payload, err := t.get(ctx, t.gateway.challengeURL(username, milliseconds, callback),
		"挑战值")
	if err != nil {
		return Challenge{}, err
	}

	var answer struct {
		Challenge string `json:"challenge"`
		ClientIP  string `json:"client_ip"`
		OnlineIP  string `json:"online_ip"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return Challenge{}, domain.Errorf(domain.CodeProtocolInvalid,
			"认证网关返回的挑战值无法解析").Wrap(err)
	}
	if answer.Challenge == "" {
		return Challenge{}, domain.Errorf(domain.CodeProtocolInvalid,
			"认证网关没有返回挑战值")
	}

	// Either field may carry the address; the baseline accepted both and so do
	// we, preferring the one the protocol documents.
	address := answer.ClientIP
	if address == "" {
		address = answer.OnlineIP
	}
	return Challenge{Token: answer.Challenge, ClientIP: address}, nil
}

// Login sends the credentials and reports what the gateway said.
//
// The result is Accepted at best, never VerifiedSelf: the gateway saying yes is
// the gateway's claim about itself. Confirming that the session belongs to this
// account is a separate query, because spec 04 will not treat an acceptance as
// an identity.
func (t *Transaction) Login(ctx context.Context, creds Credentials, shape Shape,
	challenge Challenge) (Result, error) {

	if creds.Username == "" {
		return Result{}, domain.FieldErrorf(domain.CodeInvalidArgument,
			"user_id", "未提供学工号")
	}
	if challenge.Token == "" {
		return Result{}, domain.Errorf(domain.CodeInternal,
			"登录前必须先取得挑战值")
	}

	shape = shape.withDefaults()
	address := challenge.ClientIP
	if address == "" {
		// Without the gateway's own view, the bound source address is the
		// honest answer. It is what the packets will carry.
		address = t.client.SourceAddr().String()
	}

	digest := srun.HMACMD5Hex(challenge.Token, creds.Password)
	infoJSON, err := srun.EncodeInfo(srun.Info{
		Username: creds.Username,
		Password: creds.Password,
		IP:       address,
		ACID:     t.gateway.ACID,
		EncVer:   shape.Enc,
	})
	if err != nil {
		return Result{}, err
	}
	// The table is passed explicitly so the choice is visible at the place the
	// credentials are encrypted.
	//
	// A configured table that does not parse fails the login rather than
	// falling back to the common one. Falling back would encode the blob with a
	// table the gateway does not use, and because the blob is encrypted and
	// checksummed the only symptom would be a rejected login: the user would be
	// told their password is wrong. Configuration validation refuses a bad
	// table at save time, so reaching here with one means it arrived some other
	// way, and guessing is the worst available answer.
	alphabet := srun.DefaultAlphabet
	if shape.Alphabet != "" {
		configured, err := srun.NewAlphabet(shape.Alphabet)
		if err != nil {
			return Result{}, err
		}
		alphabet = configured
	}
	encrypted := srun.EncryptedInfo(shape.InfoPrefix, infoJSON, challenge.Token, alphabet)

	// The checksum covers the bare digest, not the {MD5}-prefixed wire field.
	checksum := srun.Checksum(challenge.Token, creds.Username, digest,
		t.gateway.ACID, address, shape.N, shape.Type, encrypted)

	payload, err := t.get(ctx, t.gateway.loginURL(loginParams{
		callback:     t.callback(),
		username:     creds.Username,
		wirePassword: srun.WirePasswordPrefix + digest,
		ip:           address,
		checksum:     checksum,
		info:         encrypted,
		n:            shape.N,
		loginType:    shape.Type,
		os:           shape.OS,
		name:         shape.Name,
		doubleStack:  boolWire(shape.DoubleStack),
		milliseconds: t.now().UnixMilli(),
	}), "登录响应")
	if err != nil {
		return Result{}, err
	}
	return interpret(payload)
}

// Online reports who is authenticated on this line right now.
//
// This is what turns an acceptance into a verified identity, and what tells an
// already-online answer apart from somebody else's session.
func (t *Transaction) Online(ctx context.Context, expected string) (Identity, error) {
	payload, err := t.get(ctx, t.gateway.onlineURL(t.now().UnixMilli(), t.callback()),
		"在线状态")
	if err != nil {
		return Identity{}, err
	}
	return readIdentity(payload, expected)
}

// Logout ends the session named by username.
//
// The username is the caller's decision and the caller must have earned it:
// spec 04 requires an explicit manual cleanup to use the identity this line's
// own query just reported, not whatever the front end passed in, and never an
// account on another line.
func (t *Transaction) Logout(ctx context.Context, username, ip string) (Result, error) {
	if username == "" {
		return Result{}, domain.FieldErrorf(domain.CodeInvalidArgument,
			"user_id", "登出需要知道要下线的账号")
	}
	if ip == "" {
		ip = t.client.SourceAddr().String()
	}

	// Seconds here, not milliseconds. Both the signature and the URL use the
	// same value, and it has to be the same one.
	seconds := t.now().Unix()
	sign := srun.LogoutSign(seconds, username, ip)

	payload, err := t.get(ctx,
		t.gateway.logoutURL(username, ip, sign, seconds, t.callback()), "登出响应")
	if err != nil {
		return Result{}, err
	}
	return interpret(payload)
}

// get performs one bounded request and returns the JSON inside the JSONP.
func (t *Transaction) get(ctx context.Context, target, what string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法构造%s请求", what).Wrap(err)
	}
	// A portal that decides by user agent is common enough that the baseline
	// sent a browser's; keeping that is not a workaround to remove.
	req.Header.Set("User-Agent", browserUserAgent)

	response, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer transport.DrainAndClose(response.Body)

	body, err := transport.ReadBounded(response.Body,
		transport.MaxAuthenticationBody, what)
	if err != nil {
		return nil, err
	}

	// The kind is discarded here on purpose. Classifying the answer -- a portal
	// page, an empty body, something unparseable -- belongs to the protocol
	// layer, and it already returns the right code with it, including
	// PortalHTMLResponse for an interception. Re-deriving that code from the
	// kind was a second place deciding the same thing, and a mutation proved it
	// decided nothing: removing it changed no behaviour at all. The kind
	// becomes useful again when there is a log to count it in (M11).
	payload, _, err := srun.ParseJSONP(body, transport.MaxAuthenticationBody)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// browserUserAgent is what the baseline sent. Portals do look at it.
const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; WOW64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/63.0.3239.26 Safari/537.36"

// withDefaults fills the protocol's own defaults for anything unset.
//
// Spec 03 fixes these: SRBX1, false, Windows 10, Windows. They are here rather
// than in configuration because they are the protocol's, and an account that
// overrides them has already had its value resolved by then.
func (s Shape) withDefaults() Shape {
	if s.N == "" {
		s.N = "200"
	}
	if s.Type == "" {
		s.Type = "1"
	}
	if s.Enc == "" {
		s.Enc = "srun_bx1"
	}
	s.InfoPrefix = srun.NormalizePrefix(s.InfoPrefix, srun.DefaultInfoPrefix)
	if s.OS == "" {
		s.OS = "Windows 10"
	}
	if s.Name == "" {
		s.Name = "Windows"
	}
	return s
}

// boolWire is the protocol's spelling for a boolean: "0" or "1", never "true".
func boolWire(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// trimIdentity normalises an account name for comparison.
//
// Case is preserved: a gateway that distinguishes two accounts by case would
// otherwise have them merged here, and treating somebody else's session as
// this one's is the mistake that matters most in this package.
func trimIdentity(value string) string { return strings.TrimSpace(value) }
