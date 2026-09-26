package cli

import (
	"context"
	"io"
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type terminalInput struct {
	input  *os.File
	output io.Writer
}

func newTerminalInput(input io.Reader, output io.Writer) (*terminalInput, error) {
	file, ok := input.(*os.File)
	if !ok || !isTerminal(file) {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "交互输入需要终端；脚本请通过标准输入提供 JSON")
	}
	return &terminalInput{input: file, output: output}, nil
}

type formInput interface {
	Read(context.Context, string, bool, int) (string, error)
}
