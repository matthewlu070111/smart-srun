//go:build linux

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"golang.org/x/sys/unix"
)

func TestTerminalSignalHelper(t *testing.T) {
	if os.Getenv("SMARTSRUN_TEST_TTY_SIGNAL") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	input, err := newTerminalInput(os.Stdin, os.Stdout)
	if err != nil {
		os.Exit(2)
	}
	_, err = input.Read(ctx, "secret-ready", true, 128)
	if code, _ := domain.CodeOf(err); code == domain.CodeCancelled {
		os.Exit(130)
	}
	os.Exit(3)
}

func TestTerminalSIGINTCleansPartialPassword(t *testing.T) {
	master, slave := testPTY(t)
	original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	program, args := os.Args[0], []string{"-test.run=^TestTerminalSignalHelper$"}
	// Keep the child itself on the target architecture when go test uses
	// -exec QEMU on a host without global binfmt registration.
	if executor := os.Getenv("SMARTSRUN_TEST_EXECUTOR"); executor != "" {
		args, program = append([]string{program}, args...), executor
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append(os.Environ(), "SMARTSRUN_TEST_TTY_SIGNAL=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 2000); err != nil || n != 1 {
		t.Fatal("no child prompt", err)
	}
	buf := make([]byte, 1024)
	n, err := unix.Read(int(master.Fd()), buf)
	if err != nil || !strings.Contains(string(buf[:n]), "secret-ready") {
		t.Fatal("missing prompt", err)
	}
	if _, err := unix.Write(int(master.Fd()), []byte("unfinished-secret")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 130 {
		t.Fatalf("exit=%v error=%v", cmd.ProcessState, err)
	}
	restored, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *restored != *original {
		t.Fatal("SIGINT did not restore terminal", err)
	}
	state := *original
	state.Lflag &^= unix.ICANON
	state.Cc[unix.VMIN], state.Cc[unix.VTIME] = 0, 0
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, &state); err != nil {
		t.Fatal(err)
	}
	n, err = unix.Read(int(slave.Fd()), buf)
	_ = unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, original)
	if err != nil || n != 0 {
		t.Fatal("SIGINT left password queued", err)
	}
}

func testPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "pty-master")
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

type promptSignal struct {
	once  sync.Once
	ready chan struct{}
}

func (s *promptSignal) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.ready) })
	return len(p), nil
}

func TestTerminalPasswordDoesNotEchoAndRestoresSettings(t *testing.T) {
	for _, mode := range []string{"enter", "cancel", "overflow", "eof"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := testPTY(t)
			original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			prompt := &promptSignal{ready: make(chan struct{})}
			input, err := newTerminalInput(slave, prompt)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type result struct {
				value string
				err   error
			}
			done := make(chan result, 1)
			limit := 128
			if mode == "overflow" {
				limit = 8
			}
			go func() { value, err := input.Read(ctx, "密码", true, limit); done <- result{value, err} }()
			select {
			case <-prompt.ready:
			case <-time.After(time.Second):
				t.Fatal("no prompt")
			}
			state, _ := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if state.Lflag&(unix.ECHO|unix.ECHONL) != 0 {
				t.Fatal("secret prompt appeared before echo disabled")
			}
			secret := " exact'秘密\\value "
			text := secret + "\n"
			if mode == "cancel" {
				text = secret
			}
			if mode == "eof" {
				text = "\x04"
			}
			if _, err := unix.Write(int(master.Fd()), []byte(text)); err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				cancel()
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal read did not return")
			}
			if mode == "enter" {
				if got.err != nil || got.value != secret {
					t.Fatal("password was changed", got.err)
				}
			} else if got.err == nil {
				t.Fatal("interrupted input succeeded")
			}
			restored, _ := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if *restored != *original {
				t.Fatal("terminal settings not restored")
			}
			buf := make([]byte, 4096)
			n, _ := unix.Read(int(master.Fd()), buf)
			if n > 0 && strings.Contains(string(buf[:n]), "exact") {
				t.Fatal("password echoed on terminal")
			}
			if mode != "enter" {
				// A partial/oversized secret must not prefix the next shell command.
				state := *original
				state.Lflag &^= unix.ICANON
				state.Cc[unix.VMIN] = 0
				state.Cc[unix.VTIME] = 0
				if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, &state); err != nil {
					t.Fatal(err)
				}
				n, err := unix.Read(int(slave.Fd()), buf)
				_ = unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, original)
				if err != nil || n != 0 {
					t.Fatal("secret input remained queued")
				}
			}
		})
	}
}

func TestInteractiveFlagRefusesPipedInputBeforeStartingService(t *testing.T) {
	client := onlineClient{ensure: func(context.Context) error { t.Fatal("non-TTY started service"); return nil }}
	code, _, _ := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(t.Context(), client, []string{"config", "account", "add", "--interactive"}, strings.NewReader("password"), stdout, stderr)
	})
	if code != ExitInvalidInput {
		t.Fatal(code)
	}
	if _, err := newTerminalInput(strings.NewReader(""), os.Stderr); err == nil {
		t.Fatal("accepted non-TTY")
	} else if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
		t.Fatal(code)
	}
}
