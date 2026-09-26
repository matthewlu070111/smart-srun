package openwrt

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ScanResult is one access point a scan reported.
//
// This is the observed neighbourhood, as opposed to the configured state in
// UCI: it says what is on the air right now, which is the only thing that can
// answer "is the access point this account is pinned to still there".
type ScanResult struct {
	SSID string
	// BSSID is lower-cased here. iwinfo prints it upper-case and UCI stores it
	// lower-case, and a comparison between the two is the kind of mismatch that
	// looks like a missing access point.
	BSSID string
	// Mode is iwinfo's word for what the entry is. Only "Master" is something a
	// client can join -- see Joinable.
	Mode       string
	Band       int
	Channel    int
	Frequency  int
	Signal     int
	Quality    int
	QualityMax int
	Encryption ScanEncryption
}

// ScanEncryption is what an access point advertised, kept as the lists iwinfo
// reported rather than flattened into one name.
//
// Flattening is where a mixed-mode access point stops being matchable: an entry
// offering both `psk` and `sae` admits two different kinds of client, and any
// single name for it is wrong for one of them.
type ScanEncryption struct {
	Enabled        bool
	WPA            []int
	Authentication []string
	Ciphers        []string
}

// Joinable reports whether a client could associate with this entry.
//
// Only infrastructure access points can be. iwinfo lists ad-hoc and mesh peers
// in the same array, and they would otherwise look like candidates -- with
// signal readings and an SSID, so nothing further down would notice.
func (r ScanResult) Joinable() bool {
	return strings.EqualFold(strings.TrimSpace(r.Mode), "Master")
}

type scanResultJSON struct {
	SSID       string  `json:"ssid"`
	BSSID      string  `json:"bssid"`
	Mode       string  `json:"mode"`
	Band       int     `json:"band"`
	Channel    int     `json:"channel"`
	MHz        int     `json:"mhz"`
	Signal     float64 `json:"signal"`
	Quality    int     `json:"quality"`
	QualityMax int     `json:"quality_max"`
	Encryption struct {
		Enabled        bool     `json:"enabled"`
		WPA            []int    `json:"wpa"`
		Authentication []string `json:"authentication"`
		Ciphers        []string `json:"ciphers"`
	} `json:"encryption"`
}

// ParseScanResults reads `ubus call iwinfo scan {"device": ...}`.
//
// An empty list is a real answer -- nothing was in range -- but output this
// cannot read is not, and must not arrive as one. The M04 lesson applies
// directly: a parser that answers "nothing found" for input it failed to
// understand hands the caller a fact it did not establish, and here that fact
// is "the access point you pinned is gone".
func ParseScanResults(device string, data []byte) ([]ScanResult, error) {
	if len(data) == 0 {
		return nil, domain.Errorf(domain.CodeInternal,
			"设备 %s 的无线扫描没有返回内容", device)
	}

	// A pointer, so that "results is absent" is distinguishable from "results
	// is empty". Any JSON object at all unmarshals into a struct of optional
	// fields, so without this a reply that is not a scan reply -- an error
	// object, another method's output -- becomes an empty candidate list.
	var raw struct {
		Results *[]scanResultJSON `json:"results"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法解析设备 %s 的无线扫描结果", device).Wrap(err)
	}
	if raw.Results == nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"设备 %s 的无线扫描输出里没有 results 字段", device)
	}

	results := make([]ScanResult, 0, len(*raw.Results))
	for _, entry := range *raw.Results {
		results = append(results, ScanResult{
			SSID:  entry.SSID,
			BSSID: strings.ToLower(strings.TrimSpace(entry.BSSID)),
			Mode:  entry.Mode,
			Band:  entry.Band,
			// iwinfo reports the frequency under `mhz` in a scan and
			// `frequency` in an info call. Same number, two spellings.
			Channel:    entry.Channel,
			Frequency:  entry.MHz,
			Signal:     normalizeSignal(entry.Signal),
			Quality:    entry.Quality,
			QualityMax: entry.QualityMax,
			Encryption: ScanEncryption{
				Enabled:        entry.Encryption.Enabled,
				WPA:            entry.Encryption.WPA,
				Authentication: entry.Encryption.Authentication,
				Ciphers:        entry.Encryption.Ciphers,
			},
		})
	}
	return results, nil
}

// Scan asks iwinfo what is on the air around one device.
//
// A scan is a read, but not a free one: the radio leaves its operating channel
// while it runs, so a client associated on this device sees a gap. That is why
// spec 04 has selection happen when a connection is being made rather than on
// every maintenance tick.
func (a *Adapter) Scan(ctx context.Context, device string) ([]ScanResult, error) {
	name, ok := NormalizeDeviceName(device)
	if !ok {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"%q 不是有效的无线设备名", device)
	}
	request, err := json.Marshal(map[string]string{"device": name})
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法构造 iwinfo 扫描请求").Wrap(err)
	}
	result, err := a.runner.Run(ctx, "ubus", "call", "iwinfo", "scan", string(request))
	if err != nil {
		return nil, err
	}
	if result.StdoutTruncated {
		// A truncated scan is a shorter candidate list, and a shorter list is
		// indistinguishable from a quieter neighbourhood. Refusing is the only
		// answer that does not silently drop the access point being looked for.
		return nil, domain.Errorf(domain.CodeInternal,
			"设备 %s 的扫描结果过长，已截断", name)
	}
	return ParseScanResults(name, result.Stdout)
}
