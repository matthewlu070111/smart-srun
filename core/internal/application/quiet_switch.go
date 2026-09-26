package application

import (
	"context"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
)

func (a *Authenticator) quietSwitch(ctx context.Context, action Action, report func(Phase)) Outcome {
	cfg := a.settings.Snapshot()
	inside := policy.EvaluateQuiet(cfg.Quiet, a.clock.Now()).Active
	entering := action.Request.Kind == KindQuietHotspot
	if !cfg.Enabled || !cfg.Failover.Enabled || inside != entering {
		return quietSwitchDeferred("静默切换条件已变化，保留当前连接")
	}
	account, known := cfg.CampusAccountByID(action.Request.AccountID)
	hotspot, exists := cfg.HotspotByID(action.Request.HotspotID)
	if !known || !exists || cfg.Selection.ActiveCampusID != account.ID {
		return quietSwitchDeferred("当前账号或热点已变化，保留当前连接")
	}
	if a.wireless == nil {
		return failure(domain.Errorf(domain.CodeUnsupportedCapability, "无法管理无线出口，未执行静默切换"))
	}
	if entering {
		// Reuse the selected hotspot without scanning or reconfiguring it. An
		// enabled timetable still owns the next campus return at quiet end.
		if out, stop := a.deferOnHotspot(ctx, cfg, account); stop {
			if out.MaintenanceDeferred {
				observed, err := a.wireless.Association(ctx, hotspot.Radio)
				if err != nil {
					return failure(err)
				}
				dest, err := hotspotDestination(hotspot)
				if err != nil {
					return failure(err)
				}
				if dest.want.Satisfied(observed) {
					return Outcome{State: StateSucceeded, Message: "保留当前热点，到上线时间自动返回校园网"}
				}
				return quietSwitchDeferred("当前热点与所选配置不同，保留当前连接")
			}
			return out
		}
		return a.switchHotspot(ctx, action, report)
	}
	observed, err := a.wireless.Association(ctx, hotspot.Radio)
	if err != nil {
		return failure(err)
	}
	dest, err := hotspotDestination(hotspot)
	if err != nil {
		return failure(err)
	}
	if !dest.want.Satisfied(observed) {
		return quietSwitchDeferred("连接已由其他操作改变，取消自动回切")
	}
	return a.switchCampus(ctx, action, report)
}

func quietSwitchDeferred(message string) Outcome {
	return Outcome{State: StateSucceeded, Message: message, MaintenanceDeferred: true}
}
