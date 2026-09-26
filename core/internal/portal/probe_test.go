package portal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// direct is the client a probe runs on in these tests: an ordinary one that,
// like the bound client on a device, refuses to follow redirects itself.
type direct struct {
	client *http.Client
	calls  int
}

func newDirect() *direct {
	return &direct{client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (d *direct) Do(request *http.Request) (*http.Response, error) {
	d.calls++
	return d.client.Do(request)
}

// portalServer answers a path map, so a chain can be written as one.
func portalServer(t *testing.T, pages map[string]func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, handler := range pages {
		mux.HandleFunc(path, handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func redirectTo(target string) func(http.ResponseWriter, *http.Request) {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target)
		writer.WriteHeader(http.StatusFound)
	}
}

func html(body string) func(http.ResponseWriter, *http.Request) {
	return func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, body)
	}
}

func TestAProbeFollowsAPortalToItsACID(t *testing.T) {
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/":               redirectTo("/srun_portal_pc?ac_id=8"),
		"/srun_portal_pc": html("<html>the login page</html>"),
	})
	client := newDirect()

	finding, err := Probe(context.Background(), client, server.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if finding.ACID != "8" || finding.Source != SourceRedirect {
		t.Errorf("finding = %+v, want 8 from the redirect", finding)
	}
	// The address answered the question, so the page behind it was never
	// fetched: one request, not two.
	if client.calls != 1 {
		t.Errorf("%d requests, want 1 -- the redirect already carried it", client.calls)
	}
	if len(finding.Checked) != 2 {
		t.Errorf("checked = %v, want both addresses recorded", finding.Checked)
	}
}

// A portal that answers 200 with a script instead of a Location header is
// common enough that following only headers finds nothing on it.
func TestAProbeFollowsAScriptRedirect(t *testing.T) {
	var base string
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": func(writer http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(writer, `<html><script>top.self.location.href="%s/login"</script></html>`, base)
		},
		"/login": html(`<form><input type="hidden" name="ac_id" value="12"></form>`),
	})
	base = server.URL
	client := newDirect()

	finding, err := Probe(context.Background(), client, server.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if finding.ACID != "12" || finding.Source != SourceHTML {
		t.Errorf("finding = %+v, want 12 from the page", finding)
	}
	if !strings.HasSuffix(finding.URL, "/login") {
		t.Errorf("URL = %q, want the page it was found on", finding.URL)
	}
}

// A chain that loops is a chain that ends. Without the guard this is a probe
// that runs until its budget, on a router, for every wizard click.
func TestALoopEndsRatherThanRepeating(t *testing.T) {
	var base string
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/a": func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Location", base+"/b")
			writer.WriteHeader(http.StatusFound)
		},
		"/b": func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Location", base+"/a")
			writer.WriteHeader(http.StatusFound)
		},
	})
	base = server.URL
	client := newDirect()

	finding, err := Probe(context.Background(), client, server.URL+"/a")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if finding.ACID != "" {
		t.Errorf("finding = %+v, want nothing found", finding)
	}
	if client.calls != 2 {
		t.Errorf("%d requests, want the loop to stop after both pages", client.calls)
	}
}

// A chain that never repeats still ends, at the hop limit.
func TestAnEndlessChainStopsAtTheLimit(t *testing.T) {
	var base string
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Location",
				fmt.Sprintf("%s/?hop=%s1", base, request.URL.Query().Get("hop")))
			writer.WriteHeader(http.StatusFound)
		},
	})
	base = server.URL
	client := newDirect()

	if _, err := Probe(context.Background(), client, server.URL); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if client.calls > MaxRedirects {
		t.Errorf("%d requests, want at most %d", client.calls, MaxRedirects)
	}
}

// An enormous page is not read into memory. A portal is a web page; a portal
// that answers with a gigabyte is not one, and a router has no memory to spare
// finding that out.
func TestAnOversizedPageIsRefused(t *testing.T) {
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": func(writer http.ResponseWriter, _ *http.Request) {
			chunk := strings.Repeat("x", 64<<10)
			for written := 0; written < transport.MaxPortalBody+(128<<10); written += len(chunk) {
				if _, err := io.WriteString(writer, chunk); err != nil {
					return
				}
			}
		},
	})
	client := newDirect()

	_, err := Probe(context.Background(), client, server.URL)
	if err == nil {
		t.Fatal("an unbounded page was accepted")
	}
	// "Not a portal page" rather than "the connection broke": the limit is a
	// judgement about the answer, and the caller may try another candidate.
	if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid {
		t.Errorf("code = %v, want the answer refused as invalid", code)
	}
}

// A portal that points somewhere a browser would not follow either is where
// the chain stops. The client would refuse the scheme anyway; the point is
// that the request is never made.
func TestAChainStopsAtASchemeAProbeMayNotFollow(t *testing.T) {
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": html(`<script>location.href = "javascript:alert(1)"</script>`),
	})
	client := newDirect()

	finding, err := Probe(context.Background(), client, server.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if finding.ACID != "" {
		t.Errorf("finding = %+v, want nothing", finding)
	}
	if client.calls != 1 {
		t.Errorf("%d requests, want only the first page", client.calls)
	}
}

func TestAnAddressThatIsNotOneIsRefusedBeforeAnyRequest(t *testing.T) {
	client := newDirect()
	for _, raw := range []string{"", "   ", "javascript:alert(1)", "ftp://10.0.0.1/x"} {
		if _, err := Probe(context.Background(), client, raw); err == nil {
			t.Errorf("Probe(%q) was accepted", raw)
		}
	}
	if client.calls != 0 {
		t.Errorf("%d requests, want none", client.calls)
	}
}

// The first hop's answer is the one that counts when the page itself carries
// the value; the probe does not keep following after it has an answer.
func TestAProbeStopsWhenItHasAnAnswer(t *testing.T) {
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/":     html(`<input name="ac_id" value="1"><meta http-equiv="refresh" content="0;url=/next">`),
		"/next": html(`<input name="ac_id" value="2">`),
	})
	client := newDirect()

	finding, err := Probe(context.Background(), client, server.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if finding.ACID != "1" {
		t.Errorf("ACID = %q, want the first page's own value", finding.ACID)
	}
	if client.calls != 1 {
		t.Errorf("%d requests, want it to stop once it had an answer", client.calls)
	}
}
