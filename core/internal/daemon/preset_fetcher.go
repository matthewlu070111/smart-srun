package daemon

import (
	"context"
	"net/http"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

type presetRoutingRunner struct{ actions, refresh application.Runner }

func (r presetRoutingRunner) Run(ctx context.Context, action application.Action, report func(application.Phase)) application.Outcome {
	if action.Request.Kind == application.KindPresetsRefresh {
		return r.refresh.Run(ctx, action, report)
	}
	return r.actions.Run(ctx, action, report)
}

// A refresh owns its client for its entire bounded lifetime. No socket survives
// to a later refresh whose DHCP address or selected interface may have changed.
func openPresetFetcher(binding domain.Binding) (presets.Fetcher, func(), error) {
	client, err := transport.NewClient(binding)
	if err != nil {
		return nil, nil, err
	}
	return presetFetcher{do: client.Do}, client.Close, nil
}

type presetFetcher struct {
	do func(*http.Request) (*http.Response, error)
}

func (f presetFetcher) Fetch(ctx context.Context, source string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "预设来源地址无效")
	}
	if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" || req.URL.User != nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "预设来源必须是不含凭据的 HTTP 地址")
	}
	req.Header.Set("Accept", "application/json")
	response, err := f.do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, domain.Errorf(domain.CodeTransportFailure, "预设来源返回 HTTP %d", response.StatusCode)
	}
	return presets.ReadLimited(response.Body, presets.MaxPayloadBytes)
}
