package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// maxSecondsValue bounds any duration-bearing field well inside the range where
// float64 seconds convert to time.Duration without overflow.
const maxSecondsValue = 1 << 30

// Seconds is a non-negative, finite duration in seconds that may be fractional.
//
// The baseline stored retry cooldowns as strings parsed with float(), and some
// users rely on sub-second cooldowns, so this is not an int. It is a distinct
// type rather than a bare float64 so that the NaN/Inf and overflow checks
// cannot be skipped at one call site.
type Seconds float64

func (s Seconds) Float() float64 { return float64(s) }

func (s Seconds) String() string {
	if s == Seconds(math.Trunc(float64(s))) {
		return fmt.Sprintf("%d", int64(s))
	}
	return fmt.Sprintf("%g", float64(s))
}

// Duration converts to time.Duration. The value was range-checked at decode
// time, so this cannot overflow; the guard is kept because a future caller
// could construct Seconds directly.
func (s Seconds) Duration() time.Duration {
	if float64(s) >= maxSecondsValue {
		return maxSecondsValue * time.Second
	}
	if s <= 0 {
		return 0
	}
	return time.Duration(float64(s) * float64(time.Second))
}

func (s Seconds) MarshalJSON() ([]byte, error) {
	if err := checkSeconds(float64(s)); err != nil {
		return nil, err
	}
	return json.Marshal(float64(s))
}

func (s *Seconds) UnmarshalJSON(data []byte) error {
	// encoding/json hands null to an Unmarshaler and treats "leave it alone" as
	// success, so null would silently mean zero seconds -- a busy loop where the
	// user expected a wait. Reject it here too, not only in the strict scanner.
	if string(data) == "null" {
		return fmt.Errorf("秒数不能是 null")
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("秒数必须是数字: %w", err)
	}
	if err := checkSeconds(value); err != nil {
		return err
	}
	*s = Seconds(value)
	return nil
}

// checkSeconds rejects the values that would turn a wait into a crash or a
// busy loop: NaN, ±Inf, negatives, and magnitudes that overflow a Duration.
func checkSeconds(value float64) error {
	if math.IsNaN(value) {
		return fmt.Errorf("秒数不能是 NaN")
	}
	if math.IsInf(value, 0) {
		return fmt.Errorf("秒数不能是无穷大")
	}
	if value < 0 {
		return fmt.Errorf("秒数不能为负：%g", value)
	}
	if value >= maxSecondsValue {
		return fmt.Errorf("秒数超出可表示范围：%g", value)
	}
	return nil
}
