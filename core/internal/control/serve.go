//go:build unix

package control

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// MaxConcurrentConnections bounds how many callers are served at once.
//
// One request per connection with a five-second read deadline means a handful
// is already generous: LuCI polls with one connection at a time and the CLI
// makes one call. The cap exists so a stuck or hostile local caller cannot open
// descriptors until the daemon runs out of them, which on a router is a few
// hundred and takes seconds.
const MaxConcurrentConnections = 8

// maxAcceptFailures bounds a run of Accept errors that are not a closed
// listener.
//
// Something like EMFILE is transient and retrying is right; something like a
// listener whose socket was unlinked is not, and retrying it forever would be a
// silent spin. Giving up after a run of them hands the problem to procd, which
// will restart the service -- a restart is a worse outcome than recovering, and
// a better one than a daemon that looks alive and answers nothing.
const maxAcceptFailures = 16

// Serve accepts connections until ctx is done.
//
// A clean stop returns nil. The listener is closed by ctx, which is what makes
// the blocked Accept return; anything else is reported.
func Serve(ctx context.Context, listener net.Listener, registry *Registry) error {
	// Closing the listener is the only way to interrupt Accept. AfterFunc runs
	// it on cancellation and unregisters itself when Serve returns first --
	// which is why the close is also deferred: on the path where Serve gives up
	// on its own, nothing else would ever close it, and the socket file would
	// stay on disk with no listener behind it for the next caller to hang on.
	// Closing twice is harmless.
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	defer listener.Close()

	var callers sync.WaitGroup
	// Waited on before returning, so a stop does not leave a handler writing to
	// a socket the caller has stopped reading and nobody is tracking.
	defer callers.Wait()

	slots := make(chan struct{}, MaxConcurrentConnections)
	failures := 0

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			failures++
			if failures > maxAcceptFailures {
				return domain.Errorf(domain.CodeServiceStopped,
					"控制套接字连续 %d 次无法接受连接", failures).Wrap(err)
			}
			continue
		}
		failures = 0

		select {
		case slots <- struct{}{}:
		default:
			// Refused rather than queued, and refused with a reason: a caller
			// that got a silent EOF has no way to tell "too busy" from "the
			// service died mid-request".
			refuse(conn)
			continue
		}

		callers.Go(func() {
			defer func() { <-slots }()
			defer conn.Close()
			serveOne(ctx, registry, conn)
		})
	}
}

// refuse answers a caller this daemon has no slot for, then closes.
//
// Reading the request before closing is not politeness. On a Unix socket,
// closing while bytes the peer sent are still unread resets the connection, and
// the reset discards the answer already written -- so the caller gets exactly
// the silent EOF this function exists to prevent, and gets it only when the
// daemon really is busy, which is when the reason matters most. A caller whose
// request is larger than the socket buffer sees it every time: its write is
// still in flight when the close lands, so the write itself fails and the
// answer is never read.
//
// Bounded by the same request limit and the same read deadline as a served
// connection. No new number: spec 03 fixes two deadlines and inventing a third
// here would make the refusal path behave unlike everything else for no reason
// anybody could look up.
func refuse(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(RequestReadDeadline))
	_ = WriteResponse(conn, NewFailure("", domain.Errorf(domain.CodeBusy,
		"同时处理的本地请求过多，请稍后重试")))
	// One frame, discarded. Draining to EOF instead would hold this connection
	// for the whole deadline every time, because the caller stops writing after
	// its request and waits for the answer.
	_, _ = readLine(bufio.NewReader(conn), MaxRequestBytes)
}

// serveOne checks who is calling before it reads anything they sent.
func serveOne(ctx context.Context, registry *Registry, conn net.Conn) {
	if err := verifyPeer(conn); err != nil {
		_ = WriteResponse(conn, NewFailure("", err))
		return
	}
	// ServeConn's own error is the transport failing, which there is nobody
	// left to tell: the connection it would be reported on is the one that
	// broke. It is bounded and it closes; that is the contract here.
	_ = ServeConn(ctx, registry, conn)
}

// verifyPeer refuses a caller that is not entitled to this socket.
//
// The 0700 directory already makes the socket unreachable for anybody else, so
// this is the second of two independent checks rather than the only one. It is
// worth having because the first depends on a mode that an installer, a backup
// restore or a well-meaning script can change, and this one does not.
func verifyPeer(conn net.Conn) error {
	uid, err := peerUID(conn)
	if err != nil {
		return err
	}
	if !allowedPeer(uid) {
		return domain.Errorf(domain.CodeInvalidArgument,
			"控制接口只接受本机 root 调用")
	}
	return nil
}

// allowedPeer is spec 03's "local root only".
//
// Root, or the uid this process itself runs as. On a router those are the same:
// procd starts the daemon as root, so the second case adds nothing. What it
// does add is that the tests and a developer running the daemon unprivileged go
// through this same function rather than around it -- a check with a
// test-only bypass is a check nobody has run.
func allowedPeer(uid uint32) bool {
	return uid == 0 || uid == uint32(os.Getuid())
}
