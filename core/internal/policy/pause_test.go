package policy

import (
	"slices"
	"testing"
)

// T22 -- switching automatic authentication off does not switch the buttons
// off.
//
// Spec 02 keeps the two apart in as many words: enabled=false means "do not
// authenticate on your own", and the control plane still answers manual
// operations. A user who turned the daemon off and then pressed 登录 has been
// perfectly clear about what they want.
func TestAUserActionStillRunsWhileAutomaticAuthenticationIsOff(t *testing.T) {
	paused := PauseSet(0).With(PauseUserDisabled)

	if paused.AllowsMaintenance() {
		t.Error("the automatic loop ran although the user switched it off")
	}
	if !paused.AllowsManual() {
		t.Error("a manual action was refused because automatic authentication " +
			"is off; those are different questions")
	}
	if !paused.With(PauseQuietHours).AllowsManual() {
		t.Error("quiet hours blocked a manual action")
	}
}

// T22 -- the two reasons at the top of the priority order stop everything.
func TestAStopOrAnUpgradeOutranksAUserAction(t *testing.T) {
	for _, reason := range []PauseReason{PauseServiceStopping, PauseUpdateInstalling} {
		set := PauseSet(0).With(reason)
		if set.AllowsManual() {
			t.Errorf("%s allowed a manual action; spec 04 puts it above one", reason)
		}
		if set.AllowsMaintenance() {
			t.Errorf("%s allowed the automatic loop", reason)
		}
	}
}

// T22 -- leaving quiet hours removes exactly one reason.
//
// This is the baseline bug the set replaces. One paused flag meant the end of
// the quiet window resumed accounts the user had switched off by hand, because
// there was nothing left to say why they were paused.
func TestLeavingQuietHoursDoesNotResumeWhatTheUserStopped(t *testing.T) {
	quiet := QuietState{Active: true}
	open := QuietState{}

	// The user switched automatic authentication off, then the window opened.
	inside := Derive(PauseSet(0), false, quiet)
	if !inside.Has(PauseUserDisabled) || !inside.Has(PauseQuietHours) {
		t.Fatalf("both reasons should apply: %v", inside.Reasons())
	}

	after := Derive(inside, false, open)
	if after.Has(PauseQuietHours) {
		t.Error("the quiet-hours reason survived the end of the window")
	}
	if !after.Has(PauseUserDisabled) {
		t.Error("leaving quiet hours resumed an account the user had disabled")
	}
	if after.AllowsMaintenance() {
		t.Error("the automatic loop resumed anyway")
	}

	// And an explicit pause is sticky: it is not derived from anything, so
	// nothing recomputes it away.
	manual := Derive(PauseSet(0).With(PauseManual), true, quiet)
	manual = Derive(manual, true, open)
	if !manual.Has(PauseManual) {
		t.Error("an explicit pause was cleared by the end of the window")
	}
	if manual.Has(PauseQuietHours) || manual.Has(PauseUserDisabled) {
		t.Errorf("a derived reason lingered: %v", manual.Reasons())
	}
	if Derive(manual.Without(PauseManual), true, open) != 0 {
		t.Error("clearing the last reason left something behind")
	}
}

// A set is a value. Two accounts holding one must not share it: clearing quiet
// hours for one would otherwise clear it for the other.
func TestASetIsAValueNotSharedState(t *testing.T) {
	original := PauseSet(0).With(PauseUserDisabled).With(PauseQuietHours)
	copied := original

	copied = copied.Without(PauseQuietHours)
	if !original.Has(PauseQuietHours) {
		t.Error("changing a copy changed the original")
	}
	if copied.Has(PauseQuietHours) {
		t.Error("the copy kept the reason it removed")
	}
	if !copied.Has(PauseUserDisabled) {
		t.Error("removing one reason removed another")
	}
}

// Every reason has its own bit, and an unknown one occupies nobody else's.
func TestEveryReasonIsDistinct(t *testing.T) {
	seen := map[PauseSet]PauseReason{}
	for _, reason := range pauseOrder {
		mask := bit(reason)
		if mask == 0 {
			t.Errorf("%s has no bit", reason)
			continue
		}
		if other, clash := seen[mask]; clash {
			t.Errorf("%s and %s share a bit", reason, other)
		}
		seen[mask] = reason
	}

	unknown := PauseReason("SomethingElse")
	set := PauseSet(0).With(unknown)
	if set != 0 {
		t.Errorf("an unrecognised reason set bits: %08b", set)
	}
	if set.Has(unknown) {
		t.Error("an unrecognised reason reported itself as present")
	}
	if PauseSet(0).With(PauseManual).Without(unknown).Has(PauseManual) != true {
		t.Error("removing an unrecognised reason removed a real one")
	}
}

// The reason shown to a user is the one furthest from their control, so
// "服务停止中" wins over "已暂停".
func TestThePrimaryReasonIsTheLeastRecoverableOne(t *testing.T) {
	if _, ok := PauseSet(0).Primary(); ok {
		t.Error("an empty set reported a reason")
	}

	set := PauseSet(0).With(PauseQuietHours).With(PauseUserDisabled).
		With(PauseServiceStopping)
	primary, ok := set.Primary()
	if !ok || primary != PauseServiceStopping {
		t.Errorf("primary = %q, want %q", primary, PauseServiceStopping)
	}

	reasons := set.Reasons()
	want := []PauseReason{PauseQuietHours, PauseUserDisabled, PauseServiceStopping}
	if !slices.Equal(reasons, want) {
		t.Errorf("reasons = %v, want %v", reasons, want)
	}
	// The order is stable, because a status line that reshuffled between polls
	// would look like something was changing when nothing was.
	for range 5 {
		if !slices.Equal(set.Reasons(), want) {
			t.Fatalf("the order changed between calls: %v", set.Reasons())
		}
	}
}
