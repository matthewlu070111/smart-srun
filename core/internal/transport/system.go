package transport

import (
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// SystemProbeTimeout bounds one whole request of the system-route probe.
const SystemProbeTimeout = 5 * time.Second

// SystemProbeClient reaches the network the way any other program on the
// router does: the kernel's routing table, the system resolver, no binding.
//
// It exists for one question only -- "is the Internet reachable at all?" --
// asked after the bound line has already confirmed this account's session and
// only when the router has a single uplink. A local proxy that hijacks the
// router's own DNS (nikki/mihomo, OpenClash) answers that question for normal
// programs while a socket pinned to the WAN device cannot even get its DNS
// reply back. It must never carry a credential or talk to a campus gateway:
// those requests have to leave by the chosen line, and that is still the
// bound Client's job.
//
// The rest of Client's rules still hold: no proxy from the environment, no
// redirects followed, no shared default transport, no pooled connections.
type SystemProbeClient struct {
	inner *http.Client
}

// NewSystemProbeClient builds the unbound probe client. The daemon's wiring
// is the only caller; an architecture test keeps it that way.
func NewSystemProbeClient() *SystemProbeClient {
	dialer := &net.Dialer{Timeout: DialTimeout}
	return &SystemProbeClient{inner: &http.Client{
		Transport: &http.Transport{
			// Nil, not http.ProxyFromEnvironment: an explicit proxy variable is
			// not the route the router itself takes.
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   TLSHandshakeTimeout,
			ResponseHeaderTimeout: ResponseHeaderTimeout,
			// One request per check, minutes apart. Holding an idle
			// connection would outlive a route change it cannot see.
			DisableKeepAlives: true,
			ForceAttemptHTTP2: false,
		},
		Timeout: SystemProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Do sends one request by the system route.
func (c *SystemProbeClient) Do(req *http.Request) (*http.Response, error) {
	response, err := c.inner.Do(req)
	if err != nil {
		return nil, classifySystem(req, err)
	}
	return response, nil
}

func classifySystem(req *http.Request, err error) error {
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return domain.Errorf(domain.CodeDNSFailure,
			"系统解析器无法解析 %s", req.URL.Host).Wrap(err)
	}
	if req.Context().Err() != nil {
		return domain.Errorf(domain.CodeDeadlineExceeded,
			"请求 %s 超时或已取消", req.URL.Host).Wrap(err)
	}
	return domain.Errorf(domain.CodeTransportFailure,
		"请求 %s 失败", req.URL.Host).Wrap(err)
}
