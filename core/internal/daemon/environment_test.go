//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

func TestEnvironmentJobWithoutAddressIsAsyncSessionBoundAndReadOnly(t *testing.T) {
	entered, proceed := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(proceed) })
	defer release()
	service := start(t, func(o *Options) {
		o.ProbeGateways = func(_ context.Context, iface string) ([]string, error) {
			if iface != "wan" {
				t.Error("wrong gateway interface")
			}
			return []string{"http://gateway.invalid/full/login?theme=pro"}, nil
		}
		o.OpenProbe = func(_ context.Context, iface string) (portal.Fetcher, func(), error) {
			if iface != "wan" {
				t.Error("wrong client interface")
			}
			return probeFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("credentials sent")
				}
				if r.URL.Host == "gateway.invalid" {
					if r.URL.RequestURI() != "/full/login?theme=pro" {
						t.Error("lost full path")
					}
					close(entered)
					select {
					case <-proceed:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`<input name="ac_id" value="007">`))}, nil
				}
				return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}), func() {}, nil
		}
	})
	params := DetectACIDParams{Iface: "wan", AccessMode: "wired", IdempotencyKey: "environment", Session: "owner"}
	var receipt SubmitResult
	if err := json.Unmarshal(service.call("detect.environment", params), &receipt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(patience):
		t.Fatal("worker did not start")
	}
	if service.status().Service != ServiceRunning {
		t.Fatal("status stalled")
	}
	var retry SubmitResult
	json.Unmarshal(service.call("detect.environment", params), &retry)
	if !retry.Duplicate || retry.ActionID != receipt.ActionID {
		t.Fatal("duplicate not deduplicated")
	}
	if codeOf(t, service.callExpectingError("action.get", ActionParams{ActionID: receipt.ActionID, Session: "other"})) != domain.CodeNotFound {
		t.Fatal("session leak")
	}
	release()
	if action := service.awaitTerminal(receipt.ActionID); action.State != application.StateSucceeded {
		t.Fatalf("%+v", action)
	}
	var result struct {
		Result DetectEnvironmentResult `json:"result"`
	}
	json.Unmarshal(service.call("action.get", ActionParams{ActionID: receipt.ActionID, Session: "owner"}), &result)
	if !result.Result.OK || result.Result.State != "online" || result.Result.ACID != "007" || result.Result.AddressSource != "所选出口的网关" {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(service.paths.ConfigFile()); !os.IsNotExist(err) {
		t.Fatal("discovery wrote config")
	}
	params.Iface, params.IdempotencyKey = "", "missing-line"
	if codeOf(t, service.callExpectingError("detect.environment", params)) != domain.CodeInvalidArgument {
		t.Fatal("missing line accepted")
	}
}

func TestEnvironmentCandidatesExcludeOtherLinesAndCredentials(t *testing.T) {
	repo, err := config.Open(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	// Use the public repository mutation path to keep this fixture realistic.
	d := &Daemon{config: repo, users: presets.NewUserStore(t.TempDir() + "/users.json"),
		publicPresets: func() ([]presets.School, error) {
			return []presets.School{{ShortName: "chosen", Defaults: presets.Defaults{BaseURL: "http://school.invalid/full"}}}, nil
		},
		probeGateways: func(_ context.Context, iface string) ([]string, error) {
			return []string{"http://gateway.invalid"}, nil
		},
	}
	// Stored account passwords never enter a candidate; only matching lines do.
	service := start(t, nil)
	for i, a := range []string{
		`{"user_id":"other","password":"private-password","access_mode":"wired","wired_iface":"wan2","base_url":"http://other.invalid"}`,
		`{"user_id":"student","password":"private-password","access_mode":"wired","wired_iface":"wan","base_url":"http://saved.invalid"}`,
		`{"user_id":"wifi","access_mode":"wifi","ssid":"campus","base_url":"http://wifi.invalid"}`,
	} {
		service.writeConfig("campus.upsert", fmt.Sprintf(`{"expected_revision":%d,"account":%s}`, i, a))
	}
	d.config, err = config.Open(service.paths.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	candidates := d.environmentCandidates(t.Context(), application.Request{Interface: "wan", ProbeMode: "wired", ProbeURL: "http://typed.invalid/login?theme=pro", ProbeSchool: "chosen"})
	want := []string{"http://typed.invalid/login?theme=pro", "http://school.invalid/full", "http://saved.invalid", "http://gateway.invalid"}
	if len(candidates) != len(want) {
		t.Fatal(candidates)
	}
	for i, c := range candidates {
		if c.URL != want[i] {
			t.Fatalf("%+v", candidates)
		}
	}
}
