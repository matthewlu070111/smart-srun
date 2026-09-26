package presets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Counterexamples from spec 04's bounds and cancellation rules and spec 03's
// requirement that an invalid publication never replace good cached data.
func TestRefreshRejectsOversizedValidJSONBeforeCaching(t *testing.T) {
	body := catalogueJSON("2026-09-16", "oversized")
	oversized := body + strings.Repeat(" ", int(MaxPayloadBytes)-len(body)+1)
	fetcher := &fakeFetcher{bodies: map[string]string{
		"oversized": oversized,
		"fallback":  catalogueJSON("2026-09-16", "usable"),
	}}
	cache := newTestCache(t)
	result, err := Refresh(t.Context(), fetcher, []string{"oversized", "fallback"}, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "fallback" || len(result.Attempts) != 2 || result.Attempts[0].Err == nil {
		t.Fatalf("oversized source accepted: source=%q attempts=%d", result.Source, len(result.Attempts))
	}
	entry, present, err := cache.Load()
	if err != nil || !present || len(entry.Catalogue.Schools) != 1 || entry.Catalogue.Schools[0].ShortName != "usable" {
		t.Fatalf("fallback was not cached: present=%v err=%v", present, err)
	}
}

func TestParseAcceptsExactlyThePayloadLimit(t *testing.T) {
	body := catalogueJSON("2026-09-16", "boundary")
	raw := []byte(body + strings.Repeat(" ", int(MaxPayloadBytes)-len(body)))
	if _, err := Parse(raw); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	if _, err := Parse(append(raw, ' ')); err == nil {
		t.Fatal("limit+1 was accepted")
	}
}

func TestCacheSaveChecksTheActualPayloadBeforeReplacingGoodData(t *testing.T) {
	cache := newTestCache(t)
	raw := []byte(catalogueJSON("2026-09-16", "good"))
	catalogue, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(raw, catalogue, "good", time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cache.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"schema_version":9,"schools":[]}`,
		`{"schema_version":1,"schools":"not an array"}`,
		`{"schema_version":1}`,
	} {
		if err := cache.Save([]byte(bad), catalogue, "bad", time.Now()); err == nil {
			t.Errorf("Save accepted invalid bytes with a previously parsed catalogue: %s", bad)
		}
		after, err := os.ReadFile(cache.Path())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Error("rejected payload changed the previous cache")
		}
	}
}

type cancelAfterFetch struct {
	cancel context.CancelFunc
	calls  int
}

func (f *cancelAfterFetch) Fetch(context.Context, string) ([]byte, error) {
	f.calls++
	f.cancel()
	return []byte(catalogueJSON("2026-09-16", "late")), nil
}

func TestRefreshDoesNotPublishAResultReturnedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fetcher := &cancelAfterFetch{cancel: cancel}
	cache := newTestCache(t)
	result, err := Refresh(ctx, fetcher, []string{"a", "b"}, cache, nil)
	if !errors.Is(err, context.Canceled) || result.Replaced {
		t.Errorf("cancelled refresh: err=%v replaced=%v", err, result.Replaced)
	}
	if fetcher.calls != 1 {
		t.Errorf("fetch calls=%d", fetcher.calls)
	}
	if _, present, _ := cache.Load(); present {
		t.Error("cancelled refresh wrote a cache")
	}
}

func TestRefreshCancelledBeforeStartDoesNotFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fetcher := &fakeFetcher{bodies: map[string]string{"a": catalogueJSON("2026-09-16", "late")}}
	_, err := Refresh(ctx, fetcher, []string{"a"}, newTestCache(t), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v, want cancelled", err)
	}
	if len(fetcher.requested()) != 0 {
		t.Error("already cancelled refresh reached the fetcher")
	}
}

func TestOversizedCacheFallsBackWithoutParsingItsPayload(t *testing.T) {
	// Valid JSON followed by excessive whitespace must fail before an unbounded
	// file allocation, and leave the offline built-in catalogue available.
	cache := newTestCache(t)
	raw := []byte(catalogueJSON("2026-09-16", "cached"))
	catalogue, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(raw, catalogue, "source", time.Now()); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(cache.Path(), os.O_WRONLY|os.O_APPEND, FileMode)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(strings.Repeat(" ", int(MaxPayloadBytes)*8))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write=%v close=%v", writeErr, closeErr)
	}
	builtin, err := Parse([]byte(catalogueJSON("2026-01-01", "builtin")))
	if err != nil {
		t.Fatal(err)
	}
	schools, err := Offline(builtin, cache)
	if err == nil {
		t.Error("oversized cache was accepted")
	}
	if len(schools) != 1 || schools[0].ShortName != "builtin" {
		t.Error("oversized cache replaced built-in fallback")
	}
}

func TestCachePreservesUnknownFieldsAndSourceLayout(t *testing.T) {
	cache := newTestCache(t)
	raw := []byte("{\n\t\"schema_version\":1, \"schools\":[],\n\t\"future\": { \"text\":\"<tag>&\", \"x\":[1, 2] }\n}\n")
	catalogue, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(raw, catalogue, "source", time.Now()); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(cache.Path())
	if err != nil || !bytes.Contains(stored, raw) {
		t.Fatalf("source layout was rewritten: %v", err)
	}
	entry, _, err := cache.Load()
	if err != nil || !bytes.Equal(entry.Raw, bytes.TrimSpace(raw)) {
		t.Fatalf("source content was changed on load: %v", err)
	}
	if err := cache.Save(raw, catalogue, strings.Repeat("x", maxSourceBytes+1), time.Now()); err == nil {
		t.Fatal("unbounded cache metadata was accepted")
	}
	if NewCache("").Path() != "/tmp/smart-srun/presets-cache.json" {
		t.Fatal("default cache is not on the spec 02 tmpfs path")
	}
}

func TestCacheRoundTripsAnExactLimitPayload(t *testing.T) {
	body := catalogueJSON("2026-09-16", "boundary")
	raw := []byte(body + strings.Repeat(" ", int(MaxPayloadBytes)-len(body)))
	catalogue, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cache := newTestCache(t)
	if err := cache.Save(raw, catalogue, strings.Repeat("\n", maxSourceBytes), time.Now()); err != nil {
		t.Fatal(err)
	}
	entry, present, err := cache.Load()
	if err != nil || !present || entry.Catalogue.Schools[0].ShortName != "boundary" {
		t.Fatalf("bounded wrapper rejected a valid maximum payload: %v", err)
	}
}
