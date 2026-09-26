package control

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type rwStream struct {
	in  *bytes.Reader
	out bytes.Buffer
}

func newStream(request string) *rwStream {
	return &rwStream{in: bytes.NewReader([]byte(request))}
}

func (s *rwStream) Read(p []byte) (int, error)  { return s.in.Read(p) }
func (s *rwStream) Write(p []byte) (int, error) { return s.out.Write(p) }

func (s *rwStream) response(t *testing.T) Response {
	t.Helper()
	frame := bytes.TrimRight(s.out.Bytes(), "\n")
	if len(frame) == 0 {
		t.Fatal("no response was written")
	}
	var decoded Response
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("response does not parse: %v (%q)", err, frame)
	}
	return decoded
}

func registryWith(t *testing.T, name string, handler Handler) *Registry {
	t.Helper()
	registry := NewRegistry()
	registry.Register(name, handler)
	return registry
}

func TestServeConnAnswersOneRequest(t *testing.T) {
	registry := registryWith(t, "version.get",
		func(context.Context, json.RawMessage) (any, error) {
			return map[string]string{"version": "2.0.0rc1"}, nil
		})
	stream := newStream(`{"rpc_version":1,"request_id":"req-7","method":"version.get"}` + "\n")

	if err := ServeConn(t.Context(), registry, stream); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	response := stream.response(t)
	if !response.OK || response.RequestID != "req-7" {
		t.Fatalf("response = %+v", response)
	}
	if response.RPCVersion != Version {
		t.Fatalf("rpc_version = %d", response.RPCVersion)
	}
	if response.Error != nil {
		t.Fatalf("error = %+v on a success", response.Error)
	}
}

// The request id is echoed so a caller with several in flight can match the
// reply to what it asked.
func TestServeConnEchoesTheRequestID(t *testing.T) {
	registry := registryWith(t, "version.get",
		func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	stream := newStream(`{"rpc_version":1,"request_id":"click-abc-123","method":"version.get"}` + "\n")

	if err := ServeConn(t.Context(), registry, stream); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	if got := stream.response(t).RequestID; got != "click-abc-123" {
		t.Fatalf("request_id = %q", got)
	}
}

// A malformed request still gets an answer: the peer is waiting for exactly one
// line, and dropping the connection would leave it to time out knowing nothing.
func TestServeConnAnswersEvenWhenTheRequestCannotBeParsed(t *testing.T) {
	registry := NewRegistry()
	stream := newStream("not json at all\n")

	if err := ServeConn(t.Context(), registry, stream); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	response := stream.response(t)
	if response.OK {
		t.Fatal("a malformed request was answered with a success")
	}
	if response.Error.Code != string(domain.CodeInvalidArgument) {
		t.Fatalf("code = %q", response.Error.Code)
	}
}

// A peer that connects and leaves gets no reply and produces no error: there is
// nobody to tell.
func TestServeConnIgnoresACleanDisconnect(t *testing.T) {
	stream := newStream("")
	if err := ServeConn(t.Context(), NewRegistry(), stream); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	if stream.out.Len() != 0 {
		t.Fatalf("wrote %q to a peer that had gone", stream.out.Bytes())
	}
}

func TestDispatchDistinguishesUnknownFromUnimplemented(t *testing.T) {
	registry := NewRegistry()

	unknown := registry.Dispatch(t.Context(),
		Request{RPCVersion: Version, RequestID: "r", Method: "config.destroy"})
	if unknown.Error.Code != string(domain.CodeNotFound) {
		t.Fatalf("undeclared method -> %q, want NotFound", unknown.Error.Code)
	}

	// Declared but not bound in this build. Telling the caller it does not
	// exist would be wrong: it does, and a later build will serve it.
	declared := registry.Dispatch(t.Context(),
		Request{RPCVersion: Version, RequestID: "r", Method: "update.start"})
	if declared.Error.Code != string(domain.CodeUnsupportedCapability) {
		t.Fatalf("unbound method -> %q, want UnsupportedCapability", declared.Error.Code)
	}
}

// Only catalogued names can be bound. A registry assembled from whatever
// handlers happened to register would make the wire surface depend on
// initialisation order.
func TestRegisterRefusesUndeclaredAndDuplicateNames(t *testing.T) {
	noop := func(context.Context, json.RawMessage) (any, error) { return nil, nil }

	for _, name := range []string{"config.destroy", "", "shell.exec", "status"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registering %q was allowed", name)
				}
			}()
			NewRegistry().Register(name, noop)
		}()
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("registering the same method twice was allowed")
			}
		}()
		registry := NewRegistry()
		registry.Register("status.get", noop)
		registry.Register("status.get", noop)
	}()

	func() {
		defer func() {
			if recover() == nil {
				t.Error("a nil handler was accepted")
			}
		}()
		NewRegistry().Register("status.get", nil)
	}()
}

