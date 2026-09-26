package presets

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const (
	UserSchemaVersion = 2
	MaxUserBytes      = 256 << 10
	// The frozen LuCI store accepts at most 50 entries in each list.
	MaxUserItems = 50
)

// UserPreset keeps the local interface choice separate from public school
// defaults: a publisher must never choose the user's logical WAN interface.
type UserPreset struct {
	School     School
	WiredIface string
}

// UserDocument keeps the original JSON alongside a normalized runtime view.
// Unknown fields, missing optional fields and formatting remain in the file.
// Operators is a shortcut view deduplicated by both suffix and label.
type UserDocument struct {
	Revision  uint64
	Presets   []UserPreset
	Operators []Operator
	raw       []byte
	revision  jsonField
}

func (d UserDocument) Document() []byte { return bytes.Clone(d.raw) }

type jsonField struct {
	raw        json.RawMessage
	start, end int
}

// objectFields also records byte spans, allowing Set to change only revision.
// Duplicate keys are rejected before any value can silently replace another.
func objectFields(data []byte) (map[string]jsonField, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, userError("需要 JSON 对象")
	}
	fields := make(map[string]jsonField)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, userError("JSON 字段无效")
		}
		key, ok := token.(string)
		if !ok {
			return nil, userError("JSON 字段名无效")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, userError("JSON 字段重复")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, userError("JSON 字段内容无效")
		}
		end := int(decoder.InputOffset())
		fields[key] = jsonField{raw: raw, start: end - len(raw), end: end}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, userError("JSON 对象不完整")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, userError("JSON 对象后还有内容")
	}
	return fields, nil
}

func userError(message string) error {
	return domain.Errorf(domain.CodeInvalidArgument, "用户预设：%s", message)
}

func userArray(field jsonField) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if len(field.raw) == 0 || field.raw[0] != '[' || json.Unmarshal(field.raw, &values) != nil {
		return nil, userError("presets 和 operators 必须是数组")
	}
	if len(values) > MaxUserItems {
		return nil, userError("每个列表最多保存 50 项")
	}
	return values, nil
}

func userString(field jsonField) (string, error) {
	var value string
	if len(field.raw) == 0 || field.raw[0] != '"' || json.Unmarshal(field.raw, &value) != nil {
		return "", userError("字段必须是字符串")
	}
	return value, nil
}

// ParseUsers reads schema v2 only. User data is never silently upgraded or
// repaired into an empty document; the old file must remain available.
func ParseUsers(data []byte) (UserDocument, error) {
	if len(data) > MaxUserBytes {
		return UserDocument{}, userError("文件超过 256 KiB 上限")
	}
	fields, err := objectFields(data)
	if err != nil {
		return UserDocument{}, err
	}
	var version *int
	var revision *uint64
	if json.Unmarshal(fields["schema_version"].raw, &version) != nil || version == nil || *version != UserSchemaVersion {
		return UserDocument{}, userError("schema_version 必须为 2，不读取旧版用户预设")
	}
	if json.Unmarshal(fields["revision"].raw, &revision) != nil || revision == nil {
		return UserDocument{}, userError("revision 必须是非负整数")
	}
	entries, err := userArray(fields["presets"])
	if err != nil {
		return UserDocument{}, err
	}
	operators, err := userOperators(fields["operators"])
	if err != nil {
		return UserDocument{}, err
	}
	document := UserDocument{Revision: *revision, Operators: operators,
		raw: bytes.Clone(data), revision: fields["revision"]}
	seen := make(map[string]bool)
	for _, entry := range entries {
		preset, err := parseUserPreset(entry)
		if err != nil {
			return UserDocument{}, err
		}
		id := SafeID(preset.School.ShortName)
		if seen[id] {
			return UserDocument{}, userError("自定义预设标识重复")
		}
		seen[id] = true
		document.Presets = append(document.Presets, preset)
	}
	return document, nil
}

var customID = regexp.MustCompile(`^custom-[A-Za-z0-9_-]+$`)

func parseUserPreset(data []byte) (UserPreset, error) {
	fields, err := objectFields(data)
	if err != nil {
		return UserPreset{}, err
	}
	id, err := userString(fields["short_name"])
	if err != nil || !customID.MatchString(id) {
		return UserPreset{}, userError("自定义预设标识必须使用 custom- 前缀及字母、数字、下划线或连字符")
	}
	name, err := userString(fields["name"])
	if err != nil || strings.TrimSpace(name) == "" {
		return UserPreset{}, userError("自定义预设名称不能为空")
	}
	var raw rawSchool
	if err := json.Unmarshal(data, &raw); err != nil {
		return UserPreset{}, userError("自定义预设格式无效")
	}
	// The user's short_name is authoritative; an unrelated future id field
	// must not override it through the public catalogue's legacy ID alias.
	raw.ID = flexString{Value: id, Present: true}
	school, _ := normalizeSchool(raw)
	school.ShortName = id
	// Local metadata must not turn legacy school defaults into account choices.
	school.Operators = nil
	if field, exists := fields["operators"]; exists {
		school.Operators, err = userOperators(field)
		if err != nil {
			return UserPreset{}, err
		}
	}
	preset := UserPreset{School: school}
	for _, key := range []string{"defaults", "observed_login_shape"} {
		if field, exists := fields[key]; exists {
			values, err := objectFields(field.raw)
			if err != nil {
				return UserPreset{}, err
			}
			if iface, exists := values["wired_iface"]; key == "defaults" && exists {
				preset.WiredIface, err = userString(iface)
				if err != nil {
					return UserPreset{}, err
				}
			}
		}
	}
	return preset, nil
}

func userOperators(field jsonField) ([]Operator, error) {
	entries, err := userArray(field)
	if err != nil {
		return nil, err
	}
	operators := make([]Operator, 0, len(entries))
	seen := make(map[Operator]bool)
	for _, entry := range entries {
		fields, err := objectFields(entry)
		if err != nil {
			return nil, err
		}
		suffix, err := userString(fields["suffix"])
		if err != nil {
			return nil, err
		}
		label, err := userString(fields["label"])
		if err != nil || strings.TrimSpace(label) == "" {
			return nil, userError("运营商名称不能为空")
		}
		operator := Operator{Suffix: suffix, Label: label}
		if !seen[operator] {
			operators = append(operators, operator)
			seen[operator] = true
		}
	}
	return operators, nil
}
