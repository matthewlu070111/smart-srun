package policy

// Priority orders the work the coordinator has to choose between.
//
// Spec 04 fixes the order, and the reason it is a list rather than a rule of
// thumb is that every pair in it has been got wrong somewhere: a status poll
// delaying a login, a quiet-hours logout racing a user's manual login, a
// service stop losing to a retry that had already been scheduled.
//
// Higher wins.
type Priority int

const (
	// PriorityPeriodic -- observation ticks and preset refreshes. Nothing a
	// user is waiting for.
	PriorityPeriodic Priority = iota + 1
	// PriorityMaintenance -- the automatic authentication loop.
	PriorityMaintenance
	// PriorityQuietBoundary -- the forced logout at the edge of quiet hours.
	// Above maintenance so the sweep is not starved by the loop it suspends.
	PriorityQuietBoundary
	// PriorityUserAction -- an explicit login, logout, switch or relogin. Above
	// the quiet boundary: a user pressing a button at 02:00 outranks the
	// schedule, for that action.
	PriorityUserAction
	// PriorityRecovery -- wireless recovery and explicit cancellation. Above
	// user actions because a cancellation is the user changing their mind about
	// one of them, and recovery is what makes the line usable at all.
	PriorityRecovery
	// PriorityFreeze -- service stop and the upgrade freeze. Nothing outranks
	// these: an upgrade that loses to a retry loop is an upgrade that never
	// finishes.
	PriorityFreeze
)

// String is used in diagnostics, so the names are the ones spec 04 uses.
func (p Priority) String() string {
	switch p {
	case PriorityPeriodic:
		return "periodic"
	case PriorityMaintenance:
		return "maintenance"
	case PriorityQuietBoundary:
		return "quiet-boundary"
	case PriorityUserAction:
		return "user-action"
	case PriorityRecovery:
		return "recovery"
	case PriorityFreeze:
		return "freeze"
	default:
		return "unknown"
	}
}
