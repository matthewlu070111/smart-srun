//go:build unix

package openwrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

func TestNativeInstallerFailureRetainsSafeEvidenceWithoutOutputSecrets(t *testing.T) {
	for _, test := range []struct {
		body, status, hint string
	}{
		{"printf 'Segmentation fault synthetic-secret\\n'; exit 139", "退出码 139", "段错误"},
		{"printf 'No space left on device synthetic-secret\\n'; exit 1", "退出码 1", "存储空间不足"},
		{"printf 'cannot find dependency synthetic-secret\\n'; exit 1", "退出码 1", "依赖包"},
		{"printf 'not enough space synthetic-secret\\n'; exit 1", "退出码 1", "存储空间不足"},
		{"printf 'cannot allocate memory synthetic-secret\\n'; exit 1", "退出码 1", "无法分配内存"},
		{"printf 'signature synthetic-secret\\n'; exit 1", "退出码 1", "信任密钥"},
		{"printf 'synthetic-secret\\n'; exit 2", "退出码 2", "安装失败"},
		{"ulimit -c 0; kill -SEGV $$", "信号", "segmentation fault"},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "opkg"), []byte("#!/bin/sh\n"+test.body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		device := PackageDevice{Runner: Runner{SearchPath: []string{dir}}, Capabilities: Capabilities{PackageManager: PackageManagerOpkg}}
		err := device.Install([]update.LocalPackage{{Path: "/tmp/synthetic.ipk", Asset: update.Asset{PackageManager: "opkg"}}})
		code, message := update.ErrorStatus(err)
		if code != domain.CodeInstallFailed || !strings.Contains(message, test.status) || !strings.Contains(message, test.hint) || strings.Contains(message, "synthetic-secret") {
			t.Fatalf("%s %s", code, message)
		}
	}
}
