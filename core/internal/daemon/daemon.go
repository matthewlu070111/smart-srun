package daemon

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

// readBudget bounds an internal read of the coordinator's state.
//
// Every such read is a channel round-trip to a loop that is not doing anything
// slow, so this is a fault detector rather than a timeout anybody should reach.
const readBudget = 3 * time.Second

// Options configures the service.
type Options struct {
	Paths   Paths
	Clock   policy.Clock
	Version string

	// Runner performs actions. Nil means the real one: an authenticator wired
	// to this device's adapter and connection pool. A test supplies its own so
	// that the lifecycle can be exercised without a router.
	Runner       application.Runner
	wizardDevice *deviceWireless // Synthetic device for daemon integration tests.
	// PresetRefresh overrides the bound refresh worker for lifecycle tests.
	// Nil builds the real adapter/transport path, independent of authentication.
	PresetRefresh application.Runner
	UpdateSource  update.ReleaseSource
	UpdateDevice  update.Device
	UpdateLaunch  func() error

	// PublicPresets reads the current merged public catalogue, including drafts.
	// Nil uses the installed built-in file and the tmpfs cache. Reading this
	// never triggers a remote refresh or an authentication request.
	PublicPresets func() ([]presets.School, error)

	// Capabilities is what this device can actually do, detected once by the
	// caller. Detected once because it does not change while the process runs,
	// and because status polling must not shell out.
	Capabilities openwrt.Capabilities

	// OnError receives faults that have nobody to return them to: a snapshot
	// that could not be written, a connection that failed mid-answer. It is the
	// seam the structured log replaces in M11. Nil discards them, which is a
	// choice a caller has to make on purpose.
	OnError func(error)

	// Ready is called once, when the socket is listening and every method this
	// build answers is bound. Before it, a caller that dialled would get a
	// connection refused; after it, the service is answering.
	Ready func()

	// Observer, if set, is called after each action change has been recorded.
	// It is how a supervisor watches the lifecycle without polling, and it is
	// where the structured event log attaches in M11.
	Observer func(application.Action)

	// OpenProbe opens a read-only client bound to one line, for discovery. Nil
	// uses this device's adapter and a real bound socket; a test supplies one
	// so that a probe can be exercised against a local server, which no bound
	// client could reach.
	OpenProbe func(ctx context.Context, iface string) (portal.Fetcher, func(), error)
	// ProbeGateways reads only the selected netifd interface's default routes.
	ProbeGateways func(context.Context, string) ([]string, error)
}

// Daemon is the assembled service.
type Daemon struct {
	paths        Paths
	version      string
	clock        policy.Clock
	capabilities openwrt.Capabilities
	onError      func(error)

	config        *config.Repository
	store         *observe.Store
	events        *logstore.Store
	openProbe     func(ctx context.Context, iface string) (portal.Fetcher, func(), error)
	probeGateways func(context.Context, string) ([]string, error)
	actions       *application.Coordinator
	observer      func(application.Action)
	users         *presets.UserStore
	publicPresets func() ([]presets.School, error)
	configChanged func(uint64)
	wizard        *wifiWizard
	manualPauses  *manualPauses
	updater       *updateController
	wirelessState wirelessObservation

	// published is the last state an action was logged in, so a republication
	// -- a cancellation arriving while the action is still running -- does not
	// produce a second "started" line. Written only from the coordinator's
	// goroutine, like everything else in onAction.
	published map[string]string

	// pause is why automatic authentication is suspended, as the maintenance
	// loop last reported it. Written from that loop and read by any status
	// call, so it takes the one lock in this struct.
	pauseMu sync.Mutex
	pause   []string

	// dirty is a one-slot signal, so a burst of changes coalesces into one
	// write instead of one write per change.
	dirty chan struct{}
}

