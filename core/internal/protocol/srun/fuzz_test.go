package srun

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// Spec 05 requires each parser and codec to be fuzzed on its own. Run them one
// at a time; -fuzz=. would start only the first match:
//
//	go test ./internal/protocol/srun/ -run xxx -fuzz FuzzParseJSONP -fuzztime 60s

// A hostile gateway controls this input entirely. Whatever it sends, the parser
// either refuses it or returns something that really is a JSON object.
func FuzzParseJSONP(f *testing.F) {
	for _, seed := range []string{
		"", " ", "{}", `{"error":"ok"}`, `cb({"error":"ok"})`, `cb({"a":1});`,
		"<html></html>", "[]", "null", `cb(`, `cb()`, `cb({)`, `((((`, `))))`,
		`cb({"a":"\ud800"})`, "\x00", `cb({"a":1}) trailing`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		value, kind, err := ParseJSONP(body, MaxAuthResponseBytes)
		if err != nil {
			if value != nil {
				t.Fatalf("a refusal returned a payload: %s", value)
			}
			if kind == KindObject || kind == "" {
				t.Fatalf("a refusal used kind %q", kind)
			}
			return
		}

		if kind != KindObject {
			t.Fatalf("success used kind %q", kind)
		}
		if len(value) == 0 || value[0] != '{' {
			t.Fatalf("success returned something that is not an object: %s", value)
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(value, &probe); err != nil {
			t.Fatalf("success returned bytes that do not parse as an object: %v", err)
		}
	})
}

// The info encoder runs on user-supplied credentials. Its output must always be
// printable ASCII that parses back to exactly what went in, or be refused.
func FuzzEncodeInfo(f *testing.F) {
	for _, seed := range []string{
		"", "u", "密码", "\U0001f600", `"`, `\`, "\x00\x01\x1f", "<&>", "\x7f",
		"a\bb\fc\nd\re\tf",
	} {
		f.Add(seed, seed, seed)
	}

	f.Fuzz(func(t *testing.T, username, password, ip string) {
		info := Info{Username: username, Password: password, IP: ip,
			ACID: "1", EncVer: "srun_bx1"}

		encoded, err := EncodeInfo(info)
		if err != nil {
			// The only reason to refuse is input that is not valid UTF-8.
			if utf8.ValidString(username) && utf8.ValidString(password) &&
				utf8.ValidString(ip) {
				t.Fatalf("valid UTF-8 was refused: %v", err)
			}
			return
		}

		for index := range len(encoded) {
			if encoded[index] < 0x20 || encoded[index] > 0x7e {
				t.Fatalf("byte %#x at %d is outside printable ASCII",
					encoded[index], index)
			}
		}

		var decoded struct {
			Username string `json:"username"`
			Password string `json:"password"`
			IP       string `json:"ip"`
		}
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			t.Fatalf("the encoded info does not parse: %v (%s)", err, encoded)
		}
		if decoded.Username != username || decoded.Password != password || decoded.IP != ip {
			t.Fatalf("round trip changed the values:\n got %q %q %q\nwant %q %q %q",
				decoded.Username, decoded.Password, decoded.IP, username, password, ip)
		}
	})
}

// Encryption has to stay invertible for every input, or some account somewhere
// sends a blob the gateway cannot read.
func FuzzXencodeRoundTrip(f *testing.F) {
	f.Add([]byte("a"), []byte("key"))
	f.Add([]byte("abcd"), []byte(""))
	f.Add([]byte("abcdefgh"), []byte("0123456789abcdef"))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}, []byte{0xff})

	f.Fuzz(func(t *testing.T, msg, key []byte) {
		if len(msg) == 0 {
			if Xencode(msg, key) != nil {
				t.Fatal("an empty message produced output")
			}
			return
		}
		// Keep the fuzzer away from inputs whose only effect is a slow test.
		if len(msg) > 4096 || len(key) > 4096 {
			return
		}

		cipher := Xencode(msg, key)
		if len(cipher)%4 != 0 {
			t.Fatalf("ciphertext is %d bytes, not a whole number of words", len(cipher))
		}

		plain := xencodeDecrypt(cipher, key)
		if plain == nil {
			t.Fatal("decrypt refused a ciphertext this package produced")
		}
		length := int(uint32(plain[len(plain)-4]) | uint32(plain[len(plain)-3])<<8 |
			uint32(plain[len(plain)-2])<<16 | uint32(plain[len(plain)-1])<<24)
		if length != len(msg) {
			t.Fatalf("recovered length %d, want %d", length, len(msg))
		}
		if got := string(plain[:len(plain)-4][:length]); got != string(msg) {
			t.Fatalf("recovered %q, want %q", got, msg)
		}
	})
}

// With the standard table this must be Base64, for every input.
func FuzzBase64AgainstTheStandardLibrary(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte("a"))
	f.Add([]byte("abc"))
	f.Add([]byte{0x00, 0xff, 0x10})

	standard, err := NewAlphabet(StandardAlphabetTable)
	if err != nil {
		f.Fatalf("standard alphabet: %v", err)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 65536 {
			return
		}
		want := base64.StdEncoding.EncodeToString(input)
		if got := standard.Encode(input); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	})
}
