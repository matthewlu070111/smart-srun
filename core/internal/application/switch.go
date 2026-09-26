package application

import (
	"context"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
)

// WirelessPlan is the client configuration a switch asks for.
//
// It names the managed client's settings and nothing else. Spec 04 forbids
// touching the home access point, the LAN or anybody else's client, and the way
// to keep that promise is to have no way to express them here.
type WirelessPlan struct {
	Radio      string
	SSID       string
	Encryption string
	Key        string
	// BSSID pins the client to one access point. Empty means do not pin, which
	// is what the auto policy asks for and what lets the supplicant roam
	// without this program having to re-authenticate afterwards.
	BSSID string
}

// Wireless moves the managed client between networks and reports what it sees.
//
// Consumer-defined here: spec 02 asks for interfaces where they are used, and
// the implementation is daemon's deviceWireless, which applies a change as a
// transaction -- staging directory, before/after comparison, a journal that
// survives a reboot, and a rollback that only restores values this transaction
// wrote.
//
// It stays an interface rather than a direct dependency because a nil one is a
// meaningful state: a build or a device that cannot change a radio refuses a
// switch outright rather than performing half of one. daemon assembles it
// explicitly for that reason -- a nil *deviceWireless in an interface is not a
// nil interface, and that is exactly where the distinction would be lost.
type Wireless interface {
	// Association is what the client radio is doing right now.
	Association(ctx context.Context, radio string) (wifi.Association, error)
	// Scan lists the access points the radio can see. It is only called when
	// something actually has to be chosen: a scan takes the radio off its
	// channel, so an associated client pays for every one.
	Scan(ctx context.Context, radio string) ([]wifi.Candidate, error)
	// Apply moves the managed client and waits for the result to be usable --
	// associated, with an address.
	Apply(ctx context.Context, plan WirelessPlan) error
	// Retire disables only owned station uplinks after a wired target is ready.
	Retire(ctx context.Context) error
}

func (a *Authenticator) switchCampus(ctx context.Context, action Action, report func(Phase)) Outcome {
	if a.wireless == nil {
		return failure(domain.Errorf(domain.CodeUnsupportedCapability, "无法管理无线出口，未执行校园网切换"))
	}
	outcome := a.authenticate(ctx, action, report)
	if outcome.State != StateSucceeded {
		return outcome
	}
	cfg := a.settings.Snapshot()
	account, known := cfg.CampusAccountByID(action.Request.AccountID)
	if known && account.IsWired() {
		a.mu.Lock()
		before := a.seen[account.ID]
		a.mu.Unlock()
		report(PhaseRetire)
		if err := a.wireless.Retire(ctx); err != nil {
			outcome.State = StateFailed
			outcome.Code = domain.CodeRecoveryRequired
			outcome.Message = "有线账号已认证，但旧无线出口未能确认退出：" + userMessage(err)
		}
		// Applying wireless UCI can reload netifd. Do not carry a successful
		// authentication across a DHCP/device change caused by that reload.
		now, err := a.observeLine(ctx, account.ID, account.WiredIface)
		if err != nil || !sameLine(before, now) {
			outcome.State, outcome.Code = StateFailed, domain.CodeBindingChanged
			outcome.Message = "无线出口处理后有线绑定发生变化或无法确认，需要重新检查连接"
			if observation := outcome.Observation; observation != nil {
				observation.Auth, observation.Connectivity = domain.AuthUnknown, domain.ConnectivityUnknown
				if err != nil {
					observation.Link = domain.LinkMissing
				} else {
					observation.Generation = now.Generation
				}
			}
		}
	}
	return outcome
}

// destination is one end of a switch: where the radio should be, and how to
// recognise that it already is.
type destination struct {
	// what is the noun a message uses, so the same code can explain a move in
	// either direction.
	what  string
	label string
	plan  WirelessPlan
	want  wifi.Target
}

// hotspotDestination builds the target for a fallback uplink.
//
// Hotspots carry no AP policy and no pinned BSSID: the form has never offered
// them, and spec 03 keeps it that way. Auto is not a default chosen here, it is
// the only thing a hotspot can mean.
func hotspotDestination(hotspot domain.HotspotProfile) (destination, error) {
	if hotspot.SSID == "" {
		return destination{}, domain.FieldErrorf(domain.CodeInvalidConfig, "ssid",
			"热点 %s 没有填写 SSID", hotspot.ID)
	}
	label := hotspot.Label
	if label == "" {
		label = hotspot.SSID
	}
	return destination{
		what:  "热点",
		label: label,
		plan: WirelessPlan{
			Radio:      hotspot.Radio,
			SSID:       hotspot.SSID,
			Encryption: hotspot.Encryption,
			Key:        hotspot.Key,
		},
		want: wifi.Target{
			SSID:     hotspot.SSID,
			Security: wifi.ParseSecurity(hotspot.Encryption),
			Policy:   domain.APSelectionAuto,
		},
	}, nil
}

