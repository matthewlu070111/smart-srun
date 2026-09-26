package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/wifi"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

// ConfirmWithin is how long an applied change has to be confirmed before
// recovery undoes it.
//
// Spec 04 gives the wizard fifteen minutes. A change this program makes on its
// own is confirmed as soon as the line comes up, so the window only matters
// when the process dies between applying and confirming -- which is exactly
// when a router would otherwise be left on a network nobody reported reaching.
const ConfirmWithin = 15 * time.Minute

// Apply moves the managed client and waits for the result to be usable.
//
// The sequence is spec 04's: refuse to start on somebody else's half-finished
// edit, back up, stage into an isolated directory, apply, reload, wait up to a
// minute for a real association and an address, and only then confirm. A change
// that does not produce a usable line is undone rather than left in place -- a
// router on a network it cannot reach is worse than one on the network it was
// already on.
func (w *deviceWireless) Apply(ctx context.Context, plan application.WirelessPlan) error {
	// One at a time. See the comment on the field: the coordinator serialises
	// actions, and this serialises the device.
	w.mu.Lock()
	defer w.mu.Unlock()

	if plan.Radio == "" {
		return domain.FieldErrorf(domain.CodeInvalidConfig, "radio",
			"没有指定要使用的无线电")
	}
	if plan.SSID == "" {
		return domain.FieldErrorf(domain.CodeInvalidConfig, "ssid",
			"没有指定要连接的 SSID")
	}

	section := stationSection(plan.Radio)
	application.ReportPhase(ctx, application.PhasePrepare)
	changes, err := w.changesFor(ctx, section, plan)
	if err != nil {
		return err
	}
	return w.applyChanges(ctx, changes, func() error { return w.awaitLine(ctx, plan) })
}

// applyChanges is shared by joining and retiring an uplink. The caller holds mu.
func (w *deviceWireless) applyChanges(ctx context.Context, changes []wireless.Change, verify func() error) error {
	w.tasks++
	transaction, err := wireless.Begin(ctx, w.store, w.paths, wireless.Plan{
		TaskID:         fmt.Sprintf("wl-%d-%d", w.clock.Now().Unix(), w.tasks),
		Package:        WirelessPackage,
		Changes:        changes,
		ConfigRevision: w.settings.Revision(),
		ConfirmWithin:  ConfirmWithin,
	}, w.clock.Now)
	if err != nil {
		return err
	}

	application.ReportPhase(ctx, application.PhaseActivate)
	if err := transaction.Apply(ctx, changes); err != nil {
		// Applied or not, the journal says where it got to, and rollback reads
		// that rather than guessing. A failure before the commit has nothing to
		// undo and says so.
		return w.undo(ctx, transaction, err)
	}
	if err := transaction.AwaitConfirm(); err != nil {
		return w.undo(ctx, transaction, err)
	}

	if err := verify(); err != nil {
		return w.undo(ctx, transaction, err)
	}
	application.ReportPhase(ctx, application.PhaseCommit)
	return transaction.Confirm()
}

// RecoverInterrupted deals with a change a previous run left open.
//
// Called once at startup, under the same lock everything else takes, and before
// any action is scheduled. Spec 04 puts three outcomes here and the difference
// between them matters: a change from the configuration that is still current
// and still inside its confirmation window is left alone, one whose window has
// passed is rolled back, and one whose options somebody else has since edited
// is reported rather than reverted over the top of them.
func (w *deviceWireless) RecoverInterrupted(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	outcome, acted, err := wireless.Recover(ctx, w.store, w.paths,
		w.settings.Revision(), w.clock.Now)
	if err != nil {
		return err
	}
	if !acted || len(outcome.Conflicts) == 0 {
		return nil
	}
	// Reported even though Recover did not fail: "recovery required" with no
	// detail tells a person there is a problem and nothing about where.
	return domain.Errorf(domain.CodeRecoveryRequired,
		"上次无线改动有 %d 项已被其他地方修改，未予还原；需要人工确认",
		len(outcome.Conflicts))
}

