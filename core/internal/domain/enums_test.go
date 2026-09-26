package domain

import "testing"

// D09 -- the frozen LuCI selector offers five levels. Dropping ALL would remove
// a visible control, so ALL stays and has a defined meaning: a threshold that
// emits everything, and never an event level of its own.
func TestLogLevelThresholds(t *testing.T) {
	if len(LogLevels()) != 5 {
		t.Fatalf("LogLevels() = %v; the frozen selector has five entries", LogLevels())
	}

	cases := []struct {
		threshold LogLevel
		event     LogLevel
		want      bool
	}{
		{LogAll, LogDebug, true},
		{LogAll, LogError, true},
		{LogDebug, LogDebug, true},
		{LogInfo, LogDebug, false},
		{LogInfo, LogInfo, true},
		{LogWarn, LogInfo, false},
		{LogWarn, LogWarn, true},
		{LogError, LogWarn, false},
		{LogError, LogError, true},
		// ALL is a threshold, not an event level: nothing is ever emitted "at
		// ALL", so no log line can claim that level.
		{LogAll, LogAll, false},
		{LogDebug, LogAll, false},
		// An unknown level emits nothing rather than everything: a typo in the
		// config must not silently turn the log to full debug.
		{LogInfo, LogLevel("LOUD"), false},
		{LogLevel("LOUD"), LogInfo, false},
	}

	for _, testCase := range cases {
		got := testCase.threshold.Emits(testCase.event)
		if got != testCase.want {
			t.Errorf("%s.Emits(%s) = %v, want %v",
				testCase.threshold, testCase.event, got, testCase.want)
		}
	}
}

func TestEnumValidity(t *testing.T) {
	for _, mode := range AccessModes() {
		if !mode.Valid() {
			t.Errorf("AccessModes() lists invalid %q", mode)
		}
	}
	for _, policy := range APSelections() {
		if !policy.Valid() {
			t.Errorf("APSelections() lists invalid %q", policy)
		}
	}
	for _, mode := range CheckModes() {
		if !mode.Valid() {
			t.Errorf("CheckModes() lists invalid %q", mode)
		}
	}
	for _, level := range LogLevels() {
		if !level.Valid() {
			t.Errorf("LogLevels() lists invalid %q", level)
		}
	}

	// Case matters: normalization folds case before validation, so a raw
	// mixed-case value reaching Valid() means normalization was skipped.
	for _, bad := range []AccessMode{"", "Wired", "WIFI", "wireless", "lan"} {
		if bad.Valid() {
			t.Errorf("AccessMode(%q) reported valid", bad)
		}
	}
	for _, bad := range []CheckMode{"", "Internet", "online", "any"} {
		if bad.Valid() {
			t.Errorf("CheckMode(%q) reported valid", bad)
		}
	}
	for _, bad := range []APSelection{"", "Fixed", "best", "roam"} {
		if bad.Valid() {
			t.Errorf("APSelection(%q) reported valid", bad)
		}
	}
}

func TestManagedWiredAccountsNeedBothGates(t *testing.T) {
	wired := func(id string, optedIn bool) CampusAccount {
		return CampusAccount{ID: id, AccessMode: AccessModeWired, AuthEnabled: optedIn}
	}
	cfg := Config{
		CampusAccounts: []CampusAccount{
			wired("a", true),
			wired("b", false),
			{ID: "c", AccessMode: AccessModeWiFi, AuthEnabled: true},
		},
	}

	if got := cfg.ManagedWiredAccounts(); len(got) != 0 {
		t.Fatalf("managed = %v with multi-WAN off; the per-account opt-in "+
			"must not take effect on its own", got)
	}

	cfg.MultiWANEnabled = true
	managed := cfg.ManagedWiredAccounts()
	if len(managed) != 1 || managed[0].ID != "a" {
		t.Fatalf("managed = %v, want only the opted-in wired account", managed)
	}
}
