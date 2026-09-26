package config

import (
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func threeAccounts() domain.Config {
	return domain.Config{CampusAccounts: []domain.CampusAccount{
		{ID: "first"}, {ID: "chosen"}, {ID: "other"},
	}}
}

// Deleting the active account lands on the nominated default, not on whichever
// account happens to be first.
//
// Before this, the default was repaired *from* the active one and never
// consulted in the other direction, so nominating an account changed nothing
// the daemon ever did.
func TestRepairPrefersTheNominatedDefaultOverTheFirstAccount(t *testing.T) {
	cfg := threeAccounts()
	cfg.Selection.ActiveCampusID = "gone"
	cfg.Selection.DefaultCampusID = "chosen"

	RepairSelection(&cfg)

	if cfg.Selection.ActiveCampusID != "chosen" {
		t.Errorf("active = %q, want the nominated default", cfg.Selection.ActiveCampusID)
	}
	if cfg.Selection.DefaultCampusID != "chosen" {
		t.Errorf("default = %q, want it kept", cfg.Selection.DefaultCampusID)
	}
}

// A default that no longer names an account must not block the repair.
func TestADanglingDefaultFallsThroughToTheFirstAccount(t *testing.T) {
	cfg := threeAccounts()
	cfg.Selection.ActiveCampusID = "gone"
	cfg.Selection.DefaultCampusID = "also-gone"

	RepairSelection(&cfg)

	if cfg.Selection.ActiveCampusID != "first" {
		t.Errorf("active = %q, want the first account", cfg.Selection.ActiveCampusID)
	}
	if cfg.Selection.DefaultCampusID != "first" {
		t.Errorf("default = %q, want it repaired to the active one", cfg.Selection.DefaultCampusID)
	}
}

// A live active pointer is never moved, whatever the default says. Repair is
// for pointers that stopped naming something, not a policy that overrides the
// account the user is actually on.
func TestALiveActivePointerIsLeftAlone(t *testing.T) {
	cfg := threeAccounts()
	cfg.Selection.ActiveCampusID = "other"
	cfg.Selection.DefaultCampusID = "chosen"

	RepairSelection(&cfg)

	if cfg.Selection.ActiveCampusID != "other" {
		t.Errorf("active = %q, want it untouched", cfg.Selection.ActiveCampusID)
	}
	if cfg.Selection.DefaultCampusID != "chosen" {
		t.Errorf("default = %q, want it untouched", cfg.Selection.DefaultCampusID)
	}
}

// Hotspots get the same treatment, and an empty list still clears both.
func TestHotspotRepairUsesTheDefaultAndClearsWhenEmpty(t *testing.T) {
	cfg := domain.Config{HotspotProfiles: []domain.HotspotProfile{
		{ID: "h-first"}, {ID: "h-chosen"},
	}}
	cfg.Selection.ActiveHotspotID = "gone"
	cfg.Selection.DefaultHotspotID = "h-chosen"
	RepairSelection(&cfg)
	if cfg.Selection.ActiveHotspotID != "h-chosen" {
		t.Errorf("active hotspot = %q, want the nominated default", cfg.Selection.ActiveHotspotID)
	}

	empty := domain.Config{}
	empty.Selection.ActiveCampusID = "gone"
	empty.Selection.DefaultCampusID = "also-gone"
	empty.Selection.ActiveHotspotID = "gone"
	empty.Selection.DefaultHotspotID = "also-gone"
	RepairSelection(&empty)
	if empty.Selection != (domain.Selection{}) {
		t.Errorf("pointers = %+v, want all cleared when nothing is configured", empty.Selection)
	}
}
