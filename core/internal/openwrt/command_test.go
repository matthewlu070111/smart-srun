package openwrt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	code, ok := domain.CodeOf(err)
	if !ok {
		t.Fatalf("error carries no code: %v", err)
	}
	return code
}

func TestBoundedInputUsesPipeAndClosesIt(t *testing.T) {
	program, args := helperCommand(t, "stdin", "public-argument")
	input := "private-value ; $(never-run) ' \\\n"
	result, err := (Runner{}).RunInput(t.Context(), program, input, args...)
	if err != nil || string(result.Stdout) != "public-argument\n"+input {
		t.Fatalf("stdin transport failed: %v", err)
	}
	_, err = (Runner{}).RunInput(t.Context(), program, strings.Repeat("x", DefaultMaxOutput+1), args...)
	if codeOf(t, err) != domain.CodeInvalidArgument {
		t.Fatal(err)
	}
}

// T28 -- the injection case, against a real execve.
//
// Everything this package runs can be handed a value the user typed: an SSID, a
// wireless key, an interface name. If any of it were assembled into a command
// string, a value like `; rm -rf /` would become a second command. This runs a
// real process and reads back the argv the kernel delivered.
func TestAnArgumentIsOneArgumentNoMatterWhatIsInIt(t *testing.T) {
	nasty := []string{
		"; rm -rf /tmp/nothing",
		"$(whoami)",
		"`id`",
		"a | b && c",
		"two words",
		"it's",
		"back\\slash",
		"new\nline",
		"tab\there",
		"--looks-like-a-flag",
		"南昌 校园网",
		"'quoted'",
		`"double"`,
		"*",
		"$HOME",
		">/tmp/redirected",
	}

	program, args := helperCommand(t, append([]string{"echo"}, nasty...)...)
	result, err := Runner{}.Run(t.Context(), program, args...)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The helper prints one argument per line, so an argument containing a
	// newline occupies two -- which is why this compares the whole block rather
	// than counting lines.
	want := strings.Join(nasty, "\n") + "\n"
	if got := string(result.Stdout); got != want {
		t.Errorf("the arguments did not arrive intact\n got %q\nwant %q", got, want)
	}
}

// The other half of the same guarantee: there is no shell to reach for.
func TestTheRunnerRefusesToStartAShell(t *testing.T) {
	for _, shell := range []string{"sh", "bash", "ash", "/bin/sh", "/usr/bin/bash"} {
		t.Run(shell, func(t *testing.T) {
			_, err := Runner{}.Run(t.Context(), shell, "-c", "echo hello")
			if err == nil {
				t.Fatal("the runner started a shell")
			}
			if code := codeOf(t, err); code != domain.CodeInvalidArgument {
				t.Errorf("code = %s, want InvalidArgument", code)
			}
		})
	}
}

// A tool this firmware does not ship is a capability that is unavailable, not a
// failure to retry. Spec 03 has a code for exactly this and the M04 card
// requires it.
func TestAMissingToolIsReportedAsAnUnsupportedCapability(t *testing.T) {
	runner := Runner{SearchPath: []string{t.TempDir()}}

	_, err := runner.Run(t.Context(), "iwinfo", "info")
	if err == nil {
		t.Fatal("running a tool that does not exist succeeded")
	}
	if code := codeOf(t, err); code != domain.CodeUnsupportedCapability {
		t.Errorf("code = %s, want UnsupportedCapability", code)
	}
	if !strings.Contains(err.Error(), "iwinfo") {
		t.Errorf("the message does not name the missing tool: %v", err)
	}
}

// A directory named like a tool, or a file without the execute bit, is the tool
// being absent -- not a failure at the moment the feature is used.
func TestSomethingThatIsNotAProgramCountsAsMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not decide executability here")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ubus"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uci"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runner := Runner{SearchPath: []string{dir}}
	for _, tool := range []string{"ubus", "uci"} {
		if _, err := runner.Resolve(tool); err == nil {
			t.Errorf("%s resolved to something that cannot be run", tool)
		} else if code := codeOf(t, err); code != domain.CodeUnsupportedCapability {
			t.Errorf("%s: code = %s, want UnsupportedCapability", tool, code)
		}
	}
}

// A tool that does not return has to stop being this program's problem. On a
// router `wifi reload` and the package managers can block for a long time, and
// a daemon that waits for one of them stops answering the interface.
func TestAToolThatDoesNotReturnIsKilled(t *testing.T) {
	program, args := helperCommand(t, "sleep", "60000")
	runner := Runner{Timeout: 300 * time.Millisecond}

	started := time.Now()
	_, err := runner.Run(t.Context(), program, args...)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a command that never returns was reported as successful")
	}
	if code := codeOf(t, err); code != domain.CodeDeadlineExceeded {
		t.Errorf("code = %s, want DeadlineExceeded", code)
	}
	if elapsed > 5*time.Second {
		t.Errorf("waited %v for a 300ms timeout", elapsed)
	}
}

// Cancellation has to be prompt and has to be reported as cancellation. Spec 04
// gives user actions 500ms to take effect; a command that ignores the context
// would hold that up for its whole timeout.
func TestCancellingStopsTheToolAndSaysSo(t *testing.T) {
	program, args := helperCommand(t, "sleep", "60000")
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	_, err := Runner{Timeout: 30 * time.Second}.Run(ctx, program, args...)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a cancelled command was reported as successful")
	}
	if code := codeOf(t, err); code != domain.CodeCancelled {
		t.Errorf("code = %s, want Cancelled", code)
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %v", elapsed)
	}
}

// The reaping that matters: killing the tool must kill what the tool started.
//
// Signalling only the process named on the command line leaves its children
// running, still holding the pipe, so the timeout that was supposed to bound
// the call does not.
func TestKillingAToolKillsWhatItStarted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are a Unix concept")
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	program, args := helperCommand(t, "group", pidFile, "60000")

	_, err := Runner{Timeout: 700 * time.Millisecond}.Run(t.Context(), program, args...)
	if err == nil {
		t.Fatal("expected the timeout")
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the helper never recorded its child: %v", err)
	}
	pid := 0
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("unreadable pid %q", data)
	}

	// The kill is asynchronous, so give it a bounded moment before concluding
	// the grandchild survived.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process %d outlived the tool that started it; the timeout only "+
		"killed the parent", pid)
}

// A tool that exits but leaves a child holding the pipe must not wedge the
// caller. Waiting for the pipe rather than for the process is the classic way a
// daemon stops responding after `wifi reload`.
func TestAToolThatLeavesAChildHoldingThePipeDoesNotHangTheCaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The grandchild keeps the test binary open and Windows refuses to
		// delete a running image, so the toolchain's own cleanup fails and the
		// run reports a failure that has nothing to do with the guarantee. The
		// target is OpenWrt and this is verified there.
		t.Skip("a lingering grandchild blocks removal of the test binary here")
	}
	program, args := helperCommand(t, "orphan", "30000")

	started := time.Now()
	_, err := Runner{Timeout: 30 * time.Second}.Run(t.Context(), program, args...)
	elapsed := time.Since(started)

	if elapsed > 10*time.Second {
		t.Fatalf("the call took %v; it waited for the grandchild", elapsed)
	}
	if err == nil {
		t.Error("the output may be incomplete, so this is not a success")
	}
}

// Output has to be bounded. A tool that produces without end would otherwise be
// read into memory on a router that has 128 MiB of it.
//
// The limit is deliberately not a multiple of the helper's write size, so the
// write that reaches it straddles the boundary. An earlier version used a round
// number, every write landed exactly on the limit, and the branch that decides
// how much of a straddling chunk to keep was never run.
func TestOutputIsCappedAndTheTruncationIsReported(t *testing.T) {
	program, args := helperCommand(t, "spew", "400000")
	runner := Runner{MaxOutput: 8000}

	result, err := runner.Run(t.Context(), program, args...)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Stdout) != runner.MaxOutput {
		t.Errorf("kept %d bytes, want exactly the limit of %d",
			len(result.Stdout), runner.MaxOutput)
	}
	if !result.StdoutTruncated {
		t.Error("the output was cut but not reported as truncated; a parser " +
			"would read half a document as a whole one")
	}
}

// The buffer itself, at the boundary.
//
// A tool writes in whatever size it likes, so the chunk that reaches the limit
// almost always straddles it. Two things have to hold on that write: exactly
// the remaining room is kept, and the whole chunk is reported as consumed --
// a short count would make the copier fail with "short write", turning "the
// output was long" into a command that appears to have gone wrong.
func TestTheOutputBufferStopsExactlyAtItsLimit(t *testing.T) {
	buffer := &boundedBuffer{limit: 10}

	written, err := buffer.Write([]byte("abcd"))
	if err != nil || written != 4 {
		t.Fatalf("Write = (%d, %v), want (4, nil)", written, err)
	}
	if buffer.truncated {
		t.Error("reported as truncated before the limit was reached")
	}

	// This one straddles: 6 bytes of room left, 8 offered.
	written, err = buffer.Write([]byte("efghijkl"))
	if err != nil || written != 8 {
		t.Fatalf("Write = (%d, %v), want (8, nil) so the copier does not fail",
			written, err)
	}
	if got := string(buffer.Bytes()); got != "abcdefghij" {
		t.Errorf("buffer = %q, want the first 10 bytes", got)
	}
	if !buffer.truncated {
		t.Error("the chunk that crossed the limit was not reported as truncating")
	}

	// And once full, further writes are dropped rather than accumulated.
	if _, err := buffer.Write([]byte("mnopq")); err != nil {
		t.Fatalf("Write after the limit: %v", err)
	}
	if len(buffer.Bytes()) != 10 {
		t.Errorf("buffer grew to %d bytes past its limit", len(buffer.Bytes()))
	}
	if buffer.dropped != 7 {
		t.Errorf("dropped = %d, want 7", buffer.dropped)
	}
}

// A non-zero exit is an answer, not an error to swallow: `uci get` uses status 1
// for "not set". It is still returned as an error so that ignoring it takes a
// deliberate line of code, and the output comes back either way.
func TestANonZeroExitIsReportedWithItsOutput(t *testing.T) {
	program, args := helperCommand(t, "exit", "3")

	result, err := Runner{}.Run(t.Context(), program, args...)
	if err == nil {
		t.Fatal("a failing command was reported as successful")
	}
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %T, want *ExitError", err)
	}
	if exit.Code != 3 {
		t.Errorf("Code = %d, want 3", exit.Code)
	}
	if result.ExitCode != 3 {
		t.Errorf("result.ExitCode = %d, want 3", result.ExitCode)
	}
	if !strings.Contains(string(result.Stdout), "stdout before exiting") {
		t.Errorf("stdout was discarded: %q", result.Stdout)
	}
	if !strings.Contains(string(result.Stderr), "stderr before exiting") {
		t.Errorf("stderr was discarded: %q", result.Stderr)
	}
}

// The error must not carry the output.
//
// An argument can be a wireless key, and a tool that fails often echoes what it
// was given. An error travels further than a Result -- into logs, into an RPC
// envelope, onto a status page -- so it carries no text from the child.
func TestAFailedCommandDoesNotPutItsOutputInTheError(t *testing.T) {
	program, args := helperCommand(t, "exit", "1")
	result, err := Runner{}.Run(t.Context(), program, args...)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if len(result.Stderr) == 0 {
		t.Fatal("the helper produced no stderr, so this proves nothing")
	}
	if strings.Contains(err.Error(), "stderr before exiting") {
		t.Errorf("the error repeats the child's output: %v", err)
	}
}

// The child gets a fixed environment.
//
// Tools translate their messages, and a parser written against the English ones
// silently mis-reads a router set to anything else. Inheriting the daemon's
// environment would also hand a system query whatever proxy or PATH the daemon
// happened to be started with.
func TestTheChildGetsAFixedEnvironment(t *testing.T) {
	t.Setenv("SMART_SRUN_LEAK_CHECK", "should-not-be-visible")
	t.Setenv("LC_ALL", "zh_CN.UTF-8")

	program, args := helperCommand(t, "env")
	result, err := Runner{}.Run(t.Context(), program, args...)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	seen := map[string]string{}
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		if name, value, found := strings.Cut(line, "="); found {
			seen[name] = value
		}
	}
	if _, leaked := seen["SMART_SRUN_LEAK_CHECK"]; leaked {
		t.Error("the daemon's environment reached the child")
	}
	if seen["LC_ALL"] != "C" {
		t.Errorf("LC_ALL = %q, want C; tool output must not be translated",
			seen["LC_ALL"])
	}
}

// An argument containing a NUL cannot be passed to execve at all, so it is
// refused with a reason rather than reaching the syscall.
func TestAnArgumentWithANulByteIsRefused(t *testing.T) {
	program, _ := helperCommand(t)
	_, err := Runner{}.Run(t.Context(), program, helperFlag, "echo", "bad\x00value")
	if err == nil {
		t.Fatal("an argument containing NUL was accepted")
	}
	if code := codeOf(t, err); code != domain.CodeInvalidArgument {
		t.Errorf("code = %s, want InvalidArgument", code)
	}
}
