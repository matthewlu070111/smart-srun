package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// Discovery: what the wizard asks the gateway about itself.
//
// Only detect.verify may authenticate. Every method requires an explicit line
// and leaves the draft unsaved for the user to review in the wizard.

// DetectACIDParams names an address and the line to reach it by.
//
// The line is required. A probe that left by whichever route happened to be
// default would report another network's portal on a router with two lines,
// and the wizard would save it.
type DetectACIDParams struct {
	BaseURL        string `json:"base_url"`
	AccessMode     string `json:"access_mode,omitempty"`
	Iface          string `json:"iface,omitempty"`
	SSID           string `json:"ssid,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
	Session        string `json:"session,omitempty"`
	School         string `json:"school,omitempty"`
	ACID           string `json:"ac_id,omitempty"`
}

// DetectACIDResult is what the address step displays.
//
// `ok` means something was found, not that the account will work: a gateway
// that names its AC_ID has said nothing about whether this user can log in.
type DetectACIDResult struct {
	OK          bool     `json:"ok"`
	ACID        string   `json:"acid,omitempty"`
	ACIDSource  string   `json:"acid_source,omitempty"`
	BaseURL     string   `json:"base_url,omitempty"`
	DetectedURL string   `json:"detected_url,omitempty"`
	Checked     []string `json:"checked,omitempty"`
	Message     string   `json:"message"`
}

// probeLine opens a client bound to the selected line for one probe.
//
// One client per probe, closed when it ends, like a preset refresh: a socket
// that outlived the probe would be bound to an address the line may no longer
// have by the time anything reused it.
func (d *Daemon) probeLine(ctx context.Context, iface string) (portal.Fetcher, func(), error) {
	if iface == "" {
		return nil, nil, domain.FieldErrorf(domain.CodeInvalidArgument, "iface",
			"需要先选择出口线路，探测不会借用默认路由")
	}
	if d.openProbe == nil {
		return nil, nil, domain.Errorf(domain.CodeUnsupportedCapability,
			"本版本没有可用的探测出口")
	}
	return d.openProbe(ctx, iface)
}

func (d *Daemon) detectACID(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.submitDiscovery(ctx, raw, application.KindDetectACID)
}

func (d *Daemon) detectEnvironment(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.submitDiscovery(ctx, raw, application.KindDetectEnvironment)
}

func (d *Daemon) detectOperators(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.submitDiscovery(ctx, raw, application.KindDetectOperators)
}

func (d *Daemon) submitDiscovery(ctx context.Context, raw json.RawMessage, kind application.Kind) (any, error) {
	var params DetectACIDParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	return d.submitProbe(ctx, params, kind, "")
}

func (d *Daemon) submitProbe(ctx context.Context, params DetectACIDParams, kind application.Kind, private string) (any, error) {
	start := portal.Address(params.BaseURL)
	if start == "" && (params.BaseURL != "" || kind != application.KindDetectEnvironment) {
		return nil, domain.FieldErrorf(domain.CodeInvalidArgument, "base_url",
			"需要一个 http:// 或 https:// 的认证地址")
	}
	if params.ACID != "" && portal.ValidACID(params.ACID) != params.ACID {
		return nil, domain.FieldErrorf(domain.CodeInvalidArgument, "ac_id", "AC_ID 无效")
	}
	if params.AccessMode != "wired" && params.AccessMode != "wifi" {
		return nil, domain.FieldErrorf(domain.CodeInvalidArgument, "access_mode", "需要先选择有线或无线出口")
	}
	if params.AccessMode == "wifi" && params.SSID == "" {
		return nil, domain.FieldErrorf(domain.CodeInvalidArgument, "ssid", "无线探测需要明确的 SSID")
	}
	if params.AccessMode == "wired" && params.SSID != "" {
		return nil, domain.FieldErrorf(domain.CodeInvalidArgument, "ssid", "有线探测不接受 SSID")
	}
	receipt, err := d.actions.Submit(ctx, application.Request{
		Kind: kind, Interface: params.Iface,
		ProbeURL: start, ProbeMode: params.AccessMode, ProbeSSID: params.SSID,
		ProbeSchool:    params.School,
		ProbeACID:      params.ACID,
		PrivateJSON:    private,
		IdempotencyKey: params.IdempotencyKey,
		Owner:          probeOwner(params.Session),
	})
	if err != nil {
		return nil, err
	}
	return SubmitResult{ActionID: receipt.ActionID, State: string(receipt.State), Duplicate: receipt.Duplicate}, nil
}

type probeRoutingRunner struct {
	actions application.Runner
	daemon  *Daemon
}

func (r probeRoutingRunner) Run(ctx context.Context, action application.Action, report func(application.Phase)) application.Outcome {
	if !action.Request.Kind.Discovery() {
		return r.actions.Run(ctx, action, report)
	}
	budget := portal.Budget
	if action.Request.Kind == application.KindDetectEnvironment {
		budget = portal.EnvironmentBudget
	}
	if action.Request.Kind == application.KindDetectVerify {
		budget = verifyBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	report(application.PhaseFetch)
	var result any
	var message string
	var err error
	if action.Request.Kind == application.KindDetectEnvironment {
		var finding DetectEnvironmentResult
		finding, err = r.daemon.runEnvironment(ctx, action.Request)
		result, message = finding, finding.Message
	} else if action.Request.Kind == application.KindDetectIdentity || action.Request.Kind == application.KindDetectVerify {
		var finding VerificationResult
		finding, err = r.daemon.runVerification(ctx, action.Request, report)
		result, message = finding, finding.Message
	} else if action.Request.Kind == application.KindDetectOperators {
		var finding portal.OperatorsResult
		finding, err = r.daemon.runOperators(ctx, action.Request)
		result, message = finding, finding.Message
	} else {
		var finding DetectACIDResult
		finding, err = r.daemon.runACID(ctx, action.Request)
		result, message = finding, finding.Message
	}
	if err != nil {
		code, known := domain.CodeOf(err)
		if !known {
			code = domain.CodeTransportFailure
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = domain.CodeDeadlineExceeded
		}
		if errors.Is(err, context.Canceled) {
			code = domain.CodeCancelled
		}
		return application.Outcome{State: application.StateFailed, Code: code, Message: "无法完成认证探测，请检查所选线路与地址"}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return application.Outcome{State: application.StateFailed, Code: domain.CodeInternal, Message: "无法编码探测结果"}
	}
	return application.Outcome{State: application.StateSucceeded, Message: message, ResultJSON: string(encoded)}
}

func (d *Daemon) runOperators(ctx context.Context, request application.Request) (portal.OperatorsResult, error) {
	var result portal.OperatorsResult
	fetcher, release, err := d.probeLine(ctx, request.Interface)
	if err != nil {
		return result, err
	}
	defer release()
	if guard, ok := fetcher.(probeFetcher); ok {
		guard.ssid = request.ProbeSSID
		fetcher = guard
		if err := guard.check(ctx); err != nil {
			return result, err
		}
	}
	result, err = portal.ProbeOperators(ctx, fetcher, request.ProbeURL, request.ProbeACID)
	if err == nil {
		if guard, ok := fetcher.(probeFetcher); ok {
			err = guard.check(ctx)
		}
	}
	return result, err
}

func (d *Daemon) runACID(ctx context.Context, request application.Request) (DetectACIDResult, error) {
	params := DetectACIDParams{Iface: request.Interface, BaseURL: request.ProbeURL, AccessMode: request.ProbeMode, SSID: request.ProbeSSID}
	var result DetectACIDResult

	fetcher, release, err := d.probeLine(ctx, params.Iface)
	if err != nil {
		return result, err
	}
	defer release()
	if guard, ok := fetcher.(probeFetcher); ok {
		guard.ssid = params.SSID
		fetcher = guard
		if err := guard.check(ctx); err != nil {
			return result, err
		}
	}

	finding, err := portal.Probe(ctx, fetcher, params.BaseURL)
	if err == nil {
		if guard, ok := fetcher.(probeFetcher); ok {
			err = guard.check(ctx)
		}
	}
	d.log(logstore.EventDetectProbe, "",
		logstore.F("iface", params.Iface),
		logstore.F("checked", strconv.Itoa(len(finding.Checked))),
		logstore.F("acid_source", finding.Source))
	if err != nil {
		// What the chain reached before it failed is still worth showing: the
		// address the user typed may simply be one hop short.
		return result, err
	}

	result = DetectACIDResult{
		ACID: finding.ACID, ACIDSource: finding.Source,
		DetectedURL: finding.URL, Checked: finding.Checked,
	}
	switch {
	case finding.ACID != "":
		result.OK = true
		result.BaseURL = portal.Origin(finding.URL)
		result.Message = "已识别认证地址与 AC_ID"
	case portal.Origin(finding.URL) != "":
		// The page answered; it simply did not name an AC_ID. That is a normal
		// gateway, and 1 is the value the form falls back to.
		result.BaseURL = portal.Origin(finding.URL)
		result.Message = "已访问认证地址，但页面没有给出 AC_ID，可留空使用默认值 1"
	default:
		result.Message = "未能从该地址识别认证参数"
	}
	return result, nil
}

// openDeviceProbe is the real line: resolve where the interface is, then bind a
// client to it.
func (d *Daemon) openDeviceProbe(resolve resolveBinding) func(context.Context, string) (portal.Fetcher, func(), error) {
	return func(ctx context.Context, iface string) (portal.Fetcher, func(), error) {
		// Generation zero: a probe records no observation, so nothing compares
		// this binding against another one. The address and device are what it
		// needs, and both are read here and now.
		binding, err := resolve(ctx, iface, 0)
		if err != nil {
			return nil, nil, err
		}
		if !binding.Ready() {
			return nil, nil, domain.FieldErrorf(domain.CodeBindingUnavailable, "iface",
				"线路 %s 还没有可用的 IPv4 地址", iface)
		}
		client, err := transport.NewClient(binding)
		if err != nil {
			return nil, nil, err
		}
		return probeFetcher{do: client.Do, binding: binding, resolve: resolve,
			info: openwrt.NewAdapter(openwrt.Runner{}).RadioInfo}, client.Close, nil
	}
}

type resolveBinding func(ctx context.Context, iface string, generation uint64) (domain.Binding, error)

func probeOwner(session string) string {
	if session == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(session)))
}

type probeFetcher struct {
	do      func(*http.Request) (*http.Response, error)
	binding domain.Binding
	resolve resolveBinding
	info    func(context.Context, string) (openwrt.RadioInfo, error)
	ssid    string
}

func (f probeFetcher) SourceAddr() netip.Addr { return f.binding.SourceIPv4 }

func (f probeFetcher) check(ctx context.Context) error {
	current, err := f.resolve(ctx, f.binding.LogicalIface, 0)
	if err != nil {
		return err
	}
	if !current.Ready() || current.L3Device != f.binding.L3Device || current.IfIndex != f.binding.IfIndex || current.SourceIPv4 != f.binding.SourceIPv4 {
		return domain.Errorf(domain.CodeBindingChanged, "探测期间出口绑定发生变化")
	}
	if f.ssid != "" {
		info, err := f.info(ctx, current.L3Device)
		if err != nil {
			return err
		}
		if !info.Associated() || info.SSID != f.ssid || (strings.ToLower(info.Mode) != "client" && strings.ToLower(info.Mode) != "station") {
			return domain.Errorf(domain.CodeBindingUnavailable, "当前无线连接不是所选 SSID")
		}
	}
	return nil
}

func (f probeFetcher) Do(request *http.Request) (*http.Response, error) {
	if err := f.check(request.Context()); err != nil {
		return nil, err
	}
	// A portal answers a browser. Asking as one is not deception: a gateway
	// that serves a different page to an unknown agent would have the wizard
	// report parameters no browser would ever be given.
	request.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	response, err := f.do(request)
	if err != nil {
		return nil, err
	}
	if err := f.check(request.Context()); err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}
