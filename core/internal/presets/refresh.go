package presets

import (
	"context"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// FetchTimeout is how long one source gets.
//
// Spec 04's five seconds for a whole HTTP request, and deliberately not the
// baseline's eight. Where the two disagree the spec wins and the difference is
// recorded rather than inherited: four sources at eight seconds is half a
// minute of a user watching a wizard that looks stuck.
const FetchTimeout = 5 * time.Second

// DefaultSources is where a catalogue is looked for, in order.
//
// Read off the baseline's own constants rather than a document about them. The
// last is a domain whose renewal is not planned past 2027 and is last for that
// reason; the GitHub raw URL is the one that works when the two mirrors are
// blocked, which on a campus network happens.
var DefaultSources = []string{
	"https://srun.guiguisocute.com/school-presets.json",
	"https://smart-srun--cloudflare-pages.pages.dev/school-presets.json",
	"https://raw.githubusercontent.com/matthewlu070111/smart-srun/main/doc/school-presets.json",
	"https://srun.edu-publish.site/school-presets.json",
}

// Fetcher retrieves one URL.
//
// Consumer-defined, and the only way this package reaches a network: the rules
// it exists for -- which source wins, when a cache may be replaced -- have to
// be testable without one, and the architecture table forbids it from building
// an HTTP client of its own.
//
// An implementation must not return more than MaxPayloadBytes; ReadLimited in
// this package is what enforces that, and is here rather than in the transport
// so the rule can be tested without a socket.
type Fetcher interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// Attempt is what one source did.
//
// Kept per source rather than reduced to a single error, because "all four
// sources failed" and "the first three were blocked and the fourth served
// something that was not a catalogue" need different things done about them,
// and only the second is this project's problem.
type Attempt struct {
	Source string
	Err    error
}

// Refreshed is the outcome of a refresh.
type Refreshed struct {
	Catalogue Catalogue
	// Raw is the payload as published, so the cache stores a copy of what was
	// served rather than a re-encoding of this build's understanding of it.
	Raw []byte
	// Source is the URL that answered.
	Source string
	// Replaced reports that the cache was written. False with no error means a
	// source answered with something older than the cache already held.
	Replaced bool
	Attempts []Attempt
}

// Refresh tries each source in turn and caches the first usable answer.
//
// A source is skipped for any reason at all, not only a network failure:
// invalid JSON, a schema this build does not know, a body over the limit. That
// is the part worth stating, because it is the part that is easy to get wrong.
// A mirror that has been redirected to a captive portal answers 200 with HTML,
// and a chain that only advanced on transport errors would stop there and
// report the catalogue as unreadable while three working sources sat behind it.
//
// Nothing is written until a payload has been parsed. The baseline records why
// in a comment and it is worth repeating: a bad publication written to the
// cache poisons it, and because the same path loads the cache and the built-in
// fallback, every later read then fails -- taking the offline catalogue down
// with it.
func Refresh(ctx context.Context, fetcher Fetcher, sources []string,
	cache *Cache, now func() time.Time) (Refreshed, error) {

	if fetcher == nil {
		return Refreshed{}, domain.Errorf(domain.CodeInvalidArgument,
			"刷新预设需要一个取回器")
	}
	if len(sources) == 0 {
		return Refreshed{}, domain.Errorf(domain.CodeInvalidArgument,
			"没有可用的预设来源")
	}
	if now == nil {
		now = time.Now
	}

	result := Refreshed{}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		catalogue, raw, err := fetchOne(ctx, fetcher, source)
		if err != nil {
			result.Attempts = append(result.Attempts, Attempt{source, err})
			if ctx.Err() != nil {
				// The caller stopped. Trying the rest would be three more
				// immediate failures reported as though the sources were bad.
				return result, ctx.Err()
			}
			continue
		}

		result.Catalogue = catalogue
		result.Raw = raw
		result.Source = source
		result.Attempts = append(result.Attempts, Attempt{Source: source})

		if cache == nil {
			return result, nil
		}
		at := now()
		if err := ctx.Err(); err != nil {
			return result, err
		}
		replaced, err := store(cache, catalogue, raw, source, at)
		if err != nil {
			return result, err
		}
		result.Replaced = replaced
		return result, nil
	}

	return result, domain.Errorf(domain.CodeTransportFailure,
		"%d 个预设来源都没有给出可用的目录", len(sources))
}

// fetchOne gets one source and reads it, with the per-source budget.
func fetchOne(ctx context.Context, fetcher Fetcher, source string) (
	Catalogue, []byte, error) {

	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	raw, err := fetcher.Fetch(ctx, source)
	if err != nil {
		return Catalogue{}, nil, err
	}
	// A transport may finish just as the caller cancels or the deadline fires.
	// Its nil error must not turn a late result into a successful refresh.
	if err := ctx.Err(); err != nil {
		return Catalogue{}, nil, err
	}
	catalogue, err := Parse(raw)
	if err != nil {
		return Catalogue{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return Catalogue{}, nil, err
	}
	return catalogue, raw, nil
}

// store writes the fresh payload unless the cache already holds something
// newer.
//
// A cache that cannot be read is replaced rather than treated as newer: there
// is nothing in it to lose, and leaving it would mean a corrupt file blocks
// every future refresh.
func store(cache *Cache, catalogue Catalogue, raw []byte, source string,
	at time.Time) (bool, error) {

	entry, present, err := cache.Load()
	if present && err == nil && !catalogue.Supersedes(entry.Catalogue) {
		// Older than what is already here. The source answered and was read;
		// it simply has nothing newer to say.
		return false, nil
	}
	if err := cache.Save(raw, catalogue, source, at); err != nil {
		return false, err
	}
	return true, nil
}

// Offline assembles the catalogue without going anywhere.
//
// Built-in first, cache over the top: the built-in list ships with the package
// and fixes the order, and the cache is newer but not more authoritative about
// which school appears where. A cache that cannot be read is reported and
// skipped rather than fatal -- the built-in catalogue is exactly what exists
// for that.
func Offline(builtin Catalogue, cache *Cache) ([]School, error) {
	if cache == nil {
		return builtin.Schools, nil
	}
	entry, present, err := cache.Load()
	if !present {
		return builtin.Schools, nil
	}
	if err != nil {
		return builtin.Schools, err
	}
	return Merge(builtin.Schools, entry.Catalogue.Schools), nil
}
