//go:build unix

package update

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func acquireLock(path string) (*os.File, error) {
	if err := privateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, storageError(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, domain.Errorf(domain.CodeBusy, "另一个更新操作正在进行").Wrap(err)
	}
	return file, nil
}

func lockHeld(path string) (bool, error) {
	// Status queries must not create or chmod anything.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, storageError(err)
	}
	defer file.Close()
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	if err != nil {
		return false, storageError(err)
	}
	return false, nil
}

func FreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, storageError(err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
