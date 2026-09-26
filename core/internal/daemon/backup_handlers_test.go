//go:build unix

package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestBackupJSONExportPreservesEmptyObjectsAndCredentials(t *testing.T) {
	service := start(t, nil)
	service.writeConfig("campus.upsert", `{"expected_revision":0,"account":{"user_id":"student","password":"synthetic-\"secret\\\u4e2d\u6587","wired_iface":"wan","login":{}}}`)
	before := service.call("config.get", nil)
	want := service.call("config.export", json.RawMessage(`{"include_secrets":true}`))
	for _, field := range []string{`"school_extra":{}`, `"login":{}`, `"hotspot_profiles":[]`} {
		if !bytes.Contains(want, []byte(field)) {
			t.Fatalf("fixture no longer exercises %s", field)
		}
	}
	raw := service.call("config.export", json.RawMessage(`{"include_secrets":true,"as_json":true}`))
	var transfer struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(raw, &transfer) != nil || transfer.Data != string(want) {
		t.Fatal("JSON transport changed the serialized backup")
	}
	decoded, _, err := config.ParseBackup([]byte(transfer.Data))
	if err != nil || decoded.CampusAccounts[0].Password != "synthetic-\"secret\\中文" {
		t.Fatal("serialized export is not restorable with exact credentials")
	}
	preview := service.call("config.import", BackupImportParams{Data: transfer.Data, CheckOnly: true})
	var result BackupImportResult
	if json.Unmarshal(preview, &result) != nil || !result.OK || result.CampusAccounts != 1 {
		t.Fatal("self-export preview failed")
	}
	if !bytes.Equal(before, service.call("config.get", nil)) {
		t.Fatal("export or preview modified configuration")
	}
	if codeOf(t, service.callExpectingError("config.export", json.RawMessage(`{"as_json":true}`))) != domain.CodeInvalidArgument {
		t.Fatal("JSON mode bypassed explicit credential consent")
	}
}

func TestBackupRPCPreviewCASAndCredentialIsolation(t *testing.T) {
	service := start(t, nil)
	data, err := os.ReadFile("../../../tests/fixtures/config-backup-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	params := BackupImportParams{Data: string(data), CheckOnly: true}
	raw := service.call("config.import", params)
	var preview BackupImportResult
	if json.Unmarshal(raw, &preview) != nil || preview.CampusAccounts != 2 || preview.ExpectedRevision != 0 {
		t.Fatal("wrong preview")
	}
	if strings.Contains(string(raw), "synthetic-secret") {
		t.Fatal("preview exposed secret")
	}
	before := service.call("config.get", nil)
	if strings.Contains(string(before), "example-user") {
		t.Fatal("preview wrote config")
	}
	params.CheckOnly = false
	if codeOf(t, service.callExpectingError("config.import", params)) != domain.CodeInvalidArgument {
		t.Fatal("missing CAS accepted")
	}
	params.ExpectedRevision = &preview.ExpectedRevision
	result := service.call("config.import", params)
	if strings.Contains(string(result), "synthetic-secret") {
		t.Fatal("import receipt exposed secret")
	}
	if codeOf(t, service.callExpectingError("config.import", params)) != domain.CodeConflict {
		t.Fatal("stale import accepted")
	}
	persisted, err := config.LoadFile(service.paths.ConfigFile())
	if err != nil || persisted.Enabled || persisted.Revision != 1 || len(persisted.CampusAccounts) != 2 {
		t.Fatal("wrong persisted import")
	}
	if codeOf(t, service.callExpectingError("config.export", json.RawMessage(`{}`))) != domain.CodeInvalidArgument {
		t.Fatal("implicit credential export accepted")
	}
	export := service.call("config.export", json.RawMessage(`{"include_secrets":true}`))
	if _, _, err := config.ParseBackup(export); err != nil || !strings.Contains(string(export), "synthetic-secret") {
		t.Fatal("explicit export not restorable")
	}
	for _, method := range []string{"config.get", "campus.get", "hotspot.get", "status.get"} {
		if strings.Contains(string(service.call(method, nil)), "synthetic-secret") {
			t.Fatalf("%s exposed secret", method)
		}
	}
}
