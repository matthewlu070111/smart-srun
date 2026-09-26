//go:build unix

package daemon

import (
	"os"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

// deviceWireless reading a real radio, on a router whose station is up.
//
// Association's two-step lookup -- UCI section to netifd's ifname to iwinfo --
// was designed from a device where every station section was disabled, so the
// only station this project had ever seen was one it constructed in a test.
// This is that path against a real associated client: a real radio name going
// in, a real device name coming back, and a real address behind it.
//
// Read-only. It calls Association and nothing else; Apply is not reachable from
// here. Set SMARTSRUN_DEVICE_RADIO to the radio to look at.
func TestAssociationOnARealAssociatedRadio(t *testing.T) {
	radio := os.Getenv("SMARTSRUN_DEVICE_RADIO")
	if radio == "" {
		t.Skip("set SMARTSRUN_DEVICE_RADIO=radio1 to run this on a device")
	}
	runner := openwrt.Runner{}
	if _, err := runner.Resolve("ubus"); err != nil {
		t.Skip("no ubus on this host -- this test is for the OpenWrt device")
	}

	radios, err := openwrt.NewAdapter(runner).WirelessStatus(t.Context())
	if err != nil {
		t.Fatalf("WirelessStatus: %v", err)
	}
	found, known := openwrt.FindRadio(radios, radio)
	if !known {
		t.Fatalf("this device has no %s", radio)
	}

	// What the lookup is for: the section this program manages, and the device
	// name netifd gave it. Neither is derivable from the radio name.
	section := stationSection(radio)
	iface, running := found.FindInterface(section)
	if !running {
		t.Skipf("%s is not up on %s; this test needs an associated station",
			section, radio)
	}
	if iface.IfName == "" {
		t.Fatal("netifd named no device for a section it reports as up")
	}
	if _, valid := openwrt.NormalizeDeviceName(iface.IfName); !valid {
		t.Errorf("ifname = %q, which is not a device name iwinfo will answer to",
			iface.IfName)
	}

	radioWireless := newDeviceWireless(wirelessOptions{
		Adapter: openwrt.NewAdapter(runner),
		Clock:   policy.SystemClock{},
	})
	observed, err := radioWireless.Association(t.Context(), radio)
	if err != nil {
		t.Fatalf("Association: %v", err)
	}

	// SSIDs and BSSIDs are broadcast; neither is a secret. The passphrase is,
	// and Association never reads one.
	t.Logf("association verified; ipv4=%v", observed.HasIPv4)

	if !observed.Joined() {
		t.Fatalf("the station is up but reported as not joined: %+v", observed)
	}
	if observed.BSSID != strings.ToLower(observed.BSSID) {
		t.Errorf("BSSID = %q, want it lower-cased to match what UCI stores",
			observed.BSSID)
	}
	if !observed.HasIPv4 {
		t.Errorf("the station carries this router's uplink but reported no " +
			"address; the logical interface lookup is wrong")
	}

	// And the decision that follows from it: a radio already on the wanted
	// network is left alone.
	//
	// This is the property that matters most on this particular router, whose
	// uplink is the radio under test -- reapplying a change it does not need
	// would cost a reassociation, a new lease and a fresh authentication, and
	// would do it to the link this test is arriving over. Spec 04: "已真实关联
	// 匹配SSID与IP可直接复用".
	target := wifi.Target{
		SSID:     observed.SSID,
		Security: wifi.ParseSecurity(""),
		Policy:   domain.APSelectionAuto,
	}
	if wifi.ShouldReselect(target, observed) {
		t.Errorf("the daemon would re-apply a radio already on %q with an "+
			"address", observed.SSID)
	}

	// The same radio, a different network: that one does need choosing.
	elsewhere := target
	elsewhere.SSID = observed.SSID + "-somewhere-else"
	if !wifi.ShouldReselect(elsewhere, observed) {
		t.Error("a radio on the wrong network was reported as already usable")
	}
}

// And a radio this device does not have is reported, not guessed at.
func TestAMissingRadioIsReportedOnTheDevice(t *testing.T) {
	if os.Getenv("SMARTSRUN_DEVICE_RADIO") == "" {
		t.Skip("set SMARTSRUN_DEVICE_RADIO to run this on a device")
	}
	runner := openwrt.Runner{}
	if _, err := runner.Resolve("ubus"); err != nil {
		t.Skip("no ubus on this host -- this test is for the OpenWrt device")
	}

	radioWireless := newDeviceWireless(wirelessOptions{
		Adapter: openwrt.NewAdapter(runner),
		Clock:   policy.SystemClock{},
	})
	if _, err := radioWireless.Association(t.Context(), "radio_not_here"); err == nil {
		t.Error("a radio that does not exist was accepted")
	}
}

// This only reads netifd and iwinfo; it never scans or changes a station.
func TestWirelessStatusOnARealClient(t *testing.T) {
	iface := os.Getenv("SMARTSRUN_DEVICE_STA_IFACE")
	if iface == "" {
		t.Skip("set SMARTSRUN_DEVICE_STA_IFACE for the read-only device test")
	}
	view := readWirelessView(t.Context(), openwrt.NewAdapter(openwrt.Runner{}), domain.Config{STAIface: iface})
	if view.State != "associated" || view.Device == "" || view.SSID == "" || view.BSSID == "" || view.Signal >= 0 || view.Channel <= 0 || view.Address == "" {
		t.Fatal("live client observation missing associated device, AP, signal, channel or IPv4")
	}
	t.Log("live client: association, interface, AP, signal, channel and IPv4 verified; no network mutation")
}
