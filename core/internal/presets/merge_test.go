package presets

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func schools(names ...string) []School {
	out := make([]School, 0, len(names))
	for _, name := range names {
		out = append(out, School{ShortName: name, Name: "from base " + name})
	}
	return out
}

// The value comes from the overlay and the position comes from the base.
//
// Half of that rule is easy to get and the other half is easy to miss. Taking
// the overlay's order too would reshuffle the list every time a refresh brought
// a school forward -- under the cursor of somebody scrolling it.
func TestMergeTakesValuesFromTheOverlayAndOrderFromTheBase(t *testing.T) {
	base := schools("alpha", "beta", "gamma")
	overlay := []School{
		{ShortName: "gamma", Name: "updated gamma"},
		{ShortName: "delta", Name: "new delta"},
		{ShortName: "alpha", Name: "updated alpha"},
	}

	merged := Merge(base, overlay)
	if len(merged) != 4 {
		t.Fatalf("merged = %+v", merged)
	}

	order := make([]string, 0, len(merged))
	for _, school := range merged {
		order = append(order, school.ShortName)
	}
	want := []string{"alpha", "beta", "gamma", "delta"}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order = %v, want %v -- the base fixes position", order, want)
		}
	}

	// Updated in place, not appended.
	if merged[0].Name != "updated alpha" {
		t.Errorf("alpha = %q, want the overlay's value", merged[0].Name)
	}
	if merged[2].Name != "updated gamma" {
		t.Errorf("gamma = %q, want the overlay's value", merged[2].Name)
	}
	// Untouched where the overlay says nothing.
	if merged[1].Name != "from base beta" {
		t.Errorf("beta = %q, want the base's value", merged[1].Name)
	}
	// Appended where it is new.
	if merged[3].Name != "new delta" {
		t.Errorf("delta = %q", merged[3].Name)
	}
}

// Neither side being there is not a special case.
func TestMergeHandlesAnEmptySide(t *testing.T) {
	base := schools("alpha")
	if got := Merge(base, nil); len(got) != 1 || got[0].ShortName != "alpha" {
		t.Errorf("Merge(base, nil) = %+v", got)
	}
	if got := Merge(nil, base); len(got) != 1 || got[0].ShortName != "alpha" {
		t.Errorf("Merge(nil, base) = %+v", got)
	}
	if got := Merge(nil, nil); len(got) != 0 {
		t.Errorf("Merge(nil, nil) = %+v", got)
	}
}

// A base that repeats a school does not gain two entries for it.
func TestMergeDoesNotDuplicateARepeatedBaseEntry(t *testing.T) {
	base := []School{
		{ShortName: "alpha", Name: "first"},
		{ShortName: "alpha", Name: "second"},
	}
	merged := Merge(base, []School{{ShortName: "alpha", Name: "overlay"}})
	if len(merged) != 1 {
		t.Fatalf("merged = %+v, want one entry", merged)
	}
	if merged[0].Name != "overlay" {
		t.Errorf("name = %q, want the overlay's", merged[0].Name)
	}
}

// Find reduces what it is given the same way the parser does.
func TestFindReducesTheIdentifierItIsGiven(t *testing.T) {
	list := schools("jxnu", "swpu")
	for _, wanted := range []string{"jxnu", "JXNU", "  jxnu  "} {
		if _, ok := Find(list, wanted); !ok {
			t.Errorf("Find(%q) found nothing", wanted)
		}
	}
	if _, ok := Find(list, "nowhere"); ok {
		t.Error("Find invented a school")
	}
}

// A body over the limit is refused, and one exactly at it is not.
//
// Spec 04: read limit+1 and check. Reading exactly the limit and stopping
// cannot tell a body that happened to be that size from one cut off at it, and
// answering "here is the catalogue" for a truncated document sends the caller
// on to the next source having learned the wrong thing about this one.
func TestReadLimitedRefusesABodyOverTheLimit(t *testing.T) {
	const limit = 16

	for name, testCase := range map[string]struct {
		body    string
		wantErr bool
	}{
		"well under":     {strings.Repeat("a", 4), false},
		"one under":      {strings.Repeat("a", limit-1), false},
		"exactly at it":  {strings.Repeat("a", limit), false},
		"one over":       {strings.Repeat("a", limit+1), true},
		"far over":       {strings.Repeat("a", limit*100), true},
		"nothing at all": {"", false},
	} {
		body, err := ReadLimited(strings.NewReader(testCase.body), limit)
		if testCase.wantErr {
			if err == nil {
				t.Errorf("%s: %d bytes were accepted under a limit of %d",
					name, len(testCase.body), limit)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(body) != testCase.body {
			t.Errorf("%s: got %d bytes, want %d", name, len(body), len(testCase.body))
		}
	}
}

// A reader that fails part way is a transport failure, not a bad document.
//
// The caller uses the difference: a source that broke mid-body is worth trying
// again, and one that served something unreadable is not.
func TestReadLimitedSeparatesAFailedReadFromABadDocument(t *testing.T) {
	_, err := ReadLimited(failingReader{}, MaxPayloadBytes)
	if code, _ := domain.CodeOf(err); code != domain.CodeTransportFailure {
		t.Errorf("code = %s, want TransportFailure", code)
	}

	_, err = ReadLimited(strings.NewReader("xx"), 1)
	if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid {
		t.Errorf("over-limit code = %s, want ProtocolInvalid", code)
	}

	if _, err := ReadLimited(strings.NewReader("x"), 0); err == nil {
		t.Error("a limit of zero was accepted")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("the connection went away")
}

var _ io.Reader = failingReader{}
