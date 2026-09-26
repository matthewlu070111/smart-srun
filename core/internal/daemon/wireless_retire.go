package daemon

import (
	"context"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

// Retire removes this program's wireless exit only after wired authentication
// succeeded. Household APs and unrelated WANs are never included in the plan.
func (w *deviceWireless) Retire(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	radios, err := w.adapter.WirelessStatus(ctx)
	if err != nil {
		return err
	}
	if len(radios) == 0 {
		return nil // wired-only systems need not have /etc/config/wireless
	}
	uci, err := w.adapter.UCI(ctx, WirelessPackage)
	if err != nil {
		return err
	}
	cfg := w.settings.Snapshot()
	iface := cfg.STAIface
	if iface == "" {
		iface = uplinkInterface
	}
	var changes []wireless.Change
	sections := map[string]bool{}
	for _, station := range openwrt.ManagedStations(uci, "", knownSSIDs(w.settings)) {
		if station.Disabled {
			continue
		}
		// Refuse an ambiguous/shared L3 target instead of bringing down LAN or
		// a configured wired WAN through a station somebody attached to it.
		section, _ := uci.Section(station.Section)
		if !exclusiveNetwork(section, iface) || iface == "lan" || wiredInterface(cfg, iface) {
			return domain.Errorf(domain.CodeConflict, "受管无线客户端未独占无线出口，未修改网络")
		}
		sections[station.Section] = true
		changes = append(changes, wireless.Change{
			Key: wireless.Key{Section: station.Section, Option: "disabled"}, Text: "1",
		})
	}
	if len(changes) == 0 {
		return nil
	}
	for _, section := range uci.SectionsOfType(openwrt.SectionWifiIface) {
		if sections[section.Name] || section.Get("disabled") == "1" {
			continue
		}
		for _, name := range networkNames(section) {
			if name == iface {
				return domain.Errorf(domain.CodeConflict, "无线出口仍被其他接口使用，未修改网络")
			}
		}
	}
	return w.applyChanges(ctx, changes, func() error {
		application.ReportPhase(ctx, application.PhaseRetire)
		return w.awaitRetired(ctx, sections, iface)
	})
}

func exclusiveNetwork(section openwrt.UCISection, iface string) bool {
	fields := networkNames(section)
	return len(fields) == 1 && fields[0] == iface
}

func networkNames(section openwrt.UCISection) []string {
	value, present := section.Lookup("network")
	if !present {
		return nil
	}
	if value.IsList {
		return strings.Fields(strings.Join(value.List, " "))
	}
	return strings.Fields(value.Text)
}

func wiredInterface(cfg domain.Config, iface string) bool {
	for _, account := range cfg.CampusAccounts {
		if account.IsWired() && account.WiredIface == iface {
			return true
		}
	}
	return false
}

func (w *deviceWireless) awaitRetired(ctx context.Context, sections map[string]bool, iface string) error {
	deadline := w.clock.Now().Add(w.settleWait)
	for {
		radios, err := w.adapter.WirelessStatus(ctx)
		if err != nil {
			return err
		}
		present := false
		for _, radio := range radios {
			for _, client := range radio.Interfaces {
				present = present || (sections[client.Section] && client.IfName != "")
			}
		}
		status, err := w.adapter.InterfaceStatus(ctx, iface)
		if err != nil {
			return err // failure to observe is not proof the old route is gone
		}
		if !present && !status.Up && len(status.IPv4) == 0 {
			return nil
		}
		if !w.clock.Now().Before(deadline) {
			return domain.Errorf(domain.CodeDeadlineExceeded, "旧无线客户端或地址未及时退出")
		}
		timer := w.clock.NewTimerAt(w.clock.Now().Add(w.settlePoll))
		select {
		case <-timer.C():
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
		timer.Stop()
	}
}
