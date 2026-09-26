package application

import (
	"context"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	portalprobe "github.com/matthewlu070111/smart-srun/core/internal/portal"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

// A hotspot obtaining a lease does not prove it provides Internet access. Use
// its own STA binding; a working wired default route must not validate it.
func (a *Authenticator) checkHotspot(ctx context.Context, cfg domain.Config, hotspot domain.HotspotProfile) (domain.Connectivity, error) {
	if cfg.STAIface == "" {
		return domain.ConnectivityUnknown, domain.Errorf(domain.CodeBindingUnavailable, "没有指定热点客户端接口，无法确认热点连通性")
	}
	key := "hotspot:" + hotspot.ID
	binding, err := a.observeLine(ctx, key, cfg.STAIface)
	if err != nil {
		return domain.ConnectivityUnknown, err
	}
	if !binding.Ready() {
		return domain.ConnectivityUnknown, domain.Errorf(domain.CodeBindingUnavailable, "热点客户端尚未取得可用 IPv4 地址")
	}
	line, err := a.lines.Line(key, binding, "hotspot-connectivity")
	if err != nil {
		return domain.ConnectivityUnknown, err
	}
	level, probeErr := portalprobe.CheckConnectivity(ctx, line, a.probeURLs)
	now, err := a.binder.ResolveBinding(ctx, cfg.STAIface, binding.Generation)
	if err != nil {
		return domain.ConnectivityUnknown, err
	}
	if !sameLine(binding, now) {
		return domain.ConnectivityUnknown, domain.Errorf(domain.CodeBindingChanged, "检测期间热点客户端绑定发生变化")
	}
	observed, err := a.wireless.Association(ctx, hotspot.Radio)
	if err != nil {
		return domain.ConnectivityUnknown, err
	}
	target := wifi.Target{SSID: hotspot.SSID, Security: wifi.ParseSecurity(hotspot.Encryption), Policy: domain.APSelectionAuto}
	if !target.Satisfied(observed) {
		return domain.ConnectivityUnknown, domain.Errorf(domain.CodeBindingChanged, "检测期间已离开所选热点")
	}
	return level, probeErr
}
