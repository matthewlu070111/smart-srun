// Package presets reads the school preset catalogue.
//
// A preset is what lets somebody pick their university from a list instead of
// typing a base URL, an ac_id and a login shape they have no way to know. The
// catalogue is published remotely, cached locally, and shipped as a fallback,
// and this package turns any of those into the same values.
//
// It decides nothing about the network. Fetching is a consumer-defined
// interface the caller supplies, for the same reason auth takes a Line: the
// rules here -- what a valid payload is, which source wins, when a cache may be
// replaced -- have to be testable without one.
package presets

import (
	"encoding/json"
	"strconv"
	"strings"
)

// SchemaVersion is the only catalogue version this build understands.
//
// A payload announcing anything else is not read at all. The alternative --
// reading the fields that happen to look familiar -- is how a future format
// gets half-applied by an old build.
const SchemaVersion = 1

// Status is where a preset is in its life.
type Status string

const (
	// StatusActive is shown to users and offered in the wizard.
	StatusActive Status = "active"
	// StatusDraft is somebody's contribution that nobody has confirmed. It is
	// carried in the catalogue and hidden by default.
	StatusDraft Status = "draft"
	// StatusDeprecated is kept so an installed router still recognises the
	// school it already chose.
	StatusDeprecated Status = "deprecated"
)

// Catalogue is one payload's worth of presets.
type Catalogue struct {
	SchemaVersion int
	// UpdatedAt is the publisher's own date string, compared as text and never
	// parsed. See Supersedes.
	UpdatedAt string
	Source    string
	Schools   []School
}

// School is one school's preset, after normalisation.
type School struct {
	ShortName          string
	Name               string
	Description        string
	Contributors       []string
	Operators          []Operator
	Defaults           Defaults
	ObservedLoginShape LoginShape
	Status             Status
	SourceIssue        string
	DocURL             string
}

// Operator is one carrier suffix a school offers.
type Operator struct {
	// Suffix is appended to the account name after an @. Empty is a real
	// choice -- "no suffix" -- and is not the same as the operator being
	// absent, which is why the decoder distinguishes a missing key from an
	// empty value.
	Suffix string
	Label  string
}

// Unverified reports the sentinel a contributor uses for "this carrier exists
// and I do not know its suffix".
//
// It is displayed with a warning and must never reach an account: an account
// carrying it would build user@?? and authenticate as nobody.
func (o Operator) Unverified() bool { return o.Suffix == UnverifiedSuffix }

// UnverifiedSuffix is that sentinel.
const UnverifiedSuffix = "??"

// Defaults are the environment values a school's network fixes.
//
// Only these four. A preset that could carry an operator default, or a login
// name, would be carrying somebody's account settings in a public catalogue.
type Defaults struct {
	BaseURL    string
	ACID       string
	SSID       string
	AccessMode string
}

// LoginShape is what a real browser was seen to send.
//
// It comes from a capture, not from a guess: getting one of these wrong
// produces an authentication that fails in a way the gateway does not explain.
type LoginShape struct {
	N           string
	Type        string
	Enc         string
	InfoPrefix  string
	DoubleStack string
	OS          string
	Name        string
}

// Supersedes reports whether this catalogue should replace the cached one.
//
// Text comparison of the publisher's date, and deliberately not a parse. The
// baseline does the same, and changing it would change which of two payloads
// wins for dates the two spellings order differently -- "2026-3-5" against
// "2026-03-05" is the obvious pair.
//
// Equal dates supersede, which is the whole of the same-day correction rule:
// a publisher who finds a mistake an hour after publishing fixes it under the
// same date, and a > comparison would leave every router on the broken copy
// until the next day. That is why this is not >=.
func (c Catalogue) Supersedes(cached Catalogue) bool {
	if c.UpdatedAt == "" || cached.UpdatedAt == "" {
		// Nothing to order by. The fresh payload wins, because the caller only
		// reaches here with one it has already validated.
		return true
	}
	return !(c.UpdatedAt < cached.UpdatedAt)
}

// flexString decodes a JSON string, number or boolean into text.
//
// The catalogue is edited by contributors in a text editor and published
// without a schema tool, so `"ac_id": 1` and `"ac_id": "1"` both turn up. The
// baseline stringifies whatever it finds; refusing one of the two would reject
// a real school over a pair of quotes.
//
// Present is separate from Value because an absent key and an empty value are
// different answers in this catalogue -- an operator with no suffix is a school
// where accounts carry none.
type flexString struct {
	Value   string
	Present bool
}

func (f *flexString) UnmarshalJSON(data []byte) error {
	f.Present = true
	text := string(data)
	if text == "null" {
		f.Present = false
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		f.Value = value
		return nil
	}
	if number, err := strconv.ParseFloat(text, 64); err == nil {
		// 'f' with -1 precision, so ac_id is "1" and not "1e+00". There was a
		// separate integer branch here and no test could be made to fail for
		// removing it -- FormatFloat already renders a whole number without a
		// decimal point -- so it went rather than staying as a second way of
		// producing the same string.
		f.Value = strconv.FormatFloat(number, 'f', -1, 64)
		return nil
	}
	if text == "true" || text == "false" {
		f.Value = text
		return nil
	}
	// Anything else -- an object, an array -- is not a scalar and is treated as
	// absent rather than as an error, so one malformed field cannot cost the
	// whole catalogue.
	f.Present = false
	return nil
}

func (f flexString) trimmed() string { return strings.TrimSpace(f.Value) }
