package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// BeijingOffsetSeconds fixes the quiet-hours timezone at UTC+08:00.
//
// Quiet hours are a campus-network policy expressed in Beijing time. Reading
// the router's local zone would make the window move when someone fixes the
// device clock, so the offset is part of the contract, not configuration.
const BeijingOffsetSeconds = 8 * 60 * 60

// Beijing is the fixed zone quiet hours are evaluated in.
var Beijing = time.FixedZone("UTC+8", BeijingOffsetSeconds)

// ClockTime is a wall-clock minute of the day, serialized as "HH:MM".
//
// Storing minutes rather than a string means an invalid time cannot exist past
// decoding, and comparisons are ordinary integer comparisons.
type ClockTime struct {
	minutes int
}

// NewClockTime builds a ClockTime from hour and minute.
func NewClockTime(hour, minute int) (ClockTime, error) {
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return ClockTime{}, fmt.Errorf("clock time out of range: %02d:%02d", hour, minute)
	}
	return ClockTime{minutes: hour*60 + minute}, nil
}

// ParseClockTime accepts exactly "HH:MM" with two digits on each side.
//
// Lenient forms are rejected on purpose: "6:00", " 06:00" and "06:00:00" all
// mean someone typed something the UI did not produce, and silently accepting
// them hides the real problem.
func ParseClockTime(text string) (ClockTime, error) {
	if len(text) != 5 || text[2] != ':' {
		return ClockTime{}, fmt.Errorf("时间必须是 HH:MM 格式：%q", text)
	}
	hour, ok := twoDigits(text[0:2])
	if !ok {
		return ClockTime{}, fmt.Errorf("时间必须是 HH:MM 格式：%q", text)
	}
	minute, ok := twoDigits(text[3:5])
	if !ok {
		return ClockTime{}, fmt.Errorf("时间必须是 HH:MM 格式：%q", text)
	}
	return NewClockTime(hour, minute)
}

func twoDigits(pair string) (int, bool) {
	if pair[0] < '0' || pair[0] > '9' || pair[1] < '0' || pair[1] > '9' {
		return 0, false
	}
	return int(pair[0]-'0')*10 + int(pair[1]-'0'), true
}

func (c ClockTime) Hour() int      { return c.minutes / 60 }
func (c ClockTime) Minute() int    { return c.minutes % 60 }
func (c ClockTime) Minutes() int   { return c.minutes }
func (c ClockTime) String() string { return fmt.Sprintf("%02d:%02d", c.Hour(), c.Minute()) }

func (c ClockTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

func (c *ClockTime) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("时间必须是 HH:MM 字符串: %w", err)
	}
	parsed, err := ParseClockTime(text)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// QuietWindow is a half-open [start, end) window in Beijing time.
//
// start == end is an empty window, not a 24-hour one. Treating it as all-day
// would silently log every account out forever the moment a user typed the same
// value twice.
type QuietWindow struct {
	Start ClockTime
	End   ClockTime
}

// IsEmpty reports the start == end case.
func (w QuietWindow) IsEmpty() bool { return w.Start.minutes == w.End.minutes }

// WrapsMidnight reports whether the window is the union of [start, 24:00) and
// [00:00, end).
func (w QuietWindow) WrapsMidnight() bool { return w.Start.minutes > w.End.minutes }

// Contains reports whether an instant falls inside the window. The instant is
// converted to Beijing time first, so the caller may pass any zone.
func (w QuietWindow) Contains(at time.Time) bool {
	if w.IsEmpty() {
		return false
	}
	local := at.In(Beijing)
	minute := local.Hour()*60 + local.Minute()
	if w.WrapsMidnight() {
		return minute >= w.Start.minutes || minute < w.End.minutes
	}
	return minute >= w.Start.minutes && minute < w.End.minutes
}

// OccurrenceID identifies the single window occurrence containing `at`.
//
// Forced logout must happen once per occurrence. Deriving the identity from the
// occurrence's start instant means a clock correction inside the window maps to
// the same ID, so a corrected clock does not trigger a second logout sweep.
// Returns false when `at` is outside the window.
func (w QuietWindow) OccurrenceID(at time.Time) (string, bool) {
	if !w.Contains(at) {
		return "", false
	}
	local := at.In(Beijing)
	minute := local.Hour()*60 + local.Minute()
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, Beijing)
	if w.WrapsMidnight() && minute < w.End.minutes {
		// Still inside the occurrence that began on the previous day.
		day = day.AddDate(0, 0, -1)
	}
	start := day.Add(time.Duration(w.Start.minutes) * time.Minute)
	return start.Format("20060102T1504-0700"), true
}

// NextBoundary returns the next instant at which membership changes.
//
// The scheduler waits until this instant instead of polling, and re-derives it
// whenever the clock jumps.
func (w QuietWindow) NextBoundary(at time.Time) (time.Time, bool) {
	if w.IsEmpty() {
		return time.Time{}, false
	}
	local := at.In(Beijing)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, Beijing)
	candidates := []time.Time{
		day.Add(time.Duration(w.Start.minutes) * time.Minute),
		day.Add(time.Duration(w.End.minutes) * time.Minute),
	}
	best := time.Time{}
	for _, candidate := range candidates {
		for _, shifted := range []time.Time{candidate, candidate.AddDate(0, 0, 1)} {
			if shifted.After(local) && (best.IsZero() || shifted.Before(best)) {
				best = shifted
			}
		}
	}
	return best, !best.IsZero()
}
