package config

import (
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/strategy"
)

// apply runs one change through a repository and returns the result, so every
// test below goes through the same transaction the real callers use.
func apply(t *testing.T, repository *Repository, change Change) domain.Config {
	t.Helper()
	cfg, err := repository.Update(repository.Revision(), change)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return cfg
}

func emptyRepository(t *testing.T) *Repository {
	t.Helper()
	repository, err := Open(tempConfigPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return repository
}

func newAccountPatch() CampusPatch {
	return CampusPatch{
		Label:      stringPtr("宿舍"),
		UserID:     stringPtr("2020123456"),
		Password:   stringPtr("pw123456"),
		AccessMode: stringPtr("wired"),
		WiredIface: stringPtr("wan"),
		BaseURL:    stringPtr("http://10.0.0.1"),
		ACID:       stringPtr("1"),
	}
}

// T08 -- the rule the whole patch type exists for. An account dialog that does
// not display the password submits no password field, and must not clear it.
func TestOmittingASecretKeepsIt(t *testing.T) {
	repository := emptyRepository(t)
	created := apply(t, repository, UpsertCampus(newAccountPatch()))
	id := created.CampusAccounts[0].ID

	// Edit only the label. No password field at all.
	edited := apply(t, repository, UpsertCampus(CampusPatch{
		ID:    id,
		Label: stringPtr("改名后"),
	}))

	account := edited.CampusAccounts[0]
	if account.Label != "改名后" {
		t.Fatalf("label = %q", account.Label)
	}
	if account.Password != "pw123456" {
		t.Fatalf("password = %q; omitting the field must keep it", account.Password)
	}
	if account.ID != id {
		t.Fatalf("id changed from %q to %q; renaming is not a new identity", id, account.ID)
	}
}

// The other half of the rule: an explicitly empty string does clear it. Sending
// the field is the deliberate act.
func TestAnExplicitEmptySecretClearsIt(t *testing.T) {
	repository := emptyRepository(t)
	created := apply(t, repository, UpsertCampus(newAccountPatch()))
	id := created.CampusAccounts[0].ID

	edited := apply(t, repository, UpsertCampus(CampusPatch{
		ID:       id,
		Password: stringPtr(""),
	}))
	if edited.CampusAccounts[0].Password != "" {
		t.Fatalf("password = %q, want cleared", edited.CampusAccounts[0].Password)
	}
}

// T08 -- a placeholder is a value, not a signal. A user whose password really
// is that string has to be able to keep it.
func TestAPlaceholderIsStoredAsTheLiteralPassword(t *testing.T) {
	repository := emptyRepository(t)
	created := apply(t, repository, UpsertCampus(newAccountPatch()))
	id := created.CampusAccounts[0].ID

	edited := apply(t, repository, UpsertCampus(CampusPatch{
		ID:       id,
		Password: stringPtr("******"),
	}))
	if edited.CampusAccounts[0].Password != "******" {
		t.Fatalf("password = %q; the patch must not interpret placeholders",
			edited.CampusAccounts[0].Password)
	}
}

// T08 -- secrets are stored exactly as typed. Trimming turns a working
// credential into a failing one that looks right in the form.
func TestSecretsKeepTheirSurroundingSpace(t *testing.T) {
	repository := emptyRepository(t)
	patch := newAccountPatch()
	patch.Password = stringPtr("  pw with space  ")
	created := apply(t, repository, UpsertCampus(patch))

	if got := created.CampusAccounts[0].Password; got != "  pw with space  " {
		t.Fatalf("password = %q", got)
	}
}

// A per-account override is a tri-state: unset, true, or an explicit false.
// Presence and value are separate questions, so the patch nests two pointers.
func TestExplicitFalseAndClearingAreDifferentEdits(t *testing.T) {
	repository := emptyRepository(t)
	created := apply(t, repository, UpsertCampus(newAccountPatch()))
	id := created.CampusAccounts[0].ID
	if created.CampusAccounts[0].Login.DoubleStack != nil {
		t.Fatal("a new account should not carry an override")
	}

	enabled := boolPtr(false)
	withFalse := apply(t, repository, UpsertCampus(CampusPatch{
		ID: id, Login: &LoginPatch{DoubleStack: &enabled},
	}))
	stored := withFalse.CampusAccounts[0].Login.DoubleStack
	if stored == nil || *stored != false {
		t.Fatalf("an explicit false did not survive: %v", stored)
	}

	var cleared *bool
	back := apply(t, repository, UpsertCampus(CampusPatch{
		ID: id, Login: &LoginPatch{DoubleStack: &cleared},
	}))
	if back.CampusAccounts[0].Login.DoubleStack != nil {
		t.Fatal("an explicit null did not clear the override")
	}

	// And leaving the login patch out entirely changes nothing.
	untouched := apply(t, repository, UpsertCampus(CampusPatch{
		ID: id, Label: stringPtr("x"),
	}))
	if untouched.CampusAccounts[0].Login.DoubleStack != nil {
		t.Fatal("an omitted login patch reintroduced an override")
	}
}

// T11 -- the first account has to be usable without a second, separate action.
func TestTheFirstAccountBecomesActiveAndDefault(t *testing.T) {
	repository := emptyRepository(t)
	cfg := apply(t, repository, UpsertCampus(newAccountPatch()))

	id := cfg.CampusAccounts[0].ID
	if cfg.Selection.ActiveCampusID != id || cfg.Selection.DefaultCampusID != id {
		t.Fatalf("selection = %+v, want both pointing at %q", cfg.Selection, id)
	}

	// A second one does not steal the pointers.
	second := newAccountPatch()
	second.Label = stringPtr("第二个")
	cfg = apply(t, repository, UpsertCampus(second))
	if cfg.Selection.DefaultCampusID != id {
		t.Fatalf("adding an account moved the default to %q", cfg.Selection.DefaultCampusID)
	}
}

// T11 -- deleting the account the pointers refer to must leave a configuration
// that is still valid, not one that fails its own reference check.
func TestDeletingTheDefaultAccountRepairsThePointers(t *testing.T) {
	repository := emptyRepository(t)
	first := apply(t, repository, UpsertCampus(newAccountPatch()))
	firstID := first.CampusAccounts[0].ID

	second := newAccountPatch()
	second.Label = stringPtr("第二个")
	withTwo := apply(t, repository, UpsertCampus(second))
	secondID := withTwo.CampusAccounts[1].ID

	after := apply(t, repository, RemoveCampus(firstID))
	if len(after.CampusAccounts) != 1 {
		t.Fatalf("%d accounts left", len(after.CampusAccounts))
	}
	if after.Selection.ActiveCampusID != secondID {
		t.Errorf("active = %q, want the remaining account %q",
			after.Selection.ActiveCampusID, secondID)
	}
	if after.Selection.DefaultCampusID != secondID {
		t.Errorf("default = %q, want it to follow active", after.Selection.DefaultCampusID)
	}
	if err := Validate(after); err != nil {
		t.Fatalf("the configuration after a delete does not validate: %v", err)
	}

	// Removing the last one empties the pointers rather than leaving them
	// dangling.
	empty := apply(t, repository, RemoveCampus(secondID))
	if empty.Selection.ActiveCampusID != "" || empty.Selection.DefaultCampusID != "" {
		t.Fatalf("selection = %+v, want empty", empty.Selection)
	}
	if err := Validate(empty); err != nil {
		t.Fatalf("an empty configuration does not validate: %v", err)
	}
}

// Setting the default also switches to it, which is what the button appears to
// do; leaving active behind would make it look like nothing happened.
func TestSettingTheDefaultAlsoSwitchesToIt(t *testing.T) {
	repository := emptyRepository(t)
	apply(t, repository, UpsertCampus(newAccountPatch()))
	second := newAccountPatch()
	second.Label = stringPtr("第二个")
	withTwo := apply(t, repository, UpsertCampus(second))
	secondID := withTwo.CampusAccounts[1].ID

	after := apply(t, repository, SetDefaultCampus(secondID))
	if after.Selection.DefaultCampusID != secondID || after.Selection.ActiveCampusID != secondID {
		t.Fatalf("selection = %+v, want both at %q", after.Selection, secondID)
	}
}

// T11 -- identifiers are unique within the configuration, and adding an account
// never collides with one that is still there.
//
// They are not permanently reserved: removing the highest one frees it again.
// That is a deliberate limit, documented on nextID -- reserving identifiers
// forever would mean a counter in the configuration file, which the on-disk
// contract does not declare. A queued action outliving its account is handled
// by revision, not by name scarcity.
func TestIdentifiersAreUniqueAndDoNotCollideWithLiveEntries(t *testing.T) {
	repository := emptyRepository(t)

	first := apply(t, repository, UpsertCampus(newAccountPatch()))
	if got := first.CampusAccounts[0].ID; got != "c1" {
		t.Fatalf("first id = %q, want c1", got)
	}

	second := apply(t, repository, UpsertCampus(newAccountPatch()))
	if got := second.CampusAccounts[1].ID; got != "c2" {
		t.Fatalf("second id = %q, want c2", got)
	}

	// Deleting from the middle must not hand out an identifier that is still in
	// use, which is the collision that would actually corrupt a reference.
	apply(t, repository, RemoveCampus("c1"))
	third := apply(t, repository, UpsertCampus(newAccountPatch()))
	if got := third.CampusAccounts[1].ID; got != "c3" {
		t.Fatalf("third id = %q, want c3", got)
	}

	seen := map[string]bool{}
	for _, account := range third.CampusAccounts {
		if seen[account.ID] {
			t.Fatalf("duplicate id %q", account.ID)
		}
		seen[account.ID] = true
	}
	if err := Validate(third); err != nil {
		t.Fatalf("validation rejects the result: %v", err)
	}
}

// Every kind gets its own sequence, and a hand-written identifier that collides
// with the next generated one is stepped over.
func TestIdentifierAllocation(t *testing.T) {
	if got := nextID(nil, "c"); got != "c1" {
		t.Errorf("nextID(empty) = %q", got)
	}
	if got := nextID([]string{"c1", "c2"}, "c"); got != "c3" {
		t.Errorf("nextID(c1,c2) = %q", got)
	}
	if got := nextID([]string{"c7"}, "c"); got != "c8" {
		t.Errorf("nextID(c7) = %q", got)
	}
	// A user-chosen identifier that is not in the generated shape is skipped
	// when counting but must still not be collided with.
	if got := nextID([]string{"dorm", "c1"}, "c"); got != "c2" {
		t.Errorf("nextID(dorm,c1) = %q", got)
	}
	if got := nextID([]string{"c2"}, "c"); got != "c3" {
		t.Errorf("nextID(c2) = %q", got)
	}
	if got := nextID([]string{"h1"}, "h"); got != "h2" {
		t.Errorf("nextID(h1) = %q", got)
	}
}

func TestEditingAMissingEntryIsNotFound(t *testing.T) {
	repository := emptyRepository(t)

	for name, change := range map[string]Change{
		"edit a campus account":    UpsertCampus(CampusPatch{ID: "nope", Label: stringPtr("x")}),
		"delete a campus account":  RemoveCampus("nope"),
		"default a campus account": SetDefaultCampus("nope"),
		"edit a hotspot":           UpsertHotspot(HotspotPatch{ID: "nope", Label: stringPtr("x")}),
		"delete a hotspot":         RemoveHotspot("nope"),
		"default a hotspot":        SetDefaultHotspot("nope"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := repository.Update(repository.Revision(), change)
			if err == nil {
				t.Fatal("accepted")
			}
			if code, _ := domain.CodeOf(err); code != domain.CodeNotFound {
				t.Fatalf("code = %q, want NotFound", code)
			}
		})
	}
}

