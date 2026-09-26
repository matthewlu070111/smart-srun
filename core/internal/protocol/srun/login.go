package srun

import (
	"strconv"
	"strings"
)

// DefaultInfoPrefix is the marker the gateway looks for in front of the
// encrypted blob.
const DefaultInfoPrefix = "SRBX1"

// LogoutUnbind is the fixed unbind value. It is a constant because the same
// literal has to appear in the request and inside the signed string; two
// separate literals would let them drift apart, and the gateway would reject
// the signature with no explanation.
const LogoutUnbind = "1"

// WirePasswordPrefix marks the login password field as a digest rather than a
// password. It is not part of the checksum input.
const WirePasswordPrefix = "{MD5}"

// NormalizePrefix reduces a configured info prefix to the bare token.
//
// Users paste the prefix both ways -- SRBX1 and {SRBX1} -- and the braces are
// added when the blob is built. Stripping repeatedly, rather than once, is what
// guarantees the property spec 04 actually asks for: the result can never
// produce a doubled brace. A prefix that still contains a brace after that, or
// is empty, falls back: a stray brace inside the marker would corrupt the field
// for the gateway. Reporting a malformed prefix to the user belongs to config
// validation, which sees it before it ever gets here.
func NormalizePrefix(raw, fallback string) string {
	text := strings.TrimSpace(raw)
	for len(text) > 2 && strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}") {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	if text == "" || strings.ContainsAny(text, "{}") {
		return fallback
	}
	return text
}

// EncryptedInfo builds the info field: the marker, then the credential object
// encrypted under the challenge token and encoded with the school's alphabet.
//
// The prefix is normalised here so there is one place that does it and no
// caller can forget; normalising an already-normalised prefix changes nothing.
//
// A nil alphabet means the baseline table. It is the table almost every school
// uses, and the alternative was a panic on the one path that carries
// credentials -- a daemon crashing mid-login is a worse answer to "the caller
// did not pass an alphabet" than quietly using the one it would have chosen.
func EncryptedInfo(prefix, infoJSON, token string, alphabet *Alphabet) string {
	if alphabet == nil {
		alphabet = DefaultAlphabet
	}
	marker := NormalizePrefix(prefix, DefaultInfoPrefix)
	return "{" + marker + "}" + alphabet.Encode(Xencode([]byte(infoJSON), []byte(token)))
}

// ChecksumInput is the string the login checksum is taken over.
//
// The token is interleaved before every field; that repetition is the format,
// not a mistake. The password digest goes in bare: prefixing it with {MD5}
// here, as the wire field does, produces a checksum the gateway rejects.
func ChecksumInput(token, username, hmd5, acID, ip, n, loginType, info string) string {
	var out strings.Builder
	out.Grow(len(token)*7 + len(username) + len(hmd5) + len(acID) +
		len(ip) + len(n) + len(loginType) + len(info))
	for _, field := range []string{username, hmd5, acID, ip, n, loginType, info} {
		out.WriteString(token)
		out.WriteString(field)
	}
	return out.String()
}

// Checksum is the SHA-1 of ChecksumInput.
func Checksum(token, username, hmd5, acID, ip, n, loginType, info string) string {
	return SHA1Hex(ChecksumInput(token, username, hmd5, acID, ip, n, loginType, info))
}

// LogoutSignInput is the string the logout signature is taken over.
//
// The timestamp appears at both ends. It is in whole seconds, while the login
// callback and cache-buster are in milliseconds -- passing one where the other
// belongs produces a signature the gateway rejects, so the parameter is typed
// as seconds and named for it.
func LogoutSignInput(timeSeconds int64, username, ip string) string {
	stamp := strconv.FormatInt(timeSeconds, 10)
	return stamp + username + ip + LogoutUnbind + stamp
}

// LogoutSign is the SHA-1 of LogoutSignInput.
func LogoutSign(timeSeconds int64, username, ip string) string {
	return SHA1Hex(LogoutSignInput(timeSeconds, username, ip))
}
