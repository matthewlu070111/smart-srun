package openwrt

import (
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestOpkgStatusRecognizesUserFlagAndRejectsHalfInstallation(t *testing.T) {
	for _, flag := range []string{"ok", "user"} {
		data := []byte("Package: smart-srun\nVersion: 2.0.0~rc1-r1\nStatus: install " + flag + " installed\nArchitecture: x86_64\n")
		packages, err := parseOpkgStatus(data)
		if err != nil || len(packages) != 1 || packages[0].Version != "2.0.0~rc1-r1" || packages[0].Arch != "x86_64" {
			t.Fatalf("actual opkg status with %s flag: %+v, %v", flag, packages, err)
		}
	}
	for _, state := range []string{"unpacked", "half-installed", "half-configured"} {
		_, err := parseOpkgStatus([]byte("Package: smart-srun\nStatus: install user " + state + "\n"))
		if code, _ := domain.CodeOf(err); code != domain.CodeRecoveryRequired {
			t.Fatalf("unsafe state %s accepted: %v", state, err)
		}
	}
	if _, err := parseOpkgStatus([]byte("Package: smart-srun\nStatus: install ok installed\nVersion: 1\nVersion: 2\n")); err == nil {
		t.Fatal("duplicate native version accepted")
	}
}
