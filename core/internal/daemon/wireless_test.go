package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

// fakeDevice answers the tools the adapter runs, and remembers what was asked.
//
// It stands in for a router, not for a process: what running a process does is
// the openwrt package's to demonstrate, against real ones. What this is for is
// the composition -- which question is asked of which object, and in which
// order -- which is the whole of what this file assembles.
type fakeDevice struct {
	mu        sync.Mutex
	responses map[string]string
	errors    map[string]error
	calls     [][]string
}

func newDevice() *fakeDevice {
	return &fakeDevice{responses: map[string]string{}, errors: map[string]error{}}
}

func (d *fakeDevice) Run(_ context.Context, program string, args ...string) (
	openwrt.Result, error) {

	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, append([]string{program}, args...))
	key := program + " " + strings.Join(args, " ")
	if err, ok := d.errors[key]; ok {
		return openwrt.Result{Program: program}, err
	}
	if body, ok := d.responses[key]; ok {
		return openwrt.Result{Program: program, Stdout: []byte(body)}, nil
	}
	return openwrt.Result{Program: program},
		&openwrt.ExitError{Program: program, Code: 1}
}

// Resolve is the other half of what the adapter needs from a runner. Every
// program these tests use is present; a build with a missing tool is the
// openwrt package's case, not this one's.
func (d *fakeDevice) Resolve(program string) (string, error) {
	return "/usr/bin/" + program, nil
}

func (d *fakeDevice) answer(body string, program string, args ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.responses[program+" "+strings.Join(args, " ")] = body
}

func (d *fakeDevice) asked(program string, args ...string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	want := program + " " + strings.Join(args, " ")
	for _, call := range d.calls {
		if call[0]+" "+strings.Join(call[1:], " ") == want {
			return true
		}
	}
	return false
}

// Constructed, not captured, and worth saying so: every station section on the
// router this project measures against is disabled, so no real capture of
// `network.wireless status` contains a station interface. The shape is the
// device's -- the same fields in the same places, checked against
// kwrt-25.12-feb/wireless-status.json -- and what is under test here is this
// package's use of it.
const statusWithStation = `{
  "radio0": {"up": true, "pending": false, "disabled": false,
    "retry_setup_failed": false,
    "interfaces": [{"section": "default_radio0", "ifname": "phy0-ap0",
      "config": {"mode": "ap", "ssid": "HomeNet-4", "network": ["lan"]}}]},
  "radio1": {"up": true, "pending": false, "disabled": false,
    "retry_setup_failed": false,
    "interfaces": [
      {"section": "default_radio1", "ifname": "phy1-ap0",
       "config": {"mode": "ap", "ssid": "HomeNet-5", "network": ["lan"]}},
      {"section": "jxnu_sta_radio1", "ifname": "phy1-sta0",
       "config": {"mode": "sta", "ssid": "jxnu_stu", "network": ["wwan"]}}]}
}`

// The same router before this program has enabled anything: the client section
// exists in UCI but netifd lists no interface for it. This one is the real
// capture's shape.
const statusWithoutStation = `{
  "radio1": {"up": true, "pending": false, "disabled": false,
    "retry_setup_failed": false,
    "interfaces": [{"section": "default_radio1", "ifname": "phy1-ap0",
      "config": {"mode": "ap", "ssid": "HomeNet-5", "network": ["lan"]}}]}
}`

const associatedInfo = `{"phy": "phy1", "ssid": "jxnu_stu",
  "bssid": "02:00:5E:00:53:02", "mode": "Client", "channel": 52,
  "frequency": 5260, "signal": -52, "noise": -95,
  "encryption": {"enabled": false}}`

const wwanUp = `{"up": true, "pending": false, "available": true,
  "proto": "dhcp", "device": "phy1-sta0", "l3_device": "phy1-sta0",
  "ipv4-address": [{"address": "10.20.30.40", "mask": 24}]}`

