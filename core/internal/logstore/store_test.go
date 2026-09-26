package logstore

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", "plugin.log")
	store := New(path)
	store.OnError(func(err error) { t.Errorf("unexpected log fault: %v", err) })
	return store, path
}

func emit(store *Store, count int, event string) {
	for index := range count {
		store.Emit(noon.Add(time.Duration(index)*time.Second), domain.LogInfo,
			event, "", F("n", strings.Repeat("x", 8)))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestAnEmittedRecordReachesBothTheFileAndTheTail(t *testing.T) {
	store, path := newStore(t)
	store.Emit(noon, domain.LogInfo, EventDaemonStart, "启动", F("version", "1.2.3"))

	page := store.Tail(TailQuery{})
	if len(page.Lines) != 1 || !strings.Contains(page.Lines[0], "daemon_start") {
		t.Fatalf("tail = %v, want the record", page.Lines)
	}
	if page.Cursor != 1 {
		t.Errorf("cursor = %d, want 1", page.Cursor)
	}
	if got := readFile(t, path); !strings.Contains(got, "version=1.2.3") {
		t.Errorf("file = %q, want the record", got)
	}
}

// The threshold is the user's setting, and it applies before anything is
// written: a DEBUG line the user asked not to see must not reach the file
// either, or the download would contain what the panel hid.
func TestTheThresholdDecidesBeforeAnythingIsWritten(t *testing.T) {
	store, path := newStore(t)
	store.Emit(noon, domain.LogDebug, EventActionPhase, "")
	if page := store.Tail(TailQuery{}); len(page.Lines) != 0 {
		t.Fatalf("tail = %v, want nothing at the default threshold", page.Lines)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat = %v, want no file written at all", err)
	}

	store.SetLevel(domain.LogAll)
	store.Emit(noon, domain.LogDebug, EventActionPhase, "")
	if page := store.Tail(TailQuery{}); len(page.Lines) != 1 {
		t.Errorf("tail = %v, want the debug record once ALL is set", page.Lines)
	}

	store.SetLevel(domain.LogError)
	store.Emit(noon, domain.LogWarn, EventRetryScheduled, "")
	if page := store.Tail(TailQuery{}); len(page.Lines) != 1 {
		t.Errorf("tail = %v, want the warning dropped at ERROR", page.Lines)
	}
}

// ALL is a threshold, never an event's own level. Emitting at ALL would be a
// record with no severity, which nothing could filter.
func TestAnEventCannotBeEmittedAtTheAllThreshold(t *testing.T) {
	store, _ := newStore(t)
	store.SetLevel(domain.LogAll)
	store.Emit(noon, domain.LogAll, EventDaemonStart, "")
	if page := store.Tail(TailQuery{}); len(page.Lines) != 0 {
		t.Errorf("tail = %v, want nothing", page.Lines)
	}
}

func TestACursorReturnsOnlyWhatArrivedAfterIt(t *testing.T) {
	store, _ := newStore(t)
	emit(store, 3, EventActionStart)

	first := store.Tail(TailQuery{})
	if len(first.Lines) != 3 {
		t.Fatalf("first page = %d lines, want 3", len(first.Lines))
	}
	if page := store.Tail(TailQuery{Cursor: first.Cursor}); len(page.Lines) != 0 {
		t.Errorf("second page = %v, want nothing new", page.Lines)
	}

	store.Emit(noon.Add(time.Minute), domain.LogInfo, EventActionResult, "完成")
	page := store.Tail(TailQuery{Cursor: first.Cursor})
	if len(page.Lines) != 1 || !strings.Contains(page.Lines[0], "完成") {
		t.Errorf("page = %v, want only the new record", page.Lines)
	}
	if page.Dropped {
		t.Error("dropped = true, want false: nothing left the window")
	}
}

// The progress dialog polls with the moment it submitted, so that it shows its
// own action's lines rather than everything that came before the click.
func TestSinceDropsWhatHappenedBeforeIt(t *testing.T) {
	store, _ := newStore(t)
	store.Emit(noon, domain.LogInfo, EventActionResult, "旧的")
	store.Emit(noon.Add(time.Hour), domain.LogInfo, EventActionResult, "新的")

	page := store.Tail(TailQuery{Since: noon.Add(30 * time.Minute)})
	if len(page.Lines) != 1 || !strings.Contains(page.Lines[0], "新的") {
		t.Errorf("page = %v, want only the newer record", page.Lines)
	}
}

func TestTheNetworkChannelKeepsOnlyItsOwnEvents(t *testing.T) {
	store, _ := newStore(t)
	store.Emit(noon, domain.LogInfo, EventDaemonStart, "")
	store.Emit(noon, domain.LogInfo, EventActionStart, "")

	page := store.Tail(TailQuery{Events: NetworkEvents()})
	if len(page.Lines) != 1 || !strings.Contains(page.Lines[0], EventActionStart) {
		t.Errorf("page = %v, want only the network event", page.Lines)
	}
	if !NetworkEvents()[EventActionResult] || NetworkEvents()[EventDaemonStop] {
		t.Error("the network set does not match the catalogue's own flags")
	}
}

// A burst keeps its newest end. A page that showed the oldest N would scroll a
// user backwards as the burst grew.
func TestALimitKeepsTheNewestLines(t *testing.T) {
	store, _ := newStore(t)
	emit(store, 10, EventActionPhase)
	store.SetLevel(domain.LogAll)
	store.Emit(noon.Add(time.Hour), domain.LogInfo, EventActionResult, "最后一条")

	page := store.Tail(TailQuery{Limit: 2})
	if len(page.Lines) != 2 {
		t.Fatalf("page = %d lines, want 2", len(page.Lines))
	}
	if !strings.Contains(page.Lines[1], "最后一条") {
		t.Errorf("page = %v, want the newest record last", page.Lines)
	}
	if !page.Dropped {
		t.Error("dropped = false, want true: the answer is not the whole selection")
	}
}

// A reader whose cursor is older than the memory window has missed records. It
// is told so, rather than handed a page with a silent hole in it.
func TestACursorOlderThanTheWindowIsReportedAsDropped(t *testing.T) {
	store, _ := newStore(t)
	emit(store, MemoryRecords+50, EventActionStart)

	page := store.Tail(TailQuery{Cursor: 1})
	if !page.Dropped {
		t.Error("dropped = false, want true")
	}
	if len(page.Lines) > MemoryRecords {
		t.Errorf("page = %d lines, want at most the window", len(page.Lines))
	}
}

// The file is bounded, and rotation cuts between records: a half line would be
// rendered as raw text by a parser that expects every line to start with a
// timestamp.
func TestTheFileStaysWithinItsBudgetAndKeepsWholeLines(t *testing.T) {
	store, path := newStore(t)
	const records = 700
	filler := strings.Repeat("y", MaxMessageBytes)
	for index := range records {
		store.Emit(noon.Add(time.Duration(index)*time.Second), domain.LogInfo,
			EventActionResult, filler)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > MaxBytes {
		t.Errorf("file is %d bytes, want it inside the %d budget", info.Size(), MaxBytes)
	}
	content := readFile(t, path)
	for line := range strings.SplitSeq(strings.TrimRight(content, "\n"), "\n") {
		if !strings.HasPrefix(line, "[2026-") {
			t.Fatalf("a line does not start with a timestamp: %.60s", line)
		}
	}
	// The newest record survives a rotation; the oldest is what is dropped.
	if !strings.Contains(content, noon.Add((records-1)*time.Second).In(domain.Beijing).
		Format("2006-01-02 15:04:05")) {
		t.Error("the newest record did not survive rotation")
	}
	if strings.Contains(content, noon.In(domain.Beijing).Format("2006-01-02 15:04:05")+"]") {
		t.Error("the oldest record survived a rotation that should have dropped it")
	}
}

func TestDownloadReadsTheFileAndCanBeBounded(t *testing.T) {
	store, _ := newStore(t)
	emit(store, 5, EventActionStart)

	text, err := store.Download(0)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := strings.Count(text, "\n") + 1; got != 5 {
		t.Errorf("download = %d lines, want 5", got)
	}
	bounded, err := store.Download(2)
	if err != nil {
		t.Fatalf("bounded download: %v", err)
	}
	if got := strings.Count(bounded, "\n") + 1; got != 2 {
		t.Errorf("bounded download = %d lines, want 2", got)
	}
	if !strings.HasSuffix(bounded, strings.Split(text, "\n")[4]) {
		t.Error("a bounded download did not keep the newest lines")
	}
}

func TestDownloadingAnAbsentLogIsEmptyRatherThanAnError(t *testing.T) {
	store, _ := newStore(t)
	text, err := store.Download(0)
	if err != nil || text != "" {
		t.Errorf("download = %q, %v; want empty and no error", text, err)
	}
}

// Clearing empties the file and the window, and the cursor keeps counting: a
// reader holding a cursor from before the clear must not be handed records
// written after it as if nothing had happened.
func TestClearEmptiesBothHalvesAndKeepsTheSequenceMoving(t *testing.T) {
	store, path := newStore(t)
	emit(store, 3, EventActionStart)
	before := store.Tail(TailQuery{}).Cursor

	if err := store.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := readFile(t, path); got != "" {
		t.Errorf("file = %q, want empty", got)
	}
	page := store.Tail(TailQuery{})
	if len(page.Lines) != 0 {
		t.Errorf("tail = %v, want empty", page.Lines)
	}

	store.Emit(noon, domain.LogInfo, EventLogCleared, "")
	after := store.Tail(TailQuery{Cursor: before})
	if len(after.Lines) != 1 {
		t.Errorf("tail after clear = %v, want the one new record", after.Lines)
	}
	if store.Tail(TailQuery{}).Cursor <= before {
		t.Error("the cursor went backwards across a clear")
	}
}

// Emit runs on the coordinator's goroutine and the fault path; Tail runs on
// whichever connection is polling. Under -race this is the check that the two
// do not share anything unguarded.
func TestEmittingAndTailingAreSafeInParallel(t *testing.T) {
	store, _ := newStore(t)
	var waiting sync.WaitGroup
	for worker := range 4 {
		waiting.Go(func() {
			for index := range 50 {
				store.Emit(noon.Add(time.Duration(index)*time.Second), domain.LogInfo,
					EventActionResult, "", F("worker", string(rune('a'+worker))))
			}
		})
	}
	waiting.Go(func() {
		for range 100 {
			store.Tail(TailQuery{Limit: 10})
		}
	})
	waiting.Go(func() {
		for range 5 {
			if _, err := store.Download(10); err != nil {
				t.Errorf("download: %v", err)
			}
		}
	})
	waiting.Wait()

	if got := store.Tail(TailQuery{}).Cursor; got != 200 {
		t.Errorf("cursor = %d, want 200 records", got)
	}
}

// A store that cannot write still answers: the daemon must not lose its
// scheduling loop because /tmp is full or read-only.
func TestAFailedWriteIsReportedRatherThanFatal(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	store := New(filepath.Join(blocked, "plugin.log"))
	var faults int
	store.OnError(func(error) { faults++ })

	store.Emit(noon, domain.LogInfo, EventDaemonStart, "")
	if faults == 0 {
		t.Error("a write failure went unreported")
	}
	if page := store.Tail(TailQuery{}); len(page.Lines) != 1 {
		t.Errorf("tail = %v, want the record kept in memory anyway", page.Lines)
	}
}
