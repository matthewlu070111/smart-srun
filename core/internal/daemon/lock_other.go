//go:build !unix

package daemon

import "github.com/matthewlu070111/smart-srun/core/internal/domain"

// Lock exists on this platform only so the module builds. The service runs on
// OpenWrt; a Windows development host can compile and run the tests that do not
// need a kernel lock.
type Lock struct{}

func Acquire(path string) (*Lock, error) {
	return nil, domain.Errorf(domain.CodeUnsupportedCapability,
		"本平台不支持服务单实例锁：%s", path)
}

func (l *Lock) Release() error { return nil }
func (l *Lock) Path() string   { return "" }

func Holder(string) (int, bool) { return 0, false }