// Missing() lets startup refuse to serve a half-wired protocol rather than
// return NotFound for something the catalogue says exists.
func TestRegistryReportsUnboundMethods(t *testing.T) {
	registry := NewRegistry()
	if len(registry.Missing()) != len(MethodNames()) {
		t.Fatalf("an empty registry reported %d missing, want %d",
			len(registry.Missing()), len(MethodNames()))
	}

	registry.Register("status.get", func(context.Context, json.RawMessage) (any, error) {
		return nil, nil
	})
	if slices.Contains(registry.Missing(), "status.get") {
		t.Fatal("a bound method was still reported missing")
	}
}

type sampleParams struct {
	AccountID string `json:"account_id"`
	Force     bool   `json:"force"`
}

// Unknown parameters are refused. Otherwise a caller could attach a parameter
// one method honours to another that silently ignores it.
func TestDecodeParamsRefusesUnknownFields(t *testing.T) {
	var params sampleParams
	if err := DecodeParams(json.RawMessage(`{"account_id":"c1","force":true}`), &params); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	if params.AccountID != "c1" || !params.Force {
		t.Fatalf("params = %+v", params)
	}

	for _, raw := range []string{
		`{"account_id":"c1","run_as":"root"}`,
		`{"account_id":123}`,
		`{"account_id":"c1"} {"account_id":"c2"}`,
		`[1,2,3]`,
	} {
		var target sampleParams
		if err := DecodeParams(json.RawMessage(raw), &target); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}

	// Absent parameters are not an error: a read method may take none.
	var empty sampleParams
	if err := DecodeParams(nil, &empty); err != nil {
		t.Errorf("absent params rejected: %v", err)
	}
	if err := DecodeParams(json.RawMessage(`null`), &empty); err != nil {
		t.Errorf("null params rejected: %v", err)
	}
}

// A handler's error reaches the caller as a stable code and a Chinese message,
// with the internal cause left behind.
func TestHandlerErrorsBecomeTheWireEnvelope(t *testing.T) {
	registry := registryWith(t, "config.apply",
		func(context.Context, json.RawMessage) (any, error) {
			return nil, domain.FieldErrorf(domain.CodeConflict, "revision",
				"配置已被其他地方修改，请重新加载后再保存")
		})

	response := registry.Dispatch(t.Context(),
		Request{RPCVersion: Version, RequestID: "r", Method: "config.apply"})

	if response.OK {
		t.Fatal("a failing handler produced a success")
	}
	if string(response.Result) != "null" {
		t.Fatalf("result = %s, want null so no half-answer can be read", response.Result)
	}
	if response.Error.Code != string(domain.CodeConflict) {
		t.Fatalf("code = %q", response.Error.Code)
	}
	if !strings.Contains(response.Error.Message, "重新加载") {
		t.Fatalf("message = %q, want the Chinese guidance", response.Error.Message)
	}
	if response.Error.Details["field"] != "revision" {
		t.Fatalf("details = %v", response.Error.Details)
	}
	// A conflict needs the caller to re-read first, so retrying the same
	// request unchanged would just fail again.
	if response.Error.Retryable {
		t.Fatal("Conflict was marked retryable")
	}
}

func TestRetryabilityComesFromTheCodeNotTheTransport(t *testing.T) {
	retryable := []domain.ErrorCode{
		domain.CodeBusy, domain.CodeDeadlineExceeded, domain.CodeServiceStopped,
		domain.CodeBindingUnavailable, domain.CodeBindingChanged,
		domain.CodeDNSFailure, domain.CodeTransportFailure, domain.CodeTLSFailure,
	}
	permanent := []domain.ErrorCode{
		domain.CodeInvalidArgument, domain.CodeInvalidConfig, domain.CodeConflict,
		domain.CodeNotFound, domain.CodeCancelled, domain.CodeAuthRejected,
		domain.CodeProtocolInvalid, domain.CodeOnlineIdentityMismatch,
		domain.CodeRecoveryRequired, domain.CodeUnsupportedCapability,
		domain.CodeChecksumMismatch, domain.CodePackageIncompatible,
		domain.CodeInstallFailed, domain.CodePortalHTMLResponse,
		domain.CodeInternal,
	}
	for _, code := range retryable {
		if !Retryable(code) {
			t.Errorf("%s should be retryable", code)
		}
	}
	for _, code := range permanent {
		if Retryable(code) {
			t.Errorf("%s should not be retryable", code)
		}
	}
}