// Hotspots follow the same rules; they are a separate list with separate
// pointers, and the two must not interfere.
func TestHotspotsHaveTheirOwnPointers(t *testing.T) {
	repository := emptyRepository(t)
	apply(t, repository, UpsertCampus(newAccountPatch()))

	cfg := apply(t, repository, UpsertHotspot(HotspotPatch{
		Label: stringPtr("手机热点"), SSID: stringPtr("iPhone"),
		Encryption: stringPtr("psk2"), Key: stringPtr("hotspotpw"),
	}))
	hotspotID := cfg.HotspotProfiles[0].ID
	if hotspotID != "h1" {
		t.Fatalf("hotspot id = %q, want h1", hotspotID)
	}
	if cfg.Selection.ActiveHotspotID != hotspotID ||
		cfg.Selection.DefaultHotspotID != hotspotID {
		t.Fatalf("hotspot selection = %+v", cfg.Selection)
	}
	if cfg.Selection.ActiveCampusID != "c1" {
		t.Error("adding a hotspot disturbed the campus pointers")
	}

	after := apply(t, repository, RemoveHotspot(hotspotID))
	if after.Selection.ActiveHotspotID != "" || after.Selection.DefaultHotspotID != "" {
		t.Fatalf("hotspot selection = %+v after delete, want empty", after.Selection)
	}
	if after.Selection.ActiveCampusID != "c1" {
		t.Error("removing a hotspot disturbed the campus pointers")
	}
}

