package application

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/matthewlu070111/smart-srun/core/internal/auth"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	portalprobe "github.com/matthewlu070111/smart-srun/core/internal/portal"
)

// Binder answers where a line is and whether it can carry traffic.
//
// Consumer-defined, as spec 02 requires: implementing it needs a router, and
// this package has to be testable without one. The adapter that does implement
// it reads the interface; deciding which interface to read is this layer's job,
// and the dependency points that way round so that neither can drift into the
// other's.
type Binder interface {
	// ResolveBinding produces one observation of how a line reaches the
	// network. The generation is supplied by the caller, so a reply that
	// arrives from a line that no longer exists can be recognised.
	ResolveBinding(ctx context.Context, logicalIface string,
		generation uint64) (domain.Binding, error)
	// LinkState is what to tell a user when the binding could not be made.
	// "BindingUnavailable" is this program's word; "the cable is unplugged" is
	// the one that gets the problem fixed, and only the adapter can tell the
	// difference between that, a missing interface and DHCP still in flight.
	LinkState(ctx context.Context, logicalIface string) (domain.LinkState, error)
}

// Lines hands out the bound transport an attempt sends over.
//
// It returns auth.Line -- two methods -- rather than a transport client,
// because that is all an attempt needs and because a test can then supply one
// without opening a socket. The connection pool behind it is assembled in the
// daemon's wiring, which is the only place that knows both halves.
type Lines interface {
	Line(accountID string, binding domain.Binding, gateway string) (auth.Line, error)
	// Retire closes the clients belonging to generations older than the
	// current one, so a reply cannot arrive over a socket bound to an address
	// this account no longer has.
	Retire(accountID string, currentGeneration uint64) int
}

// Settings is the configuration an attempt reads, once, at its start.
//
// Once: spec 03 resolves an account into an immutable effective form, and an
// attempt that re-read the configuration halfway through could send a password
// from before a save with a username from after it.
type Settings interface {
	Snapshot() domain.Config
	Revision() uint64
}

// Authenticator performs the actions the coordinator schedules.
//
// It is the Runner the coordinator was built around, and it holds no state
// between actions except the binding generation counter -- which only ever goes
// up, so two observations can always be ordered.
type Authenticator struct {
	binder    Binder
	lines     Lines
	settings  Settings
	wireless  Wireless
	clock     policy.Clock
	probeURLs []string

	generation atomic.Uint64

	// seen is the binding each account was last observed on, so a generation is
	// spent when the line changes rather than when an action starts.
	mu   sync.Mutex
	seen map[string]domain.Binding
}

// AuthenticatorOptions wires one worker.
//
// A struct rather than five positional parameters, and Wireless is allowed to
// be nil: a build without the wireless transaction still authenticates over a
// client somebody configured by hand, it just cannot move the radio itself.
type AuthenticatorOptions struct {
	Binder   Binder
	Lines    Lines
	Settings Settings
	// Wireless moves the managed client. Nil until M10 supplies the
	// transaction, and a switch is refused rather than half-performed while it
	// is nil.
	Wireless Wireless
	Clock    policy.Clock
	// ConnectivityURLs replaces the ordered, credential-free probe endpoints
	// for an isolated environment. Nil uses the shipped endpoint list.
	ConnectivityURLs []string
}

// NewAuthenticator wires one.
func NewAuthenticator(options AuthenticatorOptions) *Authenticator {
	clock := options.Clock
	if clock == nil {
		clock = policy.SystemClock{}
	}
	urls := options.ConnectivityURLs
	if urls == nil {
		urls = portalprobe.ConnectivityURLs()
	}
	return &Authenticator{
		binder:    options.Binder,
		lines:     options.Lines,
		settings:  options.Settings,
		wireless:  options.Wireless,
		clock:     clock,
		probeURLs: append([]string(nil), urls...),
		seen:      map[string]domain.Binding{},
	}
}

