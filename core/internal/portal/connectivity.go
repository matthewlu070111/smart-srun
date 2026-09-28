package portal

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// EndpointBudget bounds one endpoint of a routine connectivity check over the
// bound line, name resolution included.
const EndpointBudget = 2 * time.Second

// SystemEndpointBudget bounds one endpoint when the check is asked over the
// router's own route instead of the bound line.
const SystemEndpointBudget = transport.SystemProbeTimeout

// ConnectivityEndpoints is how many endpoints one check tries at most.
const ConnectivityEndpoints = 3

// FailureKind says why one endpoint did not prove Internet access. It is a
// diagnosis for a DEBUG line, never shown as a user-facing message.
type FailureKind string

const (
	// FailureNone is an endpoint that answered 204 with an empty body.
	FailureNone FailureKind = ""
	// FailureDNS is a name that could not be resolved: no answer, NXDOMAIN or
	// a resolver the line cannot reach.
	FailureDNS FailureKind = "dns"
	// FailureTimeout is a budget that ran out before an answer arrived,
	// whichever step it ran out in.
	FailureTimeout FailureKind = "timeout"
	// FailureTransport is a connection that was refused, reset or unroutable.
	FailureTransport FailureKind = "transport"
	// FailureStatus is an answer that was not an empty 204: an intercepted
	// page, a redirect or an error status.
	FailureStatus FailureKind = "status"
	// FailureInvalid is an endpoint that could not even be turned into a
	// request.
	FailureInvalid FailureKind = "invalid"
)

// EndpointResult is what one endpoint answered.
type EndpointResult struct {
	Host   string
	Kind   FailureKind
	Status int
}

// String renders one result for a log field: host=kind or host=status_302.
func (r EndpointResult) String() string {
	switch r.Kind {
	case FailureNone:
		return r.Host + "=ok"
	case FailureStatus:
		return r.Host + "=status_" + strconv.Itoa(r.Status)
	default:
		return r.Host + "=" + string(r.Kind)
	}
}

// ConnectivityReport is one check's level plus what each endpoint answered, so
// a failure can say whether DNS, the connection or the answer was the problem.
type ConnectivityReport struct {
	Level     domain.Connectivity
	Endpoints []EndpointResult
}

// Summary lists every endpoint result in order, for a DEBUG line.
func (r ConnectivityReport) Summary() string {
	if len(r.Endpoints) == 0 {
		return "no_endpoints"
	}
	parts := make([]string, 0, len(r.Endpoints))
	for _, endpoint := range r.Endpoints {
		parts = append(parts, endpoint.String())
	}
	return strings.Join(parts, ",")
}

// LastFailure is the kind of the last endpoint that failed, or FailureNone.
func (r ConnectivityReport) LastFailure() FailureKind {
	for i := len(r.Endpoints) - 1; i >= 0; i-- {
		if r.Endpoints[i].Kind != FailureNone {
			return r.Endpoints[i].Kind
		}
	}
	return FailureNone
}

// CheckConnectivity uses the caller's bound transport, without discovering or
// following a portal. An intercepted response is never an authentication error.
// A later endpoint can establish Internet access even if the first is blocked.
func CheckConnectivity(ctx context.Context, client Fetcher, endpoints []string) (domain.Connectivity, error) {
	report, err := CheckConnectivityReport(ctx, client, endpoints, EndpointBudget)
	return report.Level, err
}

// CheckConnectivityReport is CheckConnectivity with a per-endpoint budget and
// the per-endpoint diagnosis kept. The client decides which way the requests
// leave; this function decides only what counts as Internet access.
func CheckConnectivityReport(ctx context.Context, client Fetcher, endpoints []string,
	budget time.Duration) (ConnectivityReport, error) {
	report := ConnectivityReport{Level: domain.ConnectivityUnknown}
	if budget <= 0 {
		budget = EndpointBudget
	}
	for _, endpoint := range endpoints[:min(ConnectivityEndpoints, len(endpoints))] {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		endpoint = Address(endpoint)
		if endpoint == "" {
			continue
		}
		result := EndpointResult{Host: endpointHost(endpoint)}
		probeCtx, stop := context.WithTimeout(ctx, budget)
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			stop()
			result.Kind = FailureInvalid
			report.Endpoints = append(report.Endpoints, result)
			continue
		}
		response, err := client.Do(req)
		if err != nil {
			timedOut := probeCtx.Err() != nil
			stop()
			if fatalDiscoveryError(err) {
				return report, err
			}
			result.Kind = classifyProbeError(err, timedOut)
			report.Endpoints = append(report.Endpoints, result)
			continue
		}
		status := response.StatusCode
		// Never retain HTML, cookies, or a Location from a routine check. Bound
		// the read so a slow or oversized error response cannot hold the loop.
		_, readErr := transport.ReadBounded(response.Body, transport.MaxAuthenticationBody, "连通性响应")
		response.Body.Close()
		stop()
		result.Status = status
		if status == http.StatusNoContent && readErr == nil {
			report.Endpoints = append(report.Endpoints, result)
			report.Level = domain.ConnectivityInternetReachable
			return report, nil
		}
		result.Kind = FailureStatus
		report.Endpoints = append(report.Endpoints, result)
		if status >= 200 && status < 400 {
			report.Level = domain.ConnectivityLimited
		}
	}
	return report, ctx.Err()
}

// classifyProbeError names why a request failed. A DNS failure is checked
// before the budget, because a resolver that never answers is the reason the
// budget ran out and is the part a user can act on.
func classifyProbeError(err error, timedOut bool) FailureKind {
	if code, ok := domain.CodeOf(err); ok && code == domain.CodeDNSFailure {
		return FailureDNS
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return FailureDNS
	}
	if code, ok := domain.CodeOf(err); ok && code == domain.CodeDeadlineExceeded {
		return FailureTimeout
	}
	if timedOut || errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return FailureTimeout
	}
	return FailureTransport
}

func endpointHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "invalid"
	}
	return parsed.Host
}
