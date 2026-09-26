package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/strategy"
)

// A school is a declaration, not a code module.
//
// Spec 07 asks for proof that adding a school needs no code. The strategy below
// is declared in this test and compiled into nothing: if its fields reach the
// published schema and its values survive storage, then a real school needs a
// registry entry and no more.
func TestADeclaredSchoolNeedsNoCode(t *testing.T) {
	useTestStrategies(t, strategy.Strategy{
		ID: "example-university", Label: "示例大学",
		Fields: []strategy.Field{
			{Key: "campus_zone", Label: "校区", Kind: strategy.FieldSelect,
				Help:    "选择你所在的校区",
				Options: []strategy.Option{{Value: "north", Label: "北区"}, {Value: "south", Label: "南区"}}},
			{Key: "legacy_portal", Label: "旧门户", Kind: strategy.FieldBool},
		},
	})

	schema := BuildSchemaFor("example-university")
	if len(schema.SchoolExtra) != 2 {
		t.Fatalf("published %d private fields, want 2: %+v", len(schema.SchoolExtra), schema.SchoolExtra)
	}

	zone := schema.SchoolExtra[0]
	if zone.Key != "campus_zone" || zone.Kind != string(strategy.FieldSelect) {
		t.Errorf("first field = %+v", zone)
	}
	if zone.Strategy != "example-university" {
		t.Errorf("field does not name its strategy: %+v", zone)
	}
	if zone.StoragePath != "school_extra.campus_zone" {
		t.Errorf("storage path = %q", zone.StoragePath)
	}
	if len(zone.Choices) != 2 || zone.Choices[0].Value != "north" || zone.Choices[0].Label != "北区" {
		t.Errorf("choices = %+v", zone.Choices)
	}
	if zone.Help != "选择你所在的校区" {
		t.Errorf("help = %q", zone.Help)
	}

	// The page reads this as JSON, and an absent key is a different thing from
	// an empty list: the bridge iterates it directly.
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["school_extra"] == nil {
		t.Error("school_extra is missing from the published schema")
	}
}

// A strategy with no private fields publishes an empty list, not null.
func TestAStrategyWithoutPrivateFieldsPublishesAnEmptyList(t *testing.T) {
	encoded, err := json.Marshal(BuildSchema())
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SchoolExtra []SchoolField `json:"school_extra"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchoolExtra == nil {
		t.Error("school_extra decoded as null; the page iterates it")
	}
	if len(decoded.SchoolExtra) != 0 {
		t.Errorf("the built-in strategy published %d fields", len(decoded.SchoolExtra))
	}
}

// A configuration naming a strategy this build does not have renders as "no
// private settings" rather than failing the whole schema call.
func TestAnUnknownStrategyPublishesNothingInsteadOfFailing(t *testing.T) {
	schema := BuildSchemaFor("a-school-nobody-compiled-in")
	if schema.SchoolExtra == nil {
		t.Fatal("school_extra is nil for an unknown strategy")
	}
	if len(schema.SchoolExtra) != 0 {
		t.Errorf("an unknown strategy published %d fields", len(schema.SchoolExtra))
	}
	if len(schema.Global) == 0 || len(schema.CampusAccount) == 0 {
		t.Error("an unknown strategy blanked the rest of the schema")
	}
}

// An import preview names the school_extra keys the backup carried and the
// imported configuration will not keep.
//
// Parsing normalises and normalising filters, silently from inside this
// package. The preview is where a person decides whether to go ahead, so it is
// where they are told -- by key name only, since a value may be private.
func TestImportPreviewNamesDroppedSchoolExtraKeys(t *testing.T) {
	useTestStrategies(t, strategy.Strategy{ID: "zoned", Label: "分区",
		Fields: []strategy.Field{{Key: "zone", Label: "区", Kind: strategy.FieldString}}})

	document := `{"format":"smart-srun-config","format_version":1,"config_schema":2,"config":` +
		`{"schema_version":2,"school":"zoned","school_extra":{"zone":"north","old_knob":"secret-value"}}}`
	cfg, warnings, err := ParseBackup([]byte(document))
	if err != nil {
		t.Fatalf("ParseBackup: %v", err)
	}
	if cfg.SchoolExtra["zone"] != "north" {
		t.Errorf("the declared key was lost: %+v", cfg.SchoolExtra)
	}
	found := false
	for _, warning := range warnings {
		if strings.Contains(warning, "school_extra.old_knob") {
			found = true
		}
		if strings.Contains(warning, "secret-value") {
			t.Errorf("a warning quoted a dropped value: %q", warning)
		}
		if strings.Contains(warning, "school_extra.zone") {
			t.Errorf("a kept key was reported as dropped: %q", warning)
		}
	}
	if !found {
		t.Errorf("warnings = %q, want one naming school_extra.old_knob", warnings)
	}
}

// Filtering names what it dropped, so a caller can tell the user rather than
// silently discarding their input.
func TestFilteringReportsTheKeysItDropped(t *testing.T) {
	useTestStrategies(t, strategy.Strategy{ID: "declares-one", Label: "一个字段",
		Fields: []strategy.Field{{Key: "kept", Label: "保留", Kind: strategy.FieldString}}})

	kept, dropped := FilterSchoolExtra("declares-one", map[string]any{
		"kept": "yes", "zebra": 1, "alpha": true,
	})
	if len(kept) != 1 || kept["kept"] != "yes" {
		t.Errorf("kept = %+v", kept)
	}
	if len(dropped) != 2 || dropped[0] != "alpha" || dropped[1] != "zebra" {
		t.Errorf("dropped = %v, want a sorted [alpha zebra]", dropped)
	}

	// Nothing supplied is not a diagnostic.
	empty, none := FilterSchoolExtra("declares-one", nil)
	if empty == nil {
		t.Error("filtering nil produced a nil map; callers assign it straight back")
	}
	if none != nil {
		t.Errorf("an empty map reported drops: %v", none)
	}
}
