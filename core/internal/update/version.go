// Package update validates releases and runs isolated package installations.
package update

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:rc([1-9][0-9]*))?$`)

type Version struct {
	Major, Minor, Patch, RC uint32
}

func ParseVersion(text string) (Version, error) {
	parts := versionPattern.FindStringSubmatch(text)
	if len(parts) == 0 {
		return Version{}, domain.Errorf(domain.CodePackageIncompatible, "版本必须为 X.Y.Z 或 X.Y.ZrcN")
	}
	var values [4]uint32
	for i, part := range parts[1:] {
		if part == "" {
			continue
		}
		number, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return Version{}, domain.Errorf(domain.CodePackageIncompatible, "版本数字超出范围")
		}
		values[i] = uint32(number)
	}
	return Version{values[0], values[1], values[2], values[3]}, nil
}

func (v Version) String() string {
	text := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.RC != 0 {
		text += fmt.Sprintf("rc%d", v.RC)
	}
	return text
}

func (v Version) Compare(other Version) int {
	for _, pair := range [][2]uint32{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}} {
		if order := cmp.Compare(pair[0], pair[1]); order != 0 {
			return order
		}
	}
	if v.RC == 0 && other.RC != 0 {
		return 1
	}
	if other.RC == 0 && v.RC != 0 {
		return -1
	}
	return cmp.Compare(v.RC, other.RC)
}

func (v Version) Channel() string {
	if v.RC != 0 {
		return "rc"
	}
	return "stable"
}

// NativeBase is the upstream version portion. The SDK owns the release suffix.
func (v Version) NativeBase(manager string) string {
	base := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.RC != 0 {
		separator := "~rc"
		if manager == "apk" {
			separator = "_rc"
		}
		base += fmt.Sprintf("%s%d", separator, v.RC)
	}
	return base
}
