//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

type cliRunner struct{ requests chan application.Request }

func (r cliRunner) Run(_ context.Context, action application.Action, report func(application.Phase)) application.Outcome {
	r.requests <- action.Request
	report(application.PhaseLogin)
	if action.Request.Kind == application.KindRelogin {
		return application.Outcome{State: application.StateFailed, Code: domain.CodeAuthRejected, Message: "测试拒绝"}
	}
	return application.Outcome{State: application.StateSucceeded, Message: "测试完成"}
}

func onlineDaemon(t *testing.T) (onlineClient, <-chan application.Request) {
	t.Helper()
	paths := daemon.Paths{Runtime: filepath.Join(t.TempDir(), "run"), Config: filepath.Join(t.TempDir(), "etc")}
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Quiet.Enabled = false
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ready, done := make(chan struct{}), make(chan error, 1)
	requests := make(chan application.Request, 16)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		done <- daemon.Run(ctx, daemon.Options{
			Paths: paths, Runner: cliRunner{requests},
			// 08:00 Beijing is before daily refresh, including after a backup
			// import replaces the fixture's configuration with preset defaults.
			Clock:   faketime.New(time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)),
			Ready:   func() { close(ready) },
			OnError: func(err error) { t.Error(err) },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon not ready")
	}
	return onlineClient{call: (control.Client{Path: paths.Socket()}).Call, ensure: func(context.Context) error { return nil }}, requests
}

func TestCLIUsesRealRPCForAccountSaveLoginLogoutAndSettings(t *testing.T) {
	client, requests := onlineDaemon(t)
	run := func(args []string, input string, want int) string {
		t.Helper()
		code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
			return runOnline(t.Context(), client, args, strings.NewReader(input), stdout, stderr)
		})
		if code != want {
			t.Fatalf("%v: exit=%d want=%d stderr=%s", args, code, want, errOut)
		}
		if !json.Valid([]byte(out)) || strings.Contains(out+errOut, "synthetic-private-secret") {
			t.Fatalf("%v: stdout was not a single redacted JSON value", args)
		}
		return out
	}
	run([]string{"config", "account", "add"}, `{"expected_revision":0,"account":{"user_id":"student","password":"synthetic-private-secret","wired_iface":"wan"}}`, ExitOK)
	run([]string{"config", "account", "edit"}, `{"expected_revision":1,"account":{"id":"c1","login":{"double_stack":false}}}`, ExitOK)
	run([]string{"config", "account", "edit"}, `{"expected_revision":2,"account":{"id":"c1","login":{"double_stack":null}}}`, ExitOK)
	run([]string{"config", "show"}, "", ExitOK)
	for _, verb := range []string{"login", "logout", "relogin"} {
		want := ExitOK
		if verb == "relogin" {
			want = ExitActionFailed
		}
		run([]string{verb, "--json"}, "", want)
		select {
		case request := <-requests:
			if request.AccountID != "c1" || !request.CheckRevision || request.ConfigRevision != 3 {
				t.Fatalf("bad action request: %+v", request)
			}
		case <-time.After(time.Second):
			t.Fatal("CLI did not execute the action")
		}
	}
	run([]string{"config", "set"}, `{"expected_revision":3,"settings":{"quiet":{"start":"01:30"}}}`, ExitOK)
	if got := strings.TrimSpace(run([]string{"config", "get", "quiet.start"}, "", ExitOK)); got != `"01:30"` {
		t.Fatalf("quiet.start = %s", got)
	}
	run([]string{"disable"}, "", ExitOK)
	run([]string{"config", "account", "rm"}, `{"expected_revision":5,"id":"c1"}`, ExitOK)
	status, err := client.call(t.Context(), "status.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot daemon.Snapshot
	if err := json.Unmarshal(status, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Accounts) != 0 {
		t.Fatal("deleted account retained its previous session status")
	}
	// Enabling an empty configuration persists the switch without authenticating.
	run([]string{"enable"}, "", ExitOK)
	if got := strings.TrimSpace(run([]string{"config", "get", "enabled"}, "", ExitOK)); got != "true" {
		t.Fatalf("enabled = %s", got)
	}
}

