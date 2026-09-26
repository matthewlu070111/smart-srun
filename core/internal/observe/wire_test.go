package observe

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The names the page reads.
//
// This projection is half of status.get, and the interface matches on these
// exact keys. Publishing Go's own field names instead is a failure with no
// symptom on either side: the daemon answers, the page parses it, and every
// per-account field silently stays empty. It happened, on a device, and was
// only visible because the address the login had just used was not on the
// page. Renaming one of these means changing the page in the same commit.
func TestTheProjectionKeepsTheNamesTheInterfaceReads(t *testing.T) {
	view := AccountView{
		AccountID: "c1",
		Link:      domain.LinkReady, Auth: domain.AuthVerifiedSelf,
		Connectivity: domain.ConnectivityInternetReachable,
		Identity:     "2021001@telecom",
		Line:         LineView{Iface: "wan", Device: "eth0.2", Address: "10.0.0.77"},
		ObservedAt:   time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
		Revision:     7, Generation: 3, Sequence: 11,
		RunningAction: "a2",
		Note: &Note{ActionID: "a1", Kind: "manual_login", State: "failed",
			Message: "用户名或密码错误", Code: domain.CodeAuthRejected},
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := []string{"account_id", "link", "auth", "connectivity", "identity",
		"line", "observed_at", "config_revision", "generation", "sequence",
		"running_action", "note"}
	for _, name := range want {
		if _, present := fields[name]; !present {
			t.Errorf("status.get has no %q; it has %v", name, sortedKeys(fields))
		}
	}
	for name := range fields {
		if !slices.Contains(want, name) {
			t.Errorf("status.get gained an undeclared field %q", name)
		}
	}

	var line map[string]json.RawMessage
	if err := json.Unmarshal(fields["line"], &line); err != nil {
		t.Fatalf("decode line: %v", err)
	}
	for _, name := range []string{"iface", "device", "address"} {
		if _, present := line[name]; !present {
			t.Errorf("the observed line has no %q; it has %v", name, sortedKeys(line))
		}
	}

	var note map[string]json.RawMessage
	if err := json.Unmarshal(fields["note"], &note); err != nil {
		t.Fatalf("decode note: %v", err)
	}
	for _, name := range []string{"action_id", "kind", "state", "message", "code", "at"} {
		if _, present := note[name]; !present {
			t.Errorf("the note has no %q; it has %v", name, sortedKeys(note))
		}
	}
}

// An account nothing has been observed about publishes an empty line rather
// than dropping the member, so a reader can tell "no address" from "no such
// field" without special-casing either.
func TestAnUnobservedLineIsPresentAndEmpty(t *testing.T) {
	encoded, err := json.Marshal(AccountView{AccountID: "c1"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(fields["line"]) != "{}" {
		t.Errorf("line = %s, want an empty object", fields["line"])
	}
	if _, present := fields["note"]; present {
		t.Error("an account with nothing to report published a note")
	}
}

func sortedKeys(fields map[string]json.RawMessage) []string {
	out := make([]string, 0, len(fields))
	for name := range fields {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
