//go:build unix

package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

const wizardJobID = "0123456789abcdef0123456789abcdef"
const idleWizardUCI = "wireless.radio1=wifi-device\nwireless.radio1.band='5g'\nwireless.home=wifi-iface\nwireless.home.device='radio1'\nwireless.home.mode='ap'\nwireless.home.ssid='HomeNet'\nwireless.home.network='lan'\n"
const wizardFirewallExport = "package firewall\nconfig zone 'wan_zone'\n option name 'wan'\n option input 'REJECT'\n option masq '1'\n list network 'wan'\n list network 'wwan'\nconfig forwarding 'lan_wan'\n option src 'lan'\n option dest 'wan'\n"

func wizardFixture(t *testing.T) *wirelessFixture {
	t.Helper()
	f := newWirelessFixture(t)
	f.radio.clock = policy.SystemClock{}
	f.radio.settleWait = 30 * time.Millisecond
	f.radio.settlePoll = time.Millisecond
	f.radio.wizardStore = f.store
	f.device.answer(idleWizardUCI, "uci", "show", "wireless")
	f.device.answer("network.lan=interface\nnetwork.lan.proto='static'\nnetwork.wwan=interface\nnetwork.wwan.proto='dhcp'\n", "uci", "show", "network")
	f.device.answer(wizardFirewallExport, "uci", "-n", "export", "firewall")
	f.device.answer(statusWithoutStation, "ubus", "call", "network.wireless", "status")
	f.device.answer(scanResults, "ubus", iwinfoScan("phy1-ap0")...)
	f.device.answer(associatedInfo, "ubus", iwinfoInfo("phy1-sta0")...)
	f.device.answer(wwanUp, "ubus", "call", "network.interface.wwan", "status")
	return f
}

func awaitWifi(t *testing.T, r *running, state string) WifiSetupView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var view WifiSetupView
		if err := json.Unmarshal(r.call("setup_wifi.status", wifiJobParams{Job: wizardJobID, Session: "browser"}), &view); err != nil {
			t.Fatal(err)
		}
		if view.State == state {
			return view
		}
		if view.State == "failed" && state != "failed" {
			t.Fatalf("wifi failed: %s", view.Message)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wifi did not reach %s", state)
	return WifiSetupView{}
}

