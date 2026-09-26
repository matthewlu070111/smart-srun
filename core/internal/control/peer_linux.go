//go:build linux

package control

import (
	"net"
	"syscall"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// peerUID reads the caller's uid from the kernel.
//
// SO_PEERCRED, not anything the caller sends. The credentials are recorded by
// the kernel at connect time and cannot be chosen by the process on the other
// end, which is the whole reason this is worth checking: a uid a client claimed
// would be a uid a client picked.
func peerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, domain.Errorf(domain.CodeInvalidArgument,
			"控制连接不是 Unix 套接字，无法确认调用者身份")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, domain.Errorf(domain.CodeServiceStopped,
			"无法读取控制连接").Wrap(err)
	}

	var credentials *syscall.Ucred
	var sockErr error
	controlErr := raw.Control(func(fd uintptr) {
		credentials, sockErr = syscall.GetsockoptUcred(
			int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if controlErr != nil {
		return 0, domain.Errorf(domain.CodeServiceStopped,
			"无法读取控制连接").Wrap(controlErr)
	}
	if sockErr != nil {
		return 0, domain.Errorf(domain.CodeInvalidArgument,
			"无法确认调用者身份").Wrap(sockErr)
	}
	return credentials.Uid, nil
}
