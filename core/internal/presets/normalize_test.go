package presets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "presets", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// The catalogue this project actually publishes.
//
// Not a fixture designed to exercise the parser: it is doc/school-presets.json
// as it stands, so the values below are the publisher's and not the test
// author's. A parser that only ever meets documents written to suit it is a
// parser that has not met the one it is for.
func TestTheRealPublishedCatalogueIsRead(t *testing.T) {
	catalogue, err := Parse(testdata(t, "school-presets.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if catalogue.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d", catalogue.SchemaVersion)
	}
	if catalogue.UpdatedAt == "" {
		t.Error("updated_at is empty; the cache rule has nothing to compare")
	}
	if len(catalogue.Schools) == 0 {
		t.Fatal("no schools were read out of the published catalogue")
	}

	var jxnu School
	for _, school := range catalogue.Schools {
		if school.ShortName == "jxnu" {
			jxnu = school
		}
	}
	if jxnu.ShortName == "" {
		t.Fatal("jxnu is not in the published catalogue")
	}
	if jxnu.Status != StatusActive {
		t.Errorf("jxnu status = %q", jxnu.Status)
	}
	if jxnu.Defaults.BaseURL != "http://172.17.1.2" {
		t.Errorf("base_url = %q", jxnu.Defaults.BaseURL)
	}
	if jxnu.Defaults.SSID != "jxnu_stu" || jxnu.Defaults.ACID != "1" {
		t.Errorf("defaults = %+v", jxnu.Defaults)
	}
	// The braces a contributor pastes in with a captured value are gone.
	if jxnu.ObservedLoginShape.InfoPrefix != "SRBX1" {
		t.Errorf("info_prefix = %q", jxnu.ObservedLoginShape.InfoPrefix)
	}
	if jxnu.ObservedLoginShape.OS != "Windows 10" || jxnu.ObservedLoginShape.Name != "Windows" {
		t.Errorf("login shape = %+v", jxnu.ObservedLoginShape)
	}

	// Four carriers, one of which is the no-suffix choice -- which is a choice,
	// not an absence.
	var plain bool
	for _, operator := range jxnu.Operators {
		if operator.Suffix == "" {
			plain = true
			if operator.Label == "" {
				t.Error("the no-suffix option has no label to show")
			}
		}
	}
	if !plain {
		t.Errorf("jxnu offers %+v, with no plain-account option", jxnu.Operators)
	}
}

