package srun

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// This file is the second basis spec 04 requires. Nothing in it comes from the
// baseline Python: if the vectors and the implementation were wrong in the same
// way, these are the tests that would still fail.

// Running this encoder with RFC 4648's table has to produce exactly what
// encoding/base64 produces. Agreeing with the standard library on the standard
// table, and with the baseline vectors on the SRun table, is what says this is
// Base64 with a substituted alphabet rather than something that merely matches
// one set of expectations.
func TestStandardAlphabetAgreesWithEncodingBase64(t *testing.T) {
	standard, err := NewAlphabet(StandardAlphabetTable)
	if err != nil {
		t.Fatalf("standard alphabet: %v", err)
	}

	inputs := [][]byte{
		nil, {}, {0x00}, {0xff}, {'a'}, {'a', 'b'}, {'a', 'b', 'c'},
		{'a', 'b', 'c', 'd'}, {'a', 'b', 'c', 'd', 'e'},
		[]byte("the quick brown fox jumps over the lazy dog"),
		{0x00, 0x10, 0x83, 0x10, 0x51, 0x87, 0x20, 0x92, 0x8b},
	}
	for length := range 300 {
		filler := make([]byte, length)
		for index := range filler {
			filler[index] = byte(index * 7)
		}
		inputs = append(inputs, filler)
	}

	for _, input := range inputs {
		want := base64.StdEncoding.EncodeToString(input)
		if got := standard.Encode(input); got != want {
			t.Fatalf("input %s:\n got %s\nwant %s", hex.EncodeToString(input), got, want)
		}
	}
}

// The SRun table is the standard table with the symbols permuted, so the two
// outputs must be the same length and differ only by that substitution. This
// catches a table loaded in the wrong order, which the vectors alone would not
// distinguish from a wrong encoder.
func TestTheSRunTableIsAPermutationOfTheStandardOne(t *testing.T) {
	if len(DefaultAlphabetTable) != len(StandardAlphabetTable) {
		t.Fatal("the two tables are different lengths")
	}
	standard := []byte(StandardAlphabetTable)
	custom := []byte(DefaultAlphabetTable)

	var standardSeen, customSeen [256]bool
	for index := range standard {
		standardSeen[standard[index]] = true
		customSeen[custom[index]] = true
	}
	if standardSeen != customSeen {
		t.Fatal("the SRun table is not a permutation of the standard one")
	}

	payload := []byte("2020123456:pw123456")
	standardEncoding, _ := NewAlphabet(StandardAlphabetTable)
	srunOut := DefaultAlphabet.Encode(payload)
	standardOut := standardEncoding.Encode(payload)
	if len(srunOut) != len(standardOut) {
		t.Fatalf("lengths differ: %d vs %d", len(srunOut), len(standardOut))
	}
	for index := range srunOut {
		if standardOut[index] == PadChar {
			if srunOut[index] != PadChar {
				t.Fatalf("padding at %d was substituted", index)
			}
			continue
		}
		position := bytes.IndexByte(standard, standardOut[index])
		if srunOut[index] != custom[position] {
			t.Fatalf("at %d: standard %q maps to %q, got %q",
				index, standardOut[index], custom[position], srunOut[index])
		}
	}
}

// RFC 2202 section 2, HMAC-MD5. Note the argument order: the token is the key.
func TestHMACMD5AgainstRFC2202(t *testing.T) {
	cases := []struct {
		key, data, want string
	}{
		{string(bytes.Repeat([]byte{0x0b}, 16)), "Hi There",
			"9294727a3638bb1c13f48ef8158bfc9d"},
		{"Jefe", "what do ya want for nothing?",
			"750c783e6ab0b503eaa86e310a5db738"},
		{string(bytes.Repeat([]byte{0xaa}, 16)), string(bytes.Repeat([]byte{0xdd}, 50)),
			"56be34521d144c88dbb8c733f0e8b3f6"},
		{string(bytes.Repeat([]byte{0x0c}, 16)), "Test With Truncation",
			"56461ef2342edc00f9bab995690efd4c"},
		{string(bytes.Repeat([]byte{0xaa}, 80)),
			"Test Using Larger Than Block-Size Key - Hash Key First",
			"6b1ab7fe4bd7bf8f0b62e6ce61b9d0cd"},
	}

	for _, testCase := range cases {
		if got := HMACMD5Hex(testCase.key, testCase.data); got != testCase.want {
			t.Errorf("HMAC-MD5(%q)\n got %s\nwant %s", testCase.data, got, testCase.want)
		}
	}
}