func startWizard(t *testing.T, failAssociation bool) (*running, *wirelessFixture, WifiSetupParams) {
	t.Helper()
	f := wizardFixture(t)
	if !failAssociation {
		f.store.onStage = func() { f.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status") }
	}
	r := start(t, func(o *Options) { o.Runner = switchRunner{}; o.wizardDevice = f.radio })
	p := WifiSetupParams{Job: wizardJobID, Session: "browser", SSID: "jxnu_stu", Encryption: "none"}
	r.call("setup_wifi.start", p)
	return r, f, p
}

func TestWifiWizardSavesExactlySettledConnectionAndCommitIsIdempotent(t *testing.T) {
	r, f, p := startWizard(t, false)
	view := awaitWifi(t, r, "ready")
	if view.Iface != "wwan" || view.Radio != "radio1" {
		t.Fatal("wrong settled uplink")
	}
	if !strings.Contains(string(r.call("setup_wifi.start", p)), "ready") {
		t.Fatal("retry replaced job")
	}
	r.callExpectingError("setup_wifi.status", wifiJobParams{Job: p.Job, Session: "foreign"})
	r.callExpectingError("setup_wifi.cancel", wifiJobParams{Job: p.Job, Session: "foreign"})
	r.callExpectingError("config.apply", json.RawMessage(`{"expected_revision":0,"settings":{"enabled":true}}`))
	zero := uint64(0)
	user, password, wrongSSID, wrongKey := "student", " account secret ", "wrong SSID", "wrong wifi secret"
	params := wifiAccountParams{Job: p.Job, Session: p.Session, ExpectedRevision: &zero, Account: &config.CampusPatch{UserID: &user, Password: &password, SSID: &wrongSSID, Key: &wrongKey}}
	var saved ConfigWriteResult
	if err := json.Unmarshal(r.call("setup_wifi.account", params), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.ID != "c1" || saved.Config.CampusAccounts[0].Password != "" {
		t.Fatal("bad save result")
	}
	r.call("setup_wifi.commit", params)
	awaitWifi(t, r, "done")
	repo, err := config.Open(r.paths.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	cfg := repo.Snapshot()
	if cfg.Revision != 1 || len(cfg.CampusAccounts) != 1 {
		t.Fatal("duplicate save created another account")
	}
	account := cfg.CampusAccounts[0]
	if account.SSID != "jxnu_stu" || account.Radio != "radio1" || account.Key != "" || account.Password != password || cfg.STAIface != "wwan" {
		t.Fatal("saved browser fields instead of settled connection, or changed credential whitespace")
	}
	if _, found, _ := (wireless.Paths{Dir: r.paths.Recovery() + "/setup-wifi/wireless"}).LoadJournal(); found {
		t.Fatal("retained secret backup after confirmation")
	}
	before := f.store.batches()
	r.call("setup_wifi.cancel", wifiJobParams{Job: p.Job, Session: p.Session})
	if f.store.batches() != before {
		t.Fatal("cancel undid confirmed connection")
	}
}

func TestWifiWizardCancelAndFailedAssociationRestoreOnlyOwnedOptions(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "association-failure"}[fail], func(t *testing.T) {
			r, f, p := startWizard(t, fail)
			if fail {
				awaitWifi(t, r, "failed")
			} else {
				awaitWifi(t, r, "ready")
				r.call("setup_wifi.cancel", wifiJobParams{Job: p.Job, Session: p.Session})
				awaitWifi(t, r, "cancelled")
			}
			f.store.mu.Lock()
			remaining := len(f.store.values)
			f.store.mu.Unlock()
			if remaining != 0 {
				t.Fatal("temporary station survived rollback")
			}
			if r.status().ConfigRevision != 0 {
				t.Fatal("temporary connection saved account configuration")
			}
		})
	}
}

