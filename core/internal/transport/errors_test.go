package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type requestTimeout struct{}

func (requestTimeout) Error() string   { return "synthetic timeout" }
func (requestTimeout) Timeout() bool   { return true }
func (requestTimeout) Temporary() bool { return false }

func TestRequestErrorCodePreservesFailureCategories(t *testing.T) {
	for _, test := range []struct {
		err  error
		code domain.ErrorCode
	}{
		{&net.DNSError{Err: "timeout", IsTimeout: true}, domain.CodeDNSFailure},
		{&tls.CertificateVerificationError{Err: errors.New("certificate")}, domain.CodeTLSFailure},
		{x509.UnknownAuthorityError{}, domain.CodeTLSFailure},
		{x509.HostnameError{}, domain.CodeTLSFailure},
		{x509.CertificateInvalidError{}, domain.CodeTLSFailure},
		{requestTimeout{}, domain.CodeDeadlineExceeded},
		{domain.Errorf(domain.CodeBindingUnavailable, "binding"), domain.CodeBindingUnavailable},
		{errors.New("connection refused"), domain.CodeTransportFailure},
	} {
		if got := RequestErrorCode(t.Context(), fmt.Errorf("wrapped: %w", test.err)); got != test.code {
			t.Fatalf("%T: got %s, want %s", test.err, got, test.code)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := RequestErrorCode(ctx, &net.DNSError{}); got != domain.CodeCancelled {
		t.Fatal(got)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if got := RequestErrorCode(ctx, errors.New("connection closed")); got != domain.CodeDeadlineExceeded {
		t.Fatal(got)
	}
}
