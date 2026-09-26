package config

import "github.com/matthewlu070111/smart-srun/core/internal/strategy"

// SchoolRegistry resolves which strategy a configuration names.
//
// Two things need it and both live here: school_extra filtering, which has to
// know what the selected strategy declares, and schema publication, which has
// to hand those declarations to the page.
//
// It is a variable rather than a call to strategy.Builtin() at each use because
// spec 07 requires proving that adding a school needs no code -- and a test can
// only prove that with a strategy this build does not compile in. Swap it with
// UseSchoolRegistry, which restores the built-in one when the test ends.
//
// Read-only after construction, so concurrent readers are safe. A test that
// swaps it must not run in parallel with one that reads it.
var SchoolRegistry = strategy.Builtin()

// UseSchoolRegistry installs a registry for the duration of one test.
//
// Declared here rather than in a _test.go file because the daemon and CLI
// packages need it too: an end-to-end check of a declared field is worth
// little if only this package can declare one.
func UseSchoolRegistry(registry *strategy.Registry, restore func(func())) {
	previous := SchoolRegistry
	SchoolRegistry = registry
	restore(func() { SchoolRegistry = previous })
}
