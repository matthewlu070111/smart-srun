package application

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

func refreshRequest(iface, key string) Request {
	return Request{Kind: KindPresetsRefresh, Interface: iface, IdempotencyKey: key}
}

func TestPresetRefreshSerializesCacheAcrossLinesAndCancellation(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Lines = func(r Request) string { return r.Interface } })
	first, err := h.Submit(t.Context(), refreshRequest("wan", "first"))
	if err != nil {
		t.Fatal(err)
	}
	h.awaitStart()
	h.lingerOn(first.ActionID)
	second, err := h.Submit(t.Context(), refreshRequest("wan2", "second"))
	if err != nil {
		t.Fatal(err)
	}
	h.expectNoStart("two refreshes must not write the same cache")
	h.cancel(first.ActionID)
	h.awaitCancelSeen(first.ActionID)
	h.expectNoStart("cancelled worker has not released the cache")
	h.succeed(first.ActionID)
	if next := h.awaitStart(); next.ID != second.ActionID {
		t.Fatalf("started %+v", next)
	}
	h.succeed(second.ActionID)
	h.awaitState(second.ActionID, StateSucceeded)
	if h.state(first.ActionID).State != StateCancelled {
		t.Fatal("late success reopened cancellation")
	}
}

func TestPresetRefreshYieldsToAuthenticationAndKeysIncludeLine(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Parallel = 1 })
	blocker := h.submit(KindLogin, "a", "block")
	h.awaitStart()
	request := refreshRequest("wan", "refresh")
	refresh, err := h.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.Submit(t.Context(), request)
	if err != nil || again.ActionID != refresh.ActionID || !again.Duplicate {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	request.Interface = "wan2"
	if code, _ := domain.CodeOf(h.submitExpectingError(request)); code != domain.CodeConflict {
		t.Fatal(code)
	}
	manual := h.submit(KindLogin, "b", "manual")
	h.succeed(blocker.ActionID)
	if next := h.awaitStart(); next.ID != manual.ActionID {
		t.Fatal("refresh outranked manual action")
	}
	h.succeed(manual.ActionID)
	if next := h.awaitStart(); next.ID != refresh.ActionID {
		t.Fatal("refresh did not follow manual action")
	}
	h.succeed(refresh.ActionID)
	if KindPresetsRefresh.Priority() != policy.PriorityPeriodic || KindPresetsRefresh.Manual() {
		t.Fatal("refresh priority")
	}
}

type presetFetchFunc func(context.Context, string) ([]byte, error)

func (f presetFetchFunc) Fetch(ctx context.Context, s string) ([]byte, error) { return f(ctx, s) }

const workerCatalogue = `{"schema_version":1,"updated_at":"2026-09-16","schools":[{"short_name":"new","name":"New","status":"active"}]}`

func TestPresetWorkerBindingBudgetsFallbackAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := presets.NewCache(path)
	closed, opened, fetched := 0, 0, 0
	worker := PresetRefresher{Cache: cache, Sources: []string{"bad", "good"}, Now: func() time.Time { return epoch }}
	worker.Resolve = func(ctx context.Context, iface string, seq uint64) (domain.Binding, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > PresetRefreshBudget || time.Until(deadline) < 30*time.Second {
			t.Fatal("missing total budget")
		}
		if iface != "wan2" || seq != 42 {
			t.Fatalf("wrong selected line %q/%d", iface, seq)
		}
		return domain.Binding{LogicalIface: iface, Generation: seq, L3Device: "eth1", SourceIPv4: netip.MustParseAddr("192.0.2.2")}, nil
	}
	worker.Open = func(b domain.Binding) (presets.Fetcher, func(), error) {
		opened++
		if b.L3Device != "eth1" || b.SourceIPv4.String() != "192.0.2.2" {
			t.Fatal("lost binding")
		}
		return presetFetchFunc(func(ctx context.Context, source string) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > presets.FetchTimeout {
				t.Fatal("missing per-source deadline")
			}
			fetched++
			if source == "bad" {
				return []byte("<html>captive portal</html>"), nil
			}
			return []byte(workerCatalogue), nil
		}), func() { closed++ }, nil
	}
	action := Action{Request: refreshRequest("wan2", "key"), Sequence: 42}
	var phase Phase
	result := worker.Run(t.Context(), action, func(p Phase) { phase = p })
	if result.State != StateSucceeded || closed != 1 || opened != 1 || fetched != 2 || phase != PhaseFetch {
		t.Fatalf("outcome %+v close/open/fetch %d/%d/%d", result, closed, opened, fetched)
	}
	entry, exists, err := cache.Load()
	if err != nil || !exists || string(entry.Raw) != workerCatalogue {
		t.Fatalf("cache %+v %v", entry, err)
	}
	// A successful response older than the cache does not publish the older view.
	worker.Sources = []string{"good"}
	newer := []byte(`{"schema_version":1,"updated_at":"2099","schools":[]}`)
	catalogue, _ := presets.Parse(newer)
	if err := cache.Save(newer, catalogue, "existing", epoch); err != nil {
		t.Fatal(err)
	}
	result = worker.Run(t.Context(), action, func(Phase) {})
	if result.State != StateSucceeded || result.Message != "已检查预设来源，保留较新的本地缓存" {
		t.Fatal(result)
	}
	entry, _, _ = cache.Load()
	if string(entry.Raw) != string(newer) {
		t.Fatal("downgrade")
	}
}