// Run starts the service and returns when ctx is done.
//
// The order matters at both ends. The lock is taken before anything is opened,
// so a second copy fails before it can touch the first one's files. The socket
// is removed and the stopped snapshot written after the loops have stopped, so
// nothing is still answering when a reader is told the service is gone.
func Run(ctx context.Context, options Options) error {
	paths := options.Paths
	if paths.Runtime == "" || paths.Config == "" {
		defaults := DefaultPaths()
		if paths.Runtime == "" {
			paths.Runtime = defaults.Runtime
		}
		if paths.Config == "" {
			paths.Config = defaults.Config
		}
	}
	clock := options.Clock
	if clock == nil {
		clock = policy.SystemClock{}
	}
	onError := options.OnError
	if onError == nil {
		onError = func(error) {}
	}

	// The event log is opened before anything that can fail, so a fault during
	// startup is recorded and not only printed to whatever captured stderr.
	events := logstore.New(paths.LogFile())
	// Raw, not the wrapper below: a failure to write the log cannot be logged.
	events.OnError(onError)
	report := func(err error) {
		if err == nil {
			return
		}
		onError(err)
		// The error's own text, which for this program's errors is a message it
		// wrote. Wrapped causes stay out of it, exactly as they stay out of the
		// wire envelope.
		events.Emit(clock.Now(), domain.LogError, logstore.EventInternalError,
			err.Error())
	}

	if err := EnsureRuntimeDir(paths); err != nil {
		return err
	}
	lock, err := Acquire(paths.Lock())
	if err != nil {
		return err
	}
	defer lock.Release()

	repository, err := config.Open(paths.ConfigFile())
	if err != nil {
		return err
	}

	// One pool for the process, closed when the service stops. Its clients hold
	// sockets bound to particular addresses, and leaving them open past the
	// stop would leave a socket bound to an address the next start may not
	// have.
	pool := transport.NewPool()
	defer pool.Close()

	// The worker is built here rather than by the caller: the CLI has no
	// business knowing which adapter or which pool the service authenticates
	// through, and a test supplies its own through Options.
	runner := options.Runner
	wizardDevice := options.wizardDevice
	if runner == nil {
		// Written out rather than passed straight through: a nil
		// *deviceWireless in an interface is not a nil interface, and the check
		// for "this build cannot change a radio" is exactly where that
		// distinction would be lost.
		var radio application.Wireless
		device, err := newDeviceWirelessFor(paths, repository, clock)
		if err != nil {
			// Reported, not fatal. A service that refused to start because it
			// could not prepare a wireless staging directory would stop
			// authenticating a wired line for a reason that has nothing to do
			// with it. Without a radio, a switch is refused outright.
			report(err)
		} else {
			radio = device
			wizardDevice = device
			// Before anything is scheduled. A change the last run applied and
			// never confirmed is either still meaningful or has to be undone,
			// and spec 04 will not have that decided while a switch is already
			// running against the same radio.
			if update.Guard(paths.Update()) == nil {
				if err := device.RecoverInterrupted(ctx); err != nil {
					report(err)
				}
			}
		}
		runner = newDeviceRunner(repository, pool, clock, radio)
	}

	observer := options.Observer
	if observer == nil {
		observer = func(application.Action) {}
	}

	service := &Daemon{
		paths:         paths,
		version:       options.Version,
		clock:         clock,
		capabilities:  options.Capabilities,
		onError:       report,
		config:        repository,
		store:         observe.New(),
		events:        events,
		observer:      observer,
		users:         presets.NewUserStore(paths.UserPresets()),
		publicPresets: options.PublicPresets,
		published:     map[string]string{},
		dirty:         make(chan struct{}, 1),
	}
	service.initUpdater(ctx, options)
	defer service.updater.close()
	service.openProbe = options.OpenProbe
	service.probeGateways = options.ProbeGateways
	if service.probeGateways == nil {
		service.probeGateways = deviceProbeGateways
	}
	if service.openProbe == nil {
		service.openProbe = service.openDeviceProbe(
			openwrt.NewAdapter(openwrt.Runner{}).ResolveBinding)
	}
	service.store.SetRevision(repository.Revision())
	if wizardDevice != nil {
		service.wizard = newWifiWizard(service, wizardDevice)
		if update.Guard(paths.Update()) == nil {
			if err := service.wizard.recover(ctx); err != nil {
				report(err)
			}
		}
	}

	// The threshold is the user's setting, applied before the first line: a log
	// that started at the default and changed level a moment later would open
	// every run with lines the user asked not to see.
	initial := repository.Snapshot()
	events.SetLevel(initial.Log.Level)
	service.log(logstore.EventDaemonStart, "", logstore.F("version", options.Version),
		logstore.F("pid", strconv.Itoa(os.Getpid())))
	service.log(logstore.EventConfigLoaded, "",
		logstore.F("revision", strconv.FormatUint(initial.Revision, 10)),
		logstore.F("enabled", strconv.FormatBool(initial.Enabled)),
		logstore.F("campus_accounts", strconv.Itoa(len(initial.CampusAccounts))),
		logstore.F("hotspot_profiles", strconv.Itoa(len(initial.HotspotProfiles))))
	defer service.log(logstore.EventDaemonStop, "")
	refresh := options.PresetRefresh
	if refresh == nil {
		refresh = application.PresetRefresher{
			Resolve: openwrt.NewAdapter(openwrt.Runner{}).ResolveBinding,
			Open:    openPresetFetcher,
			Cache:   presets.NewCache(paths.PresetCacheFile()),
			Sources: append([]string(nil), presets.DefaultSources...), Now: clock.Now,
		}
	}
	runner = presetRoutingRunner{actions: runner, refresh: refresh}
	runner = probeRoutingRunner{actions: runner, daemon: service}
	// Unconditional: a nil wizard is a state this router reports, not a reason
	// to leave the wifi_setup kinds unrouted and let them fall through to the
	// authentication worker's "not implemented" default.
	runner = wifiRoutingRunner{actions: runner, wizard: service.wizard}

	// The maintenance loop is built before the coordinator and submits through
	// a closure, because each needs the other: the loop submits actions, and
	// the coordinator publishes their results back to it. Resolving
	// service.actions at call time rather than at construction is what breaks
	// the knot without an initialisation order nobody can see.
	resumeQuiet, resumeErr := readQuietResume(paths)
	if resumeErr != nil {
		report(resumeErr) // unknown ownership never grants permission to switch
	}
	service.manualPauses, resumeErr = loadManualPauses(paths, repository.Snapshot())
	if resumeErr != nil {
		report(resumeErr)
	}
	maintainer := application.NewMaintainer(application.MaintainerOptions{
		Clock:          clock,
		Settings:       repository,
		ResumeQuiet:    resumeQuiet,
		ManuallyPaused: service.manualPauses.paused,
		Submit: func(ctx context.Context, request application.Request) (
			application.Receipt, error) {
			return service.actions.Submit(ctx, request)
		},
		Line:    service.lineOf,
		OnEvent: service.onMaintenanceEvent,
	})

	service.configChanged = func(revision uint64) {
		service.store.ResetConfiguration(revision)
		pool.Close()
		maintainer.ConfigurationChanged()
		// The threshold follows the setting on the same transaction that
		// changed it, so the first line after a save is already at the level
		// the user just chose.
		events.SetLevel(repository.Snapshot().Log.Level)
		service.log(logstore.EventConfigApplied, "",
			logstore.F("revision", strconv.FormatUint(revision, 10)))
		service.markDirty()
	}
	service.actions = application.New(application.Options{
		Clock:    clock,
		Runner:   manualPauseRunner{actions: runner, daemon: service},
		Lines:    service.lineOf,
		Finalize: service.finishUserAction,
		Admit:    service.admitManualLogout,
		Check: func(request application.Request) error {
			if err := update.Guard(paths.Update()); err != nil {
				return err
			}
			if request.CheckRevision && request.ConfigRevision != repository.Revision() {
				return domain.Errorf(domain.CodeConflict, "配置已变化，请刷新后重试")
			}
			if !request.Kind.Manual() && service.manualPauses.paused(repository.Snapshot(), request.AccountID) {
				return domain.Errorf(domain.CodeBusy, "该账号已手动暂停，可手动登录恢复")
			}
			return service.wizard.check(request)
		},
		Observer: func(action application.Action) {
			if (action.Request.Kind == application.KindQuietHotspot || action.Request.Kind == application.KindQuietCampus) &&
				(action.State == application.StateCancelled || action.State == application.StateInterrupted) {
				report(clearQuietResume(paths))
			}
			service.wizard.observe(action)
			service.onAction(action)
			// The loop learns what happened from the same publication the
			// status projection does, rather than polling for it.
			maintainer.Observe(action)
		},
		// The store decides for itself whether an arriving observation is still
		// current -- it holds the revision, generation and sequence rules -- so
		// the answer is handed over rather than filtered first.
		Record: func(observation observe.Observation) {
			service.store.Accept(observation)
		},
	})

	// The coordinator and the snapshot writer outlive the listener on purpose:
	// an RPC that is still being answered when the stop arrives can still read
	// the coordinator, and the last snapshot is written after both have stopped.
	background, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()

	var loops sync.WaitGroup
	var coordinatorErr, maintainerErr error
	loops.Go(func() { coordinatorErr = service.actions.Run(background) })
	loops.Go(func() { maintainerErr = maintainer.Run(background) })
	loops.Go(func() { service.writeSnapshots(background) })
	loops.Go(func() { service.schedulePresets(background) })
	if options.Runner == nil {
		loops.Go(func() { service.observeWireless(background, openwrt.NewAdapter(openwrt.Runner{})) })
	}
	if service.wizard != nil {
		loops.Go(func() { service.wizard.runExpiry(background) })
	}

	listener, err := control.Listen(paths.Socket())
	if err != nil {
		stopBackground()
		loops.Wait()
		return err
	}

	registry := control.NewRegistry()
	service.register(registry)
	service.markDirty()
	if options.Ready != nil {
		options.Ready()
	}

	serveErr := control.Serve(ctx, listener, registry)

	stopBackground()
	loops.Wait()
	if service.wizard != nil {
		service.wizard.shutdown()
	}

	// The socket is already gone: closing a Unix listener unlinks the path it
	// created, and Serve closes its listener on every return. An explicit
	// removal here looked like belt and braces and was in fact unreachable --
	// a mutation of it changed nothing, which is how it was noticed. The case
	// it appeared to cover, a socket outliving a daemon that was killed, is
	// real; it is handled by the lifecycle helper, which is the only thing
	// running at that point.
	current := repository.Snapshot()
	if err := MarkStopped(paths, current.Enabled, repository.Revision(),
		options.Version); err != nil {
		report(err)
	}
	return errors.Join(serveErr, coordinatorErr, maintainerErr)
}

