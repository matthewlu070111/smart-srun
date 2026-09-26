package update

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func fixtureManifest(manager string, kinds ...string) Manifest {
	m := Manifest{SchemaVersion: 1, Release: "2.0.0rc10", Channel: "rc", SourceCommit: strings.Repeat("a", 40)}
	for _, kind := range kinds {
		a := Asset{ID: kind + "-" + manager, Kind: kind, PackageManager: manager,
			OpenWrtArch: "x86_64", Format: "ipk", PackageVersion: "2.0.0~rc10-r1", SDKRelease: "24.10.8",
			Target: "x86/64", GOOS: "linux", GOARCH: "amd64", SHA256: strings.Repeat("b", 64),
			Bytes: 2048, InstalledBytes: 4096, FirmwareCompat: []string{"24.10"}, Validation: Validation{Build: true, ELF: true}}
		if manager == "apk" {
			a.Format, a.PackageVersion, a.SDKRelease, a.FirmwareCompat = "apk", "2.0.0_rc10-r1", "25.12.2", []string{"25.12"}
		}
		if kind == "luci" {
			a.OpenWrtArch = "all"
			if manager == "apk" {
				a.OpenWrtArch = "noarch"
			}
			a.Validation.ELF = false
		}
		a.URL = "https://github.com/" + Repository + "/releases/download/" + m.Release + "/" + a.PackageName() + "-fixture." + a.Format
		if a.Format == "ipk" {
			a.URL = strings.Replace(a.URL, a.PackageName()+"-fixture", a.PackageName()+"_fixture", 1)
		}
		m.Assets = append(m.Assets, a)
	}
	return m
}

func fixtureInventory(manager, mode string) Inventory {
	i := Inventory{PackageManager: manager, Architecture: "x86_64", FirmwareFamily: "24.10",
		DisplayVersion: "2.0.0rc2", Packages: map[string]string{}}
	native := "2.0.0~rc2-r1"
	if manager == "apk" {
		i.FirmwareFamily, native = "25.12", "2.0.0_rc2-r1"
	}
	if mode == "bundle" {
		i.Packages["luci-app-smart-srun-bundle"] = native
	} else {
		i.Packages["smart-srun"] = native
		if mode == "split" {
			i.Packages["luci-app-smart-srun"] = native
		}
	}
	return i
}

func TestVersionNumericRCAndStableOrdering(t *testing.T) {
	values := []string{"1.9.9", "2.0.0rc2", "2.0.0rc10", "2.0.0", "2.0.1rc1", "2.1.0", "3.0.0"}
	for left := range values {
		a, err := ParseVersion(values[left])
		if err != nil || a.String() != values[left] {
			t.Fatalf("parse %q: %v", values[left], err)
		}
		for right := range values {
			b, _ := ParseVersion(values[right])
			order := a.Compare(b)
			if (left < right && order >= 0) || (left == right && order != 0) || (left > right && order <= 0) {
				t.Fatalf("%v vs %v: %d", a, b, order)
			}
		}
	}
	for _, text := range []string{"v2.0.0", "2.0.0rc0", "2.0.0rc01", "2.0.0-beta1", "02.0.0", "2.0.0\n", "4294967296.0.0", "2.0.0rc4294967296"} {
		if _, err := ParseVersion(text); err == nil {
			t.Fatalf("accepted invalid version %q", text)
		}
	}
}

func TestPlanPreservesNativeFormatArchitectureAndOwnership(t *testing.T) {
	for _, manager := range []string{"opkg", "apk"} {
		for _, mode := range []string{"core", "split", "bundle"} {
			t.Run(manager+"/"+mode, func(t *testing.T) {
				manifest := fixtureManifest(manager, "core", "luci", "bundle")
				data, _ := json.Marshal(manifest)
				parsed, err := ParseManifest(data)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := BuildPlan(parsed, fixtureInventory(manager, mode), "")
				if err != nil || plan.InstallMode != mode || len(plan.ID) != 64 {
					t.Fatalf("plan: %+v, %v", plan, err)
				}
				count := 1
				if mode == "split" {
					count = 2
				}
				if len(plan.Assets) != count {
					t.Fatal("wrong package set")
				}
				for _, asset := range plan.Assets {
					if asset.PackageManager != manager || (mode == "bundle") != (asset.Kind == "bundle") {
						t.Fatal("silently changed package format or ownership")
					}
				}
			})
		}
	}
}

