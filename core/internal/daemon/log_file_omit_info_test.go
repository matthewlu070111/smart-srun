//go:build unix

package daemon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
)

func logFile(t *testing.T, service *running) string {
	t.Helper()
	data, err := os.ReadFile(service.paths.LogFile())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// The default configuration keeps INFO out of plugin.log while log.tail (the
// LuCI live view) still has it, and a save that turns the switch off applies
// on the same transaction.
func TestLogFileOmitInfoFollowsTheConfiguration(t *testing.T) {
	service := start(t, nil)
	page := tailLog(t, service, LogTailParams{})
	if !containsLine(page.Lines, logstore.EventConfigLoaded) {
		t.Fatalf("tail lacks config_loaded: %v", page.Lines)
	}
	file := logFile(t, service)
	if !strings.Contains(file, logstore.EventDaemonStart) {
		t.Fatalf("lifecycle line missing from file: %q", file)
	}
	if strings.Contains(file, logstore.EventConfigLoaded) {
		t.Fatalf("INFO written although log.file_omit_info defaults on: %q", file)
	}

	service.writeConfig("config.apply", `{"expected_revision":0,"settings":{"log":{"file_omit_info":false}}}`)
	file = logFile(t, service)
	if !strings.Contains(file, logstore.EventConfigApplied) {
		t.Fatalf("config_applied missing from file: %q", file)
	}
	service.writeConfig("campus.upsert",
		`{"expected_revision":1,"account":{"user_id":"2021001","wired_iface":"wan"}}`)
	runAction(t, service, "c1", "file-omit-info-off")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logFile(t, service), logstore.EventActionQueued) {
		if time.Now().After(deadline) {
			t.Fatalf("INFO not written after the switch was turned off: %q", logFile(t, service))
		}
		time.Sleep(20 * time.Millisecond)
	}

	service.writeConfig("config.apply", `{"expected_revision":2,"settings":{"log":{"file_omit_info":true}}}`)
	before := logFile(t, service)
	runAction(t, service, "c1", "file-omit-info-on")
	added := ""
	for deadline := time.Now().Add(5 * time.Second); ; {
		added = strings.TrimPrefix(logFile(t, service), before)
		if strings.Contains(added, logstore.EventActionResult) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(added, logstore.EventActionQueued) || strings.Contains(added, logstore.EventActionStart) {
		t.Fatalf("INFO written after the switch was turned back on: %q", added)
	}
	if !strings.Contains(added, "WARN") || !strings.Contains(added, logstore.EventActionResult) {
		t.Fatalf("a failed result (WARN) must still reach the file: %q", added)
	}
}
