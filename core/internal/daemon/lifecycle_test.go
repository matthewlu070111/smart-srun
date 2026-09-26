//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

// recordingRunner stands in for the init script.
//
// What the tests need from it is not that it starts anything but that they can
// see exactly what was run and how often. "A status poll does not start the
// service" is a requirement; with a concrete Runner the only way to check it
// would be to read the code and believe it.
type recordingRunner struct {
	mu    sync.Mutex
	calls [][]string
	// onStart runs when the script is asked to start, so a test can decide
	// whether the service actually comes up.
	onStart func()
	// onStop likewise.
	onStop func()
	err    error
}

func (r *recordingRunner) Run(_ context.Context, program string,
	args ...string) (openwrt.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{program}, args...))
	onStart, onStop := r.onStart, r.onStop
	err := r.err
	r.mu.Unlock()

	switch {
	case len(args) > 0 && args[0] == "start" && onStart != nil:
		onStart()
	case len(args) > 0 && args[0] == "stop" && onStop != nil:
		onStop()
	}
	return openwrt.Result{Program: program}, err
}

func (r *recordingRunner) invocations() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

func (r *recordingRunner) count(verb string) int {
	seen := 0
	for _, call := range r.invocations() {
		if len(call) > 1 && call[1] == verb {
			seen++
		}
	}
	return seen
}

// drive keeps a fake clock moving while something waits on it, so a bounded
// wait finishes in test time rather than in real time. The returned function
// stops the driver; without a way out it would block forever on the last
// BlockUntil, because nothing tells it the waiter has stopped waiting.
func drive(clock *faketime.Clock) func() {
	ctx, cancel := context.WithCancel(context.Background())
	var driver sync.WaitGroup
	driver.Go(func() {
		for {
			if err := clock.BlockUntilContext(ctx, 1); err != nil {
				return
			}
			clock.Advance(pollInterval)
		}
	})
	return func() {
		cancel()
		driver.Wait()
	}
}

