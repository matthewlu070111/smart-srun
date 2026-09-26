// Package srun is the SRun authentication protocol's byte layer.
//
// Everything here is a pure function over bytes. Nothing reads the clock,
// touches the network or opens a file: timestamps and callback names arrive as
// arguments. That is what makes the wire format testable against fixed vectors
// instead of against a campus gateway.
//
// The byte sequences are fixed by spec 04 and cross-checked two ways: vectors
// generated from the frozen baseline Python, and independent bases in the tests
// (RFC 2202/3174 digests, the standard Base64 alphabet, and an XXTEA decrypt
// written from the published algorithm that has to recover the plaintext).
package srun

import (
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// PadChar is the padding byte. SRun keeps standard Base64 padding even though
// the table is custom.
const PadChar = '='

// AlphabetSize is how many bytes a table has: one per six-bit group. Named so
// the configuration layer can bound the field without restating 64.
const AlphabetSize = 64

// DefaultAlphabetTable is the table the baseline shipped. A school may use a
// different one; the algorithm does not change with it.
const DefaultAlphabetTable = "LVoJPiCN2R8G90yg+hmFHuacZ1OWMnrsSTXkYpUq/3dlbfKwv6xztjI7DeBE45QA"

// StandardAlphabetTable is RFC 4648's table.
//
// It is here so the tests can run this encoder with it and compare against
// encoding/base64: an encoder that agrees with the standard library on the
// standard table, and with the baseline vectors on the SRun table, is doing
// Base64 rather than something that merely matches one set of expectations.
const StandardAlphabetTable = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// Alphabet is a validated 64-byte Base64 table.
type Alphabet struct {
	table [64]byte
}

// DefaultAlphabet is the baseline table, validated at startup.
var DefaultAlphabet = mustAlphabet(DefaultAlphabetTable)

func mustAlphabet(table string) *Alphabet {
	alphabet, err := NewAlphabet(table)
	if err != nil {
		panic(err.Error())
	}
	return alphabet
}

// NewAlphabet validates a table and returns an encoder for it.
//
// The rules are what makes the encoding reversible and the output usable in a
// query string: exactly 64 bytes, all distinct, all printable ASCII, and none
// of them the padding byte. A table with a repeat would map two different
// six-bit groups to the same character, so the gateway could not decode what it
// was sent -- and it would do so silently, which is why this is refused up
// front rather than discovered as an authentication failure.
func NewAlphabet(table string) (*Alphabet, error) {
	if len(table) != AlphabetSize {
		return nil, domain.Errorf(domain.CodeInvalidConfig,
			"Base64 字母表必须是 %d 个字节，收到 %d 个", AlphabetSize, len(table))
	}

	var alphabet Alphabet
	var seen [256]bool
	for index := range AlphabetSize {
		symbol := table[index]
		switch {
		case symbol < 0x21 || symbol > 0x7e:
			return nil, domain.Errorf(domain.CodeInvalidConfig,
				"Base64 字母表第 %d 位不是可见 ASCII 字符", index)
		case symbol == PadChar:
			return nil, domain.Errorf(domain.CodeInvalidConfig,
				"Base64 字母表不能包含填充字符 %q", string(PadChar))
		case seen[symbol]:
			return nil, domain.Errorf(domain.CodeInvalidConfig,
				"Base64 字母表中 %q 重复出现", string(symbol))
		}
		seen[symbol] = true
		alphabet.table[index] = symbol
	}
	return &alphabet, nil
}

// Table returns the alphabet as a string.
func (a *Alphabet) Table() string { return string(a.table[:]) }

// Encode encodes src with this alphabet.
//
// Written out rather than delegated to encoding/base64 with a custom encoding
// because the two must be shown to agree, and using one to implement the other
// would make that comparison prove nothing.
func (a *Alphabet) Encode(src []byte) string {
	if len(src) == 0 {
		return ""
	}

	full := len(src) - len(src)%3
	var out strings.Builder
	out.Grow((len(src)+2)/3*4 + 1)

	for index := 0; index < full; index += 3 {
		group := uint32(src[index])<<16 | uint32(src[index+1])<<8 | uint32(src[index+2])
		out.WriteByte(a.table[group>>18])
		out.WriteByte(a.table[group>>12&63])
		out.WriteByte(a.table[group>>6&63])
		out.WriteByte(a.table[group&63])
	}

	switch len(src) - full {
	case 1:
		group := uint32(src[full]) << 16
		out.WriteByte(a.table[group>>18])
		out.WriteByte(a.table[group>>12&63])
		out.WriteByte(PadChar)
		out.WriteByte(PadChar)
	case 2:
		group := uint32(src[full])<<16 | uint32(src[full+1])<<8
		out.WriteByte(a.table[group>>18])
		out.WriteByte(a.table[group>>12&63])
		out.WriteByte(a.table[group>>6&63])
		out.WriteByte(PadChar)
	}
	return out.String()
}
