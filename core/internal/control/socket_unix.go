//go:build unix

package control

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Listen creates the control endpoint with the modes the contract fixes.
//
// The security of this path is the directory, not the socket file. Between
// net.Listen creating the socket and the chmod that follows it, the socket
// carries whatever the process umask allowed; inside a 0700 directory owned by
// the daemon's user, that window is unreachable. That is why the directory mode
// is applied first and re-applied on every start rather than treated as
// cosmetic, and it is what makes "only the local root user" true here.
//
// This establishes the endpoint and nothing else. Accepting connections is
// Serve; the procd service and its lifecycle are the daemon package's.
func Listen(path string) (net.Listener, error) {
	if err := prepareRuntimeDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := clearStaleSocket(path); err != nil {
		return nil, err
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, domain.Errorf(domain.CodeServiceStopped,
			"无法创建控制套接字 %s", path).Wrap(err)
	}
	if err := os.Chmod(path, SocketFileMode); err != nil {
		listener.Close()
		return nil, domain.Errorf(domain.CodeServiceStopped,
			"无法设置控制套接字权限").Wrap(err)
	}
	return listener, nil
}

// prepareRuntimeDir makes the directory exist and be 0700.
//
// MkdirAll applies the umask when it creates and does nothing at all when the
// path already exists, so neither outcome can be trusted to have produced 0700.
// An upgrade from a version that was looser has to be tightened, not inherited.
func prepareRuntimeDir(dir string) error {
	if err := os.MkdirAll(dir, RuntimeDirMode); err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法创建运行目录 %s", dir).Wrap(err)
	}

	info, err := os.Lstat(dir)
	if err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法检查运行目录 %s", dir).Wrap(err)
	}
	// Lstat, not Stat: a symlink here would redirect the chmod below and every
	// file the daemon writes afterwards to a location it did not choose.
	if info.Mode()&fs.ModeSymlink != 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"运行目录 %s 是符号链接，拒绝使用", dir)
	}
	if !info.IsDir() {
		return domain.Errorf(domain.CodeInvalidArgument,
			"运行目录 %s 不是目录", dir)
	}
	if err := os.Chmod(dir, RuntimeDirMode); err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法设置运行目录权限 %s", dir).Wrap(err)
	}
	return nil
}

// clearStaleSocket removes the socket a killed daemon left behind.
//
// Only a socket. Removing whatever happens to be at the path would make this
// program delete a file somebody else chose to put there.
func clearStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法检查 %s", path).Wrap(err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"%s 已存在且不是套接字，拒绝删除", path)
	}
	if err := os.Remove(path); err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法清理旧的控制套接字 %s", path).Wrap(err)
	}
	return nil
}

// VerifySocketSecurity re-checks the contract on an endpoint that already
// exists, so a daemon refuses to serve through a socket somebody loosened
// rather than discovering it from the first unexpected caller.
func VerifySocketSecurity(path string) error {
	dir := filepath.Dir(path)
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法检查运行目录 %s", dir).Wrap(err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&fs.ModeSymlink != 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"运行目录 %s 不是普通目录", dir)
	}
	// Any bit outside the contract's, not "different from": a stricter mode is
	// somebody's deliberate hardening and is not this program's to undo.
	if extra := dirInfo.Mode().Perm() &^ RuntimeDirMode; extra != 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"运行目录 %s 权限为 %#o，超出 %#o", dir, dirInfo.Mode().Perm(), RuntimeDirMode)
	}

	socketInfo, err := os.Lstat(path)
	if err != nil {
		return domain.Errorf(domain.CodeServiceStopped,
			"无法检查控制套接字 %s", path).Wrap(err)
	}
	if socketInfo.Mode()&fs.ModeSocket == 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"%s 不是套接字", path)
	}
	if extra := socketInfo.Mode().Perm() &^ SocketFileMode; extra != 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"控制套接字权限为 %#o，超出 %#o", socketInfo.Mode().Perm(), SocketFileMode)
	}
	return nil
}