// Run performs one action.
func (a *Authenticator) Run(ctx context.Context, action Action,
	report func(Phase)) Outcome {
	if kind := action.Request.Kind; kind == KindLogin || kind == KindRelogin || kind == KindMaintain || kind == KindSwitchCampus {
		cfg := a.settings.Snapshot()
		if _, known := cfg.CampusAccountByID(action.Request.AccountID); !known {
			return failure(domain.Errorf(domain.CodeNotFound, "账号 %s 不存在", action.Request.AccountID))
		}
		if !action.Request.IgnoreQuiet && policy.EvaluateQuiet(cfg.Quiet, a.clock.Now()).Active {
			return failure(domain.Errorf(domain.CodeBusy, "当前为静默时段，未修改线路或发起认证；手动操作可选择仅本次忽略静默"))
		}
	}
	switch action.Request.Kind {
	case KindQuietHotspot, KindQuietCampus:
		return a.quietSwitch(ctx, action, report)
	case KindLogin, KindRelogin, KindMaintain:
		return a.authenticate(ctx, action, report)
	case KindSwitchCampus:
		return a.switchCampus(ctx, action, report)
	case KindForcedLogout:
		cfg := a.settings.Snapshot()
		quiet := policy.EvaluateQuiet(cfg.Quiet, a.clock.Now())
		if !cfg.Enabled || !quiet.Active || !quiet.ForceLogout {
			return quietSwitchDeferred("静默下线条件已变化，未发送退出请求")
		}
		return a.logout(ctx, action, report)
	case KindLogout:
		return a.manualLogout(ctx, action, report)
	case KindSwitchHotspot:
		return a.switchHotspot(ctx, action, report)
	default:
		// Reached only by a kind that no decorator claimed. RoutedElsewhere
		// names the ones that are somebody else's on purpose, and a test pins
		// HandledHere plus RoutedElsewhere to AllKinds -- so a new kind whose
		// router was never wired fails that test instead of reaching a user as
		// "not implemented".
		return Outcome{State: StateFailed, Code: domain.CodeUnsupportedCapability,
			Message: "动作 " + string(action.Request.Kind) + " 没有对应的执行器，请报告此问题"}
	}
}

// HandledHere are the kinds the authentication worker performs itself.
//
// Listed rather than derived: the switch above is the implementation, and a
// list generated from it could not catch the switch being wrong.
func HandledHere() []Kind {
	return []Kind{KindQuietHotspot, KindQuietCampus, KindLogin, KindRelogin,
		KindMaintain, KindSwitchCampus, KindForcedLogout, KindLogout,
		KindSwitchHotspot}
}

// RoutedElsewhere are the kinds a decorator intercepts before this worker sees
// them: preset refresh, the discovery probes and the wireless wizard.
func RoutedElsewhere() []Kind {
	return []Kind{KindPresetsRefresh, KindDetectACID, KindDetectEnvironment,
		KindDetectOperators, KindDetectIdentity, KindDetectVerify,
		KindWifiSetupStart, KindWifiSetupCancel}
}

// attempt is everything one action needs, resolved once at its start.
type attempt struct {
	account   domain.CampusAccount
	username  string
	shape     auth.Shape
	intent    auth.Intent
	checkMode domain.CheckMode
	checks    domain.ChecksConfig
	// Scheduled transitions wait for the portal to settle before moving on.
	// Their authentication intent remains automatic: another user's session
	// must never be cleared merely because a timetable fired.
	confirmTerminal bool
	revision        uint64
	binding         domain.Binding
	line            auth.Line
	gateway         auth.Gateway
	sequence        uint64
}

// authenticate is the login path: challenge, login, and then ask whose session
// is actually on the line.
func (a *Authenticator) authenticate(ctx context.Context, action Action,
	report func(Phase)) Outcome {

	// The radio has to be on the right network before there is a line to
	// authenticate over. This is a no-op for a wired account, for a build with
	// no wireless transaction, and -- the case that matters on every
	// maintenance tick -- for a client that is already associated with an
	// address, which is checked without scanning.
	if outcome, stop := a.ensureWirelessLine(ctx, action, report); stop {
		return outcome
	}

	prepared, outcome := a.prepare(ctx, action, report)
	if prepared == nil {
		return outcome
	}
	transaction := auth.NewTransaction(prepared.line, prepared.gateway)
	if action.Request.Kind == KindMaintain || action.Request.Kind == KindQuietCampus {
		if outcome, stop := a.checkExisting(ctx, transaction, prepared, report); stop {
			return outcome
		}
	}

	result, err := a.attemptLogin(ctx, transaction, prepared, report)
	if err != nil {
		return prepared.failed(a, err, domain.AuthAuthenticating, "")
	}

	if result.AlreadyOnline {
		return a.settleAlreadyOnline(ctx, transaction, prepared, report)
	}
	if result.State == domain.AuthRejected {
		// A refusal is an answer, and retrying it in a loop is how an account
		// gets locked out. The coordinator sees a failure it must not repeat.
		return prepared.failed(a,
			domain.Errorf(domain.CodeAuthRejected, "网关拒绝了本次登录"),
			domain.AuthRejected, result.Identity)
	}

	return a.verify(ctx, transaction, prepared, report)
}

