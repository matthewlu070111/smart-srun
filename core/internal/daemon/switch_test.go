//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type switchRunner struct{}

func (switchRunner) Run(_ context.Context, a application.Action, _ func(application.Phase)) application.Outcome {
	if a.Request.IdempotencyKey == "reject" {
		return application.Outcome{State: application.StateFailed, Code: domain.CodeAuthRejected, Message: "rejected"}
	}
	return application.Outcome{State: application.StateSucceeded, Message: "connected"}
}

func setupSwitchRPC(t *testing.T, configure ...func(*Options)) *running {
	r := start(t, func(o *Options) {
		o.Runner = switchRunner{}
		for _, apply := range configure {
			apply(o)
		}
	})
	for i := 0; i < 2; i++ {
		r.writeConfig("campus.upsert", fmt.Sprintf(`{"expected_revision":%d,"account":{"user_id":"student%d","wired_iface":"wan"}}`, i, i))
	}
	for i := 0; i < 2; i++ {
		r.writeConfig("hotspot.upsert", fmt.Sprintf(`{"expected_revision":%d,"profile":{"ssid":"hotspot%d","radio":"radio1","encryption":"none"}}`, 2+i, i))
	}
	return r
}

func TestSwitchRPCPersistsSelectionWithoutChangingDefaultsOrEnabled(t *testing.T) {
	r := setupSwitchRPC(t)
	var revision uint64 = 4
	for _, target := range []struct{ kind, account, hotspot string }{{"switch_campus", "c2", ""}, {"switch_hotspot", "", "h2"}} {
		params := SubmitParams{Kind: target.kind, AccountID: target.account, HotspotID: target.hotspot,
			ExpectedRevision: &revision, IdempotencyKey: target.kind}
		var receipt SubmitResult
		if err := json.Unmarshal(r.call("action.submit", params), &receipt); err != nil {
			t.Fatal(err)
		}
		if got := r.awaitTerminal(receipt.ActionID); got.State != application.StateSucceeded {
			t.Fatalf("switch failed: %+v", got)
		}
		var retry SubmitResult
		if err := json.Unmarshal(r.call("action.submit", params), &retry); err != nil {
			t.Fatal(err)
		}
		if !retry.Duplicate || retry.ActionID != receipt.ActionID {
			t.Fatal("retry started another switch after commit")
		}
		revision++
	}
	loaded, err := config.LoadFile(r.paths.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 6 || loaded.Enabled || loaded.Selection != (domain.Selection{ActiveCampusID: "c2", DefaultCampusID: "c1", ActiveHotspotID: "h2", DefaultHotspotID: "h1"}) {
		t.Fatalf("wrong durable choice: %+v", loaded.Selection)
	}
	for _, key := range []string{"same-target", "reject"} {
		var receipt SubmitResult
		json.Unmarshal(r.call("action.submit", SubmitParams{Kind: "switch_campus", AccountID: "c2", ExpectedRevision: &revision, IdempotencyKey: key}), &receipt)
		got := r.awaitTerminal(receipt.ActionID)
		if key == "reject" && got.State != application.StateFailed {
			t.Fatal("failure was changed to success")
		}
	}
	loaded, err = config.LoadFile(r.paths.ConfigFile())
	if err != nil || loaded.Revision != 6 {
		t.Fatal("same target or failed action wrote config", err)
	}
	// A new process opens precisely the saved selection, with no replay needed.
	reopened, err := config.Open(r.paths.ConfigFile())
	if err != nil || reopened.Snapshot().Selection != loaded.Selection {
		t.Fatal("selection did not survive reopen", err)
	}
}

func TestSwitchRPCReportsRecoveryWhenSelectionCannotBeSaved(t *testing.T) {
	faults := make(chan error, 1)
	worker := blockingRunner{entered: make(chan struct{}, 1), release: make(chan struct{})}
	r := setupSwitchRPC(t, func(o *Options) {
		o.OnError = func(err error) { faults <- err }
		o.Runner = worker
	})
	var receipt SubmitResult
	json.Unmarshal(r.call("action.submit", SubmitParams{Kind: "switch_campus", AccountID: "c2", IdempotencyKey: "save-failure"}), &receipt)
	<-worker.entered
	// Fail persistence after dispatch. An already unreadable recovery parent
	// is now correctly refused by the updater guard before a switch begins.
	backup := r.paths.Config + "-saved"
	if err := os.Rename(r.paths.Config, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.paths.Config, []byte("block config directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(worker.release)
	got := r.awaitTerminal(receipt.ActionID)
	if got.State != application.StateFailed || got.Code != domain.CodeRecoveryRequired {
		t.Fatalf("save failure reported success: %+v", got)
	}
	if err := <-faults; err == nil {
		t.Fatal("write failure was not reported")
	}
	var cfg domain.Config
	json.Unmarshal(r.call("config.get", nil), &cfg)
	if cfg.Selection.ActiveCampusID != "c1" || cfg.Revision != 4 {
		t.Fatal("failed save published a new selection")
	}
}
