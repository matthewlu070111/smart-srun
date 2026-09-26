package presets

import (
	"io"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// MaxPayloadBytes is what a preset catalogue is allowed to be.
//
// Spec 04 fixes it at 2 MiB. The published catalogue is a few kilobytes, so
// this is not a budget anybody is close to: it is the ceiling that stops a
// redirected download, a captive portal's video splash or a hostile mirror
// from being read into the memory of a router with 128 MiB of it.
const MaxPayloadBytes int64 = 2 << 20

// ReadLimited reads a body, refusing one that exceeds the limit.
//
// It reads limit+1 bytes and checks, which is spec 04's rule in as many words:
// "读取 limit+1 检测超限，不能读满以后再判断". Reading exactly limit and stopping
// cannot tell a body that happened to be exactly that size from one that was
// cut off at it, and answering "here is the catalogue" for a truncated document
// is worse than answering nothing -- the JSON would fail to parse and the
// caller would move on to the next source having learned the wrong thing about
// this one.
//
// The extra byte is read and discarded rather than kept: nothing needs it
// except the fact that it was there.
func ReadLimited(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"读取上限必须是正数")
	}

	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, domain.Errorf(domain.CodeTransportFailure,
			"读取内容失败").Wrap(err)
	}
	if int64(len(body)) > limit {
		return nil, domain.Errorf(domain.CodeProtocolInvalid,
			"内容超过 %d 字节的上限", limit)
	}
	return body, nil
}