// verify turns the gateway's acceptance into a checked identity.
//
// Spec 04 will not let "the gateway said ok" stand as "this account is online":
// that is the gateway's claim about itself, and the session it is talking about
// may belong to somebody else.
func (a *Authenticator) verify(ctx context.Context, transaction *auth.Transaction,
	prepared *attempt, report func(Phase)) Outcome {
	return a.verifySession(ctx, transaction, prepared, report, nil)
}

func (a *Authenticator) verifyOnce(ctx context.Context, transaction *auth.Transaction,
	prepared *attempt, report func(Phase), known *auth.Identity) Outcome {

	report(PhaseVerify)
	var identity auth.Identity
	var err error
	if known != nil {
		identity = *known
	} else {
		identity, err = transaction.Online(ctx, prepared.username)
	}
	if err != nil {
		// Accepted but unverifiable. Reporting success here would be reporting
		// the gateway's word for it.
		return prepared.failed(a, err, domain.AuthAccepted, "")
	}

	switch identity.State() {
	case domain.AuthVerifiedSelf:
		return a.verifyConnectivity(ctx, prepared, "认证完成", identity.Username)
	case domain.AuthVerifiedOther:
		return prepared.failed(a,
			domain.Errorf(domain.CodeOnlineIdentityMismatch,
				"这条线路上在线的是另一个账号"),
			domain.AuthVerifiedOther, identity.Username)
	default:
		return prepared.failed(a,
			domain.Errorf(domain.CodeDeadlineExceeded,
				"网关接受了登录，但这条线路上查不到任何在线会话"),
			domain.AuthAccepted, "")
	}
}

// settleAlreadyOnline handles the gateway's "this line already has a session".
//
// It is neither success nor failure until somebody asks whose session it is,
// and the baseline learned the two cases the hard way:
//
//   - the session is on this address and is ours. A router that rebooted and
//     got the same lease back is already in the state the user wants; calling
//     it a failure makes the daemon retry with backoff forever.
//   - the session is on an address this account no longer has, after a reboot
//     or a reconnect changed the lease. Then the old session has to be unbound
//     before a new login can take.
//
// The clean-up happens once. Doing it in a loop would be a program that logs
// somebody out every time it is unsure.
func (a *Authenticator) settleAlreadyOnline(ctx context.Context,
	transaction *auth.Transaction, prepared *attempt,
	report func(Phase)) Outcome {

	report(PhaseVerify)
	identity, err := transaction.Online(ctx, prepared.username)
	if err != nil {
		return prepared.failed(a, err, domain.AuthAccepted, "")
	}

	switch identity.State() {
	case domain.AuthVerifiedSelf:
		return a.verifySession(ctx, transaction, prepared, report, &identity)

	case domain.AuthVerifiedOther:
		if prepared.intent != auth.IntentManual {
			// Spec 04: automatic maintenance reports another identity and never
			// ends it. The account on this line may be a housemate's.
			return prepared.failed(a,
				domain.Errorf(domain.CodeOnlineIdentityMismatch,
					"这条线路上在线的是另一个账号，自动维护不会将其下线"),
				domain.AuthVerifiedOther, identity.Username)
		}
		// An explicit request may clear this line's session -- and it clears
		// the identity the query just returned, not a name somebody passed in.
		return a.clearAndRetry(ctx, transaction, prepared,
			identity.SessionUsername, identity.Username, report)

	default:
		// Nobody is online at this address, so the session the gateway is
		// refusing over is on the one before the lease changed.
		return a.clearAndRetry(ctx, transaction, prepared,
			prepared.account.UserID, prepared.username, report)
	}
}

