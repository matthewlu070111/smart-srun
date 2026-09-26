//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

const usersRPCFixture = `{"schema_version":2,"revision":0,"presets":[
 {"short_name":"custom-local","name":"本校","future":{"keep":true}}],
 "operators":[{"suffix":"","label":"甲"},{"suffix":"","label":"甲"},{"suffix":"","label":"乙"}]}`

func TestUserPresetsRPCPersistsAndRefusesStaleSave(t *testing.T) {
	var publicReads atomic.Int32
	service := start(t, func(options *Options) {
		options.PublicPresets = func() ([]presets.School, error) {
			publicReads.Add(1)
			return []presets.School{{ShortName: "public-school"}}, nil
		}
	})
	beforeConfig := service.call("config.get", nil)
	var initial UserPresetsResult
	if err := json.Unmarshal(service.call("user_presets.get", nil), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Revision != 0 || publicReads.Load() != 0 {
		t.Fatal("user preset read unexpectedly loaded a public catalogue")
	}
	revision := uint64(0)
	params := UserPresetsParams{ExpectedRevision: &revision, Document: json.RawMessage(usersRPCFixture)}
	var saved UserPresetsResult
	if err := json.Unmarshal(service.call("user_presets.set", params), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || len(saved.Operators) != 2 || saved.Operators[0].Label == saved.Operators[1].Label {
		t.Fatalf("saved result = %+v", saved)
	}
	if code := codeOf(t, service.callExpectingError("user_presets.set", params)); code != domain.CodeConflict {
		t.Fatalf("stale save: %s", code)
	}
	if string(service.call("config.get", nil)) != string(beforeConfig) {
		t.Fatal("user preset save changed account configuration or its revision")
	}
	data, err := os.ReadFile(service.paths.UserPresets())
	if err != nil {
		t.Fatal(err)
	}
	document, err := presets.ParseUsers(data)
	if err != nil || document.Revision != 1 {
		t.Fatalf("disk document: %v %+v", err, document)
	}
	service.call("status.get", nil)
	if publicReads.Load() != 2 {
		t.Fatal("status poll loaded presets")
	}
}

func TestUserPresetsRPCValidatesPublicCollisionsAndRequiredRevision(t *testing.T) {
	service := start(t, func(options *Options) {
		options.PublicPresets = func() ([]presets.School, error) {
			return []presets.School{{ShortName: "custom-local", Status: presets.StatusDraft}}, nil
		}
	})
	service.callExpectingError("user_presets.set", nil)
	service.callExpectingError("user_presets.set", map[string]any{"document": json.RawMessage(usersRPCFixture)})
	service.callExpectingError("user_presets.get", map[string]any{"unexpected": true})
	revision := uint64(0)
	service.callExpectingError("user_presets.set", UserPresetsParams{
		ExpectedRevision: &revision, Document: json.RawMessage(usersRPCFixture)})
	if _, err := os.Stat(service.paths.UserPresets()); !os.IsNotExist(err) {
		t.Fatal("invalid or colliding input wrote a file")
	}
}