func TestCLIBadInputAndReadsNeverStartTheService(t *testing.T) {
	starts := 0
	client := onlineClient{
		ensure: func(context.Context) error { starts++; return nil },
		call: func(context.Context, string, any) (json.RawMessage, error) {
			return nil, domain.Errorf(domain.CodeServiceStopped, "stopped")
		},
	}
	for _, tc := range []struct {
		args  []string
		input string
		want  int
	}{
		{[]string{"config", "show"}, "", ExitServiceStopped},
		{[]string{"config", "account", "list"}, "", ExitServiceStopped},
		{[]string{"config", "set"}, `{"settings":{"enabled":true}}`, ExitInvalidInput},
		{[]string{"config", "set"}, `{"expected_revision":0,"settings":{"enabled":true,"Enabled":false}}`, ExitInvalidInput},
		{[]string{"config", "account", "add"}, `{"expected_revision":0,"account":{"id":"c1"}}`, ExitInvalidInput},
		{[]string{"config", "account", "edit"}, `{"expected_revision":0,"account":{"user_id":"student"}}`, ExitInvalidInput},
		{[]string{"login", "--password", "secret"}, "", ExitInvalidInput},
		{[]string{"switch"}, "", ExitInvalidInput},
		{[]string{"switch", "unknown"}, "", ExitInvalidInput},
		{[]string{"switch", "hotspot", "h1", "h2"}, "", ExitInvalidInput},
	} {
		code, _, _ := capture(t, func(out, errOut *os.File) int {
			return runOnline(t.Context(), client, tc.args, strings.NewReader(tc.input), out, errOut)
		})
		if code != tc.want {
			t.Errorf("%v: exit=%d want=%d", tc.args, code, tc.want)
		}
	}
	if starts != 0 {
		t.Fatalf("reads or malformed writes started service %d times", starts)
	}
}

func TestCLISwitchUsesSelectedFamilyAndPersistsOnlyActiveChoice(t *testing.T) {
	client, requests := onlineDaemon(t)
	inputs := []struct{ method, raw string }{
		{"campus.upsert", `{"expected_revision":0,"account":{"user_id":"first","wired_iface":"wan"}}`},
		{"campus.upsert", `{"expected_revision":1,"account":{"user_id":"second","wired_iface":"wan"}}`},
		{"hotspot.upsert", `{"expected_revision":2,"profile":{"ssid":"phone","radio":"radio0","encryption":"none"}}`},
	}
	for _, input := range inputs {
		if _, err := client.call(t.Context(), input.method, json.RawMessage(input.raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args             []string
		kind             application.Kind
		account, hotspot string
	}{
		{[]string{"switch", "campus", "c2", "--json"}, application.KindSwitchCampus, "c2", ""},
		{[]string{"switch", "hotspot", "--json"}, application.KindSwitchHotspot, "", "h1"},
		{[]string{"switch", "campus", "--json"}, application.KindSwitchCampus, "c1", ""},
	} {
		code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
			return runOnline(t.Context(), client, tc.args, strings.NewReader(""), stdout, stderr)
		})
		if code != ExitOK || !json.Valid([]byte(out)) {
			t.Fatalf("%v: %d %s", tc.args, code, errOut)
		}
		request := <-requests
		if request.Kind != tc.kind || request.AccountID != tc.account || request.HotspotID != tc.hotspot {
			t.Fatalf("wrong switch: %+v", request)
		}
		cfg, err := readOnlineConfig(t.Context(), client)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Selection.DefaultCampusID != "c1" || (tc.account != "" && cfg.Selection.ActiveCampusID != tc.account) {
			t.Fatal("incorrect persisted selection")
		}
	}
}

func TestCLICancellationCancelsOnlyItsAction(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := ""
	client := onlineClient{call: func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		if method == "action.get" {
			cancel()
			return nil, ctx.Err()
		}
		if method != "action.cancel" || ctx.Err() != nil {
			t.Errorf("unexpected cancellation call: %s / %v", method, ctx.Err())
		}
		cancelled = params.(daemon.ActionParams).ActionID
		return json.RawMessage(`{}`), nil
	}}
	code, _, _ := capture(t, func(_, stderr *os.File) int {
		_, err := waitAction(ctx, client, "action-7", stderr)
		return ExitCodeFor(err)
	})
	if code != ExitCancelled || cancelled != "action-7" {
		t.Fatalf("exit=%d cancelled=%q", code, cancelled)
	}
}
