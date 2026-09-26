package application

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
)

// Maintainer is the loop that decides when to authenticate.
//
// It is the layer M07 described and nobody built. The policy package had every
// answer already -- when quiet hours start, how long to wait after a failure,
// which accounts to sweep, which pairs collide on one line -- and not one of
// those functions had a caller outside its own tests. A KindMaintain branch in
// the worker is not automatic authentication; this is what makes the daemon
// keep an account online without anybody pressing a button.
//
// Like the coordinator, it is a single writer: everything below the line is
// touched only by the goroutine running Run. Results arrive as messages on a
// channel rather than as calls into this state, because the coordinator
// publishes them from its own goroutine and two writers is the bug this shape
// exists to prevent.
type Maintainer struct {
	clock          policy.Clock
	settings       Settings
	submit         func(context.Context, Request) (Receipt, error)
	line           func(Request) string
	onEvent        func(MaintenanceEvent)
	manuallyPaused func(domain.Config, string) bool

	resultsMu   sync.Mutex
	results     map[string]maintenanceResult
	resultOrder uint64
	resultReady chan struct{}
	changes     chan struct{}
	started     atomic.Bool
	keys        atomic.Uint64

	// Loop-owned below here.
	pause    policy.PauseSet
	sweep    policy.Sweep
	accounts map[string]*accountState
	// reported is the last conflict set announced, so an unchanged conflict is
	// not repeated on every tick.
	reported string
	// occurrence is the quiet-hours visit the sweep is currently recording
	// against. policy.Sweep keys on it and does not hand it back.
	occurrence  string
	revision    uint64
	quietSwitch quietSwitchState
}

// accountState is what the loop remembers about one account between ticks.
type accountState struct {
	// failures counts consecutive failures within the current round. It drives
	// the backoff and is reset by a success or by the round ending.
	failures policy.Failures
	// dueAt is the earliest instant this account may be attempted again.
	dueAt time.Time
	// inFlight is the action currently running for it, so the loop does not
	// queue a second one behind the first.
	inFlight string
	// sweepAction is the forced-logout action in flight for this account.
	sweepAction string
	sweepDueAt  time.Time
}

// MaintenanceEvent is something the loop wants a log to record.
//
// A seam rather than a logger: spec 05 keeps the structured event log for M11,
// and a loop that formatted its own messages would have to be unpicked then.
type MaintenanceEvent struct {
	Kind string
	// AccountID is empty for events about the loop rather than one account.
	AccountID string
	Message   string
	// Conflicts is set on the "two accounts on one line" event.
	Conflicts []policy.LineConflict
	// Pause is the reason set as it now stands.
	Pause policy.PauseSet
	At    time.Time
}

// Event kinds. A closed set so a log can branch on them.
const (
	EventPauseChanged   = "pause_changed"
	EventLineConflict   = "line_conflict"
	EventMaintainQueued = "maintain_queued"
	EventSweepQueued    = "sweep_queued"
	EventBackoff        = "backoff"
	EventRoundCooldown  = "round_cooldown"
)

// MaintainerOptions wires the loop.
type MaintainerOptions struct {
	Clock    policy.Clock
	Settings Settings
	// Submit is the coordinator's Submit. Taken as a function so the loop can
	// be driven without one: what is under test here is when it submits, not
	// what the coordinator then does.
	Submit func(context.Context, Request) (Receipt, error)
	// Line resolves what a request would occupy, for conflict detection. It is
	// the same resolver the coordinator schedules on, so a conflict the loop
	// reports is a conflict the coordinator would really serialise.
	Line func(Request) string
	// OnEvent receives what happened. Runs on the loop's goroutine, so it must
	// return promptly and must not call back in.
	OnEvent func(MaintenanceEvent)
	// ResumeQuiet is a validated runtime record from the daemon's single
	// writer. It is never inferred from merely observing a hotspot connection.
	ResumeQuiet *QuietResume
	// ManuallyPaused reads explicit per-account logout intent. It also guards
	// queued dispatch in the daemon, so an old maintenance request cannot race it.
	ManuallyPaused func(domain.Config, string) bool
}

