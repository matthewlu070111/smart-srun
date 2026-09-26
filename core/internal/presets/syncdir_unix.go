//go:build unix

package presets

import (
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// syncDir makes the rename itself durable.
//
// Syncing the file guarantees its contents; until the directory entry is
// flushed, a power loss can leave the name pointing at the old inode or at
// nothing. This matters for user presets on persistent storage; the remote
// cache uses tmpfs and is replaced by the built-in fallback after reboot.
//
// wireless has its own copy of this and they are deliberately not shared. Two
// packages needing the same three system calls is not a reason for one to
// import the other, and the architecture table says which of them is allowed to.
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