const wwanDown = `{"up": false, "pending": true, "available": true,
  "proto": "dhcp", "ipv4-address": []}`

const liveWirelessUCI = `wireless.radio1=wifi-device
wireless.radio1.band='5g'
wireless.default_radio1=wifi-iface
wireless.default_radio1.device='radio1'
wireless.default_radio1.mode='ap'
wireless.default_radio1.ssid='HomeNet-5'
wireless.default_radio1.key='home-secret'
wireless.old_sta=wifi-iface
wireless.old_sta.device='radio1'
wireless.old_sta.mode='sta'
wireless.old_sta.ssid='jxnu_tea'
wireless.old_sta.jxnu_auto='1'
wireless.old_sta.disabled='0'
`

// recordingStore is a wireless.Store that records rather than writes.
type recordingStore struct {
	mu        sync.Mutex
	values    map[wireless.Key]wireless.Value
	staged    [][]wireless.Change
	commits   int
	reloads   int
	failStage error
	// onStage runs while the store's lock is not held, so a test can see how
	// many changes are in flight at once.
	onStage func()
}

func newRecordingStore() *recordingStore {
	return &recordingStore{values: map[wireless.Key]wireless.Value{}}
}

func (s *recordingStore) Read(ctx context.Context, _ string, keys []wireless.Key) (
	map[wireless.Key]wireless.Value, error) {

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[wireless.Key]wireless.Value, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

func (s *recordingStore) Stage(ctx context.Context, _ string, changes []wireless.Change) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.onStage != nil {
		s.onStage()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failStage != nil {
		return s.failStage
	}
	s.staged = append(s.staged, changes)
	for _, change := range changes {
		if change.Delete {
			delete(s.values, change.Key)
			continue
		}
		s.values[change.Key] = wireless.Value{Text: change.Text, Present: true}
	}
	return nil
}

func (s *recordingStore) Commit(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits++
	return nil
}

func (s *recordingStore) PendingChanges(context.Context, string) ([]string, error) {
	return nil, nil
}

func (s *recordingStore) SectionKeys(_ context.Context, _, section string) ([]wireless.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []wireless.Key
	for key, value := range s.values {
		if key.Section == section && !key.IsSection() && value.Present {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *recordingStore) Reload(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloads++
	return nil
}

func (s *recordingStore) batches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.staged)
}

func (s *recordingStore) lastStaged() []wireless.Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.staged) == 0 {
		return nil
	}
	return s.staged[len(s.staged)-1]
}

// wroteOption finds what one option was set to in a staged batch.
func wroteOption(changes []wireless.Change, section, option string) (wireless.Change, bool) {
	for _, change := range changes {
		if change.Key.Section == section && change.Key.Option == option {
			return change, true
		}
	}
	return wireless.Change{}, false
}

type fixedSettings struct {
	cfg      domain.Config
	revision uint64
}

func (s fixedSettings) Snapshot() domain.Config { return s.cfg }
func (s fixedSettings) Revision() uint64        { return s.revision }

type wirelessFixture struct {
	radio    *deviceWireless
	device   *fakeDevice
	store    *recordingStore
	clock    *faketime.Clock
	settings fixedSettings
}

func newWirelessFixture(t *testing.T) *wirelessFixture {
	t.Helper()
	device := newDevice()
	store := newRecordingStore()
	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	settings := fixedSettings{revision: 11, cfg: domain.Config{
		CampusAccounts: []domain.CampusAccount{{ID: "c1", SSID: "jxnu_stu"}},
	}}

	return &wirelessFixture{
		device:   device,
		store:    store,
		clock:    clock,
		settings: settings,
		radio: newDeviceWireless(wirelessOptions{
			Adapter:    openwrt.NewAdapter(device),
			Store:      store,
			Paths:      wireless.Paths{Dir: t.TempDir()},
			Settings:   settings,
			Clock:      clock,
			SettleWait: 60 * time.Second,
			SettlePoll: 2 * time.Second,
		}),
	}
}

