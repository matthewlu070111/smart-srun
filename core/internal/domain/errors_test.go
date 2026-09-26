package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorRendering(t *testing.T) {
	plain := Errorf(CodeBusy, "队列已满")
	if got := plain.Error(); got != "Busy: 队列已满" {
		t.Errorf("Error() = %q", got)
	}

	attributed := FieldErrorf(CodeInvalidConfig, "retry.max_seconds", "不能小于 %d", 10)
	if got := attributed.Error(); got != "InvalidConfig: retry.max_seconds: 不能小于 10" {
		t.Errorf("Error() = %q", got)
	}
}

// The wrapped cause stays internal: it may hold detail the user-facing message
// must not. Callers still reach it with errors.Is/As for diagnosis.
func TestWrappedCauseIsReachableButNotRendered(t *testing.T) {
	cause := errors.New("dial 10.0.0.2:80: connection refused")
	err := Errorf(CodeTransportFailure, "无法连接认证服务器").Wrap(cause)

	if strings.Contains(err.Error(), "10.0.0.2") {
		t.Fatalf("message %q leaked the internal cause", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause is unreachable, so nothing can diagnose it")
	}
}

func TestCodeOfTraversesWrapping(t *testing.T) {
	err := Errorf(CodeNotFound, "找不到账号")
	if code, ok := CodeOf(err); !ok || code != CodeNotFound {
		t.Fatalf("CodeOf = (%q, %v)", code, ok)
	}

	wrapped := errors.Join(errors.New("context"), err)
	if code, ok := CodeOf(wrapped); !ok || code != CodeNotFound {
		t.Fatalf("CodeOf through join = (%q, %v)", code, ok)
	}

	// Errors from elsewhere are not guessed at.
	if _, ok := CodeOf(errors.New("plain")); ok {
		t.Fatal("a foreign error was given a code")
	}
	if _, ok := CodeOf(nil); ok {
		t.Fatal("nil was given a code")
	}
}

func TestErrorsSet(t *testing.T) {
	set := &Errors{Code: CodeInvalidConfig}
	if set.Err() != nil {
		t.Fatal("an empty set must report no error, so callers can return it directly")
	}

	set.Addf("a", "坏了")
	set.Addf("b", "也坏了")
	set.Add(&Error{Message: "没有字段"})

	if set.Err() == nil {
		t.Fatal("a non-empty set must report an error")
	}
	if got := set.Fields(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Fields() = %v, want the two attributable paths in order", got)
	}
	// An item added without a code inherits the set's, so every problem an RPC
	// envelope reports has a stable code.
	if set.Items[2].Code != CodeInvalidConfig {
		t.Fatalf("item code = %q, want the set's code", set.Items[2].Code)
	}
	if !strings.Contains(set.Error(), "a: 坏了") {
		t.Fatalf("Error() = %q, want every problem included", set.Error())
	}

	var nilSet *Errors
	if nilSet.Err() != nil {
		t.Fatal("a nil set must report no error")
	}
}

func TestConfigLookupHelpers(t *testing.T) {
	cfg := Config{
		CampusAccounts:  []CampusAccount{{ID: "c1"}, {ID: "c2"}},
		HotspotProfiles: []HotspotProfile{{ID: "h1"}},
	}

	if account, ok := cfg.CampusAccountByID("c2"); !ok || account.ID != "c2" {
		t.Errorf("CampusAccountByID(c2) = (%+v, %v)", account, ok)
	}
	if _, ok := cfg.CampusAccountByID("gone"); ok {
		t.Error("a missing account was reported found")
	}
	if hotspot, ok := cfg.HotspotByID("h1"); !ok || hotspot.ID != "h1" {
		t.Errorf("HotspotByID(h1) = (%+v, %v)", hotspot, ok)
	}
	if _, ok := cfg.HotspotByID(""); ok {
		t.Error("an empty id matched a hotspot")
	}

	// The returned value is a copy: mutating it must not edit the config.
	account, _ := cfg.CampusAccountByID("c1")
	account.UserID = "changed"
	if cfg.CampusAccounts[0].UserID != "" {
		t.Fatal("the lookup handed out a reference into the config")
	}
}

func TestQuietConfigWindow(t *testing.T) {
	start, _ := NewClockTime(23, 0)
	end, _ := NewClockTime(6, 0)
	quiet := QuietConfig{Enabled: true, Start: start, End: end}

	window := quiet.Window()
	if window.Start != start || window.End != end {
		t.Fatalf("Window() = %+v", window)
	}
	if !window.WrapsMidnight() {
		t.Fatal("23:00-06:00 must be reported as wrapping midnight")
	}
}
