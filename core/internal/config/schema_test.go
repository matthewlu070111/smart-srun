package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Fields the schema deliberately does not publish: the format version and the
// CAS revision are owned by the repository, not by any form.
var schemaExemptPaths = []string{"schema_version", "revision"}

// leafPaths walks a struct's JSON tags and returns every leaf path. ClockTime
// and Seconds are leaves despite being structs/named types: they serialize as a
// single scalar.
func leafPaths(t *testing.T, value any) []string {
	t.Helper()
	var out []string
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, prefix string) {
		for i := range typ.NumField() {
			field := typ.Field(i)
			tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if tag == "" || tag == "-" {
				continue
			}
			path := tag
			if prefix != "" {
				path = prefix + "." + tag
			}
			fieldType := field.Type
			for fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			isScalarStruct := fieldType == reflect.TypeOf(domain.ClockTime{})
			if fieldType.Kind() == reflect.Struct && !isScalarStruct {
				walk(fieldType, path)
				continue
			}
			out = append(out, path)
		}
	}
	walk(reflect.TypeOf(value), "")
	return out
}

func schemaPaths(fields []Field) []string {
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = field.Path
	}
	return out
}

// A field that exists in the struct but not the schema would reach the form
// with no default and no bounds; one in the schema but not the struct would be
// rendered and then silently dropped on save.
func TestSchemaCoversEveryPersistedField(t *testing.T) {
	schema := BuildSchema()
	cases := []struct {
		name     string
		sample   any
		declared []string
	}{
		{"global", domain.Config{}, schemaPaths(schema.Global)},
		{"campus_account", domain.CampusAccount{}, schemaPaths(schema.CampusAccount)},
		{"hotspot", domain.HotspotProfile{}, schemaPaths(schema.Hotspot)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := leafPaths(t, testCase.sample)
			for _, path := range actual {
				if slices.Contains(schemaExemptPaths, path) {
					continue
				}
				if !slices.Contains(testCase.declared, path) {
					t.Errorf("%q is persisted but not in the schema; the form "+
						"would get no default and no bounds for it", path)
				}
			}
			for _, path := range testCase.declared {
				if !slices.Contains(actual, path) {
					t.Errorf("%q is in the schema but not persisted; the form "+
						"would render it and the save would drop it", path)
				}
			}
		})
	}
}

// The schema's defaults are the values Defaults() produces. A second copy would
// let the form offer one value while the scheduler used another -- which is
// exactly what made 1.x `config show` misreport (decision D07).
func TestSchemaDefaultsMatchDefaults(t *testing.T) {
	defaults := Defaults()
	encoded, err := json.Marshal(defaults)
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	var flat map[string]any
	if err := json.Unmarshal(encoded, &flat); err != nil {
		t.Fatalf("unmarshal defaults: %v", err)
	}

	for _, field := range BuildSchema().Global {
		if field.Default == nil || field.Kind == KindList || field.Kind == KindMap {
			continue
		}
		actual, ok := lookupPath(flat, field.Path)
		if !ok {
			t.Errorf("%s: no such path in the encoded defaults", field.Path)
			continue
		}
		wantJSON, _ := json.Marshal(field.Default)
		gotJSON, _ := json.Marshal(actual)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("%s: schema default %s, Defaults() has %s",
				field.Path, wantJSON, gotJSON)
		}
	}
}

func lookupPath(root map[string]any, path string) (any, bool) {
	current := any(root)
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// Every enum field's choices are the enum's own list, so the form cannot offer
// a value validation rejects, nor hide one it accepts.
func TestSchemaEnumChoicesAreValid(t *testing.T) {
	schema := BuildSchema()
	all := slices.Concat(schema.Global, schema.CampusAccount, schema.Hotspot)
	seenEnum := 0
	for _, field := range all {
		if field.Kind != KindEnum {
			if len(field.Choices) > 0 {
				t.Errorf("%s: non-enum field lists choices", field.Path)
			}
			continue
		}
		seenEnum++
		if len(field.Choices) == 0 {
			t.Errorf("%s: enum with no choices", field.Path)
		}
		if field.Default != nil && !slices.Contains(field.Choices, field.Default.(string)) {
			t.Errorf("%s: default %v is not one of %v",
				field.Path, field.Default, field.Choices)
		}
	}
	if seenEnum == 0 {
		t.Fatal("no enum fields found; the walk is not reaching the schema")
	}
}

// Secrets are marked so that every response builder can exclude them from one
// list rather than each remembering which fields are sensitive.
func TestSecretFieldsAreMarked(t *testing.T) {
	schema := BuildSchema()
	wantSecret := map[string][]Field{
		"password": schema.CampusAccount,
		"key":      schema.CampusAccount,
	}
	for path, fields := range wantSecret {
		index := slices.IndexFunc(fields, func(f Field) bool { return f.Path == path })
		if index < 0 {
			t.Fatalf("%q missing from the account schema", path)
		}
		if !fields[index].Secret {
			t.Errorf("%q is not marked secret", path)
		}
	}
	for _, field := range schema.Hotspot {
		if field.Path == "key" && !field.Secret {
			t.Error("hotspot key is not marked secret")
		}
	}
}

// The schema is what the Lua bridge and the CLI read, so it has to survive a
// JSON round trip without losing a bound.
func TestSchemaSerializes(t *testing.T) {
	encoded, err := json.Marshal(BuildSchema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Schema
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Global) != len(BuildSchema().Global) {
		t.Fatal("global field count changed across a round trip")
	}
	if decoded.UnverifiedOperatorSuffix != UnverifiedOperatorSuffix {
		t.Fatal("the unverified sentinel was lost")
	}

	index := slices.IndexFunc(decoded.Global, func(f Field) bool {
		return f.Path == "checks.interval_seconds"
	})
	field := decoded.Global[index]
	if field.Min == nil || field.Max == nil || *field.Min != MinIntervalSeconds ||
		*field.Max != MaxIntervalSeconds {
		t.Fatalf("interval bounds did not survive: %+v", field)
	}
}
