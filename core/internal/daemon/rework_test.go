//go:build unix

package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

// R08 -- everything that touches the radio serialises on one key.
//
// A hotspot switch carries a HotspotID and usually no AccountID, so resolving
// the account first sent it to "account:" while a campus wireless switch went
// to "wireless": two keys for one radio, which lets the coordinator dispatch
// both at once. Nothing has been seen corrupting a real configuration because
// the wireless transaction does not exist yet -- which is exactly why this has
// to be right before M10 attaches the side effects.
//
// The scheduling key is not a substitute for the global wireless transaction
// lock spec 04 requires. It keeps this process from dispatching two wireless
// actions at once; the lock protects the radio from everything else.
func TestEverythingThatTouchesTheRadioSharesOneSchedulingKey(t *testing.T) {
	service := &Daemon{config: wirelessRepository(t)}

	campus := service.lineOf(application.Request{
		Kind: application.KindSwitchCampus, AccountID: "wifi"})
	hotspot := service.lineOf(application.Request{
		Kind: application.KindSwitchHotspot, HotspotID: "h1"})

	if campus != hotspot {
		t.Fatalf("campus wireless = %q, hotspot = %q; one radio, two keys",
			campus, hotspot)
	}
	if campus != wirelessLine {
		t.Errorf("key = %q, want the wireless line", campus)
	}

	// A hotspot switch that does name an account still lands on the radio, so
	// the key cannot be talked out of it by filling in a field.
	withAccount := service.lineOf(application.Request{
		Kind: application.KindSwitchHotspot, HotspotID: "h1", AccountID: "wired"})
	if withAccount != wirelessLine {
		t.Errorf("key = %q for a hotspot switch naming a wired account, want the wireless line",
			withAccount)
	}
}

// And wired accounts keep their own keys, because they share no resource with
// the radio and serialising them onto it would throw away the parallelism that
// lets four lines authenticate at once.
func TestWiredAccountsAreNotSerialisedOntoTheRadio(t *testing.T) {
	service := &Daemon{config: wirelessRepository(t)}

	wired := service.lineOf(application.Request{
		Kind: application.KindLogin, AccountID: "wired"})
	if wired == wirelessLine {
		t.Fatalf("a wired login took the wireless key")
	}
	if wired != "iface:wan" {
		t.Errorf("key = %q, want the interface it authenticates through", wired)
	}

	// Two wired accounts on different interfaces stay independent.
	other := service.lineOf(application.Request{
		Kind: application.KindLogin, AccountID: "wired2"})
	if other == wired {
		t.Errorf("two interfaces shared the key %q", wired)
	}
}

// maintainRunner answers every action at once and says what it was asked.
type maintainRunner struct{ seen chan application.Action }

func (r maintainRunner) Run(_ context.Context, action application.Action,
	_ func(application.Phase)) application.Outcome {

	select {
	case r.seen <- action:
	default:
	}
	return application.Outcome{State: application.StateSucceeded,
		Message: "认证完成"}
}

// M09.1 -- the assembled service authenticates on its own, and learns how it
// went.
//
// Two attempts rather than one, and the second is the point. The first proves
// the maintenance loop is wired to the coordinator at all; only the second
// proves the results come back, because an account whose result never arrives
// stays marked in-flight forever and is never queued again. Removing that one
// line in Run leaves the first attempt working and the service silently stuck
// after it -- which is exactly the failure a test that stopped at one would
// miss.
func TestTheServiceAuthenticatesOnItsOwnAndLearnsHowItWent(t *testing.T) {
	paths := tempPaths(t)
	repository, err := config.Open(paths.ConfigFile())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err := repository.Update(repository.Revision(),
		func(cfg *domain.Config) error {
			cfg.Enabled = true
			// This fixture observes authentication maintenance, not preset fetches.
			cfg.PresetUpdates.Enabled = false
			cfg.Checks.IntervalSeconds = 60
			cfg.Selection.ActiveCampusID = "c1"
			cfg.CampusAccounts = []domain.CampusAccount{{
				ID: "c1", Label: "校园网", UserID: "a", Password: "p",
				AccessMode: domain.AccessModeWired, WiredIface: "wan",
				BaseURL: "http://192.0.2.1", ACID: "1",
			}}
			return nil
		}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	clock := faketime.New(time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC))
	runner := maintainRunner{seen: make(chan application.Action, 8)}

	ready := make(chan struct{})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Paths:   paths,
			Version: "2.0.0rc1",
			Clock:   clock,
			Runner:  runner,
			Ready:   sync.OnceFunc(func() { close(ready) }),
			OnError: func(error) {},
		})
	}()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(patience):
			t.Error("the daemon did not stop")
		}
	})

	select {
	case <-ready:
	case <-time.After(patience):
		t.Fatal("the daemon never became ready")
	}

	first := awaitRun(t, runner.seen, "the service never authenticated on its own")
	if first.Request.Kind != application.KindMaintain {
		t.Fatalf("first action = %s, want maintain", first.Request.Kind)
	}

	// Push the clock along until the next check falls due. The result of the
	// first attempt has to have reached the loop for this to happen at all.
	deadline := time.Now().Add(patience)
	for {
		select {
		case second := <-runner.seen:
			if second.Request.Kind != application.KindMaintain {
				t.Errorf("second action = %s, want maintain", second.Request.Kind)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no second check: the loop was never told how the first attempt ended")
		}
		clock.Advance(30 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
}

func awaitRun(t *testing.T, seen chan application.Action, why string) application.Action {
	t.Helper()
	select {
	case action := <-seen:
		return action
	case <-time.After(patience):
		t.Fatal(why)
		return application.Action{}
	}
}

// wirelessRepository is a repository holding one wireless account, two wired
// ones and a hotspot.
func wirelessRepository(t *testing.T) *config.Repository {
	t.Helper()
	paths := tempPaths(t)
	repository, err := config.Open(paths.ConfigFile())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err := repository.Update(repository.Revision(), func(cfg *domain.Config) error {
		cfg.STAIface = "wwan"
		cfg.CampusAccounts = []domain.CampusAccount{
			{
				ID: "wifi", Label: "无线", UserID: "a", Password: "p",
				AccessMode: domain.AccessModeWiFi, SSID: "campus",
				Encryption: "none", APSelection: domain.APSelectionAuto,
				BaseURL: "http://192.0.2.1", ACID: "1",
			},
			{
				ID: "wired", Label: "有线", UserID: "b", Password: "p",
				AccessMode: domain.AccessModeWired, WiredIface: "wan",
				BaseURL: "http://192.0.2.1", ACID: "1",
			},
			{
				ID: "wired2", Label: "有线二", UserID: "c", Password: "p",
				AccessMode: domain.AccessModeWired, WiredIface: "wan2",
				BaseURL: "http://192.0.2.1", ACID: "1",
			},
		}
		cfg.HotspotProfiles = []domain.HotspotProfile{
			{ID: "h1", Label: "手机热点", SSID: "phone", Encryption: "psk2",
				Key: "letmein1", Radio: "radio0"},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return repository
}