// Saving the settings page must be incapable of touching a credential, because
// the page never shows one. This is structural: Settings has no account fields.
func TestSavingSettingsCannotTouchAnAccount(t *testing.T) {
	repository := emptyRepository(t)
	created := apply(t, repository, UpsertCampus(newAccountPatch()))
	before := created.CampusAccounts[0]

	settings := SettingsOf(created)
	settings.Enabled = true
	settings.Checks.IntervalSeconds = 120
	after := apply(t, repository, ApplySettings(settings))

	if !after.Enabled || after.Checks.IntervalSeconds != 120 {
		t.Fatalf("the settings were not applied: %+v", after.Checks)
	}
	if len(after.CampusAccounts) != 1 {
		t.Fatalf("the account list changed: %d entries", len(after.CampusAccounts))
	}
	if after.CampusAccounts[0] != before {
		t.Fatalf("the account changed:\n got %+v\nwant %+v", after.CampusAccounts[0], before)
	}
	if after.Selection != created.Selection {
		t.Fatalf("the selection changed: %+v", after.Selection)
	}
}

// Reading the settings out and writing them straight back must change nothing
// but the revision, or every page load would rewrite the user's configuration.
func TestSettingsRoundTripChangesOnlyTheRevision(t *testing.T) {
	repository := emptyRepository(t)
	created := apply(t, repository, UpsertCampus(newAccountPatch()))

	after := apply(t, repository, ApplySettings(SettingsOf(created)))

	expected := created
	expected.Revision = after.Revision
	if !configsEqual(after, expected) {
		t.Fatalf("a no-op settings save changed something:\n got %+v\nwant %+v",
			after, expected)
	}
}

