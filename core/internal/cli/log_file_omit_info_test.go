package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// srunnet config get/set reaches log.file_omit_info like any other setting,
// and a partial set leaves log.level alone.
func TestCLIReadsAndWritesLogFileOmitInfo(t *testing.T) {
	client, _ := onlineDaemon(t)
	run := func(args []string, input string, want int) string {
		t.Helper()
		code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
			return runOnline(t.Context(), client, args, strings.NewReader(input), stdout, stderr)
		})
		if code != want {
			t.Fatalf("%v: exit=%d want=%d stderr=%s", args, code, want, errOut)
		}
		if want == ExitOK && !json.Valid([]byte(out)) {
			t.Fatalf("%v: stdout is not JSON: %s", args, out)
		}
		return strings.TrimSpace(out)
	}
	if got := run([]string{"config", "get", "log.file_omit_info"}, "", ExitOK); got != "true" {
		t.Fatalf("default log.file_omit_info = %s", got)
	}
	run([]string{"config", "set"}, `{"expected_revision":0,"settings":{"log":{"file_omit_info":false}}}`, ExitOK)
	if got := run([]string{"config", "get", "log.file_omit_info"}, "", ExitOK); got != "false" {
		t.Fatalf("log.file_omit_info after set = %s", got)
	}
	if got := run([]string{"config", "get", "log.level"}, "", ExitOK); got != `"INFO"` {
		t.Fatalf("partial set changed log.level to %s", got)
	}
	run([]string{"config", "set"}, `{"expected_revision":1,"settings":{"log":{"file_omit_info":"no"}}}`, ExitInvalidInput)
}