func iwinfoInfo(device string) []string {
	request, _ := json.Marshal(map[string]string{"device": device})
	return []string{"call", "iwinfo", "info", string(request)}
}

func iwinfoScan(device string) []string {
	request, _ := json.Marshal(map[string]string{"device": device})
	return []string{"call", "iwinfo", "scan", string(request)}
}

// The device iwinfo answers to is looked up, not derived.
//
// A client on radio1 is phy1-sta0 here, phy1-sta1 on the next router, and
// absent entirely while the section is disabled. Nothing about the radio name
// says which, so netifd is asked -- and this is the test that would fail if
// somebody replaced the lookup with string formatting, which works right up
// until it does not.
func TestTheAssociationIsReadFromTheDeviceNetifdNames(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	fixture.device.answer(associatedInfo, "ubus", iwinfoInfo("phy1-sta0")...)
	fixture.device.answer(wwanUp, "ubus", "call", "network.interface.wwan", "status")

	observed, err := fixture.radio.Association(t.Context(), "radio1")
	if err != nil {
		t.Fatalf("Association: %v", err)
	}
	if observed.SSID != "jxnu_stu" {
		t.Errorf("SSID = %q", observed.SSID)
	}
	if observed.BSSID != "02:00:5e:00:53:02" {
		t.Errorf("BSSID = %q, want it lower-cased", observed.BSSID)
	}
	if !observed.HasIPv4 {
		t.Error("the interface has an address and was reported as having none")
	}
	if !fixture.device.asked("ubus", iwinfoInfo("phy1-sta0")...) {
		t.Error("iwinfo was not asked about the device netifd named")
	}
}

// A section netifd has not brought up is not associated, and not a failure.
//
// This is the ordinary state of a router this program has not yet connected:
// the client section is in UCI and disabled, so netifd lists no interface for
// it. Reporting that as an error would make the first connection on every
// router look like broken hardware.
func TestASectionWithNoInterfaceIsNotAssociatedAndNotAnError(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(statusWithoutStation, "ubus", "call", "network.wireless", "status")

	observed, err := fixture.radio.Association(t.Context(), "radio1")
	if err != nil {
		t.Fatalf("Association: %v", err)
	}
	if observed.Joined() {
		t.Errorf("observed = %+v, want nothing joined", observed)
	}
	if fixture.device.asked("ubus", iwinfoInfo("phy1-ap0")...) {
		t.Error("the household's access point was read as this program's client")
	}
}

// A client that joined and got no address is not a line that can be reused.
//
// Spec 04: an association with no address is a client that associated and got
// nothing, and authenticating over it sends a request that goes nowhere.
func TestAnAssociationWithNoAddressIsNotUsable(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	fixture.device.answer(associatedInfo, "ubus", iwinfoInfo("phy1-sta0")...)
	fixture.device.answer(wwanDown, "ubus", "call", "network.interface.wwan", "status")

	observed, err := fixture.radio.Association(t.Context(), "radio1")
	if err != nil {
		t.Fatalf("Association: %v", err)
	}
	if !observed.Joined() {
		t.Fatal("the client is associated")
	}
	if observed.HasIPv4 {
		t.Error("an interface that is down was reported as carrying an address")
	}

	target := wifi.Target{SSID: "jxnu_stu", Policy: domain.APSelectionAuto}
	if target.Satisfied(observed) {
		t.Error("a line with no address was accepted as already usable")
	}
}

// An address this program cannot confirm is treated as absent.
//
// The caller is deciding whether to reuse an existing line. "I could not tell"
// has to mean "do not reuse it": the other way round authenticates over a line
// nobody established was working.
func TestAnUnreadableInterfaceMeansNoAddress(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	fixture.device.answer(associatedInfo, "ubus", iwinfoInfo("phy1-sta0")...)
	// network.interface.wwan is left unanswered, so the adapter fails.

	observed, err := fixture.radio.Association(t.Context(), "radio1")
	if err != nil {
		t.Fatalf("Association: %v", err)
	}
	if observed.HasIPv4 {
		t.Error("an unreadable interface was reported as carrying an address")
	}
}

