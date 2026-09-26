//go:build !unix

package control

import (
	"context"
	"net"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The control socket is a Unix domain socket, and spec 03 forbids listening on
// TCP -- a management port on a router becomes a management port on the
// internet the first time somebody opens the firewall. So there is nothing to
// substitute here.
//
// These exist so the module builds on a Windows development host, where the
// pure parts (framing, dispatch, the error envelope) are worth being able to
// compile and test. Anything that needs the endpoint says so and stops.

func Listen(path string) (net.Listener, error) {
	return nil, domain.Errorf(domain.CodeUnsupportedCapability,
		"本平台不支持 Unix 控制套接字：%s", path)
}

func Serve(context.Context, net.Listener, *Registry) error {
	return domain.Errorf(domain.CodeUnsupportedCapability,
		"本平台不支持本地控制服务")
}

func VerifySocketSecurity(path string) error {
	return domain.Errorf(domain.CodeUnsupportedCapability,
		"本平台不支持 Unix 控制套接字：%s", path)
}
