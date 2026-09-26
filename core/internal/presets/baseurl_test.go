package presets

import "testing"

// A base URL is reduced to scheme and authority, and the path is dropped.
//
// The value is a gateway's address, and the paths this program appends are
// fixed by the protocol. A preset carrying /portal would produce requests to
// /portal/cgi-bin/srun_portal, which a gateway answers with its login page
// rather than an error -- so the failure would surface as "the gateway sent
// HTML" and be diagnosed as a protocol problem rather than a wrong address.
func TestABaseURLKeepsOnlySchemeAndHost(t *testing.T) {
	for raw, want := range map[string]string{
		// The case the whole function exists for.
		"http://172.17.1.2/portal":       "http://172.17.1.2",
		"http://172.17.1.2/portal/":      "http://172.17.1.2",
		"http://gw.example.edu/a/b?c=d":  "http://gw.example.edu",
		"https://gw.example.edu/":        "https://gw.example.edu",
		"http://gw.example.edu#fragment": "http://gw.example.edu",

		// Ports and credentials are part of the authority and stay.
		"http://172.17.1.2:8080/x": "http://172.17.1.2:8080",

		// A bare host gains the scheme campus gateways almost always are.
		"172.17.1.2":       "http://172.17.1.2",
		"gw.example.edu":   "http://gw.example.edu",
		"gw.example.edu/x": "http://gw.example.edu",

		// Already minimal.
		"http://172.17.1.2":  "http://172.17.1.2",
		"https://172.17.1.2": "https://172.17.1.2",

		// Whitespace a contributor left in.
		"  http://172.17.1.2/portal  ": "http://172.17.1.2",

		// Nothing in, nothing out.
		"": "",

		// A value with no host at all. Nonsense input with a defined answer
		// rather than an accidental one, and the answer is the baseline's: the
		// scheme is prepended before anything else looks at the value, so what
		// comes back still carries it. Here to pin the prepend, which is
		// otherwise indistinguishable from the host fallback below it.
		"/portal": "http:///portal",
	} {
		if got := NormalizeBaseURL(raw); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

// A scheme this function does not understand is left as it was.
//
// Rewriting it would mean inventing an address the catalogue did not publish.
// The trailing slash goes, because that much is safe and the rest is not.
func TestAnUnknownSchemeIsLeftAlone(t *testing.T) {
	for raw, want := range map[string]string{
		"ftp://files.example.edu/pub/": "ftp://files.example.edu/pub",
		"ftp://files.example.edu":      "ftp://files.example.edu",
	} {
		if got := NormalizeBaseURL(raw); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Something that contains :// but is not a scheme is treated as a host.
//
// It is a strange input and it has a defined answer rather than an accidental
// one -- which is the point of testing it. A leading digit is not a scheme, so
// what precedes the slash is read as an authority and given http.
func TestSomethingThatOnlyLooksLikeASchemeIsReadAsAHost(t *testing.T) {
	if got, want := NormalizeBaseURL("1abc://host"), "http://1abc:"; got != want {
		t.Errorf("NormalizeBaseURL(%q) = %q, want %q", "1abc://host", got, want)
	}
}

// A JSON number arrives as the text a person wrote, not as Go's float.
func TestNumbersBecomeTheTextTheyLookLike(t *testing.T) {
	catalogue, err := Parse([]byte(`{"schema_version":1,"schools":[{"id":"t",
		"defaults":{"ac_id":1},
		"observed_login_shape":{"n":200,"type":1.5,"double_stack":0}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	school := catalogue.Schools[0]
	if school.Defaults.ACID != "1" {
		t.Errorf("ac_id = %q, want %q", school.Defaults.ACID, "1")
	}
	if school.ObservedLoginShape.N != "200" {
		t.Errorf("n = %q, want %q", school.ObservedLoginShape.N, "200")
	}
	// Not a value anybody should publish, but it must not become "1.5e+00".
	if school.ObservedLoginShape.Type != "1.5" {
		t.Errorf("type = %q, want %q", school.ObservedLoginShape.Type, "1.5")
	}
	if school.ObservedLoginShape.DoubleStack != "0" {
		t.Errorf("double_stack = %q, want %q",
			school.ObservedLoginShape.DoubleStack, "0")
	}

	// The guarantee is that a number never comes back in exponent notation,
	// and a large one is the only way to check it: Go's default float
	// formatting switches to 1e+21 exactly here, and a value that reached a
	// gateway as "1e+21" would be rejected without explanation.
	large, err := Parse([]byte(`{"schema_version":1,"schools":[{"id":"t",
		"defaults":{"ac_id":1e21}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := large.Schools[0].Defaults.ACID; got != "1000000000000000000000" {
		t.Errorf("ac_id = %q, want it spelled out rather than as an exponent", got)
	}
}

// A fresh catalogue with the same date as the cached one replaces it.
//
// This is the whole of the same-day correction rule. A publisher who finds a
// mistake an hour after publishing fixes it under the same date; a comparison
// that required a strictly newer date would leave every router on the broken
// copy until the next day.
func TestACatalogueWithTheSameDateStillSupersedes(t *testing.T) {
	cached := Catalogue{UpdatedAt: "2026-09-03"}

	for name, testCase := range map[string]struct {
		fresh Catalogue
		want  bool
	}{
		"newer":             {Catalogue{UpdatedAt: "2026-09-04"}, true},
		"the same day":      {Catalogue{UpdatedAt: "2026-09-03"}, true},
		"older":             {Catalogue{UpdatedAt: "2026-09-02"}, false},
		"much older":        {Catalogue{UpdatedAt: "2025-01-01"}, false},
		"fresh has no date": {Catalogue{}, true},
		"compared as text":  {Catalogue{UpdatedAt: "2026-9-4"}, true},
	} {
		if got := testCase.fresh.Supersedes(cached); got != testCase.want {
			t.Errorf("%s: Supersedes = %v, want %v", name, got, testCase.want)
		}
	}

	// And a cache with no date is replaced by anything, because there is
	// nothing to order against.
	if !(Catalogue{UpdatedAt: "2020-01-01"}).Supersedes(Catalogue{}) {
		t.Error("a cache with no date was treated as newer than a dated payload")
	}
}

// Dates are compared as text, which is a decision and not an accident.
//
// The baseline does the same. Parsing them would change which of two payloads
// wins for spellings that order differently as text than as dates -- "2026-9-4"
// sorts before "2026-09-03" as text and after it as a date -- and a rule that
// silently changed behaviour on a publisher's formatting choice is worse than
// one that is strict about the format.
func TestDatesAreComparedAsTextNotParsed(t *testing.T) {
	// "2026-9-4" < "2026-09-03" as text, so it supersedes under >= on text...
	fresh := Catalogue{UpdatedAt: "2026-9-4"}
	cached := Catalogue{UpdatedAt: "2026-09-03"}
	if !fresh.Supersedes(cached) {
		t.Error("text comparison changed; a date parser has crept in")
	}
	// ...and the other way round it does not, which a date parser would
	// reverse. This is here to fail loudly if somebody "fixes" the comparison.
	if (Catalogue{UpdatedAt: "2026-09-03"}).Supersedes(Catalogue{UpdatedAt: "2026-9-4"}) {
		t.Error("text comparison changed; a date parser has crept in")
	}
}
