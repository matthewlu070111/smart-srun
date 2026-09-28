package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func connectivityLogDaemon(level domain.LogLevel) *Daemon {
	events := logstore.New("")
	events.SetLevel(level)
	return &Daemon{events: events, clock: faketime.New(time.Unix(1_800_000_000, 0)),
		published: map[string]string{}}
}

func finishedMaintain(diagnosis *application.ConnectivityDiagnosis) application.Action {
	return application.Action{ID: "a1", State: application.StateSucceeded,
		Request:      application.Request{Kind: application.KindMaintain, AccountID: "c1"},
		Message:      "本线路已是该账号的在线会话",
		Connectivity: diagnosis}
}

// Issue #64: the fallback is visible at DEBUG, and says which way it went.
func TestSystemRouteConfirmationIsLoggedAtDebug(t *testing.T) {
	d := connectivityLogDaemon(domain.LogDebug)
	d.logAction(finishedMaintain(&application.ConnectivityDiagnosis{
		Via: "system", Bound: "connect.rom.miui.com=dns", BoundLast: "dns",
		System: "connect.rom.miui.com=ok", Fallback: "used"}))
	lines := d.events.Tail(logstore.TailQuery{}).Lines
	var found string
	for _, line := range lines {
		if strings.Contains(line, logstore.EventConnectivityProbe) {
			found = line
		}
	}
	for _, want := range []string{"DEBUG", "via=system", "bound_last_error=dns",
		"system_fallback=used", "account_id=c1", "系统路由"} {
		if !strings.Contains(found, want) {
			t.Fatalf("connectivity line %q missing %q (all: %v)", found, want, lines)
		}
	}
}

func TestConnectivityDiagnosisStaysOutOfTheDefaultLog(t *testing.T) {
	d := connectivityLogDaemon(domain.LogInfo)
	d.logAction(finishedMaintain(&application.ConnectivityDiagnosis{Via: "", Bound: "a=timeout",
		Fallback: "skipped_multi_wan"}))
	d.logAction(application.Action{ID: "a2", State: application.StateSucceeded,
		Request: application.Request{Kind: application.KindMaintain}})
	for _, line := range d.events.Tail(logstore.TailQuery{}).Lines {
		if strings.Contains(line, logstore.EventConnectivityProbe) {
			t.Fatalf("DEBUG diagnosis written at INFO: %q", line)
		}
	}
	d = connectivityLogDaemon(domain.LogDebug)
	d.logAction(finishedMaintain(&application.ConnectivityDiagnosis{Bound: "a=timeout", Fallback: "skipped_multi_wan"}))
	lines := strings.Join(d.events.Tail(logstore.TailQuery{}).Lines, "\n")
	if !strings.Contains(lines, "via=none") || !strings.Contains(lines, "system_fallback=skipped_multi_wan") {
		t.Fatalf("lines = %s", lines)
	}
}
