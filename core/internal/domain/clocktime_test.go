package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func mustParse(t *testing.T, text string) ClockTime {
	t.Helper()
	value, err := ParseClockTime(text)
	if err != nil {
		t.Fatalf("ParseClockTime(%q): %v", text, err)
	}
	return value
}

func beijing(t *testing.T, text string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", text, Beijing)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return at
}

func TestParseClockTimeAcceptsOnlyHHMM(t *testing.T) {
	valid := map[string][2]int{
		"00:00": {0, 0},
		"06:00": {6, 0},
		"23:59": {23, 59},
	}
	for text, want := range valid {
		got := mustParse(t, text)
		if got.Hour() != want[0] || got.Minute() != want[1] {
			t.Errorf("%q parsed to %02d:%02d", text, got.Hour(), got.Minute())
		}
		if got.String() != text {
			t.Errorf("round trip of %q produced %q", text, got.String())
		}
	}

	// Lenient forms are rejected: they mean something other than the UI wrote
	// this value, and accepting them hides the real problem.
	for _, text := range []string{
		"6:00", "06:0", " 06:00", "06:00 ", "06:00:00", "24:00", "12:60",
		"aa:bb", "0600", "", "06-00", "１２:００",
	} {
		if _, err := ParseClockTime(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}

func TestClockTimeJSONRoundTrip(t *testing.T) {
	type holder struct {
		At ClockTime `json:"at"`
	}
	encoded, err := json.Marshal(holder{At: mustParse(t, "06:05")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `{"at":"06:05"}` {
		t.Fatalf("encoded = %s, want {\"at\":\"06:05\"}", encoded)
	}

	var decoded holder
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.At != mustParse(t, "06:05") {
		t.Fatalf("decoded = %v", decoded.At)
	}

	for _, bad := range []string{`{"at":"6:05"}`, `{"at":605}`, `{"at":null}`} {
		if err := json.Unmarshal([]byte(bad), &decoded); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// T20 -- the three window shapes and the boundaries between them.
func TestQuietWindowMembership(t *testing.T) {
	sameDay := QuietWindow{Start: mustParse(t, "01:00"), End: mustParse(t, "06:00")}
	overnight := QuietWindow{Start: mustParse(t, "23:00"), End: mustParse(t, "06:00")}
	empty := QuietWindow{Start: mustParse(t, "03:00"), End: mustParse(t, "03:00")}

	cases := []struct {
		name   string
		window QuietWindow
		at     string
		want   bool
	}{
		{"same day, before", sameDay, "2026-09-12 00:59", false},
		{"same day, at start is inside", sameDay, "2026-09-12 01:00", true},
		{"same day, middle", sameDay, "2026-09-12 03:00", true},
		{"same day, at end is outside", sameDay, "2026-09-12 06:00", false},
		{"overnight, evening", overnight, "2026-09-12 23:30", true},
		{"overnight, after midnight", overnight, "2026-09-12 02:00", true},
		{"overnight, at end is outside", overnight, "2026-09-12 06:00", false},
		{"overnight, daytime", overnight, "2026-09-12 12:00", false},
		// start == end is an empty window, not a 24-hour one. Reading it as
		// all-day would log every account out forever the moment a user typed
		// the same value twice.
		{"equal endpoints exclude everything", empty, "2026-09-12 03:00", false},
		{"equal endpoints exclude noon", empty, "2026-09-12 12:00", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.window.Contains(beijing(t, testCase.at)); got != testCase.want {
				t.Fatalf("Contains(%s) = %v, want %v", testCase.at, got, testCase.want)
			}
		})
	}
}

// The window is Beijing time regardless of the caller's zone, so a router whose
// local zone is wrong still applies the campus policy at the right moment.
func TestQuietWindowIsEvaluatedInBeijingTime(t *testing.T) {
	window := QuietWindow{Start: mustParse(t, "01:00"), End: mustParse(t, "06:00")}
	utc := time.Date(2026, 9, 11, 18, 0, 0, 0, time.UTC) // 02:00 Beijing

	if !window.Contains(utc) {
		t.Fatal("a UTC instant inside the Beijing window was reported outside")
	}
	if !window.Contains(utc.In(time.FixedZone("UTC-5", -5*3600))) {
		t.Fatal("the same instant in another zone gave a different answer")
	}
}

// Forced logout runs once per occurrence. Deriving the identity from the
// occurrence's start instant means a clock correction inside the window maps to
// the same id, so the sweep does not run twice.
func TestQuietOccurrenceIDIsStableWithinOneOccurrence(t *testing.T) {
	overnight := QuietWindow{Start: mustParse(t, "23:00"), End: mustParse(t, "06:00")}

	evening, ok := overnight.OccurrenceID(beijing(t, "2026-09-12 23:10"))
	if !ok {
		t.Fatal("23:10 is inside the window")
	}
	afterMidnight, ok := overnight.OccurrenceID(beijing(t, "2026-09-13 02:00"))
	if !ok {
		t.Fatal("02:00 is inside the same occurrence")
	}
	if evening != afterMidnight {
		t.Fatalf("occurrence ids differ across midnight: %q vs %q; forced "+
			"logout would run a second time", evening, afterMidnight)
	}

	nextNight, ok := overnight.OccurrenceID(beijing(t, "2026-09-13 23:10"))
	if !ok {
		t.Fatal("the next night is inside the window")
	}
	if nextNight == evening {
		t.Fatal("two different nights share an occurrence id, so the second " +
			"night would be skipped")
	}

	if _, ok := overnight.OccurrenceID(beijing(t, "2026-09-13 12:00")); ok {
		t.Fatal("noon is outside the window but produced an occurrence id")
	}
}

func TestQuietWindowNextBoundary(t *testing.T) {
	overnight := QuietWindow{Start: mustParse(t, "23:00"), End: mustParse(t, "06:00")}

	next, ok := overnight.NextBoundary(beijing(t, "2026-09-12 12:00"))
	if !ok {
		t.Fatal("a non-empty window always has a next boundary")
	}
	if want := beijing(t, "2026-09-12 23:00"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}

	next, _ = overnight.NextBoundary(beijing(t, "2026-09-12 23:30"))
	if want := beijing(t, "2026-09-13 06:00"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}

	empty := QuietWindow{Start: mustParse(t, "03:00"), End: mustParse(t, "03:00")}
	if _, ok := empty.NextBoundary(beijing(t, "2026-09-12 12:00")); ok {
		t.Fatal("an empty window has no boundary to wait for")
	}
}
