package cli

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/strategy"
)

func TestExitCodeForKnownCodes(t *testing.T) {
	cases := map[domain.ErrorCode]int{
		domain.CodeInvalidArgument:       ExitInvalidInput,
		domain.CodeInvalidConfig:         ExitInvalidInput,
		domain.CodeServiceStopped:        ExitServiceStopped,
		domain.CodeConflict:              ExitConflict,
		domain.CodeBusy:                  ExitConflict,
		domain.CodeUnsupportedCapability: ExitUnsupported,
		domain.CodeCancelled:             ExitCancelled,
		domain.CodeAuthRejected:          ExitActionFailed,
		domain.CodeBindingUnavailable:    ExitActionFailed,
	}
	for code, want := range cases {
		if got := ExitCodeFor(domain.Errorf(code, "x")); got != want {
			t.Errorf("%s -> %d, want %d", code, got, want)
		}
	}
	if got := ExitCodeFor(nil); got != ExitOK {
		t.Errorf("nil -> %d, want 0", got)
	}
	// An error from outside this package means the command could not determine
	// what happened. That is a failure, not a success.
	if got := ExitCodeFor(errors.New("boom")); got != ExitActionFailed {
		t.Errorf("unknown error -> %d, want %d", got, ExitActionFailed)
	}
}

// A whole batch of validation problems must map to the same exit code as one of
// them. Reporting more problems is not a different kind of failure.
func TestExitCodeForValidationBatch(t *testing.T) {
	cfg := config.Normalize(config.Defaults())
	cfg.Checks.IntervalSeconds = 0
	cfg.Log.Level = "LOUD"

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	if got := ExitCodeFor(err); got != ExitInvalidInput {
		t.Fatalf("a batch of config problems exited %d, want %d", got, ExitInvalidInput)
	}

	// Wrapping must not change the answer either.
	wrapped := fmt.Errorf("保存配置：%w", err)
	if got := ExitCodeFor(wrapped); got != ExitInvalidInput {
		t.Fatalf("wrapped batch exited %d, want %d", got, ExitInvalidInput)
	}
}

// A strategy that shadowed a core command would break the documented way to do
// the thing that command does.
func TestCoreCommandsAreReserved(t *testing.T) {
	for _, name := range []string{
		"status", "login", "logout", "relogin", "daemon", "schools", "config",
		"switch", "log", "enable", "disable", "help", "man", "update",
		"presets", "detect",
	} {
		if !IsCoreCommand(name) {
			t.Errorf("%q is not reserved", name)
		}
	}
	for _, name := range []string{"", "jxnu", "campus-portal", "Login", "cfg"} {
		if IsCoreCommand(name) {
			t.Errorf("%q was reported reserved", name)
		}
	}
}

// The CLI's view and the registry's view are the same list, not two lists that
// happen to look alike.
//
// They were two copies that had drifted apart by "service" and "version", each
// under a comment promising they could not disagree. A promise nothing checks
// is how they drifted.
func TestCoreCommandsAreExactlyTheRegistrysReservedList(t *testing.T) {
	if !slices.Equal(CoreCommands(), strategy.ReservedCommands) {
		t.Errorf("CoreCommands() = %v, registry reserves %v",
			CoreCommands(), strategy.ReservedCommands)
	}

	// Returned by value: a caller that sorted or truncated the result must not
	// be able to change what the registry refuses.
	got := CoreCommands()
	if len(got) > 0 {
		got[0] = "mutated"
		if strategy.ReservedCommands[0] == "mutated" {
			t.Error("CoreCommands exposes the registry's own slice")
		}
	}
}
