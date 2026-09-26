//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

// patience bounds a wait on a real goroutine. Nothing reaches it on the happy
// path; it is there so a hang fails as a test rather than as a timeout.
const patience = 5 * time.Second

// running is a service on temporary paths, already answering.
type running struct {
	t      *testing.T
	paths  Paths
	client control.Client
	stop   context.CancelFunc
	wait   func() error

	actions chan application.Action
}

func start(t *testing.T, configure func(*Options)) *running {
	t.Helper()

	paths := tempPaths(t)
	service := &running{t: t, paths: paths,
		client:  control.Client{Path: paths.Socket()},
		actions: make(chan application.Action, 64)}

	ready := make(chan struct{})
	options := Options{
		Paths:   paths,
		Version: "2.0.0rc1",
		Ready:   sync.OnceFunc(func() { close(ready) }),
		// 08:00 Beijing is outside default quiet hours and before the 09:00
		// preset refresh, so background actions cannot race RPC/log fixtures.
		// Scheduling tests can override this clock explicitly.
		Clock: faketime.New(time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)),
		Observer: func(action application.Action) {
			select {
			case service.actions <- action:
			default:
				t.Errorf("the action event buffer overflowed at %s", action.ID)
			}
		},
		OnError: func(err error) { t.Errorf("daemon: %v", err) },
	}
	if configure != nil {
		configure(&options)
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()

	service.stop = stop
	service.wait = sync.OnceValue(func() error {
		select {
		case err := <-done:
			return err
		case <-time.After(patience):
			t.Error("the daemon did not stop")
			return nil
		}
	})
	t.Cleanup(func() {
		stop()
		service.wait()
	})

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("the daemon stopped before it was ready: %v", err)
	case <-time.After(patience):
		t.Fatal("the daemon never became ready")
	}
	return service
}

func (r *running) call(method string, params any) json.RawMessage {
	r.t.Helper()
	raw, err := r.client.Call(r.t.Context(), method, params)
	if err != nil {
		r.t.Fatalf("%s: %v", method, err)
	}
	return raw
}

func (r *running) callExpectingError(method string, params any) error {
	r.t.Helper()
	_, err := r.client.Call(r.t.Context(), method, params)
	if err == nil {
		r.t.Fatalf("%s was accepted", method)
	}
	return err
}

func (r *running) status() Snapshot {
	r.t.Helper()
	var snapshot Snapshot
	if err := json.Unmarshal(r.call("status.get", nil), &snapshot); err != nil {
		r.t.Fatalf("decode status: %v", err)
	}
	return snapshot
}

// awaitAction waits for an action to be published in a terminal state.
func (r *running) awaitTerminal(actionID string) application.Action {
	r.t.Helper()
	deadline := time.After(patience)
	for {
		select {
		case action := <-r.actions:
			if action.ID == actionID && action.State.Terminal() {
				return action
			}
		case <-deadline:
			r.t.Fatalf("%s never finished", actionID)
			return application.Action{}
		}
	}
}

// T38 -- the service answers over the real socket.
func TestTheServiceAnswersOverItsSocket(t *testing.T) {
	service := start(t, nil)

	var version VersionResult
	if err := json.Unmarshal(service.call("version.get", nil), &version); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if version.Version != "2.0.0rc1" || version.RPCVersion != control.Version {
		t.Errorf("version = %+v", version)
	}

	status := service.status()
	if status.SchemaVersion != SnapshotSchemaVersion || status.WrittenAt.IsZero() {
		t.Errorf("live snapshot has no valid schema/timestamp: %+v", status)
	}
	if status.Service != ServiceRunning {
		t.Errorf("service = %q, want running", status.Service)
	}
	if status.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", status.PID, os.Getpid())
	}
	if status.Version != "2.0.0rc1" {
		t.Errorf("version = %q", status.Version)
	}

	// A declared method this build does not implement is refused as
	// unimplemented, not answered with an empty result.
	err := service.callExpectingError("school.command", nil)
	if code := codeOf(t, err); code != domain.CodeUnsupportedCapability {
		t.Errorf("code = %s, want UnsupportedCapability", code)
	}
}

