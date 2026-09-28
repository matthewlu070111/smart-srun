package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// log.file_omit_info defaults to true everywhere a configuration can come
// from: a fresh install, an upgraded config.json written before the key
// existed, a 1.6.1 backup and an rc1 v2 backup.
func TestFileOmitInfoDefaultsOn(t *testing.T) {
	if !Defaults().Log.FileOmitInfo {
		t.Fatal("Defaults() leaves file_omit_info off")
	}
	// An rc1 config.json has log.level and nothing else under log.
	for _, doc := range []string{
		`{"schema_version":2}`,
		`{"schema_version":2,"log":{"level":"WARN"}}`,
	} {
		cfg, err := Decode([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		if !cfg.Log.FileOmitInfo {
			t.Errorf("%s: missing key decoded as false", doc)
		}
		if err := Validate(cfg); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
	cfg, err := Decode([]byte(`{"schema_version":2,"log":{"level":"INFO","file_omit_info":false}}`))
	if err != nil || cfg.Log.FileOmitInfo {
		t.Fatalf("explicit false not kept: %+v / %v", cfg.Log, err)
	}
	if _, err := Decode([]byte(`{"schema_version":2,"log":{"file_omit_info":"yes"}}`)); err == nil {
		t.Fatal("a string was accepted as a bool")
	}
}

func TestFileOmitInfoInTheSchema(t *testing.T) {
	for _, field := range BuildSchema().Global {
		if field.Path != "log.file_omit_info" {
			continue
		}
		if field.Kind != KindBool || field.Default != true || field.Note == "" {
			t.Fatalf("field = %+v", field)
		}
		return
	}
	t.Fatal("log.file_omit_info is not in the schema")
}

func TestFileOmitInfoSurvivesBackups(t *testing.T) {
	legacy, _, err := ParseBackup(legacyBackupFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Log.FileOmitInfo {
		t.Error("a 1.6.1 backup imported with file_omit_info off")
	}

	// A v2 backup exported by rc1 has no such key.
	cfg := Defaults()
	backup, err := ExportBackup(cfg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(backup)
	old := strings.Replace(string(encoded), `,\"file_omit_info\":true`, ``, 1)
	old = strings.Replace(old, `,"file_omit_info":true`, ``, 1)
	if strings.Contains(old, "file_omit_info") {
		t.Fatalf("could not strip the key: %s", old)
	}
	restored, _, err := ParseBackup([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Log.FileOmitInfo {
		t.Error("an rc1 v2 backup restored with file_omit_info off")
	}

	cfg.Log.FileOmitInfo = false
	backup, _ = ExportBackup(cfg)
	encoded, _ = json.Marshal(backup)
	restored, _, err = ParseBackup(encoded)
	if err != nil || restored.Log.FileOmitInfo {
		t.Fatalf("explicit false lost in a v2 roundtrip: %v", err)
	}
}

func TestFileOmitInfoIsASetting(t *testing.T) {
	cfg := Defaults()
	settings := SettingsOf(cfg)
	if err := DecodePatch([]byte(`{"log":{"file_omit_info":false}}`), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Log.Level != cfg.Log.Level {
		t.Fatalf("partial patch changed the level: %q", settings.Log.Level)
	}
	if err := ApplySettings(settings)(&cfg); err != nil || cfg.Log.FileOmitInfo {
		t.Fatalf("setting not applied: %+v / %v", cfg.Log, err)
	}
}
