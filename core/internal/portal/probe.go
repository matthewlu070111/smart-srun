package portal

import (
	"context"
	"net/http"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// Budget bounds one whole probe, not one request in it.
//
// The chain is the thing that has to terminate: eight hops each inside the
// per-request timeout would be half a minute, and the page waiting for this
// answer is a wizard step somebody is watching.
const Budget = 5 * time.Second

// Fetcher is the bound client a probe sends over.
//
// Defined here rather than taken as *transport.Client so a test can answer
// without a socket, and so this package cannot reach for a default client: a
// probe that left by the wrong line would report another network's portal.
type Fetcher interface {
	Do(request *http.Request) (*http.Response, error)
}

// Finding is what a probe learned.
type Finding struct {
	// ACID is empty when the chain ended without one, which is a normal
	// outcome and not an error: plenty of portals do not name it.
	ACID   string
	Source string
	// URL is where the AC_ID was found, or the last page the chain reached.
	URL string
	// Checked is every address visited, in order, so a message can say what was
	// actually looked at rather than "detection failed".
	Checked []string
}

// Probe follows a portal's own redirects looking for an AC_ID.
//
// It is deliberately not http.Client's redirect handling: the client this runs
// on refuses to follow redirects at all, because a credentialed request that
// gets redirected is being pointed somewhere this program never agreed to send
// credentials. Here, with no credentials involved, following is the point --
// and the loop is bounded, cycle-guarded and budgeted rather than trusted.
func Probe(ctx context.Context, client Fetcher, start string) (Finding, error) {
	finding := Finding{}
	current := Address(start)
	if current == "" {
		return finding, domain.FieldErrorf(domain.CodeInvalidArgument, "base_url",
			"需要一个 http:// 或 https:// 地址")
	}

	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()

	seen := make(map[string]bool, MaxRedirects)
	for range MaxRedirects {
		if current == "" || seen[current] {
			break
		}
		seen[current] = true
		finding.URL = current
		finding.Checked = append(finding.Checked, current)

		// The address first: a redirect that already carries ac_id answers the
		// question without fetching anything.
		if acid := ACIDFromURL(current); acid != "" {
			finding.ACID, finding.Source = acid, SourceURL
			return finding, nil
		}
		if ctx.Err() != nil {
			return finding, ctx.Err()
		}

		status, body, location, err := fetch(ctx, client, current)
		if err != nil {
			// A hop that failed is not a failed probe: the chain may have
			// already passed the interesting page, and the caller has other
			// candidates. What was learned so far travels back with the error.
			return finding, err
		}
		if location == "" {
			location = RedirectFromHTML(body)
		}
		if location != "" && Address(Join(current, location)) == "" {
			break
		}
		// Query-string evidence belongs to the redirect target, not this page's
		// origin. Only the page's own fields/assignments may stop us here.
		patterns := htmlACID[:len(htmlACID)-1]
		if location != "" {
			// A hidden field remains the strongest evidence. A generic script
			// assignment must not reinterpret text inside the redirect string.
			patterns = htmlACID[:3]
		}
		if acid := acidFromPatterns(body, patterns); acid != "" {
			finding.ACID, finding.Source = acid, SourceHTML
			return finding, nil
		}
		if location == "" {
			break
		}

		// Where the page pointed, if it is somewhere a probe may go. A portal
		// redirecting to javascript: or file: is broken or hostile, and the
		// chain stops rather than handing it to a client to refuse.
		next := Address(Join(current, location))
		if next == "" {
			break
		}
		if acid := ACIDFromURL(next); acid != "" {
			finding.ACID, finding.Source, finding.URL = acid, SourceRedirect, next
			finding.Checked = append(finding.Checked, next)
			return finding, nil
		}
		current = next
		if status >= 400 {
			// Followed once, because a 404 page can still carry the redirect a
			// portal uses, and then stopped: an error page's own error page is
			// not evidence of anything.
			break
		}
	}
	return finding, nil
}

// fetch performs one hop and reads a bounded body.
func fetch(ctx context.Context, client Fetcher, target string) (
	status int, body []byte, location string, err error) {

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, "", domain.FieldErrorf(domain.CodeInvalidArgument, "base_url",
			"无法请求 %s", target)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, "", err
	}
	defer response.Body.Close()

	body, err = transport.ReadBounded(response.Body, transport.MaxPortalBody, "认证页面")
	if err != nil {
		return response.StatusCode, nil, "", err
	}
	return response.StatusCode, body, response.Header.Get("Location"), nil
}
