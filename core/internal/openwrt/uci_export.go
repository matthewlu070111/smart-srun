package openwrt

import (
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ParseUCIExport reads `uci -n export`: explicit names for anonymous sections,
// and explicit option/list types even for a single-member list. `show` loses
// that distinction and therefore cannot supply a lossless rollback backup.
func ParseUCIExport(pkg string, data []byte) (UCIConfig, error) {
	c := UCIConfig{Package: pkg, index: map[string]int{}}
	text := string(data)
	section := ""
	seenPackage := false
	bad := func() (UCIConfig, error) {
		return UCIConfig{}, domain.Errorf(domain.CodeInternal, "uci export 输出格式无效")
	}
	for offset := 0; offset < len(text); {
		words, next, err := scanValue(text, offset)
		if err != nil {
			return UCIConfig{}, err
		}
		offset = next
		if len(words) == 0 {
			continue
		}
		switch words[0] {
		case "package":
			if seenPackage || len(words) != 2 || words[1] != pkg {
				return bad()
			}
			seenPackage = true
		case "config":
			if !seenPackage || len(words) != 3 || words[1] == "" || !IsLogicalInterfaceName(words[2]) {
				return bad()
			}
			section = words[2]
			if _, exists := c.Section(section); exists {
				return bad()
			}
			c.ensure(section).Type = words[1]
		case "option", "list":
			if section == "" || len(words) != 3 || !IsLogicalInterfaceName(words[1]) {
				return bad()
			}
			s := c.ensure(section)
			name, value := words[1], words[2]
			previous, exists := s.options[name]
			if words[0] == "option" {
				if exists {
					return bad()
				}
				if value == "" {
					continue
				} // UCI does not expose an empty scalar.
				s.options[name] = UCIValue{Text: value}
			} else {
				if exists && !previous.IsList {
					return bad()
				}
				previous.IsList = true
				previous.List = append(previous.List, value)
				s.options[name] = previous
			}
			if !exists {
				s.order = append(s.order, name)
			}
		default:
			return bad()
		}
	}
	if !seenPackage || strings.ContainsRune(text, '\x00') {
		return bad()
	}
	return c, nil
}
