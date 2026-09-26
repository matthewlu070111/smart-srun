// Package logstore is the structured event log: what happened, in one bounded
// place, in a format the interface already knows how to read.
//
// The line format is not ours to choose. The shipped LuCI page parses
// "[ts] LEVEL event k=v ... | message" and renders it in Chinese, and that
// parser is frozen -- so this package's job is to produce exactly those lines,
// redact anything that must never reach a log, and stay inside its size budget
// on a device whose storage is tmpfs.
package logstore

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Redacted is what a secret's value becomes. The key survives, so a reader can
// still see that a password was involved without learning it.
const Redacted = "***"

// sensitive matches by substring, not by exact name.
//
// Substring on purpose: the fields that carry secrets are named by whoever adds
// them -- campus_key, hotspot_key, wifi_key, hmd5, chksum -- and a whitelist of
// exact names is a list somebody will forget to extend. The cost of a false
// positive is a redacted value in a log; the cost of a false negative is a
// credential on disk.
// "key" alone covers campus_key, hotspot_key, wifi_key and private_key, which
// the baseline listed one at a time; the price is that an idempotency key is
// hidden too, and nothing needs to read one out of a log.
var sensitive = []string{
	"password", "passwd", "secret", "token", "chksum", "checksum", "hmd5",
	"credential", "authorization", "cookie", "session", "key", "psk",
}

// IsSensitive reports whether a field name must have its value hidden.
func IsSensitive(key string) bool {
	name := strings.ToLower(strings.TrimSpace(key))
	for _, part := range sensitive {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

// Bounds for one line's parts.
//
// A log is a bounded store; a single record that ignored that would be a way to
// push everything else out of it. Values are clipped rather than rejected,
// because the point of the line is what happened, not the whole of a message
// somebody else's library wrote.
const (
	MaxEventBytes   = 64
	MaxKeyBytes     = 64
	MaxValueBytes   = 512
	MaxMessageBytes = 1024
	// Ellipsis marks a clipped value, so a truncated URL is not mistaken for a
	// short one.
	Ellipsis = "…"
)

// clip cuts on a rune boundary, so a clipped value is still valid UTF-8 and the
// page that renders it does not show a replacement character.
func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + Ellipsis
}

// Field is one key/value pair. A slice of these rather than a map: the order a
// line reads in is part of what makes it readable, and a map would shuffle it.
type Field struct {
	Key   string
	Value string
}

// F builds a field. Short because call sites carry several.
func F(key, value string) Field { return Field{Key: key, Value: value} }

// Record is one event.
type Record struct {
	At      time.Time
	Level   domain.LogLevel
	Event   string
	Fields  []Field
	Message string
}

// Line renders the record in the frozen format.
//
// The timestamp is Beijing time, like the rest of this program's user-facing
// times: a router whose clock is set to UTC would otherwise print a log an hour
// away from the quiet hours the same user configured.
func (r Record) Line() string {
	var builder strings.Builder
	builder.WriteString("[")
	builder.WriteString(r.At.In(domain.Beijing).Format("2006-01-02 15:04:05"))
	builder.WriteString("] ")
	builder.WriteString(string(r.Level))
	builder.WriteString(" ")
	builder.WriteString(escapeText(clip(r.Event, MaxEventBytes)))

	for _, field := range r.Fields {
		builder.WriteString(" ")
		builder.WriteString(escapeText(clip(field.Key, MaxKeyBytes)))
		builder.WriteString("=")
		builder.WriteString(formatValue(field.Key, clip(field.Value, MaxValueBytes)))
	}
	if r.Message != "" {
		builder.WriteString(" | ")
		builder.WriteString(escapeText(clip(r.Message, MaxMessageBytes)))
	}
	return builder.String()
}

// escapeText keeps one record on one line.
//
// A newline inside a message would split it into two records, the second of
// which has no timestamp and would be rendered as raw text; a tab or carriage
// return would do the same to a terminal. The backslash is escaped first, so
// the escapes this adds cannot be confused with a backslash the value had.
func escapeText(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\", "\r", "\\r", "\n", "\\n", "\t", "\\t")
	return replacer.Replace(value)
}

// formatValue redacts, escapes and quotes in that order.
//
// Redaction first, so no transformation can be applied to a secret and leak its
// length or shape; quoting last, so a value with a space stays one field.
func formatValue(key, value string) string {
	if IsSensitive(key) {
		return Redacted
	}
	text := escapeText(value)
	if text == "" || strings.ContainsAny(text, " \"") {
		return "\"" + strings.ReplaceAll(text, "\"", "\\\"") + "\""
	}
	return text
}
