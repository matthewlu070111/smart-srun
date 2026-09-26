package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture runs fn with stdout and stderr redirected to temp files and returns
// what each received. The CLI writes to *os.File so the daemon can hand it a
// real descriptor later; pipes would deadlock on large output.
func capture(t *testing.T, fn func(stdout, stderr *os.File) int) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	open := func(name string) *os.File {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return file
	}
	stdout, stderr := open("stdout"), open("stderr")
	code := fn(stdout, stderr)
	stdout.Close()
	stderr.Close()

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	return code, read("stdout"), read("stderr")
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestConfigValidateAcceptsAGoodFile(t *testing.T) {
	path := writeTemp(t, `{"schema_version":2,"enabled":true,
	  "campus_accounts":[{"id":"c1","user_id":"u1","password":"p",
	    "access_mode":"wired","wired_iface":"wan","operator_suffix":""}],
	  "selection":{"active_campus_id":"c1","default_campus_id":"c1"}}`)

	code, stdout, stderr := capture(t, func(out, errOut *os.File) int {
		return RunConfig([]string{"validate", path}, out, errOut)
	})

	if code != ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "1 个校园账号") {
		t.Fatalf("stdout = %q, want a summary of what was validated", stdout)
	}
}

// Every problem is listed with its field path, so a user fixing a file by hand
// sees the whole list instead of one problem per run.
func TestConfigValidateListsEveryProblemWithItsField(t *testing.T) {
	path := writeTemp(t, `{"schema_version":2,
	  "checks":{"interval_seconds":0},
	  "log":{"level":"LOUD"},
	  "campus_accounts":[{"id":"c1","user_id":"u1","access_mode":"wired",
	    "wired_iface":"wan","operator_suffix":"??"}]}`)

	code, stdout, stderr := capture(t, func(out, errOut *os.File) int {
		return RunConfig([]string{"validate", path}, out, errOut)
	})

	if code != ExitInvalidInput {
		t.Fatalf("exit %d, want %d", code, ExitInvalidInput)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want diagnostics on stderr only", stdout)
	}
	for _, want := range []string{
		"checks.interval_seconds", "log.level", "campus_accounts[0].operator_suffix",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not mention %q:\n%s", want, stderr)
		}
	}
}

// A validation failure must never echo a secret back: the user may be pasting
// output into an issue.
func TestConfigValidateDoesNotEchoSecrets(t *testing.T) {
	path := writeTemp(t, `{"schema_version":2,
	  "campus_accounts":[{"id":"c1","user_id":"u1","password":"hunter2-secret",
	    "access_mode":"wifi","ssid":"campus","encryption":"psk2",
	    "key":"wifi-secret-key","ap_selection":"fixed","bssid":"nope"}]}`)

	_, stdout, stderr := capture(t, func(out, errOut *os.File) int {
		return RunConfig([]string{"validate", path}, out, errOut)
	})

	for _, secret := range []string{"hunter2-secret", "wifi-secret-key"} {
		if strings.Contains(stdout+stderr, secret) {
			t.Errorf("output contains the secret %q", secret)
		}
	}
}

func TestConfigValidateReportsMissingFiles(t *testing.T) {
	code, _, stderr := capture(t, func(out, errOut *os.File) int {
		return RunConfig([]string{"validate", filepath.Join(t.TempDir(), "nope")}, out, errOut)
	})
	if code != ExitInvalidInput {
		t.Fatalf("exit %d, want %d", code, ExitInvalidInput)
	}
	if !strings.Contains(stderr, "读取") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// Machine-readable output is exactly one JSON document on stdout.
func TestConfigSchemaAndDefaultsEmitOneJSONDocument(t *testing.T) {
	for _, subcommand := range []string{"schema", "defaults"} {
		t.Run(subcommand, func(t *testing.T) {
			code, stdout, stderr := capture(t, func(out, errOut *os.File) int {
				return RunConfig([]string{subcommand}, out, errOut)
			})
			if code != ExitOK {
				t.Fatalf("exit %d, stderr: %s", code, stderr)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want it empty for machine output", stderr)
			}

			decoder := json.NewDecoder(strings.NewReader(stdout))
			var first any
			if err := decoder.Decode(&first); err != nil {
				t.Fatalf("stdout is not valid JSON: %v", err)
			}
			if decoder.More() {
				t.Fatal("stdout carries more than one JSON document")
			}
		})
	}
}

func TestConfigRejectsUnknownSubcommands(t *testing.T) {
	for _, args := range [][]string{{}, {"reset"}, {"validate", "a", "b"}} {
		code, _, stderr := capture(t, func(out, errOut *os.File) int {
			return RunConfig(args, out, errOut)
		})
		if code != ExitInvalidInput {
			t.Errorf("args %v exited %d, want %d", args, code, ExitInvalidInput)
		}
		if !strings.Contains(stderr, "用法") && !strings.Contains(stderr, "未知") {
			t.Errorf("args %v: stderr = %q, want usage guidance", args, stderr)
		}
	}
}

func TestVersionStringNamesTheSchemaVersion(t *testing.T) {
	got := VersionString()
	if !strings.HasPrefix(got, "srunnet ") || !strings.Contains(got, "schema v2") {
		t.Fatalf("VersionString() = %q", got)
	}
}
