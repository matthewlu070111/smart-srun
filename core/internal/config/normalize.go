package config

import (
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Normalize canonicalises the fields where a canonical form exists, and leaves
// the rest exactly as the user typed them.
//
// There is no general "clean every string" pass. Trimming a password or a
// wireless key turns a working credential into a failing one that looks
// correct in the form, and an SSID may legitimately begin or end with a space.
// The fields listed here are the ones with a defined canonical form:
//
//	trimmed   identifiers, labels, interface and radio names, URLs
//	lowercased  enum-like values and BSSIDs
//	untouched   user_id, password, key, ssid, operator_suffix, login.*
//
// Normalize also clears the half of an account that its access mode does not
// use, so a stored account never carries an effective configuration for a mode
// it is not in.
func Normalize(cfg domain.Config) domain.Config {
	cfg.School = strings.TrimSpace(cfg.School)
	if cfg.School == "" {
		cfg.School = DefaultSchool
	}
	cfg.STAIface = strings.TrimSpace(cfg.STAIface)
	cfg.Log.Level = domain.LogLevel(strings.ToUpper(strings.TrimSpace(string(cfg.Log.Level))))
	cfg.Checks.Mode = domain.CheckMode(strings.ToLower(strings.TrimSpace(string(cfg.Checks.Mode))))

	cfg.Selection.ActiveCampusID = strings.TrimSpace(cfg.Selection.ActiveCampusID)
	cfg.Selection.DefaultCampusID = strings.TrimSpace(cfg.Selection.DefaultCampusID)
	cfg.Selection.ActiveHotspotID = strings.TrimSpace(cfg.Selection.ActiveHotspotID)
	cfg.Selection.DefaultHotspotID = strings.TrimSpace(cfg.Selection.DefaultHotspotID)

	accounts := make([]domain.CampusAccount, len(cfg.CampusAccounts))
	for i, account := range cfg.CampusAccounts {
		accounts[i] = NormalizeCampusAccount(account)
	}
	cfg.CampusAccounts = accounts

	hotspots := make([]domain.HotspotProfile, len(cfg.HotspotProfiles))
	for i, hotspot := range cfg.HotspotProfiles {
		hotspots[i] = NormalizeHotspot(hotspot)
	}
	cfg.HotspotProfiles = hotspots

	cfg.SchoolExtra, _ = FilterSchoolExtra(cfg.School, cfg.SchoolExtra)
	return cfg
}

// FilterSchoolExtra keeps only the keys the named strategy declares, and
// reports the ones it dropped.
//
// Spec 03: school_extra accepts only the current strategy's declared
// descriptors, unknown keys are dropped with a diagnostic, and switching
// strategy must not carry the previous one's private parameters along. Until
// this existed the map was stored verbatim, so a key nothing declared lived in
// the configuration forever and a strategy could be handed parameters it never
// asked for.
//
// The dropped names are returned rather than logged here: this package does no
// I/O, and the caller knows whether a person is waiting for the answer.
func FilterSchoolExtra(school string, extra map[string]any) (map[string]any, []string) {
	kept := make(map[string]any, len(extra))
	if len(extra) == 0 {
		return kept, nil
	}
	declared, known := SchoolRegistry.Lookup(school)
	var dropped []string
	for key, value := range extra {
		// An unknown strategy declares nothing, so everything goes. That is the
		// same answer as a known strategy with no fields, and it is the right
		// one: values whose meaning depends on code this build does not have
		// are not values this build can honour.
		if known && declared.DeclaresField(key) {
			kept[key] = value
			continue
		}
		dropped = append(dropped, key)
	}
	slices.Sort(dropped)
	return kept, dropped
}

// NormalizeCampusAccount canonicalises one account and clears the fields its
// access mode does not use.
func NormalizeCampusAccount(account domain.CampusAccount) domain.CampusAccount {
	account.ID = strings.TrimSpace(account.ID)
	account.Label = strings.TrimSpace(account.Label)
	account.Operator = strings.TrimSpace(account.Operator)
	account.BaseURL = strings.TrimSpace(account.BaseURL)
	account.ACID = strings.TrimSpace(account.ACID)
	account.PresetID = strings.TrimSpace(account.PresetID)
	account.AccessMode = domain.AccessMode(
		strings.ToLower(strings.TrimSpace(string(account.AccessMode))))

	if account.IsWired() {
		account.WiredIface = strings.TrimSpace(account.WiredIface)
		if account.WiredIface == "" {
			account.WiredIface = DefaultWiredIface
		}
		account.SSID = ""
		account.Radio = ""
		account.Encryption = ""
		account.Key = ""
		account.APSelection = ""
		account.BSSID = ""
		return account
	}

	account.Radio = strings.TrimSpace(account.Radio)
	account.Encryption = NormalizeEncryption(account.Encryption)
	account.BSSID = strings.ToLower(strings.TrimSpace(account.BSSID))
	account.APSelection = normalizeAPSelection(account.APSelection, account.BSSID)
	account.WiredIface = ""
	account.AuthEnabled = false
	return account
}

// NormalizeHotspot canonicalises one hotspot profile.
func NormalizeHotspot(hotspot domain.HotspotProfile) domain.HotspotProfile {
	hotspot.ID = strings.TrimSpace(hotspot.ID)
	hotspot.Label = strings.TrimSpace(hotspot.Label)
	hotspot.Radio = strings.TrimSpace(hotspot.Radio)
	hotspot.Encryption = NormalizeEncryption(hotspot.Encryption)
	return hotspot
}

// EncryptionNone is the canonical spelling for an open network.
const EncryptionNone = "none"

// NormalizeEncryption folds the several ways an open network is written into
// one value, so "same SSID, different encryption" stays a real distinction.
func NormalizeEncryption(value string) string {
	folded := strings.ToLower(strings.TrimSpace(value))
	switch folded {
	case "", "none", "open", "nopass":
		return EncryptionNone
	default:
		return folded
	}
}

// KeyRequired reports whether a passphrase is needed for this encryption.
func KeyRequired(encryption string) bool {
	return NormalizeEncryption(encryption) != EncryptionNone
}

// normalizeAPSelection keeps the policy consistent with whether a BSSID was
// actually supplied: an unrecognised policy with a BSSID means "fixed", and
// without one means "auto". A *stated* fixed policy with a missing or invalid
// BSSID is not quietly downgraded here -- validation rejects it, because
// silently roaming away from a pinned AP is exactly the surprise a user who
// pinned it was trying to avoid.
func normalizeAPSelection(policy domain.APSelection, bssid string) domain.APSelection {
	folded := domain.APSelection(strings.ToLower(strings.TrimSpace(string(policy))))
	if folded.Valid() {
		return folded
	}
	if bssid != "" {
		return domain.APSelectionFixed
	}
	return domain.APSelectionAuto
}
