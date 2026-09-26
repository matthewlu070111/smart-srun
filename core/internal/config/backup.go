package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"unicode/utf8"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const BackupFormat = "smart-srun-config"

// Backup is an explicit credential-bearing transfer document, never runtime state.
type Backup struct {
	Format        string          `json:"format"`
	FormatVersion int             `json:"format_version"`
	ConfigSchema  int             `json:"config_schema"`
	Config        json.RawMessage `json:"config"`
}

func ExportBackup(cfg domain.Config) (Backup, error) {
	data, err := Marshal(cfg)
	if err != nil {
		return Backup{}, err
	}
	result := Backup{BackupFormat, 1, domain.ConfigSchemaVersion, data}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > MaxConfigBytes {
		return Backup{}, invalidBackup()
	}
	return result, nil
}

// ParseBackup accepts only versioned exports. Errors intentionally omit supplied
// keys/values: a malformed document may put credentials anywhere, even in a key.
func ParseBackup(data []byte) (domain.Config, []string, error) {
	if len(data) > MaxConfigBytes || !utf8.Valid(data) || scanStrict(data) != nil {
		return domain.Config{}, nil, invalidBackup()
	}
	var envelope Backup
	if DecodePatch(data, &envelope) != nil || envelope.Format != BackupFormat || envelope.FormatVersion != 1 {
		return domain.Config{}, nil, invalidBackup()
	}
	var cfg domain.Config
	var warnings []string
	var err error
	switch envelope.ConfigSchema {
	case 1:
		cfg, warnings, err = importLegacy(envelope.Config)
	case domain.ConfigSchemaVersion:
		if !exactFields(envelope.Config, reflect.TypeFor[domain.Config]()) {
			return domain.Config{}, nil, invalidBackup()
		}
		cfg, err = Parse(envelope.Config)
	default:
		err = invalidBackup()
	}
	if err != nil {
		return domain.Config{}, nil, invalidBackup()
	}
	warnings = append(warnings, droppedExtraWarnings(envelope.Config, cfg)...)
	// An export can be restored onto another router. Enabling policy is always
	// a separate user action after checking credentials and interface names.
	cfg.Enabled = false
	cfg.Revision = 0
	return cfg, warnings, nil
}

// droppedExtraWarnings names the school_extra keys the backup carried and the
// imported configuration did not keep.
//
// Parsing normalises, and normalising filters school_extra against the
// selected strategy -- silently, from here. The import preview is where a
// person decides whether to go ahead, so it is where they are told. Compared
// against the result rather than recomputed, so this stays right for both the
// 2.0 and the 1.6.1 path, whichever strategy the legacy import settled on.
// Key names only: a value may be anything, including something private.
func droppedExtraWarnings(raw json.RawMessage, cfg domain.Config) []string {
	var carried struct {
		SchoolExtra map[string]json.RawMessage `json:"school_extra"`
	}
	if json.Unmarshal(raw, &carried) != nil || len(carried.SchoolExtra) == 0 {
		return nil
	}
	var names []string
	for key := range carried.SchoolExtra {
		if _, kept := cfg.SchoolExtra[key]; !kept {
			names = append(names, key)
		}
	}
	slices.Sort(names)
	out := make([]string, 0, len(names))
	for _, key := range names {
		out = append(out, "备份中的 school_extra."+key+" 不是当前认证策略声明的字段，导入时已丢弃。")
	}
	return out
}

func invalidBackup() error {
	return domain.Errorf(domain.CodeInvalidConfig, "备份格式或配置无效；请选择 1.6.1 或 2.0 导出的 JSON，检查字段类型、范围和账号引用（上限 512 KiB）")
}