// wirelessLine is the scheduling key every wireless account shares.
//
// Spec 04 gives wireless a single global transaction: two accounts cannot both
// be re-associating a radio. Wired accounts get one key per interface. The keys
// are prefixed so an interface literally named "wireless" cannot collide with
// this one.
const wirelessLine = "wireless"

// lineOf is the coordinator's line resolver: a cheap lookup in the current
// configuration, never a probe. It runs while a submission waits.
//
// The action's kind is asked first, and the account only afterwards. A hotspot
// switch carries a HotspotID and usually no AccountID at all, so resolving the
// account first sent it to "account:" while a campus wireless switch went to
// "wireless" -- two keys for one radio, which lets the coordinator run both at
// once. Nothing has been observed corrupting a real configuration, because the
// wireless transaction does not exist yet; that is exactly why this has to be
// right before M10 attaches the side effects to it.
//
// The scheduling key is not a substitute for the global wireless transaction
// lock spec 04 requires. It keeps this process from dispatching two wireless
// actions at once; the lock is what protects the radio from everything else.
func (d *Daemon) lineOf(request application.Request) string {
	if request.Kind.WifiSetup() {
		return wirelessLine
	}
	if request.Kind == application.KindPresetsRefresh || request.Kind.Discovery() {
		if request.ProbeMode == "wifi" {
			return wirelessLine
		}
		if request.Interface == d.config.Snapshot().STAIface {
			return wirelessLine
		}
		return "iface:" + request.Interface
	}
	if wirelessKind(request.Kind) {
		return wirelessLine
	}

	cfg := d.config.Snapshot()
	account, known := cfg.CampusAccountByID(request.AccountID)
	switch {
	case !known:
		// An action for an account that is gone still gets a key of its own, so
		// it cannot serialise against an unrelated one on its way to failing.
		return "account:" + request.AccountID
	case account.IsWired():
		// Wired accounts keep one key per interface. Serialising them onto the
		// wireless key as well would throw away the parallelism that lets four
		// lines authenticate at once, and they share no resource with the radio.
		return "iface:" + account.WiredIface
	default:
		return wirelessLine
	}
}

