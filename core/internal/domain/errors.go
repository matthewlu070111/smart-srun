package domain

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorCode is the stable machine-readable half of an error. Codes never
// change once published; the Chinese message may be reworded freely.
//
// Spec 03 fixes this list. A code is added here only when some caller has to
// branch on it, never to decorate a message.
type ErrorCode string

const (
	CodeInvalidArgument        ErrorCode = "InvalidArgument"
	CodeInvalidConfig          ErrorCode = "InvalidConfig"
	CodeConflict               ErrorCode = "Conflict"
	CodeBusy                   ErrorCode = "Busy"
	CodeNotFound               ErrorCode = "NotFound"
	CodeServiceStopped         ErrorCode = "ServiceStopped"
	CodeCancelled              ErrorCode = "Cancelled"
	CodeDeadlineExceeded       ErrorCode = "DeadlineExceeded"
	CodeBindingUnavailable     ErrorCode = "BindingUnavailable"
	CodeBindingChanged         ErrorCode = "BindingChanged"
	CodeDNSFailure             ErrorCode = "DNSFailure"
	CodeTransportFailure       ErrorCode = "TransportFailure"
	CodeTLSFailure             ErrorCode = "TLSFailure"
	CodePortalHTMLResponse     ErrorCode = "PortalHTMLResponse"
	CodeProtocolInvalid        ErrorCode = "ProtocolInvalid"
	CodeAuthRejected           ErrorCode = "AuthRejected"
	CodeOnlineIdentityMismatch ErrorCode = "OnlineIdentityMismatch"
	CodeRecoveryRequired       ErrorCode = "RecoveryRequired"
	CodeUnsupportedCapability  ErrorCode = "UnsupportedCapability"
	CodeChecksumMismatch       ErrorCode = "ChecksumMismatch"
	CodePackageIncompatible    ErrorCode = "PackageIncompatible"
	CodeInstallFailed          ErrorCode = "InstallFailed"

	// CodeInternal is for a failure this program did not classify: a bug, or an
	// error from the standard library that reached the wire unwrapped. Spec 03
	// fixes a minimum list and this is above it (D11), because the alternative
	// is reporting a fault in the daemon as InvalidArgument -- telling the user
	// to correct input that was already correct.
	CodeInternal ErrorCode = "Internal"
)

// Error carries one user-facing problem. Field is the dotted path the user can
// act on ("retry.max_seconds", "campus_accounts[1].operator_suffix"); it is
// empty when the problem is not attributable to one field.
//
// Message is written for a user, in Chinese, and must never contain a secret.
// Anything sensitive belongs in the wrapped error, which stays internal.
type Error struct {
	Code    ErrorCode
	Field   string
	Message string
	wrapped error
}

func (e *Error) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Message)
}

func (e *Error) Unwrap() error { return e.wrapped }

// Errorf builds an error with no field attribution.
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// FieldErrorf builds an error the UI can attach to one input.
func FieldErrorf(code ErrorCode, field, format string, args ...any) *Error {
	return &Error{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches an internal cause. The cause is never rendered to the user, so
// it may hold detail that the message must not.
func (e *Error) Wrap(cause error) *Error {
	e.wrapped = cause
	return e
}

// CodeOf reports the code of the first *Error in err's chain. Errors that did
// not come from this package are not guessed at: they report false.
func CodeOf(err error) (ErrorCode, bool) {
	if typed, ok := errors.AsType[*Error](err); ok {
		return typed.Code, true
	}
	return "", false
}

// Errors is a set of problems reported together.
//
// Config validation returns every problem it found in one pass: a settings form
// that reveals its mistakes one reload at a time is a worse form.
type Errors struct {
	Code  ErrorCode
	Items []*Error
}

func (e *Errors) Error() string {
	parts := make([]string, 0, len(e.Items))
	for _, item := range e.Items {
		parts = append(parts, item.Error())
	}
	return strings.Join(parts, "; ")
}

// Unwrap exposes the set to errors.Is/As, so a caller that only needs the code
// -- an exit-code mapper, an RPC envelope -- gets the same answer whether one
// problem or ten were reported.
func (e *Errors) Unwrap() []error {
	out := make([]error, len(e.Items))
	for i, item := range e.Items {
		out[i] = item
	}
	return out
}

// Err returns nil when nothing was collected, so callers can `return v.Err()`.
func (e *Errors) Err() error {
	if e == nil || len(e.Items) == 0 {
		return nil
	}
	return e
}

// Add appends one problem, tagging it with the set's code when it has none.
func (e *Errors) Add(item *Error) {
	if item.Code == "" {
		item.Code = e.Code
	}
	e.Items = append(e.Items, item)
}

// Addf is the common case: a field problem with the set's code.
func (e *Errors) Addf(field, format string, args ...any) {
	e.Add(FieldErrorf(e.Code, field, format, args...))
}

// Fields lists the attributable field paths, in the order they were reported.
func (e *Errors) Fields() []string {
	out := make([]string, 0, len(e.Items))
	for _, item := range e.Items {
		if item.Field != "" {
			out = append(out, item.Field)
		}
	}
	return out
}
