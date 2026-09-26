package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/cli"
)

// capture runs the dispatcher with stdout and stderr on temporary files.
//
// Only commands that do not touch the runtime directory are exercised here: the
// behaviour of the ones that do is tested where it lives, and a unit test that
// reached into /var/run would be testing the machine it happened to run on.
func capture(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	open := func(name string) *os.File {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return file
	}
	stdout, stderr := open("stdout"), open("stderr")
	code := run(t.Context(), args, stdout, stderr)
	stdout.Close()
	stderr.Close()

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	return code, read("stdout"), read("stderr")
}

func TestVersionAndHelpAnswerOnStdout(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-V"}} {
		code, stdout, stderr := capture(t, args)
		if code != cli.ExitOK {
			t.Errorf("%v: exit %d, stderr: %s", args, code, stderr)
		}
		if !strings.Contains(stdout, "srunnet") {
			t.Errorf("%v printed %q", args, stdout)
		}
	}

	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		code, stdout, _ := capture(t, args)
		if code != cli.ExitOK {
			t.Errorf("%v: exit %d", args, code)
		}
		// The help is the list of what this build can do, so it has to name the
		// commands the dispatcher actually accepts.
		for _, command := range []string{"status", "service", "daemon", "config"} {
			if !strings.Contains(stdout, command) {
				t.Errorf("help does not mention %q", command)
			}
		}
	}
}

// A known command missing its subcommand gets actionable usage, while a typo
// still reports an unknown command.
func TestIncompleteAndUnknownCommandsDiffer(t *testing.T) {
	code, _, stderr := capture(t, []string{"update"})
	if code != cli.ExitInvalidInput {
		t.Errorf("incomplete update exited %d, want %d", code, cli.ExitInvalidInput)
	}
	if !strings.Contains(stderr, "update check") {
		t.Errorf("stderr = %q", stderr)
	}

	code, _, stderr = capture(t, []string{"lgoin"})
	if code != cli.ExitInvalidInput {
		t.Errorf("an unknown command exited %d, want %d", code, cli.ExitInvalidInput)
	}
	if !strings.Contains(stderr, "未知命令") {
		t.Errorf("stderr = %q", stderr)
	}
}

// config runs offline, with the daemon stopped, which is what the spec requires
// of it and what makes it usable when something is wrong.
func TestConfigRunsWithoutTheDaemon(t *testing.T) {
	code, stdout, stderr := capture(t, []string{"config", "defaults"})
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"schema_version"`) {
		t.Errorf("stdout = %q", stdout)
	}

	code, _, stderr = capture(t, []string{"config", "nonsense"})
	if code != cli.ExitInvalidInput {
		t.Errorf("exit %d, want %d", code, cli.ExitInvalidInput)
	}
	if !strings.Contains(stderr, "未知子命令") {
		t.Errorf("stderr = %q", stderr)
	}
}

// Every command the dispatcher answers is one the reserved list knows about, so
// a school strategy cannot claim it later.
func TestEveryDispatchedCommandIsReserved(t *testing.T) {
	for _, command := range []string{"status", "service", "daemon", "config",
		"version", "help"} {
		if !cli.IsCoreCommand(command) {
			t.Errorf("%q is dispatched but not reserved; a strategy could claim it",
				command)
		}
	}
}
