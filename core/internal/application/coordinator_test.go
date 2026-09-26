package application

import (
	"slices"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
)

// T23 -- the same key submitted twice is one action.
//
// The key is the LuCI click id. A double-click, a page reload mid-request, or a
// retry after a slow response must not start a second login: the gateway would
// see two, and the second would knock the first off.
func TestARepeatedKeyReturnsTheSameAction(t *testing.T) {
	h := newHarness(t, nil)

	first := h.submit(KindLogin, "campus", "click-1")
	if first.Duplicate {
		t.Error("the first submission was reported as a duplicate")
	}

	second := h.submit(KindLogin, "campus", "click-1")
	if second.ActionID != first.ActionID {
		t.Errorf("second submission = %s, want the same action %s",
			second.ActionID, first.ActionID)
	}
	if !second.Duplicate {
		t.Error("the repeat was not reported as a duplicate")
	}

	started := h.awaitStart()
	if started.ID != first.ActionID {
		t.Errorf("the worker ran %s, want %s", started.ID, first.ActionID)
	}
	h.expectNoStart("one key, one action")
}

func TestReceiptsCannotReferToActionsInAnotherCoordinatorLifetime(t *testing.T) {
	old := newHarness(t, nil)
	first := old.submit(KindLogin, "campus", "click-1")
	old.awaitStart()
	old.shutdown()

	restarted := newHarness(t, nil)
	second := restarted.submit(KindLogin, "campus", "click-1")
	if second.ActionID == first.ActionID {
		t.Fatal("restart reused an action receipt")
	}
	ctx, cancel := restarted.callContext()
	defer cancel()
	_, err := restarted.Action(ctx, first.ActionID)
	if codeOf(t, err) != domain.CodeNotFound {
		t.Fatalf("old receipt returned another action: %v", err)
	}
	if repeated := restarted.submit(KindLogin, "campus", "click-1"); repeated.ActionID != second.ActionID || !repeated.Duplicate {
		t.Fatalf("same-lifetime idempotency changed: %+v", repeated)
	}
}

// T23 -- the same key for different work is a conflict.
//
// Handing back the first action's id would report the wrong thing as finished:
// the caller polls it, sees success, and believes a logout succeeded when what
// actually ran was a login.
func TestTheSameKeyForDifferentWorkIsAConflict(t *testing.T) {
	h := newHarness(t, nil)
	h.submit(KindLogin, "campus", "click-1")

	cases := []Request{
		{Kind: KindLogout, AccountID: "campus", IdempotencyKey: "click-1"},
		{Kind: KindLogin, AccountID: "other", IdempotencyKey: "click-1"},
		{Kind: KindLogin, AccountID: "campus", IdempotencyKey: "click-1",
			IgnoreQuiet: true},
	}
	for _, request := range cases {
		err := h.submitExpectingError(request)
		if code := codeOf(t, err); code != domain.CodeConflict {
			t.Errorf("%+v gave %s, want Conflict", request, code)
		}
	}
}

// T23 -- a full queue answers Busy instead of growing.
//
// An unbounded queue on a router turns a network outage into an out-of-memory
// kill: every failed tick enqueues another attempt and nothing drains.
func TestAFullQueueIsBusy(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		options.Lines = func(Request) string { return "wan" }
		options.Parallel = 1
	})

	running := h.submit(KindLogin, "first", "click-0")
	h.awaitStart()

	for index := range QueueLimit {
		h.submit(KindLogin, "acct", "click-"+string(rune('a'+index)))
	}

	err := h.submitExpectingError(Request{Kind: KindLogin, AccountID: "acct",
		IdempotencyKey: "one-too-many"})
	if code := codeOf(t, err); code != domain.CodeBusy {
		t.Errorf("code = %s, want Busy", code)
	}
	if got := h.state(running.ActionID).State; got != StateRunning {
		t.Errorf("the running action became %s while the queue filled", got)
	}
}

