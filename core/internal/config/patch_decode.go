package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// DecodePatch decodes a bounded object with exact field names and presence
// semantics. Only explicitly listed paths may contain null. Error messages do
// not include input values: a malformed credential must not reach the log.
func DecodePatch(data []byte, target any, nullablePaths ...string) error {
	return decodePatchBounded(data, target, MaxConfigBytes, nullablePaths...)
}

// DecodeBackupTransfer permits JSON escaping of a bounded backup string. The
// contained document still goes through ParseBackup's 512 KiB limit.
func DecodeBackupTransfer(data []byte, target any) error {
	return decodePatchBounded(data, target, 2*MaxConfigBytes+4096)
}

func decodePatchBounded(data []byte, target any, limit int, nullablePaths ...string) error {
	invalid := func() error {
		return domain.Errorf(domain.CodeInvalidArgument, "参数必须是有效的 JSON 对象；请检查字段名、类型和重复字段")
	}
	if len(data) > limit {
		return invalid()
	}
	nullable := make(map[string]bool, len(nullablePaths))
	for _, path := range nullablePaths {
		nullable[path] = true
	}
	if err := scanNullable(data, nullable); err != nil {
		return invalid()
	}
	if !exactFields(data, reflect.TypeOf(target)) {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid()
	}
	return nil
}

// encoding/json otherwise accepts case-insensitive aliases, so "Password" and
// "password" could overwrite one another despite duplicate-key validation.
func exactFields(data json.RawMessage, typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Slice && typ.Elem().Kind() != reflect.Uint8 {
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return false
		}
		for _, item := range items {
			if !exactFields(item, typ.Elem()) {
				return false
			}
		}
		return true
	}
	if typ.Kind() != reflect.Struct {
		return true // map keys belong to the strategy; RawMessage is checked later.
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		// Value types such as ClockTime have a string JSON representation. Let
		// their decoder validate that value rather than treating it as an object.
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	known := make(map[string]reflect.Type, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			known[name] = field.Type
		}
	}
	for name, value := range fields {
		field, found := known[name]
		if !found || !exactFields(value, field) {
			return false
		}
	}
	return true
}

// UnmarshalJSON keeps explicit null distinct from an absent double_stack.
// DecodePatch performs the structural checks before invoking this method.
func (p *LoginPatch) UnmarshalJSON(data []byte) error {
	type plain LoginPatch
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, present := fields["double_stack"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var unset *bool
		decoded.DoubleStack = &unset
	}
	*p = LoginPatch(decoded)
	return nil
}
