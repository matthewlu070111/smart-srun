//go:build unix

package openwrt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestCurrentInventoryRecoversAfterPostinstallPackageLock(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("locked", "held by native installer")
	write("opkg", `#!/bin/sh
test ! -e "${0%/*}/locked" || exit 255
case "$*" in
  print-architecture) printf 'arch all 1\narch aarch64_cortex-a53 10\n';;
  'status luci-app-smart-srun-bundle') printf 'Package: luci-app-smart-srun-bundle\nVersion: 2.0.0~rc1-r1\nStatus: install user installed\nArchitecture: aarch64_cortex-a53\n';;
  'status smart-srun'|'status luci-app-smart-srun') exit 0;;
  *) exit 90;;
esac
`)
	write("ubus", `#!/bin/sh
case "$*" in
  list) printf 'iwinfo\nnetwork.wireless\n';;
  'call system board') printf '{"release":{"version":"25.12-SNAPSHOT"}}';;
  *) exit 90;;
esac
`)
	runner := Runner{SearchPath: []string{dir}}
	startup := Detect(t.Context(), runner)
	if startup.PackageManager != PackageManagerNone {
		t.Fatal("locked package manager was accepted")
	}
	_, err := CurrentPackageInventory(t.Context(), runner, "2.0.0rc1")
	if code, _ := domain.CodeOf(err); code != domain.CodeUnsupportedCapability {
		t.Fatalf("locked inventory: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "locked")); err != nil {
		t.Fatal(err)
	}
	got, err := CurrentPackageInventory(t.Context(), runner, "2.0.0rc1")
	if err != nil || got.PackageManager != "opkg" || got.Architecture != "aarch64_cortex-a53" || got.FirmwareFamily != "25.12" || got.Packages["luci-app-smart-srun-bundle"] != "2.0.0~rc1-r1" {
		t.Fatalf("inventory after lock release: %+v, %v", got, err)
	}
	write("locked", "another native installer")
	if _, err := CurrentPackageInventory(t.Context(), runner, "2.0.0rc1"); err == nil {
		t.Fatal("a later package lock reused an earlier successful probe")
	}
}
