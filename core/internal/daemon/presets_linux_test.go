//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

func loopbackPresetBinding(_ context.Context, iface string, gen uint64) (domain.Binding, error) {
	device := "lo"
	// WSL mirrored networking routes TCP loopback through loopback0, not lo.
	// This still uses the real SO_BINDTODEVICE; no unbound retry is allowed.
	if _, err := net.InterfaceByName("loopback0"); err == nil {
		device = "loopback0"
	}
	return domain.Binding{LogicalIface: iface, Generation: gen, L3Device: device, SourceIPv4: netip.MustParseAddr("127.0.0.1")}, nil
}

func submitPresetRefresh(t *testing.T, s *running, key string) SubmitResult {
	t.Helper()
	var receipt SubmitResult
	if err := json.Unmarshal(s.call("presets.refresh", PresetRefreshParams{Interface: "wan", IdempotencyKey: key}), &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestPresetRPCRefreshUsesBoundHTTPFallbackAndKeepsConfig(t *testing.T) {
	for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(env, "http://127.0.0.1:1")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") || r.Header.Get("Authorization") != "" {
			t.Error("unbound or credentialed request")
		}
		switch r.URL.Path {
		case "/http":
			w.WriteHeader(503)
		case "/html":
			w.Write([]byte("<html>campus login</html>"))
		case "/large":
			w.Write([]byte(strings.Repeat("x", int(presets.MaxPayloadBytes)+1)))
		default:
			w.Write([]byte(`{"schema_version":1,"schools":[{"short_name":"remote","name":"Remote","status":"active"}]}`))
		}
	}))
	defer server.Close()
	base := t.TempDir()
	cachePath := filepath.Join(base, "cache.json")
	builtin := filepath.Join(base, "builtin.json")
	if err := os.WriteFile(builtin, []byte(`{"schema_version":1,"schools":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	service := start(t, func(o *Options) {
		o.Paths.BuiltinPresets = builtin
		o.Paths.PresetsCache = cachePath
		o.PresetRefresh = application.PresetRefresher{Resolve: loopbackPresetBinding, Open: openPresetFetcher, Cache: presets.NewCache(cachePath), Sources: []string{server.URL + "/http", server.URL + "/html", server.URL + "/large", server.URL + "/good"}}
	})
	before := string(service.call("config.get", nil))
	usersBefore := string(service.call("user_presets.get", nil))
	receipt := submitPresetRefresh(t, service, "refresh")
	terminal := service.awaitTerminal(receipt.ActionID)
	if terminal.State != application.StateSucceeded {
		t.Fatalf("refresh %+v", terminal)
	}
	again := submitPresetRefresh(t, service, "refresh")
	if again.ActionID != receipt.ActionID || !again.Duplicate || requests.Load() != 4 {
		t.Fatal("idempotent retry fetched twice")
	}
	if list := readPresetList(t, service, nil); len(list.Public) != 1 || list.Public[0].ShortName != "remote" {
		t.Fatal(list)
	}
	var action ActionView
	if err := json.Unmarshal(service.call("action.get", ActionParams{ActionID: receipt.ActionID}), &action); err != nil {
		t.Fatal(err)
	}
	if action.Interface != "wan" || action.State != "succeeded" {
		t.Fatal(action)
	}
	if before != string(service.call("config.get", nil)) || usersBefore != string(service.call("user_presets.get", nil)) || len(service.status().Accounts) != 0 {
		t.Fatal("refresh rewrote account/user state")
	}
}

func TestPresetRPCCancelAndStopAbortHTTPWithoutLateCacheWrites(t *testing.T) {
	for _, mode := range []string{"cancel", "stop"} {
		t.Run(mode, func(t *testing.T) {
			entered, disconnected := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(entered)
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-time.After(patience):
					t.Error("server did not observe cancellation")
				}
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "cache.json")
			service := start(t, func(o *Options) {
				o.PresetRefresh = application.PresetRefresher{Resolve: loopbackPresetBinding, Open: openPresetFetcher, Cache: presets.NewCache(path), Sources: []string{server.URL}}
			})
			receipt := submitPresetRefresh(t, service, mode)
			select {
			case <-entered:
			case <-time.After(patience):
				t.Fatal("fetch never began")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if _, err := service.client.Call(ctx, "status.get", nil); err != nil {
				t.Fatalf("refresh blocked status: %v", err)
			}
			if mode == "cancel" {
				service.call("action.cancel", ActionParams{ActionID: receipt.ActionID})
				if terminal := service.awaitTerminal(receipt.ActionID); terminal.State != application.StateCancelled {
					t.Fatal(terminal)
				}
			} else {
				service.stop()
				if err := service.wait(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-disconnected:
			case <-time.After(patience):
				t.Fatal("HTTP body left running")
			}
			service.stop()
			service.wait()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("cancelled request published a cache")
			}
		})
	}
}

func TestPresetTransportFailsClosedForMissingBindingRedirectAndTLS(t *testing.T) {
	var reached atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); http.Redirect(w, r, "/other", 302) }))
	defer server.Close()
	b, _ := loopbackPresetBinding(t.Context(), "wan", 1)
	fetcher, closeClient, err := openPresetFetcher(b)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	if _, err := fetcher.Fetch(t.Context(), server.URL); err == nil || reached.Load() != 1 {
		t.Fatalf("redirect result: err=%v cause=%v requests=%d", err, errors.Unwrap(err), reached.Load())
	}
	b.L3Device = "srun-missing0"
	fetcher, closeBad, err := openPresetFetcher(b)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBad()
	if _, err := fetcher.Fetch(t.Context(), server.URL); err == nil || reached.Load() != 1 {
		t.Fatal("failed binding used default route")
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS accepted") }))
	defer tlsServer.Close()
	b, _ = loopbackPresetBinding(t.Context(), "wan", 2)
	fetcher, closeTLS, err := openPresetFetcher(b)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTLS()
	if _, err := fetcher.Fetch(t.Context(), tlsServer.URL); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}