// T23 -- two accounts resolving to one line never overlap.
//
// They cannot both be online, so overlapping logins would knock each other off
// and then take turns doing it. The serialisation is per resolved line, not per
// account: two different logical interface names over one device are one line.
func TestTwoAccountsOnOneLineNeverOverlap(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		options.Lines = func(Request) string { return "eth0" }
	})

	first := h.submit(KindLogin, "campus-a", "click-1")
	second := h.submit(KindLogin, "campus-b", "click-2")

	started := h.awaitStart()
	if started.ID != first.ActionID {
		t.Fatalf("started %s, want the first submission", started.ID)
	}
	h.expectNoStart("the line is already in use")
	if got := h.state(second.ActionID).State; got != StateQueued {
		t.Errorf("the second action is %s, want it waiting", got)
	}

	h.succeed(first.ActionID)
	h.awaitState(first.ActionID, StateSucceeded)

	next := h.awaitStart()
	if next.ID != second.ActionID {
		t.Errorf("started %s, want the one that was waiting", next.ID)
	}
	if next.Line != "eth0" {
		t.Errorf("line = %q", next.Line)
	}
}

// T23/T19 -- four lines run at once, and one that hangs does not hold up the
// others or the next arrival.
//
// This is the multi-WAN case. Spec 02 allows four lines' worth of I/O in
// parallel and requires that one account's network failure not block another
// account's status from moving.
func TestFourLinesRunAtOnceAndAStallDoesNotBlockThem(t *testing.T) {
	h := newHarness(t, nil)

	accounts := []string{"wan-a", "wan-b", "wan-c", "wan-d"}
	receipts := map[string]Receipt{}
	for index, account := range accounts {
		receipts[account] = h.submit(KindMaintain, account,
			"tick-"+string(rune('a'+index)))
	}

	live := map[string]bool{}
	for range accounts {
		live[h.awaitStart().Request.AccountID] = true
	}
	for _, account := range accounts {
		if !live[account] {
			t.Fatalf("%s never started; the four lines are not concurrent", account)
		}
	}

	// wan-a hangs. The other three finish regardless.
	for _, account := range accounts[1:] {
		h.succeed(receipts[account].ActionID)
		h.awaitState(receipts[account].ActionID, StateSucceeded)
	}
	if got := h.state(receipts["wan-a"].ActionID).State; got != StateRunning {
		t.Errorf("the stalled action is %s", got)
	}

	// And a new line can start while the stalled one is still going.
	fresh := h.submit(KindMaintain, "wan-e", "tick-e")
	if started := h.awaitStart(); started.ID != fresh.ActionID {
		t.Errorf("started %s, want the new arrival %s", started.ID, fresh.ActionID)
	}
}

// T23 -- a queued action that is cancelled never runs.
func TestCancellingAQueuedActionNeverStartsIt(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		options.Lines = func(Request) string { return "eth0" }
	})

	first := h.submit(KindLogin, "campus-a", "click-1")
	waiting := h.submit(KindLogin, "campus-b", "click-2")
	h.awaitStart()

	h.cancel(waiting.ActionID)
	cancelled := h.awaitState(waiting.ActionID, StateCancelled)
	if cancelled.Code != domain.CodeCancelled {
		t.Errorf("code = %s", cancelled.Code)
	}

	h.succeed(first.ActionID)
	h.awaitState(first.ActionID, StateSucceeded)
	h.expectNoStart("a cancelled action must not run later")
}

// T23 -- cancelling a running action reaches its worker at once, and the line
// stays held until that worker actually stops.
//
// Spec 04 is explicit that a cancellation does not mean the gateway never
// received the request. Freeing the line the moment the user clicks would let
// the next action start authenticating over the same uplink while the old one
// is still unwinding, which is the race the per-line rule exists to prevent.
func TestCancellingARunningActionHoldsTheLineUntilTheWorkerStops(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		options.Lines = func(Request) string { return "eth0" }
	})

	running := h.submit(KindLogin, "campus-a", "click-1")
	h.lingerOn(running.ActionID)
	waiting := h.submit(KindLogin, "campus-b", "click-2")
	h.awaitStart()

	h.cancel(running.ActionID)
	h.awaitState(running.ActionID, StateCancelled)
	h.awaitCancelSeen(running.ActionID)

	h.expectNoStart("the cancelled worker has not stopped yet")
	if got := h.state(waiting.ActionID).State; got != StateQueued {
		t.Errorf("the waiting action is %s", got)
	}

	// Now the worker really stops, and the line is released.
	h.finish(running.ActionID, Outcome{State: StateSucceeded, Message: "太晚了"})
	if next := h.awaitStart(); next.ID != waiting.ActionID {
		t.Errorf("started %s, want %s", next.ID, waiting.ActionID)
	}
}