func configsEqual(a, b domain.Config) bool {
	left, errLeft := Marshal(a)
	right, errRight := Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}

// Strategy-private values belong to the strategy that declared them. Carrying
// them across a switch would hand the new one parameters it cannot interpret.
//
// Both schools here are test strategies, declared and not compiled in: spec 07
// asks for proof that adding a school needs no code, and a test that used the
// built-in strategy could not provide it -- that one declares no fields, so
// every key would be dropped for the wrong reason.
func TestSwitchingSchoolDropsThePreviousStrategysPrivateValues(t *testing.T) {
	useTestStrategies(t,
		strategy.Strategy{ID: "north-campus", Label: "北区",
			Fields: []strategy.Field{{Key: "campus_zone", Label: "校区", Kind: strategy.FieldString}}},
		strategy.Strategy{ID: "another-school", Label: "另一所",
			Fields: []strategy.Field{{Key: "other_key", Label: "其它", Kind: strategy.FieldBool}}},
	)
	repository := emptyRepository(t)

	// Selecting the school is its own save: ApplySettings clears the map on a
	// switch, so a save that changed both at once could never store anything.
	selecting := SettingsOf(repository.Snapshot())
	selecting.School = "north-campus"
	selected := apply(t, repository, ApplySettings(selecting))

	start := SettingsOf(selected)
	start.SchoolExtra = map[string]any{"campus_zone": "north"}
	stored := apply(t, repository, ApplySettings(start))
	if stored.SchoolExtra["campus_zone"] != "north" {
		t.Fatalf("a declared key was not stored: %+v", stored.SchoolExtra)
	}

	switching := SettingsOf(stored)
	switching.School = "another-school"
	after := apply(t, repository, ApplySettings(switching))

	if len(after.SchoolExtra) != 0 {
		t.Fatalf("school_extra survived a strategy switch: %+v", after.SchoolExtra)
	}

	// Saving again without changing the school keeps what the new strategy set.
	same := SettingsOf(after)
	same.SchoolExtra = map[string]any{"other_key": true}
	kept := apply(t, repository, ApplySettings(same))
	if kept.SchoolExtra["other_key"] != true {
		t.Fatalf("a declared key was dropped without a switch: %+v", kept.SchoolExtra)
	}

	// And a key the selected strategy never declared does not survive at all.
	undeclared := SettingsOf(kept)
	undeclared.SchoolExtra = map[string]any{"other_key": true, "smuggled": "x"}
	filtered := apply(t, repository, ApplySettings(undeclared))
	if _, present := filtered.SchoolExtra["smuggled"]; present {
		t.Errorf("an undeclared key was stored: %+v", filtered.SchoolExtra)
	}
	if filtered.SchoolExtra["other_key"] != true {
		t.Errorf("the declared key was lost alongside it: %+v", filtered.SchoolExtra)
	}
}

