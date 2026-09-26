package daemon

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func hotspotStatusFixture() (domain.Config, WirelessView, domain.Binding) {
	cfg := domain.Config{Revision: 2, STAIface: "wwan", Checks: domain.ChecksConfig{IntervalSeconds: 60},
		HotspotProfiles: []domain.HotspotProfile{{ID: "phone", Radio: "radio1", SSID: "phone"}}}
	view := WirelessView{State: "associated", Radio: "radio1", Section: "client", Device: "phy1-sta0",
		Interface: "wwan", Address: "192.0.2.2", SSID: "phone", BSSID: "02:00:00:00:00:01"}
	binding := domain.Binding{LogicalIface: "wwan", L3Device: "phy1-sta0", IfIndex: 7,
		SourceIPv4: netip.MustParseAddr("192.0.2.2"), DNSServers: []netip.Addr{netip.MustParseAddr("192.0.2.1")}}
	return cfg, view, binding
}

func TestHotspotHealthIsIndependentAndPeriodicallyRefreshed(t *testing.T) {
	cfg, view, binding := hotspotStatusFixture()
	calls := 0
	level := domain.ConnectivityInternetReachable
	m := hotspotMonitor{
		read: func(context.Context, domain.Config) WirelessView { return view },
		resolve: func(_ context.Context, iface string, _ uint64) (domain.Binding, error) {
			if iface != "wwan" {
				t.Fatalf("borrowed another line: %s", iface)
			}
			return binding, nil
		},
		probe: func(_ context.Context, got domain.Binding) (domain.Connectivity, error) {
			calls++
			if !bindingMatchesView(got, view) {
				t.Fatal("unbound hotspot probe")
			}
			return level, nil
		},
	}
	now := time.Now()
	// No campus account, action history or automatic-auth switch is needed.
	got := m.refresh(t.Context(), cfg, view, now)
	if got.HotspotID != "phone" || got.Connectivity != domain.ConnectivityInternetReachable || calls != 1 {
		t.Fatalf("restart did not observe the existing hotspot: %+v, calls=%d", got, calls)
	}
	view.Signal = -60
	for range 20 {
		got = m.refresh(t.Context(), cfg, view, now.Add(15*time.Second))
	}
	if calls != 1 || got.Signal != -60 || got.Connectivity != domain.ConnectivityInternetReachable {
		t.Fatal("passive reads or signal changes triggered probes or lost health")
	}
	for _, result := range []domain.Connectivity{domain.ConnectivityLimited, domain.ConnectivityUnknown, domain.ConnectivityInternetReachable} {
		now = now.Add(time.Minute)
		level = result
		got = m.refresh(t.Context(), cfg, view, now)
		want := result
		if want == domain.ConnectivityUnknown {
			want = domain.ConnectivityOffline
		}
		if got.Connectivity != want {
			t.Fatalf("result %s became %+v", result, got)
		}
	}
	if calls != 4 {
		t.Fatalf("unexpected probe cadence: %d", calls)
	}
	cfg.Revision++
	if m.current(cfg, view, now).Connectivity != domain.ConnectivityUnknown {
		t.Fatal("old revision reused a connectivity result")
	}
}

func TestHotspotHealthRejectsChangesDuringProbe(t *testing.T) {
	for _, scenario := range []string{"address", "device", "index", "dns", "ap", "ssid", "down", "read_error", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, view, binding := hotspotStatusFixture()
			current, after := view, binding
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var bindErr error
			m := hotspotMonitor{
				read:    func(context.Context, domain.Config) WirelessView { return current },
				resolve: func(context.Context, string, uint64) (domain.Binding, error) { return after, bindErr },
				probe: func(context.Context, domain.Binding) (domain.Connectivity, error) {
					switch scenario {
					case "address":
						after.SourceIPv4 = netip.MustParseAddr("192.0.2.3")
					case "device":
						after.L3Device = "other-sta"
					case "index":
						after.IfIndex++
					case "dns":
						after.DNSServers = []netip.Addr{netip.MustParseAddr("192.0.2.4")}
					case "ap":
						current.BSSID = "02:00:00:00:00:02"
					case "ssid":
						current.SSID = "campus"
					case "down":
						current = WirelessView{State: "disconnected"}
					case "read_error":
						bindErr = errors.New("interface missing")
					case "cancelled":
						cancel()
					}
					return domain.ConnectivityInternetReachable, nil
				},
			}
			got := m.refresh(ctx, cfg, view, time.Now())
			if got.Connectivity != domain.ConnectivityUnknown || !m.checked.IsZero() {
				t.Fatalf("stale success survived %s: %+v", scenario, got)
			}
		})
	}
}

func TestHotspotDoesNotProbeAnUnconfirmedOrDifferentLine(t *testing.T) {
	for _, scenario := range []string{"campus", "ambiguous", "no_address", "no_interface", "wrong_binding", "binding_error", "disconnected"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, view, binding := hotspotStatusFixture()
			var err error
			switch scenario {
			case "campus":
				cfg.CampusAccounts = []domain.CampusAccount{{Radio: view.Radio, SSID: view.SSID}}
			case "ambiguous":
				cfg.HotspotProfiles = append(cfg.HotspotProfiles, cfg.HotspotProfiles[0])
			case "no_address":
				view.Address = ""
			case "no_interface":
				view.Interface = ""
			case "wrong_binding":
				binding.L3Device = "wan"
			case "binding_error":
				err = errors.New("unavailable")
			case "disconnected":
				view.State = "disconnected"
			}
			m := hotspotMonitor{
				resolve: func(context.Context, string, uint64) (domain.Binding, error) { return binding, err },
				probe: func(context.Context, domain.Binding) (domain.Connectivity, error) {
					t.Fatal("unconfirmed hotspot sent network traffic")
					return "", nil
				},
			}
			if got := m.refresh(t.Context(), cfg, view, time.Now()); got.Connectivity != domain.ConnectivityUnknown {
				t.Fatalf("unconfirmed hotspot presented as reachable: %+v", got)
			}
		})
	}
}

func TestHotspotLossCannotReviveCachedSuccess(t *testing.T) {
	cfg, view, _ := hotspotStatusFixture()
	view.HotspotID, view.Connectivity = "phone", domain.ConnectivityInternetReachable
	now := time.Now()
	m := hotspotMonitor{view: view, revision: cfg.Revision, checked: now}
	if m.current(cfg, WirelessView{State: "disconnected"}, now).Connectivity != domain.ConnectivityUnknown {
		t.Fatal("disconnect retained success")
	}
	if m.current(cfg, view, now).Connectivity != domain.ConnectivityUnknown {
		t.Fatal("same-address reconnect revived pre-disconnect success")
	}
	m.checked = now
	if m.current(cfg, view, now.Add(-time.Second)).Connectivity != domain.ConnectivityUnknown {
		t.Fatal("backwards clock prolonged a result")
	}
}
