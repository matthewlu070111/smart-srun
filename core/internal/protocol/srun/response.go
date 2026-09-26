package srun

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// MaxAuthResponseBytes is the authentication response cap from spec 04. A
// gateway that answers an authentication request with more than this is not
// answering it; it is serving a page.
const MaxAuthResponseBytes = 64 * 1024

// ResponseKind classifies a response without quoting it.
//
// It exists so a failure can be logged and counted usefully. The body may hold
// a session identifier, an account name or a portal page, so the text itself
// never reaches a log line or a user -- the kind is what gets recorded instead.
type ResponseKind string

const (
	KindObject    ResponseKind = "object"
	KindEmpty     ResponseKind = "empty"
	KindHTML      ResponseKind = "html"
	KindNonObject ResponseKind = "non_object"
	KindOversize  ResponseKind = "oversize"
	KindMalformed ResponseKind = "malformed"
)

// htmlMarkers are the openings that mean a portal page rather than an API
// answer. Matched case-insensitively against the start of the body.
var htmlMarkers = [][]byte{
	[]byte("<!doctype html"),
	[]byte("<html"),
	[]byte("<head"),
	[]byte("<body"),
	[]byte("<form"),
}

// htmlSniffLimit bounds how much of the body is examined for those markers.
const htmlSniffLimit = 1024

// ParseJSONP extracts the JSON object from an SRun response.
//
// The gateway answers either bare JSON or a JSONP call, and this accepts both.
// What it does not do is treat the text between the first "(" and the last ")"
// as the payload, which is how the baseline read it: that accepts any prefix at
// all as a callback name. Here the callback has to look like a JavaScript
// identifier, and the payload has to be exactly one JSON object with nothing
// after it. Nothing is evaluated -- the name is only ever compared, never run.
//
// The returned bytes are the object as it arrived. Decoding it into a typed
// response belongs to the caller: this layer settles the framing, not the
// meaning.
//
// body is expected to already be UTF-8; converting from a declared charset is
// the transport's job. A body that is not valid UTF-8 is not rejected here,
// because the fields that decide success are ASCII and refusing the whole
// response over a mis-declared Chinese error message would turn a working login
// into a failure.
func ParseJSONP(body []byte, limit int) (json.RawMessage, ResponseKind, error) {
	if limit > 0 && len(body) > limit {
		return nil, KindOversize, domain.Errorf(domain.CodeProtocolInvalid,
			"认证响应超过 %d KiB 上限", limit/1024)
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, KindEmpty, domain.Errorf(domain.CodeProtocolInvalid,
			"认证服务器返回了空响应")
	}

	value, err := decodeSingleJSONValue(unwrapCallback(trimmed))
	if err != nil {
		return nil, classifyUnparseable(trimmed), htmlOrProtocolError(trimmed)
	}
	if len(value) == 0 || value[0] != '{' {
		return nil, KindNonObject, domain.Errorf(domain.CodeProtocolInvalid,
			"认证服务器返回的不是 JSON 对象")
	}
	return value, KindObject, nil
}

// unwrapCallback strips a JSONP wrapper when there is one that this
// understands. Anything else is handed back unchanged for the JSON decoder to
// judge, so there is one place that decides a body is unparseable.
func unwrapCallback(trimmed []byte) []byte {
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return trimmed
	}

	open := bytes.IndexByte(trimmed, '(')
	if open <= 0 || !isCallbackName(trimmed[:open]) {
		return trimmed
	}

	// One optional statement terminator and trailing whitespace are allowed,
	// because real gateways send both. Anything after the closing bracket is
	// left in place: the decoder then refuses it as trailing content, which is
	// what stops appended code from being ignored.
	tail := bytes.TrimRight(trimmed[open+1:], " \t\r\n")
	tail = bytes.TrimSuffix(tail, []byte(";"))
	tail = bytes.TrimRight(tail, " \t\r\n")
	if len(tail) == 0 || tail[len(tail)-1] != ')' {
		return trimmed
	}
	return tail[:len(tail)-1]
}

// isCallbackName reports whether name could be a JavaScript identifier path.
// It is a shape check, not a lookup: the name is never executed, and the point
// is to refuse a body that merely happens to contain a bracket.
func isCallbackName(name []byte) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for index, symbol := range name {
		switch {
		case symbol >= 'a' && symbol <= 'z',
			symbol >= 'A' && symbol <= 'Z',
			symbol == '_', symbol == '$':
		case index > 0 && (symbol >= '0' && symbol <= '9' || symbol == '.'):
		default:
			return false
		}
	}
	return true
}

// decodeSingleJSONValue parses exactly one JSON value and refuses anything
// after it. That trailing check is what rejects a payload with code appended.
func decodeSingleJSONValue(payload []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("srun: trailing content after the JSON value")
	}
	return value, nil
}

func looksLikeHTML(trimmed []byte) bool {
	head := trimmed
	if len(head) > htmlSniffLimit {
		head = head[:htmlSniffLimit]
	}
	lowered := bytes.ToLower(head)
	for _, marker := range htmlMarkers {
		if bytes.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func classifyUnparseable(trimmed []byte) ResponseKind {
	if looksLikeHTML(trimmed) {
		return KindHTML
	}
	return KindMalformed
}

// htmlOrProtocolError picks the code. A portal page is its own failure: it
// means the request never reached the authentication API, which the user can
// act on, while a malformed answer cannot be distinguished from a broken
// gateway.
func htmlOrProtocolError(trimmed []byte) error {
	if looksLikeHTML(trimmed) {
		return domain.Errorf(domain.CodePortalHTMLResponse,
			"认证服务器返回了网页而不是认证结果，可能被门户拦截")
	}
	return domain.Errorf(domain.CodeProtocolInvalid,
		"无法解析认证服务器的响应")
}
