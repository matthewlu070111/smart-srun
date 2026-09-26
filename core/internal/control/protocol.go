// Package control is the local control protocol: the one way the CLI and the
// LuCI bridge ask the daemon to do something.
//
// It is a Unix socket, never TCP. The wire format is one UTF-8 JSON object per
// line, one request and one response per connection. That is deliberately dull:
// the surface an attacker who reaches the socket can reason about is a fixed
// list of method names and typed parameters, and nothing on this path ever
// turns a string into a command to execute.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Version is the protocol version. A client that sends anything else is
// refused rather than served a guess at what it meant.
const Version = 1

// Wire limits. They bound allocation before it happens: the reader stops at the
// limit instead of reading an unbounded line and checking its size afterwards.
const (
	// Backup JSON is transported intact inside a JSON string (up to 2x escaping).
	MaxRequestBytes  = 1024*1024 + 4096
	MaxResponseBytes = 1024 * 1024
)

// SocketPath and its directory. The directory is 0700 and the socket 0600, so
// reaching the protocol at all requires already being root on the device.
const (
	RuntimeDir = "/var/run/smart-srun"
	SocketPath = RuntimeDir + "/control.sock"

	RuntimeDirMode fs.FileMode = 0o700
	SocketFileMode fs.FileMode = 0o600
)

// Request is one call.
//
// Params stays raw so each method decodes it into its own type with unknown
// fields refused. A shared map would let a caller attach a parameter one method
// honours to another method that ignores it.
type Request struct {
	RPCVersion int             `json:"rpc_version"`
	RequestID  string          `json:"request_id"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params,omitempty"`
}

// Response is one reply. Exactly one of Result and Error is populated.
type Response struct {
	RPCVersion int             `json:"rpc_version"`
	RequestID  string          `json:"request_id"`
	OK         bool            `json:"ok"`
	Result     json.RawMessage `json:"result"`
	Error      *ErrorPayload   `json:"error"`
}

// ErrorPayload is the failure envelope.
//
// Code is stable and machine-readable; Message is Chinese and written for a
// person. Retryable is decided from the code, not guessed from a transport
// status: "the gateway returned 503" and "this configuration is invalid" are
// not the same kind of failure even when both arrive over HTTP.
type ErrorPayload struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
	Retryable bool              `json:"retryable"`
}

// detailKeys is the whole set of keys a response may carry in Details.
//
// A whitelist rather than a filter: an error built anywhere in the program can
// only ever surface these, so no future call site can leak a password, a URL
// with credentials or a device identifier by attaching it as "context".
var detailKeys = map[string]struct{}{
	"field":               {},
	"expected_revision":   {},
	"actual_revision":     {},
	"action_id":           {},
	"job_id":              {},
	"retry_after_seconds": {},
	"limit":               {},
	"capability":          {},
	"interface":           {},
}

// AllowedDetailKeys lists the whitelist, for tests and documentation.
func AllowedDetailKeys() []string {
	out := make([]string, 0, len(detailKeys))
	for key := range detailKeys {
		out = append(out, key)
	}
	return out
}

// sanitizeDetails drops anything not on the whitelist.
func sanitizeDetails(details map[string]string) map[string]string {
	if len(details) == 0 {
		return nil
	}
	out := make(map[string]string, len(details))
	for key, value := range details {
		if _, ok := detailKeys[key]; ok {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// retryableCodes are the failures where the same request, unchanged, could
// succeed later. Everything else needs the caller to change something first.
var retryableCodes = map[domain.ErrorCode]bool{
	domain.CodeBusy:               true,
	domain.CodeDeadlineExceeded:   true,
	domain.CodeServiceStopped:     true,
	domain.CodeBindingUnavailable: true,
	domain.CodeBindingChanged:     true,
	domain.CodeDNSFailure:         true,
	domain.CodeTransportFailure:   true,
	domain.CodeTLSFailure:         true,
}

// Retryable reports whether a code describes a transient failure.
func Retryable(code domain.ErrorCode) bool { return retryableCodes[code] }

// NewErrorPayload renders an error for the wire.
//
// Only messages this program wrote are shown to the user. An error from
// anywhere else -- the standard library, a dependency, a bug -- gets a generic
// one: its text was written for a developer reading a log, has never been
// checked for a password or an address, and is in English.
//
// Wrapped causes stay inside the daemon for the same reason. They exist to be
// logged under the operator's own redaction rules, not handed to a browser.
func NewErrorPayload(err error) *ErrorPayload {
	code := domain.CodeInternal
	message := "服务内部错误"
	details := map[string]string{}

	switch set, single := asErrorSet(err), asError(err); {
	// The set is checked first: it unwraps to its items, so looking for a
	// single error would find the first problem and report only that one, and a
	// form with five mistakes would reveal them one save at a time.
	case set != nil:
		code, message = set.Code, set.Error()
		if fields := set.Fields(); len(fields) > 0 {
			details["field"] = fields[0]
		}
	case single != nil:
		code, message = single.Code, single.Message
		if single.Field != "" {
			details["field"] = single.Field
		}
	// Only reached when nothing classified the failure, so a timeout that a
	// handler forgot to describe still arrives as a timeout rather than as a
	// complaint about the request.
	case errors.Is(err, context.Canceled):
		code, message = domain.CodeCancelled, "操作已取消"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		code, message = domain.CodeDeadlineExceeded, "操作超时"
	}

	if code == "" {
		code = domain.CodeInternal
	}
	return &ErrorPayload{
		Code:      string(code),
		Message:   message,
		Details:   sanitizeDetails(details),
		Retryable: Retryable(code),
	}
}

// asErrorSet and asError find this program's own error types anywhere in the
// chain. A handler that adds context with %w must not lose the code the UI
// branches on.
func asErrorSet(err error) *domain.Errors {
	if set, ok := errors.AsType[*domain.Errors](err); ok {
		return set
	}
	return nil
}

func asError(err error) *domain.Error {
	if single, ok := errors.AsType[*domain.Error](err); ok {
		return single
	}
	return nil
}

// NewSuccess builds an ok response. The result is marshalled here so a method
// that returns an unencodable value fails as an error rather than a truncated
// success.
func NewSuccess(requestID string, result any) (Response, error) {
	encoded := json.RawMessage("null")
	if result != nil {
		data, err := json.Marshal(result)
		if err != nil {
			return Response{}, domain.Errorf(domain.CodeInvalidArgument,
				"结果无法序列化").Wrap(err)
		}
		encoded = data
	}
	return Response{
		RPCVersion: Version,
		RequestID:  requestID,
		OK:         true,
		Result:     encoded,
	}, nil
}

// NewFailure builds an error response. Result stays null so a client cannot
// read a half-result from a failed call.
func NewFailure(requestID string, err error) Response {
	return Response{
		RPCVersion: Version,
		RequestID:  requestID,
		OK:         false,
		Result:     json.RawMessage("null"),
		Error:      NewErrorPayload(err),
	}
}
