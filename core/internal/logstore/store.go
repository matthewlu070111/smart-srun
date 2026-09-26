package logstore

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const (
	// MaxBytes bounds the log file. Spec 05 caps the plugin log at 2 MiB; this
	// is well inside it, and it is the size the baseline used, so a device that
	// was coping before still is.
	MaxBytes = 512 << 10
	// KeepBytes is how much survives a rotation. Half rather than all-but-one
	// line, so rotation happens once in a while instead of on every write once
	// the file is full.
	KeepBytes = MaxBytes / 2
	// MemoryRecords is the tail a reader can page through by cursor. A poll
	// every second with a browser open needs the last few hundred lines, not
	// the file; anything older is still in the file for a download.
	MemoryRecords = 500
	// MaxTailLines bounds one answer, so a caller asking for everything cannot
	// make the daemon build a megabyte of response.
	MaxTailLines = 2000
	// FileMode keeps the log readable only by root, like everything else this
	// service writes: a log line naming an account is not public.
	FileMode os.FileMode = 0o600
	DirMode  os.FileMode = 0o700
)

// entry is one remembered record plus the cursor that identifies it.
type entry struct {
	sequence uint64
	at       time.Time
	line     string
	event    string
}

// Store is the daemon's event log: a bounded file plus the tail of it in
// memory.
//
// Both, not one or the other. The file is what a user downloads and what
// survives a restart; the memory is what a page polling every second reads,
// because re-reading and re-parsing the file for each poll is how a status
// panel turns into a background load on a router.
type Store struct {
	mu        sync.Mutex
	path      string
	threshold domain.LogLevel
	sequence  uint64
	records   []entry
	// dropped counts records that left the memory window, so a reader whose
	// cursor is older than the window can be told rather than silently handed a
	// gap.
	dropped uint64
	// size tracks the file so the common case does not stat it on every write.
	size    int64
	sizeOK  bool
	onError func(error)
}

// New builds a store writing to path. A nil-safe zero value is deliberately not
// supported: a logger that silently discards everything is worse than one that
// fails to be constructed.
func New(path string) *Store {
	return &Store{path: path, threshold: domain.LogInfo, onError: func(error) {}}
}

// OnError receives failures from writing the log itself. They cannot be logged
// -- that is the thing that just failed -- so they go to whatever the daemon
// uses for faults with nowhere to return to.
func (s *Store) OnError(handler func(error)) {
	if handler == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onError = handler
}

// SetLevel changes the emit threshold. ALL means everything, including levels
// added later; it is never an event's own level.
func (s *Store) SetLevel(level domain.LogLevel) {
	if !level.Valid() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threshold = level
}

func (s *Store) Level() domain.LogLevel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threshold
}

// Emit records one event.
//
// It never returns an error and never blocks on anything but its own mutex: the
// callers are the coordinator's single-writer loop and the paths that report
// faults, and a log that could fail an action by failing to write itself would
// be worse than no log.
func (s *Store) Emit(at time.Time, level domain.LogLevel, event, message string,
	fields ...Field) {
	record := Record{At: at, Level: level, Event: event, Fields: fields,
		Message: message}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.threshold.Emits(level) {
		return
	}
	s.sequence++
	line := record.Line()
	s.records = append(s.records, entry{sequence: s.sequence, at: at,
		line: line, event: event})
	if len(s.records) > MemoryRecords {
		s.dropped += uint64(len(s.records) - MemoryRecords)
		s.records = append([]entry(nil), s.records[len(s.records)-MemoryRecords:]...)
	}
	s.append(line)
}

// append writes one line, rotating first if the file has grown past its budget.
func (s *Store) append(line string) {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), DirMode); err != nil {
		s.onError(domain.Errorf(domain.CodeInternal, "无法创建日志目录").Wrap(err))
		return
	}
	if !s.sizeOK {
		if info, err := os.Stat(s.path); err == nil {
			s.size = info.Size()
		} else {
			s.size = 0
		}
		s.sizeOK = true
	}
	if s.size > MaxBytes {
		s.rotate()
	}

	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, FileMode)
	if err != nil {
		s.sizeOK = false
		s.onError(domain.Errorf(domain.CodeInternal, "无法写入日志").Wrap(err))
		return
	}
	written, err := file.WriteString(line + "\n")
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	s.size += int64(written)
	if err != nil {
		s.sizeOK = false
		s.onError(domain.Errorf(domain.CodeInternal, "无法写入日志").Wrap(err))
	}
	// No fsync, deliberately. This file lives on tmpfs and a reboot takes it
	// either way; syncing every event would be flash wear on the one path that
	// runs continuously.
}

