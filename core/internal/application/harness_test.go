package application

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

// epoch is where the fake clock starts. Every duration in these tests is
// relative to it, and none of them is a real one: the coordinator's deadlines
// are measured against the injected clock precisely so a six-hundred-second
// budget can be reached in a microsecond.
var epoch = time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)

// patience bounds the waits for a real goroutine handoff. It is a failure
// timeout, not a delay: nothing in these tests waits for it on the happy path.
const patience = 5 * time.Second

// step is one instruction to a worker: report a phase, or finish.
type step struct {
	phase   Phase
	outcome Outcome
}

// harness runs a coordinator whose clock and worker the test drives by hand.
type harness struct {
	t     *testing.T
	clock *faketime.Clock
	*Coordinator

	// entered receives every action a worker started on.
	entered chan Action
	// sawCancel receives the id of every action whose worker observed its
	// context being cancelled.
	sawCancel chan string

	mu     sync.Mutex
	steps  map[string]chan step
	linger map[string]bool
	// deaf accounts get a worker that ignores cancellation entirely, which is
	// how the stop path's own escape hatch gets exercised. Keyed by account
	// rather than action id because the worker consults it the instant it
	// starts, which can be before the test has seen the receipt.
	deaf   map[string]bool
	seen   []Action
	notify chan struct{}
	// teardown releases deaf workers when the test is over.
	teardown chan struct{}

	stop context.CancelFunc
	// stopped is closed when Run returns, so several waiters -- a test and the
	// cleanup behind it -- can each see it.
	stopped chan struct{}
	runErr  error
}

func newHarness(t *testing.T, configure func(*Options)) *harness {
	t.Helper()

	h := &harness{
		t:         t,
		clock:     faketime.New(epoch),
		entered:   make(chan Action, 256),
		sawCancel: make(chan string, 256),
		steps:     map[string]chan step{},
		linger:    map[string]bool{},
		deaf:      map[string]bool{},
		notify:    make(chan struct{}, 1),
		stopped:   make(chan struct{}),
		teardown:  make(chan struct{}),
	}
	t.Cleanup(func() { close(h.teardown) })

	options := Options{
		Clock:    h.clock,
		Runner:   h,
		Lines:    func(request Request) string { return request.AccountID },
		Observer: h.record,
	}
	if configure != nil {
		configure(&options)
	}
	h.Coordinator = New(options)

	ctx, stop := context.WithCancel(context.Background())
	h.stop = stop
	go func() {
		h.runErr = h.Coordinator.Run(ctx)
		close(h.stopped)
	}()
	t.Cleanup(h.shutdown)

	// One round-trip before handing the harness over, so every test can assume
	// the loop is already running. Without it a test that calls Run itself
	// races the goroutine above for the right to be the loop.
	h.list()
	return h
}

// record is the Observer. It must not block the coordinator's loop.
func (h *harness) record(action Action) {
	h.mu.Lock()
	h.seen = append(h.seen, action)
	h.mu.Unlock()
	select {
	case h.notify <- struct{}{}:
	default:
	}
}

// Run is the Runner. A worker waits for the test to tell it what to do.
func (h *harness) Run(ctx context.Context, action Action, report func(Phase)) Outcome {
	h.entered <- action
	steps := h.stepsFor(action.ID)

	if h.isDeaf(action.Request.AccountID) {
		// A worker that never notices its context. Nothing but the end of the
		// test releases it.
		select {
		case next := <-steps:
			return next.outcome
		case <-h.teardown:
			return Outcome{State: StateFailed, Code: domain.CodeInternal,
				Message: "the test ended"}
		}
	}

	for {
		select {
		case next := <-steps:
			if next.phase != "" {
				report(next.phase)
				continue
			}
			return next.outcome
		case <-ctx.Done():
			h.sawCancel <- action.ID
			if !h.lingersOn(action.ID) {
				return Outcome{State: StateFailed, Code: domain.CodeCancelled,
					Message: "worker stopped when it was cancelled"}
			}
			// Keep going, so a test can show that the line is still held while
			// a cancelled worker is unwinding.
			select {
			case next := <-steps:
				return next.outcome
			case <-time.After(patience):
				return Outcome{State: StateFailed, Code: domain.CodeInternal,
					Message: "the test never released a lingering worker"}
			}
		}
	}
}

func (h *harness) stepsFor(actionID string) chan step {
	h.mu.Lock()
	defer h.mu.Unlock()
	channel, known := h.steps[actionID]
	if !known {
		channel = make(chan step, 8)
		h.steps[actionID] = channel
	}
	return channel
}

func (h *harness) lingersOn(actionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.linger[actionID]
}

// lingerOn makes the worker for the next action on this account keep running
// after it is cancelled, until the test finishes it.
func (h *harness) lingerOn(actionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.linger[actionID] = true
}

func (h *harness) isDeaf(accountID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deaf[accountID]
}

// deafOn makes this account's workers ignore cancellation. Call it before
// submitting: the worker reads it as soon as it starts.
func (h *harness) deafOn(accountID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deaf[accountID] = true
}

func (h *harness) callContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), patience)
}

func (h *harness) submit(kind Kind, account, key string) Receipt {
	h.t.Helper()
	ctx, cancel := h.callContext()
	defer cancel()
	receipt, err := h.Submit(ctx, Request{Kind: kind, AccountID: account,
		IdempotencyKey: key})
	if err != nil {
		h.t.Fatalf("submit %s/%s: %v", kind, account, err)
	}
	return receipt
}

