//go:build unix

package daemon

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Lock is the single-instance guard.
//
// An advisory flock on an open descriptor, not a pidfile with a number in it.
// A pidfile has to answer "is that process still mine?" and cannot: pids are
// reused, and on a router that has just rebooted they are reused within
// seconds, so a stale pidfile eventually names somebody else's process. Then
// either the daemon refuses to start because a completely unrelated program has
// the old pid, or it decides the pid is stale and starts a second copy.
//
// The kernel releases a flock when the holder's last descriptor closes, which
// happens on exit however the process died -- clean, crashed, or killed. So a
// dead daemon leaves no stale lock, and a recycled pid cannot look like a live
// one. The pid is still written into the file, but only so a person reading it
// knows who to look at; nothing decides anything from it.
//
// syscall rather than golang.org/x/sys/unix: one Flock call is not worth the
// first third-party dependency in the module.
type Lock struct {
	file *os.File
}

// Acquire takes the lock, or reports Conflict naming whoever holds it.
func Acquire(path string) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, RuntimeFileMode)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法打开服务锁文件 %s", path).Wrap(err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readPID(file)
		file.Close()
		if holder > 0 {
			return nil, domain.Errorf(domain.CodeConflict,
				"认证服务已经在运行（进程 %d）", holder).Wrap(err)
		}
		return nil, domain.Errorf(domain.CodeConflict,
			"认证服务已经在运行").Wrap(err)
	}

	// Only now that the lock is held: truncating first would erase the previous
	// holder's pid on the failure path, taking the diagnostic with it.
	if err := writePID(file, os.Getpid()); err != nil {
		file.Close()
		return nil, err
	}
	return &Lock{file: file}, nil
}

// Release drops the lock.
//
// The file is not unlinked. Removing it would let a second process create a
// fresh one and lock that instead, so both would believe they were alone --
// the classic way to turn a lock into a decoration.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return file.Close()
}

// Path is where the lock lives, for diagnostics.
func (l *Lock) Path() string {
	if l == nil || l.file == nil {
		return ""
	}
	return l.file.Name()
}

// Holder reports the pid recorded in an existing lock file, if any. It says who
// to look at, not whether anything is running: only taking the lock answers
// that.
func Holder(path string) (int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	pid := readPID(file)
	return pid, pid > 0
}

func writePID(file *os.File, pid int) error {
	if err := file.Truncate(0); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法写入服务锁文件").Wrap(err)
	}
	if _, err := file.WriteAt([]byte(strconv.Itoa(pid)+"\n"), 0); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法写入服务锁文件").Wrap(err)
	}
	return nil
}

// readPID is deliberately forgiving: the file is a diagnostic, and a truncated
// or empty one means "no idea", not an error worth propagating.
func readPID(file *os.File) int {
	buffer := make([]byte, 32)
	n, _ := file.ReadAt(buffer, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(buffer[:n])))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
