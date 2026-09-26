package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/protocol/srun"
)

func accountWith(t *testing.T, login string) domain.Config {
	t.Helper()
	raw := `{"schema_version":2,"campus_accounts":[{"id":"c1","user_id":"2020123456",
		"access_mode":"wired","wired_iface":"wan","base_url":"http://10.0.0.1"` + login + `}]}`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// An unset table resolves to the common one, and a set table is what resolution
// hands to the protocol layer.
func TestEffectiveLoginResolvesTheAlphabet(t *testing.T) {
	cfg := accountWith(t, "")
	if got := EffectiveLogin(cfg, cfg.CampusAccounts[0]).Alphabet; got != srun.DefaultAlphabetTable {
		t.Errorf("unset alphabet resolved to %q, want the common table", got)
	}

	cfg = accountWith(t, `,"login":{"alphabet":"`+srun.StandardAlphabetTable+`"}`)
	if got := EffectiveLogin(cfg, cfg.CampusAccounts[0]).Alphabet; got != srun.StandardAlphabetTable {
		t.Errorf("alphabet = %q, want the configured table", got)
	}
}

// Validation refuses a table the gateway could not decode.
//
// Every one of these encodes without complaint and fails only at the gateway,
// as a login rejected with no stated reason, so they have to be caught at save
// time rather than discovered as "wrong password".
func TestValidationRefusesAnUnusableAlphabet(t *testing.T) {
	for name, table := range map[string]string{
		"short":    strings.Repeat("A", srun.AlphabetSize-1),
		"long":     strings.Repeat("A", srun.AlphabetSize+1),
		"repeated": strings.Repeat("AB", srun.AlphabetSize/2),
		"padding":  "=" + srun.StandardAlphabetTable[1:],
		"control":  "\x01" + srun.StandardAlphabetTable[1:],
	} {
		t.Run(name, func(t *testing.T) {
			raw := `{"schema_version":2,"campus_accounts":[{"id":"c1","user_id":"u",
				"access_mode":"wired","wired_iface":"wan","login":{"alphabet":` +
				mustJSON(t, table) + `}}]}`
			_, err := Parse([]byte(raw))
			if err == nil {
				t.Fatal("an unusable alphabet was accepted")
			}
			if !strings.Contains(err.Error(), "login.alphabet") {
				t.Errorf("error does not name the field: %v", err)
			}
		})
	}
}

// A form that does not submit the field must not clear it.
//
// The frozen LuCI account dialog sends n, type, enc, info_prefix, os, name and
// double_stack, and has no alphabet control. If an absent field were read as
// "set to empty", saving any unrelated account change from the page would
// silently drop a table set from the CLI and put the account back on the common
// one -- which fails as a rejected password, days later.
func TestAPageSaveWithoutTheFieldKeepsTheStoredAlphabet(t *testing.T) {
	cfg := accountWith(t, `,"login":{"alphabet":"`+srun.StandardAlphabetTable+`"}`)

	// Exactly the login fields the frozen page submits, and no alphabet.
	n, kind, enc, prefix, os, name := "200", "1", "srun_bx1", "SRBX1", "Windows 10", "Windows"
	patch := CampusPatch{ID: "c1", Login: &LoginPatch{
		N: &n, Type: &kind, Enc: &enc, InfoPrefix: &prefix, OS: &os, Name: &name,
	}}
	if err := UpsertCampus(patch)(&cfg); err != nil {
		t.Fatalf("UpsertCampus: %v", err)
	}
	if got := cfg.CampusAccounts[0].Login.Alphabet; got != srun.StandardAlphabetTable {
		t.Fatalf("alphabet = %q after a page-shaped save, want it preserved", got)
	}

	// An explicit empty string is still a real instruction: back to the common
	// table. Absence and emptiness must not be the same thing.
	empty := ""
	if err := UpsertCampus(CampusPatch{ID: "c1", Login: &LoginPatch{Alphabet: &empty}})(&cfg); err != nil {
		t.Fatalf("UpsertCampus: %v", err)
	}
	if got := cfg.CampusAccounts[0].Login.Alphabet; got != "" {
		t.Errorf("an explicit empty alphabet did not clear the override: %q", got)
	}
}

// The schema the page and the CLI read has to publish the field, otherwise a
// client cannot discover it exists.
func TestSchemaPublishesTheAlphabet(t *testing.T) {
	for _, field := range BuildSchema().CampusAccount {
		if field.Path != "login.alphabet" {
			continue
		}
		if field.MaxBytes != srun.AlphabetSize {
			t.Errorf("max_bytes = %d, want %d", field.MaxBytes, srun.AlphabetSize)
		}
		if field.Secret {
			t.Error("the table is a public property of the gateway, not a secret")
		}
		if field.Default != srun.DefaultAlphabetTable {
			t.Errorf("default = %v, want the common table", field.Default)
		}
		return
	}
	t.Fatal("the schema does not publish login.alphabet")
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
