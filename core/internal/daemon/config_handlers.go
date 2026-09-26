package daemon

import (
	"context"
	"encoding/json"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

// ConfigWriteResult never includes credentials, including after an account edit.
type ConfigWriteResult struct {
	Revision uint64        `json:"config_revision"`
	ID       string        `json:"id,omitempty"`
	Config   domain.Config `json:"config"`
	// Warnings names input the save accepted but did not keep. Spec 03 drops
	// school_extra keys the selected strategy does not declare "with a
	// diagnostic"; this is that diagnostic, so a caller that sent a key learns
	// it was discarded instead of finding it missing later.
	Warnings []string `json:"warnings,omitempty"`
}

type ConfigApplyParams struct {
	ExpectedRevision *uint64         `json:"expected_revision"`
	Settings         json.RawMessage `json:"settings"`
}

func (d *Daemon) configApply(ctx context.Context, raw json.RawMessage) (any, error) {
	var params ConfigApplyParams
	if err := config.DecodePatch(raw, &params); err != nil {
		return nil, err
	}
	// Decode once before dispatch, then again onto the current settings inside
	// the transaction. Omitted fields, including nested ones, stay unchanged.
	var shape config.Settings
	if err := config.DecodePatch(params.Settings, &shape); err != nil {
		return nil, err
	}
	var dropped []string
	result, err := d.changeConfig(ctx, params.ExpectedRevision, "", "", func(cfg *domain.Config) error {
		settings := config.SettingsOf(*cfg)
		var supplied map[string]json.RawMessage
		if err := json.Unmarshal(params.Settings, &supplied); err != nil {
			return domain.Errorf(domain.CodeInvalidArgument, "settings 格式无效")
		}
		_, extraSupplied := supplied["school_extra"]
		if extraSupplied {
			settings.SchoolExtra = nil // a supplied object replaces the old map
		}
		if err := config.DecodePatch(params.Settings, &settings); err != nil {
			return err
		}
		// Only keys the caller actually sent are reported. A save that switched
		// strategy clears the map by design and is not the caller's mistake.
		dropped = nil
		if extraSupplied && settings.School == cfg.School {
			_, dropped = config.FilterSchoolExtra(settings.School, settings.SchoolExtra)
		}
		return config.ApplySettings(settings)(cfg)
	})
	if err != nil {
		return nil, err
	}
	written := result.(ConfigWriteResult)
	for _, key := range dropped {
		written.Warnings = append(written.Warnings,
			"school_extra."+key+" 不是当前认证策略声明的字段，已丢弃")
	}
	return written, nil
}

type CampusUpsertParams struct {
	ExpectedRevision *uint64             `json:"expected_revision"`
	Account          *config.CampusPatch `json:"account"`
}

type HotspotUpsertParams struct {
	ExpectedRevision *uint64              `json:"expected_revision"`
	Profile          *config.HotspotPatch `json:"profile"`
}

func (d *Daemon) campusUpsert(ctx context.Context, raw json.RawMessage) (any, error) {
	var params CampusUpsertParams
	if err := config.DecodePatch(raw, &params, "account.login.double_stack"); err != nil {
		return nil, err
	}
	if params.Account == nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "需要 account")
	}
	return d.changeConfig(ctx, params.ExpectedRevision, "campus", params.Account.ID, config.UpsertCampus(*params.Account))
}

func (d *Daemon) hotspotUpsert(ctx context.Context, raw json.RawMessage) (any, error) {
	var params HotspotUpsertParams
	if err := config.DecodePatch(raw, &params); err != nil {
		return nil, err
	}
	if params.Profile == nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "需要 profile")
	}
	return d.changeConfig(ctx, params.ExpectedRevision, "hotspot", params.Profile.ID, config.UpsertHotspot(*params.Profile))
}

type ConfigIDParams struct {
	ExpectedRevision *uint64 `json:"expected_revision"`
	ID               string  `json:"id"`
}

func (d *Daemon) campusRemove(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.changeID(ctx, raw, config.RemoveCampus)
}

func (d *Daemon) campusSetDefault(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.changeID(ctx, raw, config.SetDefaultCampus)
}

func (d *Daemon) hotspotRemove(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.changeID(ctx, raw, config.RemoveHotspot)
}

func (d *Daemon) hotspotSetDefault(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.changeID(ctx, raw, config.SetDefaultHotspot)
}

func (d *Daemon) changeID(ctx context.Context, raw json.RawMessage, change func(string) config.Change) (any, error) {
	var params ConfigIDParams
	if err := config.DecodePatch(raw, &params); err != nil {
		return nil, err
	}
	if params.ID == "" {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "需要 id")
	}
	return d.changeConfig(ctx, params.ExpectedRevision, "", params.ID, change(params.ID))
}