// UndoBudget is how long the rollback gets when the change's own context is
// already dead.
//
// Under the coordinator's ten-second shutdown grace on purpose. The undo runs
// on a context of its own -- a switch the user cancelled cannot unwind itself
// with a context that was cancelled, and leaving it unwound means leaving a
// journal on disk that Begin refuses to start on top of, so the next wireless
// change is blocked until a restart. That is the failure this budget exists to
// avoid.
//
// Bounded rather than generous because the other side of it is a service stop:
// an undo that outlasted the grace would be reported as a worker that ignored
// its cancellation. Running out of budget is survivable and already tested --
// the journal says rolling_back and the next start resumes it -- while hanging
// the stop is not.
const UndoBudget = 8 * time.Second

// undo rolls the change back and reports which of the two failures the caller
// is looking at.
//
// The original cause survives: "could not reach the network" and "could not
// reach the network, and could not get back either" are different situations
// for whoever has to fix it, and the second must not hide the first.
func (w *deviceWireless) undo(ctx context.Context, transaction *wireless.Transaction,
	cause error) error {
	application.ReportPhase(ctx, application.PhaseRollback)

	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), UndoBudget)
		defer cancel()
	}

	outcome, err := transaction.Rollback(ctx)
	if err != nil {
		return domain.Errorf(domain.CodeRecoveryRequired,
			"无线改动失败后回滚也没有成功（%d 项已还原，%d 项冲突）：%s",
			len(outcome.Restored), len(outcome.Conflicts), cause.Error()).Wrap(err)
	}
	return cause
}

