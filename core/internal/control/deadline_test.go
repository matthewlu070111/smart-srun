package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// timedStream is a stream that records the deadlines set on it, the way a
// net.Conn does.
type timedStream struct {
	*rwStream
	readDeadline  time.Time
	writeDeadline time.Time
}

func (s *timedStream) SetReadDeadline(at time.Time) error  { s.readDeadline = at; return nil }
func (s *timedStream) SetWriteDeadline(at time.Time) error { s.writeDeadline = at; return nil }

func newTimedStream(request string) *timedStream {
	return &timedStream{rwStream: newStream(request)}
}

func noopHandler(context.Context, json.RawMessage) (any, error) { return nil, nil }

// A peer that connects and then stops sending must not be able to hold the
// connection open for as long as it likes.
func TestServeConnBoundsTheWaitForARequest(t *testing.T) {
	stream := newTimedStream(
		`{"rpc_version":1,"request_id":"r","method":"version.get"}` + "\n")
	registry := registryWith(t, "version.get", noopHandler)

	before := time.Now()
	if err := ServeConn(t.Context(), registry, stream); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}

	if stream.readDeadline.IsZero() {
		t.Fatal("no read deadline was set; a peer that stalls mid-frame would " +
			"hold a daemon connection open indefinitely")
	}
	// Measured from before the call, so the budget is the contract's five
	// seconds plus however long the call took to reach the SetReadDeadline.
	budget := stream.readDeadline.Sub(before)
	if budget < RequestReadDeadline || budget > RequestReadDeadline+time.Second {
		t.Fatalf("read budget %v, want about %v", budget, RequestReadDeadline)
	}
}

// The response of a read is bounded too. Without it a handler that hangs would
// keep the browser's poll open past the budget the read was given.
func TestServeConnBoundsTheWriteOfAReadResponse(t *testing.T) {
	stream := newTimedStream(
		`{"rpc_version":1,"request_id":"r","method":"status.get"}` + "\n")
	registry := registryWith(t, "status.get", noopHandler)

	before := time.Now()
	if err := ServeConn(t.Context(), registry, stream); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}

	if stream.writeDeadline.IsZero() {
		t.Fatal("no write deadline was set for a read response")
	}
	// The response budget starts once the request is understood, so measured
	// from before the call it is three seconds plus the instant the read took.
	budget := stream.writeDeadline.Sub(before)
	if budget < ReadResponseDeadline || budget > ReadResponseDeadline+time.Second {
		t.Fatalf("write budget %v, want about %v", budget, ReadResponseDeadline)
	}
	if budget >= RequestReadDeadline {
		t.Fatalf("write budget %v is the request read budget reused, not the "+
			"response budget", budget)
	}
}

// The in-process paths use a pipe, not a socket. A stream that cannot express a
// deadline is served without one rather than refused.
func TestAStreamWithoutDeadlinesIsStillServed(t *testing.T) {
	registry := registryWith(t, "version.get", noopHandler)
	stream := newStream(`{"rpc_version":1,"request_id":"r","method":"version.get"}` + "\n")

	if err := ServeConn(t.Context(), registry, stream); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	if !stream.response(t).OK {
		t.Fatal("a stream without deadline support was refused")
	}
}

// Reads are what the page polls. Bounding them in the dispatcher rather than in
// each handler means a read added later cannot forget to be bounded.
func TestReadMethodsCarryTheResponseBudget(t *testing.T) {
	var budget time.Duration
	var bounded bool
	registry := registryWith(t, "status.get",
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			deadline, ok := ctx.Deadline()
			bounded = ok
			if ok {
				budget = time.Until(deadline)
			}
			return nil, nil
		})

	registry.Dispatch(t.Context(),
		Request{RPCVersion: Version, RequestID: "r", Method: "status.get"})

	if !bounded {
		t.Fatal("a read handler was given no deadline")
	}
	if budget <= 0 || budget > ReadResponseDeadline {
		t.Fatalf("budget %v, want at most %v", budget, ReadResponseDeadline)
	}
}