// attemptLogin is the only path credentials take.
//
// Challenge, re-check the line, then send -- in that order, every time. The
// first attempt and the retry after a clean-up both go through here because a
// retry that skipped either step would be a second, weaker way of doing the
// same thing, and that is precisely what the earlier version was: it reused the
// token issued before the unbind and never looked at the line again, so a lease
// that moved during the clean-up sent credentials from an address the account
// no longer had.
//
// Spec 04 asks for both. A token issued for an address this account has since
// lost is refused by the gateway without saying why, and the attempt is spent.
func (a *Authenticator) attemptLogin(ctx context.Context,
	transaction *auth.Transaction, prepared *attempt,
	report func(Phase)) (auth.Result, error) {

	report(PhaseChallenge)
	challenge, err := transaction.Challenge(ctx, prepared.username)
	if err != nil {
		return auth.Result{}, err
	}
	if err := a.confirmUnchanged(ctx, prepared); err != nil {
		return auth.Result{}, err
	}

	report(PhaseLogin)
	return transaction.Login(ctx,
		auth.Credentials{Username: prepared.username,
			Password: prepared.account.Password},
		prepared.shape, challenge)
}

// clearAndRetry unbinds one session and logs in once more.
func (a *Authenticator) clearAndRetry(ctx context.Context,
	transaction *auth.Transaction, prepared *attempt,
	sessionUsername, identity string, report func(Phase)) Outcome {

	report(PhaseLogout)
	cleared, err := transaction.Logout(ctx, sessionUsername, "")
	if err != nil {
		return prepared.failed(a, err, domain.AuthVerifiedOther, identity)
	}
	if cleared.State == domain.AuthRejected {
		// The gateway refused the unbind. Logging in again on the strength of a
		// clean-up that did not happen just spends another attempt.
		return prepared.failed(a,
			domain.Errorf(domain.CodeConflict,
				"网关拒绝了清理旧会话的请求：%s", gatewayWords(cleared)),
			domain.AuthVerifiedOther, identity)
	}

	result, err := a.attemptLogin(ctx, transaction, prepared, report)
	if err != nil {
		return prepared.failed(a, err, domain.AuthAuthenticating, "")
	}
	if result.AlreadyOnline {
		// Cleared once and the gateway still says the line is taken. Spec 04
		// stops here: the wireless rebuild is the next remedy and it belongs
		// to the wireless path, not to another round of logging people out.
		return prepared.failed(a,
			domain.Errorf(domain.CodeConflict,
				"清理旧会话后网关仍报该线路已有会话"),
			domain.AuthAccepted, identity)
	}
	if result.State == domain.AuthRejected {
		return prepared.failed(a,
			domain.Errorf(domain.CodeAuthRejected, "网关拒绝了本次登录"),
			domain.AuthRejected, result.Identity)
	}
	return a.verify(ctx, transaction, prepared, report)
}

// logout ends this account's own session on its own line.
func (a *Authenticator) logout(ctx context.Context, action Action,
	report func(Phase)) Outcome {
	// No logout may contact a campus portal over a hotspot that now occupies
	// the wireless interface. Logout never changes association to find a session.
	if a.wireless != nil {
		cfg := a.settings.Snapshot()
		if account, ok := cfg.CampusAccountByID(action.Request.AccountID); ok && !account.IsWired() {
			observed, err := a.wireless.Association(ctx, account.Radio)
			if err != nil {
				return failure(err)
			}
			dest, err := campusDestination(account)
			if err != nil {
				return failure(err)
			}
			if !dest.want.Satisfied(observed) {
				if action.Request.Kind == KindForcedLogout {
					// There is no usable campus wireless path to log out. Do not
					// send credentials through a hotspot or retry this all night;
					// completing this local step permits the scheduled transition.
					return Outcome{State: StateSucceeded, Message: "校园无线未连接，未发送退出请求"}
				}
				return failure(domain.Errorf(domain.CodeBindingUnavailable, "当前无线连接不是该校园账号的线路，未发送退出请求"))
			}
		}
	}

	prepared, outcome := a.prepare(ctx, action, report)
	if prepared == nil {
		return outcome
	}
	transaction := auth.NewTransaction(prepared.line, prepared.gateway)

	// Whose session to end is settled by asking this line, not by trusting the
	// configured name: spec 04 requires a manual logout to act on the identity
	// this line just reported, and a failed query is not proof of being
	// offline.
	report(PhaseVerify)
	identity, err := transaction.Online(ctx, prepared.username)
	if err != nil {
		return prepared.failed(a, err, domain.AuthUnknown, "")
	}
	if !identity.Present {
		return prepared.wentOffline(a, "这条线路上没有在线会话")
	}
	if !identity.MatchesExpected && prepared.intent != auth.IntentManual {
		return prepared.failed(a,
			domain.Errorf(domain.CodeOnlineIdentityMismatch,
				"这条线路上在线的是另一个账号，自动维护不会将其下线"),
			domain.AuthVerifiedOther, identity.Username)
	}

	report(PhaseLogout)
	result, err := transaction.Logout(ctx, identity.SessionUsername, identity.ClientIP)
	if err != nil {
		return prepared.failed(a, err, identity.State(), identity.Username)
	}
	if result.State == domain.AuthRejected {
		// The transport succeeded and the gateway refused. These are different
		// things, and reading only the error made the second look like the
		// first: a reply of {"error":"sign_error"} came back as a nil error and
		// was reported as "已登出".
		return prepared.failed(a,
			domain.Errorf(domain.CodeAuthRejected,
				"网关拒绝了登出请求：%s", gatewayWords(result)),
			identity.State(), identity.Username)
	}

	return a.verifyLogout(ctx, transaction, prepared, identity.Username, report)
}