// A radio the configuration names but the system does not have is a
// configuration error, not an empty answer.
func TestAMissingRadioIsReported(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(statusWithoutStation, "ubus", "call", "network.wireless", "status")

	_, err := fixture.radio.Association(t.Context(), "radio7")
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidConfig {
		t.Fatalf("Association = %v (code %s), want InvalidConfig", err, code)
	}
	if _, err := fixture.radio.Scan(t.Context(), "radio7"); err == nil {
		t.Error("Scan accepted a radio that is not there")
	}
}

const scanResults = `{"results": [
  {"ssid": "jxnu_stu", "bssid": "02:00:5E:00:53:02", "mode": "Master",
   "channel": 52, "mhz": 5260, "signal": -45, "quality": 60, "quality_max": 70,
   "encryption": {"enabled": false}},
  {"ssid": "HomeNet-5", "bssid": "02:00:5E:00:53:03", "mode": "Master",
   "channel": 149, "mhz": 5745, "signal": -60, "quality": 40, "quality_max": 70,
   "encryption": {"enabled": true, "wpa": [2], "authentication": ["psk"],
                  "ciphers": ["ccmp"]}},
  {"ssid": "a-mesh-peer", "bssid": "02:00:5E:00:53:04", "mode": "Mesh Point",
   "channel": 36, "mhz": 5180, "signal": -70, "quality": 30, "quality_max": 70,
   "encryption": {"enabled": false}}
]}`

// A scan comes back as candidates the wifi package can choose between, and the
// judgement about what each one offers is wifi's.
//
// Classifying here would be a second, quieter copy of a taxonomy that already
// exists and is tested -- and the one that matters, an open access point
// sharing a name with the protected network, is exactly what a second copy
// would eventually disagree about.
func TestAScanBecomesCandidatesClassifiedByWifi(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	fixture.device.answer(scanResults, "ubus", iwinfoScan("phy1-sta0")...)

	candidates, err := fixture.radio.Scan(t.Context(), "radio1")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// The mesh peer is gone: it has an SSID and a signal, so nothing further
	// down would have noticed it was unjoinable.
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want the two joinable ones: %+v",
			len(candidates), candidates)
	}

	campus := candidates[0]
	if campus.SSID != "jxnu_stu" || campus.Signal != -45 {
		t.Errorf("campus candidate = %+v", campus)
	}
	if campus.BSSID != "02:00:5e:00:53:02" {
		t.Errorf("BSSID = %q, want it lower-cased", campus.BSSID)
	}
	if campus.Radio != "radio1" {
		t.Errorf("Radio = %q; two radios can see the same access point and which "+
			"one saw it decides which client section is written", campus.Radio)
	}
	if want := wifi.ClassifyScan(false, nil, nil); campus.Security != want {
		t.Errorf("Security = %v, want %v -- wifi's classification, not a local one",
			campus.Security, want)
	}
	if want := wifi.ClassifyScan(true, []string{"psk"}, []int{2}); candidates[1].Security != want {
		t.Errorf("Security = %v, want %v", candidates[1].Security, want)
	}
}

// A radio with nothing up on it cannot scan, and says so.
//
// An empty candidate list would be read by the selection as "the access point
// you pinned is gone", which is a different fact and one nobody established.
func TestARadioWithNothingUpCannotScan(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(`{"radio1": {"up": false, "pending": false,
	  "disabled": true, "retry_setup_failed": false, "interfaces": []}}`,
		"ubus", "call", "network.wireless", "status")

	_, err := fixture.radio.Scan(t.Context(), "radio1")
	if code, _ := domain.CodeOf(err); code != domain.CodeUnsupportedCapability {
		t.Fatalf("Scan = %v (code %s), want UnsupportedCapability", err, code)
	}
}