// wirelessKind reports that an action touches the radio whatever account it
// names.
func wirelessKind(kind application.Kind) bool {
	return kind == application.KindSwitchHotspot || kind == application.KindQuietHotspot || kind == application.KindQuietCampus
}

// onMaintenanceEvent is where the maintenance loop's narration goes until M11
// gives it a structured log.
//
// Only the line conflict reaches onError, because it is the one event somebody
// has to act on: two accounts resolving to one line will never both be online,
// and the loop has stopped trying rather than letting them knock each other off
// every interval. Backoffs, pauses and queueings are ordinary progress and would
// be noise on a stderr that procd captures.
func (d *Daemon) onMaintenanceEvent(event application.MaintenanceEvent) {
	d.logMaintenance(event)
	if event.Kind == application.EventPauseChanged {
		reasons := make([]string, 0, 2)
		for _, reason := range event.Pause.Reasons() {
			reasons = append(reasons, string(reason))
		}
		d.pauseMu.Lock()
		d.pause = reasons
		d.pauseMu.Unlock()
		// The status file is what a stopped page reads; a pause that only
		// existed in memory would disappear from it until something else
		// changed.
		d.markDirty()
	}
	if event.Kind == application.EventLineConflict {
		d.onError(domain.Errorf(domain.CodeConflict, "%s", event.Message))
	}
}

