package auth

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/protocol/srun"
)

// A configured Base64 table has to reach the bytes on the wire.
//
// The table used to be a hard-coded srun.DefaultAlphabet at the encryption
// site, under a comment saying a school needing another one "gets it from its
// strategy" -- a path that did not exist. A Shape field that were plumbed but
// never read would reproduce exactly that, and it would look correct in every
// unit test that only checks the login succeeds. So this test does not check
// that a table is accepted; it checks that the info blob differs from the one
// the common table produces, and that it decodes back under the configured
// table and not the common one.
func TestAConfiguredAlphabetReachesTheEncryptedInfo(t *testing.T) {
	// The standard RFC 4648 table: a real, valid, different alphabet. Using it
	// rather than a shuffle keeps the expected encoding independently checkable.
	custom := srun.StandardAlphabetTable

	infoWith := func(shape Shape) string {
		gateway := newFakeGateway(t)
		transaction := transactionFor(t, gateway)
		challenge, err := transaction.Challenge(t.Context(), "2020123456")
		if err != nil {
			t.Fatalf("Challenge: %v", err)
		}
		if _, err := transaction.Login(t.Context(),
			Credentials{Username: "2020123456", Password: "pw12345678"},
			shape, challenge); err != nil {
			t.Fatalf("Login: %v", err)
		}
		return gateway.lastQuery(t, portalPath).Get("info")
	}

	common := infoWith(Shape{})
	configured := infoWith(Shape{Alphabet: custom})

	if common == "" || configured == "" {
		t.Fatal("the login carried no info blob")
	}
	if common == configured {
		t.Fatal("the configured alphabet did not change the info blob; " +
			"the field is plumbed but not read")
	}

	// Both must keep the marker and differ only after it: the prefix is not
	// part of what the alphabet encodes.
	const marker = "{" + srun.DefaultInfoPrefix + "}"
	if !strings.HasPrefix(common, marker) || !strings.HasPrefix(configured, marker) {
		t.Fatalf("info lost its marker: %q / %q", common, configured)
	}

	// The payload the custom table produced must be exactly what that table
	// produces for the same bytes -- not merely "something else".
	table, err := srun.NewAlphabet(custom)
	if err != nil {
		t.Fatalf("NewAlphabet: %v", err)
	}
	plain := strings.TrimPrefix(common, marker)
	raw := decodeWith(t, srun.DefaultAlphabetTable, plain)
	if want := marker + table.Encode(raw); want != configured {
		t.Errorf("info = %q, want %q re-encoded under the configured table",
			configured, want)
	}
}

// A malformed table fails the login instead of quietly using the common one.
//
// Falling back would encrypt the credentials under a table the gateway does not
// use. The blob is encrypted and checksummed, so the gateway cannot say what
// went wrong: the user would be told their password is wrong.
func TestAMalformedAlphabetFailsTheLoginRatherThanFallingBack(t *testing.T) {
	for name, table := range map[string]string{
		"short":     "ABC",
		"repeated":  strings.Repeat("A", srun.AlphabetSize),
		"padding":   "=" + srun.StandardAlphabetTable[1:],
		"nonascii":  "√" + srun.StandardAlphabetTable[3:],
		"withspace": " " + srun.StandardAlphabetTable[1:],
	} {
		t.Run(name, func(t *testing.T) {
			gateway := newFakeGateway(t)
			transaction := transactionFor(t, gateway)
			challenge, _ := transaction.Challenge(t.Context(), "2020123456")

			_, err := transaction.Login(t.Context(),
				Credentials{Username: "2020123456", Password: "pw12345678"},
				Shape{Alphabet: table}, challenge)
			if err == nil {
				t.Fatal("a malformed alphabet was accepted")
			}
			for _, request := range gateway.seen() {
				if request.path == portalPath {
					t.Error("credentials were sent before the table was refused")
					break
				}
			}
		})
	}
}

// decodeWith reverses this project's encoder for one table, so the test can
// compare payloads rather than trusting the encoder to check itself.
func decodeWith(t *testing.T, table, encoded string) []byte {
	t.Helper()
	index := map[byte]int{}
	for position := 0; position < len(table); position++ {
		index[table[position]] = position
	}
	var bits, count uint
	var out []byte
	for position := 0; position < len(encoded); position++ {
		symbol := encoded[position]
		if symbol == srun.PadChar {
			break
		}
		value, ok := index[symbol]
		if !ok {
			t.Fatalf("byte %q is not in the table", string(symbol))
		}
		bits = bits<<6 | uint(value)
		count += 6
		if count >= 8 {
			count -= 8
			out = append(out, byte(bits>>count))
		}
	}
	return out
}
