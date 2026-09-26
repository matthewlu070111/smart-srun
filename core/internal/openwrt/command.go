// Package openwrt adapts this program to the router it runs on: running system
// tools, reading their output, and turning what they say into the vocabulary
// the rest of the program uses.
//
// Everything here is I/O against somebody else's program. The rule that shapes
// the package is that a tool's output is untrusted input -- it may be absent,
// truncated, from a firmware nobody tested against, or contain a value a user
// typed. Parsing lives in pure functions over bytes so it can be tested against
// output captured from real devices, and only the running of the tool needs a
// router.
package openwrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const (
	// DefaultTimeout bounds one tool invocation. Everything this package runs
	// is a local query that answers in milliseconds; five seconds is already
	// the pathological case, and spec 04 puts the whole authentication attempt
	// at thirty.
	DefaultTimeout = 5 * time.Second

	// DefaultMaxOutput bounds what one invocation may return. `uci show` on a
	// large configuration is tens of kilobytes; anything approaching this is a
	// tool that has gone wrong, and on a 128 MiB router reading it all is worse
	// than losing it.
	DefaultMaxOutput = 256 << 10

	// waitGrace is how long Wait may keep going after the process was killed.
	//
	// Killing the process does not close a pipe that a grandchild inherited, so
	// without this the read of the output would block forever on a process that
	// is already dead. It is not the timeout -- it is the wait for the pipes to
	// be released after the timeout already fired.
	waitGrace = 500 * time.Millisecond
)

// DefaultSearchPath is where OpenWrt keeps the tools this package runs.
//
// Resolution is explicit rather than inherited so that the lookup and the
// child's PATH cannot disagree, and so a daemon started from an unusual
// environment still finds the same uci it found during testing.
var DefaultSearchPath = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// fixedEnvironment is the child's entire environment.
//
// Nothing is inherited. LC_ALL fixes the language because several of these
// tools translate their messages and a parser that works in English is a parser
// that fails on a router set to anything else. Dropping the rest keeps
// http_proxy, a modified PATH, and anything else in the daemon's environment
// from changing what a system query returns.
var fixedEnvironment = []string{
	"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
	"LC_ALL=C",
	"LANG=C",
}

// refusedPrograms are the shells.
//
// This package builds argv directly and passes it to execve; there is no string
// for a shell to re-split, so an SSID containing `; rm -rf /` is one argument
// and stays one argument. Refusing to start a shell at all is what keeps that
// true: the failure mode is somebody adding `sh -c "uci set ...$value"` later
// because it was quicker, and a review rule does not stop that. Whoever
// genuinely needs a shell has to delete this and explain why.
var refusedPrograms = []string{"sh", "ash", "bash", "dash", "ksh", "zsh", "csh"}

// Result is everything one invocation produced. It is returned even when the
// call failed, because the output of a tool that exited non-zero is usually the
// explanation.
type Result struct {
	Program  string
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	// StdoutTruncated reports that the tool produced more than MaxOutput and
	// the rest was dropped. Parsers must treat a truncated document as invalid
	// rather than as a short one: half a JSON object still parses as far as it
	// goes.
	StdoutTruncated bool
	StderrTruncated bool
	Duration        time.Duration
}

// ExitError reports that a tool ran and refused.
//
// It carries no output. `uci get x.y` exits 1 for "not set", which is an
// ordinary answer, so callers branch on Code; anything they want to log is in
// the Result they also received. Keeping the text out means an error that
// bubbles up through a wrapper into a log line cannot carry a value that was
// on the command line with it.
type ExitError struct {
	Program string
	Code    int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("%s exited with status %d", e.Program, e.Code)
}

// Runner executes one system tool at a time.
//
// The zero value works and uses the defaults. Tests override SearchPath to
// point at a directory of their own; nothing else needs to change to run a real
// process against a real kernel.
type Runner struct {
	Timeout    time.Duration
	MaxOutput  int
	SearchPath []string
}

func (r Runner) timeout() time.Duration {
	if r.Timeout <= 0 {
		return DefaultTimeout
	}
	return r.Timeout
}

func (r Runner) maxOutput() int {
	if r.MaxOutput <= 0 {
		return DefaultMaxOutput
	}
	return r.MaxOutput
}

func (r Runner) searchPath() []string {
	if len(r.SearchPath) == 0 {
		return DefaultSearchPath
	}
	return r.SearchPath
}

