package srun

import (
	"encoding/json"
	"strings"
	"testing"
)

// T01 -- the field order is the wire format, not a serialisation detail.
func TestInfoFieldOrderIsFixed(t *testing.T) {
	encoded, err := EncodeInfo(Info{
		Username: "u", Password: "p", IP: "i", ACID: "a", EncVer: "e",
	})
	if err != nil {
		t.Fatalf("EncodeInfo: %v", err)
	}
	if encoded != `{"username":"u","password":"p","ip":"i","acid":"a","enc_ver":"e"}` {
		t.Fatalf("encoded = %s", encoded)
	}

	// Repeating has to give the same bytes. A map would not.
	for range 50 {
		again, _ := EncodeInfo(Info{
			Username: "u", Password: "p", IP: "i", ACID: "a", EncVer: "e",
		})
		if again != encoded {
			t.Fatalf("two encodings of the same input differ:\n%s\n%s", encoded, again)
		}
	}
}

// The whole reason this encoder is hand-written. encoding/json produces
// different bytes for the same object, and because the result is encrypted and
// checksummed the only symptom would be a login the gateway rejects.
func TestEncodingJSONWouldProduceDifferentBytes(t *testing.T) {
	cases := []struct {
		name     string
		password string
	}{
		{"an ampersand", "a&b"},
		{"angle brackets", "a<b>c"},
		{"Chinese", "密码"},
		{"an emoji", "\U0001f600"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ours, err := EncodeInfo(Info{Username: "u", Password: testCase.password,
				IP: "i", ACID: "a", EncVer: "e"})
			if err != nil {
				t.Fatalf("EncodeInfo: %v", err)
			}

			// The same object through the standard encoder, field order aside.
			theirs, err := json.Marshal(map[string]string{"password": testCase.password})
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			standardField := strings.TrimSuffix(strings.TrimPrefix(string(theirs), `{`), `}`)
			if strings.Contains(ours, standardField) {
				t.Fatalf("encoding/json now agrees for %q; if that is really true "+
					"this encoder could be replaced, but check every case first",
					testCase.password)
			}
		})
	}
}

// The output is ASCII-only, whatever goes in. A non-ASCII byte would mean the
// escaping rule changed, and the gateway would receive a different blob.
func TestInfoOutputIsAlwaysASCII(t *testing.T) {
	for _, password := range []string{
		"密码", "\U0001f600", "  ", "\x7f", "Ünïcödé", " ",
	} {
		encoded, err := EncodeInfo(Info{Username: "u", Password: password,
			IP: "i", ACID: "a", EncVer: "e"})
		if err != nil {
			t.Fatalf("EncodeInfo(%q): %v", password, err)
		}
		for index := range len(encoded) {
			if encoded[index] > 0x7e || encoded[index] < 0x20 {
				t.Fatalf("password %q produced a non-printable-ASCII byte %#x at %d",
					password, encoded[index], index)
			}
		}
	}
}

// Whatever the escaping, the result must still mean the same thing. This reads
// the bytes back with the standard parser, which is independent of how they
// were written.
func TestInfoRoundTripsThroughAStandardParser(t *testing.T) {
	original := Info{
		Username: "2020123456@cmcc",
		Password: "p@ss \"w0rd\"\\ 密码 \U0001f600 <&>",
		IP:       "10.0.0.2",
		ACID:     "12",
		EncVer:   "srun_bx1",
	}
	encoded, err := EncodeInfo(original)
	if err != nil {
		t.Fatalf("EncodeInfo: %v", err)
	}

	var decoded struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IP       string `json:"ip"`
		ACID     string `json:"acid"`
		EncVer   string `json:"enc_ver"`
	}
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("the encoded info does not parse: %v (%s)", err, encoded)
	}
	if decoded.Username != original.Username || decoded.Password != original.Password ||
		decoded.IP != original.IP || decoded.ACID != original.ACID ||
		decoded.EncVer != original.EncVer {
		t.Fatalf("round trip changed the values:\n got %+v\nwant %+v", decoded, original)
	}
}

// A password with invalid UTF-8 must be refused, not silently repaired. The
// replacement character would go to the gateway in place of the real bytes and
// the user would see an authentication failure with a correct password.
func TestInvalidUTF8IsRefusedRatherThanSubstituted(t *testing.T) {
	_, err := EncodeInfo(Info{Username: "u", Password: "pw\xffmore",
		IP: "i", ACID: "a", EncVer: "e"})
	if err == nil {
		t.Fatal("invalid UTF-8 in a password was accepted")
	}
	if strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), "more") {
		t.Fatalf("the error quotes the password: %v", err)
	}

	// A genuine replacement character is valid UTF-8 and must go through.
	if _, err := EncodeInfo(Info{Username: "u", Password: "pw�more",
		IP: "i", ACID: "a", EncVer: "e"}); err != nil {
		t.Fatalf("a real U+FFFD was rejected: %v", err)
	}
}

// Every field is escaped, not just the password: a username can carry a realm
// and an ac_id arrives as a string from configuration.
func TestEveryFieldIsEscaped(t *testing.T) {
	for _, build := range []func(string) Info{
		func(v string) Info { return Info{Username: v} },
		func(v string) Info { return Info{Password: v} },
		func(v string) Info { return Info{IP: v} },
		func(v string) Info { return Info{ACID: v} },
		func(v string) Info { return Info{EncVer: v} },
	} {
		encoded, err := EncodeInfo(build(`"`))
		if err != nil {
			t.Fatalf("EncodeInfo: %v", err)
		}
		if !strings.Contains(encoded, `\"`) {
			t.Fatalf("a quote went through unescaped: %s", encoded)
		}
		var probe map[string]string
		if err := json.Unmarshal([]byte(encoded), &probe); err != nil {
			t.Fatalf("an unescaped field broke the JSON: %v (%s)", err, encoded)
		}

		if _, err := EncodeInfo(build("\xff")); err == nil {
			t.Fatal("invalid UTF-8 was accepted in one of the fields")
		}
	}
}
