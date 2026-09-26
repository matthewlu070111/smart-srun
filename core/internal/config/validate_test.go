package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func wiredAccount(id string) domain.CampusAccount {
	return domain.CampusAccount{
		ID:         id,
		Label:      "有线 " + id,
		UserID:     "2021" + id,
		Password:   "secret",
		AccessMode: domain.AccessModeWired,
		WiredIface: "wan",
		BaseURL:    "http://172.17.1.2",
		ACID:       "1",
	}
}

func wifiAccount(id string) domain.CampusAccount {
	return domain.CampusAccount{
		ID:          id,
		Label:       "无线 " + id,
		UserID:      "2021" + id,
		Password:    "secret",
		AccessMode:  domain.AccessModeWiFi,
		SSID:        "campus",
		Encryption:  "psk2",
		Key:         "wifi-pass",
		APSelection: domain.APSelectionAuto,
		BaseURL:     "http://172.17.1.2",
		ACID:        "1",
	}
}

// configWith builds a normalized, otherwise-valid configuration.
func configWith(mutate func(*domain.Config)) domain.Config {
	cfg := Defaults()
	if mutate != nil {
		mutate(&cfg)
	}
	return Normalize(cfg)
}

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	problems, ok := err.(*domain.Errors)
	if !ok {
		t.Fatalf("err = %T, want *domain.Errors so the UI can attach each "+
			"problem to its own field", err)
	}
	return problems.Fields()
}

func requireField(t *testing.T, err error, want string) {
	t.Helper()
	fields := fieldsOf(t, err)
	if !slices.Contains(fields, want) {
		t.Fatalf("problems %v do not include %q (err: %v)", fields, want, err)
	}
}

func requireValid(t *testing.T, cfg domain.Config) {
	t.Helper()
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected a valid configuration, got: %v", err)
	}
}

// T08 -- the operator suffix is the sole authority for the username sent to the
// gateway, so every one of its states has to stay distinguishable.

func TestOperatorSuffixStates(t *testing.T) {
	cases := []struct {
		name         string
		suffix       string
		wantUsername string
		wantRejected bool
	}{
		{name: "empty means the plain account", suffix: "", wantUsername: "2021c1"},
		{name: "a carrier realm", suffix: "cmcc", wantUsername: "2021c1@cmcc"},
		{name: "case is preserved", suffix: "CMCC", wantUsername: "2021c1@CMCC"},
		{name: "a dotted realm", suffix: "student.edu.cn", wantUsername: "2021c1@student.edu.cn"},
		{name: "an arbitrary realm nobody predicted", suffix: "xyz-2", wantUsername: "2021c1@xyz-2"},
		{name: "the unverified sentinel", suffix: UnverifiedOperatorSuffix, wantRejected: true},
		{name: "a suffix with an at sign", suffix: "@cmcc", wantRejected: true},
		{name: "a suffix with whitespace", suffix: "cm cc", wantRejected: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			account := wiredAccount("c1")
			account.OperatorSuffix = testCase.suffix
			cfg := configWith(func(c *domain.Config) {
				c.CampusAccounts = []domain.CampusAccount{account}
			})

			err := Validate(cfg)
			if testCase.wantRejected {
				requireField(t, err, "campus_accounts[0].operator_suffix")
				return
			}
			if err != nil {
				t.Fatalf("suffix %q rejected: %v", testCase.suffix, err)
			}
			if got := EffectiveUsername(cfg.CampusAccounts[0]); got != testCase.wantUsername {
				t.Fatalf("username = %q, want %q", got, testCase.wantUsername)
			}
		})
	}
}

// The sentinel must never become a username. This asserts the outcome, not just
// that validation complained: a future "helpful" normalization that mapped ??
// to "" would pass the validation assertion alone.
func TestUnverifiedSuffixNeverBecomesAUsername(t *testing.T) {
	account := wiredAccount("c1")
	account.OperatorSuffix = UnverifiedOperatorSuffix
	normalized := NormalizeCampusAccount(account)

	if got := EffectiveUsername(normalized); strings.Contains(got, UnverifiedOperatorSuffix) {
		t.Fatalf("username = %q; the unverified sentinel reached the wire", got)
	}
	if normalized.OperatorSuffix != UnverifiedOperatorSuffix {
		t.Fatalf("suffix = %q; normalization quietly rewrote the sentinel to a "+
			"valid-looking value, so the user is never told to fix it",
			normalized.OperatorSuffix)
	}
}

// T08 -- secrets are stored byte for byte. Trimming turns a working credential
// into a failing one that still looks right in the form.
func TestSecretsAreNotTrimmed(t *testing.T) {
	account := wifiAccount("c1")
	account.Password = "  pa ss  "
	account.Key = "\twifi key \n"
	account.SSID = " campus with spaces "

	normalized := NormalizeCampusAccount(account)

	if normalized.Password != "  pa ss  " {
		t.Errorf("password = %q, want it byte-identical", normalized.Password)
	}
	if normalized.Key != "\twifi key \n" {
		t.Errorf("key = %q, want it byte-identical", normalized.Key)
	}
	if normalized.SSID != " campus with spaces " {
		t.Errorf("ssid = %q; leading and trailing spaces are legal in an SSID",
			normalized.SSID)
	}
}