// onAction is the coordinator's observer. It runs on the coordinator's
// goroutine, so it does no I/O: it updates the in-memory projection and rings a
// bell for the writer.
func (d *Daemon) onAction(action application.Action) {
	d.logAction(action)
	switch {
	case action.State == application.StateRunning:
		d.store.ActionStarted(action.Request.AccountID, action.ID)
	case action.State.Terminal():
		d.store.ActionFinished(action.Request.AccountID, observe.Note{
			ActionID: action.ID,
			Kind:     string(action.Request.Kind),
			State:    string(action.State),
			Message:  action.Message,
			Code:     action.Code,
			At:       action.EndedAt,
		})
	}
	d.markDirty()
	// After the projection, never before: a watcher told an action had finished
	// and then reading a status that did not say so would be watching a state
	// that briefly disagreed with itself.
	d.observer(action)
}

// markDirty never blocks. The signal is a bell, not a queue: one pending ring
// means "something changed", however many things changed.
func (d *Daemon) markDirty() {
	select {
	case d.dirty <- struct{}{}:
	default:
	}
}

func (d *Daemon) writeSnapshots(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.dirty:
			if err := WriteSnapshot(d.paths, d.Snapshot()); err != nil {
				d.onError(err)
			}
		}
	}
}

// Snapshot is the combined picture, built from memory alone. Nothing here
// touches the network: spec 03 requires a status read to be answerable while
// the line is down, and a poll that probed would turn an open browser tab into
// continuous authentication traffic.
func (d *Daemon) Snapshot() Snapshot {
	ctx, cancel := context.WithTimeout(context.Background(), readBudget)
	defer cancel()
	// Buffered: if the caller times out after dispatch, the loop may still
	// finish the read without blocking or writing into a returned value.
	result := make(chan Snapshot, 1)
	if err := d.actions.Inspect(ctx, func(actions []application.Action) {
		result <- d.snapshotOf(actions)
	}); err == nil {
		return <-result
	}
	// The loop may already have stopped during service shutdown. Report only
	// the persisted settings; no mixed account/action state is presented.
	snapshot := d.snapshotOf(nil)
	snapshot.Accounts = nil
	return snapshot
}

func (d *Daemon) snapshotOf(actions []application.Action) Snapshot {
	cfg := d.config.Snapshot()
	projection := d.store.Read()
	if projection.Revision != cfg.Revision {
		// A save between the two reads invalidates the old observations.
		projection.Accounts = nil
	}

	d.pauseMu.Lock()
	pause := append([]string(nil), d.pause...)
	d.pauseMu.Unlock()

	snapshot := Snapshot{
		SchemaVersion:        SnapshotSchemaVersion,
		WrittenAt:            d.clock.Now().UTC(),
		Service:              ServiceRunning,
		PID:                  os.Getpid(),
		Enabled:              cfg.Enabled,
		Pause:                pause,
		ConfigRevision:       cfg.Revision,
		Version:              d.version,
		Accounts:             projection.Accounts,
		Wireless:             d.wirelessState.read(cfg.Revision),
		ManualPausedAccounts: d.manualPauses.accounts(cfg),
	}

	snapshot.Actions = make([]ActionView, 0, len(actions))
	for _, action := range actions {
		snapshot.Actions = append(snapshot.Actions, ViewOf(action))
	}
	return snapshot
}

// UnavailableRunner is gone. It existed because M08 had the service, the queue
// and the action state machine but no authentication worker behind them, so an
// action queued, ran, and failed with UnsupportedCapability -- which was the
// honest answer at the time. M09 supplies the worker, newDeviceRunner builds
// it, and nothing referred to the placeholder any more.
