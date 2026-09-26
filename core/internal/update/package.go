package update

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"path"
	"strings"
)

// PackageMetadata is checked against the manifest before invoking an installer.
// Payload lists only regular files; links and arbitrary install locations are
// not needed by this package and are rejected.
type PackageMetadata struct {
	Name, Version, Architecture string
	Files                       map[string]int64
	Directories                 []string
}

var corePayload = []string{
	"usr/bin/srunnet", "etc/init.d/smart_srun", "lib/upgrade/keep.d/smart-srun",
	"usr/share/smart-srun/school-presets.json", "usr/share/smart-srun/third-party-licenses.txt",
}

var luciPayload = []string{
	"usr/lib/lua/luci/controller/smart_srun.lua", "usr/lib/lua/luci/model/cbi/smart_srun.lua",
	"usr/lib/lua/luci/smart_srun/rpc.lua", "usr/lib/lua/luci/smart_srun/schema.lua",
	"usr/lib/lua/luci/smart_srun/bridge.lua", "www/luci-static/resources/smart_srun.js",
}

func (m PackageMetadata) Validate(asset Asset) error {
	arch := asset.OpenWrtArch
	if m.Name != asset.PackageName() || m.Version != asset.PackageVersion || m.Architecture != arch {
		return invalidManifest()
	}
	allowed := make(map[string]bool)
	if asset.Kind != "luci" {
		for _, name := range corePayload {
			allowed[name] = true
		}
	}
	if asset.Kind != "core" {
		for _, name := range luciPayload {
			allowed[name] = true
		}
	}
	for required := range allowed {
		if _, found := m.Files[required]; !found {
			return invalidManifest()
		}
	}
	if asset.Kind != "luci" {
		allowed["etc/init.d/smart_srun_update"] = true
		allowed["lib/upgrade/keep.d/"+m.Name] = true
	}
	for _, suffix := range []string{"list", "conffiles", "conffiles_static"} {
		if asset.Format == "apk" {
			allowed["lib/apk/packages/"+m.Name+"."+suffix] = true
		}
	}
	directories := map[string]bool{"": true}
	if asset.Kind != "luci" {
		directories["etc/smart-srun"] = true
	}
	for file := range allowed {
		for parent := path.Dir(file); parent != "."; parent = path.Dir(parent) {
			directories[parent] = true
		}
	}
	for _, directory := range m.Directories {
		if !directories[directory] {
			return invalidManifest()
		}
	}
	var total int64
	for name, size := range m.Files {
		if !allowed[name] || size < 0 || size > MaxPayloadBytes {
			return invalidManifest()
		}
		total += size
		if total > MaxPayloadBytes {
			return invalidManifest()
		}
	}
	if total != asset.InstalledBytes {
		return invalidManifest()
	}
	return nil
}

// InspectIPK streams the nested OpenWrt tar/gzip container. It never extracts
// paths or runs scripts, and independently bounds each compression layer.
func InspectIPK(input io.Reader) (PackageMetadata, error) {
	metadata := PackageMetadata{Files: make(map[string]int64)}
	seen := make(map[string]bool)
	err := readTarGzip(input, MaxAssetBytes+(256<<10), func(header *tar.Header, content io.Reader) error {
		name, err := packagePath(header.Name)
		if err != nil || seen[name] || header.Typeflag != tar.TypeReg {
			return invalidManifest()
		}
		seen[name] = true
		switch name {
		case "debian-binary":
			data, err := io.ReadAll(io.LimitReader(content, 16))
			if err != nil || string(data) != "2.0\n" || header.Size != 4 {
				return invalidManifest()
			}
		case "control.tar.gz":
			return inspectControl(content, &metadata)
		case "data.tar.gz":
			return inspectData(content, &metadata)
		default:
			return invalidManifest()
		}
		return nil
	})
	if err != nil || len(seen) != 3 || metadata.Name == "" || metadata.Version == "" || metadata.Architecture == "" {
		return PackageMetadata{}, invalidManifest()
	}
	return metadata, nil
}

func readTarGzip(input io.Reader, limit int64, visit func(*tar.Header, io.Reader) error) error {
	compressed, err := gzip.NewReader(input)
	if err != nil {
		return invalidManifest()
	}
	defer compressed.Close()
	bounded := &io.LimitedReader{R: compressed, N: limit + 1}
	archive := tar.NewReader(bounded)
	count := 0
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		count++
		if err != nil || bounded.N <= 0 || count > 256 || header.Size < 0 || header.Size > limit {
			return invalidManifest()
		}
		if err := visit(header, io.LimitReader(archive, header.Size)); err != nil {
			return err
		}
	}
	// tar's terminator may precede gzip's checksum/trailer. Consume it within
	// the same bound, so an oversized padding tail is not an extraction bomb.
	var padding [4096]byte
	for {
		n, err := bounded.Read(padding[:])
		for _, value := range padding[:n] {
			if value != 0 {
				return invalidManifest()
			}
		}
		if bounded.N <= 0 || (err != nil && err != io.EOF) {
			return invalidManifest()
		}
		if err == io.EOF {
			break
		}
	}
	return nil
}

func packagePath(name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return "", nil // archive root directory only
	}
	if len(name) > 256 || strings.ContainsAny(name, "\\\x00\r\n\t") ||
		strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || path.Clean(name) != name {
		return "", invalidManifest()
	}
	return name, nil
}

func inspectControl(input io.Reader, metadata *PackageMetadata) error {
	seen := make(map[string]bool)
	err := readTarGzip(input, 128<<10, func(header *tar.Header, content io.Reader) error {
		name, err := packagePath(header.Name)
		if err != nil || seen[name] {
			return invalidManifest()
		}
		seen[name] = true
		if header.Typeflag == tar.TypeDir && name == "" {
			return nil
		}
		if header.Typeflag != tar.TypeReg {
			return invalidManifest()
		}
		switch name {
		case "control":
			if header.Size > 32<<10 {
				return invalidManifest()
			}
			data, err := io.ReadAll(content)
			if err != nil {
				return invalidManifest()
			}
			return parseControl(string(data), metadata)
		case "preinst", "postinst", "prerm", "postrm", "conffiles", "files-sha256sum":
			return nil // installer-owned scripts, not executed by the inspector
		default:
			return invalidManifest()
		}
	})
	if err != nil || !seen["control"] {
		return invalidManifest()
	}
	return nil
}

func parseControl(text string, metadata *PackageMetadata) error {
	fields := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return invalidManifest()
		}
		if _, duplicate := fields[name]; duplicate {
			return invalidManifest()
		}
		fields[name] = strings.TrimSpace(value)
	}
	metadata.Name, metadata.Version, metadata.Architecture = fields["Package"], fields["Version"], fields["Architecture"]
	return nil
}

func inspectData(input io.Reader, metadata *PackageMetadata) error {
	seen := make(map[string]bool)
	return readTarGzip(input, MaxPayloadBytes+(256<<10), func(header *tar.Header, _ io.Reader) error {
		name, err := packagePath(header.Name)
		if err != nil || seen[name] || header.Mode&^0o755 != 0 || header.Uid != 0 || header.Gid != 0 {
			return invalidManifest()
		}
		seen[name] = true
		switch header.Typeflag {
		case tar.TypeDir:
			metadata.Directories = append(metadata.Directories, name)
			return nil
		case tar.TypeReg:
			if name == "" {
				return invalidManifest()
			}
			metadata.Files[name] = header.Size
		default:
			return invalidManifest()
		}
		return nil
	})
}