// RFC 3174 section 7.3 and the classic NIST examples.
func TestSHA1AgainstPublishedVectors(t *testing.T) {
	cases := []struct{ input, want string }{
		{"", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
		{"abc", "a9993e364706816aba3e25717850c26c9cd0d89d"},
		{"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
			"84983e441c3bd26ebaae4aa1f95129e5e54670f1"},
		{string(bytes.Repeat([]byte("a"), 1000000)),
			"34aa973cd4c4daa4f61eeb2bdbad27316534016f"},
	}

	for _, testCase := range cases {
		label := testCase.input
		if len(label) > 24 {
			label = label[:24] + "..."
		}
		if got := SHA1Hex(testCase.input); got != testCase.want {
			t.Errorf("SHA1(%q)\n got %s\nwant %s", label, got, testCase.want)
		}
	}
}

// xencodeDecrypt inverts Xencode.
//
// Written from the algorithm rather than from the Python: the encryption runs
// its rounds forward, so this runs them backward, recovering each word with the
// same y and z the forward pass used. It exists only in the tests -- nothing at
// runtime decrypts -- and its job is to show that Xencode is an invertible
// cipher rather than a function that happens to reproduce a fixture.
func xencodeDecrypt(cipher, key []byte) []byte {
	if len(cipher) == 0 || len(cipher)%4 != 0 {
		return nil
	}
	block := make([]uint32, len(cipher)/4)
	for index := range block {
		base := index * 4
		block[index] = uint32(cipher[base]) | uint32(cipher[base+1])<<8 |
			uint32(cipher[base+2])<<16 | uint32(cipher[base+3])<<24
	}

	keyBlock := packWords(key, false)
	for len(keyBlock) < 4 {
		keyBlock = append(keyBlock, 0)
	}

	last := len(block) - 1
	if last < 1 {
		return nil
	}
	rounds := 6 + 52/(last+1)
	sum := uint32(rounds) * delta

	for ; rounds > 0; rounds-- {
		e := sum >> 2 & 3

		// The forward pass updated block[last] last, using the already-updated
		// block[0] and block[last-1]; both still hold those values here.
		block[last] -= mix(block[0], block[last-1], sum, keyBlock[last&3^int(e)])
		for position := last - 1; position >= 1; position-- {
			// block[position+1] has just been restored to its pre-round value,
			// which is what the forward pass read; block[position-1] is still
			// post-update, which is also what it read.
			block[position] -= mix(block[position+1], block[position-1], sum,
				keyBlock[position&3^int(e)])
		}
		block[0] -= mix(block[1], block[last], sum, keyBlock[int(e)])
		sum -= delta
	}
	return unpackWords(block)
}

// If encrypting then decrypting returns the message, the forward direction is a
// real XXTEA-shaped cipher and not, say, a permutation that loses a bit.
func TestXencodeRoundTripsThroughAnIndependentDecrypt(t *testing.T) {
	keys := []string{"", "k", "key", "keyx", "0123456789abcdef", "a-much-longer-token-value"}
	messages := []string{
		"a", "ab", "abc", "abcd", "abcde", "abcdefgh",
		"{\"username\":\"2020123456\",\"password\":\"pw\"}",
		string(bytes.Repeat([]byte{0xff}, 8)),
		string(bytes.Repeat([]byte("x"), 257)),
	}

	for _, key := range keys {
		for _, message := range messages {
			cipher := Xencode([]byte(message), []byte(key))
			plain := xencodeDecrypt(cipher, []byte(key))
			if plain == nil {
				t.Fatalf("key %q message %q: decrypt refused the ciphertext", key, message)
			}

			// The last word is the byte length the encoder appended; the words
			// before it are the padded message.
			if len(plain) < 4 {
				t.Fatalf("key %q message %q: ciphertext too short", key, message)
			}
			body := plain[:len(plain)-4]
			lengthWord := plain[len(plain)-4:]
			length := int(uint32(lengthWord[0]) | uint32(lengthWord[1])<<8 |
				uint32(lengthWord[2])<<16 | uint32(lengthWord[3])<<24)

			if length != len(message) {
				t.Fatalf("key %q message %q: recovered length %d, want %d",
					key, message, length, len(message))
			}
			if got := string(body[:length]); got != message {
				t.Fatalf("key %q: recovered %q, want %q", key, got, message)
			}
		}
	}
}

// SRun's round function is not textbook XXTEA.
//
// Textbook MX is ((A+B) ^ (C+D)); the obfuscated SRun JavaScript computes
// A + (B^C) + D. Someone reaching for a published XXTEA implementation, or
// "correcting" mix() to match the paper, would produce a blob the gateway
// rejects with no clue why. This asserts the difference so that change cannot
// pass as a cleanup.
// A nil alphabet means the baseline table rather than a crash.
//
// This is the one call that carries credentials, and it is reached from a
// daemon. Panicking there because a caller left an argument unset would take
// the service down for a mistake that has an obvious right answer.
func TestANilAlphabetUsesTheBaselineTable(t *testing.T) {
	const (
		prefix = "SRBX1"
		body   = `{"username":"u","password":"p","ip":"10.0.0.1","acid":"1",` +
			`"enc_ver":"srun_bx1"}`
		token = "tok"
	)
	if got, want := EncryptedInfo(prefix, body, token, nil),
		EncryptedInfo(prefix, body, token, DefaultAlphabet); got != want {
		t.Errorf("a nil alphabet produced %q, want the baseline table's %q",
			got, want)
	}
}

func TestTheRoundFunctionIsNotTextbookXXTEA(t *testing.T) {
	// Typed as uint32, not left as untyped constants: those default to int, and
	// this expression overflows a 32-bit int -- which is the same mistake this
	// file exists to keep out of the algorithm itself.
	var y, z, sum, keyWord uint32 = 0x12345678, 0x9abcdef0, 0x9e3779b9, 0x0f0f0f0f

	textbook := ((z>>5 ^ y<<2) + (y>>3 ^ z<<4)) ^ ((sum ^ y) + (keyWord ^ z))
	if got := mix(y, z, sum, keyWord); got == textbook {
		t.Fatal("mix() now matches textbook XXTEA; the SRun front-end does not " +
			"compute it that way, so this would break every login")
	}
}
