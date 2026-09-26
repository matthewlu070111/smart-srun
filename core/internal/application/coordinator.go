package application

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
)

// Limits fixed by specs 02 and 03. They are defaults here and overridable in
// Options so tests can reach a boundary without waiting for it, but the daemon
// uses these.
const (
	// QueueLimit is how many actions may wait. Past it, submissions are Busy
	// rather than queued: an unbounded queue on a router turns a network outage
	// into an out-of-memory kill.
	QueueLimit = 32
	// TerminalHistory is how many finished actions stay readable, so a UI that
	// polls a moment late still finds the result it was waiting for.
	TerminalHistory = 64
	// QueueWait is the longest an action waits for its turn.
	QueueWait = 30 * time.Second
	// ActionBudget is one action's total deadline.
	ActionBudget = 600 * time.Second
	// ShutdownGrace bounds how long a stop waits for workers that are not
	// honouring their cancellation.
	ShutdownGrace = 10 * time.Second
)

// Options configures a Coordinator. Zero-valued numeric fields take the
// constants above.
type Options struct {
	Clock  policy.Clock
	Runner Runner
	// Lines resolves the line a request will occupy. Two actions that resolve
	// to the same line never run at the same time, which is what keeps two
	// accounts from taking turns knocking each other off one uplink.
	//
	// It must be a cheap, pure lookup -- an account's configured interface,
	// not a probe. It runs on the coordinator's goroutine while a submission
	// waits, so a ubus call here would block every other caller. Resolving the
	// actual binding is the worker's job, and it happens after dispatch.
	Lines func(Request) string
	// Check runs on the loop before a new request is queued. It rejects work
	// prepared against a configuration that has since changed.
	Check func(Request) error
	// Admit records explicit user intent once, after validation and capacity
	// checks, before any cancellation or network work. Duplicates skip it.
	Admit func(Request) error
	// Finalize commits local effects of a successful worker on the loop, before
	// publishing success. It must not call back into the coordinator.
	Finalize func(Action, Outcome) Outcome

	// Observer is called whenever an action changes state or phase, with the
	// action as it now stands. It is the seam the structured event log will
	// attach to (M11), and it is what lets a test wait for a transition instead
	// of polling for one.
	//
	// It runs on the coordinator's own goroutine, so it must return promptly
	// and must not call back into the coordinator.
	Observer func(Action)

	// Record receives what a finished attempt learned about its line. The
	// daemon points it at the observe store.
	//
	// It runs on the coordinator's goroutine, for the same reason Observer
	// does: a worker calling the store directly from its own goroutine would
	// be a second writer, and the ordering guarantee the store enforces would
	// be enforced against results that arrived in whatever order the scheduler
	// produced.
	Record func(observe.Observation)

	// Parallel caps simultaneously running actions. Spec 02 fixes it at four
	// lines' worth of I/O.
	Parallel      int
	QueueLimit    int
	History       int
	QueueWait     time.Duration
	ActionBudget  time.Duration
	ShutdownGrace time.Duration
}

// Coordinator owns the action index, the queue and the running set.
//
// Every field below the mutex-free line is touched only by the goroutine
// running Run. There is no mutex because there is no sharing: callers post a
// closure onto calls and wait for it to run on the loop.
type Coordinator struct {
	clock         policy.Clock
	runner        Runner
	lines         func(Request) string
	check         func(Request) error
	admit         func(Request) error
	finalize      func(Action, Outcome) Outcome
	observer      func(Action)
	record        func(observe.Observation)
	parallel      int
	queueLimit    int
	history       int
	queueWait     time.Duration
	actionBudget  time.Duration
	shutdownGrace time.Duration
	instanceID    string

	calls   chan func()
	finish  chan completion
	phases  chan phaseReport
	done    chan struct{}
	started atomic.Bool
	workers sync.WaitGroup

	// Loop-owned state.
	queue     []*Action
	index     map[string]*Action
	byKey     map[string]string
	running   map[string]*run
	busyLines map[string]string
	terminals []string
	nextID    uint64
	nextSeq   uint64
}

// run is the coordinator's handle on one worker.
//
// Membership of the running map is the whole liveness test. An earlier version
// also carried the dispatch sequence and compared it against every arriving
// result, on the theory that a superseded worker's answer had to be refused --
// but an action is dispatched exactly once and leaves the map when it finishes,
// so no comparison could ever fail, and writing the mutation that was supposed
// to break it turned out to be impossible. The ordering guarantee that does
// need enforcing is in observe, where results about different dispatches really
// do arrive out of order.
type run struct {
	cancel context.CancelFunc
	line   string
	// overdue records that the action budget expired, so the completion that
	// eventually arrives is reported as a deadline rather than as whatever the
	// worker happened to return once its context died.
	overdue bool
}

