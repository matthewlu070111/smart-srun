package application

import (
	"context"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

// The maintenance loop is driven a tick at a time.
//
// Every deadline it works with is measured on the injected clock, so a
// six-hour quiet window and a twenty-minute cooldown are reached in
// microseconds. The one test that starts the real goroutine is at the bottom;
// the rest call tick directly, because what is under test is the decision and
// not the scheduling machinery underneath it.

type recorder struct {
	submitted []Request
	refuse    error
	events    []MaintenanceEvent
	nextID    int
}

func (r *recorder) submit(_ context.Context, request Request) (Receipt, error) {
	if r.refuse != nil {
		return Receipt{}, r.refuse
	}
	r.submitted = append(r.submitted, request)
	r.nextID++
	return Receipt{ActionID: formatID(uint64(r.nextID)), State: StateQueued}, nil
}

func (r *recorder) onEvent(event MaintenanceEvent) {
	r.events = append(r.events, event)
}

func (r *recorder) kinds() []Kind {
	out := make([]Kind, 0, len(r.submitted))
	for _, request := range r.submitted {
		out = append(out, request.Kind)
	}
	return out
}

func (r *recorder) accounts() []string {
	out := make([]string, 0, len(r.submitted))
	for _, request := range r.submitted {
		out = append(out, request.AccountID)
	}
	return out
}

func (r *recorder) sawEvent(kind string) bool {
	for _, event := range r.events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

// maintainWorld is one wired campus account, maintained, active, 60s interval.
func maintainWorld() *fakeSettings {
	return &fakeSettings{
		revision: 1,
		cfg: domain.Config{
			Enabled:   true,
			Selection: domain.Selection{ActiveCampusID: "c1"},
			Checks:    domain.ChecksConfig{IntervalSeconds: 60},
			Retry: domain.RetryConfig{
				Enabled: true, MaxRetries: 3,
				InitialSeconds: 10, MaxSeconds: 300,
			},
			CampusAccounts: []domain.CampusAccount{{
				ID: "c1", Label: "校园网", UserID: "a", Password: "p",
				AccessMode: domain.AccessModeWired, WiredIface: "wan",
				BaseURL: "http://192.0.2.1", ACID: "1",
			}},
		},
	}
}

func maintainerFor(t *testing.T, settings *fakeSettings,
	clock *faketime.Clock) (*Maintainer, *recorder) {

	t.Helper()
	sink := &recorder{}
	loop := NewMaintainer(MaintainerOptions{
		Clock:    clock,
		Settings: settings,
		Submit:   sink.submit,
		Line: func(request Request) string {
			cfg := settings.Snapshot()
			account, known := cfg.CampusAccountByID(request.AccountID)
			if !known {
				return ""
			}
			return "iface:" + account.WiredIface
		},
		OnEvent: sink.onEvent,
	})
	return loop, sink
}

var maintainEpoch = time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)

// at parses a quiet-hours endpoint the way the configuration does.
func at(t *testing.T, text string) domain.ClockTime {
	t.Helper()
	value, err := domain.ParseClockTime(text)
	if err != nil {
		t.Fatalf("ParseClockTime(%q): %v", text, err)
	}
	return value
}

// The first check happens at startup, not one interval later.
//
// A router that just booted is the case this program exists for, and making the
// user wait out a full interval for the first login is the one moment it is
// most obviously not working.
func TestTheFirstCheckHappensAtStartupRatherThanAnIntervalLater(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)

	loop.tick(t.Context(), clock.Now())

	if len(sink.submitted) != 1 {
		t.Fatalf("submitted %v at startup, want one maintenance action", sink.kinds())
	}
	if sink.submitted[0].Kind != KindMaintain {
		t.Errorf("kind = %s, want maintain", sink.submitted[0].Kind)
	}
	if sink.submitted[0].AccountID != "c1" {
		t.Errorf("account = %q", sink.submitted[0].AccountID)
	}
}