// A login or an install is bounded by its own use case. Handing it a poll's
// three seconds would cancel real work in the middle.
func TestWritingMethodsAreNotBoundedByTheReadBudget(t *testing.T) {
	for _, name := range []string{"config.apply", "action.submit", "detect.verify"} {
		var bounded bool
		registry := registryWith(t, name,
			func(ctx context.Context, _ json.RawMessage) (any, error) {
				_, bounded = ctx.Deadline()
				return nil, nil
			})

		registry.Dispatch(t.Context(),
			Request{RPCVersion: Version, RequestID: "r", Method: name})

		if bounded {
			t.Errorf("%s inherited the read budget; a login cut off after three "+
				"seconds would look like a network failure", name)
		}
	}
}

// A caller that asked for less time than the protocol's default meant it.
func TestATighterCallerDeadlineIsNotWidened(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	var budget time.Duration
	registry := registryWith(t, "status.get",
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("the caller's deadline was dropped")
				return nil, nil
			}
			budget = time.Until(deadline)
			return nil, nil
		})

	registry.Dispatch(parent,
		Request{RPCVersion: Version, RequestID: "r", Method: "status.get"})

	if budget > time.Second {
		t.Fatalf("budget %v; the caller asked for 20ms and the dispatcher widened it", budget)
	}
}

// A handler that respects its deadline reports it, and the envelope carries the
// timeout as such rather than as a bad request.
func TestAReadThatRunsOutOfTimeIsReportedAsATimeout(t *testing.T) {
	registry := registryWith(t, "status.get",
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			expired, cancel := context.WithTimeout(ctx, 0)
			defer cancel()
			<-expired.Done()
			return nil, expired.Err()
		})

	response := registry.Dispatch(t.Context(),
		Request{RPCVersion: Version, RequestID: "r", Method: "status.get"})

	if response.OK {
		t.Fatal("a handler that timed out reported success")
	}
	if response.Error.Code != "DeadlineExceeded" {
		t.Fatalf("code = %q, want DeadlineExceeded", response.Error.Code)
	}
	if !response.Error.Retryable {
		t.Error("a timeout is worth retrying; the caller was told otherwise")
	}
}

// unsettableStream is a connection whose deadlines cannot be set -- one that
// has already been closed under us, in practice.
type unsettableStream struct {
	*rwStream
	failWrite bool
}

func (s *unsettableStream) SetReadDeadline(time.Time) error {
	if s.failWrite {
		return nil
	}
	return errors.New("use of closed network connection")
}

func (s *unsettableStream) SetWriteDeadline(time.Time) error {
	if s.failWrite {
		return errors.New("use of closed network connection")
	}
	return nil
}

// Serving a connection that cannot be bounded would reintroduce exactly the
// hang the deadlines exist to prevent, so it stops instead.
func TestServeConnStopsWhenAConnectionCannotBeBounded(t *testing.T) {
	registry := registryWith(t, "status.get", noopHandler)
	line := `{"rpc_version":1,"request_id":"r","method":"status.get"}` + "\n"

	failingRead := &unsettableStream{rwStream: newStream(line)}
	if err := ServeConn(t.Context(), registry, failingRead); err == nil {
		t.Error("a connection whose read deadline could not be set was served anyway")
	}
	if failingRead.out.Len() != 0 {
		t.Errorf("wrote %q to a connection that could not be bounded", failingRead.out.Bytes())
	}

	failingWrite := &unsettableStream{rwStream: newStream(line), failWrite: true}
	if err := ServeConn(t.Context(), registry, failingWrite); err == nil {
		t.Error("a read response whose write deadline could not be set was sent anyway")
	}
}

// The two numbers are fixed by the contract, not chosen per call site.
func TestTheDeadlinesAreTheOnesTheContractFixes(t *testing.T) {
	if RequestReadDeadline != 5*time.Second {
		t.Errorf("RequestReadDeadline = %v, contract says 5s", RequestReadDeadline)
	}
	if ReadResponseDeadline != 3*time.Second {
		t.Errorf("ReadResponseDeadline = %v, contract says 3s", ReadResponseDeadline)
	}
}