// NewMaintainer builds one.
func NewMaintainer(options MaintainerOptions) *Maintainer {
	if options.Submit == nil {
		panic("application: Maintainer needs a Submit")
	}
	if options.Settings == nil {
		panic("application: Maintainer needs Settings")
	}
	clock := options.Clock
	if clock == nil {
		clock = policy.SystemClock{}
	}
	line := options.Line
	if line == nil {
		line = func(Request) string { return "" }
	}
	onEvent := options.OnEvent
	if onEvent == nil {
		onEvent = func(MaintenanceEvent) {}
	}
	paused := options.ManuallyPaused
	if paused == nil {
		paused = func(domain.Config, string) bool { return false }
	}
	m := &Maintainer{
		clock:          clock,
		settings:       options.Settings,
		submit:         options.Submit,
		line:           line,
		onEvent:        onEvent,
		manuallyPaused: paused,
		results:        map[string]maintenanceResult{},
		resultReady:    make(chan struct{}, 1),
		changes:        make(chan struct{}, 1),
		accounts:       map[string]*accountState{},
	}
	cfg := options.Settings.Snapshot()
	m.revision = cfg.Revision
	if resume := options.ResumeQuiet; resume != nil && resume.Matches(cfg, clock.Now()) {
		m.quietSwitch = quietSwitchState{occurrence: resume.Occurrence, hotspotID: resume.HotspotID, done: true, owned: true}
		m.occurrence = resume.Occurrence
		if cfg.Quiet.ForceLogout && !resume.SweepPending {
			// A recorded transition was allowed only after the entire logout
			// sweep completed. Do not contact those campus lines via the hotspot.
			for _, target := range policy.ForcedLogoutTargets(&cfg) {
				m.sweep.Succeeded(resume.Occurrence, target.AccountID)
			}
		}
	}
	return m
}

// ConfigurationChanged wakes the loop after a committed settings change.
func (m *Maintainer) ConfigurationChanged() {
	select {
	case m.changes <- struct{}{}:
	default:
	}
}

// Run is the loop. It returns when ctx is done.
func (m *Maintainer) Run(ctx context.Context) error {
	if !m.started.CompareAndSwap(false, true) {
		return domain.Errorf(domain.CodeConflict, "维护循环已经在运行")
	}

	for {
		// The first pass happens before any waiting. Spec 02 asks for a check at
		// startup rather than one interval later: a router that just booted is
		// the case this program exists for, and making the user wait out a
		// full interval for the first login is the one moment it is most
		// obviously not working.
		wake := m.tick(ctx, m.clock.Now())

		timer := m.clock.NewTimerAt(wake)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-m.resultReady:
			timer.Stop()
			for _, action := range m.takeResults() {
				m.apply(action, m.clock.Now())
			}
		case <-m.changes:
			timer.Stop()
		case <-timer.C():
		}
	}
}

// tick does one pass and returns the instant the loop must wake at.
func (m *Maintainer) tick(ctx context.Context, now time.Time) time.Time {
	cfg := m.settings.Snapshot()
	if cfg.Revision != m.revision {
		m.accounts = map[string]*accountState{}
		m.sweep = policy.Sweep{}
		m.occurrence = ""
		m.reported = ""
		m.revision = cfg.Revision
		m.quietSwitch = quietSwitchState{}
	}

	quiet := policy.EvaluateQuiet(cfg.Quiet, now)
	previous := m.pause
	// Recomputed from the world rather than accumulated. policy.PauseSet also
	// carries sticky reasons -- an updater freezing the loop, a service
	// stopping -- and this loop has none of them: both reasons it uses are
	// derived from the configuration and the clock. Carrying the previous set
	// forward would look like it preserved something and preserve nothing, and
	// no mutation could tell the difference. Whichever card first needs a
	// sticky reason adds the state to hold it at the same time.
	m.pause = policy.Derive(0, cfg.Enabled, quiet)
	if m.pause != previous {
		m.emit(MaintenanceEvent{Kind: EventPauseChanged, Pause: m.pause,
			Message: pauseMessage(m.pause), At: now})
	}

	// The sweep runs inside the window and before the pause check, because the
	// quiet hours that stop maintenance are the same quiet hours that require
	// the logout. Checking AllowsMaintenance first would mean the window could
	// never sweep.
	if cfg.Enabled && quiet.Active && quiet.ForceLogout {
		m.sweepQuietHours(ctx, &cfg, quiet, now)
	}
	m.scheduleQuietSwitch(ctx, &cfg, quiet, now)

	if m.pause.AllowsMaintenance() {
		m.maintain(ctx, &cfg, now)
	}

	return m.nextWake(&cfg, quiet, now)
}

// maintain queues an attempt for every account that is due one.
func (m *Maintainer) maintain(ctx context.Context, cfg *domain.Config, now time.Time) {
	// The union of the managed wired accounts and the active campus account.
	// It is the same set the quiet-hours sweep works on, and deliberately so:
	// the accounts this program keeps online are exactly the accounts it must
	// take offline at the boundary. policy.ForcedLogoutTargets is where that
	// union and its deduplication live.
	targets := policy.ForcedLogoutTargets(cfg)
	if len(targets) == 0 {
		return
	}

	blocked := m.reportConflicts(targets, now)
	for _, target := range targets {
		if m.manuallyPaused(*cfg, target.AccountID) {
			continue
		}
		if target.AccountID == cfg.Selection.ActiveCampusID && m.quietSwitch.owned {
			// The return action verifies the campus path before retiring the
			// scheduled hotspot. Other managed wired lines remain independent.
			continue
		}
		if blocked[target.AccountID] {
			// Two accounts on one line cannot both be online; whichever
			// authenticates second knocks the first off and they take turns
			// forever. Spec 02 refuses rather than guessing which one wins.
			continue
		}
		state := m.stateFor(target.AccountID)
		if state.inFlight != "" || now.Before(state.dueAt) {
			continue
		}
		m.queue(ctx, state, Request{
			Kind:           KindMaintain,
			CheckRevision:  true,
			ConfigRevision: cfg.Revision,
			AccountID:      target.AccountID,
			IdempotencyKey: m.key("maintain", target.AccountID),
		}, EventMaintainQueued, now)
	}
}

