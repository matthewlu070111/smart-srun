//go:build unix && !linux

package control

import (
	"net"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// peerUID has no implementation on this platform, so it fails closed.
//
// The target is OpenWrt, which is Linux. Another Unix could serve this socket
// the moment somebody wrote getpeereid for it; until then, refusing is the only
// honest answer. Returning "probably fine" would turn the check into a comment.
func peerUID(net.Conn) (uint32, error) {
	return 0, domain.Errorf(domain.CodeUnsupportedCapability,
		"本平台无法确认控制连接的调用者身份，拒绝服务")
}