func TestPresetWorkerFailuresAndLateCancellationDoNotWrite(t *testing.T) {
	for _, mode := range []string{"resolve", "wrong-line", "unready", "generation", "open", "sources", "cancel", "deadline", "early-cancel"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache.json")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opened, closed := 0, 0
			worker := PresetRefresher{Cache: presets.NewCache(path), Sources: []string{"source"}}
			worker.Resolve = func(context.Context, string, uint64) (domain.Binding, error) {
				b := domain.Binding{LogicalIface: "wan", Generation: 1, L3Device: "eth0", SourceIPv4: netip.MustParseAddr("192.0.2.1")}
				switch mode {
				case "resolve":
					return b, domain.Errorf(domain.CodeBindingUnavailable, "down")
				case "wrong-line":
					b.LogicalIface = "other"
				case "unready":
					b.L3Device = ""
				case "generation":
					b.Generation = 2
				case "deadline":
					return b, context.DeadlineExceeded
				}
				return b, nil
			}
			worker.Open = func(domain.Binding) (presets.Fetcher, func(), error) {
				opened++
				if mode == "open" {
					return nil, nil, errors.New("cannot open")
				}
				return presetFetchFunc(func(context.Context, string) ([]byte, error) {
					if mode == "sources" {
						return nil, errors.New("offline")
					}
					cancel()
					return []byte(workerCatalogue), nil
				}), func() { closed++ }, nil
			}
			if mode == "early-cancel" {
				cancel()
			}
			result := worker.Run(ctx, Action{Request: refreshRequest("wan", "key"), Sequence: 1}, func(Phase) {})
			if result.State != StateFailed {
				t.Fatal(result)
			}
			if mode == "cancel" && result.Code != domain.CodeCancelled {
				t.Fatal(result)
			}
			if mode == "deadline" && result.Code != domain.CodeDeadlineExceeded {
				t.Fatal(result)
			}
			if mode == "cancel" || mode == "sources" {
				if opened != 1 || closed != 1 {
					t.Fatal("client not closed")
				}
			} else if mode != "open" && opened != 0 {
				t.Fatal("opened invalid binding")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("failure wrote cache")
			}
		})
	}
}

func TestPresetRequestRejectsAmbiguousInputs(t *testing.T) {
	for _, iface := range []string{"", "wan\x00x", "wan other", " wan", "/wan"} {
		if err := refreshRequest(iface, "key").Validate(); err == nil {
			t.Fatalf("accepted %q", iface)
		}
	}
	for _, request := range []Request{
		{Kind: KindPresetsRefresh, Interface: "wan", AccountID: "a", IdempotencyKey: "k"},
		{Kind: KindPresetsRefresh, Interface: "wan", HotspotID: "h", IdempotencyKey: "k"},
		{Kind: KindPresetsRefresh, Interface: "wan", IgnoreQuiet: true, IdempotencyKey: "k"},
		{Kind: KindLogin, Interface: "wan", AccountID: "a", IdempotencyKey: "k"},
	} {
		if request.Validate() == nil {
			t.Fatal("accepted ambiguous request")
		}
	}
}
