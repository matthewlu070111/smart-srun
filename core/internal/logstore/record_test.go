package logstore

import (
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The moment every expected line below is written at, in Beijing time.
var noon = time.Date(2026, 9, 18, 12, 0, 0, 0, domain.Beijing)

// The format is not this program's to choose: the shipped page parses it.
func TestALineIsTheShapeTheInterfaceParses(t *testing.T) {
	record := Record{At: noon, Level: domain.LogInfo, Event: "action_started",
		Fields:  []Field{F("action_id", "a1"), F("kind", "manual_login")},
		Message: "开始执行"}

	want := "[2026-09-18 12:00:00] INFO action_started action_id=a1 kind=manual_login | 开始执行"
	if got := record.Line(); got != want {
		t.Errorf("line =\n%s\nwant\n%s", got, want)
	}
}

// A router whose clock is set to UTC still logs the time the same user sees in
// the quiet-hours setting, which is Beijing time by contract.
func TestTheTimestampIsBeijingWhateverTheHostZoneIs(t *testing.T) {
	utc := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	record := Record{At: utc, Level: domain.LogInfo, Event: "daemon_start"}
	if got := record.Line(); !strings.HasPrefix(got, "[2026-09-18 12:00:00]") {
		t.Errorf("line = %s, want a 12:00:00 Beijing timestamp", got)
	}
}

func TestAValueKeepsOneRecordOnOneLine(t *testing.T) {
	record := Record{At: noon, Level: domain.LogWarn, Event: "action_result",
		Fields: []Field{F("message", "line one\nline two"), F("empty", ""),
			F("spaced", "two words"), F("quoted", `say "hi"`),
			F("slash", `a\b`)},
		Message: "first\nsecond\ttabbed"}

	line := record.Line()
	if strings.Contains(line, "\n") || strings.Contains(line, "\t") {
		t.Fatalf("line contains a raw control character: %q", line)
	}
	for _, want := range []string{
		`message="line one\nline two"`,
		`empty=""`,
		`spaced="two words"`,
		`quoted="say \"hi\""`,
		`slash=a\\b`,
		`| first\nsecond\ttabbed`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line = %s\nmissing %s", line, want)
		}
	}
}

// The value is what must never appear. The key stays, so a reader can still see
// that a credential was part of the event.
func TestASecretIsRedactedByTheNameOfItsField(t *testing.T) {
	secret := "hunter2-please-do-not-log-me"
	record := Record{At: noon, Level: domain.LogInfo, Event: "action_started",
		Fields: []Field{
			F("password", secret), F("campus_key", secret), F("hotspot_key", secret),
			F("wifi_key", secret), F("psk", secret), F("hmd5", secret),
			F("chksum", secret), F("Authorization", secret), F("session", secret),
			F("idempotency_key", secret), F("user_id", "2021001"),
		}}

	line := record.Line()
	if strings.Contains(line, secret) {
		t.Fatalf("a secret reached the log: %s", line)
	}
	if strings.Count(line, Redacted) != 10 {
		t.Errorf("line = %s, want ten redacted values", line)
	}
	if !strings.Contains(line, "user_id=2021001") {
		t.Errorf("line = %s, want the non-secret field intact", line)
	}
	if !strings.Contains(line, "password=") {
		t.Errorf("line = %s, want the field name kept", line)
	}
}

// Matching is on the field name, not the value: a value that happens to look
// like a password under a name that is not one is ordinary data, and hiding it
// would make the log useless for the thing it was written for.
func TestAnOrdinaryFieldIsNotRedactedForLookingSecret(t *testing.T) {
	record := Record{At: noon, Level: domain.LogInfo, Event: "action_started",
		Fields: []Field{F("state", "password-expired")}}
	if got := record.Line(); !strings.Contains(got, "state=password-expired") {
		t.Errorf("line = %s, want the value kept", got)
	}
}

func TestOneRecordCannotFillTheLog(t *testing.T) {
	long := strings.Repeat("x", 4096)
	record := Record{At: noon, Level: domain.LogInfo, Event: long,
		Fields: []Field{F(long, long)}, Message: long}

	line := record.Line()
	if len(line) > MaxEventBytes+MaxKeyBytes+MaxValueBytes+MaxMessageBytes+128 {
		t.Errorf("line is %d bytes, want it clipped", len(line))
	}
	if strings.Count(line, Ellipsis) != 4 {
		t.Errorf("line = %.120s..., want every clipped part marked", line)
	}
}

// Clipping cuts on a rune boundary, so a Chinese message does not end in half a
// character the page would render as a replacement glyph.
func TestClippingLeavesValidUTF8(t *testing.T) {
	message := strings.Repeat("认证", 1000)
	record := Record{At: noon, Level: domain.LogInfo, Event: "action_result",
		Message: message}
	line := record.Line()
	if !strings.HasSuffix(line, Ellipsis) {
		t.Fatalf("line = %.80s..., want it clipped", line)
	}
	if strings.Contains(line, "�") {
		t.Errorf("clipping produced an invalid rune: %.80s...", line)
	}
}
