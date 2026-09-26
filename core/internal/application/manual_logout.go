package application

import (
	"context"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// A manual logout owns the global switch slot: its final step may reload the
// managed wireless uplink. A failed/unknown logout never reaches that step.
func (a *Authenticator) manualLogout(ctx context.Context, action Action, report func(Phase)) Outcome {
	cfg := a.settings.Snapshot()
	out := a.logout(ctx, action, report)
	if out.State != StateSucceeded || !cfg.Failover.Enabled || cfg.Selection.ActiveCampusID != action.Request.AccountID {
		return out
	}
	id := cfg.Selection.ActiveHotspotID
	if id == "" {
		id = cfg.Selection.DefaultHotspotID
	}
	if _, ok := cfg.HotspotByID(id); !ok {
		out.Message += "；未配置可用热点，保留当前线路"
		return out
	}
	action.Request.HotspotID = id
	switched := a.switchHotspot(ctx, action, report)
	switched.Message = "校园会话已确认下线；" + switched.Message
	switched.Observation = out.Observation
	if observed := switched.Observation; observed != nil {
		account, _ := cfg.CampusAccountByID(action.Request.AccountID)
		// Wireless now describes a different network; a wired line may also
		// have changed during netifd reload. Do not publish old binding evidence.
		iface, err := lineInterface(cfg, account)
		var current domain.Binding
		if err == nil {
			current, err = a.observeLine(ctx, account.ID, iface)
		}
		if !account.IsWired() || err != nil || current.Generation != observed.Generation {
			observed.Auth, observed.Connectivity = domain.AuthUnknown, domain.ConnectivityUnknown
			observed.Identity = ""
			if err != nil {
				observed.Link = domain.LinkMissing
				observed.Line.Device, observed.Line.Address = "", ""
			} else {
				observed.Generation = current.Generation
				observed.Line.Device, observed.Line.Address = current.L3Device, current.SourceIPv4.String()
			}
		}
	}
	return switched
}