// sweepQuietHours logs out the accounts this occurrence has not finished with.
func (m *Maintainer) sweepQuietHours(ctx context.Context, cfg *domain.Config,
	quiet policy.QuietState, now time.Time) {

	m.occurrence = quiet.Occurrence
	pending := m.sweep.Pending(quiet.Occurrence, policy.ForcedLogoutTargets(cfg))
	for _, target := range pending {
		if m.manuallyPaused(*cfg, target.AccountID) {
			continue
		}
		state := m.stateFor(target.AccountID)
		if state.sweepAction != "" || state.inFlight != "" || now.Before(state.sweepDueAt) {
			continue
		}
		receipt, err := m.submit(ctx, Request{
			Kind:           KindForcedLogout,
			CheckRevision:  true,
			ConfigRevision: cfg.Revision,
			AccountID:      target.AccountID,
			// Sweep records successful targets once per occurrence. A failed
			// action needs a fresh key: reusing its key just returns the cached
			// failure from the coordinator instead of running a retry.
			IdempotencyKey: m.key("sweep:"+quiet.Occurrence+":"+strconv.FormatUint(cfg.Revision, 10), target.AccountID),
		})
		if err != nil {
			continue
		}
		state.sweepAction = receipt.ActionID
		m.emit(MaintenanceEvent{Kind: EventSweepQueued,
			AccountID: target.AccountID, Message: "静默时段：排队强制下线",
			At: now})
	}
}

// queue submits one request and records what is now in flight for the account.
func (m *Maintainer) queue(ctx context.Context, state *accountState,
	request Request, kind string, now time.Time) {

	receipt, err := m.submit(ctx, request)
	if err != nil {
		// A refusal is the coordinator's to explain -- a full queue, a stopped
		// service. The loop waits and tries again rather than treating it as an
		// authentication failure, which would spend the account's backoff on
		// something the account had nothing to do with.
		return
	}
	state.inFlight = receipt.ActionID
	m.emit(MaintenanceEvent{Kind: kind, AccountID: request.AccountID,
		Message: "已排队", At: now})
}

// apply folds one finished action back into the account's state.
func (m *Maintainer) apply(action Action, now time.Time) {
	cfg := m.settings.Snapshot()
	if action.Request.CheckRevision && action.Request.ConfigRevision != cfg.Revision {
		return
	}
	if m.applyQuietSwitch(action, cfg, now) {
		return
	}
	accountID := action.Request.AccountID
	if accountID == "" {
		return
	}
	state := m.stateFor(accountID)
	if action.ID == state.sweepAction {
		state.sweepAction = ""
		state.sweepDueAt = now.Add(checkInterval(&cfg))
		if action.State == StateSucceeded && !action.MaintenanceDeferred {
			// Only a success is recorded. A failure comes back on the next tick,
			// which is what "仅重试失败的账号" means: the ones that worked are
			// not swept again this occurrence.
			m.sweep.Succeeded(m.occurrence, accountID)
		}
		return
	}
	if action.ID != state.inFlight {
		// A manual action, or one from before a restart. The loop does not own
		// it and must not let it move the account's backoff.
		return
	}
	state.inFlight = ""
	if action.MaintenanceDeferred {
		state.dueAt = now.Add(checkInterval(&cfg))
		return
	}

	switch action.State {
	case StateSucceeded:
		state.failures = 0
		state.dueAt = now.Add(checkInterval(&cfg))
		return
	case StateCancelled, StateInterrupted:
		// Spec 02 forbids replaying what a user stopped. The account simply
		// becomes due again on the ordinary schedule.
		state.dueAt = now.Add(checkInterval(&cfg))
		return
	}

	plan := policy.PlanFrom(cfg.Retry)
	state.failures = state.failures.Next()
	if plan.RoundExhausted(state.failures) {
		// The round is over, not the account. Spec 04 requires a bounded
		// cooldown and then a new round: a managed WAN that stopped forever
		// after its retry limit is the failure the baseline's own comment warns
		// about.
		state.failures = 0
		state.dueAt = now.Add(plan.RoundCooldown())
		m.emit(MaintenanceEvent{Kind: EventRoundCooldown, AccountID: accountID,
			Message: "本轮重试已用尽，进入冷却后重新开始", At: now})
		return
	}
	wait := plan.Wait(state.failures)
	state.dueAt = now.Add(wait)
	m.emit(MaintenanceEvent{Kind: EventBackoff, AccountID: accountID,
		Message: "认证失败，退避后重试", At: now})
}