// enabled=false stops the automatic loop and nothing else.
//
// Spec 02 keeps the two apart in as many words: "do not authenticate on your
// own" is not "refuse what I ask for". The manual half is policy.PauseSet's
// and is tested there; what this checks is that the loop honours it.
func TestAutomaticAuthenticationOffQueuesNothing(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.Enabled = false
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, settings, clock)

	loop.tick(t.Context(), clock.Now())

	if len(sink.submitted) != 0 {
		t.Fatalf("queued %v with automatic authentication off", sink.kinds())
	}
	if !loop.pause.Has(policy.PauseUserDisabled) {
		t.Errorf("pause = %v, want the user-disabled reason", loop.pause.Reasons())
	}
	// And a manual action would still be allowed -- the loop does not pretend
	// otherwise by claiming the service is frozen.
	if !loop.pause.AllowsManual() {
		t.Error("switching automatic authentication off blocked manual actions")
	}
}

// One attempt is in flight at a time for an account.
func TestAnAccountWithAnAttemptInFlightIsNotQueuedAgain(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)

	loop.tick(t.Context(), clock.Now())
	clock.Advance(5 * time.Minute)
	loop.tick(t.Context(), clock.Now())

	if len(sink.submitted) != 1 {
		t.Fatalf("queued %d actions while one was still running", len(sink.submitted))
	}
}

// A success schedules the next check one interval out.
func TestASuccessSchedulesTheNextCheckOneIntervalLater(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)

	loop.tick(t.Context(), clock.Now())
	loop.apply(finished(sink, 0, StateSucceeded), clock.Now())

	state := loop.accounts["c1"]
	if want := maintainEpoch.Add(60 * time.Second); !state.dueAt.Equal(want) {
		t.Errorf("dueAt = %v, want %v", state.dueAt, want)
	}
	if state.failures != 0 {
		t.Errorf("failures = %d after a success", state.failures)
	}

	// Not before then, and then yes.
	clock.Advance(59 * time.Second)
	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 1 {
		t.Fatalf("queued again %v before the interval elapsed", sink.kinds())
	}
	clock.Advance(2 * time.Second)
	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 2 {
		t.Fatalf("did not queue after the interval elapsed: %v", sink.kinds())
	}
}

// A failure backs off, and the backoff grows.
func TestAFailureBacksOffAndTheWaitGrows(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)
	plan := policy.PlanFrom(maintainWorld().cfg.Retry)

	var waits []time.Duration
	for range 3 {
		loop.tick(t.Context(), clock.Now())
		before := clock.Now()
		loop.apply(finished(sink, len(sink.submitted)-1, StateFailed), clock.Now())
		waits = append(waits, loop.accounts["c1"].dueAt.Sub(before))
		clock.Advance(loop.accounts["c1"].dueAt.Sub(clock.Now()))
	}

	if waits[0] != plan.Wait(1) || waits[1] != plan.Wait(2) {
		t.Fatalf("waits = %v, want the plan's %v then %v",
			waits, plan.Wait(1), plan.Wait(2))
	}
	if !(waits[1] > waits[0]) {
		t.Errorf("the backoff did not grow: %v", waits)
	}
	if !sink.sawEvent(EventBackoff) {
		t.Error("no backoff event was reported")
	}
}

// A success clears what the failures before it accumulated.
//
// Without this the backoff only ever grows: an account that fails twice, comes
// back, and fails once more would wait as though it had failed three times in a
// row, and a flaky line would drift towards the cap and stay there.
func TestASuccessClearsTheFailuresBeforeIt(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)
	plan := policy.PlanFrom(maintainWorld().cfg.Retry)

	// Two failures, then a success, then one more failure.
	for range 2 {
		loop.tick(t.Context(), clock.Now())
		loop.apply(finished(sink, len(sink.submitted)-1, StateFailed), clock.Now())
		clock.Advance(loop.accounts["c1"].dueAt.Sub(clock.Now()))
	}
	loop.tick(t.Context(), clock.Now())
	loop.apply(finished(sink, len(sink.submitted)-1, StateSucceeded), clock.Now())
	clock.Advance(loop.accounts["c1"].dueAt.Sub(clock.Now()))

	loop.tick(t.Context(), clock.Now())
	before := clock.Now()
	loop.apply(finished(sink, len(sink.submitted)-1, StateFailed), clock.Now())

	wait := loop.accounts["c1"].dueAt.Sub(before)
	if wait != plan.Wait(1) {
		t.Errorf("wait after a success then one failure = %v, want the first "+
			"step %v -- the success did not clear the earlier failures",
			wait, plan.Wait(1))
	}
}

