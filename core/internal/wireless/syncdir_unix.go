//go:build unix

package wireless

import (
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// syncDir makes the rename itself durable.
//
// Syncing the file only guarantees its contents; until the directory entry is
// flushed a power loss can leave the target pointing at the old inode, or at
// nothing. For this package that is the whole point: the journal exists to be
// there after a power cut, and a journal that was never flushed is a journal
// that will not be.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法打开目录以同步").Wrap(err)
	}
	defer handle.Close()

	if err := handle.Sync(); err != nil {
		return domain.Errorf(domain.CodeInternal, "目录同步失败").Wrap(err)
	}
	return nil
}
