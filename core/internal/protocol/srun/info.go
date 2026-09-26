package srun

import (
	"strings"
	"unicode/utf8"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Info is the credential object the gateway expects inside the encrypted blob.
//
// A struct with fixed fields, not a map: the field order is part of the wire
// format, and Go randomises map iteration. It is also not encoded with
// encoding/json, for the reasons on EncodeInfo.
type Info struct {
	Username string
	Password string
	IP       string
	ACID     string
	EncVer   string
}

// EncodeInfo renders the info object exactly as the gateway expects it.
//
// This is hand-written because encoding/json cannot produce these bytes:
//
//   - It escapes the HTML-significant bytes < > and & into six-byte
//     backslash-u escapes. The protocol does not, so a password containing any
//     of them would encrypt to different bytes and the checksum would not match
//     what the gateway computes.
//   - It emits non-ASCII as UTF-8. Spec 04 fixes the baseline behaviour of
//     escaping to lowercase four-hex-digit escapes, using a surrogate pair for
//     anything above the BMP.
//
// The short control escapes and the treatment of U+0000 do match, as of the Go
// version this is built with -- but they are not guaranteed to, and the two
// differences above are enough on their own.
//
// None of those differences would fail a JSON parser, which is exactly the
// danger: the blob is encrypted and checksummed, so the first symptom would be
// a login rejected by the gateway for no visible reason.
func EncodeInfo(info Info) (string, error) {
	var out strings.Builder
	out.Grow(96 + len(info.Username) + len(info.Password) + len(info.IP))

	out.WriteString(`{"username":`)
	if err := appendJSONString(&out, info.Username, "username"); err != nil {
		return "", err
	}
	out.WriteString(`,"password":`)
	if err := appendJSONString(&out, info.Password, "password"); err != nil {
		return "", err
	}
	out.WriteString(`,"ip":`)
	if err := appendJSONString(&out, info.IP, "ip"); err != nil {
		return "", err
	}
	out.WriteString(`,"acid":`)
	if err := appendJSONString(&out, info.ACID, "acid"); err != nil {
		return "", err
	}
	out.WriteString(`,"enc_ver":`)
	if err := appendJSONString(&out, info.EncVer, "enc_ver"); err != nil {
		return "", err
	}
	out.WriteByte('}')
	return out.String(), nil
}

const hexDigits = "0123456789abcdef"

// appendJSONString writes one JSON string, ASCII-only.
//
// field names the offending value in the error without quoting it: this runs on
// passwords, so the value itself must not reach a message.
func appendJSONString(out *strings.Builder, value, field string) error {
	out.WriteByte('"')
	for index := 0; index < len(value); {
		symbol, size := utf8.DecodeRuneInString(value[index:])
		// A real U+FFFD decodes with size 3, so this catches only bytes that
		// are not valid UTF-8. Letting them through as U+FFFD would silently
		// change a password on its way to the gateway.
		if symbol == utf8.RuneError && size <= 1 {
			return domain.FieldErrorf(domain.CodeInvalidArgument, field,
				"包含无效的 UTF-8 字节，无法编码为认证参数")
		}
		index += size

		switch symbol {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			switch {
			// Printable ASCII goes through untouched, including < > & / and the
			// space, which is what the protocol expects.
			case symbol >= 0x20 && symbol <= 0x7e:
				out.WriteByte(byte(symbol))
			case symbol < 0x10000:
				appendUnicodeEscape(out, uint16(symbol))
			default:
				// Above the BMP: the escape form only has four hex digits, so
				// the code point goes out as the surrogate pair that UTF-16
				// would use for it.
				offset := symbol - 0x10000
				appendUnicodeEscape(out, uint16(0xd800+(offset>>10&0x3ff)))
				appendUnicodeEscape(out, uint16(0xdc00+(offset&0x3ff)))
			}
		}
	}
	out.WriteByte('"')
	return nil
}

func appendUnicodeEscape(out *strings.Builder, value uint16) {
	out.WriteString(`\u`)
	out.WriteByte(hexDigits[value>>12&0xf])
	out.WriteByte(hexDigits[value>>8&0xf])
	out.WriteByte(hexDigits[value>>4&0xf])
	out.WriteByte(hexDigits[value&0xf])
}
