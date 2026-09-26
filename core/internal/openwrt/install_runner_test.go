//go:build unix

package openwrt

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeInstallerHasBoundedOutputButNoKillTimeout(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "opkg")
	text := "#!/bin/sh\n/bin/sleep 0.05\nprintf '%s' \"${SMARTSRUN_INSTALL_WORKER}:${HTTP_PROXY-unset}\"\n"
	if err := os.WriteFile(program, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_PROXY", "http://must-not-be-inherited.invalid")
	runner := Runner{SearchPath: []string{dir}, Timeout: time.Nanosecond}
	result, err := runner.RunInstall("opkg", "install", "/tmp/fixed.ipk")
	if err != nil || string(result.Stdout) != "1:unset" {
		t.Fatalf("install timeout/environment: %q %v", result.Stdout, err)
	}
	if _, err := runner.RunInstall("sh", "-c", "exit 0"); err == nil {
		t.Fatal("non-native command allowed")
	}
	if err := os.WriteFile(program, []byte("#!/bin/sh\n/usr/bin/head -c 100000 /dev/zero\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err = runner.RunInstall("opkg", "install", "/tmp/fixed.ipk")
	if err != nil || !result.StdoutTruncated || len(result.Stdout) != 64<<10 {
		t.Fatalf("unbounded installer output: %d %v", len(result.Stdout), err)
	}
}
