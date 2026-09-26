package presets

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Parse reads a catalogue payload.
//
// A payload this build cannot read is an error rather than an empty catalogue,
// which is the M04 rule applied to the one document that decides what the
// wizard offers: "no schools" and "I could not read the schools" lead to
// different next steps, and only one of them should make the caller try the
// next source.
func Parse(data []byte) (Catalogue, error) {
	if int64(len(data)) > MaxPayloadBytes {
		return Catalogue{}, domain.Errorf(domain.CodeProtocolInvalid,
			"预设内容超过 %d 字节的上限", MaxPayloadBytes)
	}
	if len(data) == 0 {
		return Catalogue{}, domain.Errorf(domain.CodeProtocolInvalid,
			"预设内容为空")
	}

	var raw rawCatalogue
	if err := json.Unmarshal(data, &raw); err != nil {
		return Catalogue{}, domain.Errorf(domain.CodeProtocolInvalid,
			"预设不是有效的 JSON").Wrap(err)
	}
	// A pointer, so "schools is absent" and "schools is empty" stay apart. Any
	// JSON object at all decodes into a struct of optional fields, so without
	// this an error document, or another endpoint's reply, becomes a catalogue
	// with no schools -- and the caller stops looking at further sources.
	if raw.Schools == nil {
		return Catalogue{}, domain.Errorf(domain.CodeProtocolInvalid,
			"预设里没有 schools 列表")
	}

	version, ok := raw.SchemaVersion.version()
	if !ok {
		return Catalogue{}, domain.Errorf(domain.CodeProtocolInvalid,
			"预设的 schema_version 不是一个版本号")
	}
	if version != SchemaVersion {
		return Catalogue{}, domain.Errorf(domain.CodePackageIncompatible,
			"预设 schema_version 是 %d，本版本只认识 %d", version, SchemaVersion)
	}

	catalogue := Catalogue{
		SchemaVersion: version,
		UpdatedAt:     raw.UpdatedAt.trimmed(),
		Source:        raw.Source.trimmed(),
	}

	// First occurrence wins on a repeated id. The catalogue is edited by hand,
	// so a school pasted twice is a real possibility, and taking the later copy
	// would make which one applies depend on file order in a way nobody
	// intended.
	seen := map[string]struct{}{}
	for _, entry := range *raw.Schools {
		school, ok := normalizeSchool(entry)
		if !ok {
			continue
		}
		if _, repeated := seen[school.ShortName]; repeated {
			continue
		}
		seen[school.ShortName] = struct{}{}
		catalogue.Schools = append(catalogue.Schools, school)
	}
	return catalogue, nil
}

// Active is the catalogue with only the presets meant to be offered.
//
// Draft entries stay in the catalogue and are hidden here rather than dropped
// at parse time. Filtering earlier would let a built-in active entry silently
// replace a remote draft of the same school during a merge, which is not the
// same result.
func (c Catalogue) Active() []School {
	active := make([]School, 0, len(c.Schools))
	for _, school := range c.Schools {
		if school.Status == StatusActive {
			active = append(active, school)
		}
	}
	return active
}

// rawCatalogue mirrors the published document.
type rawCatalogue struct {
	SchemaVersion flexString   `json:"schema_version"`
	UpdatedAt     flexString   `json:"updated_at"`
	Source        flexString   `json:"source"`
	Schools       *[]rawSchool `json:"schools"`
}

type rawSchool struct {
	ID          flexString `json:"id"`
	ShortName   flexString `json:"short_name"`
	Name        flexString `json:"name"`
	Status      flexString `json:"status"`
	Verified    flexString `json:"verified"`
	Description flexString `json:"description"`
	DocURL      flexString `json:"doc_url"`
	SourceIssue flexString `json:"source_issue"`

	Contributors []flexString   `json:"contributors"`
	Operators    *[]rawOperator `json:"operators"`
	Defaults     *rawDefaults   `json:"defaults"`
	LoginShape   *rawLoginShape `json:"observed_login_shape"`
}

type rawOperator struct {
	Suffix flexString `json:"suffix"`
	// ID is the field's old name. Published catalogues that have not been
	// refreshed still carry it, and dropping those operators would make a
	// school's carrier list silently shrink.
	ID    flexString `json:"id"`
	Label flexString `json:"label"`
}

