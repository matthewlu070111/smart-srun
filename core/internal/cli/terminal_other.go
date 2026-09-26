//go:build !linux

package cli

import (
	"context"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"os"
)

func isTerminal(*os.File) bool { return false }
func (*terminalInput) Read(context.Context, string, bool, int) (string, error) {
	return "", domain.Errorf(domain.CodeUnsupportedCapability, "此平台请使用 JSON 标准输入")
}
