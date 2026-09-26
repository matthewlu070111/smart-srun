package portal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type environmentFetcher func(*http.Request) (*http.Response, error)

func (f environmentFetcher) Do(r *http.Request) (*http.Response, error) { return f(r) }
func page(status int, body, location string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Location": []string{location}}}
}

func TestEnvironmentOnlineStillFindsPortalUsingFullCandidatePath(t *testing.T) {
	var calls []string
	client := environmentFetcher(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.String())
		if r.Method != "GET" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("credentialed request")
		}
		switch r.URL.Path {
		case "/204":
			return page(204, "", ""), nil
		case "/bad":
			return nil, errors.New("unreachable")
		case "/login":
			if r.URL.RawQuery != "theme=pro" {
				t.Fatal("lost query")
			}
			return page(200, `<input name="ac_id" value="007">`, ""), nil
		}
		t.Fatal("unexpected request")
		return nil, nil
	})
	result, err := DetectEnvironment(t.Context(), client, []string{"http://check.invalid/204"}, []Candidate{
		{"http://portal.invalid/bad", "学校预设"}, {"http://portal.invalid/login?theme=pro", "该线路已有账号"},
	})
	if err != nil || !result.OK || result.State != "online" || result.ACID != "007" || result.BaseURL != "http://portal.invalid" || len(calls) != 3 {
		t.Fatalf("%+v / %v / %v", result, err, calls)
	}
}

func TestEnvironmentNeverMistakesConnectivityServerForPortal(t *testing.T) {
	for _, response := range []struct {
		status         int
		body, location string
		state          string
	}{
		{204, "", "", "online"},
		{200, `<html><input name="ac_id" value="8"></html>`, "", "portal"},
		{302, "", "/login?ac_id=9", "portal"},
		{302, "", "https://check.invalid/login?ac_id=9", "portal"},
		{302, "", "http://portal.invalid/?action=logout", "portal"},
		{503, "unavailable", "", "down"},
	} {
		client := environmentFetcher(func(*http.Request) (*http.Response, error) {
			return page(response.status, response.body, response.location), nil
		})
		result, err := DetectEnvironment(t.Context(), client, []string{"http://check.invalid/204"}, nil)
		if err != nil || result.OK || result.BaseURL != "" || result.State != response.state {
			t.Fatalf("%+v / %v", result, err)
		}
	}
}

func TestEnvironmentFollowsRelativeThenCrossOriginRedirectWithoutAuth(t *testing.T) {
	calls := 0
	client := environmentFetcher(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path == "/204" {
			return page(302, "", "/landing"), nil
		}
		if r.URL.Path == "/landing" {
			return page(200, `<script>location.href="http://portal.invalid/login?theme=pro&amp;ac_id=008"</script>`, ""), nil
		}
		t.Fatal("should use redirect evidence without another fetch")
		return nil, nil
	})
	r, err := DetectEnvironment(t.Context(), client, []string{"http://check.invalid/204"}, nil)
	if err != nil || !r.OK || r.ACID != "008" || r.BaseURL != "http://portal.invalid" || calls != 2 {
		t.Fatalf("%+v / %v", r, err)
	}
}

func TestEnvironmentBindingChangeAndCancellationStopFallback(t *testing.T) {
	for _, failure := range []error{domain.Errorf(domain.CodeBindingChanged, "changed"), context.Canceled} {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		client := environmentFetcher(func(*http.Request) (*http.Response, error) {
			calls++
			if failure == context.Canceled {
				cancel()
			}
			return nil, failure
		})
		_, err := DetectEnvironment(ctx, client, []string{"http://check.invalid/204", "http://check2.invalid/204"}, []Candidate{{"http://portal.invalid", "网关"}})
		cancel()
		if err == nil || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}

func TestEnvironmentLimitsCandidatesAndSkipsCredentialURLs(t *testing.T) {
	client := newDirect()
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){"/": html("nothing here")})
	candidates := []Candidate{{"http://student:secret@portal.invalid/", "bad"}}
	for _, path := range []string{"/1", "/1", "/2", "/3", "/4", "/5"} {
		candidates = append(candidates, Candidate{server.URL + path, "网关"})
	}
	r, err := DetectEnvironment(t.Context(), client, nil, candidates)
	if err != nil || r.OK || client.calls != 4 || len(r.CandidatesChecked) != 4 {
		t.Fatalf("%+v / %v / %d", r, err, client.calls)
	}
}