// T25 -- a second daemon on the same runtime directory refuses to start.
func TestASecondDaemonOnTheSamePathsRefusesToStart(t *testing.T) {
	service := start(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	err := Run(ctx, Options{Paths: service.paths, Version: "2.0.0rc1"})
	if err == nil {
		t.Fatal("a second daemon started")
	}
	if code := codeOf(t, err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}
	// And the first one is unharmed.
	if service.status().Service != ServiceRunning {
		t.Error("the first daemon stopped answering")
	}
}

// An action submitted over the socket goes all the way through the state
// machine and comes back with the reason it failed.
//
// The reason used to be "this build has no authentication worker". It now has
// one, and this service was started on an empty configuration, so the reason is
// that the account does not exist -- which is the more interesting answer,
// because it means the real worker ran and read the real configuration. What
// the test is checking either way is that queued, running and a terminal state
// all happen and are all visible: reporting success without doing the work is
// the failure worth catching.
func TestAnActionRunsAndReportsWhyItFailed(t *testing.T) {
	service := start(t, nil)

	var receipt SubmitResult
	raw := service.call("action.submit", SubmitParams{
		Kind: "manual_login", AccountID: "campus", IdempotencyKey: "click-1"})
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if receipt.ActionID == "" || receipt.State != "queued" {
		t.Fatalf("receipt = %+v", receipt)
	}

	finished := service.awaitTerminal(receipt.ActionID)
	if finished.State != application.StateFailed {
		t.Errorf("state = %s, want failed", finished.State)
	}
	if finished.Code != domain.CodeNotFound {
		t.Errorf("code = %s, want NotFound for an account this service does not have",
			finished.Code)
	}

	var view ActionView
	if err := json.Unmarshal(service.call("action.get",
		ActionParams{ActionID: receipt.ActionID}), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.State != "failed" || view.Message == "" {
		t.Errorf("view = %+v", view)
	}

	// The same key returns the same action rather than starting a second one.
	var again SubmitResult
	if err := json.Unmarshal(service.call("action.submit", SubmitParams{
		Kind: "manual_login", AccountID: "campus", IdempotencyKey: "click-1"}),
		&again); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if again.ActionID != receipt.ActionID || !again.Duplicate {
		t.Errorf("resubmission = %+v, want the same action", again)
	}
}

// The scheduler's own work cannot be submitted from outside.
//
// A caller that could queue maintenance would be queueing work the maintenance
// loop did not decide to do, and one that could raise a quiet-hours sweep could
// log every managed account out at any time of day.
func TestSchedulerOnlyActionsAreRefused(t *testing.T) {
	service := start(t, nil)

	for _, kind := range []string{"maintain", "forced_logout"} {
		err := service.callExpectingError("action.submit", SubmitParams{
			Kind: kind, AccountID: "campus", IdempotencyKey: "sneaky-" + kind})
		if code := codeOf(t, err); code != domain.CodeInvalidArgument {
			t.Errorf("%s gave %s, want InvalidArgument", kind, code)
		}
	}
}

// An action submitted against a configuration the caller has not seen is
// refused. A login sent from a page that was still showing the previous
// account's settings is not the login the user meant.
func TestAnActionAgainstAStaleRevisionIsRefused(t *testing.T) {
	service := start(t, nil)
	current := service.status().ConfigRevision

	stale := current + 1
	err := service.callExpectingError("action.submit", SubmitParams{
		Kind: "manual_login", AccountID: "campus", IdempotencyKey: "click-1",
		ExpectedRevision: &stale})
	if code := codeOf(t, err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}

	// The matching revision goes through.
	if raw := service.call("action.submit", SubmitParams{
		Kind: "manual_login", AccountID: "campus", IdempotencyKey: "click-2",
		ExpectedRevision: &current}); len(raw) == 0 {
		t.Error("an action against the current revision was not accepted")
	}
}

// config.get fills the settings page, and the settings page does not display
// passwords.
func TestConfigGetCarriesNoSecret(t *testing.T) {
	service := start(t, nil)

	// The defaults have no accounts, so this checks the shape rather than a
	// particular secret; the redaction itself is checked below against a
	// configuration that has one.
	raw := service.call("config.get", nil)
	var cfg domain.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.SchemaVersion != domain.ConfigSchemaVersion {
		t.Errorf("schema version = %d", cfg.SchemaVersion)
	}

	withSecret := domain.Config{
		CampusAccounts: []domain.CampusAccount{{
			ID: "c1", Password: "sup3rsecret", Key: "wifikey1234",
			AccessMode: domain.AccessModeWiFi,
		}},
		HotspotProfiles: []domain.HotspotProfile{{ID: "h1", Key: "hotspotkey"}},
	}
	cleaned := redact(withSecret)
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"sup3rsecret", "wifikey1234", "hotspotkey"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("config.get carries %q", secret)
		}
	}
	// And redacting did not reach back into the caller's configuration.
	if withSecret.CampusAccounts[0].Password != "sup3rsecret" {
		t.Error("redacting erased the secret in the original configuration")
	}
}