// prepare resolves everything an attempt needs, or explains why it cannot.
//
// A nil attempt means the Outcome beside it is the answer.
func (a *Authenticator) prepare(ctx context.Context, action Action,
	report func(Phase)) (*attempt, Outcome) {

	cfg := a.settings.Snapshot()
	revision := a.settings.Revision()
	accountID := action.Request.AccountID

	account, known := cfg.CampusAccountByID(accountID)
	if !known {
		return nil, Outcome{State: StateFailed, Code: domain.CodeNotFound,
			Message: "账号 " + accountID + " 不存在"}
	}

	prepared := &attempt{
		account:         account,
		username:        config.EffectiveUsername(account),
		shape:           shapeOf(config.EffectiveLogin(cfg, account)),
		intent:          intentOf(action.Request.Kind),
		checkMode:       cfg.Checks.Mode,
		checks:          cfg.Checks,
		revision:        revision,
		sequence:        action.Sequence,
		confirmTerminal: action.Request.Kind == KindForcedLogout || action.Request.Kind == KindQuietCampus,
	}

	iface, err := lineInterface(cfg, account)
	if err != nil {
		return nil, a.linkFailure(ctx, prepared, "", err)
	}

	report(PhaseWaitingLink)
	binding, err := a.observeLine(ctx, account.ID, iface)
	prepared.binding = binding
	if err != nil {
		return nil, a.linkFailure(ctx, prepared, iface, err)
	}

	gateway, err := auth.ParseGateway(account.BaseURL, account.ACID)
	if err != nil {
		return nil, prepared.failed(a, err, domain.AuthUnknown, "")
	}
	prepared.gateway = gateway

	line, err := a.lines.Line(account.ID, binding, gateway.BaseURL)
	if err != nil {
		return nil, prepared.failed(a, err, domain.AuthUnknown, "")
	}
	prepared.line = line
	return prepared, Outcome{}
}

// observeLine reads where an account's line is, and spends a generation only if
// it moved.
//
// A generation is the identity of a binding, not a counter of actions. The
// earlier version raised it at the start of every action and retired the pool
// against the new number, so two consecutive logins on an unchanged line closed
// a perfectly good connection and built another -- paying for a handshake and a
// resolution to arrive where it already was, every single tick of the
// maintenance loop.
//
// What must not be lost is the guarantee the churn was standing in for: when
// the line really does move, everything bound to the old address is retired
// before any credential goes out. That is why the retirement stays here, on the
// path that assigns the new generation.
func (a *Authenticator) observeLine(ctx context.Context, accountID,
	iface string) (domain.Binding, error) {

	a.mu.Lock()
	previous, known := a.seen[accountID]
	a.mu.Unlock()

	// Observed under the generation this account is already on. Assigning a new
	// one first is what made the observation look like a change to everything
	// downstream.
	stamp := previous.Generation
	if !known {
		stamp = a.generation.Add(1)
	}
	binding, err := a.binder.ResolveBinding(ctx, iface, stamp)
	if err != nil {
		a.invalidateLine(accountID, stamp)
		// The failed observation must supersede this generation's prior success.
		// A zero stamp would be discarded as older than the still-visible state.
		return domain.Binding{Generation: stamp}, err
	}
	binding.Generation = stamp

	moved := !known || !sameLine(previous, binding)
	if moved && known {
		binding.Generation = a.generation.Add(1)
	}

	a.mu.Lock()
	a.seen[accountID] = binding
	a.mu.Unlock()

	if moved {
		// Anything older than this observation is gone: its socket is bound to
		// an address this account no longer has.
		a.lines.Retire(accountID, binding.Generation)
	}
	return binding, nil
}