// T25 -- a status poll never starts the service.
//
// A browser tab left open polls this every few seconds. If polling started the
// daemon, a user who stopped it would find it running again, and a status
// request would be changing the thing it reports on.
func TestAStatusPollNeverStartsTheService(t *testing.T) {
	paths := tempPaths(t)
	if err := MarkStopped(paths, true, 7, "2.0.0rc1"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	runner := &recordingRunner{}
	lifecycle := Lifecycle{Paths: paths, Runner: runner}

	for range 3 {
		snapshot, err := lifecycle.Status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if snapshot.Service != ServiceStopped {
			t.Errorf("service = %q, want stopped", snapshot.Service)
		}
		if !snapshot.Enabled || snapshot.ConfigRevision != 7 {
			t.Errorf("the stopped record lost its contents: %+v", snapshot)
		}
	}
	if calls := runner.invocations(); len(calls) != 0 {
		t.Errorf("a status poll ran %v", calls)
	}
}

// A service that has never run has no snapshot, and that is still an answer.
func TestStatusWithNoRecordAtAllSaysStopped(t *testing.T) {
	runner := &recordingRunner{}
	lifecycle := Lifecycle{Paths: tempPaths(t), Runner: runner}

	snapshot, err := lifecycle.Status(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if snapshot.Service != ServiceStopped {
		t.Errorf("service = %q", snapshot.Service)
	}
	if len(runner.invocations()) != 0 {
		t.Error("reading a missing snapshot ran something")
	}
}

// The running service answers, and its own snapshot is what comes back.
func TestStatusPrefersTheRunningService(t *testing.T) {
	service := start(t, nil)
	runner := &recordingRunner{}
	lifecycle := Lifecycle{Paths: service.paths, Runner: runner}

	snapshot, err := lifecycle.Status(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if snapshot.Service != ServiceRunning || snapshot.PID != os.Getpid() {
		t.Errorf("snapshot = %+v", snapshot)
	}
	if len(runner.invocations()) != 0 {
		t.Error("reading a running service's status ran something")
	}
}

// Ensuring a service that is already up runs nothing. Spec 02 is explicit that
// this is not "restart the daemon for every operation".
func TestEnsureRunningDoesNothingWhenTheServiceIsUp(t *testing.T) {
	service := start(t, nil)
	runner := &recordingRunner{}
	lifecycle := Lifecycle{Paths: service.paths, Runner: runner}

	if err := lifecycle.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("ensure-running: %v", err)
	}
	if calls := runner.invocations(); len(calls) != 0 {
		t.Errorf("a running service was started again: %v", calls)
	}
}

// T25 -- ensure-running starts the service once, waits, and gives up.
//
// Once, not in a loop: spec 02 forbids the loop because a service that cannot
// come up will not come up on the second try either, and turning one user
// action into a restart storm makes the router worse, not better.
func TestEnsureRunningStartsOnceAndThenGivesUp(t *testing.T) {
	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	stopDriver := drive(clock)
	defer stopDriver()

	// A start that does not bring anything up, which is the case being tested.
	runner := &recordingRunner{}
	lifecycle := Lifecycle{Paths: tempPaths(t), Runner: runner, Clock: clock}

	err := lifecycle.EnsureRunning(t.Context())
	if err == nil {
		t.Fatal("ensure-running reported success with nothing listening")
	}
	if code := codeOf(t, err); code != domain.CodeServiceStopped {
		t.Errorf("code = %s, want ServiceStopped", code)
	}
	if got := runner.count("start"); got != 1 {
		t.Errorf("the script was started %d times, want exactly 1", got)
	}
	if calls := runner.invocations(); len(calls) != 1 ||
		calls[0][0] != DefaultInitScript {
		t.Errorf("ran %v, want only %s start", calls, DefaultInitScript)
	}
}

// And when the start works, ensure-running waits for the socket and returns.
func TestEnsureRunningWaitsForTheServiceToAnswer(t *testing.T) {
	paths := tempPaths(t)
	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	stopDriver := drive(clock)
	defer stopDriver()

	ready := make(chan struct{})
	done := make(chan error, 1)
	ctx, stopService := context.WithCancel(context.Background())
	defer func() {
		stopService()
		select {
		case <-done:
		case <-time.After(patience):
			t.Error("the started service did not stop")
		}
	}()

	runner := &recordingRunner{onStart: func() {
		go func() {
			done <- Run(ctx, Options{Paths: paths, Version: "2.0.0rc1",
				Ready: sync.OnceFunc(func() { close(ready) })})
		}()
		// The init script returns once procd has the service; waiting for the
		// socket here is what makes the test's timing deterministic rather than
		// a race between a fake clock and a real bind.
		select {
		case <-ready:
		case <-time.After(patience):
		}
	}}

	lifecycle := Lifecycle{Paths: paths, Runner: runner, Clock: clock}
	if err := lifecycle.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("ensure-running: %v", err)
	}
	if got := runner.count("start"); got != 1 {
		t.Errorf("started %d times, want 1", got)
	}
}

// T25 -- stopping cancels what is in flight first, then stops the service, and
// leaves a record that says so.
func TestStopCancelsInFlightWorkThenStops(t *testing.T) {
	held := make(chan struct{})
	entered := make(chan struct{}, 1)
	service := start(t, func(options *Options) {
		options.Runner = blockingRunner{entered: entered, release: held}
	})
	defer close(held)

	var receipt SubmitResult
	if err := json.Unmarshal(service.call("action.submit", SubmitParams{
		Kind: "manual_login", AccountID: "campus", IdempotencyKey: "click-1"}),
		&receipt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(patience):
		t.Fatal("the action never started")
	}

	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	stopDriver := drive(clock)
	defer stopDriver()

	runner := &recordingRunner{onStop: func() {
		service.stop()
		service.wait()
	}}
	lifecycle := Lifecycle{Paths: service.paths, Runner: runner, Clock: clock}

	report, err := lifecycle.Stop(t.Context())
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if report.AlreadyStopped {
		t.Error("a running service was reported as already stopped")
	}
	if report.CancelledActions != 1 {
		t.Errorf("cancelled %d actions, want 1", report.CancelledActions)
	}
	if got := runner.count("stop"); got != 1 {
		t.Errorf("the script was stopped %d times, want 1", got)
	}

	snapshot, err := ReadSnapshot(service.paths)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snapshot.Service != ServiceStopped {
		t.Errorf("service = %q", snapshot.Service)
	}
	if _, err := os.Stat(service.paths.Socket()); !os.IsNotExist(err) {
		t.Errorf("the socket survived the stop: %v", err)
	}

	// The action the user cancelled stays cancelled. Spec 02 forbids replaying
	// it at the next start, and that depends on it not being relabelled here.
	final := service.awaitTerminal(receipt.ActionID)
	if final.State != application.StateCancelled {
		t.Errorf("state = %s, want cancelled", final.State)
	}
}

// Stopping something that is not running is not an error, and it still leaves a
// record saying so -- including the user's switch, untouched.
func TestStoppingSomethingThatIsNotRunningIsNotAnError(t *testing.T) {
	paths := tempPaths(t)
	if err := WriteSnapshot(paths, Snapshot{Service: ServiceRunning, PID: 999999,
		Enabled: true, ConfigRevision: 4, Version: "2.0.0rc1"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	stopDriver := drive(clock)
	defer stopDriver()

	runner := &recordingRunner{}
	lifecycle := Lifecycle{Paths: paths, Runner: runner, Clock: clock}

	// A socket file a killed daemon left behind. Nothing unlinked it, because
	// SIGKILL closes nothing, and a caller that dials it waits for a connection
	// that will never be accepted instead of seeing a stopped service.
	if err := os.MkdirAll(paths.Runtime, RuntimeDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale, err := net.Listen("unix", paths.Socket())
	if err != nil {
		t.Fatalf("create a stale socket: %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if _, err := os.Stat(paths.Socket()); err != nil {
		t.Fatalf("the stale socket was not left in place: %v", err)
	}

	report, err := lifecycle.Stop(t.Context())
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !report.AlreadyStopped || report.CancelledActions != 0 {
		t.Errorf("report = %+v", report)
	}
	if _, err := os.Stat(paths.Socket()); !os.IsNotExist(err) {
		t.Errorf("the stale socket survived the stop: %v", err)
	}
	// The script is still run: a snapshot that says "running" after a crash is
	// exactly when procd may still have something to stop.
	if got := runner.count("stop"); got != 1 {
		t.Errorf("stopped %d times, want 1", got)
	}

	snapshot, err := ReadSnapshot(paths)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if snapshot.Service != ServiceStopped {
		t.Errorf("service = %q", snapshot.Service)
	}
	if !snapshot.Enabled {
		t.Error("stopping switched automatic authentication off")
	}
	if snapshot.ConfigRevision != 4 || snapshot.Version != "2.0.0rc1" {
		t.Errorf("the record lost what the daemon had published: %+v", snapshot)
	}
}

// A service that will not stop is reported rather than declared stopped.
//
// Writing "stopped" over something that is still authenticating would make the
// interface lie about the one thing a user opened it to check, and would leave
// the next start fighting the copy that never went away.
func TestAServiceThatRefusesToStopIsReported(t *testing.T) {
	service := start(t, nil)

	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	stopDriver := drive(clock)
	defer stopDriver()

	// A stop script that does nothing at all, which is what a wedged procd or a
	// daemon ignoring SIGTERM looks like from here.
	runner := &recordingRunner{}
	lifecycle := Lifecycle{Paths: service.paths, Runner: runner, Clock: clock}

	_, err := lifecycle.Stop(t.Context())
	if err == nil {
		t.Fatal("a service that never stopped was reported as stopped")
	}
	if code := codeOf(t, err); code != domain.CodeServiceStopped {
		t.Errorf("code = %s, want ServiceStopped", code)
	}
	if got := runner.count("stop"); got != 1 {
		t.Errorf("stopped %d times, want exactly 1 -- retrying a stop that did "+
			"nothing will not make it work", got)
	}

	// And the record still says running, because it is.
	snapshot, err := ReadSnapshot(service.paths)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if snapshot.Service != ServiceRunning {
		t.Errorf("service = %q, want it left alone", snapshot.Service)
	}
}

// The stopped record is not written while the daemon still holds its lock.
//
// The lock is the proof that it has gone. Writing "stopped" over a service that
// is still running would make the interface lie about the one thing a user
// looks at it for.
func TestTheStoppedRecordIsRefusedWhileTheLockIsHeld(t *testing.T) {
	paths := tempPaths(t)
	if err := EnsureRuntimeDir(paths); err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	if err := WriteSnapshot(paths, Snapshot{Service: ServiceRunning,
		PID: 1234, Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	held, err := Acquire(paths.Lock())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.Release()

	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	stopDriver := drive(clock)
	defer stopDriver()

	lifecycle := Lifecycle{Paths: paths, Runner: &recordingRunner{}, Clock: clock}
	if err := lifecycle.recordStopped(); err == nil {
		t.Fatal("the record was written while the lock was held")
	} else if code := codeOf(t, err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}

	snapshot, err := ReadSnapshot(paths)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if snapshot.Service != ServiceRunning {
		t.Errorf("service = %q, want it left alone", snapshot.Service)
	}
}