type completion struct {
	id                 string
	outcome            Outcome
	timings            []PhaseTiming
	workerMilliseconds int64
}

type phaseReport struct {
	id    string
	phase Phase
}

// New builds a Coordinator. Lines and Runner are required: a coordinator
// without them would accept work and then have nothing to do with it, and
// discovering that at the first login is worse than discovering it at startup.
func New(options Options) *Coordinator {
	if options.Runner == nil {
		panic("application: Coordinator needs a Runner")
	}
	if options.Lines == nil {
		panic("application: Coordinator needs a line resolver")
	}
	clock := options.Clock
	if clock == nil {
		clock = policy.SystemClock{}
	}
	observer := options.Observer
	if observer == nil {
		observer = func(Action) {}
	}
	record := options.Record
	if record == nil {
		record = func(observe.Observation) {}
	}

	c := &Coordinator{
		clock:         clock,
		runner:        options.Runner,
		lines:         options.Lines,
		check:         options.Check,
		admit:         options.Admit,
		finalize:      options.Finalize,
		observer:      observer,
		record:        record,
		parallel:      orDefaultInt(options.Parallel, policy.MaxConcurrentLines),
		queueLimit:    orDefaultInt(options.QueueLimit, QueueLimit),
		history:       orDefaultInt(options.History, TerminalHistory),
		queueWait:     orDefault(options.QueueWait, QueueWait),
		actionBudget:  orDefault(options.ActionBudget, ActionBudget),
		shutdownGrace: orDefault(options.ShutdownGrace, ShutdownGrace),
		// Receipts outlive the process in browser tabs. A fresh namespace keeps
		// a restarted daemon from assigning an old receipt to unrelated work.
		instanceID: rand.Text(),

		calls:     make(chan func()),
		done:      make(chan struct{}),
		index:     map[string]*Action{},
		byKey:     map[string]string{},
		running:   map[string]*run{},
		busyLines: map[string]string{},
	}
	// Buffered so a worker reporting progress never waits for the loop, and
	// bounded so it cannot become an unread backlog either.
	c.finish = make(chan completion, c.parallel)
	c.phases = make(chan phaseReport, c.parallel*4)
	return c
}

func orDefaultInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func orDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// Run is the loop. It returns when ctx is done, after every worker has stopped
// or the shutdown grace has run out.
//
// One call only: a second Run would be a second writer, which is the thing this
// design exists to prevent.
func (c *Coordinator) Run(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return domain.Errorf(domain.CodeConflict, "协调器已经在运行")
	}

	for {
		c.expire(c.clock.Now())
		c.dispatch(ctx)

		var wake <-chan time.Time
		var timer policy.Timer
		if deadline, ok := c.nextDeadline(); ok {
			// Absolute, so a clock that moved while this iteration was running
			// wakes the loop at once instead of resetting the wait.
			timer = c.clock.NewTimerAt(deadline)
			wake = timer.C()
		}

		select {
		case <-ctx.Done():
			stopTimer(timer)
			return c.shutdown(nil)
		case job := <-c.calls:
			job()
		case done := <-c.finish:
			if ctx.Err() != nil {
				// The stop and this result became ready in the same instant and
				// the select took this one; Go chooses uniformly between ready
				// cases, so which branch runs is a coin toss. Applying the
				// worker's verdict here would record a force-stopped action as
				// whatever it happened to return -- and spec 02 needs
				// interrupted to stay distinguishable from failed, or a restart
				// replays something the user stopped on purpose. Handing the
				// result to shutdown means it is applied after the interruption
				// is recorded, where onFinish leaves a terminal state alone.
				stopTimer(timer)
				return c.shutdown(&done)
			}
			c.onFinish(done)
		case report := <-c.phases:
			c.onPhase(report)
		case <-wake:
		}
		stopTimer(timer)
	}
}

