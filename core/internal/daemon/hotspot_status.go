package daemon

import (
	"context"
	"slices"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// Hotspot health belongs to the observed STA, not to a campus account or to
// the last successful switch. This also works after a daemon restart. Only the
// wireless observer runs probes; status readers consume its bounded cache.
type hotspotMonitor struct {
	read     func(context.Context, domain.Config) WirelessView
	resolve  func(context.Context, string, uint64) (domain.Binding, error)
	probe    func(context.Context, domain.Binding) (domain.Connectivity, error)
	view     WirelessView
	revision uint64
	checked  time.Time
}

func observedHotspot(cfg domain.Config, view WirelessView) string {
	if view.State != "associated" || view.SSID == "" || view.Radio == "" {
		return ""
	}
	// An identical campus/hotspot name is not evidence of which network this
	// is. Do not assign another profile's result to it.
	for _, account := range cfg.CampusAccounts {
		if !account.IsWired() && account.Radio == view.Radio && account.SSID == view.SSID {
			return ""
		}
	}
	id := ""
	for _, hotspot := range cfg.HotspotProfiles {
		if hotspot.Radio == view.Radio && hotspot.SSID == view.SSID {
			if id != "" {
				return ""
			}
			id = hotspot.ID
		}
	}
	return id
}

func sameWirelessConnection(a, b WirelessView) bool {
	return a.State == b.State && a.Radio == b.Radio && a.Section == b.Section &&
		a.Device == b.Device && a.Interface == b.Interface && a.Address == b.Address &&
		a.SSID == b.SSID && a.BSSID == b.BSSID && a.HotspotID == b.HotspotID
}

func (m *hotspotMonitor) current(cfg domain.Config, view WirelessView, now time.Time) WirelessView {
	view.HotspotID = observedHotspot(cfg, view)
	view.Connectivity = domain.ConnectivityUnknown
	interval := time.Duration(max(15, cfg.Checks.IntervalSeconds)) * time.Second
	if m.revision != cfg.Revision || !sameWirelessConnection(m.view, view) ||
		now.Sub(m.checked) >= interval || now.Before(m.checked) {
		m.checked = time.Time{}
	}
	if !m.checked.IsZero() {
		view.Connectivity = m.view.Connectivity
	}
	return view
}

func bindingMatchesView(binding domain.Binding, view WirelessView) bool {
	return binding.Ready() && binding.LogicalIface == view.Interface &&
		binding.L3Device == view.Device && binding.SourceIPv4.String() == view.Address
}

func (m *hotspotMonitor) refresh(ctx context.Context, cfg domain.Config, view WirelessView, now time.Time) WirelessView {
	view = m.current(cfg, view, now)
	if view.HotspotID == "" || view.Address == "" || view.Interface == "" || !m.checked.IsZero() {
		return view
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	binding, err := m.resolve(readCtx, view.Interface, 1)
	cancel()
	if err != nil || !bindingMatchesView(binding, view) {
		return view
	}
	probeCtx, stop := context.WithTimeout(ctx, 8*time.Second)
	level, _ := m.probe(probeCtx, binding)
	stop()
	// A working wired route cannot validate this hotspot; neither can a reply
	// obtained before its address, device, DNS or associated AP changed.
	readCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	after, bindErr := m.resolve(readCtx, view.Interface, 1)
	latest := m.read(readCtx, cfg)
	cancel()
	latest.HotspotID = observedHotspot(cfg, latest)
	latest.Connectivity = domain.ConnectivityUnknown
	if ctx.Err() != nil || bindErr != nil || !bindingMatchesView(after, view) ||
		binding.IfIndex != after.IfIndex || !slices.Equal(binding.DNSServers, after.DNSServers) ||
		!sameWirelessConnection(view, latest) {
		m.checked = time.Time{}
		return latest
	}
	// No expected response is a failed Internet check, not "not checked" and
	// not an authentication failure. Keep the association visible separately.
	if level != domain.ConnectivityInternetReachable && level != domain.ConnectivityLimited {
		level = domain.ConnectivityOffline
	}
	latest.Connectivity = level
	m.view, m.revision, m.checked = latest, cfg.Revision, now
	return latest
}

func probeHotspotBinding(ctx context.Context, binding domain.Binding) (domain.Connectivity, error) {
	client, err := transport.NewClient(binding)
	if err != nil {
		return domain.ConnectivityUnknown, err
	}
	defer client.Close()
	return portal.CheckConnectivity(ctx, client, portal.ConnectivityURLs())
}
