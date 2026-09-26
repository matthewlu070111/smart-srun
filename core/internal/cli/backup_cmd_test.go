//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
)

func TestCLIBackupFileAndStandardInputRoundtrip(t *testing.T) {
	client, requests := onlineDaemon(t)
	fixture, err := os.ReadFile("../../../tests/fixtures/config-backup-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	run := func(args []string, input string, success bool) string {
		t.Helper()
		code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
			return runOnline(t.Context(), client, args, strings.NewReader(input), stdout, stderr)
		})
		if (code == 0) != success {
			t.Fatalf("unexpected exit %d: %s", code, errOut)
		}
		return out + errOut
	}
	preview := run([]string{"config", "import", "-", "--check"}, string(fixture), true)
	if strings.Contains(preview, "synthetic-secret") {
		t.Fatal("preview disclosed secret")
	}
	run([]string{"config", "import", "-", "--expected-revision", "0"}, string(fixture), true)
	run([]string{"config", "import", "-", "--expected-revision", "0"}, string(fixture), false)
	path := filepath.Join(t.TempDir(), "backup.json")
	run([]string{"config", "export", path}, "", true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.ParseBackup(data); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("public backup file")
	}
	run([]string{"config", "export", path}, "", false)
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("backup clobbered")
	}
	run([]string{"config", "import", path, "--check"}, "", true)
	run([]string{"config", "import", path}, "", true)
	stdout := run([]string{"config", "export", "-"}, "", true)
	if _, _, err := config.ParseBackup([]byte(stdout)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"config", "import"}, {"config", "import", path, "--unknown"}, {"config", "import", path, "--expected-revision"}, {"config", "import", path, "--expected-revision", "bad"}, {"config", "export", "-", "extra"}, {"config", "import", path + "missing"}} {
		run(args, "", false)
	}
	run([]string{"config", "import", "-"}, "{}", false)
	select {
	case <-requests:
		t.Fatal("backup triggered authentication")
	default:
	}
}