// reportConflicts announces accounts that would collide, and returns them.
func (m *Maintainer) reportConflicts(targets []policy.Target,
	now time.Time) map[string]bool {

	conflicts := policy.Conflicts(targets, func(target policy.Target) string {
		return m.line(Request{Kind: KindMaintain, AccountID: target.AccountID})
	})

	blocked := map[string]bool{}
	var signature string
	for _, conflict := range conflicts {
		signature += conflict.Line + ":"
		for _, accountID := range conflict.AccountIDs {
			blocked[accountID] = true
			signature += accountID + ","
		}
		signature += ";"
	}

	// Announced when it changes, not on every tick. A conflict lasts until
	// somebody edits the configuration, and repeating it every interval would
	// bury everything else in the log.
	if signature != m.reported {
		m.reported = signature
		if len(conflicts) > 0 {
			m.emit(MaintenanceEvent{Kind: EventLineConflict,
				Conflicts: conflicts, At: now,
				Message: "多个账号解析到同一条线路，已暂停这些账号的自动认证"})
		}
	}
	return blocked
}

// nextWake is the instant the loop must be awake at even if nothing happens.
func (m *Maintainer) nextWake(cfg *domain.Config, quiet policy.QuietState,
	now time.Time) time.Time {

	// An instant rather than a duration, for the reason policy.Clock exists:
	// reading the time, computing a wait and then being descheduled arms the
	// timer from a moment that has already passed, and the loop sleeps through
	// what it was waiting for.
	soonest := now.Add(checkInterval(cfg))

	if quiet.HasNext && quiet.Next.Before(soonest) {
		soonest = quiet.Next
	}
	// Account retry deadlines cannot be consumed while maintenance is paused.
	// An overdue deadline otherwise arms an immediate timer over and over for
	// the entire quiet window (or while disabled), spinning the daemon CPU.
	if !m.pause.AllowsMaintenance() {
		return soonest
	}
	for id, state := range m.accounts {
		if m.manuallyPaused(*cfg, id) {
			continue
		}
		if id == cfg.Selection.ActiveCampusID && m.quietSwitch.owned {
			continue
		}
		if state.inFlight != "" || state.dueAt.IsZero() {
			continue
		}
		if state.dueAt.Before(soonest) {
			soonest = state.dueAt
		}
	}
	if soonest.Before(now) {
		return now
	}
	return soonest
}

func (m *Maintainer) stateFor(accountID string) *accountState {
	state, known := m.accounts[accountID]
	if !known {
		// A new account is due immediately. That is the startup case and the
		// "user just added an account" case, and both want an attempt now.
		state = &accountState{}
		m.accounts[accountID] = state
	}
	return state
}

func (m *Maintainer) key(prefix, accountID string) string {
	return prefix + ":" + accountID + ":" + formatID(m.keys.Add(1))
}

func (m *Maintainer) emit(event MaintenanceEvent) { m.onEvent(event) }

// checkInterval is how long between ordinary checks.
func checkInterval(cfg *domain.Config) time.Duration {
	seconds := cfg.Checks.IntervalSeconds
	if seconds <= 0 {
		// Validation refuses this, so reaching it means the configuration was
		// built in code. A minute is short enough to be visibly wrong and long
		// enough not to hammer a gateway while somebody works out why.
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// pauseReasonText is why automatic authentication is suspended, in the words a
// user would use. The reason codes themselves stay as they are: they are what
// the program branches on, and a log line still carries one in its own field.
var pauseReasonText = map[policy.PauseReason]string{
	policy.PauseUserDisabled:     "自动认证开关已关闭",
	policy.PauseManual:           "手动暂停",
	policy.PauseQuietHours:       "处于夜间停用时段",
	policy.PauseServiceStopping:  "服务正在停止",
	policy.PauseUpdateInstalling: "正在安装更新",
}

func pauseMessage(set policy.PauseSet) string {
	if set.Empty() {
		return "自动认证已恢复"
	}
	reasons := set.Reasons()
	names := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		text, known := pauseReasonText[reason]
		if !known {
			// A reason added without wording still has to be reportable; the
			// code is worse to read than a sentence, and better than silence.
			text = string(reason)
		}
		names = append(names, text)
	}
	sort.Strings(names)
	return "自动认证已暂停：" + joinReasons(names)
}

func joinReasons(names []string) string {
	out := ""
	for index, name := range names {
		if index > 0 {
			out += "、"
		}
		out += name
	}
	return out
}
