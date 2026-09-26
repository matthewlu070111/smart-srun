package openwrt

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// RadioInfo is what iwinfo reports about one wireless interface right now.
//
// This is the observed state, as opposed to the configured state in UCI. A
// configured SSID says what should happen; this says what did. Spec 04 needs
// both, because a client section pointing at the campus network proves nothing
// about whether it associated.
type RadioInfo struct {
	Device     string
	PHY        string
	SSID       string
	BSSID      string
	Mode       string
	Channel    int
	Frequency  int
	Quality    int
	QualityMax int
	// Signal is in dBm and therefore negative. Zero means it was absent or
	// unusable rather than "no signal": a real reading of 0 dBm does not
	// happen on a client radio.
	Signal     int
	Noise      int
	Encrypted  bool
	HTMode     string
	HardwareID string
}

// Associated reports that this interface is actually joined to a network.
//
// An SSID alone is not enough: a client that is configured but not associated
// still reports the SSID it is trying to reach on some drivers. A BSSID is the
// AP it is talking to, and the all-zero one is the placeholder for "nobody".
func (r RadioInfo) Associated() bool {
	return r.Client() && r.SSID != "" && r.BSSID != "" && r.BSSID != "00:00:00:00:00:00"
}

// Client distinguishes an uplink station from the router's own access point.
func (r RadioInfo) Client() bool {
	mode := strings.ToLower(strings.TrimSpace(r.Mode))
	return mode == "client" || mode == "sta" || mode == "station" || mode == "managed"
}

type radioInfoJSON struct {
	PHY        string  `json:"phy"`
	SSID       string  `json:"ssid"`
	BSSID      string  `json:"bssid"`
	Mode       string  `json:"mode"`
	Channel    int     `json:"channel"`
	Frequency  int     `json:"frequency"`
	Quality    int     `json:"quality"`
	QualityMax int     `json:"quality_max"`
	Signal     float64 `json:"signal"`
	Noise      float64 `json:"noise"`
	HTMode     string  `json:"htmode"`
	Encryption struct {
		Enabled bool `json:"enabled"`
	} `json:"encryption"`
	Hardware struct {
		Name string `json:"name"`
	} `json:"hardware"`
}

// ParseRadioInfo reads `ubus call iwinfo info {"device": ...}`.
func ParseRadioInfo(device string, data []byte) (RadioInfo, error) {
	if len(data) == 0 {
		return RadioInfo{}, domain.Errorf(domain.CodeInternal,
			"设备 %s 的无线状态查询没有返回内容", device)
	}
	var raw radioInfoJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return RadioInfo{}, domain.Errorf(domain.CodeInternal,
			"无法解析设备 %s 的无线状态", device).Wrap(err)
	}
	return RadioInfo{
		Device:     device,
		PHY:        raw.PHY,
		SSID:       raw.SSID,
		BSSID:      strings.ToLower(strings.TrimSpace(raw.BSSID)),
		Mode:       raw.Mode,
		Channel:    raw.Channel,
		Frequency:  raw.Frequency,
		Quality:    raw.Quality,
		QualityMax: raw.QualityMax,
		Signal:     normalizeSignal(raw.Signal),
		Noise:      normalizeSignal(raw.Noise),
		Encrypted:  raw.Encryption.Enabled,
		HTMode:     raw.HTMode,
		HardwareID: raw.Hardware.Name,
	}, nil
}

// normalizeSignal turns iwinfo's reading into dBm.
//
// Some ubus JSON encoders print iwinfo's signed char as an unsigned 32-bit
// number, so -52 dBm arrives as 4294967244. Left alone that becomes the
// strongest signal ever measured, and an AP-selection policy that picks the
// strongest candidate would choose whichever radio the encoder mangled. The
// baseline hit this and fixed it the same way.
//
// Anything outside the range a radio can report becomes zero, which callers
// read as "no usable measurement" rather than as a very good or very bad one.
func normalizeSignal(value float64) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value >= math.Pow(2, 31) && value <= math.Pow(2, 32)-1 {
		value -= math.Pow(2, 32)
	}
	if value >= 0 || value < -127 {
		return 0
	}
	return int(value)
}

// RadioInfo asks iwinfo about one device.
func (a *Adapter) RadioInfo(ctx context.Context, device string) (RadioInfo, error) {
	name, ok := NormalizeDeviceName(device)
	if !ok {
		return RadioInfo{}, domain.Errorf(domain.CodeInvalidArgument,
			"%q 不是有效的无线设备名", device)
	}
	// The device name goes through JSON encoding into one argv element, and it
	// has already been checked against the kernel's character set, so it cannot
	// break out of the object or become a second argument.
	request, err := json.Marshal(map[string]string{"device": name})
	if err != nil {
		return RadioInfo{}, domain.Errorf(domain.CodeInternal,
			"无法构造 iwinfo 请求").Wrap(err)
	}
	result, err := a.runner.Run(ctx, "ubus", "call", "iwinfo", "info", string(request))
	if err != nil {
		return RadioInfo{}, err
	}
	if result.StdoutTruncated {
		return RadioInfo{}, domain.Errorf(domain.CodeInternal,
			"设备 %s 的无线状态过长，已截断", name)
	}
	return ParseRadioInfo(name, result.Stdout)
}
