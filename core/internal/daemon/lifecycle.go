package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

// DefaultInitScript is the procd service this project owns.
//
// The only service the helper will ever touch. Spec 02 requires the lifecycle
// helper to be a fixed command rather than a way to start an arbitrary one, and
// nothing reachable from an RPC parameter or a Lua caller reaches this value --
// the LuCI page runs `srunnet service ensure-running` with no arguments.
const DefaultInitScript = "/etc/init.d/smart_srun"

// StartupWait is how long ensure-running waits for the socket.
//
// Spec 02 fixes five seconds and forbids a retry loop. If procd cannot bring
// the service up in that time, starting it again will not help and would turn
// one user action into a restart storm; the honest answer is ServiceStopped and
// a message the user can act on.
const StartupWait = 5 * time.Second

// pollInterval is how often the socket is checked while waiting. Short enough
// that the common case -- procd starts the daemon in a few tens of
// milliseconds -- does not feel like a pause.
const pollInterval = 100 * time.Millisecond

// stopWait bounds the wait for the socket to disappear after a stop.
const stopWait = 10 * time.Second

// Lifecycle starts and stops the service from outside it.
//
// It is used by the CLI and, through the CLI, by the LuCI page. What it does
// not do is anything to the user's configuration: spec 02 is explicit that a
// force stop must not persist a change to the automatic-authentication switch,
// and the way to guarantee that is for this path never to open the file.
type Lifecycle struct {
	Paths Paths
	// InitScript defaults to DefaultInitScript.
	InitScript string
	Runner     commandRunner
	Clock      policy.Clock
}

// commandRunner is the one thing this helper needs from the adapter.
//
// Defined here, by the consumer, and narrow on purpose: it is what lets a test
// prove that a status poll never runs anything -- which is a requirement, not
// an implementation detail. With a concrete Runner the only way to check that
// would be to trust the code.
type commandRunner interface {
	Run(ctx context.Context, program string, args ...string) (openwrt.Result, error)
}

func (l Lifecycle) initScript() string {
	if l.InitScript != "" {
		return l.InitScript
	}
	return DefaultInitScript
}

func (l Lifecycle) clock() policy.Clock {
	if l.Clock != nil {
		return l.Clock
	}
	return policy.SystemClock{}
}

func (l Lifecycle) client() control.Client {
	return control.Client{Path: l.Paths.Socket()}
}

// Running reports whether the daemon answers.
//
// Asking it, rather than looking at the pid file or the socket's existence: a
// socket left behind by a killed daemon is still a socket, and a pid is still a
// number after the process it named has gone.
func (l Lifecycle) Running(ctx context.Context) bool {
	_, err := l.client().Call(ctx, "version.get", nil)
	return err == nil
}

// EnsureRunning starts the service if it is not already up, and waits, once,
// for it to answer.
//
// This is not "restart the daemon for every operation". It is the path spec 02
// requires so that a user who force-stopped the service can still press a
// button on the page they are looking at: the explicit action starts the
// service, waits, and then submits its own request.
func (l Lifecycle) EnsureRunning(ctx context.Context) error {
	if err := update.Guard(l.Paths.Update()); err != nil {
		if intentErr := update.RecordServiceIntent(l.Paths.Update(), true); intentErr != nil {
			return intentErr
		}
		return err
	}
	if l.Running(ctx) {
		return nil
	}

	script := l.initScript()
	if _, err := l.Runner.Run(ctx, script, "start"); err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法启动认证服务").Wrap(err)
	}

	clock := l.clock()
	deadline := clock.Now().Add(StartupWait)
	for {
		if l.Running(ctx) {
			return nil
		}
		now := clock.Now()
		if !now.Before(deadline) {
			return domain.Errorf(domain.CodeServiceStopped,
				"认证服务在 %s 内没有就绪", StartupWait)
		}
		next := now.Add(pollInterval)
		if next.After(deadline) {
			next = deadline
		}
		timer := clock.NewTimerAt(next)
		select {
		case <-timer.C():
		case <-ctx.Done():
			timer.Stop()
			return domain.Errorf(domain.CodeCancelled,
				"等待认证服务启动被取消").Wrap(ctx.Err())
		}
	}
}

// Stop asks the service to stand down, then stops it.
//
// Two steps, in this order, because they answer different questions. The RPC
// pass cancels this project's in-flight actions so a login that is halfway
// through is not killed mid-request; the init script then stops the process.
// Spec 02 requires the stop to be this project's service only -- the update
// worker runs under its own procd service precisely so that stopping this one
// cannot kill an install.
func (l Lifecycle) Stop(ctx context.Context) (StopReport, error) {
	intentErr := update.RecordServiceIntent(l.Paths.Update(), false)
	report := StopReport{AlreadyStopped: !l.Running(ctx)}
	if !report.AlreadyStopped {
		report.CancelledActions = l.cancelInFlight(ctx)
	}

	script := l.initScript()
	_, runErr := l.Runner.Run(ctx, script, "stop")
	if !l.settleStopped(ctx) {
		if runErr != nil {
			return report, domain.Errorf(domain.CodeServiceStopped,
				"无法停止认证服务").Wrap(runErr)
		}
		return report, domain.Errorf(domain.CodeServiceStopped,
			"认证服务在 %s 内没有停止", stopWait)
	}
	// A stop script that complained about a service which is now demonstrably
	// gone was complaining about it already being gone.

	return report, errors.Join(intentErr, l.recordStopped())
}

