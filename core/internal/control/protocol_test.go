package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// hasChinese reports whether a message was written for the user this program
// has. An English sentence here is a message that leaked out of the plumbing.
func hasChinese(message string) bool {
	for _, symbol := range message {
		if symbol > 0x4e00 && symbol < 0x9fff {
			return true
		}
	}
	return false
}

// A timeout is not a bad request. Reporting one as InvalidArgument tells the
// user to fix input that was fine, and marks a failure that would pass on its
// own as permanent.
func TestTimeoutsAndCancellationsGetTheirOwnCodes(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		code      domain.ErrorCode
		retryable bool
	}{
		{"a handler that ran out of time", context.DeadlineExceeded,
			domain.CodeDeadlineExceeded, true},
		{"a socket read that ran out of time", os.ErrDeadlineExceeded,
			domain.CodeDeadlineExceeded, true},
		{"a cancelled call", context.Canceled, domain.CodeCancelled, false},
		{"a deadline wrapped with context", fmt.Errorf("等待动作完成: %w", context.DeadlineExceeded),
			domain.CodeDeadlineExceeded, true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := NewErrorPayload(testCase.err)
			if payload.Code != string(testCase.code) {
				t.Fatalf("code = %q, want %q", payload.Code, testCase.code)
			}
			if payload.Retryable != testCase.retryable {
				t.Fatalf("retryable = %v, want %v", payload.Retryable, testCase.retryable)
			}
			if !hasChinese(payload.Message) {
				t.Fatalf("message %q was not written for a user", payload.Message)
			}
		})
	}
}

// An error nobody classified is a fault inside the daemon. Its text was never
// written for a user and never checked for secrets, so it does not go out.
func TestAnUnclassifiedErrorDoesNotPutItsTextOnTheWire(t *testing.T) {
	payload := NewErrorPayload(errors.New(
		"Post \"http://teacher:hunter2@10.0.0.2/cgi-bin/srun_portal\": connection refused"))

	for _, secret := range []string{"hunter2", "10.0.0.2", "srun_portal", "connection refused"} {
		if strings.Contains(payload.Message, secret) {
			t.Errorf("message %q carries %q out of an error that was never redacted",
				payload.Message, secret)
		}
	}
	if !hasChinese(payload.Message) {
		t.Errorf("message %q was not written for a user", payload.Message)
	}
	if payload.Code == string(domain.CodeInvalidArgument) {
		t.Error("a fault inside the daemon was reported as a bad request, which " +
			"tells the user to correct input that was already correct")
	}
	if payload.Retryable {
		t.Error("an unclassified fault is not something to retry unchanged")
	}
}

// Handlers add context with %w. The code has to survive that, or a wrapped
// Conflict arrives as a generic failure and the page stops offering the one
// thing that resolves it.
func TestAWrappedDomainErrorKeepsItsCodeAndMessage(t *testing.T) {
	inner := domain.FieldErrorf(domain.CodeConflict, "revision", "配置已被其他地方修改")
	payload := NewErrorPayload(fmt.Errorf("保存配置: %w", inner))

	if payload.Code != string(domain.CodeConflict) {
		t.Fatalf("code = %q, want Conflict", payload.Code)
	}
	if !strings.Contains(payload.Message, "已被其他地方修改") {
		t.Fatalf("message = %q, want the wrapped error's own message", payload.Message)
	}
	if payload.Details["field"] != "revision" {
		t.Fatalf("details = %v, want the field from the wrapped error", payload.Details)
	}
}

// Config validation reports every problem in one pass. The envelope must not
// quietly reduce that to the first one: the form would then reveal its mistakes
// one save at a time.
func TestAnErrorSetReportsEverythingItCollected(t *testing.T) {
	set := &domain.Errors{Code: domain.CodeInvalidConfig}
	set.Addf("retry.max_seconds", "不能小于 initial_seconds")
	set.Addf("checks.interval_seconds", "必须在 1—3600 之间")

	payload := NewErrorPayload(set)

	if payload.Code != string(domain.CodeInvalidConfig) {
		t.Fatalf("code = %q, want InvalidConfig", payload.Code)
	}
	for _, field := range []string{"retry.max_seconds", "checks.interval_seconds"} {
		if !strings.Contains(payload.Message, field) {
			t.Errorf("message %q lost the problem with %q", payload.Message, field)
		}
	}
	if payload.Details["field"] != "retry.max_seconds" {
		t.Fatalf("details = %v, want the first attributable field", payload.Details)
	}
}

