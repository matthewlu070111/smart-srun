package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func legacyBackupFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../tests/fixtures/config-backup-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBackupImports161WithoutChangingIdentityOrSecrets(t *testing.T) {
	cfg, warnings, err := ParseBackup(legacyBackupFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.Revision != 0 || len(warnings) < 3 {
		t.Fatal("unsafe import policy")
	}
	wifi, wired := cfg.CampusAccounts[0], cfg.CampusAccounts[1]
	if wifi.Password != " synthetic-secret " || EffectiveUsername(wifi) != "example-user" || EffectiveUsername(wired) != "other-user@arbitrary.example" {
		t.Fatal("import changed credentials or suffix")
	}
	if wifi.Login.DoubleStack == nil || *wifi.Login.DoubleStack || wifi.Login.OS != "Linux" || wired.WiredIface != "wan2" || !wired.AuthEnabled {
		t.Fatal("account login shape or interface changed")
	}
	if cfg.Retry.InitialSeconds != 2.5 || cfg.Quiet.Start.String() != "23:45" || cfg.Selection.ActiveCampusID != wifi.ID || cfg.HotspotProfiles[0].Key != "synthetic-hotspot-key" {
		t.Fatal("settings or account references changed")
	}
	backup, err := ExportBackup(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(backup)
	restored, _, err := ParseBackup(data)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Marshal(cfg)
	got, _ := Marshal(restored)
	if string(got) != string(want) {
		t.Fatal("v2 roundtrip changed config")
	}
}

func TestBackupRejectsMalformedWithoutEchoingSuppliedData(t *testing.T) {
	fixture := string(legacyBackupFixture(t))
	cases := []string{"{}", "[]", fixture + "{}", strings.Repeat("x", MaxConfigBytes+1),
		strings.Replace(fixture, `"format_version": 1`, `"format_version": 1,"Format_version": 1`, 1),
		strings.Replace(fixture, `"synthetic-hotspot-key"`, `"synthetic-hotspot-key", "Key":"override"`, 1),
		strings.Replace(fixture, `"format_version": 1`, `"format_version": 1,"format_version": 1`, 1),
		strings.Replace(fixture, `"config_schema": 1`, `"config_schema": 3`, 1),
		strings.Replace(fixture, `"retry_cooldown_seconds": "2.5"`, `"retry_cooldown_seconds": "NaN"`, 1),
		strings.Replace(fixture, `"enabled": "1"`, `"enabled": true`, 1),
		strings.Replace(fixture, `"active_campus_id": "campus-wifi"`, `"active_campus_id": "synthetic-secret"`, 1),
		strings.Replace(fixture, `"campus_accounts": [`, `"synthetic-secret":true,"campus_accounts": [`, 1),
		strings.Replace(fixture, `"password": " synthetic-secret "`, `"password":null`, 1),
		strings.Replace(fixture, `"password": " synthetic-secret "`, `"password": "`+string([]byte{255})+`"`, 1),
	}
	for i, input := range cases {
		_, _, err := ParseBackup([]byte(input))
		if err == nil {
			t.Errorf("case %d accepted", i)
		} else if strings.Contains(err.Error(), "synthetic-secret") {
			t.Errorf("case %d leaked a secret", i)
		}
	}
}