func (h *harness) submitExpectingError(request Request) error {
	h.t.Helper()
	ctx, cancel := h.callContext()
	defer cancel()
	_, err := h.Submit(ctx, request)
	if err == nil {
		h.t.Fatalf("%+v was accepted", request)
	}
	return err
}

func (h *harness) cancel(actionID string) {
	h.t.Helper()
	ctx, cancel := h.callContext()
	defer cancel()
	if err := h.Cancel(ctx, actionID); err != nil {
		h.t.Fatalf("cancel %s: %v", actionID, err)
	}
}

func (h *harness) state(actionID string) Action {
	h.t.Helper()
	ctx, cancel := h.callContext()
	defer cancel()
	action, err := h.Action(ctx, actionID)
	if err != nil {
		h.t.Fatalf("read %s: %v", actionID, err)
	}
	return action
}

func (h *harness) list() []Action {
	h.t.Helper()
	ctx, cancel := h.callContext()
	defer cancel()
	all, err := h.Actions(ctx)
	if err != nil {
		h.t.Fatalf("list: %v", err)
	}
	return all
}

// settle drives the loop through a full iteration, so a deadline the clock has
// just passed has certainly been applied.
//
// Two round-trips, not one. The first may be answered inside the iteration that
// was already in progress when the clock moved, and expiry runs at the top of
// the loop -- so only between two consecutive round-trips is there guaranteed
// to be an expiry pass at the current time. This is why none of these tests
// needs a sleep to "let things happen".
func (h *harness) settle() {
	h.t.Helper()
	for range 2 {
		h.list()
	}
}

// finish releases a worker with a result.
func (h *harness) finish(actionID string, outcome Outcome) {
	h.t.Helper()
	select {
	case h.stepsFor(actionID) <- step{outcome: outcome}:
	case <-time.After(patience):
		h.t.Fatalf("no worker was waiting for %s", actionID)
	}
}

func (h *harness) succeed(actionID string) {
	h.t.Helper()
	h.finish(actionID, Outcome{State: StateSucceeded, Message: "认证完成"})
}

// reportPhase makes a worker announce progress without finishing.
func (h *harness) reportPhase(actionID string, phase Phase) {
	h.t.Helper()
	select {
	case h.stepsFor(actionID) <- step{phase: phase}:
	case <-time.After(patience):
		h.t.Fatalf("no worker was waiting for %s", actionID)
	}
}

// awaitStart waits for a worker to begin.
func (h *harness) awaitStart() Action {
	h.t.Helper()
	select {
	case action := <-h.entered:
		return action
	case <-time.After(patience):
		h.t.Fatal("no worker started")
		return Action{}
	}
}

// expectNoStart asserts that nothing began. It settles first, because dispatch
// happens at the top of the loop and an assertion made before the loop has run
// would pass for the wrong reason.
func (h *harness) expectNoStart(why string) {
	h.t.Helper()
	h.settle()
	select {
	case action := <-h.entered:
		h.t.Fatalf("%s: %s started anyway on line %q", why, action.ID, action.Line)
	default:
	}
}

func (h *harness) awaitCancelSeen(actionID string) {
	h.t.Helper()
	for {
		select {
		case seen := <-h.sawCancel:
			if seen == actionID {
				return
			}
		case <-time.After(patience):
			h.t.Fatalf("the worker for %s never saw its cancellation", actionID)
		}
	}
}

// awaitState waits for an action to be published in a given state.
func (h *harness) awaitState(actionID string, want State) Action {
	h.t.Helper()
	deadline := time.After(patience)
	for {
		h.mu.Lock()
		for _, event := range h.seen {
			if event.ID == actionID && event.State == want {
				h.mu.Unlock()
				return event
			}
		}
		h.mu.Unlock()

		select {
		case <-h.notify:
		case <-deadline:
			h.t.Fatalf("%s never reached %s; it is %s",
				actionID, want, h.state(actionID).State)
			return Action{}
		}
	}
}

// recorded returns every state this action was published in, in order. It is
// the only way to check an action after Run has returned, and it is also how a
// test asks "did it ever pass through X" rather than only "where is it now".
func (h *harness) recorded(actionID string) []State {
	h.mu.Lock()
	defer h.mu.Unlock()
	var states []State
	for _, event := range h.seen {
		if event.ID != actionID {
			continue
		}
		if len(states) == 0 || states[len(states)-1] != event.State {
			states = append(states, event.State)
		}
	}
	return states
}

// awaitPhase waits for a phase to be published for a running action.
func (h *harness) awaitPhase(actionID string, want Phase) {
	h.t.Helper()
	deadline := time.After(patience)
	for {
		h.mu.Lock()
		for _, event := range h.seen {
			if event.ID == actionID && event.Phase == want {
				h.mu.Unlock()
				return
			}
		}
		h.mu.Unlock()

		select {
		case <-h.notify:
		case <-deadline:
			h.t.Fatalf("%s never reported phase %q", actionID, want)
			return
		}
	}
}

// shutdown stops the coordinator and waits for the loop to return. It is safe
// to call after a test has already stopped it.
func (h *harness) shutdown() {
	h.stop()
	select {
	case <-h.stopped:
	case <-time.After(patience):
		h.t.Error("Run did not return after its context was cancelled")
	}
}

func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	code, ok := domain.CodeOf(err)
	if !ok {
		t.Fatalf("error carries no code: %v", err)
	}
	return code
}