// An observed outage ends this binding even if DHCP later gives back the same
// address. Otherwise that recovery could reuse a connection from before the
// outage. The generation check keeps a late failed observation from retiring
// a newer binding that has already replaced it.
func (a *Authenticator) invalidateLine(accountID string, generation uint64) {
	a.mu.Lock()
	previous, known := a.seen[accountID]
	if !known || previous.Generation != generation {
		a.mu.Unlock()
		return
	}
	delete(a.seen, accountID)
	retired := a.generation.Add(1)
	a.mu.Unlock()
	a.lines.Retire(accountID, retired)
}

// sameLine reports whether two observations describe the same way out.
//
// The fields a socket is actually bound to, plus the resolvers it would use. A
// line that kept its address but changed its DNS is a different line for the
// purpose of reusing a connection, because the next name it resolves may answer
// differently.
func sameLine(before, after domain.Binding) bool {
	return before.L3Device == after.L3Device &&
		before.IfIndex == after.IfIndex &&
		before.SourceIPv4 == after.SourceIPv4 &&
		slices.Equal(before.DNSServers, after.DNSServers)
}

// confirmUnchanged re-reads the line and refuses if it moved.
//
// The same generation goes in deliberately: what is being compared is what the
// interface actually looks like now against what it looked like when the token
// was issued, not two numbers this program made up.
func (a *Authenticator) confirmUnchanged(ctx context.Context, prepared *attempt) error {
	cfg := a.settings.Snapshot()
	iface, err := lineInterface(cfg, prepared.account)
	if err != nil {
		return err
	}
	now, err := a.binder.ResolveBinding(ctx, iface, prepared.binding.Generation)
	if err != nil {
		a.invalidateLine(prepared.account.ID, prepared.binding.Generation)
		return err
	}
	if !sameLine(now, prepared.binding) {
		a.invalidateLine(prepared.account.ID, prepared.binding.Generation)
		return domain.Errorf(domain.CodeBindingChanged,
			"线路在取得挑战值之后发生变化，本次认证作废")
	}
	return nil
}

// linkFailure asks the adapter what is actually wrong with the line.
//
// "BindingUnavailable" is this program's word for it. "The interface does not
// exist" and "DHCP has not answered yet" are the words that get the problem
// fixed, and only the adapter can tell those apart.
func (a *Authenticator) linkFailure(ctx context.Context, prepared *attempt,
	iface string, cause error) Outcome {

	state := domain.LinkMissing
	if iface != "" {
		if observed, err := a.binder.LinkState(ctx, iface); err == nil {
			state = observed
		}
	}
	outcome := prepared.failed(a, cause, domain.AuthUnknown, "")
	if outcome.Observation != nil {
		outcome.Observation.Link = state
		outcome.Observation.Connectivity = domain.ConnectivityUnknown
	}
	return outcome
}

// succeeded and failed build the Outcome together with what the attempt learned.
//
// The observation travels back with the result rather than being written here:
// spec 02 gives the coordinator the job of confirming a worker's answer is
// still current before anything accepts it, and a worker that wrote global
// state directly would be deciding that for itself.
func (p *attempt) succeeded(a *Authenticator, message, identity string) Outcome {
	return Outcome{
		State:   StateSucceeded,
		Message: message,
		Observation: p.observation(a, domain.AuthVerifiedSelf,
			domain.ConnectivityPortalReachable, identity),
	}
}

// wentOffline is a logout that reached its goal.
//
// It exists because the login helper does not fit: that one writes
// VerifiedSelf, which is the right projection for a login and exactly the wrong
// one for a logout. Sharing it meant a successful logout left the status page
// saying this account was online -- and so did "there was no session here",
// which reported an empty line as a confirmed login.
func (p *attempt) wentOffline(a *Authenticator, message string) Outcome {
	return Outcome{
		State:   StateSucceeded,
		Message: message,
		Observation: p.observation(a, domain.AuthOffline,
			domain.ConnectivityPortalReachable, ""),
	}
}

