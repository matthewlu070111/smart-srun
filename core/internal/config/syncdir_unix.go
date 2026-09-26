//go:build unix

package config

import (
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// syncDir makes the rename itself durable.
//
// Syncing the file only guarantees its contents; until the directory entry is
// flushed, a power loss can leave the target still pointing at the old inode --
// or at nothing. This is the step that is easiest to leave out and hardest to
// notice, because everything works until the router loses power mid-save.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return domain.Errorf(domain.CodeInvalidConfig,
			"无法打开配置目录以同步").Wrap(err)
	}
	defer handle.Close()

	if err := handle.Sync(); err != nil {
		return domain.Errorf(domain.CodeInvalidConfig,
			"配置目录同步失败").Wrap(err)
	}
	return nil
}
