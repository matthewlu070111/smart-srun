// Package portal reads what a campus gateway's own pages say about themselves.
//
// It exists because a user cannot be expected to know their AC_ID, and because
// the alternative -- guessing, or asking a catalogue what some other campus
// uses -- produces a configuration that fails at the gateway with no clue why.
// Everything here is read-only and credential-free: nothing in this package
// sends a password, and nothing it finds is saved without the user seeing it.
package portal

import (
	htmlstd "html"
	"net/url"
	"regexp"
	"strings"
)

// MaxRedirects bounds one probe's chain, at the baseline's figure. A portal
// that has not arrived after eight hops is not going to.
const MaxRedirects = 8

// Where an AC_ID was found. It travels with the value because "the login page
// had it in a hidden field" and "it was in a query string we were redirected
// to" are different amounts of evidence, and the wizard says which.
const (
	SourceURL      = "url"
	SourceHTML     = "html"
	SourceRedirect = "redirect_url"
)

// acidPattern is what an AC_ID may look like. Anything else is something else:
// a whole query string, a fragment of HTML, a sentence.
var acidPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidACID returns the value if it could be an AC_ID, and "" otherwise.
func ValidACID(value string) string {
	text := strings.TrimSpace(value)
	if text == "" || len(text) > 64 || !acidPattern.MatchString(text) {
		return ""
	}
	return text
}

// ACIDFromURL reads ac_id or acid out of a query string.
func ACIDFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	query := parsed.Query()
	for _, key := range []string{"ac_id", "acid"} {
		if values := query[key]; len(values) > 0 {
			if acid := ValidACID(values[0]); acid != "" {
				return acid
			}
		}
	}
	return ""
}

// htmlACID are the shapes an AC_ID takes on a real SRun page, in the order the
// baseline tried them: the hidden input first, because that is the one the
// page itself would submit, and the bare assignment last, because it is the
// weakest match.
var htmlACID = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<input[^>]+name=["']ac_id["'][^>]*value=["']([^"']+)["']`),
	regexp.MustCompile(`(?is)<input[^>]+value=["']([^"']+)["'][^>]*name=["']ac_id["']`),
	regexp.MustCompile(`(?is)["']ac_id["'][^<>]{0,120}?value=["']([^"']+)["']`),
	regexp.MustCompile(`(?is)(?:^|[\s{,>])["']?ac_id["']?\s*[:=]\s*["']?([A-Za-z0-9_.-]+)`),
	regexp.MustCompile(`(?is)[?&]ac_id=([A-Za-z0-9_.-]+)`),
}

// ACIDFromHTML reads an AC_ID out of a portal page.
//
// It takes bytes, not a string, because a portal in this part of the world is
// as likely to be GBK as UTF-8. Every pattern here matches ASCII, so the page's
// encoding does not change the answer -- and decoding it first would mean
// carrying a charset dependency for a value that is always digits.
func ACIDFromHTML(body []byte) string {
	return acidFromPatterns(body, htmlACID)
}

func acidFromPatterns(body []byte, patterns []*regexp.Regexp) string {
	for _, pattern := range patterns {
		if match := pattern.FindSubmatch(body); match != nil {
			if acid := ValidACID(htmlstd.UnescapeString(string(match[1]))); acid != "" {
				return acid
			}
		}
	}
	return ""
}

// htmlRedirect are the ways a portal moves a browser without a Location header.
var htmlRedirect = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<script[^>]*>\s*top\.self\.location\.href\s*=\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?is)\blocation\.href\s*=\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?is)<meta[^>]+http-equiv=["']?refresh["']?[^>]+content=["'][^"']*url=([^"']+)["']`),
}

// RedirectFromHTML finds where a page sends a browser next.
//
// Portals that answer 200 with a script or a meta refresh are common enough
// that following only Location headers finds nothing on them. What is returned
// is whatever the page said, not a decision to go there: the caller joins it
// against the current URL and applies its own limits.
func RedirectFromHTML(body []byte) string {
	for _, pattern := range htmlRedirect {
		if match := pattern.FindSubmatch(body); match != nil {
			return strings.TrimSpace(htmlstd.UnescapeString(string(match[1])))
		}
	}
	return ""
}

// Join resolves a location against the page it came from.
func Join(base, location string) string {
	target := strings.TrimSpace(location)
	if target == "" {
		return ""
	}
	parsedBase, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return parsedBase.ResolveReference(parsed).String()
}

// Address accepts only an address a probe may fetch, keeping its path.
//
// The scheme check is the point: a portal that redirects to javascript:, data:
// or file: is either broken or hostile, and "the client refused it" is not the
// same as "we never asked". A bare host is assumed to be http, because that is
// what a user types when the wizard asks for a login page.
func Address(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" || len(text) > 2048 {
		return ""
	}
	if !strings.Contains(text, "://") {
		text = "http://" + text
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	// A read-only probe must not turn a supplied or redirected URL into HTTP
	// Basic authentication, a credential-bearing query, or a login/logout call.
	for key, values := range parsed.Query() {
		switch strings.ToLower(key) {
		case "password", "passwd", "pwd", "token", "access_token", "username", "user_id", "hmd5", "chksum", "info":
			return ""
		case "action":
			for _, value := range values {
				if strings.EqualFold(value, "login") || strings.EqualFold(value, "logout") {
					return ""
				}
			}
		}
	}
	parsed.Fragment = ""
	return parsed.String()
}

// Origin reduces a portal address to the scheme and host an account stores.
//
// The path is deliberately dropped: base_url is where the gateway's API lives,
// and a login page's path ("/srun_portal_pc?ac_id=1") is not it. Keeping the
// path is how an account ends up POSTing to a URL that answers with HTML.
func Origin(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if !strings.Contains(text, "://") {
		text = "http://" + text
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}