// T24 -- a worker that finishes after its action was cancelled does not reopen
// it.
//
// Terminal states are irreversible. Without that, a login the user cancelled
// ten seconds ago reports success, and the interface says the account is online
// when nobody asked for it to be.
func TestALateWorkerResultDoesNotReopenATerminalAction(t *testing.T) {
	h := newHarness(t, nil)

	action := h.submit(KindLogin, "campus", "click-1")
	h.lingerOn(action.ActionID)
	h.awaitStart()

	h.cancel(action.ActionID)
	h.awaitState(action.ActionID, StateCancelled)
	h.awaitCancelSeen(action.ActionID)

	h.finish(action.ActionID, Outcome{State: StateSucceeded, Message: "认证完成"})

	// The worker's result has to have been processed before the assertion is
	// worth anything, and the release of the line is the observable sign of it.
	h.settle()
	fresh := h.submit(KindLogin, "campus", "click-2")
	h.awaitStart()

	final := h.state(action.ActionID)
	if final.State != StateCancelled {
		t.Errorf("state = %s, want it to have stayed cancelled", final.State)
	}
	if final.Message == "认证完成" {
		t.Error("the late worker's message replaced the cancellation")
	}
	if fresh.ActionID == action.ActionID {
		t.Fatal("the new submission reused the cancelled action")
	}
}

// T24 -- progress is visible while an action runs, so a stalled challenge and a
// stalled verify are distinguishable.
func TestProgressIsVisibleWhileAnActionRuns(t *testing.T) {
	h := newHarness(t, nil)
	action := h.submit(KindLogin, "campus", "click-1")
	h.awaitStart()

	if got := h.state(action.ActionID).Phase; got != PhaseWaitingLink {
		t.Errorf("phase = %q at the start, want %q", got, PhaseWaitingLink)
	}
	for _, phase := range []Phase{PhaseChallenge, PhaseLogin, PhaseVerify} {
		h.reportPhase(action.ActionID, phase)
		h.awaitPhase(action.ActionID, phase)
	}

	h.succeed(action.ActionID)
	final := h.awaitState(action.ActionID, StateSucceeded)
	if final.Phase != PhaseVerify {
		t.Errorf("the last phase was lost: %q", final.Phase)
	}
}

// T23 -- an action that waits too long for its turn is failed rather than left
// in a queue nobody is watching.
func TestAQueuedActionThatWaitsTooLongIsFailed(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		options.Lines = func(Request) string { return "eth0" }
	})

	running := h.submit(KindLogin, "campus-a", "click-1")
	waiting := h.submit(KindLogin, "campus-b", "click-2")
	h.awaitStart()

	h.clock.Advance(QueueWait - time.Second)
	h.settle()
	if got := h.state(waiting.ActionID).State; got != StateQueued {
		t.Fatalf("the action expired a second early: %s", got)
	}

	h.clock.Advance(2 * time.Second)
	expired := h.awaitState(waiting.ActionID, StateFailed)
	if expired.Code != domain.CodeDeadlineExceeded {
		t.Errorf("code = %s, want DeadlineExceeded", expired.Code)
	}
	if got := h.state(running.ActionID).State; got != StateRunning {
		t.Errorf("the running action was caught by the queue deadline: %s", got)
	}
}

