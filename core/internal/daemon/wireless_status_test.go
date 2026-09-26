package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
)

func TestWirelessStatusObservesTheClientWithoutScanning(t *testing.T) {
	dev := newDevice()
	dev.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	dev.answer(associatedInfo, "ubus", "call", "iwinfo", "info", `{"device":"phy1-sta0"}`)
	dev.answer(wwanUp, "ubus", "call", "network.interface.wwan", "status")
	view := readWirelessView(t.Context(), openwrt.NewAdapter(dev), domain.Config{STAIface: "wwan"})
	if view.State != "associated" || view.Device != "phy1-sta0" || view.BSSID != "02:00:5e:00:53:02" || view.Signal != -52 || view.Channel != 52 || view.Address != "10.20.30.40" {
		t.Fatalf("incorrect observation: %+v", view)
	}
	for _, call := range dev.calls {
		if call[0] != "ubus" || strings.Contains(strings.Join(call, " "), "scan") {
			t.Fatalf("status must only read: %v", call)
		}
	}
	// Same SSID and BSSID on a home AP must not appear as an uplink.
	dev.answer(strings.ReplaceAll(associatedInfo, `"Client"`, `"Master"`), "ubus", "call", "iwinfo", "info", `{"device":"phy1-sta0"}`)
	view = readWirelessView(t.Context(), openwrt.NewAdapter(dev), domain.Config{STAIface: "wwan"})
	if view.State == "associated" || view.BSSID != "" || view.Address != "" {
		t.Fatalf("AP accepted as client: %+v", view)
	}
}

func TestWirelessStatusRefusesAmbiguousAndStaleObservations(t *testing.T) {
	dev := newDevice()
	duplicate := strings.Replace(statusWithStation, `"mode": "ap", "ssid": "HomeNet-5", "network": ["lan"]`, `"mode": "sta", "ssid": "jxnu_stu", "network": ["wwan"]`, 1)
	dev.answer(duplicate, "ubus", "call", "network.wireless", "status")
	view := readWirelessView(t.Context(), openwrt.NewAdapter(dev), domain.Config{STAIface: "wwan"})
	if view.State != "ambiguous" || len(dev.calls) != 1 {
		t.Fatalf("ambiguous client guessed: %+v", view)
	}
	var cache wirelessObservation
	cache.store(WirelessView{State: "associated", SSID: "old"}, 2)
	copy := cache.read(2)
	copy.SSID = "mutated"
	if cache.read(2).SSID != "old" || cache.read(3) != nil {
		t.Fatal("snapshot is shared or from the wrong configuration")
	}
	cache.at = time.Now().Add(-31 * time.Second)
	if cache.read(2) != nil {
		t.Fatal("expired observation still current")
	}
}

func TestAssociationDoesNotReuseAnAccessPointOrPendingRadio(t *testing.T) {
	for _, state := range []string{
		strings.Replace(statusWithStation, `"mode": "sta"`, `"mode": "ap"`, 1),
		strings.ReplaceAll(statusWithStation, `"pending": false`, `"pending": true`),
	} {
		dev := newDevice()
		dev.answer(state, "ubus", "call", "network.wireless", "status")
		w := newDeviceWireless(wirelessOptions{Adapter: openwrt.NewAdapter(dev)})
		association, err := w.Association(t.Context(), "radio1")
		if err != nil || association.Joined() || len(dev.calls) != 1 {
			t.Fatalf("unsafe client reuse: %+v, %v", association, err)
		}
	}
}