// A client must not have to tell "no diagnostics" apart from "an empty bag of
// diagnostics".
func TestDetailsAreAbsentRatherThanEmpty(t *testing.T) {
	encoded, err := json.Marshal(NewErrorPayload(domain.Errorf(domain.CodeBusy, "队列已满")))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "details") {
		t.Fatalf("payload = %s, want no details key at all", encoded)
	}
}

// Every failure carries a code a client can branch on. An empty one would be
// worse than a wrong one: nothing could match it.
func TestEveryFailureCarriesACode(t *testing.T) {
	for _, err := range []error{
		&domain.Errors{Items: []*domain.Error{{Message: "没有代码"}}},
		&domain.Error{Message: "没有代码"},
		errors.New("from somewhere else"),
	} {
		if code := NewErrorPayload(err).Code; code == "" {
			t.Errorf("%v produced an empty code", err)
		}
	}
}

func TestNewSuccessRefusesAResultItCannotEncode(t *testing.T) {
	if _, err := NewSuccess("r", make(chan int)); err == nil {
		t.Fatal("a result that cannot be encoded was reported as a success")
	}
}

// A handler returning something unencodable is a bug, but the caller is waiting
// for one line and must get a failure it can read, not a panic or half a frame.
func TestDispatchTurnsAnUnencodableResultIntoAFailure(t *testing.T) {
	registry := registryWith(t, "status.get",
		func(context.Context, json.RawMessage) (any, error) { return make(chan int), nil })

	response := registry.Dispatch(t.Context(),
		Request{RPCVersion: Version, RequestID: "r", Method: "status.get"})

	if response.OK {
		t.Fatal("an unencodable result was reported as a success")
	}
	if string(response.Result) != "null" {
		t.Fatalf("result = %s, want null", response.Result)
	}
}

// Catalogue() hands out a copy. A caller that sorts or trims what it got back
// must not be editing the protocol's surface.
func TestCatalogueIsSortedAndIsACopy(t *testing.T) {
	listing := Catalogue()
	if len(listing) != len(MethodNames()) {
		t.Fatalf("Catalogue has %d entries, MethodNames has %d",
			len(listing), len(MethodNames()))
	}
	if !slices.IsSortedFunc(listing, func(a, b Method) int {
		return strings.Compare(a.Name, b.Name)
	}) {
		t.Fatal("Catalogue is not sorted by name")
	}

	listing[0].Name = "shell.exec"
	if _, declared := Lookup("shell.exec"); declared {
		t.Fatal("editing the returned slice changed what the protocol accepts")
	}
	if Catalogue()[0].Name == "shell.exec" {
		t.Fatal("Catalogue handed out its own backing array")
	}
}

// The catalogue is checked at startup rather than described in a comment. These
// are the shapes that check has to refuse.
func TestCatalogueValidationRefusesMalformedEntries(t *testing.T) {
	for _, broken := range []Method{
		{"nodot", KindRead, ""},
		{".action", KindRead, ""},
		{"family.", KindRead, ""},
		{"family.action", Kind("write"), ""},
		{"family.action", Kind(""), ""},
	} {
		if err := validateCatalogue([]Method{broken}); err == nil {
			t.Errorf("%+v was accepted", broken)
		}
	}

	if err := validateCatalogue([]Method{
		{"status.get", KindRead, ""},
		{"status.get", KindRead, ""},
	}); err == nil {
		t.Error("a duplicate method name was accepted")
	}

	if err := validateCatalogue(catalogue); err != nil {
		t.Errorf("the shipped catalogue does not pass its own check: %v", err)
	}
}
