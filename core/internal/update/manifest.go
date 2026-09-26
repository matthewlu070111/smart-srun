package update

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const (
	Repository       = "matthewlu070111/smart-srun"
	MaxManifestBytes = 256 << 10
	MaxAssetBytes    = 16 << 20
	// MaxPayloadBytes is the installed size one device may accept, raised from
	// 10 MiB by D80. The old limit left 132 KiB on mipsel, which is not a
	// budget -- it is a number that refuses the next change whatever that
	// change is. D20 measured where the size actually goes: crypto, runtime,
	// net/http and encoding/json, with this project's own packages at about
	// 5%, so the limit cannot be met by writing smaller code.
	MaxPayloadBytes = 16 << 20
	MaxAssets       = 256
)

type Validation struct {
	Build          bool `json:"build"`
	ELF            bool `json:"elf"`
	EmulatedCore   bool `json:"emulated_core"`
	OpenWrtInstall bool `json:"openwrt_install"`
	HardwareCore   bool `json:"hardware_core"`
	CampusAuth     bool `json:"campus_auth"`
}

type Asset struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	PackageManager string     `json:"package_manager"`
	Format         string     `json:"format"`
	OpenWrtArch    string     `json:"openwrt_arch"`
	PackageVersion string     `json:"package_version"`
	SDKRelease     string     `json:"sdk_release"`
	Target         string     `json:"target"`
	GOOS           string     `json:"goos"`
	GOARCH         string     `json:"goarch"`
	URL            string     `json:"url"`
	SHA256         string     `json:"sha256"`
	Bytes          int64      `json:"bytes"`
	InstalledBytes int64      `json:"installed_bytes"`
	FirmwareCompat []string   `json:"firmware_compat"`
	Validation     Validation `json:"validation"`
}

type Manifest struct {
	SchemaVersion int     `json:"schema_version"`
	Release       string  `json:"release"`
	Channel       string  `json:"channel"`
	SourceCommit  string  `json:"source_commit"`
	Assets        []Asset `json:"assets"`
}

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	namePattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	familyPattern = regexp.MustCompile(`^[0-9]{2}\.[0-9]{2}$`)
	sdkPattern    = regexp.MustCompile(`^[0-9]{2}\.[0-9]{2}\.[0-9]+$`)
	targetPattern = regexp.MustCompile(`^[a-z0-9_-]+/[a-z0-9_-]+$`)
)

func invalidManifest() error {
	return domain.Errorf(domain.CodePackageIncompatible, "发布清单格式、来源或包元数据无效")
}

func ParseManifest(data []byte) (Manifest, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return Manifest{}, invalidManifest()
	}
	// Check each object separately: DecodePatch's exact-field validation does
	// not recursively inspect structs inside a slice of JSON values.
	var raw struct {
		SchemaVersion int               `json:"schema_version"`
		Release       string            `json:"release"`
		Channel       string            `json:"channel"`
		SourceCommit  string            `json:"source_commit"`
		Assets        []json.RawMessage `json:"assets"`
	}
	if err := config.DecodePatch(data, &raw); err != nil {
		return Manifest{}, invalidManifest()
	}
	manifest := Manifest{SchemaVersion: raw.SchemaVersion, Release: raw.Release,
		Channel: raw.Channel, SourceCommit: raw.SourceCommit}
	for _, entry := range raw.Assets {
		var asset Asset
		if err := config.DecodePatch(entry, &asset); err != nil {
			return Manifest{}, invalidManifest()
		}
		manifest.Assets = append(manifest.Assets, asset)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	version, err := ParseVersion(m.Release)
	if err != nil || m.SchemaVersion != 1 || version.Major < 2 || m.Channel != version.Channel() ||
		!commitPattern.MatchString(m.SourceCommit) || len(m.Assets) == 0 || len(m.Assets) > MaxAssets {
		return invalidManifest()
	}
	ids, urls, selections := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, asset := range m.Assets {
		if err := asset.validate(version); err != nil {
			return err
		}
		if ids[asset.ID] || urls[asset.URL] {
			return invalidManifest()
		}
		ids[asset.ID], urls[asset.URL] = true, true
		for _, family := range asset.FirmwareCompat {
			selection := strings.Join([]string{asset.Kind, asset.PackageManager, asset.OpenWrtArch, family}, "/")
			if selections[selection] {
				return invalidManifest() // selecting the first match would hide ambiguity
			}
			selections[selection] = true
		}
	}
	return nil
}

func (a Asset) PackageName() string {
	switch a.Kind {
	case "core":
		return "smart-srun"
	case "luci":
		return "luci-app-smart-srun"
	case "bundle":
		return "luci-app-smart-srun-bundle"
	}
	return ""
}

func (a Asset) validate(version Version) error {
	if a.PackageName() == "" || !namePattern.MatchString(a.ID) || !namePattern.MatchString(a.OpenWrtArch) ||
		!sha256Pattern.MatchString(a.SHA256) ||
		a.Bytes <= 0 || a.Bytes > MaxAssetBytes || a.InstalledBytes <= 0 || a.InstalledBytes > MaxPayloadBytes ||
		!sdkPattern.MatchString(a.SDKRelease) || !targetPattern.MatchString(a.Target) || a.GOOS != "linux" ||
		!namePattern.MatchString(a.GOARCH) || len(a.FirmwareCompat) == 0 || len(a.FirmwareCompat) > 8 {
		return invalidManifest()
	}
	if (a.PackageManager != "opkg" || a.Format != "ipk") && (a.PackageManager != "apk" || a.Format != "apk") {
		return invalidManifest()
	}
	if a.Kind == "luci" {
		expected := "all"
		if a.PackageManager == "apk" {
			expected = "noarch"
		}
		if a.OpenWrtArch != expected {
			return invalidManifest()
		}
	} else if a.OpenWrtArch == "all" || a.OpenWrtArch == "noarch" {
		return invalidManifest()
	}
	prefix := version.NativeBase(a.PackageManager) + "-r"
	if !strings.HasPrefix(a.PackageVersion, prefix) || !positiveNumber(strings.TrimPrefix(a.PackageVersion, prefix)) {
		return invalidManifest()
	}
	for _, family := range a.FirmwareCompat {
		if !familyPattern.MatchString(family) {
			return invalidManifest()
		}
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" ||
		!strings.HasPrefix(u.Path, "/"+Repository+"/releases/download/"+version.String()+"/") {
		return invalidManifest()
	}
	filename := strings.TrimPrefix(u.Path, "/"+Repository+"/releases/download/"+version.String()+"/")
	if strings.ContainsAny(filename, "/\\\x00\r\n\t ") || !strings.HasSuffix(filename, "."+a.Format) ||
		!strings.HasPrefix(filename, a.PackageName()+map[string]string{"ipk": "_", "apk": "-"}[a.Format]) {
		return invalidManifest()
	}
	return nil
}

func positiveNumber(value string) bool {
	if value == "" || value[0] < '1' || value[0] > '9' || len(value) > 9 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