// awaitLine waits for the radio to be on the right network with an address.
//
// Both halves are required and spec 04 says why: a scan result proves an access
// point exists, an association proves the client joined it, and only an address
// proves the line can carry anything. Authenticating over a client that
// associated and got nothing is a request that goes nowhere.
func (w *deviceWireless) awaitLine(ctx context.Context, plan application.WirelessPlan) error {
	wait := w.settleWait
	if seconds := w.settings.Snapshot().Checks.SwitchTimeoutSeconds; seconds > 0 {
		// Keep the transaction's sixty-second safety ceiling even if the
		// shared configuration allows a longer outer readiness budget.
		wait = min(wait, time.Duration(seconds)*time.Second)
	}
	deadline := w.clock.Now().Add(wait)
	application.ReportPhase(ctx, application.PhaseAssociation)
	target := wifi.Target{SSID: plan.SSID, Security: wifi.ParseSecurity(plan.Encryption), Policy: domain.APSelectionAuto}
	if plan.BSSID != "" {
		target.Policy, target.PinnedBSSID = domain.APSelectionFixed, plan.BSSID
	}
	var last error
	for {
		observed, err := w.Association(ctx, plan.Radio)
		switch {
		case err != nil:
			last = err
		case target.Satisfied(observed):
			return nil
		case observed.Joined() && observed.SSID == plan.SSID &&
			((target.Security.Protected() && !observed.Encrypted) || (plan.BSSID != "" && !strings.EqualFold(plan.BSSID, observed.BSSID))):
			last = domain.Errorf(domain.CodeBindingChanged, "实际无线关联与所选接入点或加密要求不符")
		default:
			last = describeWait(plan, observed)
		}
		if err == nil && observed.Joined() && observed.SSID == plan.SSID {
			application.ReportPhase(ctx, application.PhaseAddress)
		} else {
			application.ReportPhase(ctx, application.PhaseAssociation)
		}

		if !w.clock.Now().Before(deadline) {
			return domain.Errorf(domain.CodeDeadlineExceeded,
				"切换到 %s 后 %s 内没有连上并拿到地址：%s",
				plan.SSID, wait, last.Error())
		}
		wake := w.clock.Now().Add(w.settlePoll)
		if deadline.Before(wake) {
			wake = deadline
		}
		timer := w.clock.NewTimerAt(wake)
		select {
		case <-timer.C():
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
		timer.Stop()
	}
}

// describeWait says which of the three things is still missing.
//
// "Timed out" names the clock and nothing else. Whether the client never
// associated, associated with the wrong network, or associated and got no
// lease are three different faults with three different fixes.
func describeWait(plan application.WirelessPlan, observed wifi.Association) error {
	switch {
	case !observed.Joined():
		return domain.Errorf(domain.CodeBindingUnavailable, "还没有关联到任何接入点")
	case observed.SSID != plan.SSID:
		return domain.Errorf(domain.CodeConflict,
			"关联到的是 %s，不是 %s", observed.SSID, plan.SSID)
	default:
		return domain.Errorf(domain.CodeBindingUnavailable,
			"已关联 %s，但还没有取得 IPv4 地址", observed.SSID)
	}
}

// changesFor builds the uci changes one plan asks for.
//
// Everything this program owns and nothing else: the client section on the
// chosen radio, and the disabled flag on the other clients it also owns. Spec
// 04 forbids touching the home access point, and the way to keep that promise
// is for no change naming one to be constructible here.
func (w *deviceWireless) changesFor(ctx context.Context, section string,
	plan application.WirelessPlan) ([]wireless.Change, error) {

	config, err := w.adapter.UCI(ctx, WirelessPackage)
	if err != nil {
		return nil, err
	}

	option := func(name, text string) wireless.Change {
		return wireless.Change{
			Key:  wireless.Key{Section: section, Option: name},
			Text: text,
		}
	}
	// An absent option, not an empty one: uci has no empty option, so asking
	// for one writes nothing and leaves the journal describing a value that was
	// never there. Measured, and the 1.x runtime deletes these for the same
	// reason.
	remove := func(name string) wireless.Change {
		return wireless.Change{
			Key:    wireless.Key{Section: section, Option: name},
			Delete: true,
		}
	}

	changes := []wireless.Change{
		// The section first. Stage orders them anyway, but a plan that reads in
		// the order it happens is easier to check against the journal.
		{Key: wireless.Key{Section: section}, Text: openwrt.SectionWifiIface},
		option("device", plan.Radio),
		option("mode", "sta"),
		option("network", uplinkInterface),
		option("ssid", plan.SSID),
		option(openwrt.ManagedMarker, "1"),
		option("disabled", "0"),
	}

	if plan.Encryption == "" || strings.EqualFold(plan.Encryption, "none") {
		changes = append(changes, option("encryption", "none"), remove("key"))
	} else {
		changes = append(changes, option("encryption", plan.Encryption))
		if plan.Key == "" {
			return nil, domain.FieldErrorf(domain.CodeInvalidConfig, "key",
				"%s 需要密码，但没有配置", plan.SSID)
		}
		changes = append(changes, option("key", plan.Key))
	}

	if plan.BSSID == "" {
		changes = append(changes, remove("bssid"))
	} else {
		changes = append(changes, option("bssid", strings.ToLower(plan.BSSID)))
	}

	// Every other client this program owns goes down. Two enabled clients on
	// one radio is the ambiguity openwrt.StationOnRadio refuses to resolve, and
	// leaving an old one up is how a router ends up authenticating over the
	// network it was told to leave.
	for _, station := range openwrt.ManagedStations(config, section, knownSSIDs(w.settings)) {
		if station.Section == section || station.Disabled {
			continue
		}
		changes = append(changes, wireless.Change{
			Key:  wireless.Key{Section: station.Section, Option: "disabled"},
			Text: "1",
		})
	}
	return changes, nil
}

// uplinkInterface is the logical interface a managed client attaches to.
//
// `wwan` is what the 1.x runtime writes and what the router this was measured
// on has: the client sections are on wwan and the household's access points are
// on lan. Writing lan instead would put the campus network inside the home
// network's bridge.
const uplinkInterface = "wwan"

// knownSSIDs is every campus and hotspot network the configuration names.
//
// It is what lets a client the user built by hand be adopted rather than
// duplicated -- and what stops a home network being adopted, since one whose
// SSID is not in the configuration is never in this list.
func knownSSIDs(settings application.Settings) []string {
	cfg := settings.Snapshot()
	names := make([]string, 0, len(cfg.CampusAccounts)+len(cfg.HotspotProfiles))
	for _, account := range cfg.CampusAccounts {
		if account.SSID != "" {
			names = append(names, account.SSID)
		}
	}
	for _, hotspot := range cfg.HotspotProfiles {
		if hotspot.SSID != "" {
			names = append(names, hotspot.SSID)
		}
	}
	return names
}