func TestManifestRejectsAliasedFieldsDuplicatesAndForeignURLs(t *testing.T) {
	data, _ := json.Marshal(fixtureManifest("opkg", "bundle"))
	for name, bad := range map[string]string{
		"duplicate":     strings.Replace(string(data), `"kind":"bundle"`, `"kind":"bundle","kind":"core"`, 1),
		"alias":         strings.Replace(string(data), `"url":`, `"URL":`, 1),
		"nested alias":  strings.Replace(string(data), `"build":true`, `"Build":true`, 1),
		"foreign repo":  strings.Replace(string(data), Repository, "someone/smart-srun", 1),
		"other release": strings.Replace(string(data), "/download/2.0.0rc10/", "/download/2.0.0rc2/", 1),
		"http":          strings.Replace(string(data), "https://", "http://", 1),
		"escaped path":  strings.Replace(string(data), "/download/", "/%64ownload/", 1),
		"null":          strings.Replace(string(data), `"kind":"bundle"`, `"kind":null`, 1),
		"trailing":      string(data) + "{}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(bad)); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestPlanRejectsAmbiguousAndIncompatibleInstallations(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest, *Inventory){
		"wrong architecture":         func(_ *Manifest, i *Inventory) { i.Architecture = "aarch64_cortex-a53" },
		"wrong firmware":             func(_ *Manifest, i *Inventory) { i.FirmwareFamily = "23.05" },
		"wrong manager":              func(_ *Manifest, i *Inventory) { i.PackageManager = "apk" },
		"mixed ownership":            func(_ *Manifest, i *Inventory) { i.Packages["luci-app-smart-srun-bundle"] = "2.0.0~rc2-r1" },
		"half updated":               func(_ *Manifest, i *Inventory) { i.Packages["luci-app-smart-srun"] = "2.0.0~rc1-r1" },
		"different release counters": func(m *Manifest, _ *Inventory) { m.Assets[1].PackageVersion = "2.0.0~rc10-r2" },
		"missing ELF check":          func(m *Manifest, _ *Inventory) { m.Assets[0].Validation.ELF = false },
		"combined size":              func(m *Manifest, _ *Inventory) { m.Assets[0].InstalledBytes = MaxPayloadBytes },
		"ambiguous": func(m *Manifest, _ *Inventory) {
			a := m.Assets[0]
			a.ID += "-other"
			a.URL = strings.Replace(a.URL, "_fixture", "_second", 1)
			m.Assets = append(m.Assets, a)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, i := fixtureManifest("opkg", "core", "luci"), fixtureInventory("opkg", "split")
			mutate(&m, &i)
			if _, err := BuildPlan(m, i, ""); err == nil {
				t.Fatal("unsafe update plan accepted")
			}
		})
	}
}

func TestStableDoesNotSilentlyChangeChannelOrDowngrade(t *testing.T) {
	m := fixtureManifest("apk", "core")
	i := fixtureInventory("apk", "core")
	i.DisplayVersion, i.Packages["smart-srun"] = "2.0.0", "2.0.0-r1"
	m.Release = "2.1.0rc10"
	m.Assets[0].PackageVersion = "2.1.0_rc10-r1"
	m.Assets[0].URL = strings.Replace(m.Assets[0].URL, "/2.0.0rc10/", "/2.1.0rc10/", 1)
	if _, err := BuildPlan(m, i, ""); err == nil {
		t.Fatal("stable implicitly selected RC")
	}
	if _, err := BuildPlan(m, i, "rc"); err != nil {
		t.Fatalf("explicit RC channel was rejected: %v", err)
	}
	m = fixtureManifest("apk", "core")
	i = fixtureInventory("apk", "core")
	i.DisplayVersion, i.Packages["smart-srun"] = "2.0.0", "2.0.0-r1"
	_, err := BuildPlan(m, i, "rc")
	if code, _ := domain.CodeOf(err); code != domain.CodeNotFound {
		t.Fatalf("RC below stable should not be an upgrade: %v", err)
	}
}
