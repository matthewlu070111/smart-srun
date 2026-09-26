package domain

// ConfigSchemaVersion is the on-disk shape version.
//
// v2 is a deliberate break from the 1.x UCI-style string map. It is not read by
// 1.x; finding an old file at startup is reported, never converted. Explicit
// versioned backup import is the only supported 1.6.1 conversion path.
const ConfigSchemaVersion = 2

// Config is the whole persisted user configuration.
//
// Every field is a decided value: there is no map[string]any escape hatch and
// no runtime state. Runtime state lives in /var/run and is owned by the daemon,
// so a crash can never leave a scheduling artifact in the user's settings.
type Config struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`

	Enabled         bool   `json:"enabled"`
	MultiWANEnabled bool   `json:"multi_wan_enabled"`
	School          string `json:"school"`
	STAIface        string `json:"sta_iface"`

	LoginDefaults LoginDefaults      `json:"login_defaults"`
	Selection     Selection          `json:"selection"`
	Quiet         QuietConfig        `json:"quiet"`
	Retry         RetryConfig        `json:"retry"`
	Checks        ChecksConfig       `json:"checks"`
	Failover      FailoverConfig     `json:"failover"`
	Log           LogConfig          `json:"log"`
	PresetUpdates PresetUpdateConfig `json:"preset_updates"`

	CampusAccounts  []CampusAccount  `json:"campus_accounts"`
	HotspotProfiles []HotspotProfile `json:"hotspot_profiles"`

	// SchoolExtra holds values declared by the selected strategy's descriptors.
	// Keys the current strategy does not declare are dropped at normalization
	// time, so switching strategies cannot carry another strategy's private
	// parameters along.
	SchoolExtra map[string]any `json:"school_extra"`
}

// LoginDefaults are the protocol strings used when an account does not override
// them. They stay strings: "1" and 1 are not interchangeable on the wire, and
// converting to int would throw away a valid representation.
type LoginDefaults struct {
	N    string `json:"n"`
	Type string `json:"type"`
	Enc  string `json:"enc"`
}

// Selection points at accounts by ID.
//
// IDs, not array positions: renaming or reordering the list must not silently
// change which account authenticates.
type Selection struct {
	ActiveCampusID   string `json:"active_campus_id"`
	DefaultCampusID  string `json:"default_campus_id"`
	ActiveHotspotID  string `json:"active_hotspot_id"`
	DefaultHotspotID string `json:"default_hotspot_id"`
}

type QuietConfig struct {
	Enabled     bool      `json:"enabled"`
	Start       ClockTime `json:"start"`
	End         ClockTime `json:"end"`
	ForceLogout bool      `json:"force_logout"`
}

// Window is the evaluable form of the configured quiet hours.
func (q QuietConfig) Window() QuietWindow {
	return QuietWindow{Start: q.Start, End: q.End}
}

// RetryConfig is the one retry policy. The 1.x exponent/inter/outer factors are
// gone: they were normalized and stored but never read by any scheduler path
// (decision D05).
type RetryConfig struct {
	Enabled bool `json:"enabled"`
	// MaxRetries counts attempts *after* the first one. 0 means no finite cap;
	// long-running maintenance is then bounded by cancellation and policy, not
	// by a retry count.
	MaxRetries     int     `json:"max_retries"`
	InitialSeconds Seconds `json:"initial_seconds"`
	MaxSeconds     Seconds `json:"max_seconds"`
}

type ChecksConfig struct {
	IntervalSeconds         int       `json:"interval_seconds"`
	Mode                    CheckMode `json:"mode"`
	SwitchTimeoutSeconds    int       `json:"switch_timeout_seconds"`
	TerminalAttempts        int       `json:"terminal_attempts"`
	TerminalIntervalSeconds int       `json:"terminal_interval_seconds"`
}

type FailoverConfig struct {
	Enabled                bool `json:"enabled"`
	HotspotFailbackEnabled bool `json:"hotspot_failback_enabled"`
}

type LogConfig struct {
	Level LogLevel `json:"level"`
}

// PresetUpdateConfig schedules one catalogue check per Beijing calendar day.
type PresetUpdateConfig struct {
	Enabled bool      `json:"enabled"`
	Time    ClockTime `json:"time"`
}

// CampusAccount is one campus identity plus the environment it authenticates
// through. AccessMode discriminates which half of the fields apply; the other
// half is cleared before the account is written, so a stored account never
// carries an effective configuration for a mode it is not in.
type CampusAccount struct {
	ID    string `json:"id"`
	Label string `json:"label"`

	UserID   string `json:"user_id"`
	Password string `json:"password"`

	// Operator is a display label only. OperatorSuffix alone decides the
	// username sent to the gateway: empty means the plain account, non-empty
	// means user@suffix. They are separate fields because a school can offer
	// two differently-labelled options that share one suffix.
	Operator       string `json:"operator"`
	OperatorSuffix string `json:"operator_suffix"`

	AccessMode AccessMode `json:"access_mode"`

	// Wired half.
	WiredIface string `json:"wired_iface,omitempty"`
	// AuthEnabled opts this account into the multi-WAN daemon. It only takes
	// effect while the global MultiWANEnabled is on.
	AuthEnabled bool `json:"auth_enabled,omitempty"`

	BaseURL string `json:"base_url"`
	ACID    string `json:"ac_id"`

	// Wireless half.
	SSID        string      `json:"ssid,omitempty"`
	Radio       string      `json:"radio,omitempty"`
	Encryption  string      `json:"encryption,omitempty"`
	Key         string      `json:"key,omitempty"`
	APSelection APSelection `json:"ap_selection,omitempty"`
	BSSID       string      `json:"bssid,omitempty"`

	Login LoginShape `json:"login"`

	// PresetID records which catalogue entry the user filled this account from.
	// It is provenance, not a strategy id, and refreshing the catalogue must
	// never use it to overwrite saved parameters.
	PresetID string `json:"preset_id,omitempty"`
}

// IsWired is the discriminator every mode-dependent decision goes through.
func (a CampusAccount) IsWired() bool { return a.AccessMode == AccessModeWired }

// LoginShape are per-account protocol overrides. Every field is optional:
// absent means "fall back", which is why DoubleStack is a pointer -- an
// explicit false has to survive a round trip distinguishably from unset.
type LoginShape struct {
	N           string `json:"n,omitempty"`
	Type        string `json:"type,omitempty"`
	Enc         string `json:"enc,omitempty"`
	InfoPrefix  string `json:"info_prefix,omitempty"`
	DoubleStack *bool  `json:"double_stack,omitempty"`
	OS          string `json:"os,omitempty"`
	Name        string `json:"name,omitempty"`
	// Alphabet is the 64-byte Base64 table this school's gateway decodes the
	// encrypted info blob with. Empty means the table almost every SRun
	// deployment uses.
	//
	// It is here rather than in a strategy because it is the same kind of thing
	// as the fields above it: a protocol parameter that varies by deployment and
	// needs no code to express. The baseline carried it as a per-school Python
	// constant, which meant a school with a different table could only be
	// supported by shipping a new module.
	Alphabet string `json:"alphabet,omitempty"`
}

// HotspotProfile is a fallback uplink. AP policy and BSSID pinning belong to
// wireless campus accounts; the hotspot form has never offered them and the Go
// rewrite does not add them.
type HotspotProfile struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	SSID       string `json:"ssid"`
	Encryption string `json:"encryption"`
	Key        string `json:"key"`
	Radio      string `json:"radio"`
}

// CampusAccountByID returns a copy of the account with the given ID.
func (c *Config) CampusAccountByID(id string) (CampusAccount, bool) {
	for _, account := range c.CampusAccounts {
		if account.ID == id {
			return account, true
		}
	}
	return CampusAccount{}, false
}

// HotspotByID returns a copy of the hotspot with the given ID.
func (c *Config) HotspotByID(id string) (HotspotProfile, bool) {
	for _, hotspot := range c.HotspotProfiles {
		if hotspot.ID == id {
			return hotspot, true
		}
	}
	return HotspotProfile{}, false
}

// ManagedWiredAccounts lists the accounts the multi-WAN daemon maintains.
//
// Both gates matter: the global switch and the per-account opt-in. An account
// that opted in while the global switch is off is not managed.
func (c *Config) ManagedWiredAccounts() []CampusAccount {
	if !c.MultiWANEnabled {
		return nil
	}
	var out []CampusAccount
	for _, account := range c.CampusAccounts {
		if account.IsWired() && account.AuthEnabled {
			out = append(out, account)
		}
	}
	return out
}