// Output this build cannot read is refused, not read as an empty catalogue.
//
// The caller uses the difference to decide whether to try the next source. An
// empty catalogue means "this school list has no schools", which is a fact
// about the publisher; a refusal means "I could not read it", which is a fact
// about this payload.
func TestAPayloadThatCannotBeReadIsRefused(t *testing.T) {
	for name, input := range map[string]string{
		"nothing at all":       "",
		"not json":             "<html>404</html>",
		"an array":             "[]",
		"no schools key":       `{"schema_version":1,"updated_at":"2026-01-01"}`,
		"schools is an object": `{"schema_version":1,"schools":{}}`,
		"version is a word":    `{"schema_version":"v2","schools":[]}`,
		"version is absent":    `{"schools":[]}`,
	} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("%s: parsed without complaint", name)
		}
	}

	// A future version is refused with its own code, so a caller can tell
	// "this build is too old" from "this document is broken".
	_, err := Parse([]byte(`{"schema_version":2,"schools":[]}`))
	if code, _ := domain.CodeOf(err); code != domain.CodePackageIncompatible {
		t.Errorf("a newer schema = %v (code %s), want PackageIncompatible", err, code)
	}

	// And a version that is not a number at all is the other one. Reporting
	// "v2" as an incompatible package would send somebody to look for a newer
	// build to read a document that is simply malformed.
	for _, body := range []string{
		`{"schema_version":"v2","schools":[]}`,
		`{"schema_version":"","schools":[]}`,
		`{"schema_version":[1],"schools":[]}`,
		`{"schools":[]}`,
	} {
		_, err := Parse([]byte(body))
		if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid {
			t.Errorf("%s = %v (code %s), want ProtocolInvalid", body, err, code)
		}
	}

	// An empty school list is a real answer and not an error.
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[]}`))
	if err != nil {
		t.Fatalf("an empty catalogue is not a failure: %v", err)
	}
	if len(catalogue.Schools) != 0 {
		t.Errorf("read %d schools out of none", len(catalogue.Schools))
	}
}

// An operator with no suffix and one with no evidence of a suffix are
// different.
//
// F20 in one test. `{"suffix":""}` is a school where accounts carry no suffix
// and is offered; `{}` is a catalogue entry that says nothing and is dropped.
// Go's zero value cannot tell them apart, which is why the decoder keeps
// presence separately.
func TestAnEmptySuffixIsAChoiceAndAMissingOneIsNot(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[{
		"id":"t","status":"active","operators":[
			{"suffix":"","label":"校园网"},
			{"suffix":"cmcc"},
			{"label":"carrier with no suffix at all"},
			{"id":"legacy-spelling"}
		]}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	operators := catalogue.Schools[0].Operators
	if len(operators) != 3 {
		t.Fatalf("got %d operators, want the three with evidence: %+v",
			len(operators), operators)
	}
	if operators[0].Suffix != "" || operators[0].Label != "校园网" {
		t.Errorf("operators[0] = %+v, want the no-suffix choice", operators[0])
	}
	// A suffix with no label gets one, because the list is shown to a person.
	if operators[1].Suffix != "cmcc" || operators[1].Label != "cmcc" {
		t.Errorf("operators[1] = %+v", operators[1])
	}
	// The old field name still counts as evidence.
	if operators[2].Suffix != "legacy-spelling" {
		t.Errorf("operators[2] = %+v, want the legacy id read as a suffix",
			operators[2])
	}
}

// A school's old default operator becomes a carrier choice, at the front.
//
// It was the school's default, so it is the one most of that school's users
// want. Present-with-an-empty-value is still present: it means the default is
// "no suffix", which is a choice. Absent means the catalogue says nothing, and
// nothing is what gets offered.
func TestALegacyDefaultOperatorBecomesTheFirstChoice(t *testing.T) {
	for name, testCase := range map[string]struct {
		defaults string
		want     []string
	}{
		"a suffix": {
			defaults: `{"operator_suffix":"ctcc"}`,
			want:     []string{"ctcc", "cmcc"},
		},
		"no suffix, said explicitly": {
			defaults: `{"operator_suffix":""}`,
			want:     []string{"", "cmcc"},
		},
		"the xn placeholder means no suffix": {
			defaults: `{"operator":"XN"}`,
			want:     []string{"", "cmcc"},
		},
		"already in the list": {
			defaults: `{"operator_suffix":"cmcc"}`,
			want:     []string{"cmcc"},
		},
		"nothing said": {
			defaults: `{"base_url":"http://x"}`,
			want:     []string{"cmcc"},
		},
	} {
		body := `{"schema_version":1,"schools":[{"id":"t","status":"active",` +
			`"defaults":` + testCase.defaults + `,"operators":[{"suffix":"cmcc"}]}]}`
		catalogue, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("%s: Parse: %v", name, err)
		}
		operators := catalogue.Schools[0].Operators
		if len(operators) != len(testCase.want) {
			t.Errorf("%s: got %+v, want %v", name, operators, testCase.want)
			continue
		}
		for index, want := range testCase.want {
			if operators[index].Suffix != want {
				t.Errorf("%s: operators[%d] = %q, want %q",
					name, index, operators[index].Suffix, want)
			}
		}
	}
}

// A status nobody recognises is a draft, not an active preset.
//
// Defaulting the other way would put an unreviewed contribution in front of
// users as though somebody had checked it.
func TestAnUnrecognisedStatusIsADraft(t *testing.T) {
	for body, want := range map[string]Status{
		`{"id":"a","status":"active"}`:        StatusActive,
		`{"id":"a","status":"verified"}`:      StatusActive,
		`{"id":"a","status":"ACTIVE"}`:        StatusActive,
		`{"id":"a","status":"draft"}`:         StatusDraft,
		`{"id":"a","status":"deprecated"}`:    StatusDeprecated,
		`{"id":"a","status":"probably-fine"}`: StatusDraft,
		`{"id":"a"}`:                          StatusDraft,
		`{"id":"a","verified":true}`:          StatusActive,
		`{"id":"a","verified":false}`:         StatusDraft,
	} {
		catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[` + body + `]}`))
		if err != nil {
			t.Fatalf("%s: Parse: %v", body, err)
		}
		if got := catalogue.Schools[0].Status; got != want {
			t.Errorf("%s: status = %q, want %q", body, got, want)
		}
	}
}