// useTestStrategies installs a registry holding only the given strategies.
func useTestStrategies(t *testing.T, items ...strategy.Strategy) {
	t.Helper()
	registry := strategy.NewRegistry()
	registry.MustRegister(strategy.Default())
	for _, item := range items {
		if err := registry.Register(item); err != nil {
			t.Fatalf("register %s: %v", item.ID, err)
		}
	}
	UseSchoolRegistry(registry, t.Cleanup)
}

// The wireless half of an account must not survive a switch to wired, or the
// stored account carries an effective configuration for a mode it is not in.
func TestSwitchingAccessModeClearsTheOtherHalf(t *testing.T) {
	repository := emptyRepository(t)

	wireless := apply(t, repository, UpsertCampus(CampusPatch{
		Label: stringPtr("无线"), UserID: stringPtr("2020123456"),
		Password: stringPtr("pw"), AccessMode: stringPtr("wifi"),
		SSID: stringPtr("campus"), Encryption: stringPtr("psk2"),
		Key: stringPtr("wifikey"), BaseURL: stringPtr("http://10.0.0.1"),
		ACID: stringPtr("1"),
	}))
	id := wireless.CampusAccounts[0].ID
	if wireless.CampusAccounts[0].SSID != "campus" {
		t.Fatalf("ssid = %q", wireless.CampusAccounts[0].SSID)
	}

	wired := apply(t, repository, UpsertCampus(CampusPatch{
		ID: id, AccessMode: stringPtr("wired"), WiredIface: stringPtr("wan"),
	}))
	account := wired.CampusAccounts[0]
	if account.SSID != "" || account.Key != "" || account.Encryption != "" {
		t.Fatalf("wireless fields survived the switch: %+v", account)
	}
	if account.WiredIface != "wan" {
		t.Fatalf("wired_iface = %q", account.WiredIface)
	}
}

// RepairSelection is called inside the transaction, so a pointer that a change
// left dangling cannot reach the disk.
func TestRepairSelectionRules(t *testing.T) {
	cfg := Normalize(Defaults())
	cfg.CampusAccounts = []domain.CampusAccount{{ID: "c1"}, {ID: "c2"}}
	cfg.HotspotProfiles = []domain.HotspotProfile{{ID: "h1"}}

	cfg.Selection = domain.Selection{
		ActiveCampusID: "gone", DefaultCampusID: "gone",
		ActiveHotspotID: "gone", DefaultHotspotID: "gone",
	}
	RepairSelection(&cfg)
	if cfg.Selection.ActiveCampusID != "c1" || cfg.Selection.DefaultCampusID != "c1" {
		t.Fatalf("dangling campus pointers were not repaired: %+v", cfg.Selection)
	}
	if cfg.Selection.ActiveHotspotID != "h1" || cfg.Selection.DefaultHotspotID != "h1" {
		t.Fatalf("dangling hotspot pointers were not repaired: %+v", cfg.Selection)
	}

	// A valid pointer is left alone.
	cfg.Selection.ActiveCampusID = "c2"
	RepairSelection(&cfg)
	if cfg.Selection.ActiveCampusID != "c2" {
		t.Fatalf("a valid pointer was reset to %q", cfg.Selection.ActiveCampusID)
	}

	// A dangling default follows the repaired active, not the first entry.
	cfg.Selection.DefaultCampusID = "gone"
	RepairSelection(&cfg)
	if cfg.Selection.DefaultCampusID != "c2" {
		t.Fatalf("default = %q, want it to follow active c2", cfg.Selection.DefaultCampusID)
	}

	// With nothing left, the pointers empty rather than dangle.
	cfg.CampusAccounts = nil
	RepairSelection(&cfg)
	if cfg.Selection.ActiveCampusID != "" || cfg.Selection.DefaultCampusID != "" {
		t.Fatalf("selection = %+v, want empty", cfg.Selection)
	}
}

// The collection bound is enforced where the account is added, so the error
// names the limit instead of arriving as a validation failure on the whole
// document.
func TestTheAccountLimitIsEnforcedOnCreate(t *testing.T) {
	repository := emptyRepository(t)
	for range MaxCampusAccounts {
		if _, err := repository.Update(repository.Revision(),
			UpsertCampus(newAccountPatch())); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	_, err := repository.Update(repository.Revision(), UpsertCampus(newAccountPatch()))
	if err == nil {
		t.Fatal("the limit was not enforced")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidConfig {
		t.Fatalf("code = %q, want InvalidConfig", code)
	}
}
