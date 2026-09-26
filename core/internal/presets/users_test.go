package presets

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const userExample = `{
  "schema_version": 2,
  "revision" : 0,
  "future": { "keep": [1, 2] },
  "presets": [
    {"short_name":"custom-local", "name":"本校", "status":"draft",
     "defaults":{"base_url":"https://portal.example","wired_iface":"wan.v2"},
     "operators":[{"suffix":"??","label":"待核实"}], "future_school": true}
  ],
  "operators": [
    {"suffix":"","label":"校园网"},
    {"suffix":"","label":"校园网"},
    {"suffix":"","label":"另一运营商"},
    {"suffix":"Realm.X","label":"运营商"}
  ]
}`

func TestUserPresetsRoundTripRetainsLayoutUnknownsAndBothArrays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "user-presets.json")
	store := NewUserStore(path)
	initial, err := store.Get()
	if err != nil || initial.Revision != 0 {
		t.Fatalf("initial: %v %+v", err, initial)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("reading an absent user store created a file")
	}
	saved, err := store.Set(t.Context(), 0, []byte(userExample), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(userExample, `"revision" : 0`, `"revision" : 1`, 1)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want || string(saved.Document()) != want {
		t.Fatalf("save rewrote fields or layout: err=%v\n%s", err, data)
	}
	// A fresh process sees the same committed revision and data.
	reloaded, err := NewUserStore(path).Get()
	if err != nil || reloaded.Revision != 1 || len(reloaded.Presets) != 1 {
		t.Fatalf("reload: %v %+v", err, reloaded)
	}
	preset := reloaded.Presets[0]
	if preset.School.Status != StatusDraft || preset.WiredIface != "wan.v2" || !preset.School.Operators[0].Unverified() {
		t.Fatalf("user preset lost environment or draft state: %+v", preset)
	}
	if len(reloaded.Operators) != 3 || reloaded.Operators[0].Label == reloaded.Operators[1].Label || reloaded.Operators[2].Suffix != "Realm.X" {
		t.Fatalf("operator dedup merged distinct labels or changed realm: %+v", reloaded.Operators)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != FileMode {
		t.Fatalf("file mode: %v %v", info, err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != DirMode {
		t.Fatalf("directory mode: %v %v", dir, err)
	}
}

func TestUserPresetsConcurrentWritersUseTheirOwnRevision(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Go(func() {
			<-start
			_, err := store.Set(t.Context(), 0, []byte(userExample), nil)
			errs <- err
		})
	}
	close(start)
	workers.Wait()
	close(errs)
	succeeded, conflicted := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if code, _ := domain.CodeOf(err); code == domain.CodeConflict {
			conflicted++
		} else {
			t.Fatalf("unexpected save error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestUserPresetsRejectInvalidOrCollidingDataWithoutWriting(t *testing.T) {
	for name, raw := range map[string]string{
		"legacy":        `{"schema_version":1,"revision":0,"presets":[],"operators":[]}`,
		"null":          `null`,
		"missing ops":   `{"schema_version":2,"revision":0,"presets":[]}`,
		"null ops":      `{"schema_version":2,"revision":0,"presets":[],"operators":null}`,
		"missing rev":   `{"schema_version":2,"presets":[],"operators":[]}`,
		"null rev":      strings.Replace(userExample, `"revision" : 0`, `"revision":null`, 1),
		"duplicate rev": strings.Replace(userExample, `"revision" : 0`, `"revision":0,"revision":1`, 1),
		"trailing":      userExample + `{}`,
		"public id":     strings.Replace(userExample, "custom-local", "jxnu", 1),
		"bad id":        strings.Replace(userExample, "custom-local", "custom-../x", 1),
		"empty name":    strings.Replace(userExample, "本校", " ", 1),
		"no suffix":     strings.Replace(userExample, `"suffix":"Realm.X",`, "", 1),
		"bad suffix":    strings.Replace(userExample, `"suffix":"Realm.X"`, `"suffix":false`, 1),
		"too large":     userExample + strings.Repeat(" ", MaxUserBytes),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "users.json")
			store := NewUserStore(path)
			if _, err := store.Set(t.Context(), 0, []byte(raw), nil); err == nil {
				t.Fatal("accepted invalid user presets")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid input wrote a file")
			}
		})
	}
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	if _, err := store.Set(t.Context(), 0, []byte(userExample), []School{{ShortName: "custom-local"}}); err == nil {
		t.Fatal("user preset shadowed a public school")
	}
}

func TestUserPresetFailedSavePreservesPreviousData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store := NewUserStore(path)
	saved, err := store.Set(t.Context(), 0, []byte(userExample), nil)
	if err != nil {
		t.Fatal(err)
	}
	before := saved.Document()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Set(ctx, 1, before, nil); err == nil {
		t.Fatal("cancelled save succeeded")
	}
	if _, err := store.Set(t.Context(), 0, []byte(userExample), nil); err == nil {
		t.Fatal("stale save succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed save changed the document")
	}
	// Corruption is never silently repaired with an empty replacement.
	if err := os.WriteFile(path, []byte("{broken"), FileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(t.Context(), 0, []byte(userExample), nil); err == nil {
		t.Fatal("corrupt existing data was overwritten")
	}
	after, _ = os.ReadFile(path)
	if string(after) != "{broken" {
		t.Fatal("corrupt user file was not preserved")
	}
}

func TestUserPresetEditDeleteAndRevisionOverflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store := NewUserStore(path)
	saved, err := store.Set(t.Context(), 0, []byte(userExample), nil)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(saved.Document(), []byte("本校"), []byte("新名称"), 1)
	saved, err = store.Set(t.Context(), 1, edited, nil)
	if err != nil || saved.Revision != 2 || saved.Presets[0].School.Name != "新名称" {
		t.Fatalf("edit: %v %+v", err, saved)
	}
	deleted := []byte(`{"schema_version":2,"revision":2,"presets":[],"operators":[]}`)
	saved, err = store.Set(t.Context(), 2, deleted, nil)
	if err != nil || saved.Revision != 3 || len(saved.Presets) != 0 {
		t.Fatalf("delete: %v %+v", err, saved)
	}
	overflow := bytes.Replace(deleted, []byte(`"revision":2`), []byte(`"revision":18446744073709551615`), 1)
	if err := os.WriteFile(path, overflow, FileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(t.Context(), ^uint64(0), overflow, nil); err == nil {
		t.Fatal("revision wrapped to zero")
	}
	if !json.Valid(saved.Document()) {
		t.Fatal("save produced invalid JSON")
	}
}

func TestUserPresetListLimitsAndDuplicateIdentifiers(t *testing.T) {
	for _, field := range []string{"presets", "operators"} {
		var document map[string]json.RawMessage
		if err := json.Unmarshal([]byte(userExample), &document); err != nil {
			t.Fatal(err)
		}
		entry := `{"suffix":"","label":"校园网"}`
		if field == "presets" {
			entry = `{"short_name":"custom-a","name":"A"}`
		}
		items := make([]string, MaxUserItems+1)
		for i := range items {
			items[i] = entry
		}
		document[field] = json.RawMessage("[" + strings.Join(items, ",") + "]")
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseUsers(raw); err == nil {
			t.Fatalf("over-limit %s accepted", field)
		}
	}
	duplicate := `{"schema_version":2,"revision":0,"presets":[
	 {"short_name":"custom-A","name":"A"},{"short_name":"custom-a","name":"B"}],"operators":[]}`
	if _, err := ParseUsers([]byte(duplicate)); err == nil {
		t.Fatal("IDs that collide after canonicalization were accepted")
	}
}

func FuzzParseUsers(f *testing.F) {
	f.Add([]byte(userExample))
	f.Add([]byte(emptyUsers))
	f.Add([]byte(`{"schema_version":2,"revision":null,"presets":[],"operators":[]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		document, err := ParseUsers(raw)
		if err != nil {
			return
		}
		if !bytes.Equal(document.Document(), raw) || !json.Valid(raw) {
			t.Fatal("accepted document changed its source text")
		}
		if _, err := ParseUsers(document.Document()); err != nil {
			t.Fatal("accepted document cannot be read again")
		}
	})
}
