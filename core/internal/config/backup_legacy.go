package config

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Legacy scalar names map to exactly one v2 field. Obsolete tuning fields are
// recognized separately and reported; arbitrary fields never disappear silently.
var legacyFields = map[string]string{
	"enabled": "enabled", "multi_wan_enabled": "multi_wan_enabled", "school": "school", "sta_iface": "sta_iface",
	"n": "login_defaults.n", "type": "login_defaults.type", "enc": "login_defaults.enc",
	"active_campus_id": "selection.active_campus_id", "default_campus_id": "selection.default_campus_id",
	"active_hotspot_id": "selection.active_hotspot_id", "default_hotspot_id": "selection.default_hotspot_id",
	"quiet_hours_enabled": "quiet.enabled", "quiet_start": "quiet.start", "quiet_end": "quiet.end", "force_logout_in_quiet": "quiet.force_logout",
	"backoff_enable": "retry.enabled", "backoff_max_retries": "retry.max_retries",
	"retry_cooldown_seconds": "retry.initial_seconds", "retry_max_cooldown_seconds": "retry.max_seconds",
	"interval": "checks.interval_seconds", "connectivity_check_mode": "checks.mode", "switch_ready_timeout_seconds": "checks.switch_timeout_seconds",
	"manual_terminal_check_max_attempts": "checks.terminal_attempts", "manual_terminal_check_interval_seconds": "checks.terminal_interval_seconds",
	"failover_enabled": "failover.enabled", "hotspot_failback_enabled": "failover.hotspot_failback_enabled", "log_level": "log.level",
}

var legacyObsolete = map[string]bool{
	"backoff_initial_duration": true, "backoff_max_duration": true,
	"backoff_exponent_factor": true, "backoff_inter_const_factor": true, "backoff_outer_const_factor": true, "developer_mode": true,
}

func importLegacy(data []byte) (domain.Config, []string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields["campus_accounts"] == nil || fields["hotspot_profiles"] == nil {
		return domain.Config{}, nil, invalidBackup()
	}
	defaults, _ := json.Marshal(Defaults())
	var target map[string]any
	_ = json.Unmarshal(defaults, &target)
	obsolete := false
	for key, raw := range fields {
		if key == "campus_accounts" || key == "hotspot_profiles" || key == "school_extra" {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return domain.Config{}, nil, invalidBackup()
		}
		if legacyObsolete[key] {
			obsolete = true
			continue
		}
		path, ok := legacyFields[key]
		if !ok {
			return domain.Config{}, nil, invalidBackup()
		}
		object := target
		parts := strings.Split(path, ".")
		if len(parts) == 2 {
			object = target[parts[0]].(map[string]any)
		}
		name := parts[len(parts)-1]
		converted, err := legacyScalar(value, object[name])
		if err != nil {
			return domain.Config{}, nil, err
		}
		object[name] = converted
	}
	var accounts []map[string]string
	if json.Unmarshal(fields["campus_accounts"], &accounts) != nil || len(accounts) > MaxCampusAccounts {
		return domain.Config{}, nil, invalidBackup()
	}
	converted := make([]domain.CampusAccount, 0, len(accounts))
	for _, item := range accounts {
		account, err := legacyAccount(item)
		if err != nil {
			return domain.Config{}, nil, err
		}
		converted = append(converted, account)
	}
	target["campus_accounts"] = converted
	var hotspots []domain.HotspotProfile
	if decodeBackupArray(fields["hotspot_profiles"], &hotspots) != nil {
		return domain.Config{}, nil, invalidBackup()
	}
	target["hotspot_profiles"] = hotspots
	if extra, ok := fields["school_extra"]; ok {
		var values map[string]any
		if json.Unmarshal(extra, &values) != nil {
			return domain.Config{}, nil, invalidBackup()
		}
		target["school_extra"] = values
	}
	encoded, _ := json.Marshal(target)
	cfg, err := Parse(encoded)
	warnings := []string{"导入后自动守护关闭；请确认账号、网口和无线设备后再启用。", "自建学校预设目录不包含在配置备份中，需要单独恢复。", "每日学校预设更新采用 2.0 默认值：北京时间 09:00。"}
	if obsolete {
		warnings = append(warnings, "1.x 的开发模式及旧退避公式不再使用；重试等待采用 retry_cooldown_seconds / retry_max_cooldown_seconds。")
	}
	return cfg, warnings, err
}

func legacyScalar(value string, seed any) (any, error) {
	switch seed.(type) {
	case bool:
		if value != "0" && value != "1" {
			return nil, invalidBackup()
		}
		return value == "1", nil
	case float64:
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return nil, invalidBackup()
		}
		return number, nil
	default:
		return value, nil
	}
}

// The enclosing backup already passed duplicate/null/depth checks.
func decodeBackupArray(data []byte, target any) error {
	if !exactFields(data, reflect.TypeOf(target)) {
		return invalidBackup()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func legacyAccount(item map[string]string) (domain.CampusAccount, error) {
	allowed := " id label user_id password operator operator_suffix access_mode base_url ac_id ssid radio encryption key ap_selection bssid wired_iface auth_enabled n type enc info_prefix double_stack login_os login_name "
	for key := range item {
		if !strings.Contains(allowed, " "+key+" ") {
			return domain.CampusAccount{}, invalidBackup()
		}
	}
	account := domain.CampusAccount{
		ID: item["id"], Label: item["label"], UserID: item["user_id"], Password: item["password"], Operator: item["operator"], OperatorSuffix: item["operator_suffix"],
		AccessMode: domain.AccessMode(item["access_mode"]), BaseURL: item["base_url"], ACID: item["ac_id"], WiredIface: item["wired_iface"],
		SSID: item["ssid"], Radio: item["radio"], Encryption: item["encryption"], Key: item["key"], APSelection: domain.APSelection(item["ap_selection"]), BSSID: item["bssid"],
		Login: domain.LoginShape{N: item["n"], Type: item["type"], Enc: item["enc"], InfoPrefix: item["info_prefix"], OS: item["login_os"], Name: item["login_name"]},
	}
	if account.AccessMode == "" {
		account.AccessMode = domain.AccessModeWiFi
	}
	if account.ACID == "" {
		account.ACID = "1"
	}
	if account.OperatorSuffix == UnverifiedOperatorSuffix {
		account.OperatorSuffix = ""
	}
	if value := item["auth_enabled"]; value != "" {
		parsed, err := legacyScalar(value, false)
		if err != nil {
			return account, err
		}
		account.AuthEnabled = parsed.(bool)
	}
	if value := item["double_stack"]; value != "" {
		parsed, err := legacyScalar(value, false)
		if err != nil {
			return account, err
		}
		flag := parsed.(bool)
		account.Login.DoubleStack = &flag
	}
	return account, nil
}