func TestWifiWizardShutdownUndoesUnconfirmedConnection(t *testing.T) {
	r, f, _ := startWizard(t, false)
	awaitWifi(t, r, "ready")
	r.stop()
	if err := r.wait(); err != nil {
		t.Fatal(err)
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if len(f.store.values) != 0 {
		t.Fatal("service stop left temporary wireless changes")
	}
}

func TestWifiWizardRefusesChangedAssociationAtSave(t *testing.T) {
	r, f, p := startWizard(t, false)
	awaitWifi(t, r, "ready")
	f.device.answer(strings.ReplaceAll(associatedInfo, "jxnu_stu", "other-network"), "ubus", iwinfoInfo("phy1-sta0")...)
	zero := uint64(0)
	user := "student"
	err := r.callExpectingError("setup_wifi.account", wifiAccountParams{Job: p.Job, Session: p.Session, ExpectedRevision: &zero, Account: &config.CampusPatch{UserID: &user}})
	if codeOf(t, err) != domain.CodeConflict {
		t.Fatal("wrong refusal")
	}
	if r.status().ConfigRevision != 0 {
		t.Fatal("saved changed association")
	}
}

func TestWifiWizardPlanProtectsActiveStationAndAmbiguousSecurity(t *testing.T) {
	f := wizardFixture(t)
	p := WifiSetupParams{Job: wizardJobID, SSID: "jxnu_stu", Encryption: "auto"}
	f.device.answer(idleWizardUCI+"wireless.other=wifi-iface\nwireless.other.device='radio1'\nwireless.other.mode='sta'\nwireless.other.ssid='phone'\nwireless.other.network='wwan'\n", "uci", "show", "wireless")
	if _, _, err := f.radio.wizardPlan(t.Context(), p); err == nil {
		t.Fatal("would overwrite active phone uplink")
	}
	f.device.answer(idleWizardUCI, "uci", "show", "wireless")
	f.device.answer(strings.ReplaceAll(scanResults, "HomeNet-5", "jxnu_stu"), "ubus", iwinfoScan("phy1-ap0")...)
	if _, _, err := f.radio.wizardPlan(t.Context(), p); err == nil {
		t.Fatal("auto chose between open/protected same-name networks")
	}
	p.Encryption = "none"
	if _, _, err := f.radio.wizardPlan(t.Context(), p); err != nil {
		t.Fatal(err)
	}
}

func TestWifiWizardExpiresWithoutBrowserPolling(t *testing.T) {
	f := wizardFixture(t)
	f.radio.clock = f.clock
	f.store.onStage = func() { f.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status") }
	r := start(t, func(o *Options) { o.Runner = switchRunner{}; o.wizardDevice = f.radio; o.Clock = f.clock })
	r.call("setup_wifi.start", WifiSetupParams{Job: wizardJobID, Session: "browser", SSID: "jxnu_stu", Encryption: "none"})
	awaitWifi(t, r, "ready")
	f.clock.Advance(16 * time.Minute)
	awaitWifi(t, r, "cancelled")
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if len(f.store.values) != 0 {
		t.Fatal("expired job left UCI changes")
	}
}

func TestWifiWizardReusesVerifiedExistingStationWithoutWritingUCI(t *testing.T) {
	f := wizardFixture(t)
	f.device.answer(idleWizardUCI+"wireless.jxnu_sta_radio1=wifi-iface\nwireless.jxnu_sta_radio1.device='radio1'\nwireless.jxnu_sta_radio1.mode='sta'\nwireless.jxnu_sta_radio1.ssid='jxnu_stu'\nwireless.jxnu_sta_radio1.network='wwan'\nwireless.jxnu_sta_radio1.encryption='none'\n", "uci", "show", "wireless")
	f.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	r := start(t, func(o *Options) { o.Runner = switchRunner{}; o.wizardDevice = f.radio })
	r.call("setup_wifi.start", WifiSetupParams{Job: wizardJobID, Session: "browser", SSID: "jxnu_stu", Encryption: "none"})
	awaitWifi(t, r, "connected")
	r.call("setup_wifi.cancel", wifiJobParams{Job: wizardJobID, Session: "browser"})
	awaitWifi(t, r, "cancelled")
	if f.store.batches() != 0 {
		t.Fatal("reused connection was modified")
	}
}

func TestWifiWizardCancelWhileAssociatingDoesNotLeaveChanges(t *testing.T) {
	f := wizardFixture(t)
	f.radio.settleWait = 30 * time.Second
	r := start(t, func(o *Options) { o.Runner = switchRunner{}; o.wizardDevice = f.radio })
	r.call("setup_wifi.start", WifiSetupParams{Job: wizardJobID, Session: "browser", SSID: "jxnu_stu", Encryption: "none"})
	awaitWifi(t, r, "connecting")
	r.call("setup_wifi.cancel", wifiJobParams{Job: wizardJobID, Session: "browser"})
	awaitWifi(t, r, "cancelled")
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if len(f.store.values) != 0 {
		t.Fatal("cancelled connection left changes")
	}
}

func TestWifiWizardNewAccountBecomesCurrentDefault(t *testing.T) {
	f := wizardFixture(t)
	f.store.onStage = func() { f.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status") }
	r := start(t, func(o *Options) { o.Runner = switchRunner{}; o.wizardDevice = f.radio })
	r.writeConfig("campus.upsert", `{"expected_revision":0,"account":{"user_id":"old","wired_iface":"wan"}}`)
	r.call("setup_wifi.start", WifiSetupParams{Job: wizardJobID, Session: "browser", SSID: "jxnu_stu", Encryption: "none"})
	awaitWifi(t, r, "ready")
	one := uint64(1)
	user := "new"
	var saved ConfigWriteResult
	if err := json.Unmarshal(r.call("setup_wifi.account", wifiAccountParams{Job: wizardJobID, Session: "browser", ExpectedRevision: &one, Account: &config.CampusPatch{UserID: &user}}), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID != "c2" || saved.Config.Selection.ActiveCampusID != "c2" || saved.Config.Selection.DefaultCampusID != "c2" {
		t.Fatal("wizard did not select the newly saved account")
	}
}
