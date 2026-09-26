package daemon

import (
	"context"
	"encoding/json"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// register binds the catalogue names this build can answer.
//
// Everything else in the catalogue stays unregistered on purpose. The
// dispatcher answers a declared-but-unbound name with "not implemented", which
// is a different thing from "unknown method": one tells a client the feature is
// not in this build, the other tells them they made a typo. Registering a stub
// that returned a plausible empty result would erase that difference.
func (d *Daemon) register(registry *control.Registry) {
	registry.Register("version.get", d.versionGet)
	registry.Register("status.get", d.statusGet)
	registry.Register("schema.get", d.schemaGet)
	registry.Register("config.get", d.configGet)
	registry.Register("config.export", d.configExport)
	registry.Register("config.import", d.configImport)
	registry.Register("config.validate", d.configValidate)
	registry.Register("config.apply", d.configApply)
	registry.Register("capabilities.get", d.capabilitiesGet)
	registry.Register("campus.get", d.campusGet)
	registry.Register("campus.upsert", d.campusUpsert)
	registry.Register("campus.remove", d.campusRemove)
	registry.Register("campus.set_default", d.campusSetDefault)
	registry.Register("hotspot.get", d.hotspotGet)
	registry.Register("hotspot.upsert", d.hotspotUpsert)
	registry.Register("hotspot.remove", d.hotspotRemove)
	registry.Register("hotspot.set_default", d.hotspotSetDefault)
	registry.Register("user_presets.get", d.userPresetsGet)
	registry.Register("user_presets.set", d.userPresetsSet)
	registry.Register("presets.list", d.presetsList)
	registry.Register("presets.refresh", d.presetsRefresh)
	registry.Register("action.submit", d.actionSubmit)
	registry.Register("action.get", d.actionGet)
	registry.Register("action.cancel", d.actionCancel)
	registry.Register("detect.acid", d.detectACID)
	registry.Register("detect.environment", d.detectEnvironment)
	registry.Register("detect.operators", d.detectOperators)
	registry.Register("detect.identity", d.detectIdentity)
	registry.Register("detect.verify", d.detectVerify)
	registry.Register("setup_wifi.start", d.setupWifiStart)
	registry.Register("setup_wifi.status", d.setupWifiStatus)
	registry.Register("setup_wifi.cancel", d.setupWifiCancel)
	registry.Register("setup_wifi.account", d.setupWifiAccount)
	registry.Register("setup_wifi.commit", d.setupWifiCommit)
	registry.Register("log.tail", d.logTail)
	registry.Register("log.download", d.logDownload)
	registry.Register("log.clear", d.logClear)
	registry.Register("update.check", d.updateCheck)
	registry.Register("update.start", d.updateStart)
	registry.Register("update.status", d.updateStatus)
	registry.Register("schools.list", d.schoolsList)
	registry.Register("schools.inspect", d.schoolsInspect)
	// school.command stays unbound: a strategy is declarative and may not do
	// I/O, so a declared command has nothing that could run it. The dispatcher
	// answering "not in this build" is the truthful reply until that exists.
}

// SchoolInspectParams names one strategy.
type SchoolInspectParams struct {
	ID string `json:"id"`
}

// schoolsList answers the strategies this build knows, from the same registry
// school_extra filtering and schema publication read. The CLI used to answer
// this offline from its own copy while the RPC stayed unbound.
func (d *Daemon) schoolsList(context.Context, json.RawMessage) (any, error) {
	return config.SchoolRegistry.List(), nil
}

func (d *Daemon) schoolsInspect(_ context.Context, raw json.RawMessage) (any, error) {
	var params SchoolInspectParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	school, ok := config.SchoolRegistry.Lookup(params.ID)
	if !ok {
		return nil, domain.FieldErrorf(domain.CodeNotFound, "id",
			"没有这个认证策略；学校参数预设请用 presets.list")
	}
	return school, nil
}

// VersionResult is what version.get answers.
type VersionResult struct {
	Version string `json:"version"`
	// RPCVersion lets a client refuse a daemon it does not understand without
	// having to parse a release string.
	RPCVersion int `json:"rpc_version"`
}

func (d *Daemon) versionGet(context.Context, json.RawMessage) (any, error) {
	return VersionResult{Version: d.version, RPCVersion: control.Version}, nil
}

func (d *Daemon) statusGet(context.Context, json.RawMessage) (any, error) {
	return d.Snapshot(), nil
}

func (d *Daemon) schemaGet(context.Context, json.RawMessage) (any, error) {
	// For the configured school, not the built-in one: the private fields a
	// page has to render depend on which strategy is selected.
	return config.BuildSchemaFor(d.config.Snapshot().School), nil
}

func (d *Daemon) configGet(context.Context, json.RawMessage) (any, error) {
	return redact(d.config.Snapshot()), nil
}

// redact removes every secret before a configuration goes on the wire.
//
// config.get is what fills the settings page, and the settings page does not
// display passwords. Spec 03 allows the plaintext only in an explicitly
// authorised account-detail response, which is a different method with a
// different audience.
//
// The clone matters as much as the blanking: without it this would erase the
// secrets in the repository's own snapshot.
func redact(cfg domain.Config) domain.Config {
	clean := config.CloneConfig(cfg)
	for index := range clean.CampusAccounts {
		clean.CampusAccounts[index].Password = ""
		clean.CampusAccounts[index].Key = ""
	}
	for index := range clean.HotspotProfiles {
		clean.HotspotProfiles[index].Key = ""
	}
	return clean
}

// ValidateParams carries a candidate configuration. Omitting it validates the
// stored one, which is what a user asking "is my configuration all right?"
// means.
type ValidateParams struct {
	Config json.RawMessage `json:"config,omitempty"`
}

// ValidateResult is the answer when the configuration is acceptable. A rejected
// one comes back as an error envelope with a problem per field, because a
// caller that has to inspect a result field to find out whether it failed will
// eventually forget to.
type ValidateResult struct {
	Valid    bool   `json:"valid"`
	Revision uint64 `json:"config_revision"`
}

func (d *Daemon) configValidate(_ context.Context, raw json.RawMessage) (any, error) {
	var params ValidateParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	if len(params.Config) == 0 {
		if err := config.Validate(d.config.Snapshot()); err != nil {
			return nil, err
		}
		return ValidateResult{Valid: true, Revision: d.config.Revision()}, nil
	}
	// Parse, not Validate: a candidate arrives as bytes, and the strict decode
	// is half of what makes it acceptable. Validating a decoded value would
	// skip the duplicate keys and unknown fields entirely.
	if _, err := config.Parse(params.Config); err != nil {
		return nil, err
	}
	return ValidateResult{Valid: true}, nil
}

// SubmitParams is one action request.
type SubmitParams struct {
	Kind           string `json:"kind"`
	AccountID      string `json:"account_id,omitempty"`
	HotspotID      string `json:"hotspot_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
	// IgnoreQuiet is the single-action quiet-hours override. It applies to this
	// action and leaves the schedule alone.
	IgnoreQuiet bool `json:"ignore_quiet,omitempty"`
	// ExpectedRevision, when given, refuses the action if the configuration has
	// moved since the caller read it. A login submitted from a page that was
	// showing the previous account's settings is not the login the user meant.
	ExpectedRevision *uint64 `json:"expected_revision,omitempty"`
}

// SubmitResult is spec 03's receipt: an id to poll, not an outcome. Accepting
// an action is not performing it.
type SubmitResult struct {
	ActionID string `json:"action_id"`
	State    string `json:"state"`
	// Duplicate says an identical key was already in flight, so nothing new
	// started. A double-click reports this, not a second login.
	Duplicate bool `json:"duplicate,omitempty"`
}

// schedulerOnly are the kinds the scheduler raises for itself. A caller that
// could submit them would be able to queue maintenance work that the
// maintenance loop did not decide to do, and to fake a quiet-hours sweep.
var schedulerOnly = map[application.Kind]bool{
	application.KindMaintain:          true,
	application.KindForcedLogout:      true,
	application.KindQuietHotspot:      true,
	application.KindQuietCampus:       true,
	application.KindPresetsRefresh:    true, // submitted through presets.refresh
	application.KindDetectACID:        true, // submitted through detect.acid
	application.KindDetectEnvironment: true, // submitted through detect.environment
	application.KindDetectOperators:   true,
	application.KindDetectIdentity:    true,
	application.KindDetectVerify:      true,
	application.KindWifiSetupStart:    true,
	application.KindWifiSetupCancel:   true,
}

func (d *Daemon) actionSubmit(ctx context.Context, raw json.RawMessage) (any, error) {
	var params SubmitParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}

	kind := application.Kind(params.Kind)
	if schedulerOnly[kind] {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"动作 %q 由调度器自行发起，不接受外部提交", params.Kind)
	}
	revision := d.config.Revision()
	if params.ExpectedRevision != nil {
		// The coordinator checks deduplication before revision. A retry of a
		// successful switch still refers to its original, pre-switch revision.
		revision = *params.ExpectedRevision
	}

	receipt, err := d.actions.Submit(ctx, application.Request{
		Kind:           kind,
		AccountID:      params.AccountID,
		HotspotID:      params.HotspotID,
		IdempotencyKey: params.IdempotencyKey,
		IgnoreQuiet:    params.IgnoreQuiet,
		CheckRevision:  true,
		ConfigRevision: revision,
	})
	if err != nil {
		return nil, err
	}
	return SubmitResult{ActionID: receipt.ActionID,
		State: string(receipt.State), Duplicate: receipt.Duplicate}, nil
}

// ActionParams names one action.
type ActionParams struct {
	ActionID string `json:"action_id"`
	// LuCI supplies its authenticated session when polling a discovery job.
	// The privileged CLI may inspect all actions without this web-only guard.
	Session string `json:"session,omitempty"`
}

func (d *Daemon) actionGet(ctx context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeActionParams(raw)
	if err != nil {
		return nil, err
	}
	action, err := d.actions.Action(ctx, params.ActionID)
	if err != nil {
		return nil, err
	}
	if params.Session != "" && action.Request.Owner != probeOwner(params.Session) {
		return nil, domain.Errorf(domain.CodeNotFound, "此会话没有该探测任务")
	}
	return struct {
		ActionView
		Result json.RawMessage `json:"result,omitempty"`
	}{ViewOf(action), json.RawMessage(action.ResultJSON)}, nil
}

func (d *Daemon) actionCancel(ctx context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeActionParams(raw)
	if err != nil {
		return nil, err
	}
	if params.Session != "" {
		action, err := d.actions.Action(ctx, params.ActionID)
		if err != nil {
			return nil, err
		}
		if action.Request.Owner != probeOwner(params.Session) {
			return nil, domain.Errorf(domain.CodeNotFound, "此会话没有该探测任务")
		}
	}
	if err := d.actions.Cancel(ctx, params.ActionID); err != nil {
		return nil, err
	}
	// The resulting state, not a bare acknowledgement: cancelling something
	// that had already finished is not an error, and the caller needs to be
	// able to see that is what happened.
	action, err := d.actions.Action(ctx, params.ActionID)
	if err != nil {
		return nil, err
	}
	return ViewOf(action), nil
}

func decodeActionParams(raw json.RawMessage) (ActionParams, error) {
	var params ActionParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return params, err
	}
	if params.ActionID == "" {
		return params, domain.Errorf(domain.CodeInvalidArgument, "需要 action_id")
	}
	return params, nil
}