// Drafts stay in the catalogue and are hidden by Active, not dropped by Parse.
//
// Dropping them at parse time would let a built-in active entry replace a
// remote draft of the same school during a merge, which is a different result
// from hiding it.
func TestDraftsAreCarriedAndHiddenRatherThanDropped(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[
		{"id":"a","status":"active"},
		{"id":"b","status":"draft"},
		{"id":"c","status":"deprecated"}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(catalogue.Schools) != 3 {
		t.Errorf("Parse kept %d schools, want all three", len(catalogue.Schools))
	}
	if active := catalogue.Active(); len(active) != 1 || active[0].ShortName != "a" {
		t.Errorf("Active() = %+v, want only the active one", active)
	}
}

// The first copy of a repeated school wins.
func TestARepeatedSchoolKeepsItsFirstCopy(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[
		{"id":"dup","name":"first","status":"active"},
		{"id":"dup","name":"second","status":"active"}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(catalogue.Schools) != 1 {
		t.Fatalf("got %d schools, want one", len(catalogue.Schools))
	}
	if catalogue.Schools[0].Name != "first" {
		t.Errorf("name = %q, want the first copy", catalogue.Schools[0].Name)
	}
}

// An id that would not be safe in a path or a UCI value is made safe, and one
// that reduces to nothing takes its school with it.
func TestSchoolIdentifiersAreMadeSafe(t *testing.T) {
	for raw, want := range map[string]string{
		`"JXNU"`:            "jxnu",
		`"some school"`:     "some-school",
		`"a/../b"`:          "a-..-b",
		`"  trimmed  "`:     "trimmed",
		`"keep_this.one-2"`: "keep_this.one-2",
	} {
		catalogue, err := Parse([]byte(
			`{"schema_version":1,"schools":[{"id":` + raw + `,"status":"active"}]}`))
		if err != nil {
			t.Fatalf("%s: Parse: %v", raw, err)
		}
		if len(catalogue.Schools) != 1 {
			t.Fatalf("%s: got %d schools", raw, len(catalogue.Schools))
		}
		if got := catalogue.Schools[0].ShortName; got != want {
			t.Errorf("%s: short_name = %q, want %q", raw, got, want)
		}
	}

	// Nothing left to store it under.
	catalogue, err := Parse([]byte(
		`{"schema_version":1,"schools":[{"id":"///","status":"active"},{"id":"ok"}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(catalogue.Schools) != 1 || catalogue.Schools[0].ShortName != "ok" {
		t.Errorf("schools = %+v, want the unnamed one dropped", catalogue.Schools)
	}
}

// A number where a string was expected is read, not refused.
//
// The catalogue is edited by hand and published without a schema tool, so
// `"ac_id": 1` turns up beside `"ac_id": "1"`. Rejecting one of them would
// drop a real school over a pair of quotes.
func TestNumbersAndStringsAreBothRead(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":"1","schools":[{
		"id":"t","status":"active",
		"defaults":{"ac_id":1,"base_url":"172.17.1.2"},
		"observed_login_shape":{"n":200,"double_stack":0}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	school := catalogue.Schools[0]
	if school.Defaults.ACID != "1" {
		t.Errorf("ac_id = %q, want 1 without exponent notation", school.Defaults.ACID)
	}
	if school.ObservedLoginShape.N != "200" || school.ObservedLoginShape.DoubleStack != "0" {
		t.Errorf("login shape = %+v", school.ObservedLoginShape)
	}
	// And a bare host gained a scheme.
	if school.Defaults.BaseURL != "http://172.17.1.2" {
		t.Errorf("base_url = %q", school.Defaults.BaseURL)
	}
}

// An access mode this program cannot act on is dropped rather than carried.
//
// It would otherwise be written into an account and then decide which
// interface authenticates.
func TestAnUnknownAccessModeIsDropped(t *testing.T) {
	for mode, want := range map[string]string{
		`"wifi"`:     "wifi",
		`"wired"`:    "wired",
		`"WIRED"`:    "wired",
		`"mesh"`:     "",
		`"ethernet"`: "",
		`""`:         "",
	} {
		catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[{"id":"t",
			"status":"active","defaults":{"access_mode":` + mode + `}}]}`))
		if err != nil {
			t.Fatalf("%s: Parse: %v", mode, err)
		}
		if got := catalogue.Schools[0].Defaults.AccessMode; got != want {
			t.Errorf("access_mode %s -> %q, want %q", mode, got, want)
		}
	}
}

// The unverified sentinel is carried and marked, never silently used.
//
// A contributor writes ?? to say "this carrier exists and I could not confirm
// its suffix". It has to reach the wizard so it can be shown with a warning,
// and it must never reach an account -- one carrying it would build user@??
// and authenticate as nobody.
func TestTheUnverifiedSentinelIsCarriedAndRecognisable(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[{"id":"t",
		"status":"active","operators":[{"suffix":"??","label":"中国移动"}]}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	operators := catalogue.Schools[0].Operators
	if len(operators) != 1 {
		t.Fatalf("got %+v", operators)
	}
	if !operators[0].Unverified() {
		t.Error("the ?? sentinel was not recognised")
	}
	if operators[0].Label != "中国移动" {
		t.Errorf("label = %q; the carrier's name is what makes the warning useful",
			operators[0].Label)
	}
	if (Operator{Suffix: "cmcc"}).Unverified() {
		t.Error("a real suffix was called unverified")
	}
}

// Both spellings of the captured login shape are read.
func TestEitherSpellingOfTheLoginShapeIsRead(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[
		{"id":"a","observed_login_shape":{"os":"Windows 10","name":"Windows"}},
		{"id":"b","observed_login_shape":{"login_os":"Linux","login_name":"Firefox"}},
		{"id":"c","observed_login_shape":{"os":"","login_os":"fallback"}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	shapes := map[string]LoginShape{}
	for _, school := range catalogue.Schools {
		shapes[school.ShortName] = school.ObservedLoginShape
	}
	if shapes["a"].OS != "Windows 10" || shapes["a"].Name != "Windows" {
		t.Errorf("a = %+v", shapes["a"])
	}
	if shapes["b"].OS != "Linux" || shapes["b"].Name != "Firefox" {
		t.Errorf("b = %+v", shapes["b"])
	}
	if shapes["c"].OS != "fallback" {
		t.Errorf("c = %+v, want the longer spelling used when the short one is empty",
			shapes["c"])
	}
}

// A captured info prefix arrives wrapped in the braces of the capture.
func TestTheInfoPrefixLosesTheBracesItWasPastedWith(t *testing.T) {
	for raw, want := range map[string]string{
		`"{SRBX1}"`:   "SRBX1",
		`"SRBX1"`:     "SRBX1",
		`"{ SRBX1 }"`: "SRBX1",
		`"{}"`:        "{}",
		`"{"`:         "{",
		`"{a}b"`:      "{a}b",
	} {
		catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[{"id":"t",
			"observed_login_shape":{"info_prefix":` + raw + `}}]}`))
		if err != nil {
			t.Fatalf("%s: Parse: %v", raw, err)
		}
		if got := catalogue.Schools[0].ObservedLoginShape.InfoPrefix; got != want {
			t.Errorf("info_prefix %s -> %q, want %q", raw, got, want)
		}
	}
}

// A name and a description are always there to show.
func TestAPresetAlwaysHasSomethingToDisplay(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[
		{"id":"nameless","status":"active"},
		{"id":"drafty","status":"draft"}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	first, second := catalogue.Schools[0], catalogue.Schools[1]
	if first.Name != "nameless" {
		t.Errorf("name = %q, want the identifier as a fallback", first.Name)
	}
	if first.Description == "" || second.Description == "" {
		t.Error("a preset with no description has nothing to show")
	}
	// And a draft does not describe itself as a confirmed one.
	if first.Description == second.Description {
		t.Errorf("an active and a draft preset describe themselves identically: %q",
			first.Description)
	}
}
