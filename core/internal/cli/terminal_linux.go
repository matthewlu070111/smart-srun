//go:build linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"golang.org/x/sys/unix"
)

func isTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}

// Read leaves line editing and signal processing with the terminal driver.
// Poll bounds cancellation latency without an abandoned goroutine reading the
// user's next command. Error/cancel paths flush pending input before restoring
// the exact original terminal state, so an unfinished secret cannot hit a shell.
func (t *terminalInput) Read(ctx context.Context, label string, secret bool, limit int) (value string, err error) {
	fd := int(t.input.Fd())
	original, failure := unix.IoctlGetTermios(fd, unix.TCGETS)
	if failure != nil {
		return "", domain.Errorf(domain.CodeUnsupportedCapability, "无法读取终端设置")
	}
	state := *original
	state.Lflag |= unix.ICANON | unix.ISIG
	state.Iflag |= unix.ICRNL
	state.Iflag &^= unix.IGNCR | unix.INLCR
	if secret {
		state.Lflag &^= unix.ECHO | unix.ECHONL
	}
	if failure = unix.IoctlSetTermios(fd, unix.TCSETS, &state); failure != nil {
		return "", domain.Errorf(domain.CodeUnsupportedCapability, "无法安全设置终端输入")
	}
	defer func() {
		if err != nil {
			_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
		}
		if restoreErr := unix.IoctlSetTermios(fd, unix.TCSETS, original); restoreErr != nil {
			value = ""
			err = domain.Errorf(domain.CodeInternal, "未能恢复终端设置")
		}
		if secret || err != nil {
			fmt.Fprintln(t.output)
		}
	}()
	if _, err = fmt.Fprint(t.output, label+"："); err != nil {
		return "", domain.Errorf(domain.CodeInternal, "无法显示输入提示")
	}
	var data []byte
	defer func() { clear(data) }()
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		if ctx.Err() != nil {
			code := domain.CodeCancelled
			if ctx.Err() == context.DeadlineExceeded {
				code = domain.CodeDeadlineExceeded
			}
			return "", domain.Errorf(code, "输入已取消，未保存配置")
		}
		_, failure = unix.Poll(poll, 100)
		if errors.Is(failure, unix.EINTR) {
			continue
		}
		if failure != nil {
			return "", domain.Errorf(domain.CodeInternal, "无法读取终端")
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				return "", domain.Errorf(domain.CodeCancelled, "终端输入已关闭，未保存配置")
			}
			continue
		}
		var next [1]byte
		n, failure := unix.Read(fd, next[:])
		if errors.Is(failure, unix.EINTR) {
			continue
		}
		if failure != nil || n == 0 {
			return "", domain.Errorf(domain.CodeCancelled, "输入已结束，未保存配置")
		}
		if next[0] == '\n' {
			if !utf8.Valid(data) {
				return "", domain.Errorf(domain.CodeInvalidArgument, "输入必须是有效的 UTF-8 文本")
			}
			return string(data), nil
		}
		if next[0] == 0 || len(data) >= limit {
			return "", domain.Errorf(domain.CodeInvalidArgument, "输入含空字符或超过长度限制")
		}
		data = append(data, next[0])
	}
}
