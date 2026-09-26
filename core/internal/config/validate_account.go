package config

import (
	"fmt"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/protocol/srun"
)

func validateCampusAccounts(problems *domain.Errors, cfg domain.Config) {
	if len(cfg.CampusAccounts) > MaxCampusAccounts {
		problems.Addf("campus_accounts", "最多 %d 个校园账号", MaxCampusAccounts)
	}

	seen := make(map[string]int, len(cfg.CampusAccounts))
	managedIfaces := make(map[string]int)
	managed := 0

	for i, account := range cfg.CampusAccounts {
		field := func(name string) string {
			return fmt.Sprintf("campus_accounts[%d].%s", i, name)
		}
		validateCampusAccount(problems, field, account)

		if previous, duplicate := seen[account.ID]; duplicate && account.ID != "" {
			problems.Addf(field("id"), "与第 %d 个账号的 ID 重复", previous+1)
		}
		seen[account.ID] = i

		if !cfg.MultiWANEnabled || !account.IsWired() || !account.AuthEnabled {
			continue
		}
		managed++
		// Two managed accounts on one logical interface would authenticate over
		// the same line and fight each other. Different logical names can still
		// resolve to one L3 device; that collision is caught at binding time,
		// where the device is actually known.
		if previous, clash := managedIfaces[account.WiredIface]; clash {
			problems.Addf(field("wired_iface"),
				"与第 %d 个账号使用同一条线路 %q；多 WAN 下每条线路只能有一个启用账号",
				previous+1, account.WiredIface)
		}
		managedIfaces[account.WiredIface] = i
	}

	if managed > MaxManagedWiredAccounts {
		problems.Addf("campus_accounts",
			"多 WAN 最多同时管理 %d 个有线账号（当前 %d）",
			MaxManagedWiredAccounts, managed)
	}
}

func validateCampusAccount(problems *domain.Errors,
	field func(string) string, account domain.CampusAccount) {
	checkBytes(problems, field("id"), account.ID, MaxIDBytes, true)
	checkBytes(problems, field("label"), account.Label, MaxLabelBytes, false)
	checkBytes(problems, field("user_id"), account.UserID, MaxUserIDBytes, true)
	checkBytes(problems, field("password"), account.Password, MaxSecretBytes, false)
	checkBytes(problems, field("operator"), account.Operator, MaxLabelBytes, false)
	checkBytes(problems, field("base_url"), account.BaseURL, MaxURLBytes, false)
	checkBytes(problems, field("ac_id"), account.ACID, MaxNameBytes, false)
	checkBytes(problems, field("preset_id"), account.PresetID, MaxIDBytes, false)

	// A user id is an identity: trimming it would authenticate as someone else,
	// and accepting it as typed fails at the gateway with no clue why. Say so.
	if account.UserID != strings.TrimSpace(account.UserID) {
		problems.Addf(field("user_id"), "不能以空格开头或结尾")
	}

	validateOperatorSuffix(problems, field, account.OperatorSuffix)
	validateLoginShape(problems, field, account.Login)

	if !account.AccessMode.Valid() {
		problems.Addf(field("access_mode"), "必须是 wired 或 wifi")
		return
	}
	if account.IsWired() {
		checkBytes(problems, field("wired_iface"), account.WiredIface, MaxNameBytes, true)
		return
	}
	validateWirelessHalf(problems, field, account)
}

func validateOperatorSuffix(problems *domain.Errors,
	field func(string) string, suffix string) {
	checkBytes(problems, field("operator_suffix"), suffix, MaxSuffixBytes, false)
	if suffix == UnverifiedOperatorSuffix {
		problems.Addf(field("operator_suffix"),
			"%q 表示该运营商后缀尚未被确认，不能保存到账号；请填写真实后缀，或明确选择“无后缀”",
			UnverifiedOperatorSuffix)
		return
	}
	if suffix == "" {
		// An empty suffix is a real choice: log in as the plain account.
		return
	}
	if strings.ContainsAny(suffix, " \t\r\n") {
		problems.Addf(field("operator_suffix"), "不能包含空白字符")
	}
	if strings.Contains(suffix, "@") {
		problems.Addf(field("operator_suffix"), "只填 @ 之后的部分，不要包含 @")
	}
}

