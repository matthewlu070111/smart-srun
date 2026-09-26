package presets

import (
	"regexp"
	"strings"
)

// NormalizeBaseURL reduces a contributor's base URL to scheme and authority.
//
// Everything after the host is dropped on purpose. The value is a gateway's
// address, and the paths this program appends to it are fixed by the protocol
// -- /cgi-bin/srun_portal and the rest. A preset carrying a path would produce
// requests to /portal/cgi-bin/srun_portal, which the gateway answers with its
// login page rather than an error, so the failure would be reported as a
// protocol problem rather than a wrong address.
//
// A value with no scheme gets http, which is what campus gateways almost
// always are and what the baseline assumes. It is not a security decision this
// package is entitled to make either way: the address comes from a catalogue,
// and TLS to a gateway that does not speak it fails closed.
var baseURLPattern = regexp.MustCompile(`^(https?)://([^/%?#]+)`)
var schemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
var hostPattern = regexp.MustCompile(`^([^/%?#]+)`)

func NormalizeBaseURL(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	if !strings.Contains(text, "://") {
		text = "http://" + text
	}

	if match := baseURLPattern.FindStringSubmatch(text); match != nil {
		return match[1] + "://" + match[2]
	}
	if !schemePattern.MatchString(text) {
		if host := hostPattern.FindStringSubmatch(text); host != nil && host[1] != "" {
			return "http://" + host[1]
		}
	}
	// Some other scheme entirely. Left as it was apart from a trailing slash,
	// so a catalogue that starts carrying one does not have its value quietly
	// rewritten into something this package invented.
	return strings.TrimRight(text, "/")
}
