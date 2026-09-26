package control

import "time"

// Deadlines fixed by spec 03.
//
// They are constants rather than configuration because they bound the protocol,
// not the work: a caller cannot ask for a longer poll and a handler cannot
// decide it deserves more time. Anything that legitimately takes longer is a
// mutation or a task, and is bounded by its own use case instead.
const (
	// RequestReadDeadline bounds how long a connected peer may take to finish
	// sending its request frame. Without it, opening a connection and stopping
	// mid-frame holds a daemon goroutine for as long as the peer likes.
	RequestReadDeadline = 5 * time.Second

	// ReadResponseDeadline bounds a read method: the handler's work and the
	// write of its reply together. Reads are what the LuCI page polls, so one
	// that hangs holds the browser's request open behind it.
	ReadResponseDeadline = 3 * time.Second
)

// deadlineStream is the part of net.Conn that can bound a slow peer.
//
// Streams that cannot -- an in-process pipe, a buffer in a test -- are served
// without deadlines rather than refused: the hazard is a remote peer that
// stalls, and there is no remote peer on those paths.
type deadlineStream interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}
