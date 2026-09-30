package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// RequestErrorCode classifies a failed request without exposing its URL,
// response or wrapped error. Cancellation takes precedence over lower-level
// resolver errors; a resolver timeout otherwise remains a DNS failure.
func RequestErrorCode(ctx context.Context, err error) domain.ErrorCode {
	if errors.Is(ctx.Err(), context.Canceled) {
		return domain.CodeCancelled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.CodeDeadlineExceeded
	}
	if code, ok := domain.CodeOf(err); ok {
		return code
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return domain.CodeDNSFailure
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return domain.CodeTLSFailure
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return domain.CodeTLSFailure
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return domain.CodeTLSFailure
	}
	if _, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		return domain.CodeTLSFailure
	}
	if network, ok := errors.AsType[net.Error](err); ok && network.Timeout() {
		return domain.CodeDeadlineExceeded
	}
	return domain.CodeTransportFailure
}
