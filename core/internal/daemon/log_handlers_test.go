//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
)

func tailLog(t *testing.T, service *running, params LogTailParams) LogTailResult {
	t.Helper()
	var result LogTailResult
	if err := json.Unmarshal(service.call("log.tail", params), &result); err != nil {
		t.Fatalf("decode log.tail: %v", err)
	}
	return result
}

// runAction submits one action and waits for it to finish, whatever it decides.
//
// The default runner is the real one, and an account this service does not have
// fails at once -- which is all these tests need: the log records the timeline,
// not the outcome.
func runAction(t *testing.T, service *running, account, key string) {
	t.Helper()
	var receipt SubmitResult
	if err := json.Unmarshal(service.call("action.submit", SubmitParams{
		Kind: "manual_login", AccountID: account, IdempotencyKey: key}),
		&receipt); err != nil {
		t.Fatalf("decode action.submit: %v", err)
	}
	service.awaitTerminal(receipt.ActionID)
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// Ready means the socket is listening, not that the initial maintenance tick
// has published its pause. Cursor and clear tests need that real log producer
// to settle before asserting an otherwise idle log boundary.
func startSettledLog(t *testing.T) *running {
	t.Helper()
	service := start(t, nil)
	awaitPause(t, service, func(reasons []string) bool {
		return len(reasons) == 1 && reasons[0] == "UserDisabled"
	})
	return service
}

// A run says so. Without it, an empty log and a service that never started look
// the same to somebody reading the panel.
func TestTheLogOpensWithWhatTheServiceStartedWith(t *testing.T) {
	service := start(t, nil)

	page := tailLog(t, service, LogTailParams{})
	for _, want := range []string{
		logstore.EventDaemonStart, "version=2.0.0rc1", logstore.EventConfigLoaded,
	} {
		if !containsLine(page.Lines, want) {
			t.Errorf("lines = %v\nmissing %s", page.Lines, want)
		}
	}
	if page.Cursor == 0 {
		t.Error("cursor = 0, want a position to continue from")
	}
	if page.Channel != ChannelPlugin {
		t.Errorf("channel = %q, want the default plugin channel", page.Channel)
	}
}

// One action, one timeline: queued, started, and the result a user reads after
// it failed. The progress dialog polls exactly this.
func TestAnActionsTimelineReachesTheLog(t *testing.T) {
	service := start(t, nil)
	// An account that exists, so the action is dispatched and runs. One that
	// does not is refused before it starts -- which is also correct, and is
	// what the queued-then-failed case below covers.
	service.writeConfig("campus.upsert",
		`{"expected_revision":0,"account":{"user_id":"2021001","wired_iface":"wan"}}`)
	before := tailLog(t, service, LogTailParams{}).Cursor
	runAction(t, service, "c1", "log-timeline")

	page := tailLog(t, service, LogTailParams{Cursor: before})
	for _, want := range []string{
		logstore.EventActionQueued, logstore.EventActionStart,
		logstore.EventActionResult, "account_id=c1", "kind=manual_login",
		"state=failed",
	} {
		if !containsLine(page.Lines, want) {
			t.Errorf("lines = %v\nmissing %s", page.Lines, want)
		}
	}
	// A failure is a warning, not routine information: the level prefix is what
	// the page colours the line by, and what a level filter selects on.
	for _, line := range page.Lines {
		if strings.Contains(line, logstore.EventActionResult) &&
			!strings.Contains(line, "WARN") {
			t.Errorf("a failed result was logged as %q, want WARN", line)
		}
	}
	// One line per transition. A republished action -- a cancellation arriving
	// while it still runs -- must not add a second "started".
	if got := countLines(page.Lines, logstore.EventActionStart); got != 1 {
		t.Errorf("%d start lines, want exactly 1", got)
	}
}

// A submission the coordinator refuses outright -- a stale revision, a
// scheduler-only kind -- never becomes an action, so there is nothing for the
// log to record and the caller gets the refusal in its own answer instead.
func TestARefusedSubmissionWritesNothing(t *testing.T) {
	service := startSettledLog(t)
	service.writeConfig("campus.upsert",
		`{"expected_revision":0,"account":{"user_id":"2021001","wired_iface":"wan"}}`)
	before := tailLog(t, service, LogTailParams{}).Cursor

	stale := uint64(0)
	err := service.callExpectingError("action.submit", SubmitParams{
		Kind: "manual_login", AccountID: "c1", IdempotencyKey: "stale-revision",
		ExpectedRevision: &stale})
	if err == nil || !strings.Contains(err.Error(), "配置已变化") {
		t.Fatalf("error = %v, want the stale revision refused", err)
	}
	if page := tailLog(t, service, LogTailParams{Cursor: before}); len(page.Lines) != 0 {
		t.Errorf("lines = %v, want nothing logged for an action that never existed",
			page.Lines)
	}
}

func countLines(lines []string, want string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, want) {
			count++
		}
	}
	return count
}

