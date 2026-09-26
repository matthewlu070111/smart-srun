//go:build unix

package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

// The write half of T28-T30, on a real radio.
//
// Everything else runs against a copy. This one changes /etc/config/wireless,
// reloads the network, waits for an association that will not happen, and then
// has to put it all back -- which is the failure the whole package exists for,
// exercised end to end on hardware instead of described.
//
// It is guarded twice because it is genuinely disruptive: the environment
// variable has to say so in words, and the radio has to be named. On a router
// whose uplink is the radio -- which is the case it was first run on -- this
// takes the router off the network for as long as the settle wait, and the
// caller is expected to have armed a watchdog that restores the configuration
// unconditionally if this process never finishes.
//
//	SMARTSRUN_DEVICE_WRITE=yes-change-this-router-wireless \
//	SMARTSRUN_DEVICE_RADIO=radio1 ./daemon.test -test.run RollsBackOnRealHardware
func TestAChangeThatCannotAssociateRollsBackOnRealHardware(t *testing.T) {
	if os.Getenv("SMARTSRUN_DEVICE_WRITE") != "yes-change-this-router-wireless" {
		t.Skip("refusing to change a real wireless configuration without " +
			"SMARTSRUN_DEVICE_WRITE=yes-change-this-router-wireless")
	}
	radio := os.Getenv("SMARTSRUN_DEVICE_RADIO")
	if radio == "" {
		t.Skip("set SMARTSRUN_DEVICE_RADIO to the radio to use")
	}
	runner := openwrt.Runner{}
	if _, err := runner.Resolve("uci"); err != nil {
		t.Skip("no uci on this host -- this test is for the OpenWrt device")
	}

	const live = "/etc/config/wireless"
	original, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("read %s: %v", live, err)
	}
	// This test's own restore, independent of the caller's watchdog. Belt and
	// braces on purpose: the watchdog covers this process dying, and this
	// covers it merely failing.
	t.Cleanup(func() {
		if current, err := os.ReadFile(live); err == nil && bytes.Equal(current, original) {
			return
		}
		t.Logf("restoring %s from this test's own copy", live)
		if err := os.WriteFile(live, original, 0o644); err != nil {
			t.Errorf("RESTORE FAILED, the watchdog has to catch this: %v", err)
			return
		}
		if _, err := runner.Run(t.Context(), "/etc/init.d/network", "reload"); err != nil {
			t.Errorf("restore reload failed: %v", err)
		}
	})

	store, err := wireless.NewUCIStore(runner, wireless.StoreOptions{
		Staging: filepath.Join(t.TempDir(), "staging")})
	if err != nil {
		t.Fatalf("NewUCIStore: %v", err)
	}
	radioWireless := newDeviceWireless(wirelessOptions{
		Adapter:  openwrt.NewAdapter(runner),
		Store:    store,
		Paths:    wireless.Paths{Dir: t.TempDir()},
		Settings: deviceSettings{},
		Clock:    policy.SystemClock{},
		// Short on purpose. The sixty-second ceiling is spec 04's and is
		// already locked with a fake clock; what this is for is the machinery
		// around it, and every second here is a second this router is off the
		// air.
		SettleWait: 25 * time.Second,
		SettlePoll: 2 * time.Second,
	})

	// An SSID nothing will answer to, so the association cannot succeed and the
	// undo is the thing under test.
	plan := application.WirelessPlan{
		Radio:      radio,
		SSID:       "smartsrun-gate-no-such-network",
		Encryption: "none",
	}

	start := time.Now()
	err = radioWireless.Apply(t.Context(), plan)
	t.Logf("Apply returned after %s: %v", time.Since(start).Round(time.Second), err)
	if err == nil {
		t.Fatal("associating with a network that does not exist was reported " +
			"as success")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeDeadlineExceeded {
		t.Errorf("code = %s, want DeadlineExceeded", code)
	}

	// The undo happened, and put back what was there.
	restored, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("read %s: %v", live, err)
	}
	if strings.Contains(string(restored), "smartsrun-gate-no-such-network") {
		t.Error("the change that failed was left in the live configuration")
	}

	// Section by section rather than byte by byte: committing makes uci rewrite
	// the file in canonical form, so the bytes legitimately differ.
	for _, name := range deviceSectionNames(t, string(original)) {
		if !strings.Contains(string(restored), name) {
			t.Errorf("section %s did not survive the rollback", name)
		}
	}
	// And the option that was actually moved is back at its old value.
	config, err := openwrt.NewAdapter(runner).UCI(t.Context(), "wireless")
	if err != nil {
		t.Fatalf("UCI: %v", err)
	}
	section, ok := config.Section(stationSection(radio))
	if !ok {
		t.Fatalf("%s is gone after the rollback", stationSection(radio))
	}
	t.Logf("after rollback: %s.ssid=%q disabled=%q",
		stationSection(radio), section.Get("ssid"), section.Get("disabled"))
	if section.Get("ssid") == plan.SSID {
		t.Error("the station is still pointed at the network that does not exist")
	}
}

