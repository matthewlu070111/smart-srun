package srun

import (
	// MD5 and SHA-1 are both broken as general-purpose hashes. They are here
	// because the SRun gateway computes these exact digests and compares them;
	// this is protocol compatibility, not a security choice, and swapping in a
	// stronger hash would simply fail to authenticate.
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
)

// HMACMD5Hex is the login password digest: HMAC-MD5 keyed with the challenge
// token, over the password, in lowercase hex.
//
// The argument order is (token, password) because that is key-then-message.
// The baseline's helper took them the other way round, which is an easy way to
// produce a digest that looks fine and never authenticates.
func HMACMD5Hex(token, password string) string {
	mac := hmac.New(md5.New, []byte(token))
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

// SHA1Hex is the checksum and signature digest, lowercase hex over the UTF-8
// bytes of value.
func SHA1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