func stopTimer(timer policy.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

// shutdown cancels everything and waits, bounded, for the workers.
//
// Queued actions become interrupted rather than failed: spec 02 forbids
// replaying an action the user force-stopped, and telling the two apart at
// restart is how that is obeyed.
// pending is a completion that arrived in the same instant as the stop. It is
// applied only after everything has been marked interrupted, so the worker's
// verdict files the action and frees its line without changing its state.
func (c *Coordinator) shutdown(pending *completion) error {
	now := c.clock.Now()
	for _, action := range c.queue {
		action.transition(StateInterrupted, now)
		action.Message = "服务停止，动作未开始"
		c.retire(action)
		c.publish(action)
	}
	c.queue = nil
	for id, state := range c.running {
		state.cancel()
		if action, ok := c.index[id]; ok && action.transition(StateInterrupted, now) {
			action.Message = "服务停止，动作被中断"
			c.publish(action)
		}
	}

	if pending != nil {
		c.onFinish(*pending)
	}

	stopped := make(chan struct{})
	go func() {
		c.workers.Wait()
		close(stopped)
	}()

	grace := c.clock.NewTimerAt(c.clock.Now().Add(c.shutdownGrace))
	defer grace.Stop()
	for {
		select {
		case <-stopped:
			// A clean stop is not a failure. Returning ctx.Err() here would
			// make every ordinary SIGTERM look like the daemon crashed, and
			// procd would log it as one.
			close(c.done)
			return nil
		case <-grace.C():
			// A worker is ignoring its cancellation. Closing done releases it
			// if it is only blocked reporting a result, and reports the fault
			// rather than hanging the service stop forever.
			close(c.done)
			return domain.Errorf(domain.CodeInternal,
				"停止时仍有 %d 个动作未在取消后退出", len(c.running))
		case done := <-c.finish:
			c.onFinish(done)
		case <-c.phases:
			// Progress after a stop has nobody to inform.
		}
	}
}

// call runs job on the loop goroutine and waits for wait to be closed.
func (c *Coordinator) call(ctx context.Context, job func(), wait <-chan struct{}) error {
	select {
	case c.calls <- job:
	case <-ctx.Done():
		return contextError(ctx)
	case <-c.done:
		return stoppedError()
	}
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return contextError(ctx)
	case <-c.done:
		return stoppedError()
	}
}

func stoppedError() error {
	return domain.Errorf(domain.CodeServiceStopped, "认证服务未在运行")
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.Errorf(domain.CodeDeadlineExceeded, "等待协调器超时").Wrap(ctx.Err())
	}
	return domain.Errorf(domain.CodeCancelled, "调用已取消").Wrap(ctx.Err())
}

// Submit queues an action, or returns the existing one for a repeated key.
func (c *Coordinator) Submit(ctx context.Context, request Request) (Receipt, error) {
	if err := request.Validate(); err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	var failure error
	ready := make(chan struct{})
	err := c.call(ctx, func() {
		receipt, failure = c.onSubmit(request)
		close(ready)
	}, ready)
	if err != nil {
		return Receipt{}, err
	}
	return receipt, failure
}

// ChangeConfiguration serializes a short, local configuration transaction with
// dispatch. Even a cancelled worker must have exited before credentials or line
// settings can change. The callback must not call back into the coordinator.
func (c *Coordinator) ChangeConfiguration(ctx context.Context, change func() error) error {
	var failure error
	ready := make(chan struct{})
	err := c.call(ctx, func() {
		defer close(ready)
		switch {
		case ctx.Err() != nil:
			failure = contextError(ctx)
		case len(c.queue) != 0 || len(c.running) != 0:
			failure = domain.Errorf(domain.CodeBusy, "动作尚未结束，请稍后保存配置")
		default:
			failure = change()
		}
	}, ready)
	if err != nil {
		return err
	}
	return failure
}

// Cancel asks for an action to stop. Cancelling a finished action is not an
// error: the caller's intent is already satisfied.
func (c *Coordinator) Cancel(ctx context.Context, actionID string) error {
	var failure error
	ready := make(chan struct{})
	err := c.call(ctx, func() {
		failure = c.onCancel(actionID)
		close(ready)
	}, ready)
	if err != nil {
		return err
	}
	return failure
}

// Action reads one action.
func (c *Coordinator) Action(ctx context.Context, actionID string) (Action, error) {
	var found Action
	var failure error
	ready := make(chan struct{})
	err := c.call(ctx, func() {
		if action, ok := c.index[actionID]; ok {
			found = action.publicCopy()
		} else {
			failure = domain.Errorf(domain.CodeNotFound, "没有编号为 %s 的动作", actionID)
		}
		close(ready)
	}, ready)
	if err != nil {
		return Action{}, err
	}
	return found, failure
}

// Actions reads every action the coordinator still remembers, queued and
// running first, then the retained terminals in the order they finished.
func (c *Coordinator) Actions(ctx context.Context) ([]Action, error) {
	var all []Action
	ready := make(chan struct{})
	err := c.call(ctx, func() {
		all = c.onList()
		close(ready)
	}, ready)
	if err != nil {
		return nil, err
	}
	return all, nil
}

// Inspect collects a read-only projection on the loop, so its config, account
// observations and action list cannot straddle a configuration transaction.
// read must be fast, must not retain mutable state, and must not call back in.
func (c *Coordinator) Inspect(ctx context.Context, read func([]Action)) error {
	ready := make(chan struct{})
	return c.call(ctx, func() {
		read(c.onList())
		close(ready)
	}, ready)
}