// campusDestination builds the target for a wireless campus account.
func campusDestination(account domain.CampusAccount) (destination, error) {
	if account.IsWired() {
		return destination{}, domain.Errorf(domain.CodeInvalidArgument,
			"账号 %s 是有线接入，没有可切换的无线网络", account.ID)
	}
	if account.SSID == "" {
		return destination{}, domain.FieldErrorf(domain.CodeInvalidConfig, "ssid",
			"无线账号 %s 没有填写 SSID", account.ID)
	}
	label := account.Label
	if label == "" {
		label = account.SSID
	}
	return destination{
		what:  "校园网",
		label: label,
		plan: WirelessPlan{
			Radio:      account.Radio,
			SSID:       account.SSID,
			Encryption: account.Encryption,
			Key:        account.Key,
		},
		want: wifi.Target{
			SSID:        account.SSID,
			Security:    wifi.ParseSecurity(account.Encryption),
			Policy:      account.APSelection,
			PinnedBSSID: account.BSSID,
		},
	}, nil
}

// switchHotspot moves the uplink to a hotspot profile.
func (a *Authenticator) switchHotspot(ctx context.Context, action Action,
	report func(Phase)) Outcome {

	cfg := a.settings.Snapshot()
	hotspot, known := cfg.HotspotByID(action.Request.HotspotID)
	if !known {
		return Outcome{State: StateFailed, Code: domain.CodeNotFound,
			Message: "热点 " + action.Request.HotspotID + " 不存在"}
	}
	dest, err := hotspotDestination(hotspot)
	if err != nil {
		return failure(err)
	}
	if a.wireless == nil {
		return Outcome{State: StateFailed, Code: domain.CodeUnsupportedCapability,
			Message: "这个版本还不能修改无线设置，无法切换到" + dest.what}
	}

	if err := a.moveTo(ctx, dest, report); err != nil {
		return a.failBack(ctx, cfg, dest, err, report)
	}
	report(PhaseVerify)
	level, err := a.checkHotspot(ctx, cfg, hotspot)
	if err != nil {
		return a.failBack(ctx, cfg, dest, err, report)
	}
	if level != domain.ConnectivityInternetReachable {
		if cfg.Failover.HotspotFailbackEnabled {
			return a.failBack(ctx, cfg, dest, domain.Errorf(domain.CodeTransportFailure, "热点尚未确认互联网连通"), report)
		}
		return Outcome{State: StateSucceeded, Message: "已连接热点 " + dest.label + "，但尚未确认互联网连通"}
	}
	return Outcome{State: StateSucceeded,
		Message: "已切换到" + dest.what + " " + dest.label}
}

// moveTo puts the radio on one network, choosing an access point if it has to.
func (a *Authenticator) moveTo(ctx context.Context, dest destination,
	report func(Phase)) error {

	report(PhaseSwitch)

	// Spec 04: an existing association that matches the SSID and has an address
	// is reused. Re-applying it would cost a reassociation, a new lease and
	// therefore a fresh authentication, to arrive where the radio already is.
	observed, err := a.wireless.Association(ctx, dest.plan.Radio)
	if err == nil && !wifi.ShouldReselect(dest.want, observed) {
		report(PhaseReuse)
		return nil
	}

	// Only scan when something actually has to be chosen. Auto pins nothing, so
	// a scan would take the radio off its channel to produce a list nobody
	// reads.
	var candidates []wifi.Candidate
	if dest.want.NeedsScan() {
		candidates, err = a.wireless.Scan(ctx, dest.plan.Radio)
		if err != nil {
			return err
		}
	}
	decision, err := wifi.Select(dest.want, candidates)
	if err != nil {
		return err
	}
	plan := dest.plan
	plan.BSSID = decision.BSSID
	return a.wireless.Apply(ctx, plan)
}