// A scan can happen before the client exists, from the access point on the same
// radio.
//
// This is the first-connection case: the section this program manages is
// disabled, so it has no device, and choosing an access point has to be
// possible anyway.
func TestAScanFallsBackToTheAccessPointOnTheRadio(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.device.answer(statusWithoutStation, "ubus", "call", "network.wireless", "status")
	fixture.device.answer(scanResults, "ubus", iwinfoScan("phy1-ap0")...)

	candidates, err := fixture.radio.Scan(t.Context(), "radio1")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(candidates) != 2 {
		t.Errorf("got %d candidates", len(candidates))
	}
	if !fixture.device.asked("ubus", iwinfoScan("phy1-ap0")...) {
		t.Error("the scan did not fall back to the access point")
	}
}

// settled makes every read report the client on the wanted network with an
// address, so Apply's wait finishes on its first look.
func (f *wirelessFixture) settled() {
	f.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	f.device.answer(strings.Replace(associatedInfo, `"enabled": false`, `"enabled": true`, 1), "ubus", iwinfoInfo("phy1-sta0")...)
	f.device.answer(wwanUp, "ubus", "call", "network.interface.wwan", "status")
	f.device.answer(liveWirelessUCI, "uci", "show", "wireless")
}

func TestAssociationRejectsAddressFromAnotherDevice(t *testing.T) {
	f := newWirelessFixture(t)
	f.settled()
	f.device.answer(strings.Replace(wwanUp, `"l3_device": "phy1-sta0"`, `"l3_device": "eth0"`, 1), "ubus", "call", "network.interface.wwan", "status")
	got, err := f.radio.Association(t.Context(), "radio1")
	if err != nil {
		t.Fatal(err)
	}
	if got.HasIPv4 {
		t.Fatal("stale address on another L3 device accepted")
	}
}