// A round that spends its retries cools down and starts again.
//
// Spec 04 requires it: a managed WAN that stopped forever after its retry limit
// is the failure the baseline's own comment warns about.
func TestAnExhaustedRoundCoolsDownAndBeginsAgain(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)
	plan := policy.PlanFrom(maintainWorld().cfg.Retry)

	// MaxRetries 3 means four attempts to a round.
	for range plan.RoundLimit() {
		loop.tick(t.Context(), clock.Now())
		loop.apply(finished(sink, len(sink.submitted)-1, StateFailed), clock.Now())
		clock.Advance(loop.accounts["c1"].dueAt.Sub(clock.Now()))
	}

	if !sink.sawEvent(EventRoundCooldown) {
		t.Fatal("the round ended without saying so")
	}
	if loop.accounts["c1"].failures != 0 {
		t.Errorf("failures = %d, want a fresh round",
			loop.accounts["c1"].failures)
	}
	// And it does come back: the account is not stopped, it is waiting.
	attemptsBefore := len(sink.submitted)
	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != attemptsBefore+1 {
		t.Error("the account never got another round")
	}
}

// Quiet hours pause the loop, and leaving them resumes it.
func TestQuietHoursPauseTheLoopAndLeavingThemResumesIt(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.Quiet = domain.QuietConfig{
		Enabled: true,
		Start:   at(t, "01:00"),
		End:     at(t, "05:00"),
	}
	// 02:00 Beijing is inside the window.
	inside := time.Date(2026, 3, 5, 2, 0, 0, 0, time.FixedZone("CST", 8*3600))
	clock := faketime.New(inside)
	loop, sink := maintainerFor(t, settings, clock)

	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 0 {
		t.Fatalf("queued %v during quiet hours", sink.kinds())
	}
	if !loop.pause.Has(policy.PauseQuietHours) {
		t.Fatalf("pause = %v, want the quiet-hours reason", loop.pause.Reasons())
	}

	// 06:00 is outside it.
	clock.Advance(4 * time.Hour)
	loop.tick(t.Context(), clock.Now())
	if loop.pause.Has(policy.PauseQuietHours) {
		t.Fatalf("still paused after leaving the window: %v", loop.pause.Reasons())
	}
	if len(sink.submitted) != 1 {
		t.Errorf("did not resume after the window: %v", sink.kinds())
	}
}

// T22 -- leaving quiet hours clears the quiet reason and nothing else.
//
// This is the baseline bug the pause set replaced: it had one flag, so the end
// of the window resumed accounts the user had switched off by hand.
func TestLeavingQuietHoursDoesNotResumeWhatTheUserSwitchedOff(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.Enabled = false
	settings.cfg.Quiet = domain.QuietConfig{
		Enabled: true,
		Start:   at(t, "01:00"),
		End:     at(t, "05:00"),
	}
	inside := time.Date(2026, 3, 5, 2, 0, 0, 0, time.FixedZone("CST", 8*3600))
	clock := faketime.New(inside)
	loop, sink := maintainerFor(t, settings, clock)

	loop.tick(t.Context(), clock.Now())
	clock.Advance(4 * time.Hour)
	loop.tick(t.Context(), clock.Now())

	if loop.pause.Has(policy.PauseQuietHours) {
		t.Error("the quiet reason survived the end of the window")
	}
	if !loop.pause.Has(policy.PauseUserDisabled) {
		t.Fatal("leaving quiet hours resumed an account the user had switched off")
	}
	if len(sink.submitted) != 0 {
		t.Errorf("queued %v for an account the user had switched off", sink.kinds())
	}
}