// StopReport is what a stop did, so the caller can say so rather than printing
// "done" whatever happened.
type StopReport struct {
	// AlreadyStopped means nothing was running when the stop was asked for.
	AlreadyStopped bool
	// CancelledActions is how many in-flight actions were cancelled first.
	// They stay cancelled: spec 02 forbids replaying them at the next start.
	CancelledActions int
}

// cancelInFlight asks the running daemon to cancel whatever it is doing.
//
// Best effort by design: if the daemon is already gone there is nothing to
// cancel, and a stop must not fail because of it. What matters is that a
// cancelled action stays cancelled -- spec 02 forbids replaying it at the next
// start, and the coordinator's terminal states are what guarantee that.
func (l Lifecycle) cancelInFlight(ctx context.Context) int {
	raw, err := l.client().Call(ctx, "status.get", nil)
	if err != nil {
		return 0
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return 0
	}

	cancelled := 0
	for _, action := range snapshot.Actions {
		switch action.State {
		case "queued", "running":
		default:
			continue
		}
		if _, err := l.client().Call(ctx, "action.cancel",
			ActionParams{ActionID: action.ID}); err == nil {
			cancelled++
		}
	}
	return cancelled
}

// settleStopped waits for the daemon to stop answering.
func (l Lifecycle) settleStopped(ctx context.Context) bool {
	clock := l.clock()
	deadline := clock.Now().Add(stopWait)
	for {
		if !l.Running(ctx) {
			return true
		}
		now := clock.Now()
		if !now.Before(deadline) {
			return false
		}
		next := now.Add(pollInterval)
		if next.After(deadline) {
			next = deadline
		}
		timer := clock.NewTimerAt(next)
		select {
		case <-timer.C():
		case <-ctx.Done():
			timer.Stop()
			return false
		}
	}
}

// lockWait bounds how long the helper waits for the daemon to let go.
//
// A stop script that has returned and a socket that no longer answers do not
// mean the process has finished its own shutdown: it closed its listener first
// and is still writing its last snapshot and releasing its lock. Failing
// immediately would report a conflict for a service that is two milliseconds
// from being gone.
const lockWait = 2 * time.Second

// recordStopped makes sure the state file says the service is stopped.
//
// In the ordinary case there is nothing to do: the daemon records its own stop
// on the way out, which is both the correct writer and the one with the
// information. This path exists for the daemon that was killed and never got
// there, and spec 02 allows exactly one other writer under exactly one
// condition -- the daemon has exited and this process holds the same exclusive
// lock. Taking that lock is the proof; a pid could not provide one.
//
// What is written preserves the previous snapshot's own fields. The user's
// automatic-authentication switch lives in the configuration and is not touched
// here at all: a force stop is not a change of mind about wanting to log in.
func (l Lifecycle) recordStopped() error {
	clock := l.clock()
	deadline := clock.Now().Add(lockWait)

	for {
		if snapshot, err := ReadSnapshot(l.Paths); err == nil &&
			snapshot.Service == ServiceStopped {
			return l.clearSocket()
		}
		if done, err := l.writeStoppedUnderLock(); done {
			if err != nil {
				return err
			}
			return l.clearSocket()
		}

		now := clock.Now()
		if !now.Before(deadline) {
			return domain.Errorf(domain.CodeConflict,
				"认证服务仍在运行，未记录停止状态")
		}
		next := now.Add(pollInterval)
		if next.After(deadline) {
			next = deadline
		}
		<-clock.NewTimerAt(next).C()
	}
}

// writeStoppedUnderLock reports whether it got the lock, and what happened if
// it did. Not getting it means the daemon is still there.
func (l Lifecycle) writeStoppedUnderLock() (bool, error) {
	lock, err := Acquire(l.Paths.Lock())
	if err != nil {
		return false, nil
	}
	defer lock.Release()

	previous, err := ReadSnapshot(l.Paths)
	if err != nil {
		// An unreadable or absent snapshot is not a reason to refuse: what a
		// caller needs from here is a file that says "stopped".
		previous = Snapshot{}
	}
	return true, MarkStopped(l.Paths, previous.Enabled, previous.ConfigRevision,
		previous.Version)
}

// clearSocket removes an endpoint with nothing behind it, so the next caller
// sees the service is down instead of waiting for a dial that will be refused.
func (l Lifecycle) clearSocket() error {
	if err := os.Remove(l.Paths.Socket()); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return domain.Errorf(domain.CodeInternal, "无法清理控制套接字").Wrap(err)
	}
	return nil
}

// Status is what a poll gets, and it never starts anything.
//
// Spec 02 requires a cold LuCI page and its status polling to show a stopped
// service rather than bring one up: a browser tab left open would otherwise
// keep the daemon alive forever, and a status request would have the side
// effect of changing the thing it is reporting on.
func (l Lifecycle) Status(ctx context.Context) (Snapshot, error) {
	raw, err := l.client().Call(ctx, "status.get", nil)
	if err == nil {
		var snapshot Snapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return Snapshot{}, domain.Errorf(domain.CodeProtocolInvalid,
				"状态响应格式无效").Wrap(err)
		}
		return snapshot, nil
	}
	if code, ok := domain.CodeOf(err); !ok || code != domain.CodeServiceStopped {
		return Snapshot{}, err
	}
	// Not running. The file is the answer, and a missing file is still an
	// answer: the service has not run since the last reboot.
	return ReadSnapshot(l.Paths)
}
