//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
)

func TestDetectCLIWaitsForResultAndSeparatesJSONFromProgress(t *testing.T) {
	for _, found := range []bool{true, false} {
		calls := []string{}
		client := onlineClient{ensure: func(context.Context) error { return nil }, call: func(_ context.Context, method string, p any) (json.RawMessage, error) {
			calls = append(calls, method)
			if method == "detect.verify" {
				params := p.(daemon.VerificationParams)
				if params.Password != "synthetic-private-secret" || params.Candidates[0] != "" || params.IdempotencyKey == "" {
					t.Fatal("draft altered")
				}
				return json.RawMessage(`{"action_id":"a1","state":"queued"}`), nil
			}
			if method != "action.get" {
				t.Fatal(method)
			}
			value, _ := json.Marshal(map[string]any{"id": "a1", "state": "succeeded", "result": map[string]any{"ok": found, "suffix": ""}})
			return value, nil
		}}
		code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
			return runOnline(t.Context(), client, []string{"detect", "verify", "--stdin", "--json"}, strings.NewReader(`{"access_mode":"wired","iface":"wan","base_url":"http://portal.invalid","user_id":"student","password":"synthetic-private-secret","candidates":[""]}`), stdout, stderr)
		})
		want := ExitActionFailed
		if found {
			want = ExitOK
		}
		if code != want || !json.Valid([]byte(out)) || !strings.Contains(out, `"result"`) || strings.Contains(out+errOut, "synthetic-private-secret") || strings.Join(calls, ",") != "detect.verify,action.get" {
			t.Fatalf("code=%d out=%s err=%s calls=%v", code, out, errOut, calls)
		}
	}
}

func TestDetectCLICancellationOnlyCancelsItsSubmittedTask(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := []string{}
	client := onlineClient{ensure: func(context.Context) error { return nil }, call: func(ctx context.Context, method string, p any) (json.RawMessage, error) {
		calls = append(calls, method)
		switch method {
		case "detect.acid":
			return json.RawMessage(`{"action_id":"a2","state":"queued"}`), nil
		case "action.get":
			cancel()
			return nil, ctx.Err()
		case "action.cancel":
			if ctx.Err() != nil || p.(daemon.ActionParams).ActionID != "a2" {
				t.Fatal("wrong cancellation")
			}
			return json.RawMessage(`{"id":"a2","state":"cancelled"}`), nil
		default:
			t.Fatal(method)
			return nil, nil
		}
	}}
	code, _, _ := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(ctx, client, []string{"detect", "acid", "--iface", "wan", "--access-mode", "wired", "--base-url", "http://portal.invalid", "--json"}, strings.NewReader(""), stdout, stderr)
	})
	if code != ExitCancelled || strings.Join(calls, ",") != "detect.acid,action.get,action.cancel" {
		t.Fatal(code, calls)
	}
}

func TestDiscoveryInvalidCLIInputsDoNotStartTheService(t *testing.T) {
	client := onlineClient{ensure: func(context.Context) error { t.Fatal("invalid input started daemon"); return nil }}
	for _, args := range [][]string{
		{"detect", "verify", "--password", "synthetic-private-secret"},
		{"detect", "acid", "--base-url", "http://portal.invalid"},
		{"detect", "identity", "--stdin", "--user-id", "student"},
		{"presets", "refresh"},
		{"log", "follow", "--json"},
	} {
		code, _, errOut := capture(t, func(stdout, stderr *os.File) int {
			return runOnline(t.Context(), client, args, strings.NewReader("{}"), stdout, stderr)
		})
		if code != ExitInvalidInput || strings.Contains(errOut, "synthetic-private-secret") {
			t.Fatal(args, code, errOut)
		}
	}
}

func TestPresetCLICollectsEveryPageWithoutStartingService(t *testing.T) {
	client := onlineClient{ensure: func(context.Context) error { t.Fatal("read started service"); return nil }, call: func(_ context.Context, method string, p any) (json.RawMessage, error) {
		if method != "presets.list" {
			t.Fatal(method)
		}
		params := p.(daemon.PresetListParams)
		if params.Offset == 0 {
			return json.RawMessage(`{"public":[{"short_name":"first","name":"学校一"}],"user":[],"revision":7,"total":2,"next_offset":1}`), nil
		}
		if params.Offset != 1 {
			t.Fatal(params)
		}
		return json.RawMessage(`{"public":[],"user":[{"short_name":"last","name":"学校二"}],"revision":7,"total":2}`), nil
	}}
	code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(t.Context(), client, []string{"presets", "list", "--json"}, nil, stdout, stderr)
	})
	if code != ExitOK || !json.Valid([]byte(out)) || !strings.Contains(out, "last") || !strings.Contains(out, "first") {
		t.Fatal(code, out, errOut)
	}
}

func TestLogCLIUsesCursorAndCancellationDoesNotMutateService(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	client := onlineClient{ensure: func(context.Context) error { t.Fatal("log started service"); return nil }, call: func(_ context.Context, method string, p any) (json.RawMessage, error) {
		calls++
		if method != "log.tail" {
			t.Fatal("log performed a write", method)
		}
		params := p.(daemon.LogTailParams)
		if calls == 1 {
			if params.Cursor != 0 {
				t.Fatal(params)
			}
			return json.RawMessage(`{"channel":"network","lines":["first"],"cursor":7}`), nil
		}
		if params.Cursor != 7 {
			t.Fatal("cursor not advanced", params)
		}
		cancel()
		return nil, ctx.Err()
	}}
	code, out, _ := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(ctx, client, []string{"log", "follow", "-n", "2", "--channel", "network"}, nil, stdout, stderr)
	})
	if code != ExitCancelled || out != "first\n" || calls != 2 {
		t.Fatal(code, out, calls)
	}
}

func TestRuntimeDiagnosticsExcludeCredentialsOverRealRPC(t *testing.T) {
	client, _ := onlineDaemon(t)
	code, _, _ := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(t.Context(), client, []string{"config", "account", "add"}, strings.NewReader(`{"expected_revision":0,"account":{"user_id":"private-account","password":"synthetic-private-secret","wired_iface":"wan"}}`), stdout, stderr)
	})
	if code != ExitOK {
		t.Fatal(code)
	}
	code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(t.Context(), client, []string{"log", "runtime", "--json"}, nil, stdout, stderr)
	})
	if code != ExitOK || !json.Valid([]byte(out)) || strings.Contains(out+errOut, "synthetic-private-secret") || strings.Contains(out, "private-account") || !strings.Contains(out, `"account_id": "c1"`) {
		t.Fatal(code, out, errOut)
	}
}