// The quiet-hours sweep logs the managed set out once per visit to the window.
func TestTheQuietSweepLogsEachAccountOutOncePerOccurrence(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.Quiet = domain.QuietConfig{
		Enabled: true, ForceLogout: true,
		Start: at(t, "01:00"),
		End:   at(t, "05:00"),
	}
	inside := time.Date(2026, 3, 5, 2, 0, 0, 0, time.FixedZone("CST", 8*3600))
	clock := faketime.New(inside)
	loop, sink := maintainerFor(t, settings, clock)

	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 1 || sink.submitted[0].Kind != KindForcedLogout {
		t.Fatalf("queued %v, want one forced logout", sink.kinds())
	}
	loop.apply(finished(sink, 0, StateSucceeded), clock.Now())

	// Ticking again inside the same window does not sweep it again. The
	// baseline logged the account out on every tick for six hours.
	clock.Advance(30 * time.Minute)
	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 1 {
		t.Fatalf("swept again inside the same window: %v", sink.kinds())
	}
}

// Only the accounts whose logout failed come back.
func TestTheQuietSweepRetriesOnlyWhatFailed(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.MultiWANEnabled = true
	settings.cfg.CampusAccounts = append(settings.cfg.CampusAccounts,
		domain.CampusAccount{
			ID: "c2", Label: "第二条", UserID: "b", Password: "p",
			AccessMode: domain.AccessModeWired, WiredIface: "wan2",
			AuthEnabled: true, BaseURL: "http://192.0.2.1", ACID: "1",
		})
	settings.cfg.CampusAccounts[0].AuthEnabled = true
	settings.cfg.Quiet = domain.QuietConfig{
		Enabled: true, ForceLogout: true,
		Start: at(t, "01:00"),
		End:   at(t, "05:00"),
	}
	inside := time.Date(2026, 3, 5, 2, 0, 0, 0, time.FixedZone("CST", 8*3600))
	clock := faketime.New(inside)
	loop, sink := maintainerFor(t, settings, clock)

	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 2 {
		t.Fatalf("swept %v, want both accounts", sink.accounts())
	}
	// The first succeeds, the second fails.
	loop.apply(finished(sink, 0, StateSucceeded), clock.Now())
	loop.apply(finished(sink, 1, StateFailed), clock.Now())

	clock.Advance(10 * time.Minute)
	loop.tick(t.Context(), clock.Now())

	retried := sink.accounts()[2:]
	if len(retried) != 1 || retried[0] != sink.accounts()[1] {
		t.Fatalf("retried %v, want only the account whose logout failed", retried)
	}
}

// Two accounts that resolve to one line are reported and neither is queued.
//
// Spec 02 refuses rather than guessing which one wins: whichever authenticates
// second knocks the first off, and the pair then take turns forever.
func TestTwoAccountsOnOneLineAreReportedAndNeitherIsQueued(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.MultiWANEnabled = true
	settings.cfg.CampusAccounts[0].AuthEnabled = true
	settings.cfg.CampusAccounts = append(settings.cfg.CampusAccounts,
		domain.CampusAccount{
			ID: "c2", Label: "撞车的", UserID: "b", Password: "p",
			AccessMode: domain.AccessModeWired, WiredIface: "wan",
			AuthEnabled: true, BaseURL: "http://192.0.2.1", ACID: "1",
		})
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, settings, clock)

	loop.tick(t.Context(), clock.Now())

	if len(sink.submitted) != 0 {
		t.Fatalf("queued %v for accounts that share a line", sink.accounts())
	}
	if !sink.sawEvent(EventLineConflict) {
		t.Fatal("the conflict was not reported")
	}

	// Reported once, not on every tick: it lasts until somebody edits the
	// configuration, and repeating it would bury everything else in the log.
	before := len(sink.events)
	clock.Advance(10 * time.Minute)
	loop.tick(t.Context(), clock.Now())
	for _, event := range sink.events[before:] {
		if event.Kind == EventLineConflict {
			t.Error("the same conflict was reported twice")
		}
	}
}

