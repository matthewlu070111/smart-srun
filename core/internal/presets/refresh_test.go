package presets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// fakeFetcher answers a canned body per URL and records what was asked for.
//
// The lock is not decoration. Refresh runs on the caller's goroutine, but the
// cancellation test has to watch from another one to know when a fetch is in
// flight -- and the race detector found that reading the slice while Fetch
// appended to it was exactly the kind of thing it is for.
type fakeFetcher struct {
	mu     sync.Mutex
	bodies map[string]string
	errs   map[string]error
	asked  []string
	// pause blocks the fetch, so a test can cancel while one is in flight.
	pause   chan struct{}
	started chan struct{}
}

func (f *fakeFetcher) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	f.mu.Lock()
	f.asked = append(f.asked, url)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.pause != nil {
		select {
		case <-f.pause:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err, ok := f.errs[url]; ok {
		return nil, err
	}
	if body, ok := f.bodies[url]; ok {
		return []byte(body), nil
	}
	return nil, errors.New("nothing configured for " + url)
}

func catalogueJSON(updatedAt string, ids ...string) string {
	schools := make([]string, 0, len(ids))
	for _, id := range ids {
		schools = append(schools, `{"id":"`+id+`","status":"active"}`)
	}
	return `{"schema_version":1,"updated_at":"` + updatedAt + `","schools":[` +
		strings.Join(schools, ",") + `]}`
}

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	return NewCache(filepath.Join(t.TempDir(), "cache", "school-presets.json"))
}

