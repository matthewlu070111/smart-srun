//go:build !unix

package update

import (
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"os"
)

func acquireLock(string) (*os.File, error) {
	return nil, domain.Errorf(domain.CodeUnsupportedCapability, "更新仅支持 OpenWrt")
}
func lockHeld(string) (bool, error) {
	return false, domain.Errorf(domain.CodeUnsupportedCapability, "更新仅支持 OpenWrt")
}
func FreeBytes(string) (uint64, error) {
	return 0, domain.Errorf(domain.CodeUnsupportedCapability, "更新仅支持 OpenWrt")
}
