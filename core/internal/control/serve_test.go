//go:build unix

package control

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// patience bounds a wait on a real goroutine. Nothing reaches it on the happy
// path; it is there so a hang fails as a test instead of as a timeout.
const patience = 5 * time.Second

// served starts a listener and an accept loop on a temporary socket.
//
// wait is idempotent: the test body and the cleanup behind it both call it, and
// a plain channel read would make the second caller wait forever for a value
// the first one already took.
func served(t *testing.T, registry *Registry) (client Client,
	stop context.CancelFunc, wait func() error) {
	t.Helper()

	path := socketPathIn(t)
	listener, err := Listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, registry) }()

	wait = sync.OnceValue(func() error {
		select {
		case err := <-done:
			return err
		case <-time.After(patience):
			t.Error("Serve did not return after its context was cancelled")
			return nil
		}
	})
	t.Cleanup(func() {
		cancel()
		wait()
	})
	return Client{Path: path}, cancel, wait
}

func TestARequestIsAnsweredOverTheSocket(t *testing.T) {
	registry := NewRegistry()
	registry.Register("version.get", func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"version": "2.0.0rc1"}, nil
	})
	client, _, _ := served(t, registry)

	raw, err := client.Call(t.Context(), "version.get", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var result struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Version != "2.0.0rc1" {
		t.Errorf("version = %q", result.Version)
	}
}

// A failure comes back with its code intact, so a caller branches on the same
// value it would have got from a local call.
func TestAFailureKeepsItsCodeAcrossTheSocket(t *testing.T) {
	registry := NewRegistry()
	registry.Register("config.apply", func(context.Context, json.RawMessage) (any, error) {
		return nil, domain.Errorf(domain.CodeConflict, "配置已被别处修改")
	})
	client, _, _ := served(t, registry)

	_, err := client.Call(t.Context(), "config.apply", map[string]any{"x": 1})
	if err == nil {
		t.Fatal("a failing handler reported success")
	}
	code, ok := domain.CodeOf(err)
	if !ok || code != domain.CodeConflict {
		t.Errorf("code = %s/%v, want Conflict", code, ok)
	}
	if err.Error() == "" {
		t.Error("the failure carries no message")
	}
}

// A name that is not in the catalogue is refused, and a name that is in it but
// unbound is refused differently. One means "you made a typo", the other means
// "this build does not have that yet", and a client can act on the difference.
func TestUnknownAndUnimplementedAreDistinctOverTheSocket(t *testing.T) {
	client, _, _ := served(t, NewRegistry())

	_, err := client.Call(t.Context(), "no.such.method", nil)
	if code, _ := domain.CodeOf(err); code != domain.CodeNotFound {
		t.Errorf("unknown method gave %s, want NotFound", code)
	}

	_, err = client.Call(t.Context(), "status.get", nil)
	if code, _ := domain.CodeOf(err); code != domain.CodeUnsupportedCapability {
		t.Errorf("unbound method gave %s, want UnsupportedCapability", code)
	}
}

