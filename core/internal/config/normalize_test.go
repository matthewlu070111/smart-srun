package config

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The access mode is a discriminated union: a stored account must never carry
// an effective configuration for the mode it is not in. Otherwise flipping the
// mode back would silently resurrect stale credentials.
func TestNormalizeClearsTheUnusedHalfOfAnAccount(t *testing.T) {
	mixed := domain.CampusAccount{
		ID:          "c1",
		UserID:      "u",
		AccessMode:  domain.AccessModeWired,
		WiredIface:  "wan.v2",
		AuthEnabled: true,
		SSID:        "campus",
		Radio:       "radio0",
		Encryption:  "psk2",
		Key:         "leftover",
		APSelection: domain.APSelectionFixed,
		BSSID:       "aa:bb:cc:dd:ee:ff",
	}

	wired := NormalizeCampusAccount(mixed)
	if wired.SSID != "" || wired.Radio != "" || wired.Encryption != "" ||
		wired.Key != "" || wired.APSelection != "" || wired.BSSID != "" {
		t.Fatalf("wired account kept wireless fields: %+v", wired)
	}
	if wired.WiredIface != "wan.v2" || !wired.AuthEnabled {
		t.Fatalf("wired account lost its own fields: %+v", wired)
	}

	mixed.AccessMode = domain.AccessModeWiFi
	wifi := NormalizeCampusAccount(mixed)
	if wifi.WiredIface != "" || wifi.AuthEnabled {
		t.Fatalf("wireless account kept wired fields: %+v", wifi)
	}
	if wifi.SSID != "campus" || wifi.Key != "leftover" {
		t.Fatalf("wireless account lost its own fields: %+v", wifi)
	}
}

func TestWiredAccountDefaultsToWan(t *testing.T) {
	account := NormalizeCampusAccount(domain.CampusAccount{
		ID: "c1", AccessMode: domain.AccessModeWired, WiredIface: "  ",
	})
	if account.WiredIface != DefaultWiredIface {
		t.Fatalf("wired_iface = %q, want %q", account.WiredIface, DefaultWiredIface)
	}
}

// Same SSID, different encryption is a real distinction: an open network
// broadcasting the campus name must never satisfy a protected profile.
func TestEncryptionNormalization(t *testing.T) {
	open := []string{"", "none", "NONE", " open ", "nopass"}
	for _, value := range open {
		if got := NormalizeEncryption(value); got != EncryptionNone {
			t.Errorf("NormalizeEncryption(%q) = %q, want %q", value, got, EncryptionNone)
		}
		if KeyRequired(value) {
			t.Errorf("KeyRequired(%q) = true for an open network", value)
		}
	}
	for _, value := range []string{"psk2", "PSK2", " sae ", "wpa3"} {
		if got := NormalizeEncryption(value); got != strings.ToLower(strings.TrimSpace(value)) {
			t.Errorf("NormalizeEncryption(%q) = %q", value, got)
		}
		if !KeyRequired(value) {
			t.Errorf("KeyRequired(%q) = false for a protected network", value)
		}
	}
}

func TestAPSelectionInference(t *testing.T) {
	cases := []struct {
		name   string
		policy domain.APSelection
		bssid  string
		want   domain.APSelection
	}{
		{"stated policies survive", domain.APSelectionStrongest, "", domain.APSelectionStrongest},
		{"case is folded", "FIXED", "aa:bb:cc:dd:ee:ff", domain.APSelectionFixed},
		{"unset with a bssid means fixed", "", "aa:bb:cc:dd:ee:ff", domain.APSelectionFixed},
		{"unset without a bssid means auto", "", "", domain.APSelectionAuto},
		{"nonsense without a bssid means auto", "roam", "", domain.APSelectionAuto},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := normalizeAPSelection(testCase.policy, testCase.bssid)
			if got != testCase.want {
				t.Fatalf("= %q, want %q", got, testCase.want)
			}
		})
	}
}

// A stated fixed policy with an unusable BSSID is reported, not downgraded to
// auto: silently roaming away from a pinned AP is the surprise the user pinned
// it to avoid.
func TestFixedAPSelectionWithABadBSSIDIsRejectedNotDowngraded(t *testing.T) {
	for _, bssid := range []string{"", "not-a-mac", "00:00:00:00:00:00", "ab:cd:ef:12:34:56"} {
		account := wifiAccount("c1")
		account.APSelection = domain.APSelectionFixed
		account.BSSID = bssid
		cfg := configWith(func(c *domain.Config) {
			c.CampusAccounts = []domain.CampusAccount{account}
		})

		if cfg.CampusAccounts[0].APSelection != domain.APSelectionFixed {
			t.Fatalf("bssid %q: policy was downgraded to %q",
				bssid, cfg.CampusAccounts[0].APSelection)
		}
		requireField(t, Validate(cfg), "campus_accounts[0].bssid")
	}
}

