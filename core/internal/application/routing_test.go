package application

import (
	"slices"
	"testing"
)

// Every kind is either performed here or claimed by a decorator.
//
// The worker's switch ends in a default that tells the user an action has no
// executor. Without this test that default is where a kind lands when someone
// adds it, wires the RPC that submits it, and forgets the router -- and the
// only symptom is a failed action at runtime, on a router, in Chinese.
func TestEveryKindIsEitherHandledHereOrRoutedElsewhere(t *testing.T) {
	covered := append(HandledHere(), RoutedElsewhere()...)

	for _, kind := range kinds {
		if !slices.Contains(covered, kind) {
			t.Errorf("kind %q has no executor: add it to the worker's switch, "+
				"or to RoutedElsewhere together with the decorator that claims it",
				kind)
		}
	}

	// The other direction: a kind listed as covered but no longer real would
	// make the check above pass while describing something that cannot happen.
	for _, kind := range covered {
		if !slices.Contains(kinds, kind) {
			t.Errorf("kind %q is listed as covered but is not in kinds", kind)
		}
	}

	if len(covered) != len(kinds) {
		t.Errorf("covered %d kinds, kinds has %d; a kind is listed twice",
			len(covered), len(kinds))
	}
}

// The two lists must not overlap: a kind both performed here and intercepted by
// a decorator means one of the two never runs, and which one depends on the
// order the decorators were assembled in.
func TestHandledAndRoutedDoNotOverlap(t *testing.T) {
	for _, kind := range HandledHere() {
		if slices.Contains(RoutedElsewhere(), kind) {
			t.Errorf("kind %q is claimed twice", kind)
		}
	}
}
