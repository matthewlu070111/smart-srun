package update

import (
	"encoding/json"
	"strings"
)

// InspectAPKMetadata consumes apk-tools 3 adbdump's bounded JSON output. The
// native verifier must also authenticate the complete APK before installation.
func InspectAPKMetadata(data []byte) (PackageMetadata, error) {
	type acl struct {
		Mode  *uint32 `json:"mode"`
		User  string  `json:"user"`
		Group string  `json:"group"`
	}
	type file struct {
		Name   string          `json:"name"`
		ACL    acl             `json:"acl"`
		Size   *int64          `json:"size"`
		Target json.RawMessage `json:"target"`
	}
	var document struct {
		Info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Arch    string `json:"arch"`
			Size    int64  `json:"installed-size"`
		} `json:"info"`
		Paths []struct {
			Name  string `json:"name"`
			ACL   acl    `json:"acl"`
			Files []file `json:"files"`
		} `json:"paths"`
	}
	if len(data) == 0 || len(data) > 256<<10 || json.Unmarshal(data, &document) != nil || len(document.Paths) > 128 {
		return PackageMetadata{}, invalidManifest()
	}
	validACL := func(a acl) bool {
		return a.Mode != nil && *a.Mode & ^uint32(0o755) == 0 && a.User == "root" && a.Group == "root"
	}
	metadata := PackageMetadata{Name: document.Info.Name, Version: document.Info.Version,
		Architecture: document.Info.Arch, Files: map[string]int64{}}
	seen := map[string]bool{}
	var total int64
	for _, directory := range document.Paths {
		name, err := packagePath(directory.Name)
		if err != nil || name != directory.Name || seen[name] || !validACL(directory.ACL) {
			return PackageMetadata{}, invalidManifest()
		}
		seen[name] = true
		metadata.Directories = append(metadata.Directories, name)
		for _, entry := range directory.Files {
			if entry.Name == "" || strings.Contains(entry.Name, "/") || entry.Size == nil ||
				*entry.Size < 0 || *entry.Size > MaxPayloadBytes || len(entry.Target) != 0 || !validACL(entry.ACL) {
				return PackageMetadata{}, invalidManifest()
			}
			full := entry.Name
			if name != "" {
				full = name + "/" + full
			}
			clean, err := packagePath(full)
			if err != nil || clean != full || seen[full] || len(metadata.Files) >= 128 {
				return PackageMetadata{}, invalidManifest()
			}
			seen[full], metadata.Files[full] = true, *entry.Size
			total += *entry.Size
			if total > MaxPayloadBytes {
				return PackageMetadata{}, invalidManifest()
			}
		}
	}
	if total != document.Info.Size {
		return PackageMetadata{}, invalidManifest()
	}
	return metadata, nil
}
