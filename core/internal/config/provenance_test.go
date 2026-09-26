package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func marshalBackup(t *testing.T, backup Backup) ([]byte, error) {
	t.Helper()
	return json.Marshal(backup)
}

// preset_id survives a backup round trip.
//
// No Go code reads this field: its reader is a person looking at an exported
// configuration or `config show` while working out why an account is set up the
// way it is. That makes it exactly the kind of field a refactor drops without
// any test going red, so this is the test that goes red.
//
// It is provenance, not a strategy id, and refreshing the catalogue must never
// use it to overwrite saved parameters -- which is why nothing reads it.
func TestPresetProvenanceSurvivesExportAndImport(t *testing.T) {
	raw := `{"schema_version":2,"campus_accounts":[{"id":"c1","user_id":"2020123456",
		"access_mode":"wired","wired_iface":"wan","preset_id":"jxnu-wired-2026"}]}`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.CampusAccounts[0].PresetID != "jxnu-wired-2026" {
		t.Fatalf("preset_id = %q after parse", cfg.CampusAccounts[0].PresetID)
	}

	backup, err := ExportBackup(cfg)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if !strings.Contains(string(backup.Config), "jxnu-wired-2026") {
		t.Fatal("the export dropped preset_id")
	}

	encoded, err := marshalBackup(t, backup)
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err := ParseBackup(encoded)
	if err != nil {
		t.Fatalf("ParseBackup: %v", err)
	}
	if got := restored.CampusAccounts[0].PresetID; got != "jxnu-wired-2026" {
		t.Errorf("preset_id = %q after a round trip, want it preserved", got)
	}
}

// An account filled in by hand carries no provenance, and an empty value must
// not be written out as one.
func TestAbsentProvenanceStaysAbsent(t *testing.T) {
	raw := `{"schema_version":2,"campus_accounts":[{"id":"c1","user_id":"u",
		"access_mode":"wired","wired_iface":"wan"}]}`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	backup, err := ExportBackup(cfg)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if strings.Contains(string(backup.Config), "preset_id") {
		t.Error("an account with no provenance exported a preset_id key")
	}
}
