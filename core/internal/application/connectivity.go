package application

import (
	"context"

	"github.com/matthewlu070111/smart-srun/core/internal/auth"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	portalprobe "github.com/matthewlu070111/smart-srun/core/internal/portal"
)

// Maintenance observes before authenticating. Repeated logins can disturb an
// already working session; an unreadable identity is not permission to send one.
func (a *Authenticator) checkExisting(ctx context.Context, tx *auth.Transaction, p *attempt, report func(Phase)) (Outcome, bool) {
	report(PhaseVerify)
	identity, err := tx.Online(ctx, p.username)
	if err != nil {
		return p.failed(a, err, domain.AuthUnknown, ""), true
	}
	switch identity.State() {
	case domain.AuthVerifiedSelf:
		return a.verifyConnectivity(ctx, p, "本线路已是该账号的在线会话", identity.Username), true
	case domain.AuthVerifiedOther:
		return p.failed(a, domain.Errorf(domain.CodeOnlineIdentityMismatch,
			"这条线路上在线的是另一个账号，自动维护不会将其下线"), domain.AuthVerifiedOther, identity.Username), true
	default:
		return Outcome{}, false
	}
}

// ConnectivityDiagnosis records how an Internet check reached its verdict.
//
// It is for a DEBUG log line: which endpoints failed and how over the bound
// line, whether the system route was consulted, and what it answered. None of
// it names a credential.
type ConnectivityDiagnosis struct {
	// Via is "bound" when the bound line proved Internet access, "system"
	// when only the system route did, and empty when neither did.
	Via string
	// Bound is the per-endpoint summary over the bound line.
	Bound string
	// BoundLast is the kind of the last bound endpoint failure.
	BoundLast string
	// System is the per-endpoint summary over the system route, when asked.
	System string
	// Fallback says why the system route was or was not consulted.
	Fallback string
}

const (
	fallbackUsed      = "used"
	fallbackMultiWAN  = "skipped_multi_wan"
	fallbackSwitching = "skipped_switch"
	fallbackNoClient  = "unavailable"
	viaBound          = "bound"
	viaSystem         = "system"
)

// systemFallback reports whether the unbound system route may answer the
// Internet question for this attempt, and if not, why.
//
// Only on a router with one uplink: with multi-WAN the default route may be a
// different line, and its answer would say nothing about this one. Never while
// switching to campus: the hotspot this switch is replacing can still own the
// default route, and a 204 through it would retire a working uplink.
func (a *Authenticator) systemFallback(p *attempt) (bool, string) {
	switch {
	case a.systemProbe == nil:
		return false, fallbackNoClient
	case p.multiWAN:
		return false, fallbackMultiWAN
	case p.kind == KindSwitchCampus || p.kind == KindQuietCampus:
		return false, fallbackSwitching
	default:
		return true, fallbackUsed
	}
}

// Authentication and connectivity remain separate evidence. A timed-out probe
// must not erase a verified identity, trigger an unbind, or blame the password.
func (a *Authenticator) verifyConnectivity(ctx context.Context, p *attempt, message, identity string) Outcome {
	out := p.succeeded(a, message, identity)
	var err error
	switch p.checkMode {
	case domain.CheckInternet:
		report, cause := portalprobe.CheckConnectivityReport(ctx, p.line, a.probeURLs, portalprobe.EndpointBudget)
		err = cause
		level := report.Level
		diagnosis := &ConnectivityDiagnosis{Bound: report.Summary(), BoundLast: string(report.LastFailure())}
		if err == nil && level == domain.ConnectivityInternetReachable {
			diagnosis.Via = viaBound
		}
		// The identity was verified over the bound line moments ago, so the
		// account is online. A local proxy that hijacks the router's DNS can
		// still make every bound probe fail; the router's own route is then
		// the better witness for "is the Internet reachable".
		if err == nil && level != domain.ConnectivityInternetReachable {
			allowed, reason := a.systemFallback(p)
			diagnosis.Fallback = reason
			if allowed {
				system, systemErr := portalprobe.CheckConnectivityReport(ctx, a.systemProbe, a.probeURLs, portalprobe.SystemEndpointBudget)
				diagnosis.System = system.Summary()
				if systemErr == nil && system.Level == domain.ConnectivityInternetReachable {
					level, diagnosis.Via = system.Level, viaSystem
				}
			}
		}
		out.Connectivity = diagnosis
		if level != domain.ConnectivityUnknown {
			out.Observation.Connectivity = level
		}
		if err == nil && level != domain.ConnectivityInternetReachable {
			err = domain.Errorf(domain.CodeTransportFailure, "已确认本账号在线，但尚未确认互联网连通，请检查线路或更换在线判定模式")
		}
	case domain.CheckPortal:
		// The verified identity was read from this bound portal just now.
	case domain.CheckSSID:
		if !p.account.IsWired() && a.wireless == nil {
			err = domain.Errorf(domain.CodeUnsupportedCapability, "无法确认所选校园无线的实际连接状态")
		}
	default:
		err = domain.Errorf(domain.CodeInvalidConfig, "在线判定模式无效")
	}
	// A response on an old socket must not validate the address acquired during
	// the probe. This also requires a usable IPv4 for wired SSID mode.
	if changed := a.confirmUnchanged(ctx, p); changed != nil {
		return p.failed(a, changed, domain.AuthUnknown, "")
	}
	if !p.account.IsWired() && a.wireless != nil {
		dest, cause := campusDestination(p.account)
		if cause != nil {
			return p.failed(a, cause, domain.AuthUnknown, "")
		}
		observed, cause := a.wireless.Association(ctx, p.account.Radio)
		if cause != nil {
			return p.failed(a, cause, domain.AuthUnknown, "")
		}
		if !dest.want.Satisfied(observed) {
			return p.failed(a, domain.Errorf(domain.CodeBindingChanged, "当前无线关联与所选校园网络或接入点不符"), domain.AuthUnknown, "")
		}
	}
	if err != nil {
		out.State, out.Code, out.Message = StateFailed, domain.CodeTransportFailure, userMessage(err)
		if code, ok := domain.CodeOf(err); ok {
			out.Code = code
			if code == domain.CodeBindingChanged || code == domain.CodeBindingUnavailable {
				out.Observation.Auth, out.Observation.Connectivity = domain.AuthUnknown, domain.ConnectivityUnknown
				out.Observation.Identity = ""
			}
		}
	}
	return out
}
