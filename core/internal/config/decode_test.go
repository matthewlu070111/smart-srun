package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// T07 -- strict decoding. Each case names an input a lenient decoder accepts
// and states what accepting it would cost the user.

func TestDecodeRejectsDocumentsALenientDecoderWouldAccept(t *testing.T) {
	cases := []struct {
		name     string
		document string
		wantPath string
		because  string
	}{{
		name:     "duplicate key",
		document: `{"schema_version":2,"enabled":true,"enabled":false}`,
		wantPath: "enabled",
		because:  "encoding/json keeps the last value, silently disabling the service",
	}, {
		name:     "duplicate key in a nested object",
		document: `{"schema_version":2,"retry":{"max_retries":4,"max_retries":0}}`,
		wantPath: "retry.max_retries",
		because:  "the same trap one level down",
	}, {
		name:     "duplicate key inside an array element",
		document: `{"schema_version":2,"campus_accounts":[{"id":"a","id":"b"}]}`,
		wantPath: "campus_accounts[0].id",
		because:  "array paths must be reported precisely enough to fix",
	}, {
		name:     "unknown key",
		document: `{"schema_version":2,"backoff_exponent_factor":"1.5"}`,
		wantPath: "backoff_exponent_factor",
		because:  "a knob that no longer exists must not look accepted",
	}, {
		name:     "legacy account field alias",
		document: `{"schema_version":2,"campus_accounts":[{"network_interface":"wan"}]}`,
		wantPath: "network_interface",
		because:  "decision D10: only wired_iface is written, the alias is gone",
	}, {
		name:     "null instead of a value",
		document: `{"schema_version":2,"school":null}`,
		wantPath: "school",
		because:  "null unmarshals into a string as \"\", indistinguishable from absent",
	}, {
		name:     "null inside an account",
		document: `{"schema_version":2,"campus_accounts":[{"id":"a","password":null}]}`,
		wantPath: "campus_accounts[0].password",
		because:  "a null password would read as an empty one",
	}, {
		name:     "second top-level value",
		document: `{"schema_version":2}{"schema_version":2}`,
		wantPath: "",
		because:  "a file appended to instead of replaced would load its stale prefix",
	}, {
		name:     "root is not an object",
		document: `[{"schema_version":2}]`,
		wantPath: "",
		because:  "the configuration is an object",
	}, {
		name:     "bool given as a string",
		document: `{"schema_version":2,"enabled":"1"}`,
		wantPath: "enabled",
		because:  "1.x stored \"1\"; v2 must say so rather than guess",
	}, {
		name:     "number given as a string",
		document: `{"schema_version":2,"checks":{"interval_seconds":"60"}}`,
		wantPath: "interval_seconds",
		because:  "same, for the numeric knobs",
	}, {
		name:     "seconds given as a string",
		document: `{"schema_version":2,"retry":{"initial_seconds":"10"}}`,
		wantPath: "",
		because:  "Seconds rejects non-numbers",
	}, {
		name:     "clock time in the wrong shape",
		document: `{"schema_version":2,"quiet":{"start":"6:00"}}`,
		wantPath: "",
		because:  "HH:MM is exact; 6:00 means the UI did not produce this",
	}, {
		name:     "truncated document",
		document: `{"schema_version":2,"quiet":{`,
		wantPath: "",
		because:  "a half-written file must not load as a half-configuration",
	}}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Decode([]byte(testCase.document))
			if err == nil {
				t.Fatalf("accepted %s; %s", testCase.name, testCase.because)
			}
			code, ok := domain.CodeOf(err)
			if !ok || code != domain.CodeInvalidConfig {
				t.Fatalf("code = %q, want InvalidConfig (err: %v)", code, err)
			}
			if testCase.wantPath != "" && !strings.Contains(err.Error(), testCase.wantPath) {
				t.Fatalf("error %q does not name %q, so the user cannot find the field",
					err.Error(), testCase.wantPath)
			}
		})
	}
}

func TestDecodeRejectsOversizedDocuments(t *testing.T) {
	padding := strings.Repeat("x", MaxConfigBytes)
	document := `{"schema_version":2,"school":"` + padding + `"}`

	_, err := Decode([]byte(document))
	if err == nil {
		t.Fatal("accepted a document over the 512 KiB limit")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("error %q does not explain the limit", err.Error())
	}
}

func TestDecodeRejectsDeepNestingBeforeItExhaustsTheStack(t *testing.T) {
	document := `{"schema_version":2,"school_extra":{"a":` +
		strings.Repeat("[", 200) + strings.Repeat("]", 200) + `}}`

	if _, err := Decode([]byte(document)); err == nil {
		t.Fatal("accepted 200 levels of nesting")
	}
}

func TestDecodeReportsLegacyConfigWithoutConverting(t *testing.T) {
	cases := map[string]string{
		"explicit schema_version 1": `{"schema_version":1,"enabled":"1"}`,
		"1.x flat string map":       `{"enabled":"1","backoff_enable":"1"}`,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(document))
			if err == nil {
				t.Fatal("a 1.x file must be reported, not loaded")
			}
			if !errors.Is(err, ErrLegacyConfig) {
				t.Fatalf("err = %v, want it to wrap ErrLegacyConfig so the "+
					"repository knows not to overwrite the old file", err)
			}
			if !strings.Contains(err.Error(), "导出备份") {
				t.Fatalf("error %q does not tell the user what to do", err.Error())
			}
		})
	}
}

func TestDecodeRejectsUnsupportedSchemaVersions(t *testing.T) {
	for _, document := range []string{
		`{"schema_version":3}`,
		`{"schema_version":0}`,
		`{"schema_version":"2"}`,
	} {
		if _, err := Decode([]byte(document)); err == nil {
			t.Fatalf("accepted %s", document)
		}
	}
}

// Omitted fields keep their defaults: a hand-written file may set only what it
// cares about. What the document does say must still be exactly right.
func TestDecodeFillsOmittedFieldsFromDefaults(t *testing.T) {
	cfg, err := Decode([]byte(`{"schema_version":2,"enabled":true}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	defaults := Defaults()
	if !cfg.Enabled {
		t.Error("enabled was not applied")
	}
	if cfg.Quiet != defaults.Quiet {
		t.Errorf("quiet = %+v, want the default %+v", cfg.Quiet, defaults.Quiet)
	}
	if cfg.Retry != defaults.Retry {
		t.Errorf("retry = %+v, want the default %+v", cfg.Retry, defaults.Retry)
	}
	if cfg.Checks != defaults.Checks {
		t.Errorf("checks = %+v, want the default %+v", cfg.Checks, defaults.Checks)
	}
	if cfg.Log.Level != domain.LogInfo {
		t.Errorf("log.level = %q, want INFO", cfg.Log.Level)
	}
	if cfg.School != DefaultSchool {
		t.Errorf("school = %q, want %q", cfg.School, DefaultSchool)
	}
}

// A partially specified nested object keeps the defaults of its siblings.
func TestDecodeOverlaysNestedObjectsFieldByField(t *testing.T) {
	cfg, err := Decode([]byte(`{"schema_version":2,"retry":{"max_retries":9}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cfg.Retry.MaxRetries != 9 {
		t.Errorf("max_retries = %d, want 9", cfg.Retry.MaxRetries)
	}
	if cfg.Retry.InitialSeconds != Defaults().Retry.InitialSeconds {
		t.Errorf("initial_seconds = %v, want the default to survive",
			cfg.Retry.InitialSeconds)
	}
	if !cfg.Retry.Enabled {
		t.Error("retry.enabled default was lost by a partial object")
	}
}
