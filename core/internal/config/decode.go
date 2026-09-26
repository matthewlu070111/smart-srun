package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// LegacySchemaVersion is the 1.x on-disk shape.
//
// Startup does not read or convert it. Explicit backups use ParseBackup.
// Recognising an old on-disk file exists so
// the user is told what happened instead of seeing a parse error -- and so the
// repository knows not to overwrite the old file.
const LegacySchemaVersion = 1

// ErrLegacyConfig reports a 1.x configuration file. Callers must leave the file
// alone: overwriting it would destroy the settings the user still needs to read
// while re-entering them.
var ErrLegacyConfig = errors.New("config: 1.x configuration file")

// Decode parses a configuration document.
//
// Fields the document omits keep their default, so a hand-written file may set
// only what it cares about. Everything the document *does* say must be exactly
// right: unknown keys, duplicate keys, nulls, wrong types and trailing content
// are all rejected rather than repaired. A configuration that was repaired on
// load is a configuration nobody can reason about.
//
// Decode does not validate ranges or references; call Validate for that.
func Decode(data []byte) (domain.Config, error) {
	if len(data) > MaxConfigBytes {
		return domain.Config{}, domain.Errorf(domain.CodeInvalidConfig,
			"配置文件超过 %d KiB 上限（实际 %d 字节）", MaxConfigBytes/1024, len(data))
	}
	if err := scanStrict(data); err != nil {
		return domain.Config{}, err
	}
	if err := checkSchemaVersion(data); err != nil {
		return domain.Config{}, err
	}

	cfg := Defaults()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return domain.Config{}, decodeError(err)
	}
	return cfg, nil
}

// schemaProbe reads just enough to tell a 1.x file from a v2 one.
type schemaProbe struct {
	SchemaVersion *int `json:"schema_version"`
}

func checkSchemaVersion(data []byte) error {
	var probe schemaProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return domain.FieldErrorf(domain.CodeInvalidConfig, "schema_version",
			"schema_version 必须是整数").Wrap(err)
	}
	if probe.SchemaVersion == nil {
		// A 1.x file is a flat string map with no schema_version at all.
		if looksLikeLegacy(data) {
			return domain.Errorf(domain.CodeInvalidConfig,
				"这是 1.x 的配置文件，2.0 不读取旧配置。原文件已保留，请使用 1.6.1 导出备份，再通过进阶设置导入。").
				Wrap(ErrLegacyConfig)
		}
		return domain.FieldErrorf(domain.CodeInvalidConfig, "schema_version",
			"缺少 schema_version，应为 %d", domain.ConfigSchemaVersion)
	}
	if *probe.SchemaVersion == LegacySchemaVersion {
		return domain.Errorf(domain.CodeInvalidConfig,
			"这是 1.x 的配置文件，2.0 不读取旧配置。原文件已保留，请使用 1.6.1 导出备份，再通过进阶设置导入。").
			Wrap(ErrLegacyConfig)
	}
	if *probe.SchemaVersion != domain.ConfigSchemaVersion {
		return domain.FieldErrorf(domain.CodeInvalidConfig, "schema_version",
			"不支持的 schema_version %d，本版本只接受 %d",
			*probe.SchemaVersion, domain.ConfigSchemaVersion)
	}
	return nil
}

// legacyMarkers are keys only a 1.x file has. Any one of them identifies the
// old format well enough to give the user a useful message.
var legacyMarkers = []string{
	`"backoff_enable"`, `"quiet_hours_enabled"`, `"active_campus_id"`,
	`"connectivity_check_mode"`, `"manual_terminal_check_max_attempts"`,
}

func looksLikeLegacy(data []byte) bool {
	text := string(data)
	for _, marker := range legacyMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// decodeError turns encoding/json's message into one a user can act on, while
// keeping the original as the wrapped cause.
func decodeError(err error) error {
	if typed, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		field := typed.Field
		if field == "" {
			field = "(根对象)"
		}
		return domain.FieldErrorf(domain.CodeInvalidConfig, typed.Field,
			"类型不正确：%s 需要 %s，收到 %s", field, goTypeName(typed.Type.String()), typed.Value).
			Wrap(err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return domain.Errorf(domain.CodeInvalidConfig, "配置文件被截断").Wrap(err)
	}
	// DisallowUnknownFields reports a plain error; its text names the field.
	if field, ok := unknownFieldName(err); ok {
		return domain.FieldErrorf(domain.CodeInvalidConfig, field,
			"未知字段；本版本不接受该键").Wrap(err)
	}
	return domain.Errorf(domain.CodeInvalidConfig, "配置解析失败：%v", err).Wrap(err)
}

const unknownFieldPrefix = `json: unknown field `

func unknownFieldName(err error) (string, bool) {
	_, quoted, found := strings.Cut(err.Error(), unknownFieldPrefix)
	if !found {
		return "", false
	}
	name, unquoteErr := unquoteJSONString(quoted)
	if unquoteErr != nil {
		return strings.Trim(quoted, `"`), true
	}
	return name, true
}

func unquoteJSONString(text string) (string, error) {
	var out string
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return "", fmt.Errorf("not a quoted string: %w", err)
	}
	return out, nil
}

// goTypeName renders a Go type as the JSON type a user would recognise.
func goTypeName(name string) string {
	switch {
	case name == "bool":
		return "布尔值 true/false"
	case name == "string":
		return "字符串"
	case strings.HasPrefix(name, "int") || strings.HasPrefix(name, "uint"):
		return "整数"
	case strings.HasPrefix(name, "float") || name == "domain.Seconds":
		return "数字"
	case strings.HasPrefix(name, "[]"):
		return "数组"
	case strings.HasPrefix(name, "map["):
		return "对象"
	case name == "domain.ClockTime":
		return "HH:MM 字符串"
	default:
		return name
	}
}