func TestAwaitLineRefusesWrongPinnedAPOrOpenDowngrade(t *testing.T) {
	for _, mode := range []string{"pinned", "encryption"} {
		t.Run(mode, func(t *testing.T) {
			f := newWirelessFixture(t)
			f.settled()
			plan := campusPlan()
			if mode == "pinned" {
				plan.BSSID = "02:00:00:00:00:01"
			} else {
				f.device.answer(associatedInfo, "ubus", iwinfoInfo("phy1-sta0")...)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			// A matching SSID/address must not return success before noticing
			// cancellation when the actual pin/security is wrong.
			if err := f.radio.awaitLine(ctx, plan); err == nil {
				t.Fatal("wrong association marked ready")
			}
		})
	}
}

func campusPlan() application.WirelessPlan {
	return application.WirelessPlan{
		Radio: "radio1", SSID: "jxnu_stu", Encryption: "psk2", Key: "hunter2-hunter2"}
}

// A change writes the client section this program owns, and nothing else.
//
// The negative half is the point. Spec 04 forbids touching the home access
// point, and the way that promise is kept is that no change naming one can be
// built here -- so this checks what was staged, not only what ended up working.
func TestApplyWritesOnlyTheSectionThisProgramOwns(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()

	if err := fixture.radio.Apply(t.Context(), campusPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	staged := fixture.store.lastStaged()
	if len(staged) == 0 {
		t.Fatal("nothing was staged")
	}

	section := stationSection("radio1")
	for _, change := range staged {
		switch change.Key.Section {
		case section, "old_sta":
		default:
			t.Errorf("a change named %s, which this program does not own: %+v",
				change.Key.Section, change)
		}
	}
	// The one it does own carries what the plan asked for.
	for option, want := range map[string]string{
		"device": "radio1", "mode": "sta", "network": "wwan",
		"ssid": "jxnu_stu", "encryption": "psk2", "key": "hunter2-hunter2",
		"disabled": "0", openwrt.ManagedMarker: "1",
	} {
		change, found := wroteOption(staged, section, option)
		if !found {
			t.Errorf("%s was not written", option)
			continue
		}
		if change.Text != want || change.Delete {
			t.Errorf("%s = %+v, want %q", option, change, want)
		}
	}
}

// The section itself is created, because uci will not set an option in one that
// is not there.
func TestApplyCreatesTheSectionItWritesInto(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()

	if err := fixture.radio.Apply(t.Context(), campusPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	staged := fixture.store.lastStaged()
	change, found := wroteOption(staged, stationSection("radio1"), "")
	if !found {
		t.Fatalf("the section was never created: %+v", staged)
	}
	if change.Text != openwrt.SectionWifiIface {
		t.Errorf("section type = %q, want %q", change.Text, openwrt.SectionWifiIface)
	}
}

// An open network has no passphrase, and the option is removed rather than
// blanked.
//
// uci has no empty option: `set x.y.key=` writes nothing, leaves the option
// absent, and leaves the journal claiming a value that was never written. The
// 1.x runtime deletes these for the same reason.
func TestAnOpenNetworkRemovesThePassphraseRatherThanBlankingIt(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()

	plan := campusPlan()
	plan.Encryption, plan.Key = "none", ""
	if err := fixture.radio.Apply(t.Context(), plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	staged := fixture.store.lastStaged()
	key, found := wroteOption(staged, stationSection("radio1"), "key")
	if !found {
		t.Fatal("the passphrase option was left as it was")
	}
	if !key.Delete || key.Text != "" {
		t.Errorf("key = %+v, want a deletion", key)
	}
	// And the same rule for the pin, which an auto policy does not set.
	bssid, found := wroteOption(staged, stationSection("radio1"), "bssid")
	if !found || !bssid.Delete {
		t.Errorf("bssid = %+v, want a deletion when nothing is pinned", bssid)
	}
}

// A protected network with no passphrase is refused before anything is written.
func TestAProtectedNetworkWithNoPassphraseIsRefused(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()

	plan := campusPlan()
	plan.Key = ""
	err := fixture.radio.Apply(t.Context(), plan)
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidConfig {
		t.Fatalf("Apply = %v (code %s), want InvalidConfig", err, code)
	}
	if len(fixture.store.staged) != 0 {
		t.Errorf("%d batches were staged before the plan was checked",
			len(fixture.store.staged))
	}
}

// The other clients this program owns go down.
//
// Two enabled clients on one radio is the ambiguity openwrt.StationOnRadio
// refuses to resolve, and leaving the old one up is how a router carries on
// authenticating over the network it was told to leave.
func TestApplyDisablesTheOtherClientsThisProgramOwns(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()

	if err := fixture.radio.Apply(t.Context(), campusPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	staged := fixture.store.lastStaged()

	change, found := wroteOption(staged, "old_sta", "disabled")
	if !found {
		t.Fatalf("the previous client was left enabled: %+v", staged)
	}
	if change.Text != "1" {
		t.Errorf("old_sta.disabled = %q, want 1", change.Text)
	}
	// The household's access point is on the same radio and is not touched.
	if _, found := wroteOption(staged, "default_radio1", "disabled"); found {
		t.Error("the home access point was disabled")
	}
}

// A change that never produces a usable line is undone.
//
// Spec 04's sixty seconds, and the undo at the end of them: a router left on a
// network it cannot reach is worse than one still on the network it was on, and
// it cannot ask for help from there either.
func TestAChangeThatNeverConnectsIsRolledBack(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()
	// Associated, but netifd never gives it an address.
	fixture.device.answer(wwanDown, "ubus", "call", "network.interface.wwan", "status")

	done := make(chan error, 1)
	go func() { done <- fixture.radio.Apply(context.Background(), campusPlan()) }()

	// Spend the whole ceiling without spending it: the wait polls, and each
	// poll is a timer this clock can move past.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-done:
			if code, _ := domain.CodeOf(err); code != domain.CodeDeadlineExceeded {
				t.Fatalf("Apply = %v (code %s), want DeadlineExceeded", err, code)
			}
			if !strings.Contains(err.Error(), "IPv4") {
				t.Errorf("the failure does not say what was missing: %v", err)
			}
			// Staged twice: the change, and the undo.
			if len(fixture.store.staged) < 2 {
				t.Errorf("the change was left in place: %d batches staged",
					len(fixture.store.staged))
			}
			return
		case <-deadline:
			t.Fatal("Apply did not finish")
		default:
		}
		fixture.clock.Advance(3 * time.Second)
		time.Sleep(time.Millisecond)
	}
}

// One wireless change at a time, for the whole process.
//
// The coordinator's scheduling key serialises actions, which is not the same
// guarantee: recovery at startup is not an action, and neither is an RPC
// arriving while a maintenance tick runs. Spec 04 asks for a global lock as
// well, and the independent review made a point of saying one does not replace
// the other.
func TestOnlyOneWirelessChangeRunsAtATime(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()

	const attempts = 8
	var running, peak int
	var mu sync.Mutex
	fixture.store.onStage = func() {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
	}

	var group sync.WaitGroup
	for range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := fixture.radio.Apply(context.Background(), campusPlan()); err != nil {
				t.Errorf("Apply: %v", err)
			}
		}()
	}
	group.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Errorf("%d changes were in flight at once, want 1", peak)
	}
}

