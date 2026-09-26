//go:build unix

package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/strategy"
)

func (r *running) writeConfig(method, params string) ConfigWriteResult {
	r.t.Helper()
	var result ConfigWriteResult
	raw := r.call(method, json.RawMessage(params))
	if err := json.Unmarshal(raw, &result); err != nil {
		r.t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-password") || strings.Contains(string(raw), "private-wifi-key") {
		r.t.Fatal("write response disclosed a credential")
	}
	return result
}

func TestConfigurationRPCPersistsAccountsSettingsAndSelection(t *testing.T) {
	service := start(t, nil)
	first := service.writeConfig("campus.upsert", `{"expected_revision":0,"account":{"user_id":"student","password":"private-password","wired_iface":"wan","login":{"double_stack":false}}}`)
	if first.ID != "c1" || first.Revision != 1 || first.Config.Selection.ActiveCampusID != first.ID {
		t.Fatalf("creation receipt = %+v", first)
	}
	service.writeConfig("campus.upsert", `{"expected_revision":1,"account":{"id":"c1","label":"Renamed"}}`)
	// The empty edit still identifies the affected account.
	noop := service.writeConfig("campus.upsert", `{"expected_revision":2,"account":{"id":"c1"}}`)
	if noop.ID != "c1" {
		t.Fatal("edit lost its account ID")
	}
	detail := service.call("campus.get", DetailParams{ID: "c1", IncludeSecrets: true})
	var got CampusResult
	if err := json.Unmarshal(detail, &got); err != nil {
		t.Fatal(err)
	}
	if got.Accounts[0].Password != "private-password" || got.Accounts[0].Login.DoubleStack == nil || *got.Accounts[0].Login.DoubleStack {
		t.Fatal("omission changed a secret or explicit false")
	}
	service.writeConfig("campus.upsert", `{"expected_revision":3,"account":{"id":"c1","password":"","login":{"double_stack":null}}}`)
	service.writeConfig("campus.upsert", `{"expected_revision":4,"account":{"user_id":"other","wired_iface":"wan2"}}`)
	service.writeConfig("campus.set_default", `{"expected_revision":5,"id":"c2"}`)
	removed := service.writeConfig("campus.remove", `{"expected_revision":6,"id":"c2"}`)
	if removed.Config.Selection.ActiveCampusID != "c1" || removed.Config.Selection.DefaultCampusID != "c1" {
		t.Fatal("deleting the selected account left dangling pointers")
	}
	service.writeConfig("hotspot.upsert", `{"expected_revision":7,"profile":{"ssid":"fallback","encryption":"psk2","key":"private-wifi-key"}}`)
	service.writeConfig("hotspot.upsert", `{"expected_revision":8,"profile":{"id":"h1","label":"Fallback"}}`)
	service.writeConfig("hotspot.upsert", `{"expected_revision":9,"profile":{"ssid":"open","encryption":"none"}}`)
	service.writeConfig("hotspot.set_default", `{"expected_revision":10,"id":"h2"}`)
	service.writeConfig("hotspot.remove", `{"expected_revision":11,"id":"h2"}`)
	last := service.writeConfig("config.apply", `{"expected_revision":12,"settings":{"checks":{"interval_seconds":90},"log":{"level":"debug"}}}`)
	if last.Config.Checks.IntervalSeconds != 90 || last.Config.Checks.TerminalAttempts != config.Defaults().Checks.TerminalAttempts || last.Config.Selection.ActiveHotspotID != "h1" {
		t.Fatal("partial settings save overwrote unrelated state")
	}
	for _, method := range []string{"config.get", "campus.get", "hotspot.get", "status.get"} {
		if strings.Contains(string(service.call(method, nil)), "private-") {
			t.Fatalf("%s disclosed a secret", method)
		}
	}
	service.stop()
	if err := service.wait(); err != nil {
		t.Fatal(err)
	}
	reopened, err := config.Open(service.paths.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	persisted := reopened.Snapshot()
	if persisted.Revision != 13 || persisted.CampusAccounts[0].Password != "" || persisted.CampusAccounts[0].Login.DoubleStack != nil || persisted.HotspotProfiles[0].Key != "private-wifi-key" {
		t.Fatal("restart read differs from the completed transactions")
	}
	info, err := os.Stat(service.paths.ConfigFile())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions: %v / %v", info, err)
	}
}

