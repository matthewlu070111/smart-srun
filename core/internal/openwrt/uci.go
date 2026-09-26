package openwrt

import (
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// UCIValue is one option. A UCI option is either a string or a list, and the
// two are different things to uci: writing a list option as a string replaces
// it, and reading a list as a string joins it ambiguously.
type UCIValue struct {
	Text   string
	List   []string
	IsList bool
}

// UCISection is one section with its options.
//
// The options are behind methods rather than an exported map because a map in a
// returned struct is shared with the struct it was copied from: handing one out
// would let a caller change the parsed configuration through what looks like a
// local copy.
type UCISection struct {
	Name      string
	Type      string
	Anonymous bool

	options map[string]UCIValue
	order   []string
}

// Get returns an option's string form, or "" when it is absent.
//
// A list answers with its first item, which is what uci's own `get` does for
// the single-item case; callers that must distinguish use Lookup.
func (s UCISection) Get(option string) string {
	value, ok := s.options[option]
	if !ok {
		return ""
	}
	if value.IsList {
		if len(value.List) == 0 {
			return ""
		}
		return value.List[0]
	}
	return value.Text
}

// Lookup returns the option and whether it was present at all. Absent and empty
// are different: uci stores no line for an option set to the empty string, so
// "not configured" and "configured as nothing" have to stay distinguishable.
func (s UCISection) Lookup(option string) (UCIValue, bool) {
	value, ok := s.options[option]
	return value, ok
}

// OptionNames lists the options in the order uci printed them, which is the
// order they appear in the file.
func (s UCISection) OptionNames() []string { return slices.Clone(s.order) }

// UCIConfig is one uci package -- one file under /etc/config.
type UCIConfig struct {
	Package string

	sections []UCISection
	index    map[string]int
}

// Section looks one up by name.
func (c UCIConfig) Section(name string) (UCISection, bool) {
	position, ok := c.index[name]
	if !ok {
		return UCISection{}, false
	}
	return c.sections[position], true
}

// Sections returns every section in file order.
func (c UCIConfig) Sections() []UCISection { return slices.Clone(c.sections) }

// SectionsOfType returns every section of a type, in file order.
//
// Type, never name. wifi-device sections are called radio0 and radio1 on
// mac80211, but MediaTek's mtwifi driver names them MT7981_1_1; code that looks
// for a name beginning with "radio" finds no radios at all on those boards.
func (c UCIConfig) SectionsOfType(sectionType string) []UCISection {
	var out []UCISection
	for _, section := range c.sections {
		if section.Type == sectionType {
			out = append(out, section)
		}
	}
	return out
}

// ParseUCIShow reads the output of `uci show <package>`.
//
// The format is not line-oriented, which is the whole difficulty. uci prints
// values in shell quoting, and a value containing a newline is printed with
// that newline in it:
//
//	wireless.awkward.key='line1
//	wireless.awkward.ssid='\''HIJACKED'\'''
//
// That is one option whose value contains a newline and some text shaped like
// another assignment. Splitting the output into lines first reads it as two
// options: the key is truncated at "line1" and the second line is taken as an
// SSID, so a wireless key nobody validated decides what the SSID field says.
// The scan below tracks quoting, so a newline ends a record only outside
// quotes.
//
// The quoting is shell quoting exactly: inside '...' every byte is literal,
// including backslash, and the only way to include a quote is to close, escape
// one, and reopen. uci really does print 'ends-with-backslash\' for a value
// ending in a backslash, so a parser that treats backslash as an escape
// swallows the closing quote and mis-reads everything after it.
//
// These rules were read off a real device rather than inferred;
// testdata/openwrt/uci-awkward holds both the capture and uci's own answer for
// each value in it.
func ParseUCIShow(pkg string, data []byte) (UCIConfig, error) {
	config := UCIConfig{Package: pkg, index: map[string]int{}}
	text := string(data)

	for position := 0; position < len(text); {
		if text[position] == '\n' {
			position++
			continue
		}
		if strings.TrimSpace(text[position:]) == "" {
			break
		}
		key, items, next, err := scanRecord(text, position)
		if err != nil {
			return UCIConfig{}, err
		}
		position = next
		if err := config.apply(pkg, key, items); err != nil {
			return UCIConfig{}, err
		}
	}
	return config, nil
}

// keyEnd finds the '=' that ends the key, or -1 when the record's line has
// none. Only the run up to the first newline is considered: a key never
// contains one, so a line without '=' is not an assignment.
func keyEnd(text string, position int) int {
	for scan := position; scan < len(text); scan++ {
		if text[scan] == '\n' {
			return -1
		}
		if text[scan] == '=' {
			return scan
		}
	}
	return -1
}

// scanRecord consumes one `key=value` assignment and returns the offset just
// past it.
func scanRecord(text string, position int) (string, []string, int, error) {
	equals := keyEnd(text, position)
	if equals < 0 {
		return "", nil, 0, domain.Errorf(domain.CodeInternal,
			"uci 输出中有一行不是赋值语句")
	}
	items, next, err := scanValue(text, equals+1)
	if err != nil {
		return "", nil, 0, err
	}
	return text[position:equals], items, next, nil
}

// scanValue reads the right-hand side, splitting it into the items uci
// separates with unquoted whitespace, and stops at the first unquoted newline.
func scanValue(text string, position int) ([]string, int, error) {
	var (
		items   []string
		current strings.Builder
		started bool
	)
	flush := func() {
		if started {
			items = append(items, current.String())
			current.Reset()
			started = false
		}
	}

	for position < len(text) {
		switch symbol := text[position]; symbol {
		case '\n':
			flush()
			return items, position + 1, nil

		case ' ', '\t':
			// Unquoted whitespace separates list items. Whitespace inside a
			// value is quoted and never reaches this branch.
			flush()
			position++

		case '\'':
			started = true
			position++
			end := strings.IndexByte(text[position:], '\'')
			if end < 0 {
				return nil, 0, domain.Errorf(domain.CodeInternal,
					"uci 输出中有未闭合的引号")
			}
			current.WriteString(text[position : position+end])
			position += end + 1

		case '\\':
			// Outside quotes a backslash escapes exactly one byte. This is how
			// uci writes an embedded quote: 'it'\''s'.
			if position+1 >= len(text) {
				return nil, 0, domain.Errorf(domain.CodeInternal,
					"uci 输出以反斜杠结束")
			}
			started = true
			current.WriteByte(text[position+1])
			position += 2

		default:
			started = true
			current.WriteByte(symbol)
			position++
		}
	}
	flush()
	return items, position, nil
}

// UCIChangeKind distinguishes the three edits uci can have staged.
type UCIChangeKind string

const (
	UCIChangeSet    UCIChangeKind = "set"
	UCIChangeDelete UCIChangeKind = "delete"
	UCIChangeAppend UCIChangeKind = "append"
)

// UCIChange is one uncommitted edit.
type UCIChange struct {
	Kind  UCIChangeKind
	Key   string
	Value string
}

// ParseUCIChanges reads the output of `uci changes`.
//
// The format is the same shell quoting as `uci show`, with three shapes:
//
//	wireless.radio0.channel='11'      a set
//	-wireless.radio0.channel          a delete, with no value at all
//	wireless.newsec.freq+='2412'      an append to a list
//
// Values here can contain newlines too, so this reuses the same quote-aware
// scan. A line-based reader would turn the second line of a multi-line value
// into an extra change -- and since the answer to "are there uncommitted
// changes" decides whether this program is allowed to touch the wireless
// configuration at all, a phantom entry is not a cosmetic error.
//
// Output it cannot account for is an error rather than a shorter list, for the
// same reason: the caller uses an empty result as permission to proceed.
func ParseUCIChanges(data []byte) ([]UCIChange, error) {
	var changes []UCIChange
	text := string(data)

	for position := 0; position < len(text); {
		if text[position] == '\n' {
			position++
			continue
		}
		if strings.TrimSpace(text[position:]) == "" {
			break
		}

		// A deletion is the one record with no '=' anywhere on its line.
		if keyEnd(text, position) < 0 {
			line, remainder, found := strings.Cut(text[position:], "\n")
			position = len(text) - len(remainder)
			if !found {
				position = len(text)
			}
			name, isDelete := strings.CutPrefix(strings.TrimSpace(line), "-")
			if !isDelete || name == "" {
				return nil, domain.Errorf(domain.CodeInternal,
					"uci changes 输出中有无法识别的记录")
			}
			changes = append(changes, UCIChange{Kind: UCIChangeDelete, Key: name})
			continue
		}

		key, items, next, err := scanRecord(text, position)
		if err != nil {
			return nil, err
		}
		position = next

		change := UCIChange{Kind: UCIChangeSet, Key: strings.TrimSpace(key)}
		if trimmed, ok := strings.CutSuffix(change.Key, "+"); ok {
			change.Kind = UCIChangeAppend
			change.Key = trimmed
		}
		if len(items) > 0 {
			change.Value = items[0]
		}
		// A record with no key is not a change that can be skipped. Dropping it
		// would let a document that plainly has something in it come back as
		// "nothing is staged", which is the answer that allows this program to
		// start rewriting the wireless configuration.
		if change.Key == "" {
			return nil, domain.Errorf(domain.CodeInternal,
				"uci changes 输出中有没有键名的记录")
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// apply files one assignment under its section, creating the section when the
// assignment is the section header.
func (c *UCIConfig) apply(pkg, key string, items []string) error {
	head, rest, found := strings.Cut(key, ".")
	if !found || head != pkg {
		return domain.Errorf(domain.CodeInternal,
			"uci 输出中的 %q 不属于配置 %q", key, pkg)
	}

	name, option, hasOption := strings.Cut(rest, ".")
	if name == "" {
		return domain.Errorf(domain.CodeInternal, "uci 输出中有空的节名")
	}

	if !hasOption {
		// `wireless.radio0=wifi-device` declares a section and its type.
		sectionType := ""
		if len(items) > 0 {
			sectionType = items[0]
		}
		c.ensure(name).Type = sectionType
		return nil
	}

	section := c.ensure(name)
	if _, seen := section.options[option]; !seen {
		section.order = append(section.order, option)
	}
	// Several tokens are a list. One is a string: a single-item list prints
	// identically and uci itself does not distinguish them on read.
	switch {
	case len(items) > 1:
		section.options[option] = UCIValue{IsList: true, List: items}
	case len(items) == 1:
		section.options[option] = UCIValue{Text: items[0]}
	default:
		section.options[option] = UCIValue{}
	}
	return nil
}

func (c *UCIConfig) ensure(name string) *UCISection {
	if position, ok := c.index[name]; ok {
		return &c.sections[position]
	}
	c.sections = append(c.sections, UCISection{
		Name:      name,
		Anonymous: isAnonymousSectionName(name),
		options:   map[string]UCIValue{},
	})
	c.index[name] = len(c.sections) - 1
	return &c.sections[len(c.sections)-1]
}

// isAnonymousSectionName reports the two shapes uci uses for a section nobody
// named: the generated cfg-hash name it prints by default, and the @type[index]
// form it prints with -N.
func isAnonymousSectionName(name string) bool {
	if strings.HasPrefix(name, "@") && strings.HasSuffix(name, "]") {
		return true
	}
	rest, ok := strings.CutPrefix(name, "cfg")
	if !ok || rest == "" {
		return false
	}
	for _, symbol := range rest {
		isHex := (symbol >= '0' && symbol <= '9') ||
			(symbol >= 'a' && symbol <= 'f') || (symbol >= 'A' && symbol <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}
