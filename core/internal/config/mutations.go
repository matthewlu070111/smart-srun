package config

import (
	"strconv"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Presence rules for the patch types below.
//
// A nil field is absent and keeps the stored value; a non-nil field replaces
// it, including with an empty string. That distinction is the whole point: an
// account dialog that does not show the password submits no password field and
// must not clear it, while a user who genuinely wants it empty sends an empty
// string. Nothing here treats a placeholder such as "******" as "unchanged" --
// a user whose password really is that would be unable to keep it.

// CampusPatch is a partial campus account.
type CampusPatch struct {
	// ID selects the account to edit. Empty means create a new one; the
	// repository assigns the identifier, because an identity the client chose
	// could collide with one added since the page was opened.
	ID string `json:"id,omitempty"`

	Label          *string `json:"label,omitempty"`
	UserID         *string `json:"user_id,omitempty"`
	Password       *string `json:"password,omitempty"`
	Operator       *string `json:"operator,omitempty"`
	OperatorSuffix *string `json:"operator_suffix,omitempty"`
	AccessMode     *string `json:"access_mode,omitempty"`
	BaseURL        *string `json:"base_url,omitempty"`
	ACID           *string `json:"ac_id,omitempty"`
	PresetID       *string `json:"preset_id,omitempty"`

	// Wired half.
	WiredIface  *string `json:"wired_iface,omitempty"`
	AuthEnabled *bool   `json:"auth_enabled,omitempty"`

	// Wireless half.
	SSID        *string `json:"ssid,omitempty"`
	Radio       *string `json:"radio,omitempty"`
	Encryption  *string `json:"encryption,omitempty"`
	Key         *string `json:"key,omitempty"`
	APSelection *string `json:"ap_selection,omitempty"`
	BSSID       *string `json:"bssid,omitempty"`

	Login *LoginPatch `json:"login,omitempty"`
}

// LoginPatch is a partial per-account protocol override.
type LoginPatch struct {
	N           *string `json:"n,omitempty"`
	Type        *string `json:"type,omitempty"`
	Enc         *string `json:"enc,omitempty"`
	InfoPrefix  *string `json:"info_prefix,omitempty"`
	DoubleStack **bool  `json:"double_stack,omitempty"`
	OS          *string `json:"os,omitempty"`
	Name        *string `json:"name,omitempty"`
	// Absent keeps the stored table. The frozen LuCI account form does not
	// submit this field, so a page save must not be able to clear a table set
	// from the CLI -- which is exactly what the pointer means here.
	Alphabet *string `json:"alphabet,omitempty"`
}

// HotspotPatch is a partial hotspot profile.
type HotspotPatch struct {
	ID string `json:"id,omitempty"`

	Label      *string `json:"label,omitempty"`
	SSID       *string `json:"ssid,omitempty"`
	Encryption *string `json:"encryption,omitempty"`
	Key        *string `json:"key,omitempty"`
	Radio      *string `json:"radio,omitempty"`
}

// Settings is the half of the configuration the settings page owns.
//
// Accounts, hotspots and the selection pointers are deliberately absent. They
// have their own operations, and a settings form that carried them would let
// saving an unrelated checkbox overwrite an account -- or, worse, submit an
// empty password field and clear a credential the page never displayed.
type Settings struct {
	Enabled         bool   `json:"enabled"`
	MultiWANEnabled bool   `json:"multi_wan_enabled"`
	School          string `json:"school"`
	STAIface        string `json:"sta_iface"`

	LoginDefaults domain.LoginDefaults      `json:"login_defaults"`
	Quiet         domain.QuietConfig        `json:"quiet"`
	Retry         domain.RetryConfig        `json:"retry"`
	Checks        domain.ChecksConfig       `json:"checks"`
	Failover      domain.FailoverConfig     `json:"failover"`
	Log           domain.LogConfig          `json:"log"`
	PresetUpdates domain.PresetUpdateConfig `json:"preset_updates"`

	// SchoolExtra is strategy-private storage. ApplySettings drops the previous
	// strategy's values on a switch; keys the selected strategy never declared
	// are dropped by Normalize, through FilterSchoolExtra and SchoolRegistry.
	SchoolExtra map[string]any `json:"school_extra"`
}

// SettingsOf reads the settings half out of a configuration.
func SettingsOf(cfg domain.Config) Settings {
	extra := make(map[string]any, len(cfg.SchoolExtra))
	for key, value := range cfg.SchoolExtra {
		extra[key] = cloneExtraValue(value)
	}
	return Settings{
		Enabled:         cfg.Enabled,
		MultiWANEnabled: cfg.MultiWANEnabled,
		School:          cfg.School,
		STAIface:        cfg.STAIface,
		LoginDefaults:   cfg.LoginDefaults,
		Quiet:           cfg.Quiet,
		Retry:           cfg.Retry,
		Checks:          cfg.Checks,
		Failover:        cfg.Failover,
		Log:             cfg.Log,
		PresetUpdates:   cfg.PresetUpdates,
		SchoolExtra:     extra,
	}
}

// ApplySettings replaces the settings half.
//
// Changing the school clears school_extra: those values belong to the strategy
// that declared them, and carrying them into another strategy would hand it
// parameters it never asked for and cannot interpret.
func ApplySettings(settings Settings) Change {
	return func(cfg *domain.Config) error {
		switching := strings.TrimSpace(settings.School) != cfg.School

		cfg.Enabled = settings.Enabled
		cfg.MultiWANEnabled = settings.MultiWANEnabled
		cfg.School = settings.School
		cfg.STAIface = settings.STAIface
		cfg.LoginDefaults = settings.LoginDefaults
		cfg.Quiet = settings.Quiet
		cfg.Retry = settings.Retry
		cfg.Checks = settings.Checks
		cfg.Failover = settings.Failover
		cfg.Log = settings.Log
		cfg.PresetUpdates = settings.PresetUpdates

		if switching {
			cfg.SchoolExtra = map[string]any{}
			return nil
		}
		extra := make(map[string]any, len(settings.SchoolExtra))
		for key, value := range settings.SchoolExtra {
			extra[key] = cloneExtraValue(value)
		}
		cfg.SchoolExtra = extra
		return nil
	}
}

// UpsertCampus creates or edits one campus account.
func UpsertCampus(patch CampusPatch) Change {
	return func(cfg *domain.Config) error {
		index := indexOfCampus(cfg, patch.ID)
		if patch.ID != "" && index < 0 {
			return domain.FieldErrorf(domain.CodeNotFound, "id",
				"找不到校园账号 %q", patch.ID)
		}

		creating := index < 0
		var account domain.CampusAccount
		if creating {
			if len(cfg.CampusAccounts) >= MaxCampusAccounts {
				return domain.FieldErrorf(domain.CodeInvalidConfig, "campus_accounts",
					"最多 %d 个校园账号", MaxCampusAccounts)
			}
			account = domain.CampusAccount{
				ID:         nextID(campusIDs(cfg), "c"),
				AccessMode: domain.AccessModeWired,
			}
		} else {
			account = cfg.CampusAccounts[index]
		}

		patch.applyTo(&account)

		if creating {
			cfg.CampusAccounts = append(cfg.CampusAccounts, account)
			// The first account becomes both the active and the default one, so
			// a fresh install has something to authenticate with without a
			// second, separate action.
			if len(cfg.CampusAccounts) == 1 {
				cfg.Selection.ActiveCampusID = account.ID
				cfg.Selection.DefaultCampusID = account.ID
			}
			return nil
		}
		cfg.CampusAccounts[index] = account
		return nil
	}
}

// RemoveCampus deletes one campus account and repairs the pointers to it.
func RemoveCampus(id string) Change {
	return func(cfg *domain.Config) error {
		index := indexOfCampus(cfg, id)
		if index < 0 {
			return domain.FieldErrorf(domain.CodeNotFound, "id",
				"找不到校园账号 %q", id)
		}
		cfg.CampusAccounts = append(cfg.CampusAccounts[:index],
			cfg.CampusAccounts[index+1:]...)
		return nil
	}
}

// SetDefaultCampus points both campus pointers at one account.
//
// Both, because the baseline's "set as default" also switches to it, and
// leaving active behind would mean the button appeared to do nothing until the
// next reconnect.
func SetDefaultCampus(id string) Change {
	return func(cfg *domain.Config) error {
		if indexOfCampus(cfg, id) < 0 {
			return domain.FieldErrorf(domain.CodeNotFound, "id",
				"找不到校园账号 %q", id)
		}
		cfg.Selection.DefaultCampusID = id
		cfg.Selection.ActiveCampusID = id
		return nil
	}
}

// UpsertHotspot creates or edits one hotspot profile.
func UpsertHotspot(patch HotspotPatch) Change {
	return func(cfg *domain.Config) error {
		index := indexOfHotspot(cfg, patch.ID)
		if patch.ID != "" && index < 0 {
			return domain.FieldErrorf(domain.CodeNotFound, "id",
				"找不到热点 %q", patch.ID)
		}

		creating := index < 0
		var hotspot domain.HotspotProfile
		if creating {
			if len(cfg.HotspotProfiles) >= MaxHotspotProfiles {
				return domain.FieldErrorf(domain.CodeInvalidConfig, "hotspot_profiles",
					"最多 %d 个热点", MaxHotspotProfiles)
			}
			hotspot = domain.HotspotProfile{ID: nextID(hotspotIDs(cfg), "h")}
		} else {
			hotspot = cfg.HotspotProfiles[index]
		}

		patch.applyTo(&hotspot)

		if creating {
			cfg.HotspotProfiles = append(cfg.HotspotProfiles, hotspot)
			if len(cfg.HotspotProfiles) == 1 {
				cfg.Selection.ActiveHotspotID = hotspot.ID
				cfg.Selection.DefaultHotspotID = hotspot.ID
			}
			return nil
		}
		cfg.HotspotProfiles[index] = hotspot
		return nil
	}
}

// RemoveHotspot deletes one hotspot profile and repairs the pointers to it.
func RemoveHotspot(id string) Change {
	return func(cfg *domain.Config) error {
		index := indexOfHotspot(cfg, id)
		if index < 0 {
			return domain.FieldErrorf(domain.CodeNotFound, "id",
				"找不到热点 %q", id)
		}
		cfg.HotspotProfiles = append(cfg.HotspotProfiles[:index],
			cfg.HotspotProfiles[index+1:]...)
		return nil
	}
}

// SetDefaultHotspot points both hotspot pointers at one profile.
func SetDefaultHotspot(id string) Change {
	return func(cfg *domain.Config) error {
		if indexOfHotspot(cfg, id) < 0 {
			return domain.FieldErrorf(domain.CodeNotFound, "id",
				"找不到热点 %q", id)
		}
		cfg.Selection.DefaultHotspotID = id
		cfg.Selection.ActiveHotspotID = id
		return nil
	}
}

// RepairSelection makes the four pointers refer to entries that exist.
//
// Spec 03 fixes the rules: a dangling active falls back to the first remaining
// entry of its kind, or to empty when there is none, and a dangling default
// follows the repaired active. Running this inside the same transaction is what
// keeps a deletion from leaving a configuration that fails its own validation.
func RepairSelection(cfg *domain.Config) {
	campus := make(map[string]struct{}, len(cfg.CampusAccounts))
	for _, account := range cfg.CampusAccounts {
		campus[account.ID] = struct{}{}
	}
	hotspots := make(map[string]struct{}, len(cfg.HotspotProfiles))
	for _, hotspot := range cfg.HotspotProfiles {
		hotspots[hotspot.ID] = struct{}{}
	}

	firstCampus := ""
	if len(cfg.CampusAccounts) > 0 {
		firstCampus = cfg.CampusAccounts[0].ID
	}
	firstHotspot := ""
	if len(cfg.HotspotProfiles) > 0 {
		firstHotspot = cfg.HotspotProfiles[0].ID
	}

	// The configured default is consulted before the first account.
	//
	// Otherwise "默认账号" is a setting that decides nothing: the active pointer
	// fell straight back to whichever account happened to be first in the list,
	// and the default was then rewritten to follow the active one -- so it could
	// only ever agree with a choice it had no part in. Reading the stored value
	// here, before the line below repairs it, is what gives it an effect: delete
	// the account you are on, and you land on the one you nominated.
	cfg.Selection.ActiveCampusID = repairPointer(
		cfg.Selection.ActiveCampusID, campus, cfg.Selection.DefaultCampusID, firstCampus)
	cfg.Selection.DefaultCampusID = repairPointer(
		cfg.Selection.DefaultCampusID, campus, cfg.Selection.ActiveCampusID)
	cfg.Selection.ActiveHotspotID = repairPointer(
		cfg.Selection.ActiveHotspotID, hotspots, cfg.Selection.DefaultHotspotID, firstHotspot)
	cfg.Selection.DefaultHotspotID = repairPointer(
		cfg.Selection.DefaultHotspotID, hotspots, cfg.Selection.ActiveHotspotID)
}

// repairPointer keeps id if it still names something, otherwise takes the first
// fallback that does. Fallbacks are tried in order, so a dangling default does
// not stop the next candidate from being used.
func repairPointer(id string, known map[string]struct{}, fallbacks ...string) string {
	for _, candidate := range append([]string{id}, fallbacks...) {
		if candidate == "" {
			continue
		}
		if _, ok := known[candidate]; ok {
			return candidate
		}
	}
	return ""
}

func (p CampusPatch) applyTo(account *domain.CampusAccount) {
	assign(&account.Label, p.Label)
	assign(&account.UserID, p.UserID)
	assign(&account.Password, p.Password)
	assign(&account.Operator, p.Operator)
	assign(&account.OperatorSuffix, p.OperatorSuffix)
	assign(&account.BaseURL, p.BaseURL)
	assign(&account.ACID, p.ACID)
	assign(&account.PresetID, p.PresetID)
	assign(&account.WiredIface, p.WiredIface)
	assign(&account.SSID, p.SSID)
	assign(&account.Radio, p.Radio)
	assign(&account.Encryption, p.Encryption)
	assign(&account.Key, p.Key)
	assign(&account.BSSID, p.BSSID)

	if p.AccessMode != nil {
		account.AccessMode = domain.AccessMode(*p.AccessMode)
	}
	if p.APSelection != nil {
		account.APSelection = domain.APSelection(*p.APSelection)
	}
	if p.AuthEnabled != nil {
		account.AuthEnabled = *p.AuthEnabled
	}
	if p.Login != nil {
		p.Login.applyTo(&account.Login)
	}
}

func (p LoginPatch) applyTo(shape *domain.LoginShape) {
	assign(&shape.N, p.N)
	assign(&shape.Type, p.Type)
	assign(&shape.Enc, p.Enc)
	assign(&shape.InfoPrefix, p.InfoPrefix)
	assign(&shape.OS, p.OS)
	assign(&shape.Name, p.Name)
	assign(&shape.Alphabet, p.Alphabet)

	// Two levels of pointer: the outer one is presence, the inner one is the
	// tri-state the field itself has. Sending an explicit null clears the
	// override back to "fall back to the default"; sending false sets it.
	if p.DoubleStack != nil {
		if *p.DoubleStack == nil {
			shape.DoubleStack = nil
			return
		}
		value := **p.DoubleStack
		shape.DoubleStack = &value
	}
}

func (p HotspotPatch) applyTo(hotspot *domain.HotspotProfile) {
	assign(&hotspot.Label, p.Label)
	assign(&hotspot.SSID, p.SSID)
	assign(&hotspot.Encryption, p.Encryption)
	assign(&hotspot.Key, p.Key)
	assign(&hotspot.Radio, p.Radio)
}

func assign(target *string, value *string) {
	if value != nil {
		*target = *value
	}
}

func indexOfCampus(cfg *domain.Config, id string) int {
	if id == "" {
		return -1
	}
	for i, account := range cfg.CampusAccounts {
		if account.ID == id {
			return i
		}
	}
	return -1
}

func indexOfHotspot(cfg *domain.Config, id string) int {
	if id == "" {
		return -1
	}
	for i, hotspot := range cfg.HotspotProfiles {
		if hotspot.ID == id {
			return i
		}
	}
	return -1
}

func campusIDs(cfg *domain.Config) []string {
	out := make([]string, 0, len(cfg.CampusAccounts))
	for _, account := range cfg.CampusAccounts {
		out = append(out, account.ID)
	}
	return out
}

func hotspotIDs(cfg *domain.Config) []string {
	out := make([]string, 0, len(cfg.HotspotProfiles))
	for _, hotspot := range cfg.HotspotProfiles {
		out = append(out, hotspot.ID)
	}
	return out
}

// nextID allocates the next identifier for a kind.
//
// One past the highest number currently in the list, rather than the lowest
// free one, so an ordinary add-edit-add sequence never reuses an identifier
// that is still referenced by an open page.
//
// This is uniqueness within the configuration, which is what spec 03 requires.
// It is not a permanent reservation: delete the last account and the next one
// created takes its identifier back. Making that impossible would mean storing
// a counter in the configuration file, which is on-disk schema the contract
// does not declare. A queued action that outlives the account it names is
// handled where actions are, by tying them to a configuration revision (M07),
// not by never reusing a name.
//
// Deterministic, so a test can state the expected identifier rather than
// pattern-match it.
func nextID(existing []string, prefix string) string {
	highest := 0
	taken := make(map[string]struct{}, len(existing))
	for _, id := range existing {
		taken[id] = struct{}{}
		digits, ok := strings.CutPrefix(id, prefix)
		if !ok {
			continue
		}
		if number, err := strconv.Atoi(digits); err == nil && number > highest {
			highest = number
		}
	}

	// The loop covers identifiers a user chose by hand that happen to collide
	// with the next generated one.
	for candidate := highest + 1; ; candidate++ {
		id := prefix + strconv.Itoa(candidate)
		if _, clash := taken[id]; !clash {
			return id
		}
	}
}
