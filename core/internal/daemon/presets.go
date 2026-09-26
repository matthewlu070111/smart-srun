package daemon

import (
	"context"
	"encoding/json"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

type PresetRefreshParams struct {
	Interface      string `json:"iface"`
	IdempotencyKey string `json:"idempotency_key"`
	Session        string `json:"session,omitempty"`
}

func (d *Daemon) presetsRefresh(ctx context.Context, raw json.RawMessage) (any, error) {
	var params PresetRefreshParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	receipt, err := d.actions.Submit(ctx, application.Request{Kind: application.KindPresetsRefresh,
		Interface: params.Interface, IdempotencyKey: params.IdempotencyKey, Owner: probeOwner(params.Session)})
	if err != nil {
		return nil, err
	}
	return SubmitResult{ActionID: receipt.ActionID, State: string(receipt.State), Duplicate: receipt.Duplicate}, nil
}

// These are display/prefill views, not the editable source or an Issue export.
// In particular an unknown private document field can never enter this view.
type PresetView struct {
	ShortName    string           `json:"short_name"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Status       presets.Status   `json:"status"`
	Contributors []string         `json:"contributors"`
	Operators    []UserOperator   `json:"operators"`
	Defaults     PresetDefaults   `json:"defaults"`
	Login        PresetLoginShape `json:"observed_login_shape"`
	SourceIssue  string           `json:"source_issue"`
	DocURL       string           `json:"doc_url"`
}

type PresetDefaults struct {
	BaseURL    string `json:"base_url"`
	ACID       string `json:"ac_id"`
	SSID       string `json:"ssid"`
	AccessMode string `json:"access_mode"`
	WiredIface string `json:"wired_iface,omitempty"`
}

type PresetLoginShape struct {
	N           string `json:"n"`
	Type        string `json:"type"`
	Enc         string `json:"enc"`
	InfoPrefix  string `json:"info_prefix"`
	DoubleStack string `json:"double_stack"`
	OS          string `json:"os"`
	Name        string `json:"name"`
}

func presetView(s presets.School, iface string) PresetView {
	operators := make([]UserOperator, 0, len(s.Operators))
	for _, op := range s.Operators {
		operators = append(operators, UserOperator{op.Suffix, op.Label})
	}
	contributors := append([]string{}, s.Contributors...)
	return PresetView{ShortName: s.ShortName, Name: s.Name, Description: s.Description, Status: s.Status,
		Contributors: contributors, Operators: operators, SourceIssue: s.SourceIssue, DocURL: s.DocURL,
		Defaults: PresetDefaults{s.Defaults.BaseURL, s.Defaults.ACID, s.Defaults.SSID, s.Defaults.AccessMode, iface},
		Login: PresetLoginShape{s.ObservedLoginShape.N, s.ObservedLoginShape.Type, s.ObservedLoginShape.Enc,
			s.ObservedLoginShape.InfoPrefix, s.ObservedLoginShape.DoubleStack, s.ObservedLoginShape.OS, s.ObservedLoginShape.Name}}
}

type PresetListParams struct {
	Offset          int  `json:"offset,omitempty"`
	Limit           int  `json:"limit,omitempty"`
	IncludeInactive bool `json:"include_inactive,omitempty"`
}

type PresetListResult struct {
	Public     []PresetView   `json:"public"`
	User       []PresetView   `json:"user"`
	Operators  []UserOperator `json:"operators"`
	Revision   uint64         `json:"revision"`
	Total      int            `json:"total"`
	NextOffset *int           `json:"next_offset,omitempty"`
}

func (d *Daemon) presetsList(_ context.Context, raw json.RawMessage) (any, error) {
	var params PresetListParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	if params.Offset < 0 || params.Limit < 0 || params.Limit > 100 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "offset 必须非负，limit 不得超过 100")
	}
	if params.Limit == 0 {
		params.Limit = 50
	}
	schools, err := d.readPublicPresets()
	if err != nil {
		return nil, err
	}
	public := make([]presets.School, 0, len(schools))
	for _, school := range schools {
		if params.IncludeInactive || school.Status == presets.StatusActive {
			public = append(public, school)
		}
	}
	users, err := d.users.Get()
	if err != nil {
		return nil, err
	}
	result := PresetListResult{Public: []PresetView{}, User: []PresetView{},
		Operators: userPresetsResult(users).Operators, Revision: users.Revision, Total: len(public) + len(users.Presets)}
	// The input catalogue limit is larger than the RPC frame limit. Paginate
	// by encoded bytes as well as count, leaving space for the response envelope.
	encoded, _ := json.Marshal(result)
	size := len(encoded)
	if size > control.MaxResponseBytes-4096 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "运营商快捷选项超过列表响应上限")
	}
	for i, count := params.Offset, 0; i < result.Total; i++ {
		var view PresetView
		if i < len(public) {
			view = presetView(public[i], "")
		} else {
			user := users.Presets[i-len(public)]
			view = presetView(user.School, user.WiredIface)
		}
		data, _ := json.Marshal(view)
		if count == params.Limit || size+len(data)+1 > control.MaxResponseBytes-4096 {
			if count == 0 {
				return nil, domain.Errorf(domain.CodeInvalidArgument, "单条预设超过列表响应上限")
			}
			next := i
			result.NextOffset = &next
			break
		}
		size += len(data) + 1
		if i < len(public) {
			result.Public = append(result.Public, view)
		} else {
			result.User = append(result.User, view)
		}
		count++
	}
	return result, nil
}
