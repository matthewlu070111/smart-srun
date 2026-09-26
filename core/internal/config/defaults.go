package config

import (
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/protocol/srun"
)

// Built-in login shape values used when neither the account nor
// login_defaults supplies one. They are not configuration: no UI has ever
// exposed them globally, and they are the SRun defaults the baseline sent.
const (
	DefaultInfoPrefix  = "SRBX1"
	DefaultLoginOS     = "Windows 10"
	DefaultLoginName   = "Windows"
	DefaultDoubleStack = false

	DefaultWiredIface = "wan"
	DefaultSchool     = "default"
)

// Defaults returns a fresh configuration.
//
// This is the single source of default values for the whole program. The CLI,
// the RPC schema and the LuCI form all read it; nothing keeps a second copy.
// The 1.x code kept display-time fallbacks separate from the real defaults,
// which made `config show` report settings the scheduler was not using
// (decision D07).
func Defaults() domain.Config {
	return domain.Config{
		SchemaVersion:   domain.ConfigSchemaVersion,
		Revision:        0,
		Enabled:         false,
		MultiWANEnabled: false,
		School:          DefaultSchool,
		STAIface:        "",
		LoginDefaults: domain.LoginDefaults{
			N:    "200",
			Type: "1",
			Enc:  "srun_bx1",
		},
		Selection: domain.Selection{},
		Quiet: domain.QuietConfig{
			Enabled:     true,
			Start:       mustClock(0, 0),
			End:         mustClock(6, 0),
			ForceLogout: true,
		},
		Retry: domain.RetryConfig{
			Enabled:        true,
			MaxRetries:     4,
			InitialSeconds: 10,
			MaxSeconds:     60,
		},
		Checks: domain.ChecksConfig{
			IntervalSeconds:         60,
			Mode:                    domain.CheckInternet,
			SwitchTimeoutSeconds:    30,
			TerminalAttempts:        5,
			TerminalIntervalSeconds: 2,
		},
		Failover: domain.FailoverConfig{
			Enabled:                true,
			HotspotFailbackEnabled: true,
		},
		Log:           domain.LogConfig{Level: domain.LogInfo},
		PresetUpdates: domain.PresetUpdateConfig{Enabled: true, Time: mustClock(9, 0)},

		CampusAccounts:  []domain.CampusAccount{},
		HotspotProfiles: []domain.HotspotProfile{},
		SchoolExtra:     map[string]any{},
	}
}

// mustClock is only ever called with the constants above.
func mustClock(hour, minute int) domain.ClockTime {
	value, err := domain.NewClockTime(hour, minute)
	if err != nil {
		panic("config: invalid built-in default clock time: " + err.Error())
	}
	return value
}

// EffectiveLogin resolves one account's protocol parameters.
//
// Order is account, then the global login_defaults, then the built-in value.
// Resolution reads only this account: it never inherits another account's
// resolved parameters, which is what made multi-WAN accounts cross-contaminate
// in the 1.x runtime.
func EffectiveLogin(cfg domain.Config, account domain.CampusAccount) EffectiveLoginShape {
	shape := EffectiveLoginShape{
		N:          firstNonEmpty(account.Login.N, cfg.LoginDefaults.N, "200"),
		Type:       firstNonEmpty(account.Login.Type, cfg.LoginDefaults.Type, "1"),
		Enc:        firstNonEmpty(account.Login.Enc, cfg.LoginDefaults.Enc, "srun_bx1"),
		InfoPrefix: firstNonEmpty(account.Login.InfoPrefix, DefaultInfoPrefix),
		OS:         firstNonEmpty(account.Login.OS, DefaultLoginOS),
		Name:       firstNonEmpty(account.Login.Name, DefaultLoginName),
		// Resolved to the concrete table rather than left empty, so the value
		// the protocol layer will use is the value diagnostics print. There is
		// no global login_defaults.alphabet: the table belongs to a gateway,
		// and an account already carries which gateway it talks to.
		Alphabet:    firstNonEmpty(account.Login.Alphabet, srun.DefaultAlphabetTable),
		DoubleStack: DefaultDoubleStack,
	}
	if account.Login.DoubleStack != nil {
		shape.DoubleStack = *account.Login.DoubleStack
	}
	return shape
}

// EffectiveLoginShape is the resolved, complete protocol parameter set. Unlike
// LoginShape it has no optional fields: every value is decided.
type EffectiveLoginShape struct {
	N           string
	Type        string
	Enc         string
	InfoPrefix  string
	DoubleStack bool
	OS          string
	Name        string
	// Alphabet is always the concrete 64-byte table, never empty. It is not a
	// secret -- it is a public property of the gateway -- so it appears in
	// diagnostic output alongside the other resolved parameters.
	Alphabet string
}

// EffectiveUsername is the username actually sent to the gateway.
//
// The suffix field is authoritative. An empty suffix means the plain account,
// which is a real configuration and not "unset": guessing a carrier suffix here
// would authenticate as a different identity.
//
// The unverified sentinel is treated as no suffix. Validation already refuses
// to save it, so reaching here with one means an account bypassed validation --
// and "user@??" is a wrong identity, which is worse than a plain login that
// fails cleanly. Normalization deliberately does *not* do this rewrite: the
// stored value keeps the sentinel so the user is still told to fix it.
func EffectiveUsername(account domain.CampusAccount) string {
	if account.OperatorSuffix == "" || account.OperatorSuffix == UnverifiedOperatorSuffix {
		return account.UserID
	}
	return account.UserID + "@" + account.OperatorSuffix
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
