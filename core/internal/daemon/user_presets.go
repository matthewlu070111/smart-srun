package daemon

import (
	"context"
	"encoding/json"
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

// UserPresetsResult returns the editable source plus a deduplicated shortcut
// view. The bridge must round-trip Document so unknown source fields survive.
// This is private local data, not the public Issue/export payload.
type UserPresetsResult struct {
	Revision  uint64          `json:"revision"`
	Document  json.RawMessage `json:"document"`
	Operators []UserOperator  `json:"operators"`
}

type UserOperator struct {
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
}

type UserPresetsParams struct {
	ExpectedRevision *uint64         `json:"expected_revision"`
	Document         json.RawMessage `json:"document"`
}

func userPresetsResult(document presets.UserDocument) UserPresetsResult {
	result := UserPresetsResult{Revision: document.Revision,
		Document: document.Document(), Operators: make([]UserOperator, 0, len(document.Operators))}
	for _, operator := range document.Operators {
		result.Operators = append(result.Operators, UserOperator{operator.Suffix, operator.Label})
	}
	return result
}

func (d *Daemon) userPresetsGet(_ context.Context, raw json.RawMessage) (any, error) {
	if err := control.DecodeParams(raw, &struct{}{}); err != nil {
		return nil, err
	}
	document, err := d.users.Get()
	if err != nil {
		return nil, err
	}
	return userPresetsResult(document), nil
}

func (d *Daemon) userPresetsSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var params UserPresetsParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	if params.ExpectedRevision == nil || len(params.Document) == 0 {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"保存用户预设需要 expected_revision 和 document")
	}
	public, err := d.readPublicPresets()
	if err != nil {
		return nil, err
	}
	var document presets.UserDocument
	err = d.actions.ChangeConfiguration(ctx, func() error {
		if err := update.Guard(d.paths.Update()); err != nil {
			return err
		}
		var err error
		document, err = d.users.Set(ctx, *params.ExpectedRevision, params.Document, public)
		return err
	})
	if err != nil {
		return nil, err
	}
	return userPresetsResult(document), nil
}

func (d *Daemon) readPublicPresets() ([]presets.School, error) {
	if d.publicPresets != nil {
		return d.publicPresets()
	}
	// M15 installs this file. A missing package resource must be diagnosed;
	// treating it as an empty catalogue would silently disable collision checks.
	file, err := os.Open(d.paths.PresetFile())
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal, "无法读取内置学校预设").Wrap(err)
	}
	defer file.Close()
	raw, err := presets.ReadLimited(file, presets.MaxPayloadBytes)
	if err != nil {
		return nil, err
	}
	builtin, err := presets.Parse(raw)
	if err != nil {
		return nil, err
	}
	schools, err := presets.Offline(builtin, presets.NewCache(d.paths.PresetCacheFile()))
	if err != nil {
		d.onError(err)
	}
	return schools, nil
}