// Details is a whitelist, so no call site anywhere can leak a secret by
// attaching it as context.
func TestErrorDetailsAreWhitelisted(t *testing.T) {
	payload := &ErrorPayload{Details: sanitizeDetails(map[string]string{
		"field":      "campus_accounts[0].password",
		"password":   "hunter2",
		"base_url":   "http://user:pass@gateway/",
		"mac":        "aa:bb:cc:dd:ee:ff",
		"action_id":  "a1",
		"stacktrace": "goroutine 1 ...",
	})}

	if payload.Details["field"] != "campus_accounts[0].password" {
		t.Fatal("a whitelisted key was dropped")
	}
	if payload.Details["action_id"] != "a1" {
		t.Fatal("action_id was dropped")
	}
	for _, forbidden := range []string{"password", "base_url", "mac", "stacktrace"} {
		if _, present := payload.Details[forbidden]; present {
			t.Errorf("%q survived the whitelist", forbidden)
		}
	}

	// Nothing allowed through means no map, not an empty one, so a client never
	// has to tell "no diagnostics" from "an empty bag of diagnostics".
	if got := sanitizeDetails(map[string]string{"password": "hunter2"}); got != nil {
		t.Errorf("sanitizeDetails = %v, want nil when nothing is on the whitelist", got)
	}
}

// The wrapped cause is for the daemon's own log, not for a browser.
func TestWireErrorDoesNotCarryTheInternalCause(t *testing.T) {
	err := domain.Errorf(domain.CodeTransportFailure, "无法连接认证服务器").
		Wrap(errNetwork{})
	payload := NewErrorPayload(err)

	if strings.Contains(payload.Message, "10.0.0.2") {
		t.Fatalf("message leaked the cause: %q", payload.Message)
	}
	if !payload.Retryable {
		t.Error("a transport failure should be retryable")
	}
}

type errNetwork struct{}

func (errNetwork) Error() string { return "dial tcp 10.0.0.2:80: connection refused" }

// The catalogue is the contract. Losing an entry would quietly remove a method
// the UI or CLI depends on.
func TestCatalogueCoversTheDeclaredMethodFamilies(t *testing.T) {
	names := MethodNames()
	for _, required := range []string{
		"status.get", "schema.get", "capabilities.get", "version.get",
		"config.get", "config.validate", "config.apply", "config.export", "config.import",
		"campus.get", "campus.upsert", "campus.remove", "campus.set_default",
		"hotspot.get", "hotspot.upsert", "hotspot.remove", "hotspot.set_default",
		"action.submit", "action.get", "action.cancel",
		"presets.list", "presets.refresh", "user_presets.get", "user_presets.set",
		"detect.environment", "detect.acid", "detect.operators",
		"detect.identity", "detect.verify",
		"setup_wifi.start", "setup_wifi.status", "setup_wifi.cancel",
		"setup_wifi.commit", "setup_wifi.account",
		"log.tail", "log.download", "log.clear",
		"update.check", "update.start", "update.status",
		"schools.list", "schools.inspect", "school.command",
	} {
		if !slices.Contains(names, required) {
			t.Errorf("catalogue is missing %q", required)
		}
	}
	if len(names) != 43 {
		t.Errorf("catalogue has %d methods, expected the 43 from the contract (D75 backups); "+
			"adding one needs a decision record", len(names))
	}
}

// Read methods are what the page polls. One that mutated or probed would turn
// an idle browser tab into traffic on the campus gateway.
func TestPolledMethodsAreDeclaredRead(t *testing.T) {
	for _, name := range []string{
		"status.get", "log.tail", "update.status", "action.get", "setup_wifi.status",
	} {
		method, ok := Lookup(name)
		if !ok {
			t.Fatalf("%q is not in the catalogue", name)
		}
		if method.Kind != KindRead {
			t.Errorf("%q is %q; the UI polls it, so it must be read-only",
				name, method.Kind)
		}
	}
}