type rawDefaults struct {
	BaseURL    flexString `json:"base_url"`
	ACID       flexString `json:"ac_id"`
	SSID       flexString `json:"ssid"`
	AccessMode flexString `json:"access_mode"`

	// The two spellings of a per-school default operator that the format used
	// to have. They are not defaults any more -- an operator belongs to an
	// account, not to a school -- but a published catalogue may still carry
	// one, and it means a carrier choice this school offers.
	Operator       flexString `json:"operator"`
	OperatorSuffix flexString `json:"operator_suffix"`
}

type rawLoginShape struct {
	N           flexString `json:"n"`
	Type        flexString `json:"type"`
	Enc         flexString `json:"enc"`
	InfoPrefix  flexString `json:"info_prefix"`
	DoubleStack flexString `json:"double_stack"`
	OS          flexString `json:"os"`
	Name        flexString `json:"name"`
	LoginOS     flexString `json:"login_os"`
	LoginName   flexString `json:"login_name"`
}

// version reads schema_version, refusing anything that is not one.
//
// "v2", "abc" and an array all mean the same thing here: this is not a document
// whose version can be compared, so it is not a document to read. The baseline
// makes the same call, with a comment about an unparsable version taking the
// whole preset load down with it -- built-in fallback included.
func (f flexString) version() (int, bool) {
	text := f.trimmed()
	if text == "" {
		return 0, false
	}
	digits := 0
	for _, symbol := range text {
		if symbol < '0' || symbol > '9' {
			return 0, false
		}
		digits = digits*10 + int(symbol-'0')
		if digits > 1<<20 {
			return 0, false
		}
	}
	return digits, true
}

var unsafeID = regexp.MustCompile(`[^a-z0-9_.-]+`)

// SafeID is the identifier a school is stored and looked up under.
//
// Lower-cased, and everything outside [a-z0-9_.-] becomes a dash. It ends up in
// a file path and a UCI value, so a name that arrived with a slash or a space
// in it must not stay that way.
//
// Exported because a caller holding what a user typed has to reduce it the same
// way before looking a school up: two spellings differing only in case would
// otherwise find nothing.
func SafeID(value string) string {
	text := unsafeID.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	return strings.Trim(text, "-")
}

func normalizeSchool(raw rawSchool) (School, bool) {
	shortName := SafeID(raw.ID.trimmed())
	if shortName == "" {
		shortName = SafeID(raw.ShortName.trimmed())
	}
	if shortName == "" {
		// Nothing to store it under, so there is nothing to store.
		return School{}, false
	}

	school := School{
		ShortName:   shortName,
		Status:      normalizeStatus(raw),
		SourceIssue: raw.SourceIssue.trimmed(),
		DocURL:      raw.DocURL.trimmed(),
	}
	school.Name = raw.Name.trimmed()
	if school.Name == "" {
		school.Name = shortName
	}
	school.Description = raw.Description.trimmed()
	if school.Description == "" {
		// Said rather than left blank, and said differently for a draft: a
		// preset nobody has confirmed should not look like one that has been.
		school.Description = "远端学校预设"
		if school.Status != StatusActive {
			school.Description = "远端草稿预设"
		}
	}

	for _, contributor := range raw.Contributors {
		if text := contributor.trimmed(); text != "" {
			school.Contributors = append(school.Contributors, text)
		}
	}
	school.Defaults = normalizeDefaults(raw.Defaults)
	school.Operators = normalizeOperators(raw.Operators, raw.Defaults)
	school.ObservedLoginShape = normalizeLoginShape(raw.LoginShape)
	return school, true
}

// normalizeStatus reads the several spellings the catalogue has used.
//
// Unknown means draft, not active. A contribution whose status nobody
// recognises has not been confirmed by anybody, and defaulting the other way
// would put an unreviewed school in front of users.
func normalizeStatus(raw rawSchool) Status {
	switch strings.ToLower(raw.Status.trimmed()) {
	case "verified", string(StatusActive):
		return StatusActive
	case string(StatusDraft):
		return StatusDraft
	case string(StatusDeprecated):
		return StatusDeprecated
	}
	if strings.EqualFold(raw.Verified.trimmed(), "true") {
		return StatusActive
	}
	return StatusDraft
}