func validateLoginShape(problems *domain.Errors,
	field func(string) string, login domain.LoginShape) {
	checkBytes(problems, field("login.n"), login.N, MaxNameBytes, false)
	checkBytes(problems, field("login.type"), login.Type, MaxNameBytes, false)
	checkBytes(problems, field("login.enc"), login.Enc, MaxNameBytes, false)
	checkBytes(problems, field("login.info_prefix"), login.InfoPrefix, MaxNameBytes, false)
	checkBytes(problems, field("login.os"), login.OS, MaxNameBytes, false)
	checkBytes(problems, field("login.name"), login.Name, MaxNameBytes, false)

	// An override is checked with the same constructor the protocol layer uses.
	// A table that is the wrong length, repeats a character or contains the
	// padding byte encodes to something the gateway cannot decode, and it would
	// do so silently: the blob is encrypted and checksummed, so the only symptom
	// is a login refused for no stated reason. Refuse it at save time instead.
	if login.Alphabet != "" {
		if _, err := srun.NewAlphabet(login.Alphabet); err != nil {
			problems.Addf(field("login.alphabet"), "%s", err.Error())
		}
	}
}

func validateWirelessHalf(problems *domain.Errors,
	field func(string) string, account domain.CampusAccount) {
	checkBytes(problems, field("ssid"), account.SSID, MaxNameBytes, true)
	checkBytes(problems, field("radio"), account.Radio, MaxNameBytes, false)
	checkBytes(problems, field("encryption"), account.Encryption, MaxNameBytes, true)
	checkBytes(problems, field("key"), account.Key, MaxSecretBytes, false)

	if KeyRequired(account.Encryption) && account.Key == "" {
		problems.Addf(field("key"), "加密方式为 %s 时必须填写密码", account.Encryption)
	}
	if !account.APSelection.Valid() {
		problems.Addf(field("ap_selection"), "必须是 auto、strongest 或 fixed")
	}
	if account.APSelection == domain.APSelectionFixed {
		if account.BSSID == "" {
			problems.Addf(field("bssid"), "选择固定 BSSID 时必须填写 BSSID")
		} else if !ValidBSSID(account.BSSID) {
			problems.Addf(field("bssid"), "不是有效的单播 BSSID（形如 aa:bb:cc:dd:ee:ff）")
		}
	} else if account.BSSID != "" && !ValidBSSID(account.BSSID) {
		problems.Addf(field("bssid"), "不是有效的单播 BSSID（形如 aa:bb:cc:dd:ee:ff）")
	}
}

// ValidBSSID accepts a unicast, non-zero MAC in lowercase colon form.
//
// The multicast bit is checked because a BSSID with it set is not an address a
// station can associate to, and all-zero is the "unknown" placeholder several
// drivers report. Pinning to either would silently never connect.
func ValidBSSID(value string) bool {
	if len(value) != 17 {
		return false
	}
	var first int
	for octet := range 6 {
		offset := octet * 3
		if octet > 0 && value[offset-1] != ':' {
			return false
		}
		high, ok := hexDigit(value[offset])
		if !ok {
			return false
		}
		low, ok := hexDigit(value[offset+1])
		if !ok {
			return false
		}
		if octet == 0 {
			first = high<<4 | low
		}
	}
	if first%2 != 0 {
		return false
	}
	return value != "00:00:00:00:00:00"
}

func hexDigit(char byte) (int, bool) {
	switch {
	case char >= '0' && char <= '9':
		return int(char - '0'), true
	case char >= 'a' && char <= 'f':
		return int(char-'a') + 10, true
	default:
		return 0, false
	}
}