// failBack returns the radio to the campus network a switch moved it away from.
//
// The rule T22 asks for is in the last branch: when the way back also fails,
// this must not report that the old network was restored. A user told "switch
// failed, you are back on the campus network" stops looking, and the radio is
// on neither network.
func (a *Authenticator) failBack(ctx context.Context, cfg domain.Config,
	dest destination, cause error, report func(Phase)) Outcome {

	outcome := failure(cause)
	outcome.Message = "切换到" + dest.what + " " + dest.label + "失败：" + outcome.Message

	if !cfg.Failover.HotspotFailbackEnabled {
		return outcome
	}
	account, known := cfg.CampusAccountByID(cfg.Selection.ActiveCampusID)
	if !known {
		return outcome
	}
	previous, err := campusDestination(account)
	if err != nil {
		// A wired account has nothing to go back to on the radio, and that is
		// not a failure of the failback -- there was nothing to undo.
		return outcome
	}

	// The same context deliberately. A cancelled switch cannot be unwound by a
	// worker whose context is already dead, and pretending otherwise would put
	// an unbounded operation on the stop path. Surviving a cancellation is what
	// M10's journal is for; what is guaranteed here is that the outcome says
	// which of the two happened.
	if err := a.moveTo(ctx, previous, report); err != nil {
		return Outcome{
			State: StateFailed,
			Code:  domain.CodeRecoveryRequired,
			Message: "切换到" + dest.what + "失败，回切到" + previous.what +
				"也没有成功，无线连接需要人工处理",
			Observation: outcome.Observation,
		}
	}
	outcome.Message += "，已回切到" + previous.what
	return outcome
}

// ensureWirelessLine puts a wireless account's radio on its own network before
// anything tries to authenticate over it.
//
// It returns false when the caller should carry on. A wired account, or a build
// with no wireless transaction, is carried on with: authentication over a line
// this program did not arrange is still authentication, and refusing it because
// the radio cannot be managed would break every router whose client was set up
// by hand.
func (a *Authenticator) ensureWirelessLine(ctx context.Context, action Action,
	report func(Phase)) (Outcome, bool) {

	if a.wireless == nil {
		return Outcome{}, false
	}
	cfg := a.settings.Snapshot()
	account, known := cfg.CampusAccountByID(action.Request.AccountID)
	if !known || account.IsWired() {
		return Outcome{}, false
	}
	// Capture the generation before any radio operation. A failed association
	// never reaches prepare(), but still invalidates the previous campus claim.
	// Keeping this stamp also prevents a late result from replacing a recovery.
	revision := a.settings.Revision()
	a.mu.Lock()
	generation := a.seen[account.ID].Generation
	a.mu.Unlock()
	if generation == 0 {
		generation = a.generation.Add(1)
	}
	unconfirmed := func(outcome Outcome) (Outcome, bool) {
		a.invalidateLine(account.ID, generation)
		prepared := attempt{account: account, revision: revision, sequence: action.Sequence,
			binding: domain.Binding{Generation: generation}}
		outcome.Observation = prepared.observation(a, domain.AuthUnknown, domain.ConnectivityUnknown, "")
		return outcome, true
	}
	if action.Request.Kind == KindMaintain {
		if outcome, stop := a.deferOnHotspot(ctx, cfg, account); stop {
			return unconfirmed(outcome)
		}
	}
	dest, err := campusDestination(account)
	if err != nil {
		return unconfirmed(failure(err))
	}
	if err := a.moveTo(ctx, dest, report); err != nil {
		outcome := failure(err)
		outcome.Message = "无法连接到" + dest.what + " " + dest.label + "：" +
			outcome.Message
		return unconfirmed(outcome)
	}
	return Outcome{}, false
}

// deferOnHotspot observes the actual association instead of saving a temporary
// enabled=false into user configuration. This also works after a daemon restart:
// a fresh worker must not move a working hotspot back to the old campus SSID.
// Explicit user actions can still return to campus. Wired WANs are independent.
func (a *Authenticator) deferOnHotspot(ctx context.Context, cfg domain.Config,
	account domain.CampusAccount) (Outcome, bool) {

	seen := map[string]wifi.Association{}
	for _, hotspot := range cfg.HotspotProfiles {
		if hotspot.Radio == "" || hotspot.SSID == "" ||
			(hotspot.Radio == account.Radio && hotspot.SSID == account.SSID) {
			continue
		}
		observed, read := seen[hotspot.Radio]
		if !read {
			var err error
			observed, err = a.wireless.Association(ctx, hotspot.Radio)
			if err != nil {
				// Unknown is not permission to reconfigure a possibly working uplink.
				return failure(err), true
			}
			seen[hotspot.Radio] = observed
		}
		if observed.Joined() && observed.SSID == hotspot.SSID {
			return Outcome{State: StateFailed, Code: domain.CodeBusy,
				Message:             "当前连接为已配置热点，自动校园认证已暂停",
				MaintenanceDeferred: true}, true
		}
	}
	return Outcome{}, false
}

// failure turns an error into a reportable outcome, with this program's own
// text rather than the error's.
func failure(cause error) Outcome {
	code := domain.CodeInternal
	if observed, ok := domain.CodeOf(cause); ok {
		code = observed
	}
	return Outcome{State: StateFailed, Code: code, Message: userMessage(cause)}
}