func TestConfigurationRPCRejectsAmbiguousOrStaleWritesWithoutChangingDisk(t *testing.T) {
	service := start(t, nil)
	service.writeConfig("campus.upsert", `{"expected_revision":0,"account":{"user_id":"student","wired_iface":"wan","password":"private-password"}}`)
	before, err := os.ReadFile(service.paths.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ method, params string }{
		{"config.apply", `{"settings":{"enabled":true}}`},
		{"config.apply", `{"expected_revision":1,"settings":null}`},
		{"config.apply", `{"expected_revision":1,"settings":{"campus_accounts":[]}}`},
		{"config.apply", `{"expected_revision":1,"settings":{"enabled":true,"enabled":false}}`},
		{"config.apply", `{"expected_revision":1,"settings":{"Enabled":true}}`},
		{"campus.upsert", `{"expected_revision":1,"account":{"id":"c1","password":null}}`},
		{"campus.upsert", `{"expected_revision":1,"account":{"id":"c1","password":false}}`},
		{"campus.upsert", `{"expected_revision":1,"account":{"id":"c1","password":"private-password","Password":"wrong"}}`},
		{"campus.upsert", `{"expected_revision":1,"account":{"id":"c1","login":{"double_stack":"false"}}}`},
		{"campus.upsert", `{"expected_revision":1,"expected_revision":0,"account":{"id":"c1"}}`},
		{"campus.remove", `{"expected_revision":1}`},
		{"hotspot.upsert", `{"expected_revision":1}`},
		{"campus.get", `{"include_secrets":true}`},
		{"hotspot.get", `{"include_secrets":true}`},
	}
	for _, tc := range cases {
		err := service.callExpectingError(tc.method, json.RawMessage(tc.params))
		if codeOf(t, err) != domain.CodeInvalidArgument || strings.Contains(err.Error(), "private-password") {
			t.Errorf("%s returned %v", tc.method, err)
		}
	}
	err = service.callExpectingError("config.apply", json.RawMessage(`{"expected_revision":0,"settings":{"enabled":true}}`))
	if codeOf(t, err) != domain.CodeConflict {
		t.Fatalf("stale write: %v", err)
	}
	after, err := os.ReadFile(service.paths.ConfigFile())
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected writes changed persisted state")
	}
}

func TestConcurrentConfigurationSavesHaveExactlyOneWinner(t *testing.T) {
	service := start(t, nil)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for _, interval := range []int{70, 80} {
		group.Go(func() {
			_, err := service.client.Call(t.Context(), "config.apply", json.RawMessage(fmt.Sprintf(`{"expected_revision":0,"settings":{"checks":{"interval_seconds":%d}}}`, interval)))
			errors <- err
		})
	}
	group.Wait()
	close(errors)
	success, conflict := 0, 0
	for err := range errors {
		if err == nil {
			success++
		} else if codeOf(t, err) == domain.CodeConflict {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || service.status().ConfigRevision != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

// A settings save that sends a school_extra key the selected strategy does not
// declare keeps the rest, drops that key, and says so.
//
// Spec 03 asks for the drop "with a diagnostic". Filtering alone satisfied the
// first half and left a caller to discover the missing key later; the warning
// is the second half. A strategy switch clears the map by design and is not the
// caller's mistake, so it is not reported.
func TestSettingsSaveReportsUndeclaredSchoolExtraKeys(t *testing.T) {
	registry := strategy.NewRegistry()
	registry.MustRegister(strategy.Default())
	registry.MustRegister(strategy.Strategy{ID: "zoned", Label: "分区",
		Fields: []strategy.Field{{Key: "zone", Label: "区", Kind: strategy.FieldString}}})
	config.UseSchoolRegistry(registry, t.Cleanup)

	service := start(t, nil)
	service.writeConfig("config.apply", `{"expected_revision":0,"settings":{"school":"zoned"}}`)

	saved := service.writeConfig("config.apply",
		`{"expected_revision":1,"settings":{"school_extra":{"zone":"north","smuggled":"x"}}}`)
	if saved.Config.SchoolExtra["zone"] != "north" {
		t.Fatalf("the declared key was not kept: %+v", saved.Config.SchoolExtra)
	}
	if _, present := saved.Config.SchoolExtra["smuggled"]; present {
		t.Fatalf("an undeclared key was stored: %+v", saved.Config.SchoolExtra)
	}
	if len(saved.Warnings) != 1 || !strings.Contains(saved.Warnings[0], "school_extra.smuggled") {
		t.Fatalf("warnings = %q, want one naming school_extra.smuggled", saved.Warnings)
	}

	clean := service.writeConfig("config.apply",
		`{"expected_revision":2,"settings":{"school_extra":{"zone":"south"}}}`)
	if len(clean.Warnings) != 0 {
		t.Errorf("a fully declared save reported warnings: %q", clean.Warnings)
	}

	switched := service.writeConfig("config.apply",
		`{"expected_revision":3,"settings":{"school":"default","school_extra":{"zone":"x"}}}`)
	if len(switched.Warnings) != 0 {
		t.Errorf("a strategy switch was reported as the caller's mistake: %q", switched.Warnings)
	}
	if len(switched.Config.SchoolExtra) != 0 {
		t.Errorf("school_extra survived a switch: %+v", switched.Config.SchoolExtra)
	}
}
