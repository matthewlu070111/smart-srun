package strategy

// DefaultID is the strategy every router uses unless a school needs otherwise.
const DefaultID = "default"

// Default is the general SRun strategy.
//
// It declares no private fields and no extra commands, and that is the point
// spec 07 is making: the protocol parameters that vary between schools -- n,
// type, enc, the info prefix, the operator suffix, the login shape -- are all
// account configuration, not code. A school is a row in a preset catalogue,
// not a module.
//
// The extension surface below it exists for the cases that genuinely differ,
// and the test strategy in the tests is what shows a school can be added
// without writing any.
func Default() Strategy {
	return Strategy{
		ID:    DefaultID,
		Label: "通用深澜认证",
	}
}

// Builtin returns a registry holding the strategies compiled into this build.
//
// One function, called once at startup, so "which strategies exist" is
// answerable by reading this file rather than by tracing init() side effects
// across packages.
func Builtin() *Registry {
	registry := NewRegistry()
	registry.MustRegister(Default())
	return registry
}
