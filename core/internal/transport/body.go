package transport

import (
	"io"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ReadBounded reads at most limit bytes and refuses anything longer.
//
// It reads limit+1 and checks, rather than reading limit and calling it done.
// The difference matters: stopping at exactly limit cannot tell a document that
// happens to be that size from one that was cut off, and a truncated
// authentication reply parses as far as it goes -- so a gateway that answered
// with something enormous would be read as having answered with a valid prefix
// of it. Spec 04 requires the limit+1 form for that reason.
//
// The reader is not closed here. Whoever opened it closes it, and on an HTTP
// response that has to happen even when this returns an error, or the
// connection is never returned to the pool.
func ReadBounded(reader io.Reader, limit int, what string) ([]byte, error) {
	if limit <= 0 {
		return nil, domain.Errorf(domain.CodeInternal,
			"读取 %s 时没有给出长度上限", what)
	}

	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, domain.Errorf(domain.CodeTransportFailure,
			"读取%s时连接中断", what).Wrap(err)
	}
	if len(body) > limit {
		return nil, domain.Errorf(domain.CodeProtocolInvalid,
			"%s超过 %d 字节上限", what, limit)
	}
	return body, nil
}

// DrainAndClose finishes with a response body.
//
// A body that is closed without being read leaves the connection unusable for
// keep-alive, so the pool opens a new one for the next request -- which on a
// router means a new TCP handshake for every poll. A little is read back first,
// bounded, because draining an unbounded body is how a hostile gateway keeps a
// worker busy forever.
func DrainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	io.CopyN(io.Discard, body, 4<<10)
	body.Close()
}