func (d *Daemon) changeConfig(ctx context.Context, expected *uint64, kind, id string, change config.Change) (any, error) {
	if expected == nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "需要 expected_revision，请读取当前配置后重试")
	}
	var result ConfigWriteResult
	err := d.actions.ChangeConfiguration(ctx, func() error {
		if err := update.Guard(d.paths.Update()); err != nil {
			return err
		}
		if d.wizard.configBlocked() {
			return domain.Errorf(domain.CodeBusy, "无线向导尚未完成，请先保存向导账号或取消临时连接")
		}
		before := d.config.Snapshot()
		updated, err := d.config.Update(*expected, change)
		// A rename followed by a failed directory fsync still changes the
		// visible config. Invalidate the old state even when durability failed.
		revision := d.config.Revision()
		if revision != before.Revision {
			d.configChanged(revision)
		}
		if err != nil {
			return err
		}
		result.Config = redact(updated)
		result.Revision = updated.Revision
		result.ID = id
		if id == "" && kind == "campus" {
			result.ID = updated.CampusAccounts[len(updated.CampusAccounts)-1].ID
		}
		if id == "" && kind == "hotspot" {
			result.ID = updated.HotspotProfiles[len(updated.HotspotProfiles)-1].ID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type DetailParams struct {
	ID             string `json:"id,omitempty"`
	IncludeSecrets bool   `json:"include_secrets,omitempty"`
}

type CampusResult struct {
	Revision uint64                 `json:"config_revision"`
	Accounts []domain.CampusAccount `json:"accounts"`
}

type HotspotResult struct {
	Revision uint64                  `json:"config_revision"`
	Profiles []domain.HotspotProfile `json:"profiles"`
}

func detailParams(raw json.RawMessage) (DetailParams, error) {
	var params DetailParams
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := config.DecodePatch(raw, &params); err != nil {
		return params, err
	}
	if params.IncludeSecrets && params.ID == "" {
		return params, domain.Errorf(domain.CodeInvalidArgument, "显示凭据需要指定单个详情 id")
	}
	return params, nil
}

func (d *Daemon) campusGet(_ context.Context, raw json.RawMessage) (any, error) {
	params, err := detailParams(raw)
	if err != nil {
		return nil, err
	}
	cfg := d.config.Snapshot()
	if !params.IncludeSecrets {
		cfg = redact(cfg)
	}
	accounts := cfg.CampusAccounts
	if params.ID != "" {
		account, found := cfg.CampusAccountByID(params.ID)
		if !found {
			return nil, domain.Errorf(domain.CodeNotFound, "找不到校园账号")
		}
		accounts = []domain.CampusAccount{account}
	}
	return CampusResult{Revision: cfg.Revision, Accounts: accounts}, nil
}

func (d *Daemon) hotspotGet(_ context.Context, raw json.RawMessage) (any, error) {
	params, err := detailParams(raw)
	if err != nil {
		return nil, err
	}
	cfg := d.config.Snapshot()
	if !params.IncludeSecrets {
		cfg = redact(cfg)
	}
	profiles := cfg.HotspotProfiles
	if params.ID != "" {
		profile, found := cfg.HotspotByID(params.ID)
		if !found {
			return nil, domain.Errorf(domain.CodeNotFound, "找不到热点")
		}
		profiles = []domain.HotspotProfile{profile}
	}
	return HotspotResult{Revision: cfg.Revision, Profiles: profiles}, nil
}

type CapabilitiesResult struct {
	Tools                map[openwrt.Tool]bool       `json:"tools"`
	UbusObjects          map[openwrt.UbusObject]bool `json:"ubus_objects"`
	PackageManager       openwrt.PackageManager      `json:"package_manager"`
	PackageArchitectures []string                    `json:"package_architectures"`
}

func (d *Daemon) capabilitiesGet(context.Context, json.RawMessage) (any, error) {
	result := CapabilitiesResult{
		Tools: map[openwrt.Tool]bool{}, UbusObjects: map[openwrt.UbusObject]bool{},
		PackageManager:       d.capabilities.PackageManager,
		PackageArchitectures: append([]string{}, d.capabilities.PackageArchitectures...),
	}
	for _, tool := range []openwrt.Tool{openwrt.ToolUCI, openwrt.ToolUbus, openwrt.ToolIwinfo, openwrt.ToolWifi, openwrt.ToolOpkg, openwrt.ToolAPK} {
		result.Tools[tool] = d.capabilities.Has(tool)
	}
	for _, object := range []openwrt.UbusObject{openwrt.ObjectIwinfo, openwrt.ObjectNetworkWireless} {
		result.UbusObjects[object] = d.capabilities.HasUbusObject(object)
	}
	return result, nil
}