// The cursor is what makes a one-second poll cheap: the second call returns
// what arrived since the first, not the whole window again.
func TestTheCursorReturnsOnlyWhatIsNew(t *testing.T) {
	service := startSettledLog(t)
	first := tailLog(t, service, LogTailParams{})
	if len(first.Lines) == 0 {
		t.Fatal("the first page was empty")
	}

	repeat := tailLog(t, service, LogTailParams{Cursor: first.Cursor})
	if len(repeat.Lines) != 0 {
		t.Errorf("lines = %v, want nothing new", repeat.Lines)
	}
	if repeat.Cursor != first.Cursor {
		t.Errorf("cursor moved from %d to %d with nothing new",
			first.Cursor, repeat.Cursor)
	}

	runAction(t, service, "c1", "cursor-check")
	page := tailLog(t, service, LogTailParams{Cursor: first.Cursor})
	if !containsLine(page.Lines, logstore.EventActionResult) {
		t.Errorf("lines = %v, want the new action's records", page.Lines)
	}
	if containsLine(page.Lines, logstore.EventDaemonStart) {
		t.Errorf("lines = %v, want the earlier records left behind", page.Lines)
	}
}

// since is what the progress dialog sends, so that it shows its own action
// rather than everything that happened before the button was pressed.
func TestSinceHidesWhatHappenedBeforeIt(t *testing.T) {
	service := start(t, nil)
	future := service.status().WrittenAt.Add(time.Hour).Unix()

	page := tailLog(t, service, LogTailParams{Since: future})
	if len(page.Lines) != 0 {
		t.Errorf("lines = %v, want nothing after a future instant", page.Lines)
	}
}

// The network channel is the catalogue's own idea of which events are about the
// line. The page does not keep a second list.
func TestTheNetworkChannelIsFilteredByTheCatalogue(t *testing.T) {
	service := start(t, nil)
	runAction(t, service, "c1", "network-channel")

	page := tailLog(t, service, LogTailParams{Channel: ChannelNetwork})
	if page.Channel != ChannelNetwork {
		t.Errorf("channel = %q, want network", page.Channel)
	}
	if containsLine(page.Lines, logstore.EventDaemonStart) {
		t.Errorf("lines = %v, want the service's own events left out", page.Lines)
	}
	if !containsLine(page.Lines, logstore.EventActionResult) {
		t.Errorf("lines = %v, want the action's records kept", page.Lines)
	}
}

func TestAnUnknownChannelIsRefused(t *testing.T) {
	service := start(t, nil)
	err := service.callExpectingError("log.tail", LogTailParams{Channel: "syslog"})
	if err == nil || !strings.Contains(err.Error(), "日志通道") {
		t.Errorf("error = %v, want a refusal naming the channel", err)
	}
}

// Clearing is a user action with a boundary: the log a user just emptied says
// who emptied it, so an empty page and a cleared one are different states.
func TestClearingEmptiesTheLogAndRecordsThat(t *testing.T) {
	service := startSettledLog(t)
	if len(tailLog(t, service, LogTailParams{}).Lines) == 0 {
		t.Fatal("there was nothing to clear")
	}

	service.call("log.clear", nil)
	page := tailLog(t, service, LogTailParams{})
	if len(page.Lines) != 1 || !containsLine(page.Lines, logstore.EventLogCleared) {
		t.Errorf("lines = %v, want only the record of the clear", page.Lines)
	}

	var download LogDownloadResult
	if err := json.Unmarshal(service.call("log.download", LogDownloadParams{}),
		&download); err != nil {
		t.Fatalf("decode log.download: %v", err)
	}
	if strings.Contains(download.Text, logstore.EventDaemonStart) {
		t.Errorf("download = %q, want the cleared records gone", download.Text)
	}
	if !strings.Contains(download.Text, logstore.EventLogCleared) {
		t.Errorf("download = %q, want the clear itself recorded", download.Text)
	}
}

// The log is a file that outlives the action. A credential that reached it
// would still be there long after the attempt that used it.
func TestNoCredentialReachesTheLogFile(t *testing.T) {
	const secret = "log-probe-password"
	service := start(t, nil)

	service.writeConfig("campus.upsert", `{"expected_revision":0,"account":`+
		`{"user_id":"2021001","password":"`+secret+`","wired_iface":"wan"}}`)
	runAction(t, service, "c1", "secret-check")
	service.call("config.apply",
		json.RawMessage(`{"expected_revision":1,"settings":{"log":{"level":"ALL"}}}`))
	runAction(t, service, "c1", "secret-check-debug")

	data, err := os.ReadFile(service.paths.LogFile())
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Errorf("the log contains the account password:\n%s", data)
	}
	// The debug threshold is what a user turns on when something is wrong, and
	// it is exactly when more of the attempt reaches the log.
	if !strings.Contains(string(data), logstore.EventConfigApplied) {
		t.Errorf("the log has no record of the level change:\n%s", data)
	}
}
