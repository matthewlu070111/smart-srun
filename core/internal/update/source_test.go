package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func response(request *http.Request, body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1, Request: request}
}

func testSource(t *testing.T, fn roundTripFunc) *Source {
	t.Helper()
	source := NewSource()
	source.client.Transport = fn
	t.Cleanup(source.Close)
	return source
}

func TestDownloadChecksExactBytesAndKeepsNoFailedPayload(t *testing.T) {
	const payload = "synthetic binary bytes"
	hash := sha256.Sum256([]byte(payload))
	asset := fixtureManifest("apk", "core").Assets[0]
	asset.Bytes, asset.SHA256 = int64(len(payload)), hex.EncodeToString(hash[:])
	for name, body := range map[string]string{"valid": payload, "short": payload[:4], "long": payload + "extra", "wrong hash": strings.Repeat("x", len(payload))} {
		t.Run(name, func(t *testing.T) {
			source := testSource(t, func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("Authorization") != "" || request.Header.Get("Accept-Encoding") != "identity" {
					t.Fatal("unexpected credentials or encoding")
				}
				return response(request, body, http.StatusOK), nil
			})
			path := filepath.Join(t.TempDir(), "payload.apk")
			err := source.Download(t.Context(), asset, path)
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(path)
				info, _ := os.Stat(path)
				if string(got) != payload || info.Mode().Perm() != 0o600 {
					t.Fatal("payload content or mode differs")
				}
				if err := source.Download(t.Context(), asset, path); err == nil {
					t.Fatal("overwrote an existing payload")
				}
			} else if _, statErr := os.Stat(path); err == nil || !os.IsNotExist(statErr) {
				t.Fatalf("failed payload retained: %v, %v", err, statErr)
			}
		})
	}
}

func TestSourceRefusesForeignOrDowngradedRedirectBeforeContact(t *testing.T) {
	for _, target := range []string{
		"http://release-assets.githubusercontent.com/test", "https://attacker.invalid/test",
		"https://github.com/someone/smart-srun/releases/download/2.0.0rc10/file.apk",
		"https://user:password@release-assets.githubusercontent.com/test", "https://release-assets.githubusercontent.com:444/test",
	} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			source := testSource(t, func(request *http.Request) (*http.Response, error) {
				calls++
				r := response(request, "", http.StatusFound)
				r.Header.Set("Location", target)
				return r, nil
			})
			_, err := source.Manifest(t.Context(), Version{Major: 2, RC: 10})
			if err == nil || calls != 1 {
				t.Fatalf("foreign redirect contacted: %d, %v", calls, err)
			}
		})
	}
}

func TestSourceAcceptsBoundedAssetRedirectAndRejectsOversizedMetadata(t *testing.T) {
	calls := 0
	source := testSource(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			r := response(request, "", http.StatusFound)
			r.Header.Set("Location", "https://release-assets.githubusercontent.com/test?signature=synthetic")
			return r, nil
		}
		return response(request, strings.Repeat("x", MaxManifestBytes+1), http.StatusOK), nil
	})
	if _, err := source.Manifest(t.Context(), Version{Major: 2, RC: 10}); err == nil || calls != 2 {
		t.Fatalf("metadata limit: calls=%d, error=%v", calls, err)
	}
}

func TestCandidatesRespectChannelDraftsMajorVersionAndNumericOrder(t *testing.T) {
	const index = `[
		{"tag_name":"2.0.0rc2","prerelease":true},
		{"tag_name":"2.0.0rc10","prerelease":true},
		{"tag_name":"2.0.0"},
		{"tag_name":"2.1.0","draft":true},
		{"tag_name":"3.0.0"},
		{"tag_name":"2.0.0rc99","prerelease":false},
		{"tag_name":"2.0.0rc10","prerelease":true},
		{"tag_name":"1.6.0"}
	]`
	source := testSource(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != releasesURL {
			t.Fatal("unexpected index source")
		}
		return response(request, index, http.StatusOK), nil
	})
	for channel, wanted := range map[string][]Version{
		"rc":     {{Major: 2}, {Major: 2, RC: 10}, {Major: 2, RC: 2}},
		"stable": {{Major: 2}},
	} {
		got, err := source.Candidates(t.Context(), Version{Major: 2, RC: 1}, channel)
		if err != nil || !reflect.DeepEqual(got, wanted) {
			t.Fatalf("%s: %+v, %v", channel, got, err)
		}
	}
}

func TestCancelledDownloadDoesNotCreateFile(t *testing.T) {
	source := testSource(t, func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join(t.TempDir(), "payload")
	err := source.Download(ctx, fixtureManifest("apk", "core").Assets[0], path)
	if _, statErr := os.Stat(path); err == nil || !os.IsNotExist(statErr) {
		t.Fatal("cancelled request created a payload")
	}
}