// A user id is an identity: trimming it would authenticate as someone else, so
// the typo is reported instead.
func TestUserIDWithSurroundingSpaceIsReportedNotTrimmed(t *testing.T) {
	account := wiredAccount("c1")
	account.UserID = " 2021c1 "
	cfg := configWith(func(c *domain.Config) {
		c.CampusAccounts = []domain.CampusAccount{account}
	})

	requireField(t, Validate(cfg), "campus_accounts[0].user_id")
	if cfg.CampusAccounts[0].UserID != " 2021c1 " {
		t.Fatalf("user_id = %q, want it left exactly as typed",
			cfg.CampusAccounts[0].UserID)
	}
}

// Problems from every section are reported together, not up to the first
// failing section. A user with a bad interval and a bad account must see both:
// otherwise fixing the interval only reveals the next complaint.
func TestValidateReportsEveryProblemInOnePass(t *testing.T) {
	badAccount := wiredAccount("c1")
	badAccount.OperatorSuffix = UnverifiedOperatorSuffix
	badHotspot := domain.HotspotProfile{ID: "", SSID: "", Encryption: "psk2"}

	cfg := configWith(func(c *domain.Config) {
		c.Checks.IntervalSeconds = 0
		c.Retry.MaxRetries = 1000
		c.Log.Level = "LOUD"
		c.CampusAccounts = []domain.CampusAccount{badAccount}
		c.HotspotProfiles = []domain.HotspotProfile{badHotspot}
		c.Selection.DefaultCampusID = "gone"
	})

	fields := fieldsOf(t, Validate(cfg))
	// One from each section, so a bail-out anywhere in the chain is caught.
	for _, want := range []string{
		"checks.interval_seconds",
		"retry.max_retries",
		"log.level",
		"campus_accounts[0].operator_suffix",
		"hotspot_profiles[0].ssid",
		"selection.default_campus_id",
	} {
		if !slices.Contains(fields, want) {
			t.Errorf("problems %v omit %q; the user would have to save "+
				"repeatedly to discover every mistake", fields, want)
		}
	}
}

func TestRangeLimits(t *testing.T) {
	cases := []struct {
		field  string
		mutate func(*domain.Config)
	}{
		{"checks.interval_seconds", func(c *domain.Config) { c.Checks.IntervalSeconds = 3601 }},
		{"checks.switch_timeout_seconds", func(c *domain.Config) { c.Checks.SwitchTimeoutSeconds = 0 }},
		{"checks.terminal_attempts", func(c *domain.Config) { c.Checks.TerminalAttempts = 21 }},
		{"checks.terminal_interval_seconds", func(c *domain.Config) { c.Checks.TerminalIntervalSeconds = 61 }},
		{"retry.max_retries", func(c *domain.Config) { c.Retry.MaxRetries = -1 }},
		{"retry.initial_seconds", func(c *domain.Config) { c.Retry.InitialSeconds = 3601; c.Retry.MaxSeconds = 3601 }},
		{"retry.max_seconds", func(c *domain.Config) { c.Retry.InitialSeconds = 30; c.Retry.MaxSeconds = 10 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.field, func(t *testing.T) {
			requireField(t, Validate(configWith(testCase.mutate)), testCase.field)
		})
	}
}

// 0 is a documented value for both retry knobs: no finite retry cap, and no
// cooldown. Neither may be rejected as "empty".
func TestZeroIsAcceptedWhereItIsMeaningful(t *testing.T) {
	requireValid(t, configWith(func(c *domain.Config) {
		c.Retry.MaxRetries = 0
		c.Retry.InitialSeconds = 0
		c.Retry.MaxSeconds = 0
	}))
}

func TestOverLongStringsAreRejectedNotTruncated(t *testing.T) {
	long := strings.Repeat("x", MaxSuffixBytes+1)
	account := wiredAccount("c1")
	account.OperatorSuffix = long
	cfg := configWith(func(c *domain.Config) {
		c.CampusAccounts = []domain.CampusAccount{account}
	})

	requireField(t, Validate(cfg), "campus_accounts[0].operator_suffix")
	if cfg.CampusAccounts[0].OperatorSuffix != long {
		t.Fatal("over-long value was truncated; a truncated suffix fails at the " +
			"gateway with no clue why")
	}
}

func TestSelectionMustReferenceExistingEntries(t *testing.T) {
	cfg := configWith(func(c *domain.Config) {
		c.CampusAccounts = []domain.CampusAccount{wiredAccount("c1")}
		c.Selection.ActiveCampusID = "c1"
		c.Selection.DefaultCampusID = "gone"
		c.Selection.ActiveHotspotID = "nope"
	})

	fields := fieldsOf(t, Validate(cfg))
	if slices.Contains(fields, "selection.active_campus_id") {
		t.Error("a pointer to an existing account was rejected")
	}
	for _, want := range []string{"selection.default_campus_id", "selection.active_hotspot_id"} {
		if !slices.Contains(fields, want) {
			t.Errorf("problems %v omit the dangling pointer %q", fields, want)
		}
	}
}

func TestDuplicateAccountIDsAreRejected(t *testing.T) {
	cfg := configWith(func(c *domain.Config) {
		c.CampusAccounts = []domain.CampusAccount{wiredAccount("c1"), wifiAccount("c1")}
	})
	requireField(t, Validate(cfg), "campus_accounts[1].id")
}