// And the other half: a change that does associate is confirmed and kept.
//
// The test above proves the undo. This proves the thing the undo exists to
// protect -- Begin, Stage, Commit, reload, a real association, a real address,
// and only then Confirm. Pointed at the network the radio is already meant to
// be on, so the end state is the one the router wants.
//
// It still puts the configuration back afterwards. A successful transaction is
// meant to be kept, so nothing in the production path undoes it; leaving this
// program's marker and its deletions on somebody's working router is not a
// side effect a test gets to have.
//
//	SMARTSRUN_DEVICE_WRITE=yes-change-this-router-wireless \
//	SMARTSRUN_DEVICE_RADIO=radio1 SMARTSRUN_DEVICE_SSID=jxnu_stu ...
func TestAChangeThatAssociatesIsConfirmedOnRealHardware(t *testing.T) {
	if os.Getenv("SMARTSRUN_DEVICE_WRITE") != "yes-change-this-router-wireless" {
		t.Skip("refusing to change a real wireless configuration without " +
			"SMARTSRUN_DEVICE_WRITE=yes-change-this-router-wireless")
	}
	radio, ssid := os.Getenv("SMARTSRUN_DEVICE_RADIO"), os.Getenv("SMARTSRUN_DEVICE_SSID")
	if radio == "" || ssid == "" {
		t.Skip("set SMARTSRUN_DEVICE_RADIO and SMARTSRUN_DEVICE_SSID to run this")
	}
	runner := openwrt.Runner{}
	if _, err := runner.Resolve("uci"); err != nil {
		t.Skip("no uci on this host -- this test is for the OpenWrt device")
	}

	const live = "/etc/config/wireless"
	original, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("read %s: %v", live, err)
	}
	t.Cleanup(func() {
		if current, err := os.ReadFile(live); err == nil && bytes.Equal(current, original) {
			return
		}
		t.Logf("putting %s back as it was found", live)
		if err := os.WriteFile(live, original, 0o644); err != nil {
			t.Errorf("RESTORE FAILED, the watchdog has to catch this: %v", err)
			return
		}
		if _, err := runner.Run(t.Context(), "/etc/init.d/network", "reload"); err != nil {
			t.Errorf("restore reload failed: %v", err)
		}
	})

	store, err := wireless.NewUCIStore(runner, wireless.StoreOptions{
		Staging: filepath.Join(t.TempDir(), "staging")})
	if err != nil {
		t.Fatalf("NewUCIStore: %v", err)
	}
	radioWireless := newDeviceWireless(wirelessOptions{
		Adapter:    openwrt.NewAdapter(runner),
		Store:      store,
		Paths:      wireless.Paths{Dir: t.TempDir()},
		Settings:   deviceSettings{},
		Clock:      policy.SystemClock{},
		SettleWait: 45 * time.Second,
		SettlePoll: 2 * time.Second,
	})

	start := time.Now()
	err = radioWireless.Apply(t.Context(), application.WirelessPlan{
		Radio: radio, SSID: ssid, Encryption: "none"})
	t.Logf("Apply returned after %s: %v", time.Since(start).Round(time.Second), err)
	if err != nil {
		t.Fatalf("a change to a network that is in range was not confirmed: %v", err)
	}

	// Confirmed means the record is gone: the journal and the passphrase copy
	// do not outlive the change they existed for.
	observed, err := radioWireless.Association(t.Context(), radio)
	if err != nil {
		t.Fatalf("Association: %v", err)
	}
	t.Logf("association after apply: ssid=%q bssid=%q ipv4=%v",
		observed.SSID, observed.BSSID, observed.HasIPv4)
	if observed.SSID != ssid || !observed.HasIPv4 {
		t.Errorf("Apply reported success but the line is %+v", observed)
	}

	config, err := openwrt.NewAdapter(runner).UCI(t.Context(), "wireless")
	if err != nil {
		t.Fatalf("UCI: %v", err)
	}
	section, ok := config.Section(stationSection(radio))
	if !ok {
		t.Fatalf("%s is not in the configuration after a successful apply",
			stationSection(radio))
	}
	if section.Get("ssid") != ssid {
		t.Errorf("ssid = %q, want %q", section.Get("ssid"), ssid)
	}
	if section.Get("disabled") != "0" {
		t.Errorf("disabled = %q, want the station enabled", section.Get("disabled"))
	}
	if section.Get(openwrt.ManagedMarker) != "1" {
		t.Errorf("%s = %q, want this program's marker",
			openwrt.ManagedMarker, section.Get(openwrt.ManagedMarker))
	}
	// Every section that was there is still there. Spec 04's promise about the
	// household's own access points, checked on a router that has some.
	for _, name := range deviceSectionNames(t, string(original)) {
		if _, ok := config.Section(name); !ok {
			t.Errorf("section %s disappeared", name)
		}
	}
}

// deviceSettings is a configuration with no accounts.
//
// knownSSIDs comes from it, and an empty one means ManagedStations adopts only
// the sections carrying this project's own marker -- not every client whose
// SSID happens to appear somewhere. On a router where the uplink is a managed
// station, which sections count as ours decides which ones get disabled.
type deviceSettings struct{}

func (deviceSettings) Snapshot() domain.Config { return domain.Config{} }
func (deviceSettings) Revision() uint64        { return 1 }

func deviceSectionNames(t *testing.T, body string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "config" {
			names = append(names, strings.Trim(fields[2], "'\""))
		}
	}
	if len(names) == 0 {
		t.Fatal("no named sections in the live configuration")
	}
	return names
}
