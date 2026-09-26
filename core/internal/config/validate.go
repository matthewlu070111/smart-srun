package config

import (
	"fmt"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Validate reports every problem in one pass.
//
// All of them, not the first: a settings form that reveals its mistakes one
// save at a time wastes the user's time, and the LuCI page attaches each
// problem to its own field.
//
// Validate assumes Normalize has run. It never repairs a value -- a
// configuration that was silently corrected on load is one nobody can reason
// about afterwards.
func Validate(cfg domain.Config) error {
	problems := &domain.Errors{Code: domain.CodeInvalidConfig}

	if cfg.SchemaVersion != domain.ConfigSchemaVersion {
		problems.Addf("schema_version", "必须是 %d", domain.ConfigSchemaVersion)
	}
	checkBytes(problems, "school", cfg.School, MaxNameBytes, true)
	checkBytes(problems, "sta_iface", cfg.STAIface, MaxNameBytes, false)

	validateLoginDefaults(problems, cfg.LoginDefaults)
	validateRetry(problems, cfg.Retry)
	validateChecks(problems, cfg.Checks)

	if !cfg.Log.Level.Valid() {
		problems.Addf("log.level", "必须是 %s 之一", joinLogLevels())
	}

	validateCampusAccounts(problems, cfg)
	validateHotspots(problems, cfg)
	validateSelection(problems, cfg)
	validateSchoolExtra(problems, cfg)

	return problems.Err()
}

func validateLoginDefaults(problems *domain.Errors, defaults domain.LoginDefaults) {
	checkBytes(problems, "login_defaults.n", defaults.N, MaxNameBytes, true)
	checkBytes(problems, "login_defaults.type", defaults.Type, MaxNameBytes, true)
	checkBytes(problems, "login_defaults.enc", defaults.Enc, MaxNameBytes, true)
}

func validateRetry(problems *domain.Errors, retry domain.RetryConfig) {
	if retry.MaxRetries < MinMaxRetries || retry.MaxRetries > MaxMaxRetries {
		problems.Addf("retry.max_retries",
			"必须在 %d 到 %d 之间（0 表示不设有限次数）", MinMaxRetries, MaxMaxRetries)
	}
	if retry.InitialSeconds > MaxCooldownSeconds {
		problems.Addf("retry.initial_seconds", "不能超过 %d 秒", MaxCooldownSeconds)
	}
	if retry.MaxSeconds > MaxCooldownSeconds {
		problems.Addf("retry.max_seconds", "不能超过 %d 秒", MaxCooldownSeconds)
	}
	if retry.InitialSeconds > retry.MaxSeconds {
		problems.Addf("retry.max_seconds",
			"退避上限 %s 秒不能小于首次等待 %s 秒",
			retry.MaxSeconds.String(), retry.InitialSeconds.String())
	}
}

func validateChecks(problems *domain.Errors, checks domain.ChecksConfig) {
	checkRange(problems, "checks.interval_seconds", checks.IntervalSeconds,
		MinIntervalSeconds, MaxIntervalSeconds)
	if !checks.Mode.Valid() {
		problems.Addf("checks.mode", "必须是 internet、portal 或 ssid")
	}
	checkRange(problems, "checks.switch_timeout_seconds", checks.SwitchTimeoutSeconds,
		MinSwitchTimeoutSeconds, MaxSwitchTimeoutSeconds)
	checkRange(problems, "checks.terminal_attempts", checks.TerminalAttempts,
		MinTerminalAttempts, MaxTerminalAttempts)
	checkRange(problems, "checks.terminal_interval_seconds", checks.TerminalIntervalSeconds,
		MinTerminalIntervalSeconds, MaxTerminalIntervalSeconds)
}

func validateHotspots(problems *domain.Errors, cfg domain.Config) {
	if len(cfg.HotspotProfiles) > MaxHotspotProfiles {
		problems.Addf("hotspot_profiles", "最多 %d 个热点", MaxHotspotProfiles)
	}
	seen := make(map[string]int, len(cfg.HotspotProfiles))
	for i, hotspot := range cfg.HotspotProfiles {
		field := func(name string) string {
			return fmt.Sprintf("hotspot_profiles[%d].%s", i, name)
		}
		checkBytes(problems, field("id"), hotspot.ID, MaxIDBytes, true)
		checkBytes(problems, field("label"), hotspot.Label, MaxLabelBytes, false)
		checkBytes(problems, field("ssid"), hotspot.SSID, MaxNameBytes, true)
		checkBytes(problems, field("radio"), hotspot.Radio, MaxNameBytes, false)
		checkBytes(problems, field("key"), hotspot.Key, MaxSecretBytes, false)
		if KeyRequired(hotspot.Encryption) && hotspot.Key == "" {
			problems.Addf(field("key"), "加密方式为 %s 时必须填写密码", hotspot.Encryption)
		}
		if previous, duplicate := seen[hotspot.ID]; duplicate && hotspot.ID != "" {
			problems.Addf(field("id"), "与第 %d 个热点的 ID 重复", previous+1)
		}
		seen[hotspot.ID] = i
	}
}

func validateSelection(problems *domain.Errors, cfg domain.Config) {
	campusIDs := make(map[string]struct{}, len(cfg.CampusAccounts))
	for _, account := range cfg.CampusAccounts {
		campusIDs[account.ID] = struct{}{}
	}
	hotspotIDs := make(map[string]struct{}, len(cfg.HotspotProfiles))
	for _, hotspot := range cfg.HotspotProfiles {
		hotspotIDs[hotspot.ID] = struct{}{}
	}

	checkPointer(problems, "selection.active_campus_id",
		cfg.Selection.ActiveCampusID, campusIDs, "校园账号")
	checkPointer(problems, "selection.default_campus_id",
		cfg.Selection.DefaultCampusID, campusIDs, "校园账号")
	checkPointer(problems, "selection.active_hotspot_id",
		cfg.Selection.ActiveHotspotID, hotspotIDs, "热点")
	checkPointer(problems, "selection.default_hotspot_id",
		cfg.Selection.DefaultHotspotID, hotspotIDs, "热点")
}

func checkPointer(problems *domain.Errors, field, id string,
	known map[string]struct{}, kind string) {
	if id == "" {
		return
	}
	if _, ok := known[id]; !ok {
		problems.Addf(field, "指向不存在的%s %q", kind, id)
	}
}

func validateSchoolExtra(problems *domain.Errors, cfg domain.Config) {
	if len(cfg.SchoolExtra) > MaxSchoolExtraKeys {
		problems.Addf("school_extra", "最多 %d 个键", MaxSchoolExtraKeys)
	}
	for key, value := range cfg.SchoolExtra {
		field := "school_extra." + key
		checkBytes(problems, field, key, MaxIDBytes, true)
		switch typed := value.(type) {
		case string:
			checkBytes(problems, field, typed, MaxNameBytes, false)
		case bool, float64:
		case []any:
			if len(typed) > MaxSchoolExtraKeys {
				problems.Addf(field, "数组元素过多")
			}
			for _, item := range typed {
				if _, ok := item.(string); !ok {
					problems.Addf(field, "多选项的元素必须是字符串")
					break
				}
			}
		default:
			problems.Addf(field, "策略私有字段只接受字符串、布尔值、数字或字符串数组")
		}
	}
}

// checkBytes enforces a byte-length bound, and non-emptiness when required.
func checkBytes(problems *domain.Errors, field, value string, limit int, required bool) {
	if required && value == "" {
		problems.Addf(field, "不能为空")
		return
	}
	if len(value) > limit {
		problems.Addf(field, "超过 %d 字节上限（实际 %d 字节）", limit, len(value))
	}
}

func checkRange(problems *domain.Errors, field string, value, low, high int) {
	if value < low || value > high {
		problems.Addf(field, "必须在 %d 到 %d 之间（当前 %d）", low, high, value)
	}
}

func joinLogLevels() string {
	names := make([]string, 0, len(domain.LogLevels()))
	for _, level := range domain.LogLevels() {
		names = append(names, string(level))
	}
	return strings.Join(names, "、")
}