// A source that answers with something unusable is skipped like one that did
// not answer at all.
//
// This is the part that is easy to get wrong and expensive when it is. A mirror
// redirected to a captive portal answers 200 with HTML; a chain that only
// advanced on transport errors would stop there and report the catalogue as
// unreadable while three working sources sat behind it.
func TestABadPayloadAdvancesToTheNextSource(t *testing.T) {
	fetcher := &fakeFetcher{
		bodies: map[string]string{
			"a": "<html>captive portal</html>",
			"b": `{"schema_version":9,"schools":[]}`,
			"c": `{"schema_version":1}`,
			"d": catalogueJSON("2026-09-03", "jxnu"),
		},
	}
	result, err := Refresh(t.Context(), fetcher, []string{"a", "b", "c", "d"},
		newTestCache(t), nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.Source != "d" {
		t.Errorf("source = %q, want the one that served a catalogue", result.Source)
	}
	if len(result.Catalogue.Schools) != 1 {
		t.Errorf("catalogue = %+v", result.Catalogue)
	}
	if len(fetcher.requested()) != 4 {
		t.Errorf("asked %v, want every source up to the one that worked",
			fetcher.requested())
	}

	// And each failure is reported with its own source, because "everything
	// failed" and "three were blocked and one served rubbish" need different
	// things done about them.
	if len(result.Attempts) != 4 {
		t.Fatalf("attempts = %+v", result.Attempts)
	}
	for _, attempt := range result.Attempts[:3] {
		if attempt.Err == nil {
			t.Errorf("attempt %q reported no error", attempt.Source)
		}
	}
	if result.Attempts[3].Err != nil {
		t.Errorf("the source that worked reported %v", result.Attempts[3].Err)
	}
}

// Every source failing is one error, and it says how many were tried.
func TestEverySourceFailingIsReportedOnce(t *testing.T) {
	fetcher := &fakeFetcher{errs: map[string]error{
		"a": errors.New("no route"),
		"b": errors.New("timeout"),
	}}
	result, err := Refresh(t.Context(), fetcher, []string{"a", "b"},
		newTestCache(t), nil)
	if err == nil {
		t.Fatal("no sources worked and that was reported as success")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeTransportFailure {
		t.Errorf("code = %s, want TransportFailure", code)
	}
	if len(result.Attempts) != 2 {
		t.Errorf("attempts = %+v, want one per source", result.Attempts)
	}
}

// A cancelled refresh stops rather than burning through the remaining sources.
//
// Carrying on would turn one cancellation into three more immediate failures,
// reported as though the sources themselves were bad.
func TestACancelledRefreshStopsAtOnce(t *testing.T) {
	fetcher := &fakeFetcher{pause: make(chan struct{}), started: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var result Refreshed
	var err error
	go func() {
		defer close(done)
		result, err = Refresh(ctx, fetcher, []string{"a", "b", "c"},
			newTestCache(t), nil)
	}()

	// Synchronize on the actual call instead of polling with real sleeps.
	<-fetcher.started
	cancel()
	<-done

	if err == nil {
		t.Fatal("a cancelled refresh was reported as success")
	}
	if len(result.Attempts) != 1 {
		t.Errorf("attempts = %+v, want only the one that was in flight",
			result.Attempts)
	}
}

// The first usable answer is cached, and an older one is not.
func TestAFreshCatalogueIsCachedAndAnOlderOneIsNot(t *testing.T) {
	cache := newTestCache(t)
	at := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	fetcher := &fakeFetcher{bodies: map[string]string{
		"a": catalogueJSON("2026-09-10", "jxnu"),
	}}
	first, err := Refresh(t.Context(), fetcher, []string{"a"}, cache,
		func() time.Time { return at })
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !first.Replaced {
		t.Fatal("the first refresh did not write the cache")
	}

	// An older publication answers, and is read, and changes nothing.
	older := &fakeFetcher{bodies: map[string]string{
		"a": catalogueJSON("2026-09-01", "somewhere-else"),
	}}
	second, err := Refresh(t.Context(), older, []string{"a"}, cache,
		func() time.Time { return at })
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if second.Replaced {
		t.Error("an older catalogue replaced a newer cache")
	}

	entry, present, err := cache.Load()
	if err != nil || !present {
		t.Fatalf("cache: present=%v err=%v", present, err)
	}
	if entry.Catalogue.UpdatedAt != "2026-09-10" {
		t.Errorf("cached updated_at = %q, want the newer one",
			entry.Catalogue.UpdatedAt)
	}
	if len(entry.Catalogue.Schools) != 1 ||
		entry.Catalogue.Schools[0].ShortName != "jxnu" {
		t.Errorf("cache holds %+v", entry.Catalogue.Schools)
	}
}

// A correction published under the same date replaces the cache.
//
// The same-day rule, through the whole refresh path rather than only against
// Supersedes: a publisher who fixes a mistake an hour after publishing does not
// bump the date, and requiring a newer one would leave every router on the
// broken copy until tomorrow.
func TestASameDayCorrectionReplacesTheCache(t *testing.T) {
	cache := newTestCache(t)
	at := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return at }

	broken := &fakeFetcher{bodies: map[string]string{
		"a": catalogueJSON("2026-09-16", "typo"),
	}}
	if _, err := Refresh(t.Context(), broken, []string{"a"}, cache, clock); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	fixed := &fakeFetcher{bodies: map[string]string{
		"a": catalogueJSON("2026-09-16", "corrected"),
	}}
	result, err := Refresh(t.Context(), fixed, []string{"a"}, cache, clock)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !result.Replaced {
		t.Fatal("a same-day correction was refused; the > became a >=")
	}

	entry, _, err := cache.Load()
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	if entry.Catalogue.Schools[0].ShortName != "corrected" {
		t.Errorf("cache holds %q, want the correction",
			entry.Catalogue.Schools[0].ShortName)
	}
}

// Nothing is written until a payload has been parsed.
//
// The baseline records the reason in a comment and it is worth keeping: a bad
// publication written to the cache poisons it, and because the same path loads
// the cache and the built-in fallback, every later read then fails -- taking
// the offline catalogue down with it.
func TestAnUnreadablePublicationNeverReachesTheCache(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &fakeFetcher{bodies: map[string]string{
		"a": `{"schema_version":1,"schools":"not a list"}`,
	}}

	if _, err := Refresh(t.Context(), fetcher, []string{"a"}, cache, nil); err == nil {
		t.Fatal("an unreadable publication was accepted")
	}
	if _, present, _ := cache.Load(); present {
		t.Error("a cache file was written for a payload nobody could read")
	}
}

// Save refuses bytes nobody has parsed.
//
// Refresh already orders it that way, so no caller today can get this wrong;
// the guard is here for the next one. It is the documented precondition of a
// method on an exported type, and a precondition nothing enforces is a comment.
func TestTheCacheRefusesAPayloadNobodyHasParsed(t *testing.T) {
	cache := newTestCache(t)
	at := time.Now()

	// A catalogue nobody parsed -- the zero value, which is what a caller who
	// skipped Parse would be holding.
	if err := cache.Save([]byte(catalogueJSON("2026-09-16", "x")),
		Catalogue{}, "src", at); err == nil {
		t.Error("Save accepted a catalogue that was never parsed")
	}
	if _, present, _ := cache.Load(); present {
		t.Error("the refused save still wrote a file")
	}

	// And an empty body, which is the other way to arrive with nothing.
	parsed, err := Parse([]byte(catalogueJSON("2026-09-16", "x")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cache.Save(nil, parsed, "src", at); err == nil {
		t.Error("Save accepted an empty payload")
	}
	if _, present, _ := cache.Load(); present {
		t.Error("the refused save still wrote a file")
	}
}

// The cache survives a round trip with the document intact.
//
// The same JSON, not the same bytes: the encoder re-indents the nested payload,
// which is what makes the file readable on a router and cannot matter to any
// parser. This asserts what is actually promised -- that a later read gets the
// same catalogue back -- rather than a byte equality the code does not provide.
func TestTheCacheKeepsTheDocumentAsItWasPublished(t *testing.T) {
	cache := newTestCache(t)
	body := catalogueJSON("2026-09-03", "jxnu")
	catalogue, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	at := time.Date(2026, 9, 16, 8, 30, 0, 0, time.UTC)

	if err := cache.Save([]byte(body), catalogue, "https://example/x", at); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entry, present, err := cache.Load()
	if err != nil || !present {
		t.Fatalf("Load: present=%v err=%v", present, err)
	}
	// Re-parsed rather than compared as text, because whitespace is the
	// encoder's. What must survive is the catalogue.
	reread, err := Parse(entry.Raw)
	if err != nil {
		t.Fatalf("the stored payload no longer parses: %v", err)
	}
	if reread.UpdatedAt != catalogue.UpdatedAt ||
		len(reread.Schools) != len(catalogue.Schools) {
		t.Errorf("round trip changed the catalogue: %+v -> %+v",
			catalogue, reread)
	}
	if len(reread.Schools) > 0 && reread.Schools[0].ShortName != "jxnu" {
		t.Errorf("round trip lost the school: %+v", reread.Schools)
	}
	if entry.SourceURL != "https://example/x" {
		t.Errorf("source = %q", entry.SourceURL)
	}
	if !entry.CachedAt.Equal(at) {
		t.Errorf("cached_at = %v, want %v", entry.CachedAt, at)
	}

	info, err := os.Stat(cache.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != FileMode {
		t.Errorf("mode = %04o, want %04o", got, FileMode)
	}
}

// A cache that cannot be read is present and broken, which is neither absent
// nor fatal.
//
// Three answers rather than two. Absent is the ordinary case on a fresh
// install; broken should fall back to the built-in catalogue rather than fail,
// and should still be said out loud instead of behaving like an empty one.
func TestABrokenCacheIsReportedRatherThanTreatedAsAbsent(t *testing.T) {
	for name, body := range map[string]string{
		"not json":        "{{{",
		"no payload":      `{"cached_at":1,"source_url":"x"}`,
		"payload is junk": `{"payload":{"schema_version":"v9"}}`,
	} {
		cache := newTestCache(t)
		if err := os.MkdirAll(filepath.Dir(cache.Path()), DirMode); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := os.WriteFile(cache.Path(), []byte(body), FileMode); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		entry, present, err := cache.Load()
		if !present {
			t.Errorf("%s: a file that exists was reported absent", name)
		}
		if err == nil {
			t.Errorf("%s: a cache that cannot be read was accepted: %+v", name, entry)
		}
	}

	// And no file at all is neither.
	cache := newTestCache(t)
	entry, present, err := cache.Load()
	if present || err != nil {
		t.Errorf("a fresh install reported present=%v err=%v entry=%+v",
			present, err, entry)
	}
}

// A broken cache does not take the built-in catalogue down with it.
func TestABrokenCacheStillLeavesTheBuiltInCatalogue(t *testing.T) {
	builtin, err := Parse([]byte(catalogueJSON("2026-01-01", "builtin-school")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cache := newTestCache(t)
	if err := os.MkdirAll(filepath.Dir(cache.Path()), DirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cache.Path(), []byte("{{{"), FileMode); err != nil {
		t.Fatalf("write: %v", err)
	}

	schools, err := Offline(builtin, cache)
	if err == nil {
		t.Error("a broken cache was not reported")
	}
	if len(schools) != 1 || schools[0].ShortName != "builtin-school" {
		t.Errorf("schools = %+v, want the built-in catalogue", schools)
	}
}

// Offline lays the cache over the built-in list.
func TestOfflineUsesTheCacheOverTheBuiltInList(t *testing.T) {
	builtin, err := Parse([]byte(`{"schema_version":1,"schools":[
		{"id":"a","name":"builtin a","status":"active"},
		{"id":"b","name":"builtin b","status":"active"}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	body := `{"schema_version":1,"updated_at":"2026-09-16","schools":[
		{"id":"b","name":"cached b","status":"active"},
		{"id":"c","name":"cached c","status":"active"}]}`
	cached, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cache := newTestCache(t)
	if err := cache.Save([]byte(body), cached, "x", time.Now()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	schools, err := Offline(builtin, cache)
	if err != nil {
		t.Fatalf("Offline: %v", err)
	}
	if len(schools) != 3 {
		t.Fatalf("schools = %+v", schools)
	}
	if schools[0].Name != "builtin a" {
		t.Errorf("schools[0] = %q", schools[0].Name)
	}
	if schools[1].Name != "cached b" {
		t.Errorf("schools[1] = %q, want the cached value", schools[1].Name)
	}
	if schools[2].Name != "cached c" {
		t.Errorf("schools[2] = %q, want the new school appended", schools[2].Name)
	}
}

// Every source in the shipped list is a URL over TLS.
//
// The catalogue decides what a wizard offers and what base URL an account is
// built from. A plain-HTTP source would let anybody on the path between a
// router and its mirror choose the gateway address this program authenticates
// against.
func TestEveryShippedSourceIsHTTPS(t *testing.T) {
	if len(DefaultSources) < 2 {
		t.Fatal("a single source is not a fallback chain")
	}
	for _, source := range DefaultSources {
		if !strings.HasPrefix(source, "https://") {
			t.Errorf("%q is not https", source)
		}
	}
}
