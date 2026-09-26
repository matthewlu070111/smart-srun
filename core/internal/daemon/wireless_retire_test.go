package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

func prepareRetire(t *testing.T) *wirelessFixture {
	f := newWirelessFixture(t)
	f.device.answer(statusWithStation, "ubus", "call", "network.wireless", "status")
	f.device.answer(liveWirelessUCI+"wireless.old_sta.network='wwan'\n", "uci", "show", "wireless")
	f.device.answer(wwanDown, "ubus", "call", "network.interface.wwan", "status")
	f.store.values[wireless.Key{Section: "old_sta", Option: "disabled"}] = wireless.Value{Present: true, Text: "0"}
	return f
}

func TestRetireOnlyDisablesOwnedStationAndWaitsForOldAddress(t *testing.T) {
	f := prepareRetire(t)
	f.store.onStage = func() { f.device.answer(statusWithoutStation, "ubus", "call", "network.wireless", "status") }
	if err := f.radio.Retire(t.Context()); err != nil {
		t.Fatal(err)
	}
	changes := f.store.lastStaged()
	if len(changes) != 1 || changes[0].Key != (wireless.Key{Section: "old_sta", Option: "disabled"}) || changes[0].Text != "1" {
		t.Fatalf("retired unrelated network: %+v", changes)
	}
	if f.store.reloads != 1 {
		t.Fatal("retirement did not apply exactly once")
	}
	// Repeating retirement after UCI reflects the result must not reload again.
	f.device.answer(liveWirelessUCI+"wireless.old_sta.network='wwan'\nwireless.old_sta.disabled='1'\n", "uci", "show", "wireless")
	if err := f.radio.Retire(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.store.reloads != 1 {
		t.Fatal("already retired link was reloaded")
	}
}

func TestRetireRejectsSharedLANAndRollsBackWhenOldAddressRemains(t *testing.T) {
	for _, extra := range []string{
		"wireless.old_sta.network='wwan' 'lan'\n",
		"wireless.old_sta.network='wwan lan'\n",
		"wireless.old_sta.network='wwan'\nwireless.other=wifi-iface\nwireless.other.mode='ap'\nwireless.other.network='wwan'\n",
	} {
		f := prepareRetire(t)
		f.device.answer(liveWirelessUCI+extra, "uci", "show", "wireless")
		if err := f.radio.Retire(t.Context()); err == nil || f.store.batches() != 0 {
			t.Fatal("shared exit was changed", err)
		}
	}
	f := prepareRetire(t)
	f.device.answer(liveWirelessUCI+"wireless.old_sta.network='lan'\n", "uci", "show", "wireless")
	if err := f.radio.Retire(t.Context()); err == nil {
		t.Fatal("LAN-backed station was retired")
	}
	if f.store.batches() != 0 {
		t.Fatal("conflict changed UCI")
	}

	f = prepareRetire(t)
	f.store.onStage = func() { f.device.answer(statusWithoutStation, "ubus", "call", "network.wireless", "status") }
	f.device.answer(wwanUp, "ubus", "call", "network.interface.wwan", "status")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.radio.Retire(ctx) }()
	waitFor(t, func() bool { return f.store.batches() == 1 })
	cancel()
	if err := <-done; err == nil {
		t.Fatal("old IPv4 was treated as retired")
	}
	change, ok := wroteOption(f.store.lastStaged(), "old_sta", "disabled")
	if !ok || change.Text != "0" {
		t.Fatal("cancelled retirement did not restore old station")
	}
}

func TestRetireRejectsWiredInterfaceAndAcceptsWiredOnlyDevice(t *testing.T) {
	f := prepareRetire(t)
	settings := fixedSettings{cfg: domain.Config{STAIface: "wan", CampusAccounts: []domain.CampusAccount{{ID: "c1", AccessMode: domain.AccessModeWired, WiredIface: "wan"}}}}
	f.radio.settings = settings
	f.device.answer(strings.ReplaceAll(liveWirelessUCI+"wireless.old_sta.network='wwan'\n", "'wwan'", "'wan'"), "uci", "show", "wireless")
	if err := f.radio.Retire(t.Context()); err == nil {
		t.Fatal("wired WAN was included in retirement")
	}
	f.device.answer(`{}`, "ubus", "call", "network.wireless", "status")
	if err := f.radio.Retire(t.Context()); err != nil {
		t.Fatal("wired-only device requires a wireless file", err)
	}
}