// config.validate answers about a candidate without touching the stored one,
// and a bad candidate comes back as a failure rather than as a result field
// somebody has to remember to check.
func TestConfigValidateJudgesACandidate(t *testing.T) {
	service := start(t, nil)

	var result ValidateResult
	if err := json.Unmarshal(service.call("config.validate", nil), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Valid {
		t.Error("the stored configuration was reported invalid")
	}

	err := service.callExpectingError("config.validate", ValidateParams{
		Config: json.RawMessage(`{"schema_version":2,"nonsense":true}`)})
	if code := codeOf(t, err); code != domain.CodeInvalidConfig {
		t.Errorf("code = %s, want InvalidConfig", code)
	}
}

// schema.get is the one source of field contracts and default values.
//
// Spec 02 requires Lua and Go to share it rather than each keeping a copy; a
// second copy is how a default drifts and a form starts saving something the
// daemon then refuses.
func TestSchemaGetServesTheSharedFieldContract(t *testing.T) {
	service := start(t, nil)

	raw := service.call("schema.get", nil)

	var schema config.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(schema.Global) == 0 || len(schema.CampusAccount) == 0 {
		t.Fatalf("schema = %+v, want the field groups", schema)
	}

	// Compared as bytes rather than as values: a Field's default is `any`, so a
	// number that went out as an integer comes back as a float64 and a
	// structural comparison would fail for a reason nobody cares about. What
	// matters is that the document on the wire is the document the CLI prints.
	want, err := json.Marshal(config.BuildSchema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != string(want) {
		t.Error("the schema on the wire is not the one the CLI prints; two " +
			"copies of a default is how a form saves what the daemon refuses")
	}
}

// T25 -- stopping leaves a readable record saying the service is stopped, and
// nothing still listening.
func TestStoppingLeavesAStoppedRecordAndNoSocket(t *testing.T) {
	service := start(t, nil)

	if _, err := os.Stat(service.paths.Socket()); err != nil {
		t.Fatalf("the socket is missing while the service runs: %v", err)
	}
	before := service.status()

	service.stop()
	if err := service.wait(); err != nil {
		t.Errorf("a clean stop returned %v", err)
	}

	snapshot, err := ReadSnapshot(service.paths)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snapshot.Service != ServiceStopped {
		t.Errorf("service = %q, want stopped", snapshot.Service)
	}
	if snapshot.Enabled != before.Enabled {
		t.Errorf("enabled = %v, want it unchanged at %v",
			snapshot.Enabled, before.Enabled)
	}
	if snapshot.ConfigRevision != before.ConfigRevision {
		t.Errorf("revision = %d, want %d",
			snapshot.ConfigRevision, before.ConfigRevision)
	}
	if _, err := os.Stat(service.paths.Socket()); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the service: %v", err)
	}

	// And the lock is free, so the next start is not blocked by the last stop.
	lock, err := Acquire(service.paths.Lock())
	if err != nil {
		t.Fatalf("the lock outlived the service: %v", err)
	}
	lock.Release()
}

// T25 -- an action still in flight when the service stops is interrupted, not
// failed, and not replayed.
func TestStoppingInterruptsAnActionInFlight(t *testing.T) {
	held := make(chan struct{})
	// Buffered: the worker starts before the test reaches its receive, and an
	// unbuffered non-blocking send would simply be dropped.
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

	service.stop()
	if err := service.wait(); err != nil {
		t.Errorf("stop returned %v", err)
	}

	finished := service.awaitTerminal(receipt.ActionID)
	if finished.State != application.StateInterrupted {
		t.Errorf("state = %s, want interrupted -- a force-stopped action must "+
			"be distinguishable from a failed one so it is not replayed",
			finished.State)
	}
}

// blockingRunner holds an action until the test lets it go, so a stop can
// arrive while something is genuinely in flight.
type blockingRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r blockingRunner) Run(ctx context.Context, _ application.Action,
	_ func(application.Phase)) application.Outcome {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
	case <-ctx.Done():
	}
	return application.Outcome{State: application.StateSucceeded, Message: "太晚了"}
}