// T23 -- an action that runs past its total budget is cancelled and reported as
// a deadline, not as whatever the worker returned once its context died.
func TestAnActionThatOverrunsIsCancelledAndReportedAsADeadline(t *testing.T) {
	h := newHarness(t, nil)
	action := h.submit(KindLogin, "campus", "click-1")
	h.awaitStart()

	h.clock.Advance(ActionBudget - time.Second)
	h.settle()
	if got := h.state(action.ActionID).State; got != StateRunning {
		t.Fatalf("the action was cut off early: %s", got)
	}

	h.clock.Advance(2 * time.Second)
	h.awaitCancelSeen(action.ActionID)
	overdue := h.awaitState(action.ActionID, StateFailed)
	if overdue.Code != domain.CodeDeadlineExceeded {
		t.Errorf("code = %s, want DeadlineExceeded", overdue.Code)
	}

	// The line is free again afterwards.
	h.submit(KindLogin, "campus", "click-2")
	h.awaitStart()
}

// A user's action outranks maintenance that was queued before it. Spec 04 fixes
// the order, and this is the pair that matters most: a user pressing 登录 while
// the automatic loop is backed up should not wait behind it.
func TestAUserActionOutranksQueuedMaintenance(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		options.Lines = func(Request) string { return "eth0" }
	})

	first := h.submit(KindMaintain, "campus", "tick-1")
	h.awaitStart()

	h.submit(KindMaintain, "campus", "tick-2")
	manual := h.submit(KindLogin, "campus", "click-1")

	h.succeed(first.ActionID)
	h.awaitState(first.ActionID, StateSucceeded)

	if next := h.awaitStart(); next.ID != manual.ActionID {
		t.Errorf("started %s, want the user's action %s", next.ID, manual.ActionID)
	}
}

// T23/T24 -- finished actions stay readable for a while and then are forgotten,
// and their keys are forgotten with them.
//
// Keeping keys forever is a slow leak on a device that runs for months.
// Forgetting them sooner would let a resubmission start a second login.
func TestTheTerminalHistoryIsBoundedAndReleasesItsKeys(t *testing.T) {
	const history = 3
	h := newHarness(t, func(options *Options) { options.History = history })

	var ids []string
	var keys []string
	for index := range history + 2 {
		key := "click-" + string(rune('a'+index))
		receipt := h.submit(KindLogin, "acct-"+string(rune('a'+index)), key)
		h.awaitStart()
		h.succeed(receipt.ActionID)
		h.awaitState(receipt.ActionID, StateSucceeded)
		ids = append(ids, receipt.ActionID)
		keys = append(keys, key)
	}

	if got := len(h.list()); got != history {
		t.Errorf("remembered %d actions, want %d", got, history)
	}
	ctx, cancel := h.callContext()
	defer cancel()
	if _, err := h.Action(ctx, ids[0]); err == nil {
		t.Error("the oldest action is still readable")
	} else if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound", code)
	}
	if _, err := h.Action(ctx, ids[len(ids)-1]); err != nil {
		t.Errorf("the newest action was forgotten: %v", err)
	}

	// The evicted key is free again, so a resubmission starts something new
	// rather than reporting a Conflict against an action nobody remembers.
	reused := h.submit(KindLogout, "acct-a", keys[0])
	if reused.Duplicate || reused.ActionID == ids[0] {
		t.Errorf("the evicted key was still bound: %+v", reused)
	}
}

// A Runner may report success or failure. Deciding that an action was cancelled
// or interrupted is the coordinator's call, and a Runner claiming it would be
// writing state it does not own.
func TestARunnerCannotDecideAnActionsFate(t *testing.T) {
	for _, claimed := range []State{StateCancelled, StateInterrupted,
		StateQueued, StateRunning, State("nonsense")} {
		t.Run(string(claimed), func(t *testing.T) {
			h := newHarness(t, nil)
			action := h.submit(KindLogin, "campus", "click-1")
			h.awaitStart()

			h.finish(action.ActionID, Outcome{State: claimed, Message: "随便"})
			failed := h.awaitState(action.ActionID, StateFailed)
			if failed.Code != domain.CodeInternal {
				t.Errorf("code = %s, want Internal", failed.Code)
			}
		})
	}
}

