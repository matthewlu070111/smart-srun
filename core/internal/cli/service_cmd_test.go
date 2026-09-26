//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
)

// stoppedService is a lifecycle pointed at a temporary directory holding a
// snapshot from a service that has stopped.
func stoppedService(t *testing.T, snapshot daemon.Snapshot) daemon.Lifecycle {
	t.Helper()
	root := t.TempDir()
	paths := daemon.Paths{
		Runtime: filepath.Join(root, "run"),
		Config:  filepath.Join(root, "etc"),
	}
	if err := daemon.WriteSnapshot(paths, snapshot); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	return daemon.Lifecycle{Paths: paths, Runner: refusingRunner{t: t}}
}

// refusingRunner fails the test if anything tries to run a command. The point
// of the status path is that it does not.
type refusingRunner struct{ t *testing.T }

func (r refusingRunner) Run(_ context.Context, program string,
	args ...string) (openwrt.Result, error) {
	r.t.Errorf("a read-only command ran %s %v", program, args)
	return openwrt.Result{}, nil
}

// T25 -- `srunnet status` reports a stopped service and exits 0.
//
// Zero, not three: a stopped service is a fact, and a script that polls should
// not have to treat "it is off" as an error. `service status` is the one that
// exits 3, because there the question being asked is exactly "is it running".
func TestStatusOnAStoppedServiceReportsItAndSucceeds(t *testing.T) {
	lifecycle := stoppedService(t, daemon.Snapshot{
		Service: daemon.ServiceStopped, Enabled: true, ConfigRevision: 7,
		Accounts: []observe.AccountView{{
			AccountID: "campus", Link: domain.LinkReady,
			Auth: domain.AuthVerifiedSelf,
			Note: &observe.Note{ActionID: "a1", Message: "密码错误"},
		}},
	})

	code, stdout, stderr := capture(t, func(out, errOut *os.File) int {
		return runStatus(t.Context(), lifecycle, nil, out, errOut)
	})

	if code != ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	for _, expected := range []string{"已停止", "自动认证：开", "配置版本：7",
		"campus", "密码错误"} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("the report does not mention %q:\n%s", expected, stdout)
		}
	}
}

// --json puts exactly one document on stdout, which is what the CLI contract
// requires of machine-readable output.
func TestStatusJSONIsOneDocument(t *testing.T) {
	lifecycle := stoppedService(t, daemon.Snapshot{Service: daemon.ServiceStopped})

	code, stdout, stderr := capture(t, func(out, errOut *os.File) int {
		return runStatus(t.Context(), lifecycle, []string{"--json"}, out, errOut)
	})

	if code != ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	var snapshot daemon.Snapshot
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if decoder.More() {
		t.Error("stdout carries more than one document")
	}
	if snapshot.Service != daemon.ServiceStopped {
		t.Errorf("service = %q", snapshot.Service)
	}
	if stderr != "" {
		t.Errorf("diagnostics leaked into the machine-readable path: %s", stderr)
	}
}

// `service status` answers the yes/no question with an exit code, so a shell
// script does not have to parse Chinese.
func TestServiceStatusExitsThreeWhenItIsNotRunning(t *testing.T) {
	lifecycle := stoppedService(t, daemon.Snapshot{Service: daemon.ServiceStopped})

	code, stdout, _ := capture(t, func(out, errOut *os.File) int {
		return runService(t.Context(), lifecycle, []string{"status"}, out, errOut)
	})

	if code != ExitServiceStopped {
		t.Errorf("exit %d, want %d", code, ExitServiceStopped)
	}
	if !strings.Contains(stdout, "已停止") {
		t.Errorf("stdout = %q", stdout)
	}
}

// The helper takes one sub-command and nothing else. It is not a general
// executor and must not start looking like one.
func TestTheServiceHelperRefusesAnythingElse(t *testing.T) {
	lifecycle := stoppedService(t, daemon.Snapshot{Service: daemon.ServiceStopped})

	for _, args := range [][]string{
		nil,
		{"restart"},
		{"ensure-running", "network"},
		{"stop", "--force"},
		{"status", "extra"},
	} {
		code, _, stderr := capture(t, func(out, errOut *os.File) int {
			return runService(t.Context(), lifecycle, args, out, errOut)
		})
		if code != ExitInvalidInput {
			t.Errorf("%v gave exit %d, want %d", args, code, ExitInvalidInput)
		}
		if stderr == "" {
			t.Errorf("%v was refused without saying why", args)
		}
	}
}

func TestStatusRefusesUnknownFlags(t *testing.T) {
	lifecycle := stoppedService(t, daemon.Snapshot{Service: daemon.ServiceStopped})

	code, _, stderr := capture(t, func(out, errOut *os.File) int {
		return runStatus(t.Context(), lifecycle, []string{"--verbose"}, out, errOut)
	})
	if code != ExitInvalidInput {
		t.Errorf("exit %d, want %d", code, ExitInvalidInput)
	}
	if !strings.Contains(stderr, "用法") {
		t.Errorf("stderr = %q", stderr)
	}
}