func TestValidBSSID(t *testing.T) {
	valid := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55", "02:00:00:00:00:01"}
	for _, value := range valid {
		if !ValidBSSID(value) {
			t.Errorf("ValidBSSID(%q) = false", value)
		}
	}
	invalid := map[string]string{
		"":                    "empty",
		"aa:bb:cc:dd:ee":      "too short",
		"aa:bb:cc:dd:ee:ff:0": "too long",
		"AA:BB:CC:DD:EE:FF":   "uppercase, normalization folds case first",
		"aa-bb-cc-dd-ee-ff":   "wrong separator",
		"gg:bb:cc:dd:ee:ff":   "not hex",
		"00:00:00:00:00:00":   "the all-zero placeholder several drivers report",
		"ab:cd:ef:12:34:56":   "multicast bit set; a station cannot associate to it",
		"01:00:5e:00:00:01":   "multicast",
	}
	for value, why := range invalid {
		if ValidBSSID(value) {
			t.Errorf("ValidBSSID(%q) = true, but %s", value, why)
		}
	}
}

func TestProtectedNetworkNeedsAKey(t *testing.T) {
	account := wifiAccount("c1")
	account.Encryption = "psk2"
	account.Key = ""
	cfg := configWith(func(c *domain.Config) {
		c.CampusAccounts = []domain.CampusAccount{account}
	})
	requireField(t, Validate(cfg), "campus_accounts[0].key")

	account.Encryption = "none"
	cfg = configWith(func(c *domain.Config) {
		c.CampusAccounts = []domain.CampusAccount{account}
	})
	requireValid(t, cfg)
}

// Two managed accounts on one logical interface would authenticate over the
// same line and fight each other.
func TestManagedAccountsMayNotShareOneLine(t *testing.T) {
	first, second := wiredAccount("c1"), wiredAccount("c2")
	first.AuthEnabled, second.AuthEnabled = true, true
	second.WiredIface = "wan"

	cfg := configWith(func(c *domain.Config) {
		c.MultiWANEnabled = true
		c.CampusAccounts = []domain.CampusAccount{first, second}
	})
	requireField(t, Validate(cfg), "campus_accounts[1].wired_iface")

	// Different lines are fine.
	second.WiredIface = "wan.v2"
	requireValid(t, configWith(func(c *domain.Config) {
		c.MultiWANEnabled = true
		c.CampusAccounts = []domain.CampusAccount{first, second}
	}))

	// So is sharing a line when only one account is managed.
	second.WiredIface = "wan"
	second.AuthEnabled = false
	requireValid(t, configWith(func(c *domain.Config) {
		c.MultiWANEnabled = true
		c.CampusAccounts = []domain.CampusAccount{first, second}
	}))
}

// One account's overrides must not leak into another's resolution. This is what
// made 1.x multi-WAN views cross-contaminate.
func TestEffectiveLoginResolvesPerAccount(t *testing.T) {
	overridden := wiredAccount("a")
	overridden.Login = domain.LoginShape{N: "201", Enc: "srun_bx2"}
	plain := wiredAccount("b")

	cfg := configWith(func(c *domain.Config) {
		c.LoginDefaults = domain.LoginDefaults{N: "200", Type: "1", Enc: "srun_bx1"}
		c.CampusAccounts = []domain.CampusAccount{overridden, plain}
	})

	first := EffectiveLogin(cfg, cfg.CampusAccounts[0])
	if first.N != "201" || first.Enc != "srun_bx2" {
		t.Fatalf("account overrides were not applied: %+v", first)
	}
	if first.Type != "1" {
		t.Fatalf("type = %q, want the global default to fill the gap", first.Type)
	}

	second := EffectiveLogin(cfg, cfg.CampusAccounts[1])
	if second.N != "200" || second.Enc != "srun_bx1" {
		t.Fatalf("account b inherited account a's overrides: %+v", second)
	}

	// Built-ins fill what neither level supplies.
	if second.InfoPrefix != DefaultInfoPrefix || second.OS != DefaultLoginOS ||
		second.Name != DefaultLoginName || second.DoubleStack != DefaultDoubleStack {
		t.Fatalf("built-in login defaults were not applied: %+v", second)
	}
}

// An explicit false must survive distinguishably from "not set", so the editor
// can show which fields the account owns.
func TestExplicitDoubleStackFalseIsPreserved(t *testing.T) {
	value := false
	account := wiredAccount("a")
	account.Login.DoubleStack = &value
	cfg := configWith(func(c *domain.Config) {
		c.CampusAccounts = []domain.CampusAccount{account}
	})

	if cfg.CampusAccounts[0].Login.DoubleStack == nil {
		t.Fatal("an explicitly stored false became unset")
	}
	if EffectiveLogin(cfg, cfg.CampusAccounts[0]).DoubleStack {
		t.Fatal("explicit false resolved to true")
	}
}