// T25's neighbour: stopping the service interrupts, and interrupted is not
// failed. Spec 02 forbids replaying an action the user force-stopped, and
// telling the two apart at restart is how that gets obeyed.
func TestStoppingInterruptsRatherThanFails(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		options.Lines = func(Request) string { return "eth0" }
	})

	running := h.submit(KindLogin, "campus-a", "click-1")
	waiting := h.submit(KindLogin, "campus-b", "click-2")
	h.awaitStart()

	h.stop()
	select {
	case <-h.stopped:
	case <-time.After(patience):
		t.Fatal("Run did not return")
	}

	for _, id := range []string{running.ActionID, waiting.ActionID} {
		found := h.awaitState(id, StateInterrupted)
		if found.State == StateFailed {
			t.Errorf("%s was reported as failed rather than interrupted", id)
		}
	}
}

// Stopping the service does not relabel an action the user had already
// cancelled.
//
// The two are different at restart. Spec 02 forbids replaying an action the
// user force-stopped, and an interrupted one may legitimately be resumed --
// so overwriting "cancelled" with "interrupted" on the way out would turn a
// decision the user made into one they did not.
//
// The worker here ignores its cancellation, which is what keeps the action
// terminal *and* still running when the stop arrives. That combination is the
// only way into this branch, and it is why the guard sits in the transition
// itself rather than at each call site.
func TestStoppingDoesNotRelabelAnAlreadyCancelledAction(t *testing.T) {
	h := newHarness(t, nil)
	h.deafOn("campus")

	action := h.submit(KindLogin, "campus", "click-1")
	h.awaitStart()

	h.cancel(action.ActionID)
	h.awaitState(action.ActionID, StateCancelled)

	h.stop()
	// Release the deaf worker so the stop can finish.
	h.finish(action.ActionID, Outcome{State: StateSucceeded, Message: "太晚了"})
	select {
	case <-h.stopped:
	case <-time.After(patience):
		t.Fatal("Run did not return")
	}

	states := h.recorded(action.ActionID)
	if len(states) == 0 || states[len(states)-1] != StateCancelled {
		t.Errorf("states = %v, want it to end cancelled", states)
	}
	if slices.Contains(states, StateInterrupted) {
		t.Errorf("the stop relabelled a cancelled action: %v", states)
	}
}

// Once the service is gone, callers are told so rather than blocking on a loop
// that will never answer.
func TestCallsAfterTheServiceStoppedAreRefused(t *testing.T) {
	h := newHarness(t, nil)
	h.stop()
	select {
	case <-h.stopped:
	case <-time.After(patience):
		t.Fatal("Run did not return")
	}

	ctx, cancel := h.callContext()
	defer cancel()

	_, err := h.Submit(ctx, Request{Kind: KindLogin, AccountID: "campus",
		IdempotencyKey: "click-1"})
	if err == nil {
		t.Fatal("a submission was accepted after the service stopped")
	}
	if code := codeOf(t, err); code != domain.CodeServiceStopped {
		t.Errorf("code = %s, want ServiceStopped", code)
	}
	if err := h.Cancel(ctx, "a1"); err == nil {
		t.Error("a cancellation was accepted after the service stopped")
	}
	if _, err := h.Actions(ctx); err == nil {
		t.Error("a listing was answered after the service stopped")
	}
}

// Every loop in the coordinator has a way out, including the one whose exit
// depends on somebody else behaving.
//
// The stop path waits for its workers, and a worker that ignores its context
// would otherwise hold the service open forever -- which on a router means a
// sysupgrade that never starts. The wait is bounded, and what comes back says
// why rather than pretending the stop was clean.
func TestAWorkerThatIgnoresItsCancellationDoesNotHangTheStop(t *testing.T) {
	h := newHarness(t, func(options *Options) {
		// A real clock here, deliberately. What is under test is a timeout, and
		// a fake one cannot be advanced past a grace timer armed inside the
		// stop path without racing the arming.
		options.Clock = policy.SystemClock{}
		options.ShutdownGrace = 100 * time.Millisecond
	})
	h.deafOn("campus")

	h.submit(KindLogin, "campus", "click-1")
	h.awaitStart()

	h.stop()
	select {
	case <-h.stopped:
	case <-time.After(patience):
		t.Fatal("a worker that ignored its cancellation held the stop open")
	}

	if h.runErr == nil {
		t.Fatal("the stop reported success although a worker never returned")
	}
	if code := codeOf(t, h.runErr); code != domain.CodeInternal {
		t.Errorf("code = %s, want Internal", code)
	}
}

