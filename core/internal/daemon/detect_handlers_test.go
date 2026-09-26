//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
)

func submitProbe(t *testing.T, service *running, target, key string) SubmitResult {
	t.Helper()
	var receipt SubmitResult
	data := service.call("detect.acid", DetectACIDParams{BaseURL: target, Iface: "wan", AccessMode: "wired", IdempotencyKey: key, Session: "test-session"})
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestACIDJobReturnsPromptlyAndPreservesDraftPath(t *testing.T) {
	entered, unblock := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.RequestURI() != "/a/login?theme=pro" || r.Method != "GET" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected probe: %s %s", r.Method, r.URL.RequestURI())
		}
		close(entered)
		select {
		case <-unblock:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, `<input name="ac_id" value="008">`)
	}))
	defer server.Close()
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	service := start(t, func(o *Options) {
		o.OpenProbe = func(ctx context.Context, iface string) (portal.Fetcher, func(), error) {
			if iface != "wan" {
				t.Error("wrong line")
			}
			return server.Client(), func() {}, nil
		}
	})
	receipt := submitProbe(t, service, server.URL+"/a/login?theme=pro", "probe-1")
	select {
	case <-entered:
	case <-time.After(patience):
		t.Fatal("probe not dispatched")
	}
	// Both the task receipt and status arrive while the actual page is blocked.
	if service.status().Service != ServiceRunning {
		t.Fatal("status blocked or unavailable")
	}
	retry := submitProbe(t, service, server.URL+"/a/login?theme=pro", "probe-1")
	if !retry.Duplicate || retry.ActionID != receipt.ActionID {
		t.Fatal("duplicate caused another job")
	}
	wrongSession := service.callExpectingError("action.get", ActionParams{ActionID: receipt.ActionID, Session: "another-session"})
	if codeOf(t, wrongSession) != domain.CodeNotFound {
		t.Fatal(wrongSession)
	}
	wrongCancel := service.callExpectingError("action.cancel", ActionParams{ActionID: receipt.ActionID, Session: "another-session"})
	if codeOf(t, wrongCancel) != domain.CodeNotFound {
		t.Fatal(wrongCancel)
	}
	release()
	action := service.awaitTerminal(receipt.ActionID)
	if action.State != application.StateSucceeded {
		t.Fatalf("state = %s / %s", action.State, action.Message)
	}
	var got struct {
		Result DetectACIDResult `json:"result"`
	}
	raw := service.call("action.get", ActionParams{ActionID: receipt.ActionID, Session: "test-session"})
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Result.OK || got.Result.ACID != "008" || got.Result.BaseURL != server.URL {
		t.Fatalf("finding = %+v", got.Result)
	}
	if calls.Load() != 1 {
		t.Fatal("probe repeated")
	}
	if strings.Contains(string(service.call("status.get", nil)), "/a/login") {
		t.Fatal("periodic snapshot included draft task result")
	}
	if _, err := os.Stat(service.paths.ConfigFile()); !os.IsNotExist(err) {
		t.Fatal("read-only discovery wrote config")
	}
}

func TestACIDJobCancellationDoesNotPublishLateResult(t *testing.T) {
	entered := make(chan struct{})
	service := start(t, func(o *Options) {
		o.OpenProbe = func(ctx context.Context, iface string) (portal.Fetcher, func(), error) {
			return probeFunc(func(req *http.Request) (*http.Response, error) {
				close(entered)
				<-req.Context().Done()
				return nil, req.Context().Err()
			}), func() {}, nil
		}
	})
	receipt := submitProbe(t, service, "http://portal.invalid/login", "cancel-me")
	select {
	case <-entered:
	case <-time.After(patience):
		t.Fatal("probe not dispatched")
	}
	service.call("action.cancel", ActionParams{ActionID: receipt.ActionID, Session: "test-session"})
	action := service.awaitTerminal(receipt.ActionID)
	if action.State != application.StateCancelled || action.ResultJSON != "" {
		t.Fatal("cancelled job acquired a result")
	}
	var view ActionView
	if err := json.Unmarshal(service.call("action.get", ActionParams{ActionID: receipt.ActionID}), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != "cancelled" {
		t.Fatal(view.State)
	}
}

type probeFunc func(*http.Request) (*http.Response, error)

func (f probeFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestACIDRejectsMissingLineAndCredentialsBeforeOpeningClient(t *testing.T) {
	var opened atomic.Int32
	service := start(t, func(o *Options) {
		o.OpenProbe = func(context.Context, string) (portal.Fetcher, func(), error) {
			opened.Add(1)
			return nil, nil, domain.Errorf(domain.CodeInternal, "unexpected client")
		}
	})
	for _, params := range []DetectACIDParams{
		{BaseURL: "http://portal.invalid", AccessMode: "wired", IdempotencyKey: "missing"},
		{BaseURL: "http://student:private-password@portal.invalid", AccessMode: "wired", Iface: "wan", IdempotencyKey: "userinfo"},
		{BaseURL: "http://portal.invalid/?password=private-password", AccessMode: "wired", Iface: "wan", IdempotencyKey: "query"},
		{BaseURL: "http://portal.invalid", AccessMode: "wifi", Iface: "wwan", IdempotencyKey: "ssid"},
	} {
		err := service.callExpectingError("detect.acid", params)
		if strings.Contains(err.Error(), "private-password") {
			t.Fatal("error leaked credentials")
		}
	}
	if opened.Load() != 0 {
		t.Fatal("invalid input reached a client")
	}
}

func TestProbeFetcherRefusesChangedAddressAndWrongSSID(t *testing.T) {
	binding := domain.Binding{LogicalIface: "wwan", L3Device: "wlan0", IfIndex: 2, SourceIPv4: netip.MustParseAddr("192.0.2.3")}
	current := binding
	calls := 0
	f := probeFetcher{binding: binding, ssid: "campus",
		resolve: func(context.Context, string, uint64) (domain.Binding, error) { return current, nil },
		info: func(context.Context, string) (openwrt.RadioInfo, error) {
			return openwrt.RadioInfo{Mode: "Client", SSID: "hotspot", BSSID: "02:11:22:33:44:55"}, nil
		},
		do: func(*http.Request) (*http.Response, error) { calls++; return nil, nil },
	}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://portal.invalid", nil)
	if _, err := f.Do(req); err == nil {
		t.Fatal("wrong SSID was accepted")
	}
	f.ssid = ""
	current.SourceIPv4 = netip.MustParseAddr("192.0.2.4")
	if _, err := f.Do(req); err == nil {
		t.Fatal("changed address was accepted")
	}
	if calls != 0 {
		t.Fatal("invalid binding sent traffic")
	}
}