// T38 -- too many callers at once are told so rather than queued.
//
// The cap exists so a stuck or hostile local caller cannot open descriptors
// until the daemon runs out of them. Refusing with a reason matters as much as
// refusing: a caller that got a silent EOF cannot tell "too busy" from "the
// service died mid-request".
func TestTooManyConcurrentCallersAreRefusedWithAReason(t *testing.T) {
	entered := make(chan struct{}, MaxConcurrentConnections+1)
	release := make(chan struct{})

	registry := NewRegistry()
	// A catalogue name with a long budget: a read method would be cut off by
	// the response deadline before the test could fill the slots.
	registry.Register("presets.refresh", func(ctx context.Context, _ json.RawMessage) (any, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return map[string]any{"ok": true}, nil
	})
	client, _, _ := served(t, registry)
	// Released once, whichever path gets there first: an early t.Fatal must not
	// leave eight handlers blocked, and the happy path releases them itself.
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	var busy sync.WaitGroup
	for range MaxConcurrentConnections {
		busy.Go(func() {
			_, _ = client.Call(context.Background(), "presets.refresh", nil)
		})
	}
	// Every slot is occupied by a handler that is actually running, so the next
	// caller cannot be served by chance.
	for range MaxConcurrentConnections {
		select {
		case <-entered:
		case <-time.After(patience):
			t.Fatal("the handlers never all started")
		}
	}

	_, err := client.Call(t.Context(), "presets.refresh", nil)
	if err == nil {
		t.Fatal("a caller beyond the cap was served")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeBusy {
		t.Errorf("code = %s, want Busy", code)
	}

	// The same refusal, by a caller whose request is too large to sit in the
	// socket buffer. This is the version that does not depend on timing, and it
	// is the one that found the defect: with a small request the caller's write
	// usually lands before the refusal closes, so the answer survives and the
	// check above passes -- most of the time, and less often on a loaded
	// machine. With a request bigger than the buffer the write is still in
	// flight when the close arrives, and a refusal that closed without reading
	// reset the connection and destroyed the answer it had just written. The
	// caller then saw a failed write, which is the silent-EOF outcome this path
	// exists to prevent, and saw it only when the daemon really was busy.
	//
	// 400 KiB is comfortably past a Linux Unix-socket buffer (208 KiB) and
	// still inside the 512 KiB request limit, so the client sends it rather
	// than refusing it itself.
	blob := strings.Repeat("x", 400*1024)
	_, err = client.Call(t.Context(), "presets.refresh",
		map[string]string{"blob": blob})
	if err == nil {
		t.Fatal("a large caller beyond the cap was served")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeBusy {
		t.Errorf("large request refused with %s, want Busy", code)
	}

	releaseOnce()
	busy.Wait()
	// Reopened once the slots are free: the cap is backpressure, not a fuse.
	if _, err := client.Call(t.Context(), "presets.refresh", nil); err != nil {
		t.Errorf("a caller after the burst was refused too: %v", err)
	}
}

// Stopping is a stop, not a failure: procd would log an ordinary SIGTERM as a
// crash if this returned its context's error.
func TestServeReturnsCleanlyWhenStopped(t *testing.T) {
	_, stop, wait := served(t, NewRegistry())
	stop()
	if err := wait(); err != nil {
		t.Errorf("a clean stop returned %v", err)
	}
}

// Serve waits for its handlers. A stop that returned while one was still
// writing would leave the caller reading a socket nobody was tracking.
//
// The handler here ignores its cancellation and waits for the test instead,
// which is what makes the check decisive: if Serve did not wait, it would
// return immediately, and the only way to say so is to watch it not return
// while the handler is definitely still there.
func TestServeWaitsForItsHandlers(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	registry := NewRegistry()
	registry.Register("presets.refresh", func(context.Context, json.RawMessage) (any, error) {
		close(entered)
		<-release
		return map[string]any{"ok": true}, nil
	})
	client, stop, wait := served(t, registry)

	go func() { _, _ = client.Call(context.Background(), "presets.refresh", nil) }()
	select {
	case <-entered:
	case <-time.After(patience):
		t.Fatal("the handler never started")
	}

	returned := make(chan error, 1)
	go func() { returned <- wait() }()

	stop()
	select {
	case <-returned:
		t.Fatal("Serve returned while a handler was still running")
	case <-time.After(200 * time.Millisecond):
		// Still waiting, which is the point. Serve takes microseconds to
		// return once it stops waiting, so this bound is not a race.
	}

	releaseOnce()
	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("Serve returned %v after its handler finished", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve never returned after its handler finished")
	}
}

// A connection whose caller cannot be identified is refused, not served.
//
// Failing closed is the only defensible answer: the socket carries commands
// that change a router's network configuration, and "probably fine" is not an
// identity check.
func TestAConnectionWithNoVerifiableCallerIsRefused(t *testing.T) {
	// net.Pipe is an in-memory connection with no kernel credentials behind it,
	// which is exactly the case the check has to refuse.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if err := verifyPeer(server); err == nil {
		t.Fatal("a connection with no verifiable caller was accepted")
	}
}

// T38 -- the caller's identity comes from the kernel.
//
// The 0700 directory already makes the socket unreachable for anybody else, so
// this is the second of two independent checks. It is worth having because the
// first depends on a mode an installer or a backup restore can change.
func TestOnlyRootAndTheDaemonsOwnUserAreAccepted(t *testing.T) {
	if !allowedPeer(0) {
		t.Error("root was refused")
	}
	if !allowedPeer(uint32(os.Getuid())) {
		t.Error("the daemon's own user was refused")
	}

	// Some uid that is neither. 65534 is nobody on every distribution this
	// runs on; if the tests happen to be running as it, use another.
	stranger := uint32(65534)
	if stranger == uint32(os.Getuid()) {
		stranger = 12345
	}
	if allowedPeer(stranger) {
		t.Errorf("uid %d was accepted; the socket is root-only", stranger)
	}
}
