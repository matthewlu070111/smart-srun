package domain

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestSecondsRejectsValuesThatWouldBreakAWait(t *testing.T) {
	// NaN and ±Inf cannot appear as JSON literals, so they arrive as text. Both
	// paths are checked: the decoder for documents, and the check itself for a
	// value computed in Go.
	for _, document := range []string{
		`"NaN"`, `"Inf"`, `-1`, `-0.5`, `1e400`, `"10"`, `true`, `null`, `[10]`,
	} {
		var value Seconds
		if err := json.Unmarshal([]byte(document), &value); err == nil {
			t.Errorf("accepted %s as Seconds (got %v)", document, value)
		}
	}

	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 1 << 40} {
		if err := checkSeconds(bad); err == nil {
			t.Errorf("checkSeconds(%v) accepted the value", bad)
		}
	}
}

func TestSecondsAcceptsFractionalCooldowns(t *testing.T) {
	// The baseline parsed cooldowns with float(), so sub-second values are an
	// existing capability. Rounding them to whole seconds would change a
	// working configuration on upgrade.
	var value Seconds
	if err := json.Unmarshal([]byte(`0.25`), &value); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if value != 0.25 {
		t.Fatalf("value = %v, want 0.25", value)
	}
	if got := value.Duration(); got != 250*time.Millisecond {
		t.Fatalf("Duration() = %v, want 250ms", got)
	}
}

func TestSecondsDurationConversion(t *testing.T) {
	cases := map[Seconds]time.Duration{
		0:    0,
		1:    time.Second,
		1.5:  1500 * time.Millisecond,
		3600: time.Hour,
	}
	for value, want := range cases {
		if got := value.Duration(); got != want {
			t.Errorf("Seconds(%v).Duration() = %v, want %v", value, got, want)
		}
	}
	// Negative values cannot come from decoding, but a caller could construct
	// one; a negative wait must be a zero wait, never a busy loop.
	if got := Seconds(-5).Duration(); got != 0 {
		t.Errorf("negative Seconds produced %v, want 0", got)
	}
}

func TestSecondsStringKeepsWholeNumbersWhole(t *testing.T) {
	cases := map[Seconds]string{10: "10", 60: "60", 0: "0", 0.5: "0.5"}
	for value, want := range cases {
		if got := value.String(); got != want {
			t.Errorf("Seconds(%v).String() = %q, want %q", float64(value), got, want)
		}
	}
}
