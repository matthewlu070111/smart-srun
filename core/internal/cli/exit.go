// Package cli holds command parsing and presentation.
//
// The daemon never re-parses a command tree: one definition, used by the CLI
// and by whatever the RPC layer needs to know about command names.
package cli

import (
	"slices"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/strategy"
)

// Exit codes, fixed by spec 03. A script can branch on these without parsing
// Chinese text.
const (
	ExitOK             = 0
	ExitInvalidInput   = 2
	ExitServiceStopped = 3
	ExitActionFailed   = 4
	ExitConflict       = 5
	ExitUnsupported    = 6
	ExitCancelled      = 130
)

// ExitCodeFor maps an error to its process exit code.
//
// Unknown errors are action failures, not successes: a command that could not
// determine what happened must not report that nothing went wrong.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	code, ok := domain.CodeOf(err)
	if !ok {
		return ExitActionFailed
	}
	switch code {
	case domain.CodeInvalidArgument, domain.CodeInvalidConfig:
		return ExitInvalidInput
	case domain.CodeServiceStopped:
		return ExitServiceStopped
	case domain.CodeConflict, domain.CodeBusy:
		return ExitConflict
	case domain.CodeUnsupportedCapability:
		return ExitUnsupported
	case domain.CodeCancelled:
		return ExitCancelled
	default:
		return ExitActionFailed
	}
}

// CoreCommands are the command names a school strategy may never claim.
//
// Returned from strategy.ReservedCommands rather than restated, so the registry
// and the CLI cannot disagree about what is reserved. They previously each kept
// their own copy under a comment making this same promise, and the copies had
// drifted apart by two entries.
func CoreCommands() []string {
	return slices.Clone(strategy.ReservedCommands)
}

// IsCoreCommand reports whether name is reserved.
func IsCoreCommand(name string) bool {
	return slices.Contains(CoreCommands(), name)
}
