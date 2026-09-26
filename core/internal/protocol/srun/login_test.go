package srun

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// T03 -- an alphabet that is not 64 distinct printable bytes cannot encode
// reversibly, so it is refused when it is built rather than when a login fails.
func TestAlphabetValidation(t *testing.T) {
	cases := []struct {
		name  string
		table string
	}{
		{"too short", StandardAlphabetTable[:63]},
		{"too long", StandardAlphabetTable + "x"},
		{"empty", ""},
		{"a repeated symbol", "A" + StandardAlphabetTable[1:63] + "A"},
		{"contains the padding character", "=" + StandardAlphabetTable[1:]},
		{"contains a space", " " + StandardAlphabetTable[1:]},
		{"contains a control character", "\x01" + StandardAlphabetTable[1:]},
		{"non-ASCII, which is 64 runes but not 64 bytes", strings.Repeat("é", 64)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewAlphabet(testCase.table); err == nil {
				t.Fatal("accepted")
			} else if code, _ := domain.CodeOf(err); code != domain.CodeInvalidConfig {
				t.Fatalf("code = %q, want InvalidConfig", code)
			}
		})
	}

	alphabet, err := NewAlphabet(StandardAlphabetTable)
	if err != nil {
		t.Fatalf("a valid table was refused: %v", err)
	}
	if alphabet.Table() != StandardAlphabetTable {
		t.Fatalf("Table() = %q", alphabet.Table())
	}
}

// Users paste the prefix with and without braces. Whichever they do, the blob
// must carry exactly one pair.
func TestPrefixNormalisationNeverProducesDoubledBraces(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"SRBX1", "SRBX1"},
		{"{SRBX1}", "SRBX1"},
		{"  {SRBX1}  ", "SRBX1"},
		{"{ SRBX1 }", "SRBX1"},
		{"{{SRBX1}}", "SRBX1"},
		{"", DefaultInfoPrefix},
		{"   ", DefaultInfoPrefix},
		{"{}", DefaultInfoPrefix},
		{"{ }", DefaultInfoPrefix},
		{"SR{BX1", DefaultInfoPrefix},
		{"SRBX2", "SRBX2"},
	}

	for _, testCase := range cases {
		got := NormalizePrefix(testCase.raw, DefaultInfoPrefix)
		if got != testCase.want {
			t.Errorf("NormalizePrefix(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
		if strings.ContainsAny(got, "{}") {
			t.Errorf("NormalizePrefix(%q) = %q, which still contains a brace",
				testCase.raw, got)
		}
		// Normalising twice must not change anything, so a caller that has
		// already done it is not punished for it.
		if again := NormalizePrefix(got, DefaultInfoPrefix); again != got {
			t.Errorf("NormalizePrefix is not idempotent: %q -> %q", got, again)
		}
	}
}

func TestEncryptedInfoCarriesExactlyOneBracePair(t *testing.T) {
	for _, prefix := range []string{"SRBX1", "{SRBX1}", "{{SRBX1}}", "", "{}"} {
		blob := EncryptedInfo(prefix, `{"username":"u"}`, "token", DefaultAlphabet)
		if !strings.HasPrefix(blob, "{SRBX1}") {
			t.Errorf("prefix %q produced %q", prefix, blob[:min(12, len(blob))])
		}
		if strings.HasPrefix(blob, "{{") {
			t.Errorf("prefix %q produced a doubled brace", prefix)
		}
	}
}

// The checksum is a fixed interleaving. Reordering it, or dropping the repeated
// token, produces a digest the gateway rejects.
func TestChecksumInputInterleavesTheTokenBeforeEveryField(t *testing.T) {
	got := ChecksumInput("T", "user", "hmd5", "acid", "ip", "n", "type", "info")
	want := "Tuser" + "Thmd5" + "Tacid" + "Tip" + "Tn" + "Ttype" + "Tinfo"
	if got != want {
		t.Fatalf("checksum input\n got %s\nwant %s", got, want)
	}
	if strings.Count(got, "T") != 7 {
		t.Fatalf("the token appears %d times, want 7", strings.Count(got, "T"))
	}
}

// The wire password field is {MD5}+digest, but the checksum is taken over the
// bare digest. Feeding the prefixed form in is a mistake that looks right in a
// log and fails every login.
func TestTheChecksumUsesTheBareDigestNotTheWireField(t *testing.T) {
	const token, hmd5 = "tok", "0123456789abcdef0123456789abcdef"

	bare := ChecksumInput(token, "user", hmd5, "1", "10.0.0.2", "200", "1", "info")
	prefixed := ChecksumInput(token, "user", WirePasswordPrefix+hmd5, "1", "10.0.0.2", "200", "1", "info")

	if strings.Contains(bare, WirePasswordPrefix) {
		t.Fatal("the checksum input carries the {MD5} prefix")
	}
	if SHA1Hex(bare) == SHA1Hex(prefixed) {
		t.Fatal("the two produce the same checksum, so this test proves nothing")
	}
}

// T06 -- the logout signature repeats the timestamp at both ends and uses whole
// seconds. Passing milliseconds produces a signature the gateway rejects, so
// the two must not be interchangeable by accident.
func TestLogoutSignatureShape(t *testing.T) {
	const seconds = int64(1758000000)
	input := LogoutSignInput(seconds, "2020123456", "10.0.0.2")

	if input != "17580000002020123456"+"10.0.0.2"+"1"+"1758000000" {
		t.Fatalf("sign input = %s", input)
	}
	if !strings.HasPrefix(input, "1758000000") || !strings.HasSuffix(input, "1758000000") {
		t.Fatal("the timestamp must appear at both ends")
	}
	if LogoutSign(seconds, "u", "ip") == LogoutSign(seconds*1000, "u", "ip") {
		t.Fatal("seconds and milliseconds produced the same signature")
	}
	if got := LogoutSign(seconds, "u", "ip"); got != SHA1Hex(LogoutSignInput(seconds, "u", "ip")) {
		t.Fatalf("LogoutSign is not the SHA-1 of its input: %s", got)
	}
}

// Empty inputs must not panic or silently become something else: an empty
// message encrypts to nothing, and an empty blob is still well formed.
func TestEmptyInputs(t *testing.T) {
	if got := Xencode(nil, []byte("key")); got != nil {
		t.Errorf("Xencode(nil) = %v, want nil", got)
	}
	if got := Xencode([]byte{}, []byte("key")); got != nil {
		t.Errorf("Xencode(empty) = %v, want nil", got)
	}
	if got := DefaultAlphabet.Encode(nil); got != "" {
		t.Errorf("Encode(nil) = %q", got)
	}
	if got := EncryptedInfo("SRBX1", "", "token", DefaultAlphabet); got != "{SRBX1}" {
		t.Errorf("EncryptedInfo with an empty body = %q", got)
	}
}
