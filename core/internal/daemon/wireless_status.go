package daemon

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
)

// WirelessView contains observed client data only, never the raw ubus config
// (which includes household Wi-Fi keys). Status polling reads this cache and
// cannot trigger scans, authentication, or shell commands.
type WirelessView struct {
	State        string              `json:"state"`
	Radio        string              `json:"radio,omitempty"`
	Section      string              `json:"section,omitempty"`
	Device       string              `json:"device,omitempty"`
	Interface    string              `json:"iface,omitempty"`
	Address      string              `json:"address,omitempty"`
	SSID         string              `json:"ssid,omitempty"`
	BSSID        string              `json:"bssid,omitempty"`
	Signal       int                 `json:"signal,omitempty"`
	Channel      int                 `json:"channel,omitempty"`
	HotspotID    string              `json:"hotspot_id,omitempty"`
	Connectivity domain.Connectivity `json:"connectivity,omitempty"`
}

type wirelessObservation struct {
	mu       sync.Mutex
	view     WirelessView
	revision uint64
	at       time.Time
}

func (o *wirelessObservation) read(revision uint64) *WirelessView {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.revision != revision || o.at.IsZero() || time.Since(o.at) > 30*time.Second {
		return nil
	}
	view := o.view
	return &view
}

func (o *wirelessObservation) store(view WirelessView, revision uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.view, o.revision, o.at = view, revision, time.Now()
}

func (d *Daemon) observeWireless(ctx context.Context, adapter *openwrt.Adapter) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	monitor := hotspotMonitor{read: func(ctx context.Context, cfg domain.Config) WirelessView {
		return readWirelessView(ctx, adapter, cfg)
	}, resolve: adapter.ResolveBinding, probe: probeHotspotBinding}
	for {
		cfg := d.config.Snapshot()
		read, cancel := context.WithTimeout(ctx, 3*time.Second)
		view := readWirelessView(read, adapter, cfg)
		cancel()
		view = monitor.current(cfg, view, time.Now())
		// Publish link loss or a changed connection before waiting for HTTP.
		d.wirelessState.store(view, cfg.Revision)
		d.markDirty()
		view = monitor.refresh(ctx, cfg, view, time.Now())
		d.wirelessState.store(view, cfg.Revision)
		d.markDirty()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func readWirelessView(ctx context.Context, adapter *openwrt.Adapter, cfg domain.Config) WirelessView {
	radios, err := adapter.WirelessStatus(ctx)
	if err != nil {
		return WirelessView{State: "unavailable"}
	}
	var selected *openwrt.WirelessInterface
	view := WirelessView{State: "disconnected"}
	for _, radio := range radios {
		if !radio.Up || radio.Pending || radio.Disabled || radio.RetrySetupFailed {
			continue
		}
		for _, iface := range radio.Interfaces {
			if !iface.Station() || iface.IfName == "" {
				continue
			}
			matches := cfg.STAIface != "" && slices.Contains(iface.Network, cfg.STAIface)
			if cfg.STAIface == "" {
				for _, a := range cfg.CampusAccounts {
					matches = matches || (!a.IsWired() && a.Radio == radio.Name && a.SSID == iface.SSID)
				}
				for _, h := range cfg.HotspotProfiles {
					matches = matches || (h.Radio == radio.Name && h.SSID == iface.SSID)
				}
			}
			if !matches {
				continue
			}
			if selected != nil {
				return WirelessView{State: "ambiguous"}
			}
			copy := iface
			selected = &copy
			view.Radio, view.Section, view.Device = radio.Name, iface.Section, iface.IfName
			view.Interface = cfg.STAIface
			if view.Interface == "" {
				view.Interface = firstNetwork(iface.Network)
			}
		}
	}
	if selected == nil {
		return view
	}
	info, err := adapter.RadioInfo(ctx, selected.IfName)
	if err != nil {
		view.State = "unavailable"
		return view
	}
	if !info.Associated() {
		return view
	}
	view.State, view.SSID, view.BSSID = "associated", info.SSID, info.BSSID
	view.Signal, view.Channel = info.Signal, info.Channel
	if view.Interface != "" {
		line, err := adapter.InterfaceStatus(ctx, view.Interface)
		if err == nil && line.Up && line.L3Device == view.Device && len(line.IPv4) > 0 {
			view.Address = line.IPv4[0].Address.String()
		}
	}
	return view
}
