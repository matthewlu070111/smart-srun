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

func TestWifiCLIWaitsForReadyWithoutPrintingKey(t *testing.T) {
	job := ""
	client := onlineClient{ensure: func(context.Context) error { return nil }, call: func(_ context.Context, method string, p any) (json.RawMessage, error) {
		state := "ready"
		if method == "setup_wifi.start" {
			params := p.(daemon.WifiSetupParams)
			job = params.Job
			if len(job) != 32 || params.Key != " wifi private key " {
				t.Fatal("input altered")
			}
			state = "starting"
		} else if method != "setup_wifi.status" {
			t.Fatal(method)
		}
		body, _ := json.Marshal(daemon.WifiSetupView{OK: true, Job: job, State: state})
		return body, nil
	}}
	code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(t.Context(), client, []string{"detect", "wifi", "start", "--stdin", "--json"}, strings.NewReader(`{"ssid":"Campus","encryption":"psk2","key":" wifi private key "}`), stdout, stderr)
	})
	if code != ExitOK || !json.Valid([]byte(out)) || !strings.Contains(out, "ready") || strings.Contains(out+errOut, "wifi private key") {
		t.Fatal(code, out, errOut)
	}
}

func TestWifiCLIInterruptCancelsOnlyItsOwnJobWithFreshContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	job := ""
	cancelled := false
	client := onlineClient{ensure: func(context.Context) error { return nil }, call: func(ctx context.Context, method string, p any) (json.RawMessage, error) {
		if method == "setup_wifi.start" {
			job = p.(daemon.WifiSetupParams).Job
			body, _ := json.Marshal(daemon.WifiSetupView{OK: true, Job: job, State: "starting"})
			return body, nil
		}
		if method == "setup_wifi.status" {
			cancel()
			return nil, ctx.Err()
		}
		if method != "setup_wifi.cancel" || ctx.Err() != nil {
			t.Fatal("incorrect cancellation")
		}
		body, _ := json.Marshal(p)
		if !strings.Contains(string(body), job) {
			t.Fatal("cancelled another job")
		}
		cancelled = true
		return json.RawMessage(`{}`), nil
	}}
	code, _, _ := capture(t, func(stdout, stderr *os.File) int {
		return runOnline(ctx, client, []string{"detect", "wifi", "--stdin"}, strings.NewReader(`{"ssid":"Campus","encryption":"none"}`), stdout, stderr)
	})
	if code != ExitCancelled || !cancelled {
		t.Fatal(code, cancelled)
	}
}
