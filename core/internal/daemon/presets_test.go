//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

func readPresetList(t *testing.T, s *running, params any) PresetListResult {
	t.Helper()
	var result PresetListResult
	if err := json.Unmarshal(s.call("presets.list", params), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPresetListGroupsUsersAndHidesPublicDrafts(t *testing.T) {
	service := start(t, func(options *Options) {
		options.PublicPresets = func() ([]presets.School, error) {
			return []presets.School{
				{ShortName: "draft", Name: "Draft", Status: presets.StatusDraft},
				{ShortName: "public", Name: "Public", Status: presets.StatusActive},
			}, nil
		}
	})
	revision := uint64(0)
	service.call("user_presets.set", UserPresetsParams{ExpectedRevision: &revision,
		Document: json.RawMessage(usersRPCFixture)})
	before := string(service.call("config.get", nil))
	var result struct {
		Public []struct {
			ID string `json:"short_name"`
		} `json:"public"`
		User []struct {
			ID string `json:"short_name"`
		} `json:"user"`
		Operators []UserOperator `json:"operators"`
		Revision  uint64         `json:"revision"`
	}
	if err := json.Unmarshal(service.call("presets.list", nil), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Public) != 1 || result.Public[0].ID != "public" ||
		len(result.User) != 1 || result.User[0].ID != "custom-local" ||
		len(result.Operators) != 2 || result.Revision != 1 {
		t.Fatalf("groups = %+v", result)
	}
	if before != string(service.call("config.get", nil)) {
		t.Fatal("listing rewrote accounts")
	}
	service.callExpectingError("presets.list", map[string]any{"refresh": true})
}

func TestPresetRefreshRequiresAnExplicitLineAndKey(t *testing.T) {
	service := start(t, nil)
	for _, params := range []any{nil, map[string]any{"iface": "wan"},
		map[string]any{"idempotency_key": "missing-line"}} {
		if code := codeOf(t, service.callExpectingError("presets.refresh", params)); code != domain.CodeInvalidArgument {
			t.Fatalf("invalid submission = %s", code)
		}
	}
}

func TestPresetListLoadsDiskMergesBeforeFilteringAndSurvivesBadCache(t *testing.T) {
	base := t.TempDir()
	builtin, cachePath := filepath.Join(base, "builtin.json"), filepath.Join(base, "cache.json")
	raw := []byte(`{"schema_version":1,"schools":[{"short_name":"one","name":"One","status":"active"},{"short_name":"two","name":"Two","status":"active"}]}`)
	if err := os.WriteFile(builtin, raw, 0600); err != nil {
		t.Fatal(err)
	}
	warnings := make(chan error, 8)
	service := start(t, func(o *Options) {
		o.Paths.BuiltinPresets = builtin
		o.Paths.PresetsCache = cachePath
		o.OnError = func(err error) { warnings <- err }
	})
	if result := readPresetList(t, service, nil); len(result.Public) != 2 {
		t.Fatal(result)
	}
	cache := presets.NewCache(cachePath)
	override := []byte(`{"schema_version":1,"schools":[{"short_name":"one","name":"Withdrawn","status":"draft"}]}`)
	catalogue, _ := presets.Parse(override)
	if err := cache.Save(override, catalogue, "source", time.Now()); err != nil {
		t.Fatal(err)
	}
	if result := readPresetList(t, service, nil); len(result.Public) != 1 || result.Public[0].ShortName != "two" {
		t.Fatalf("filter before merge: %+v", result)
	}
	if result := readPresetList(t, service, map[string]any{"include_inactive": true}); len(result.Public) != 2 || result.Public[0].Name != "Withdrawn" {
		t.Fatal(result)
	}
	if err := os.WriteFile(cachePath, []byte("bad cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := readPresetList(t, service, nil); len(result.Public) != 2 {
		t.Fatal("lost builtin fallback")
	}
	select {
	case <-warnings:
	default:
		t.Fatal("cache corruption not diagnosed")
	}
	if err := os.Remove(builtin); err != nil {
		t.Fatal(err)
	}
	if code := codeOf(t, service.callExpectingError("presets.list", nil)); code != domain.CodeInternal {
		t.Fatal(code)
	}
}

func TestPresetListPaginationAndPrivateFields(t *testing.T) {
	service := start(t, func(o *Options) {
		o.PublicPresets = func() ([]presets.School, error) {
			return []presets.School{
				{ShortName: "one", Name: "One", Status: presets.StatusActive, Description: strings.Repeat("x", 600000)},
				{ShortName: "two", Name: "Two", Status: presets.StatusActive, Description: strings.Repeat("x", 600000)},
			}, nil
		}
	})
	rev := uint64(0)
	service.call("user_presets.set", UserPresetsParams{ExpectedRevision: &rev, Document: json.RawMessage(`{"schema_version":2,"revision":0,"presets":[{"short_name":"custom-a","name":"Local","password":"SYNTHETIC-SECRET","defaults":{"wired_iface":"wan2","access_mode":"wired"}}],"operators":[]}`)})
	first := readPresetList(t, service, nil)
	if len(first.Public) != 1 || first.NextOffset == nil || *first.NextOffset != 1 || first.Total != 3 {
		t.Fatalf("first page lengths %d next %v total %d", len(first.Public), first.NextOffset, first.Total)
	}
	secondRaw := service.call("presets.list", PresetListParams{Offset: *first.NextOffset})
	if strings.Contains(string(secondRaw), "SYNTHETIC-SECRET") {
		t.Fatal("private source field escaped into list")
	}
	var second PresetListResult
	if err := json.Unmarshal(secondRaw, &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Public) != 1 || second.Public[0].ShortName != "two" || len(second.User) != 1 || second.User[0].Defaults.WiredIface != "wan2" || second.NextOffset != nil {
		t.Fatal("missing second page or user interface")
	}
	limited := readPresetList(t, service, PresetListParams{Offset: 1, Limit: 1})
	if len(limited.Public) != 1 || len(limited.User) != 0 || limited.NextOffset == nil || *limited.NextOffset != 2 {
		t.Fatal("count limit")
	}
	if empty := readPresetList(t, service, PresetListParams{Offset: 99}); len(empty.Public) != 0 || len(empty.User) != 0 || empty.NextOffset != nil {
		t.Fatal("past end")
	}
	for _, params := range []PresetListParams{{Offset: -1}, {Limit: -1}, {Limit: 101}} {
		service.callExpectingError("presets.list", params)
	}
}
