package domain

// AccessMode discriminates the two shapes a campus account can take. It decides
// which half of the account's fields are meaningful, so it is validated before
// anything else about the account.
type AccessMode string

const (
	AccessModeWired AccessMode = "wired"
	AccessModeWiFi  AccessMode = "wifi"
)

func (m AccessMode) Valid() bool {
	return m == AccessModeWired || m == AccessModeWiFi
}

// APSelection is how a wireless campus account picks its access point.
//
// "strongest" is chosen once, when a connection is initiated. It is not a
// roaming policy: an account that is already associated and online must not
// re-select on every tick.
type APSelection string

const (
	APSelectionAuto      APSelection = "auto"
	APSelectionStrongest APSelection = "strongest"
	APSelectionFixed     APSelection = "fixed"
)

func (s APSelection) Valid() bool {
	return s == APSelectionAuto || s == APSelectionStrongest || s == APSelectionFixed
}

// CheckMode is the evidence level that counts as "online".
//
// These are deliberately different questions. A reachable gateway is not proof
// of Internet access, and an associated SSID is not proof of either.
type CheckMode string

const (
	// CheckInternet requires a positive Internet probe.
	CheckInternet CheckMode = "internet"
	// CheckPortal accepts the authentication gateway being reachable.
	CheckPortal CheckMode = "portal"
	// CheckSSID accepts association with the target SSID. A wired account in
	// this mode still needs a usable link and an IPv4 address: "no SSID to
	// match" is not the same as "connected".
	CheckSSID CheckMode = "ssid"
)

func (m CheckMode) Valid() bool {
	return m == CheckInternet || m == CheckPortal || m == CheckSSID
}

// LogLevel is the emit threshold.
//
// ALL is a threshold only, never an event level: it means "emit everything,
// including levels added later". The frozen LuCI selector offers all five, so
// dropping ALL would change a visible control (see decision D09).
type LogLevel string

const (
	LogAll   LogLevel = "ALL"
	LogDebug LogLevel = "DEBUG"
	LogInfo  LogLevel = "INFO"
	LogWarn  LogLevel = "WARN"
	LogError LogLevel = "ERROR"
)

// logThresholds orders the levels. ALL sorts below DEBUG so a future level
// below DEBUG is still emitted under ALL without changing stored settings.
var logThresholds = map[LogLevel]int{
	LogAll:   0,
	LogDebug: 10,
	LogInfo:  20,
	LogWarn:  30,
	LogError: 40,
}

func (l LogLevel) Valid() bool {
	_, ok := logThresholds[l]
	return ok
}

// Emits reports whether an event at level `event` passes threshold l.
// ALL is not a valid event level; asking whether it is emitted returns false.
func (l LogLevel) Emits(event LogLevel) bool {
	if event == LogAll {
		return false
	}
	eventWeight, ok := logThresholds[event]
	if !ok {
		return false
	}
	threshold, ok := logThresholds[l]
	if !ok {
		return false
	}
	return eventWeight >= threshold
}

// LogLevels lists the selectable thresholds in display order. The LuCI selector
// is generated from this list so the two cannot drift apart.
func LogLevels() []LogLevel {
	return []LogLevel{LogAll, LogDebug, LogInfo, LogWarn, LogError}
}

// AccessModes and APSelections exist for the same reason: one list, used by
// both validation and the read-only schema the UI renders.
func AccessModes() []AccessMode {
	return []AccessMode{AccessModeWired, AccessModeWiFi}
}

func APSelections() []APSelection {
	return []APSelection{APSelectionAuto, APSelectionStrongest, APSelectionFixed}
}

func CheckModes() []CheckMode {
	return []CheckMode{CheckInternet, CheckPortal, CheckSSID}
}
