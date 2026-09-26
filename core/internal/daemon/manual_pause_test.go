//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestManualLogoutIntentSurvivesFailureAndServiceRestartUntilLogin(t *testing.T) {
	r := start(t, func(o *Options) { o.Runner = switchRunner{} })
	r.writeConfig("campus.upsert", `{"expected_revision":0,"account":{"user_id":"student","password":"private-password","wired_iface":"wan","auth_enabled":true}}`)
	r.writeConfig("campus.upsert", `{"expected_revision":1,"account":{"user_id":"second","wired_iface":"wan2","auth_enabled":true}}`)
	r.writeConfig("config.apply", `{"expected_revision":2,"settings":{"enabled":true,"multi_wan_enabled":true,"quiet":{"enabled":false}}}`)
	var logout SubmitResult
	if err := json.Unmarshal(r.call("action.submit", SubmitParams{Kind: "manual_logout", AccountID: "c1", IdempotencyKey: "reject"}), &logout); err != nil {
		t.Fatal(err)
	}
	if action := r.awaitTerminal(logout.ActionID); action.State != application.StateFailed {
		t.Fatal("fixture logout did not fail")
	} else if !strings.Contains(action.Message, "自动认证已暂停") || strings.Contains(action.Message, "已登出") {
		t.Fatalf("failed logout must explain the retained pause without claiming logout success: %q", action.Message)
	}
	if got := r.status().ManualPausedAccounts; !slices.Equal(got, []string{"c1"}) {
		t.Fatalf("failed logout lost user intent: %v", got)
	}
	r.stop()
	if err := r.wait(); err != nil {
		t.Fatal(err)
	}
	restarted := start(t, func(o *Options) { o.Paths, o.Runner = r.paths, switchRunner{} })
	restarted.paths, restarted.client = r.paths, control.Client{Path: r.paths.Socket()}
	if got := restarted.status().ManualPausedAccounts; !slices.Equal(got, []string{"c1"}) {
		t.Fatalf("restart lost pause: %v", got)
	}
	deadline := time.After(patience)
	for waiting := true; waiting; {
		select {
		case action := <-restarted.actions:
			if action.Request.AccountID == "c1" {
				t.Fatal("restart submitted authentication for paused account")
			}
			if action.Request.AccountID == "c2" && action.State.Terminal() {
				waiting = false
			}
		case <-deadline:
			t.Fatal("pause blocked the other WAN")
		}
	}
	var login SubmitResult
	if err := json.Unmarshal(restarted.call("action.submit", SubmitParams{Kind: "manual_login", AccountID: "c1", IdempotencyKey: "resume"}), &login); err != nil {
		t.Fatal(err)
	}
	if action := restarted.awaitTerminal(login.ActionID); action.State != application.StateSucceeded {
		t.Fatal("manual login failed")
	}
	if got := restarted.status().ManualPausedAccounts; len(got) != 0 {
		t.Fatal("login did not clear pause")
	}
	cfg, err := config.LoadFile(r.paths.ConfigFile())
	if err != nil || cfg.Revision != 3 {
		t.Fatal("manual intent changed persistent config")
	}
	retained, err := loadManualPauses(r.paths, cfg)
	if err != nil || retained.paused(cfg, "c1") {
		t.Fatal("cleared pause would return on restart")
	}
}

func TestManualLogoutRefusesBeforeIOWhenIntentCannotBeRetained(t *testing.T) {
	r := setupSwitchRPC(t)
	if err := os.Mkdir(r.paths.ManualPauses(), 0o700); err != nil {
		t.Fatal(err)
	}
	err := r.callExpectingError("action.submit", SubmitParams{Kind: "manual_logout", AccountID: "c1", IdempotencyKey: "bad-record"})
	if codeOf(t, err) != domain.CodeInternal || len(r.status().Actions) != 0 {
		t.Fatal("logout was admitted without retaining its intent")
	}
}

func TestOtherWANLogoutRetainsTheScheduledHotspotReturn(t *testing.T) {
	r := setupSwitchRPC(t)
	want := application.QuietResume{Revision: 4, AccountID: "c1", HotspotID: "h1", Occurrence: "scheduled", StartedAt: time.Now()}
	if err := writeQuietResume(r.paths, want); err != nil {
		t.Fatal(err)
	}
	var receipt SubmitResult
	if err := json.Unmarshal(r.call("action.submit", SubmitParams{Kind: "manual_logout", AccountID: "c2", IdempotencyKey: "other-wan"}), &receipt); err != nil {
		t.Fatal(err)
	}
	if action := r.awaitTerminal(receipt.ActionID); action.State != application.StateSucceeded {
		t.Fatal("other WAN logout failed")
	} else if strings.Count(action.Message, "自动认证已暂停") != 1 {
		t.Fatalf("successful logout must explain its pause once: %q", action.Message)
	}
	got, err := readQuietResume(r.paths)
	if err != nil || got == nil || got.AccountID != want.AccountID || got.HotspotID != want.HotspotID {
		t.Fatal("unrelated WAN logout lost scheduled return")
	}
}

func TestDamagedManualPauseRecordFailsClosedWithoutTreatingItAsConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Revision = 4
	cfg.CampusAccounts = []domain.CampusAccount{{ID: "c1"}, {ID: "c2"}}
	for _, bad := range []string{`{`, `{"schema_version":2}`, `{"schema_version":1,"revision":4,"accounts":["missing"]}`, `{"schema_version":1,"revision":4,"accounts":["c1","c1"]}`} {
		paths := Paths{Runtime: t.TempDir()}
		if err := os.WriteFile(paths.ManualPauses(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := loadManualPauses(paths, cfg)
		if err == nil || !p.paused(cfg, "c1") || !p.paused(cfg, "c2") {
			t.Fatal("invalid record permitted automatic login")
		}
		// Restart still retains the safe pause, but the corrupt bytes no longer
		// turn a later deliberate settings change back into a new pause.
		p, err = loadManualPauses(paths, cfg)
		if err != nil || !p.paused(cfg, "c1") || !p.paused(cfg, "c2") {
			t.Fatal("conservative recovery was not retained")
		}
		if err := p.set(cfg, "c1", false); err != nil {
			t.Fatal(err)
		}
		p, err = loadManualPauses(paths, cfg)
		if err != nil || p.paused(cfg, "c1") || !p.paused(cfg, "c2") {
			t.Fatal("manual recovery changed another account")
		}
		cfg.Revision++
		p, err = loadManualPauses(paths, cfg)
		if err != nil || p.paused(cfg, "c2") {
			t.Fatal("restart after new settings restored an obsolete pause")
		}
		cfg.Revision--
	}
}