// One account's failure does not hold up another's.
func TestOneAccountsFailureDoesNotBlockAnother(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.MultiWANEnabled = true
	settings.cfg.CampusAccounts[0].AuthEnabled = true
	settings.cfg.CampusAccounts = append(settings.cfg.CampusAccounts,
		domain.CampusAccount{
			ID: "c2", Label: "另一条", UserID: "b", Password: "p",
			AccessMode: domain.AccessModeWired, WiredIface: "wan2",
			AuthEnabled: true, BaseURL: "http://192.0.2.1", ACID: "1",
		})
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, settings, clock)

	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 2 {
		t.Fatalf("queued %v, want both accounts", sink.accounts())
	}

	failing, healthy := sink.accounts()[0], sink.accounts()[1]
	// The first fails and backs off ten seconds; the second succeeds and is due
	// again in sixty. The two schedules are independent, and the gap between
	// them is what makes that observable.
	loop.apply(finished(sink, 0, StateFailed), clock.Now())
	loop.apply(finished(sink, 1, StateSucceeded), clock.Now())

	// Past the failing account's backoff, well short of the healthy one's
	// interval: only the failing account is due.
	clock.Advance(11 * time.Second)
	loop.tick(t.Context(), clock.Now())
	afterBackoff := sink.accounts()[2:]
	if len(afterBackoff) != 1 || afterBackoff[0] != failing {
		t.Fatalf("queued %v after the backoff, want only %s",
			afterBackoff, failing)
	}

	// And the healthy account keeps its own schedule rather than being dragged
	// onto the failing one's.
	clock.Advance(50 * time.Second)
	loop.apply(finished(sink, 2, StateSucceeded), clock.Now())
	loop.tick(t.Context(), clock.Now())
	afterInterval := sink.accounts()[3:]
	if len(afterInterval) != 1 || afterInterval[0] != healthy {
		t.Fatalf("queued %v at the healthy account's interval, want only %s",
			afterInterval, healthy)
	}
}

// A cancelled action is not replayed.
//
// Spec 02 forbids replaying what a user force-stopped, so it returns to the
// ordinary schedule rather than being retried at once as a failure would be.
func TestACancelledAttemptIsNotRetriedAsAFailure(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)

	loop.tick(t.Context(), clock.Now())
	loop.apply(finished(sink, 0, StateCancelled), clock.Now())

	state := loop.accounts["c1"]
	if state.failures != 0 {
		t.Errorf("a cancellation counted as %d failures", state.failures)
	}
	if want := maintainEpoch.Add(60 * time.Second); !state.dueAt.Equal(want) {
		t.Errorf("dueAt = %v, want the ordinary interval %v", state.dueAt, want)
	}
}

// A result for an action this loop did not start leaves its state alone.
func TestAManualActionsResultDoesNotMoveTheLoopsBackoff(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, _ := maintainerFor(t, maintainWorld(), clock)

	loop.tick(t.Context(), clock.Now())
	inFlight := loop.accounts["c1"].inFlight

	loop.apply(Action{
		ID:      "somebody-elses-action",
		Request: Request{Kind: KindLogin, AccountID: "c1"},
		State:   StateFailed,
	}, clock.Now())

	state := loop.accounts["c1"]
	if state.failures != 0 {
		t.Errorf("a manual action's failure moved the loop's backoff to %d",
			state.failures)
	}
	if state.inFlight != inFlight {
		t.Errorf("a manual action's result cleared the loop's own in-flight id")
	}
}