// unconfirmed is an action whose request was accepted and whose result could
// not be established.
//
// Reported as a failure, because the caller asked for an outcome and did not
// get one. What it must not do is claim either outcome: the session may be
// gone, and saying it is still up would be as wrong as saying it is gone.
func (p *attempt) unconfirmed(a *Authenticator, message, identity string) Outcome {
	return Outcome{
		State:   StateFailed,
		Code:    domain.CodeDeadlineExceeded,
		Message: message,
		Observation: p.observation(a, domain.AuthUnknown,
			domain.ConnectivityPortalReachable, identity),
	}
}

func (p *attempt) failed(a *Authenticator, cause error, state domain.AuthState,
	identity string) Outcome {

	code := domain.CodeInternal
	if observed, ok := domain.CodeOf(cause); ok {
		code = observed
	}
	connectivity := domain.ConnectivityUnknown
	if state != domain.AuthUnknown {
		// The gateway answered something, whatever it was. That is what
		// PortalReachable means; whether the internet is reachable is a
		// different question and nothing here has asked it.
		connectivity = domain.ConnectivityPortalReachable
	}
	return Outcome{
		State:       StateFailed,
		Code:        code,
		Message:     userMessage(cause),
		Observation: p.observation(a, state, connectivity, identity),
	}
}

// userMessage is the part of a failure a person is shown.
//
// Only this program's own text, never the wrapped cause. domain.Error keeps the
// cause out of Error() for exactly this reason: a transport failure can carry a
// URL, and a login URL carries a checksum over the password. This string ends
// up on the status page.
// gatewayWords is what the portal said, for a message that needs to quote it.
//
// The portal's own text, marked as such by the sentence around it. It is never
// this program's explanation: a gateway that answers "sign_error" has not
// explained anything to a user, and presenting it as a diagnosis would be
// putting words in its mouth.
func gatewayWords(result auth.Result) string {
	if result.GatewayMessage != "" {
		return result.GatewayMessage
	}
	if result.GatewayCode != "" {
		return result.GatewayCode
	}
	return "未提供原因"
}

func userMessage(cause error) string {
	if typed, ok := errors.AsType[*domain.Error](cause); ok {
		return typed.Message
	}
	return "认证过程出现未分类的错误"
}

func (p *attempt) observation(a *Authenticator, state domain.AuthState,
	connectivity domain.Connectivity, identity string) *observe.Observation {

	link := domain.LinkReady
	if !p.binding.Ready() {
		link = domain.LinkMissing
	}
	// The line as this attempt actually found it. Without it the status page
	// can name the interface an account is configured for but not the device
	// or address it reached the network through, which is the difference
	// between a setting and an observation.
	line := observe.LineView{
		Iface:  p.binding.LogicalIface,
		Device: p.binding.L3Device,
	}
	if p.binding.SourceIPv4.IsValid() {
		line.Address = p.binding.SourceIPv4.String()
	}
	return &observe.Observation{
		AccountID:    p.account.ID,
		Revision:     p.revision,
		Generation:   p.binding.Generation,
		Sequence:     p.sequence,
		Link:         link,
		Auth:         state,
		Connectivity: connectivity,
		Identity:     identity,
		Line:         line,
		At:           a.clock.Now(),
	}
}

// lineInterface is the interface an account authenticates through.
func lineInterface(cfg domain.Config, account domain.CampusAccount) (string, error) {
	if account.IsWired() {
		if account.WiredIface == "" {
			return "", domain.FieldErrorf(domain.CodeInvalidConfig,
				"wired_iface", "该账号没有选择有线接口")
		}
		return account.WiredIface, nil
	}
	if cfg.STAIface == "" {
		return "", domain.FieldErrorf(domain.CodeInvalidConfig, "sta_iface",
			"无线账号需要先指定客户端接口，否则无法确定认证走哪条线路")
	}
	return cfg.STAIface, nil
}

// intentOf is spec 04's distinction between maintenance and a user asking now.
//
// It decides one thing and it decides it here, once: whether this action is
// allowed to end a session it did not create.
func intentOf(kind Kind) auth.Intent {
	if kind.Manual() {
		return auth.IntentManual
	}
	return auth.IntentAutomatic
}

func shapeOf(login config.EffectiveLoginShape) auth.Shape {
	return auth.Shape{
		N:           login.N,
		Type:        login.Type,
		Enc:         login.Enc,
		InfoPrefix:  login.InfoPrefix,
		OS:          login.OS,
		Name:        login.Name,
		DoubleStack: login.DoubleStack,
		Alphabet:    login.Alphabet,
	}
}
