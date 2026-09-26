package portal

import (
	"context"
	"net/http"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// CheckConnectivity uses the caller's bound transport, without discovering or
// following a portal. An intercepted response is never an authentication error.
// A later endpoint can establish Internet access even if the first is blocked.
func CheckConnectivity(ctx context.Context, client Fetcher, endpoints []string) (domain.Connectivity, error) {
	level := domain.ConnectivityUnknown
	for _, endpoint := range endpoints[:min(3, len(endpoints))] {
		if err := ctx.Err(); err != nil {
			return level, err
		}
		endpoint = Address(endpoint)
		if endpoint == "" {
			continue
		}
		probeCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			stop()
			continue
		}
		response, err := client.Do(req)
		if err != nil {
			stop()
			if fatalDiscoveryError(err) {
				return level, err
			}
			continue
		}
		status := response.StatusCode
		// Never retain HTML, cookies, or a Location from a routine check. Bound
		// the read so a slow or oversized error response cannot hold the loop.
		_, readErr := transport.ReadBounded(response.Body, transport.MaxAuthenticationBody, "连通性响应")
		response.Body.Close()
		stop()
		if status == http.StatusNoContent && readErr == nil {
			return domain.ConnectivityInternetReachable, nil
		}
		if status >= 200 && status < 400 {
			level = domain.ConnectivityLimited
		}
	}
	return level, ctx.Err()
}
