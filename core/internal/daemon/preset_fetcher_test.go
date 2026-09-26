package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

type trackedPresetBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *trackedPresetBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *trackedPresetBody) Close() error { b.closed = true; return nil }

func TestPresetFetcherBoundsReadsAndClosesEveryHTTPResponse(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		data      string
		wantRead  int
		wantError bool
	}{
		{"success", 200, "{}", 2, false},
		{"redirect", 302, "private portal response", 0, true},
		{"server error", 500, "private response", 0, true},
		{"oversize", 200, strings.Repeat("x", int(presets.MaxPayloadBytes)+5000), int(presets.MaxPayloadBytes) + 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedPresetBody{Reader: strings.NewReader(test.data)}
			fetcher := presetFetcher{do: func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.Header.Get("Accept") != "application/json" || req.Header.Get("Authorization") != "" || req.Body != nil {
					t.Fatal("unexpected request")
				}
				return &http.Response{StatusCode: test.status, Body: body}, nil
			}}
			_, err := fetcher.Fetch(t.Context(), "https://catalogue.test/presets.json")
			if (err != nil) != test.wantError || !body.closed || body.read != test.wantRead {
				t.Fatalf("err=%v closed=%v read=%d", err, body.closed, body.read)
			}
		})
	}
}

func TestPresetFetcherRefusesCredentialedOrMalformedSources(t *testing.T) {
	called := false
	fetcher := presetFetcher{do: func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("offline") }}
	for _, source := range []string{"://bad", "file:///tmp/secret", "/relative", "https://user:secret@example.com/file"} {
		if _, err := fetcher.Fetch(context.Background(), source); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	if called {
		t.Fatal("invalid source reached transport")
	}
	if _, err := fetcher.Fetch(context.Background(), "https://example.test"); err == nil || !called {
		t.Fatal("lost network error")
	}
}