// A second Run would be a second writer, which is the whole thing this design
// exists to prevent.
func TestASecondRunIsRefused(t *testing.T) {
	h := newHarness(t, nil)

	err := h.Coordinator.Run(t.Context())
	if err == nil {
		t.Fatal("a second Run started")
	}
	if code := codeOf(t, err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}
}

// A malformed request is refused before anything is queued, and the reason
// names what is wrong.
func TestAMalformedRequestIsRefusedBeforeQueueing(t *testing.T) {
	h := newHarness(t, nil)

	cases := map[string]Request{
		"no kind":      {AccountID: "campus", IdempotencyKey: "k"},
		"unknown kind": {Kind: "reboot", AccountID: "campus", IdempotencyKey: "k"},
		"no account":   {Kind: KindLogin, IdempotencyKey: "k"},
		"no key":       {Kind: KindLogin, AccountID: "campus"},
		"a hotspot switch with no hotspot": {Kind: KindSwitchHotspot,
			AccountID: "campus", IdempotencyKey: "k"},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			err := h.submitExpectingError(request)
			if code := codeOf(t, err); code != domain.CodeInvalidArgument {
				t.Errorf("code = %s, want InvalidArgument", code)
			}
		})
	}
	if got := len(h.list()); got != 0 {
		t.Errorf("%d actions were created by refused requests", got)
	}
	h.expectNoStart("nothing valid was submitted")
}

func TestCancellingSomethingUnknownOrFinishedIsHandledPlainly(t *testing.T) {
	h := newHarness(t, nil)

	ctx, cancel := h.callContext()
	defer cancel()
	err := h.Cancel(ctx, "a999")
	if err == nil {
		t.Fatal("cancelling an unknown action succeeded")
	}
	if code := codeOf(t, err); code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound", code)
	}

	action := h.submit(KindLogin, "campus", "click-1")
	h.awaitStart()
	h.succeed(action.ActionID)
	h.awaitState(action.ActionID, StateSucceeded)

	// Cancelling something that already finished is not an error: what the
	// caller wanted is already true.
	h.cancel(action.ActionID)
	if got := h.state(action.ActionID).State; got != StateSucceeded {
		t.Errorf("a late cancellation changed a finished action to %s", got)
	}
}

// The limits are the ones the specs fix. Raising one silently changes how much
// memory the daemon can use on a router that has none to spare.
func TestTheLimitsAreTheDocumentedOnes(t *testing.T) {
	if QueueLimit != 32 || TerminalHistory != 64 {
		t.Errorf("queue/history = %d/%d, want 32/64", QueueLimit, TerminalHistory)
	}
	if QueueWait != 30*time.Second || ActionBudget != 600*time.Second {
		t.Errorf("waits = %v/%v, want 30s/600s", QueueWait, ActionBudget)
	}
}

// Kinds carry the priority spec 04 assigns them, and only the two the scheduler
// raises itself are non-manual.
func TestKindsCarryTheirPriorityAndManualFlag(t *testing.T) {
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Errorf("%s is listed but not valid", kind)
		}
	}
	if Kind("reboot").Valid() {
		t.Error("an invented kind is valid")
	}
	if KindMaintain.Manual() || KindForcedLogout.Manual() {
		t.Error("a scheduler-raised action was reported as manual")
	}
	if !KindLogin.Manual() || !KindSwitchCampus.Manual() {
		t.Error("a user action was reported as automatic")
	}
	if KindLogin.Priority() <= KindMaintain.Priority() {
		t.Error("maintenance outranks a user action")
	}
	if KindForcedLogout.Priority() <= KindMaintain.Priority() {
		t.Error("the quiet-hours sweep can be starved by the loop it suspends")
	}
	if KindForcedLogout.Priority() >= KindLogin.Priority() {
		t.Error("the sweep outranks a user's own action")
	}
}