// rotate keeps the newest half of the file and drops the rest.
//
// Whole lines only: a truncated first line would be rendered as raw text by the
// page's parser, which expects every line to start with a timestamp.
func (s *Store) rotate() {
	kept, offset, err := s.readTail()
	if err != nil {
		s.sizeOK = false
		return
	}
	if offset > 0 {
		if index := bytes.IndexByte(kept, '\n'); index >= 0 {
			kept = kept[index+1:]
		}
	}
	if err := replaceFile(s.path, kept); err != nil {
		s.sizeOK = false
		s.onError(err)
		return
	}
	s.size = int64(len(kept))
}

// readTail reads the newest KeepBytes of the file and closes it.
//
// Closing before the replacement matters: a rename onto a file this process
// still holds open is refused outright on Windows, where the development hosts
// run their fast tests, and the failure would be a log that stops growing
// rather than one that rotates.
func (s *Store) readTail() ([]byte, int64, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	offset := max(info.Size()-KeepBytes, 0)
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	kept, err := io.ReadAll(io.LimitReader(file, KeepBytes+1))
	if err != nil {
		return nil, 0, err
	}
	return kept, offset, nil
}

// replaceFile swaps the file's contents atomically, so a reader never sees a
// half-rotated log.
func replaceFile(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".log-*.tmp")
	if err != nil {
		return domain.Errorf(domain.CodeInternal, "无法创建临时日志").Wrap(err)
	}
	name := temp.Name()
	defer os.Remove(name)

	if err := temp.Chmod(FileMode); err != nil {
		temp.Close()
		return domain.Errorf(domain.CodeInternal, "无法设置日志权限").Wrap(err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return domain.Errorf(domain.CodeInternal, "无法写入临时日志").Wrap(err)
	}
	if err := temp.Close(); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法关闭临时日志").Wrap(err)
	}
	if err := os.Rename(name, path); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法发布日志").Wrap(err)
	}
	return nil
}

// Page is one answer to a tail request.
type Page struct {
	Lines []string
	// Cursor is what to pass next time. It is the sequence of the last record
	// returned, so an empty page leaves it where it was.
	Cursor uint64
	// Dropped says records were lost between the caller's cursor and the oldest
	// one still remembered. A reader that has to know it missed something --
	// and a progress dialog does -- would otherwise just see a quiet gap.
	Dropped bool
}

// TailQuery selects records.
type TailQuery struct {
	// Cursor returns only records after this sequence. Zero means "from the
	// oldest remembered", which is what a freshly opened page wants.
	Cursor uint64
	// Since drops records older than this instant. The progress dialog uses it
	// to show only what happened after the action it submitted.
	Since time.Time
	// Limit bounds the answer. Zero means MaxTailLines.
	Limit int
	// Events, when non-empty, keeps only these event names. It is how the
	// network channel is assembled without the page having to know which events
	// are about the network.
	Events map[string]bool
}

// Tail answers from memory.
func (s *Store) Tail(query TailQuery) Page {
	limit := query.Limit
	if limit <= 0 || limit > MaxTailLines {
		limit = MaxTailLines
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	page := Page{Cursor: s.sequence}
	oldest := uint64(0)
	if len(s.records) > 0 {
		oldest = s.records[0].sequence
	}
	// A cursor older than the window means the gap is real, not empty.
	page.Dropped = query.Cursor > 0 && oldest > query.Cursor+1

	for _, record := range s.records {
		if record.sequence <= query.Cursor {
			continue
		}
		if !query.Since.IsZero() && record.at.Before(query.Since) {
			continue
		}
		if len(query.Events) > 0 && !query.Events[record.event] {
			continue
		}
		page.Lines = append(page.Lines, record.line)
	}
	if len(page.Lines) > limit {
		// Keep the newest: a page that showed the oldest N of a burst would
		// scroll a user back in time as the burst grew.
		page.Dropped = true
		page.Lines = page.Lines[len(page.Lines)-limit:]
	}
	return page
}

// Download reads the whole file, bounded.
//
// From the file rather than from memory: this is the answer to "send me the
// log", and the memory window is deliberately much shorter than the file.
func (s *Store) Download(limit int) (string, error) {
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	if path == "" {
		return "", nil
	}

	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", domain.Errorf(domain.CodeInternal, "无法读取日志").Wrap(err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return "", domain.Errorf(domain.CodeInternal, "无法读取日志").Wrap(err)
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return "", nil
	}
	if limit > 0 {
		lines := strings.Split(text, "\n")
		if len(lines) > limit {
			lines = lines[len(lines)-limit:]
		}
		text = strings.Join(lines, "\n")
	}
	return text, nil
}

// Clear empties this project's log and nothing else.
//
// The file is truncated rather than removed, so a reader holding it open does
// not keep the old contents alive. The memory window is cleared with it, and
// the sequence keeps counting: a cursor from before a clear must not match a
// record written after it.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = nil
	s.dropped = 0
	s.size = 0
	s.sizeOK = true
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), DirMode); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法创建日志目录").Wrap(err)
	}
	if err := replaceFile(s.path, nil); err != nil {
		return err
	}
	return nil
}
