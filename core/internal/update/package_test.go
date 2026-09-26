package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"
)

type tarEntry struct {
	name string
	kind byte
	mode int64
	data []byte
}

func archiveBytes(t *testing.T, entries []tarEntry, tail []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	z := gzip.NewWriter(&output)
	w := tar.NewWriter(z)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: e.mode, Size: int64(len(e.data))}
		if e.kind == tar.TypeSymlink {
			h.Linkname = "/etc/shadow"
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := z.Write(tail); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func fixtureIPK(t *testing.T, asset Asset, extra []tarEntry, tail []byte) []byte {
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\n", asset.PackageName(), asset.PackageVersion, asset.OpenWrtArch)
	var payload []tarEntry
	for _, name := range corePayload {
		payload = append(payload, tarEntry{"./" + name, tar.TypeReg, 0o644, []byte("test")})
	}
	payload = append(payload, extra...)
	return archiveBytes(t, []tarEntry{
		{"./debian-binary", tar.TypeReg, 0o644, []byte("2.0\n")},
		{"./control.tar.gz", tar.TypeReg, 0o644, archiveBytes(t, []tarEntry{{"./control", tar.TypeReg, 0o644, []byte(control)}}, nil)},
		{"./data.tar.gz", tar.TypeReg, 0o644, archiveBytes(t, payload, tail)},
	}, nil)
}

func TestIPKMetadataAndPayloadRefusals(t *testing.T) {
	asset := fixtureManifest("opkg", "core").Assets[0]
	asset.InstalledBytes = int64(len(corePayload) * 4)
	good := fixtureIPK(t, asset, []tarEntry{{"./etc/smart-srun/", tar.TypeDir, 0o700, nil}}, nil)
	m, err := InspectIPK(bytes.NewReader(good))
	if err != nil || m.Validate(asset) != nil {
		t.Fatalf("valid IPK: %+v %v", m, err)
	}
	for name, extra := range map[string]tarEntry{
		"absolute":  {"/etc/shadow", tar.TypeReg, 0o600, nil},
		"traversal": {"./../etc/shadow", tar.TypeReg, 0o600, nil},
		"symlink":   {"./etc/secret", tar.TypeSymlink, 0o644, nil},
		"setuid":    {"./usr/bin/other", tar.TypeReg, 0o4755, nil},
		"writable":  {"./usr/bin/other", tar.TypeReg, 0o666, nil},
		"duplicate": {"./usr/bin/srunnet", tar.TypeReg, 0o644, nil},
		"unrelated": {"./etc/shadow", tar.TypeReg, 0o600, nil},
		"directory": {"./root/unrelated/", tar.TypeDir, 0o700, nil},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := InspectIPK(bytes.NewReader(fixtureIPK(t, asset, []tarEntry{extra}, nil)))
			if err == nil && m.Validate(asset) == nil {
				t.Fatal("unsafe package accepted")
			}
		})
	}
	for _, tail := range [][]byte{[]byte("hidden archive"), make([]byte, MaxPayloadBytes+(512<<10))} {
		if _, err := InspectIPK(bytes.NewReader(fixtureIPK(t, asset, nil, tail))); err == nil {
			t.Fatal("unsafe tail accepted")
		}
	}
	for _, size := range []int{0, len(good) / 2, len(good) - 4} {
		if _, err := InspectIPK(bytes.NewReader(good[:size])); err == nil {
			t.Fatal("truncated package accepted")
		}
	}
	asset.PackageVersion = "2.0.0~rc11-r1"
	if m.Validate(asset) == nil {
		t.Fatal("metadata mismatch accepted")
	}
}

func TestAPKRejectsLinkDuplicateAndUnsafeModes(t *testing.T) {
	text := `{"info":{"name":"smart-srun","version":"2.0.0_rc10-r1","arch":"x86_64","installed-size":4},"paths":[{"name":"usr/bin","acl":{"mode":493,"user":"root","group":"root"},"files":[{"name":"srunnet","acl":{"mode":493,"user":"root","group":"root"},"size":4}]}]}`
	m, err := InspectAPKMetadata([]byte(text))
	if err != nil || m.Files["usr/bin/srunnet"] != 4 {
		t.Fatalf("metadata: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(text, `"size":4`, `"size":4,"target":"/etc/shadow"`, 1),
		strings.ReplaceAll(text, `"mode":493`, `"mode":2541`),
		strings.Replace(text, `"srunnet"`, `"../srunnet"`, 1),
		strings.Replace(text, `"size":4`, `"size":10485761`, 1),
		strings.Replace(text, `"installed-size":4`, `"installed-size":5`, 1),
		strings.Replace(text, `"user":"root"`, `"user":"nobody"`, 1),
	} {
		if _, err := InspectAPKMetadata([]byte(bad)); err == nil {
			t.Fatal("unsafe APK metadata accepted")
		}
	}
}
