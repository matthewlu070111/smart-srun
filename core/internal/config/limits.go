package config

// Limits the configuration format guarantees.
//
// These are part of the schema, not defensive guesses: they are published to
// the UI so a form can refuse a value before it is submitted, and they are the
// same numbers validation enforces. Over-long input is an error, never silently
// truncated -- a truncated password or SSID fails later in a way the user
// cannot diagnose.
const (
	// MaxConfigBytes bounds a whole configuration document.
	MaxConfigBytes = 512 * 1024

	// String bounds are byte lengths, not rune counts: the wire and the
	// filesystem care about bytes.
	MaxIDBytes     = 64
	MaxLabelBytes  = 128
	MaxUserIDBytes = 256
	MaxSecretBytes = 1024
	MaxURLBytes    = 2048
	MaxSuffixBytes = 256
	// MaxNameBytes covers the shorter free-text fields: interface names, SSID,
	// radio, encryption, operator label, protocol strings.
	MaxNameBytes = 256

	MinIntervalSeconds = 1
	MaxIntervalSeconds = 3600

	MinSwitchTimeoutSeconds = 1
	MaxSwitchTimeoutSeconds = 300

	MinTerminalAttempts = 1
	MaxTerminalAttempts = 20

	MinTerminalIntervalSeconds = 1
	MaxTerminalIntervalSeconds = 60

	MinMaxRetries = 0
	MaxMaxRetries = 100

	// MaxCooldownSeconds bounds one wait, both the base and the cap.
	MaxCooldownSeconds = 3600

	// Collection bounds. The UI is unchanged below these numbers; they exist so
	// that a pathological configuration cannot exhaust a 128 MiB router.
	MaxCampusAccounts       = 32
	MaxHotspotProfiles      = 32
	MaxManagedWiredAccounts = 8

	// MaxSchoolExtraKeys bounds strategy-private storage.
	MaxSchoolExtraKeys = 64
)

// UnverifiedOperatorSuffix is the catalogue sentinel for "the operator's label
// is known but nobody has confirmed its real suffix".
//
// It may appear in a published preset. It may never reach an account: building
// user@?? would send a wrong username to the gateway. Validation rejects it so
// the mistake surfaces at save time rather than at login time.
const UnverifiedOperatorSuffix = "??"
