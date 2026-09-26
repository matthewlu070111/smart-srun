//go:build !linux

package transport

import (
	"errors"
	"os"
	"syscall"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The target is OpenWrt, which is Linux. These exist so the package builds and
// its pure logic can be tested on a development workstation.
//
// Refusing rather than doing nothing is deliberate. A silent no-op would let
// the whole transport appear to work here while every packet left by the
// default route, and the difference would only show up on a router with two
// lines -- which is exactly the case this package exists for.
func bindToDevice(device string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, _ syscall.RawConn) error {
		return domain.Errorf(domain.CodeUnsupportedCapability,
			"此系统不支持把套接字绑定到设备 %s；严格绑定只在 Linux 上可用", device)
	}
}

// The errno table is Linux's; see the comment on the same name there.
const bindingErrnosClassified = false

func bindingFailure(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "没有绑定网络设备的权限"
	}
	var unsupported *domain.Error
	if errors.As(err, &unsupported) &&
		unsupported.Code == domain.CodeUnsupportedCapability {
		return "此系统不支持严格绑定"
	}
	return ""
}
