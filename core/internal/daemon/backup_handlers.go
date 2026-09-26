package daemon

import (
	"context"
	"encoding/json"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type BackupImportParams struct {
	// Keep the uploaded JSON intact until the strict parser sees it. Decoding
	// through Lua's JSON object parser first would hide duplicate fields.
	Data             string  `json:"data"`
	CheckOnly        bool    `json:"check_only"`
	ExpectedRevision *uint64 `json:"expected_revision,omitempty"`
}

type BackupImportResult struct {
	OK               bool     `json:"ok"`
	CampusAccounts   int      `json:"campus_accounts"`
	HotspotProfiles  int      `json:"hotspot_profiles"`
	ExpectedRevision uint64   `json:"expected_revision"`
	Warnings         []string `json:"warnings"`
	Message          string   `json:"message"`
}

func (d *Daemon) configExport(_ context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		IncludeSecrets bool `json:"include_secrets"`
		AsJSON         bool `json:"as_json,omitempty"`
	}
	if config.DecodePatch(raw, &params) != nil || !params.IncludeSecrets {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "导出备份需要明确 include_secrets=true；文件含账号密码")
	}
	backup, err := config.ExportBackup(d.config.Snapshot())
	if err != nil || !params.AsJSON {
		return backup, err
	}
	// Lua's JSON decoder loses the distinction between empty objects and
	// arrays. Carry the serialized document as a string through the bridge so
	// a download preserves the exact typed JSON accepted by config.import.
	data, err := json.Marshal(backup)
	if err != nil {
		return nil, err
	}
	return struct {
		Data string `json:"data"`
	}{Data: string(data)}, nil
}

func (d *Daemon) configImport(ctx context.Context, raw json.RawMessage) (any, error) {
	var params BackupImportParams
	if config.DecodeBackupTransfer(raw, &params) != nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "备份导入请求无效")
	}
	cfg, warnings, err := config.ParseBackup([]byte(params.Data))
	if err != nil {
		return nil, err
	}
	result := BackupImportResult{true, len(cfg.CampusAccounts), len(cfg.HotspotProfiles), d.config.Revision(), warnings,
		"配置已校验；导入后自动守护保持关闭，确认账号和网口后再启用"}
	if params.CheckOnly {
		return result, nil
	}
	written, err := d.changeConfig(ctx, params.ExpectedRevision, "", "", func(current *domain.Config) error {
		*current = config.CloneConfig(cfg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.ExpectedRevision = written.(ConfigWriteResult).Revision
	result.Message = "配置已导入；请确认账号和网口后再启用自动守护"
	return result, nil
}
