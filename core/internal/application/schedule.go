package application

import (
	"context"
	"crypto/sha256"
	"sort"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Everything in this file runs on the goroutine executing Run, and nowhere
// else. That is the whole reason none of it takes a lock: there is one writer,
// so there is nothing to exclude.

// publish announces a change. The observer sees the action as it now stands,
// by value, so it cannot alter what the next reader will see.
func (a Action) publicCopy() Action {
	a.Request.PrivateJSON = ""
	a.Timings = append([]PhaseTiming(nil), a.Timings...)
	return a
}
func (c *Coordinator) publish(action *Action) { c.observer(action.publicCopy()) }

// onSubmit queues an action, or hands back the one an identical key already
// started.
func (c *Coordinator) onSubmit(request Request) (Receipt, error) {
	request.privateDigest = sha256.Sum256([]byte(request.PrivateJSON))
	if existingID, seen := c.byKey[request.IdempotencyKey]; seen {
		if existing, live := c.index[existingID]; live {
			if existing.Request.fingerprint() != request.fingerprint() {
				// Same key, different work. Returning the first action's id
				// would report the wrong thing as finished, which is worse than
				// refusing.
				return Receipt{}, domain.Errorf(domain.CodeConflict,
					"幂等键 %q 已经用于另一个动作", request.IdempotencyKey)
			}
			return Receipt{ActionID: existing.ID, State: existing.State,
				Duplicate: true}, nil
		}
		delete(c.byKey, request.IdempotencyKey)
	}

	if c.check != nil {
		if err := c.check(request); err != nil {
			return Receipt{}, err
		}
	}
	if len(c.queue) >= c.queueLimit {
		return Receipt{}, domain.Errorf(domain.CodeBusy,
			"动作队列已满（上限 %d 个），请稍后重试", c.queueLimit)
	}
	if c.admit != nil {
		if err := c.admit(request); err != nil {
			return Receipt{}, err
		}
	}
	if request.Kind == KindSwitchCampus || request.Kind == KindSwitchHotspot || request.Kind == KindLogout {
		// An explicit network choice supersedes pending schedule transitions.
		// Cancellation retains the running worker's lock until it has stopped.
		var superseded []string
		for id, previous := range c.index {
			quiet := (previous.Request.Kind == KindQuietHotspot || previous.Request.Kind == KindQuietCampus) &&
				(request.Kind != KindLogout || previous.Request.AccountID == request.AccountID)
			maintenance := request.Kind == KindLogout && previous.Request.AccountID == request.AccountID && !previous.Request.Kind.Manual()
			if !previous.State.Terminal() && (quiet || maintenance) {
				superseded = append(superseded, id)
			}
		}
		for _, id := range superseded {
			_ = c.onCancel(id)
		}
	}

	c.nextID++
	action := &Action{
		ID:       c.instanceID + "-" + formatID(c.nextID),
		ordinal:  c.nextID,
		Request:  request,
		Line:     c.lines(request),
		State:    StateQueued,
		QueuedAt: c.clock.Now(),
	}
	c.index[action.ID] = action
	c.byKey[request.IdempotencyKey] = action.ID
	c.queue = append(c.queue, action)
	c.publish(action)
	return Receipt{ActionID: action.ID, State: StateQueued}, nil
}

// onCancel stops an action. Cancelling one that already finished is not an
// error: what the caller wanted is already true.
func (c *Coordinator) onCancel(actionID string) error {
	action, known := c.index[actionID]
	if !known {
		return domain.Errorf(domain.CodeNotFound, "没有编号为 %s 的动作", actionID)
	}
	if action.State.Terminal() {
		return nil
	}

	now := c.clock.Now()
	action.transition(StateCancelled, now)
	action.Message = "动作已取消"
	action.Code = domain.CodeCancelled

	if state, running := c.running[actionID]; running {
		// Cancel the worker but keep its line held until it actually stops.
		// Spec 04: a cancellation does not mean the gateway did not receive the
		// request. Freeing the line the instant the user clicks would let the
		// next action start authenticating while the old one is still unwinding
		// on the same uplink, which is exactly the race the per-line serial
		// rule exists to prevent.
		state.cancel()
		c.publish(action)
		return nil
	}

	c.removeFromQueue(actionID)
	action.Message = "动作在开始前被取消"
	c.retire(action)
	c.publish(action)
	return nil
}

// onFinish applies a worker's result and releases its line.
func (c *Coordinator) onFinish(done completion) {
	action, known := c.index[done.id]
	if !known {
		return
	}
	state, running := c.running[done.id]
	if !running {
		// This result has already been accounted for.
		return
	}
	action.Timings = append([]PhaseTiming(nil), done.timings...)
	action.WorkerMilliseconds = done.workerMilliseconds

	delete(c.running, done.id)
	if state.line != "" && c.busyLines[state.line] == done.id {
		delete(c.busyLines, state.line)
	}

	now := c.clock.Now()
	switch {
	case state.overdue && !action.State.Terminal():
		action.transition(StateFailed, now)
		action.Message = "动作超过总时限，已中止"
		action.Code = domain.CodeDeadlineExceeded
	case action.State.Terminal():
		// Cancelled or interrupted while running. The worker's own verdict does
		// not reopen a terminal state.
	default:
		outcome := done.outcome
		if outcome.State != StateSucceeded && outcome.State != StateFailed {
			// A Runner deciding an action was cancelled or interrupted would be
			// deciding something the coordinator owns.
			outcome = Outcome{State: StateFailed, Code: domain.CodeInternal,
				Message: "工作单元返回了无法解释的结果"}
		}
		if outcome.State == StateSucceeded && c.finalize != nil {
			outcome = c.finalize(*action, outcome)
		}
		if len(outcome.ResultJSON) > 32<<10 {
			outcome = Outcome{State: StateFailed, Code: domain.CodeInternal,
				Message: "任务结果超过大小上限"}
		}
		done.outcome = outcome
		action.transition(outcome.State, now)
		action.Message = outcome.Message
		action.Code = outcome.Code
		if outcome.State == StateSucceeded {
			action.ResultJSON = outcome.ResultJSON
		}
		action.MaintenanceDeferred = outcome.MaintenanceDeferred
	}

	// What the attempt learned travels whatever became of the action. A
	// cancelled login still found out whether the line had an address and whose
	// session was on it, and throwing that away because the user clicked stop
	// leaves the status page showing something older and less true. Whether the
	// observation is still current is observe's decision -- it holds the
	// revision, generation and sequence rules -- and deciding it a second time
	// here is how two answers to one question start to differ.
	if done.outcome.Observation != nil {
		c.record(*done.outcome.Observation)
	}

	c.retire(action)
	c.publish(action)
}

// onPhase records progress.
//
// A report can genuinely arrive after its action finished: the worker sends
// progress on a buffered channel and then returns, and the loop may take the
// completion first. Recording it then would put "challenge" back on top of a
// finished login.
func (c *Coordinator) onPhase(report phaseReport) {
	if !knownPhase(report.phase) {
		return
	}
	action, known := c.index[report.id]
	if !known || action.State.Terminal() {
		return
	}
	if _, running := c.running[report.id]; !running {
		return
	}
	action.Phase = report.phase
	c.publish(action)
}

// onList copies out every action still remembered, in the order a reader wants
// to see them: what is waiting, what is running, then what finished.
func (c *Coordinator) onList() []Action {
	out := make([]Action, 0, len(c.index))

	waiting := append([]*Action(nil), c.queue...)
	sort.Slice(waiting, func(i, j int) bool { return runsFirst(waiting[i], waiting[j]) })
	for _, action := range waiting {
		out = append(out, action.publicCopy())
	}

	active := make([]*Action, 0, len(c.running))
	for id := range c.running {
		if action, ok := c.index[id]; ok {
			active = append(active, action)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Sequence < active[j].Sequence })
	for _, action := range active {
		out = append(out, action.publicCopy())
	}

	for _, id := range c.terminals {
		if action, ok := c.index[id]; ok {
			out = append(out, action.publicCopy())
		}
	}
	return out
}

// dispatch starts as much queued work as the limits allow.
func (c *Coordinator) dispatch(ctx context.Context) {
	for len(c.running) < c.parallel {
		action := c.takeRunnable()
		if action == nil {
			return
		}
		// A switch can commit a new selection while other requests are queued.
		// Recheck before I/O, not only when the old page submitted the request.
		if c.check != nil {
			if err := c.check(action.Request); err != nil {
				action.transition(StateFailed, c.clock.Now())
				action.Code, _ = domain.CodeOf(err)
				action.Message = userMessage(err)
				c.retire(action)
				c.publish(action)
				continue
			}
		}
		c.startAction(ctx, action)
	}
}

// takeRunnable removes and returns the queued action that should run next, or
// nil when every candidate's line is occupied.
func (c *Coordinator) takeRunnable() *Action {
	for id := range c.running {
		if c.index[id].Request.Kind.switches() {
			return nil
		}
	}
	chosen := -1
	for index, action := range c.queue {
		// The cache is global even when callers choose different uplinks.
		// Keep the slot until a cancelled worker actually exits, like its line.
		if action.Request.Kind == KindPresetsRefresh && c.refreshRunning() {
			continue
		}
		if action.Line != "" && !action.Request.Kind.switches() {
			if _, busy := c.busyLines[action.Line]; busy {
				continue
			}
		}
		if chosen < 0 || runsFirst(action, c.queue[chosen]) {
			chosen = index
		}
	}
	if chosen < 0 {
		return nil
	}
	action := c.queue[chosen]
	// Drain existing workers before a switch. A pending high-priority switch
	// also stops new maintenance from continually filling those slots.
	if action.Request.Kind.switches() && len(c.running) != 0 {
		return nil
	}
	c.queue = append(c.queue[:chosen], c.queue[chosen+1:]...)
	return action
}

func (c *Coordinator) refreshRunning() bool {
	for id := range c.running {
		if c.index[id].Request.Kind == KindPresetsRefresh {
			return true
		}
	}
	return false
}

// runsFirst is the queue order: priority, then arrival, then submission order.
//
// Submission order rather than the id string, because "a10" sorts before "a2".
func runsFirst(a, b *Action) bool {
	if pa, pb := a.Request.Kind.Priority(), b.Request.Kind.Priority(); pa != pb {
		return pa > pb
	}
	if !a.QueuedAt.Equal(b.QueuedAt) {
		return a.QueuedAt.Before(b.QueuedAt)
	}
	return a.ordinal < b.ordinal
}

// startAction hands one action to a worker.
func (c *Coordinator) startAction(parent context.Context, action *Action) {
	c.nextSeq++
	action.Sequence = c.nextSeq
	action.transition(StateRunning, c.clock.Now())
	action.Phase = PhaseWaitingLink

	ctx, cancel := context.WithCancel(parent)
	c.running[action.ID] = &run{cancel: cancel, line: action.Line}
	if action.Line != "" {
		c.busyLines[action.Line] = action.ID
	}

	c.publish(action)

	// The worker gets a copy. Handing it the pointer would be a second writer.
	snapshot := *action
	c.workers.Go(func() {
		defer cancel()
		started := c.clock.Now()
		trace := phaseTrace{now: c.clock.Now}
		trace.change(PhaseWaitingLink)
		report := func(phase Phase) {
			if !knownPhase(phase) {
				return
			}
			trace.change(phase)
			select {
			case c.phases <- phaseReport{id: snapshot.ID, phase: phase}:
			case <-ctx.Done():
			case <-c.done:
			}
		}
		workerCtx := context.WithValue(ctx, phaseReporterKey{}, report)
		outcome := c.runner.Run(workerCtx, snapshot, report)
		timings := trace.finish()
		elapsed := c.clock.Now().Sub(started).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		select {
		case c.finish <- completion{id: snapshot.ID, outcome: outcome, timings: timings, workerMilliseconds: elapsed}:
		case <-c.done:
		}
	})
}

// expire fails anything that has waited too long and cancels anything that has
// run too long.
//
// The deadlines are measured against the injected clock rather than a
// context.WithTimeout so that a test can reach them without waiting for them.
// A 600-second budget cannot be tested by spending 600 seconds.
func (c *Coordinator) expire(now time.Time) {
	kept := c.queue[:0]
	for _, action := range c.queue {
		if now.Sub(action.QueuedAt) < c.queueWait {
			kept = append(kept, action)
			continue
		}
		action.transition(StateFailed, now)
		action.Message = "排队等待超过时限，动作未能开始"
		action.Code = domain.CodeDeadlineExceeded
		c.retire(action)
		c.publish(action)
	}
	c.queue = kept

	for id, state := range c.running {
		if state.overdue {
			continue
		}
		action, known := c.index[id]
		if !known || now.Sub(action.StartedAt) < c.actionBudget {
			continue
		}
		// Cancel now, record the reason, and let the completion arrive
		// normally: the line stays held until the worker really stops.
		state.overdue = true
		state.cancel()
	}
}

// nextDeadline is the instant the loop must wake at even if nothing happens.
//
// It is an instant rather than a wait so the timer cannot be armed from a stale
// reading of the clock. A deadline already in the past simply fires at once.
func (c *Coordinator) nextDeadline() (time.Time, bool) {
	var soonest time.Time
	consider := func(at time.Time) {
		if soonest.IsZero() || at.Before(soonest) {
			soonest = at
		}
	}
	for _, action := range c.queue {
		consider(action.QueuedAt.Add(c.queueWait))
	}
	for id, state := range c.running {
		if state.overdue {
			continue
		}
		if action, known := c.index[id]; known {
			consider(action.StartedAt.Add(c.actionBudget))
		}
	}
	return soonest, !soonest.IsZero()
}

// retire files a finished action in the bounded history.
//
// The idempotency key is forgotten with the action it belonged to. Keeping keys
// forever would be a slow leak on a device that runs for months; forgetting
// them earlier would let a retried submission start a second login.
func (c *Coordinator) retire(action *Action) {
	if action.retired || !action.State.Terminal() {
		return
	}
	action.retired = true
	action.Request.PrivateJSON = ""
	c.terminals = append(c.terminals, action.ID)

	for len(c.terminals) > c.history {
		oldest := c.terminals[0]
		c.terminals = c.terminals[1:]
		evicted, known := c.index[oldest]
		if !known {
			continue
		}
		if c.byKey[evicted.Request.IdempotencyKey] == oldest {
			delete(c.byKey, evicted.Request.IdempotencyKey)
		}
		delete(c.index, oldest)
	}
}

func (c *Coordinator) removeFromQueue(actionID string) {
	for index, action := range c.queue {
		if action.ID == actionID {
			c.queue = append(c.queue[:index], c.queue[index+1:]...)
			return
		}
	}
}
