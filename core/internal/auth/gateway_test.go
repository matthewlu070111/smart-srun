package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// fakeGateway is an SRun portal that records what it was asked and answers
// what the test tells it to.
//
// The bytes this package sends are already checked against a frozen oracle in
// protocol/srun, so what is under test here is the sequence and the reading of
// the answers. Recording the query lets a test assert that the right parameters
// arrived without re-verifying the encryption.
type fakeGateway struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []recorded
	// reply is consulted per path; the zero value answers with an error the
	// test would notice.
	challenge  string
	clientIP   string
	loginBody  string
	logoutBody string
	onlineBody string
	// rawBody, when set, is returned for every path verbatim -- for the
	// malformed and HTML cases.
	rawBody string
	status  int
}

type recorded struct {
	path  string
	query url.Values
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	gateway := &fakeGateway{
		challenge: "token-abcdef",
		clientIP:  "10.0.0.77",
		loginBody: `{"error":"ok","suc_msg":"login_ok","client_ip":"10.0.0.77",` +
			`"user_name":"2020123456"}`,
		logoutBody: `{"error":"ok","suc_msg":"logout_ok"}`,
		onlineBody: `{"error":"ok","user_name":"2020123456",` +
			`"online_ip":"10.0.0.77"}`,
	}
	gateway.server = httptest.NewServer(http.HandlerFunc(gateway.serve))
	t.Cleanup(gateway.server.Close)
	return gateway
}

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests = append(g.requests, recorded{r.URL.Path, r.URL.Query()})
	raw, status := g.rawBody, g.status
	challenge, clientIP := g.challenge, g.clientIP
	login, logout, online := g.loginBody, g.logoutBody, g.onlineBody
	g.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
	}
	callback := r.URL.Query().Get("callback")
	if raw != "" {
		w.Write([]byte(raw))
		return
	}

	var body string
	switch r.URL.Path {
	case challengePath:
		body = `{"challenge":"` + challenge + `","client_ip":"` + clientIP + `"}`
	case portalPath:
		// The portal path serves logins and nothing else. Routing a logout
		// here on the strength of action=logout is what let the production
		// code send the signed logout to the wrong endpoint without any test
		// noticing: the fake had the same wrong idea, so the two agreed.
		body = login
	case logoutPath:
		body = logout
	case onlinePath:
		body = online
	default:
		http.NotFound(w, r)
		return
	}
	// Real portals wrap the JSON in the callback they were given.
	w.Write([]byte(callback + "(" + body + ")"))
}

func (g *fakeGateway) configure(change func(*fakeGateway)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	change(g)
}

func (g *fakeGateway) seen() []recorded {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recorded(nil), g.requests...)
}

// lastQuery returns the query of the most recent request to a path.
func (g *fakeGateway) lastQuery(t *testing.T, path string) url.Values {
	t.Helper()
	for i := len(g.seen()) - 1; i >= 0; i-- {
		if item := g.seen()[i]; item.path == path {
			return item.query
		}
	}
	t.Fatalf("no request was made to %s", path)
	return nil
}

// directLine is a Line that talks straight to the fake gateway.
//
// The real one is a bound transport.Client, which cannot reach loopback while
// pinned to lo -- measured, not assumed. What this package is responsible for
// is the transaction, and the binding has its own tests where it can be shown
// properly.
type directLine struct {
	client *http.Client
	source netip.Addr
}

func (l directLine) Do(req *http.Request) (*http.Response, error) {
	return l.client.Do(req)
}

func (l directLine) SourceAddr() netip.Addr { return l.source }

// transactionFor wires a transaction to the fake gateway with a fixed clock and
// callback, so a test can assert on the exact parameters that were sent.
func transactionFor(t *testing.T, gateway *fakeGateway) *Transaction {
	t.Helper()
	parsed, err := ParseGateway(gateway.server.URL, "12")
	if err != nil {
		t.Fatalf("ParseGateway: %v", err)
	}

	line := directLine{
		client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		source: netip.MustParseAddr("10.0.0.77"),
	}

	transaction := NewTransaction(line, parsed)
	fixed := time.Unix(1700000000, 0).UTC()
	transaction.now = func() time.Time { return fixed }
	transaction.callback = func() string { return "jQuery112400" }
	return transaction
}

func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	code, ok := domain.CodeOf(err)
	if !ok {
		t.Fatalf("error carries no code: %v", err)
	}
	return code
}

// A configured base URL is reduced to its origin. The endpoints are the
// protocol's, and appending them to a path the user typed produces a URL
// nobody meant.
func TestTheBaseURLIsReducedToAnOrigin(t *testing.T) {
	cases := map[string]string{
		"http://10.0.0.1":                    "http://10.0.0.1",
		"http://10.0.0.1/":                   "http://10.0.0.1",
		"http://10.0.0.1/srun_portal":        "http://10.0.0.1",
		"https://auth.example.edu.cn:8443/x": "https://auth.example.edu.cn:8443",
		"  http://10.0.0.1  ":                "http://10.0.0.1",
	}
	for input, want := range cases {
		gateway, err := ParseGateway(input, "1")
		if err != nil {
			t.Errorf("ParseGateway(%q): %v", input, err)
			continue
		}
		if gateway.BaseURL != want {
			t.Errorf("ParseGateway(%q) = %q, want %q", input, gateway.BaseURL, want)
		}
	}
}

// An address that is not usable is refused at configuration time, where the
// user can still see the field it belongs to.
func TestAnUnusableBaseURLIsRefusedWithItsField(t *testing.T) {
	for _, input := range []string{"", "   ", "10.0.0.1", "ftp://10.0.0.1",
		"http://", "://nonsense"} {
		_, err := ParseGateway(input, "1")
		if err == nil {
			t.Errorf("ParseGateway(%q) was accepted", input)
			continue
		}
		if code := codeOf(t, err); code != domain.CodeInvalidConfig {
			t.Errorf("ParseGateway(%q): code = %s, want InvalidConfig", input, code)
		}
	}
}