// A coordinator that refuses the submission does not cost the account its
// backoff: a full queue is not an authentication failure.
func TestARefusedSubmissionIsNotAnAuthenticationFailure(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)
	sink.refuse = domain.Errorf(domain.CodeBusy, "动作队列已满")

	loop.tick(t.Context(), clock.Now())

	state := loop.accounts["c1"]
	if state.failures != 0 {
		t.Errorf("a refused submission counted as %d failures", state.failures)
	}
	if state.inFlight != "" {
		t.Errorf("a refused submission recorded %q as in flight", state.inFlight)
	}

	// And it tries again rather than giving up.
	sink.refuse = nil
	loop.tick(t.Context(), clock.Now())
	if len(sink.submitted) != 1 {
		t.Error("the loop did not retry after the queue cleared")
	}
}

// The loop wakes at the earliest thing it is waiting for.
func TestTheLoopWakesForWhicheverDeadlineComesFirst(t *testing.T) {
	settings := maintainWorld()
	settings.cfg.Quiet = domain.QuietConfig{
		Enabled: true,
		Start:   at(t, "01:00"),
		End:     at(t, "05:00"),
	}
	// 00:59:30 Beijing: the window opens in thirty seconds, sooner than the
	// sixty-second check interval.
	justBefore := time.Date(2026, 3, 5, 0, 59, 30, 0, time.FixedZone("CST", 8*3600))
	clock := faketime.New(justBefore)
	loop, _ := maintainerFor(t, settings, clock)

	wake := loop.tick(t.Context(), clock.Now())
	if got := wake.Sub(clock.Now()); got != 30*time.Second {
		t.Errorf("wake in %v, want the quiet boundary in 30s", got)
	}
}

// And it wakes for a backoff that expires before the next ordinary check.
//
// Sleeping the full interval instead would turn a ten-second retry into a
// sixty-second one, so the configured backoff would be whatever the check
// interval happened to be.
func TestTheLoopWakesWhenABackoffExpiresBeforeTheNextCheck(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	loop, sink := maintainerFor(t, maintainWorld(), clock)
	plan := policy.PlanFrom(maintainWorld().cfg.Retry)

	loop.tick(t.Context(), clock.Now())
	loop.apply(finished(sink, 0, StateFailed), clock.Now())

	backoff := plan.Wait(1)
	if backoff >= checkInterval(&maintainWorld().cfg) {
		t.Fatalf("the fixture's backoff %v is not shorter than its interval; "+
			"this test cannot tell the two apart", backoff)
	}

	wake := loop.tick(t.Context(), clock.Now())
	if got := wake.Sub(clock.Now()); got != backoff {
		t.Errorf("wake in %v, want the backoff %v", got, backoff)
	}
}

// And the whole thing runs as a goroutine, submitting without anybody asking.
//
// The one test that starts the real loop. It is here because the timer arming
// is its own hazard -- policy.Clock takes an absolute instant precisely because
// computing a duration and then being descheduled arms it from a moment that
// has already passed.
func TestTheRunningLoopAuthenticatesWithoutBeingAsked(t *testing.T) {
	clock := faketime.New(maintainEpoch)
	settings := maintainWorld()
	queued := make(chan Request, 8)

	loop := NewMaintainer(MaintainerOptions{
		Clock:    clock,
		Settings: settings,
		Submit: func(_ context.Context, request Request) (Receipt, error) {
			queued <- request
			return Receipt{ActionID: "a1", State: StateQueued}, nil
		},
	})

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the loop did not stop")
		}
	})

	select {
	case request := <-queued:
		if request.Kind != KindMaintain || request.AccountID != "c1" {
			t.Errorf("first submission = %+v", request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never authenticated on its own")
	}

	// A second Run is refused: two loops would be two writers.
	if err := loop.Run(context.Background()); err == nil {
		t.Error("a second Run was allowed")
	}
}

// finished builds the terminal Action for the nth submission the recorder saw.
func finished(sink *recorder, index int, state State) Action {
	return Action{
		ID:      formatID(uint64(index + 1)),
		Request: sink.submitted[index],
		State:   state,
	}
}
