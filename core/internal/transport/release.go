package transport

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// NewReleaseClient is the public HTTPS download path, separate from the
// credential-bearing client bound to a campus line. It follows the device's
// current Internet route, never inherits proxy/auth/cookie state, and gives
// the release source control over its finite GitHub redirect allowlist.
// Authentication and school strategies must continue using NewClient.
func NewReleaseClient(redirect func(*http.Request, []*http.Request) error) *http.Client {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10, MaxConnsPerHost: 2, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: redirect}
}