// Two changes in the same second do not share a journal identity.
//
// The task id is what a recovery names when it reports what it undid. Two
// transactions answering to the same name would make that report ambiguous
// exactly when somebody is reading it to find out what happened.
func TestEachChangeGetsItsOwnTaskIdentity(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()

	for range 3 {
		if err := fixture.radio.Apply(t.Context(), campusPlan()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	if fixture.radio.tasks != 3 {
		t.Errorf("tasks = %d after three changes", fixture.radio.tasks)
	}
}

// A cancelled switch still gets to undo itself.
//
// It cannot do that on the context that was just cancelled, and leaving it
// unwound is not "safe but untidy": Begin refuses to start while a journal is
// on disk, so the next wireless change would be blocked until a restart. The
// undo therefore runs on a bounded context of its own.
func TestACancelledChangeStillRollsItselfBack(t *testing.T) {
	fixture := newWirelessFixture(t)
	fixture.settled()
	// Associated, but no address, so the wait does not finish on its own.
	fixture.device.answer(wwanDown, "ubus", "call", "network.interface.wwan", "status")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.radio.Apply(ctx, campusPlan()) }()

	// Let the change be applied and the wait begin, then stop it.
	waitFor(t, func() bool { return fixture.store.batches() >= 1 })
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled switch was reported as done")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Apply did not finish after its context was cancelled")
	}

	if fixture.store.batches() < 2 {
		t.Fatalf("%d batches staged; the change was left in place",
			fixture.store.batches())
	}
	// And the proof that it matters: the next change can start.
	fixture.device.answer(wwanUp, "ubus", "call", "network.interface.wwan", "status")
	if err := fixture.radio.Apply(t.Context(), campusPlan()); err != nil {
		t.Errorf("a later change was blocked by the cancelled one: %v", err)
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the condition never held")
}

// A plan with no radio or no network is refused rather than written.
func TestAnIncompletePlanIsRefused(t *testing.T) {
	for name, build := range map[string]func(*application.WirelessPlan){
		"no radio": func(p *application.WirelessPlan) { p.Radio = "" },
		"no ssid":  func(p *application.WirelessPlan) { p.SSID = "" },
	} {
		fixture := newWirelessFixture(t)
		fixture.settled()
		plan := campusPlan()
		build(&plan)

		if err := fixture.radio.Apply(t.Context(), plan); err == nil {
			t.Errorf("%s: Apply accepted it", name)
		}
		if len(fixture.store.staged) != 0 {
			t.Errorf("%s: something was staged anyway", name)
		}
	}
}