func normalizeDefaults(raw *rawDefaults) Defaults {
	if raw == nil {
		return Defaults{}
	}
	defaults := Defaults{
		BaseURL: NormalizeBaseURL(raw.BaseURL.trimmed()),
		ACID:    raw.ACID.trimmed(),
		SSID:    raw.SSID.trimmed(),
	}
	// Only the two this program knows how to act on. An access mode it cannot
	// interpret is worse than none: it would be written into an account and
	// then decide which interface authenticates.
	switch mode := strings.ToLower(raw.AccessMode.trimmed()); mode {
	case "wifi", "wired":
		defaults.AccessMode = mode
	}
	return defaults
}

// normalizeOperators builds the carrier list, including the one a legacy
// default still implies.
//
// The presence checks here are the whole of requirement F20's "empty, not
// selected and unknown are different". An operator entry with neither suffix
// nor id is no evidence of a carrier and is dropped; one with an empty suffix
// is a school where accounts carry none, and is kept.
func normalizeOperators(raw *[]rawOperator, defaults *rawDefaults) []Operator {
	var operators []Operator
	if raw != nil {
		for _, entry := range *raw {
			operator, ok := normalizeOperator(entry)
			if !ok {
				continue
			}
			operators = append(operators, operator)
		}
	}

	legacy, present := legacyDefaultSuffix(defaults)
	if !present {
		return operators
	}
	for _, operator := range operators {
		if operator.Suffix == legacy {
			return operators
		}
	}
	// First, because it was the school's default: it is the choice most of
	// that school's users want.
	return append([]Operator{{Suffix: legacy, Label: labelFor(legacy)}}, operators...)
}

func normalizeOperator(raw rawOperator) (Operator, bool) {
	var suffix string
	switch {
	case raw.Suffix.Present:
		suffix = raw.Suffix.trimmed()
	case raw.ID.Present:
		suffix = raw.ID.trimmed()
	default:
		// Neither spelling. Absence of evidence must not become a carrier
		// choice, and must not become a plain-account option either.
		return Operator{}, false
	}
	label := raw.Label.trimmed()
	if label == "" {
		label = labelFor(suffix)
	}
	return Operator{Suffix: suffix, Label: label}, true
}

// legacyDefaultSuffix reads the per-school default operator the format used to
// have, reporting separately whether it was there at all.
//
// Present-with-an-empty-value means "this school's default is no suffix", which
// is a carrier choice. Absent means the catalogue says nothing, and nothing is
// what should be offered.
func legacyDefaultSuffix(defaults *rawDefaults) (string, bool) {
	if defaults == nil {
		return "", false
	}
	if defaults.OperatorSuffix.Present {
		return defaults.OperatorSuffix.trimmed(), true
	}
	if defaults.Operator.Present {
		return canonicalSuffix(defaults.Operator.trimmed()), true
	}
	return "", false
}

// canonicalSuffix folds the placeholder an old catalogue used for "no suffix".
//
// `xn` was never a real carrier suffix; it was a marker. Read as one it would
// build user@xn, which authenticates as nobody.
func canonicalSuffix(value string) string {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "xn" {
		return ""
	}
	return text
}

func labelFor(suffix string) string {
	if suffix == "" {
		return "不加后缀"
	}
	return suffix
}

func normalizeLoginShape(raw *rawLoginShape) LoginShape {
	if raw == nil {
		return LoginShape{}
	}
	shape := LoginShape{
		N:           raw.N.trimmed(),
		Type:        raw.Type.trimmed(),
		Enc:         raw.Enc.trimmed(),
		DoubleStack: raw.DoubleStack.trimmed(),
		InfoPrefix:  unwrapInfoPrefix(raw.InfoPrefix.trimmed()),
	}
	// `os` and `name` are the capture's own field names; the longer spellings
	// are what the account carries. Both turn up in published catalogues.
	shape.OS = raw.OS.trimmed()
	if shape.OS == "" {
		shape.OS = raw.LoginOS.trimmed()
	}
	shape.Name = raw.Name.trimmed()
	if shape.Name == "" {
		shape.Name = raw.LoginName.trimmed()
	}
	return shape
}

// unwrapInfoPrefix strips the braces a contributor copies in with the value.
//
// The prefix appears in a captured request as {SRBX1}, and it gets pasted that
// way. Left alone it would be sent as part of the prefix and the gateway would
// reject an otherwise correct login.
func unwrapInfoPrefix(value string) string {
	if len(value) > 2 && strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		return strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}