// Resolve turns a program name into the absolute path that will be executed.
//
// A name containing a separator is taken as given; a bare name is looked up in
// the search path. Not finding it is UnsupportedCapability rather than a
// generic failure: a firmware without iwinfo cannot scan, and the honest answer
// is that the feature is unavailable here, not that something went wrong.
func (r Runner) Resolve(program string) (string, error) {
	if program == "" {
		return "", domain.Errorf(domain.CodeInvalidArgument, "未指定要执行的程序")
	}
	if slices.Contains(refusedPrograms, filepath.Base(program)) {
		return "", domain.Errorf(domain.CodeInvalidArgument,
			"拒绝通过 shell 执行系统命令")
	}

	if strings.ContainsRune(program, os.PathSeparator) {
		if err := executable(program); err != nil {
			return "", domain.Errorf(domain.CodeUnsupportedCapability,
				"系统缺少 %s，该功能在此固件上不可用", program).Wrap(err)
		}
		return program, nil
	}

	for _, dir := range r.searchPath() {
		candidate := filepath.Join(dir, program)
		if err := executable(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", domain.Errorf(domain.CodeUnsupportedCapability,
		"系统缺少 %s，该功能在此固件上不可用", program)
}

// Run executes one tool and collects its output.
//
// Arguments go to execve as separate strings. There is no shell and no string
// that a shell could re-split, so a value containing spaces, quotes, newlines
// or metacharacters is one argument and arrives at the tool unchanged.
//
// A non-zero exit is reported as an *ExitError, so ignoring it takes a
// deliberate line of code. The Result is filled in either way.
func (r Runner) Run(ctx context.Context, program string, args ...string) (Result, error) {
	return r.run(ctx, program, "", args...)
}

// RunInput supplies a bounded document through a pipe, so secrets never need
// to appear in argv. It retains Run's environment, time and process-group limits.
func (r Runner) RunInput(ctx context.Context, program, input string, args ...string) (Result, error) {
	if len(input) > DefaultMaxOutput {
		return Result{Program: program}, domain.Errorf(domain.CodeInvalidArgument, "命令输入超过大小上限")
	}
	return r.run(ctx, program, input, args...)
}

func (r Runner) run(ctx context.Context, program, input string, args ...string) (Result, error) {
	result := Result{Program: program}

	resolved, err := r.Resolve(program)
	if err != nil {
		return result, err
	}
	for index, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return result, domain.Errorf(domain.CodeInvalidArgument,
				"第 %d 个参数包含空字符", index+1)
		}
	}

	timeout := r.timeout()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &boundedBuffer{limit: r.maxOutput()}
	stderr := &boundedBuffer{limit: r.maxOutput()}

	cmd := exec.CommandContext(runCtx, resolved, args...)
	cmd.Args[0] = program
	cmd.Env = slices.Clone(fixedEnvironment)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// The reader reaches EOF after the supplied document; no interactive prompt
	// can borrow the daemon's terminal or wait forever for more input.
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	// A tool that forks keeps the group, so cancelling reaps the children too
	// rather than leaving them attached to the pipe.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = waitGrace

	started := time.Now()
	runErr := cmd.Run()
	result.Duration = time.Since(started)
	result.Stdout, result.StdoutTruncated = stdout.Bytes(), stdout.truncated
	result.Stderr, result.StderrTruncated = stderr.Bytes(), stderr.truncated

	// The caller's cancellation is reported as cancellation even though what
	// actually happened was that the child was killed.
	if parentErr := ctx.Err(); parentErr != nil {
		if errors.Is(parentErr, context.DeadlineExceeded) {
			return result, domain.Errorf(domain.CodeDeadlineExceeded,
				"执行 %s 超时", program).Wrap(parentErr)
		}
		return result, domain.Errorf(domain.CodeCancelled,
			"执行 %s 已取消", program).Wrap(parentErr)
	}
	if runCtx.Err() != nil {
		return result, domain.Errorf(domain.CodeDeadlineExceeded,
			"执行 %s 超过 %s 未返回", program, timeout).Wrap(runCtx.Err())
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.Is(runErr, exec.ErrWaitDelay):
		// The tool exited, but something it started still holds the output
		// pipe. Waiting for that is what would hang the daemon, so the wait was
		// abandoned -- and since the output may be short, this is a failure
		// rather than a success with whatever arrived in time.
		return result, domain.Errorf(domain.CodeInternal,
			"%s 已退出，但它启动的子进程仍占用输出管道", program).Wrap(runErr)
	case errors.As(runErr, &exitErr):
		result.ExitCode = exitErr.ExitCode()
		return result, &ExitError{Program: program, Code: result.ExitCode}
	default:
		return result, domain.Errorf(domain.CodeInternal,
			"无法执行 %s", program).Wrap(runErr)
	}
	return result, nil
}

// RunInstall owns the one non-cancellable system operation: a native package
// transaction. Its caller is the independent update worker. Output and pipe
// draining stay bounded, while a live installer is never killed by a timeout.
func (r Runner) RunInstall(program string, args ...string) (Result, error) {
	result := Result{Program: program}
	if program != "opkg" && program != "apk" {
		return result, domain.Errorf(domain.CodeInvalidArgument, "不可中断执行仅用于原生安装器")
	}
	resolved, err := r.Resolve(program)
	if err != nil {
		return result, err
	}
	cmd := exec.Command(resolved, args...)
	cmd.Env = append(append([]string(nil), fixedEnvironment...), "SMARTSRUN_INSTALL_WORKER=1")
	setProcessGroup(cmd)
	output := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = time.Second
	started := time.Now()
	err = cmd.Run()
	result.Duration = time.Since(started)
	result.Stdout, result.StdoutTruncated = output.Bytes(), output.truncated
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			result.ExitCode = exit.ExitCode()
		}
		return result, domain.Errorf(domain.CodeInstallFailed, "包管理器安装未成功完成，请查看恢复状态").Wrap(err)
	}
	return result, nil
}

// boundedBuffer keeps at most limit bytes and counts the rest.
//
// Write always reports success. Returning an error would make the copier stop
// reading, the tool would take SIGPIPE, and "the output was long" would turn
// into a nondeterministic exit status. Memory is bounded here; time is bounded
// by the timeout.
type boundedBuffer struct {
	limit     int
	buf       bytes.Buffer
	truncated bool
	dropped   int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) <= room {
			return b.buf.Write(p)
		}
		if _, err := b.buf.Write(p[:room]); err != nil {
			return 0, err
		}
		b.truncated = true
		b.dropped += len(p) - room
		return len(p), nil
	}
	b.truncated = true
	b.dropped += len(p)
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }
