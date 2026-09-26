package openwrt

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests in this package run real processes rather than a stand-in for one.
//
// A fake that returns canned output cannot show that a timeout actually reaps a
// child, that a process group dies with its leader, or that an argument
// containing a semicolon reaches the program as one argument. Those are
// properties of the operating system, and only the operating system can
// demonstrate them. The card for this work says as much: simulated commands
// must not be the only evidence.
//
// The helper is this test binary re-executed. It cannot be selected with an
// environment variable, because Runner gives every child a fixed environment on
// purpose, so the selection travels in argv -- which also happens to exercise
// the argv path the real tools use.
const helperFlag = "-openwrt-helper"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == helperFlag {
		os.Exit(runHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// helperCommand builds the argv that re-enters this binary as the helper.
func helperCommand(t *testing.T, args ...string) (string, []string) {
	t.Helper()
	// Under user-mode QEMU the Runner's syscalls are emulated, but execve
	// still enters the host kernel. Use a native copy of this same helper,
	// without changing the Runner or weakening argv/environment/kill checks.
	if host := os.Getenv("SMARTSRUN_TEST_HELPER_BIN"); host != "" {
		return host, append([]string{helperFlag}, args...)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	return self, append([]string{helperFlag}, args...)
}

func runHelper(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "helper: no mode")
		return 2
	}
	switch args[0] {
	case "stdin":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 2
		}
		fmt.Println(strings.Join(args[1:], "|"))
		fmt.Print(string(data))
		return 0
	case "echo":
		// Each argument on its own line, so a test can see exactly how the
		// operating system split them.
		for _, arg := range args[1:] {
			fmt.Println(arg)
		}
		return 0

	case "env":
		for _, entry := range os.Environ() {
			fmt.Println(entry)
		}
		return 0

	case "exit":
		code, _ := strconv.Atoi(args[1])
		fmt.Println("stdout before exiting")
		fmt.Fprintln(os.Stderr, "stderr before exiting")
		return code

	case "spew":
		count, _ := strconv.Atoi(args[1])
		chunk := strings.Repeat("x", 1024)
		for written := 0; written < count; written += len(chunk) {
			fmt.Print(chunk)
		}
		return 0

	case "sleep":
		milliseconds, _ := strconv.Atoi(args[1])
		time.Sleep(time.Duration(milliseconds) * time.Millisecond)
		fmt.Println("finished sleeping")
		return 0

	case "orphan":
		// Start a grandchild that inherits stdout and outlives this process,
		// then exit at once. Anything that waits for the pipe to close instead
		// of for the process to exit now waits for the grandchild.
		self, err := os.Executable()
		if err != nil {
			return 2
		}
		child := exec.Command(self, helperFlag, "hold", args[1])
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			return 2
		}
		fmt.Println("parent done")
		return 0

	case "hold":
		// Hold the inherited stdout open without writing to it.
		milliseconds, _ := strconv.Atoi(args[1])
		time.Sleep(time.Duration(milliseconds) * time.Millisecond)
		return 0

	case "group":
		// Start a grandchild in the same process group, record its pid where
		// the test can find it, and then block. Killing only the direct child
		// leaves the grandchild running.
		self, err := os.Executable()
		if err != nil {
			return 2
		}
		child := exec.Command(self, helperFlag, "hold-quiet", args[2])
		if err := child.Start(); err != nil {
			return 2
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			return 2
		}
		time.Sleep(time.Duration(60) * time.Second)
		return 0

	case "hold-quiet":
		milliseconds, _ := strconv.Atoi(args[1])
		time.Sleep(time.Duration(milliseconds) * time.Millisecond)
		return 0

	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", args[0])
		return 2
	}
}
