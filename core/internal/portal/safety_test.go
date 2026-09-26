package portal

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestCredentialAndAuthenticationURLsAreNeverFetched(t *testing.T) {
	for _, target := range []string{
		"http://student:private-password@portal.invalid/login",
		"http://portal.invalid/?password=private-password",
		"http://portal.invalid/cgi-bin/srun_portal?action=logout",
		"http://portal.invalid/?access_token=private-token",
		"http://portal.invalid/?USERNAME=student",
		"http://portal.invalid/" + strings.Repeat("a", 2048),
	} {
		if Address(target) != "" {
			t.Fatal("unsafe address accepted")
		}
		client := newDirect()
		if _, err := Probe(context.Background(), client, target); err == nil || client.calls != 0 {
			t.Fatal("unsafe address reached fetch")
		}
	}
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": html(`<script>location.href="http://student:secret@other.invalid/?ac_id=6"</script>`),
	})
	client := newDirect()
	got, err := Probe(context.Background(), client, server.URL)
	if err != nil || got.ACID != "" || client.calls != 1 {
		t.Fatal("credential redirect was followed")
	}
}

func TestEntityEscapesAndACIDLength(t *testing.T) {
	if got := ACIDFromHTML([]byte(`<input name="ac_id" value="&#48;07">`)); got != "007" {
		t.Fatal(got)
	}
	if got := RedirectFromHTML([]byte(`<script>location.href="/p?a=1&amp;ac_id=9"</script>`)); got != "/p?a=1&ac_id=9" {
		t.Fatal(got)
	}
	if ValidACID(strings.Repeat("a", 65)) != "" {
		t.Fatal("overlong ac_id")
	}
}

func TestHTMLRedirectAttributesACIDToDestination(t *testing.T) {
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": html(`<script>location.href="http://portal.example.invalid/login?theme=pro&amp;ac_id=007"</script>`),
	})
	client := newDirect()
	finding, err := Probe(context.Background(), client, server.URL)
	if err != nil || finding.ACID != "007" || finding.Source != SourceRedirect || Origin(finding.URL) != "http://portal.example.invalid" || client.calls != 1 {
		t.Fatalf("redirect evidence assigned to wrong origin: %+v / %v", finding, err)
	}
}

func TestCancelledProbeDoesNotPretendItFoundNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := newDirect()
	_, err := Probe(ctx, client, "http://portal.invalid/login")
	if !errors.Is(err, context.Canceled) || client.calls != 0 {
		t.Fatalf("%v / %d", err, client.calls)
	}
}
