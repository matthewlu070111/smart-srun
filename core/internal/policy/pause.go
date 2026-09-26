package policy

import "slices"

// PauseReason is one reason authentication is not running.
//
// Spec 04 requires a set rather than a single state, because the reasons
// accumulate independently and clearing one must not clear the others. The
// baseline stored one paused flag, and the consequence was reproducible:
// leaving quiet hours resumed an account the user had switched off by hand.
type PauseReason string

const (
	// PauseUserDisabled -- the user turned automatic authentication off. Note
	// that this is not the same as the service being stopped: spec 02 keeps
	// those separate, and the control plane still answers while it is set.
	PauseUserDisabled PauseReason = "UserDisabled"
	// PauseManual -- an explicit pause, distinct from UserDisabled so that
	// resuming one does not resume the other.
	PauseManual PauseReason = "ManualPause"
	// PauseQuietHours -- inside the configured window.
	PauseQuietHours PauseReason = "QuietHours"
	// PauseServiceStopping -- a stop is in progress. Outranks a user action:
	// spec 04 puts the service stop at the top of the priority order.
	PauseServiceStopping PauseReason = "ServiceStopping"
	// PauseUpdateInstalling -- the upgrade freeze. Also outranks user actions,
	// because an install interrupted halfway is worse than a login refused.
	PauseUpdateInstalling PauseReason = "UpdateInstalling"
)

// pauseOrder is both the bit assignment and the reporting order: the reason
// listed last is the one a user is shown when several apply.
var pauseOrder = []PauseReason{
	PauseQuietHours,
	PauseManual,
	PauseUserDisabled,
	PauseUpdateInstalling,
	PauseServiceStopping,
}

// PauseSet is an immutable set of reasons.
//
// A bit mask rather than a map, because a map would be shared by every copy of
// the value: clearing quiet hours on one account's set would silently clear it
// on another's. The set is small and closed, so the mask costs nothing and the
// aliasing bug is unrepresentable.
type PauseSet uint8

func bit(reason PauseReason) PauseSet {
	index := slices.Index(pauseOrder, reason)
	if index < 0 {
		return 0
	}
	return 1 << uint(index)
}

// With returns the set plus reason. An unknown reason is ignored rather than
// silently occupying another reason's bit.
func (s PauseSet) With(reason PauseReason) PauseSet { return s | bit(reason) }

// Without returns the set minus reason.
func (s PauseSet) Without(reason PauseReason) PauseSet { return s &^ bit(reason) }

func (s PauseSet) Has(reason PauseReason) bool {
	mask := bit(reason)
	return mask != 0 && s&mask != 0
}

func (s PauseSet) Empty() bool { return s == 0 }

// Reasons lists the reasons in reporting order.
func (s PauseSet) Reasons() []PauseReason {
	var out []PauseReason
	for _, reason := range pauseOrder {
		if s.Has(reason) {
			out = append(out, reason)
		}
	}
	return out
}

// Primary is the reason to show when several apply: the last one in reporting
// order, which is the one furthest from the user's control.
func (s PauseSet) Primary() (PauseReason, bool) {
	reasons := s.Reasons()
	if len(reasons) == 0 {
		return "", false
	}
	return reasons[len(reasons)-1], true
}

// frozen are the reasons that outrank an explicit user action.
var frozen = []PauseReason{PauseServiceStopping, PauseUpdateInstalling}

// AllowsManual reports whether an explicit user action may still run.
//
// Spec 02 requires it for the disabled case in as many words: enabled=false
// means "do not authenticate on your own", and the control plane still answers
// manual operations. The only things that stop a manual action are the two at
// the top of the priority order.
func (s PauseSet) AllowsManual() bool {
	return !slices.ContainsFunc(frozen, s.Has)
}

// AllowsMaintenance reports whether the automatic loop may run. Any reason at
// all stops it.
func (s PauseSet) AllowsMaintenance() bool { return s.Empty() }

// derivedReasons are recomputed from the world on every evaluation. Everything
// else is sticky: it was set by an explicit request and only an explicit
// request clears it.
var derivedReasons = []PauseReason{PauseUserDisabled, PauseQuietHours}

// Derive recomputes the derived reasons while preserving the sticky ones.
//
// This is the shape T22 is about. Leaving quiet hours removes exactly one
// reason. If the user had also switched automatic authentication off, that
// reason is re-derived from the configuration and stays; if they had paused by
// hand, that reason was never touched. The alternative -- recomputing the whole
// set -- is how the baseline resumed accounts nobody asked it to resume.
func Derive(previous PauseSet, enabled bool, quiet QuietState) PauseSet {
	next := previous
	for _, reason := range derivedReasons {
		next = next.Without(reason)
	}
	if !enabled {
		next = next.With(PauseUserDisabled)
	}
	if quiet.Active {
		next = next.With(PauseQuietHours)
	}
	return next
}
