//go:build unix

package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// answering starts a socket that runs answer for one connection.
//
// A hand-rolled server rather than the real one, because these are the cases
// the real one cannot produce: an id that does not match, a version from the
// future, a response longer than the limit. A client that only ever talks to a
// correct server is a client whose checks have never run.
func answering(t *testing.T, answer func(request Request) []byte) Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var served sync.WaitGroup
	served.Go(func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		line, err := readLine(reader, MaxRequestBytes)
		if err != nil {
			return
		}
		var request Request
		_ = json.Unmarshal(line, &request)
		_, _ = conn.Write(append(answer(request), '\n'))
	})
	t.Cleanup(func() {
		listener.Close()
		served.Wait()
	})
	return Client{Path: path}
}

func mustEncode(t *testing.T, response Response) []byte {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return encoded
}

// Nothing listening is the ordinary "it is not running" case, and it has its
// own code so the CLI can exit 3 rather than report a transport fault the user
// cannot act on.
func TestCallingAServiceThatIsNotThereIsServiceStopped(t *testing.T) {
	client := Client{Path: filepath.Join(t.TempDir(), "absent.sock")}

	_, err := client.Call(t.Context(), "version.get", nil)
	if err == nil {
		t.Fatal("a call to nothing succeeded")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeServiceStopped {
		t.Errorf("code = %s, want ServiceStopped", code)
	}
}

// A result credited to a call that did not ask for it is the one thing the id
// check exists to prevent.
func TestAResultForSomebodyElsesRequestIsRefused(t *testing.T) {
	client := answering(t, func(Request) []byte {
		return mustEncode(t, Response{RPCVersion: Version,
			RequestID: "req-somebody-else", OK: true,
			Result: json.RawMessage(`{"version":"9.9.9"}`)})
	})

	_, err := client.Call(t.Context(), "version.get", nil)
	if err == nil {
		t.Fatal("a result with the wrong id was accepted")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid {
		t.Errorf("code = %s, want ProtocolInvalid", code)
	}
}

// A refusal issued before the request was read has no id to echo -- the daemon
// is at its connection cap, or the caller is not entitled to the socket. It is
// still an answer, and dropping it would turn "too busy" into "the service died
// mid-request".
func TestARefusalWithNoRequestIDIsStillDelivered(t *testing.T) {
	client := answering(t, func(Request) []byte {
		return mustEncode(t, NewFailure("", domain.Errorf(domain.CodeBusy,
			"同时处理的本地请求过多")))
	})

	_, err := client.Call(t.Context(), "version.get", nil)
	if err == nil {
		t.Fatal("a refusal was read as success")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeBusy {
		t.Errorf("code = %s, want Busy", code)
	}
}

func TestAResponseFromAnotherProtocolVersionIsRefused(t *testing.T) {
	client := answering(t, func(request Request) []byte {
		return mustEncode(t, Response{RPCVersion: Version + 1,
			RequestID: request.RequestID, OK: true,
			Result: json.RawMessage(`{}`)})
	})

	_, err := client.Call(t.Context(), "version.get", nil)
	if err == nil {
		t.Fatal("a response from another protocol version was accepted")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid {
		t.Errorf("code = %s, want ProtocolInvalid", code)
	}
}

// The response limit is enforced while reading, so an oversized answer costs
// one byte past the limit rather than all of it.
func TestAnOversizedResponseIsRefused(t *testing.T) {
	client := answering(t, func(Request) []byte {
		return []byte(strings.Repeat("x", MaxResponseBytes+1))
	})

	_, err := client.Call(t.Context(), "version.get", nil)
	if err == nil {
		t.Fatal("an oversized response was read")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeServiceStopped {
		t.Errorf("code = %s, want the read to fail", code)
	}
}

// Nonsense on the wire is refused rather than half-parsed.
func TestAMalformedResponseIsRefused(t *testing.T) {
	client := answering(t, func(Request) []byte { return []byte("not json") })

	_, err := client.Call(t.Context(), "version.get", nil)
	if err == nil {
		t.Fatal("a malformed response was accepted")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid {
		t.Errorf("code = %s, want ProtocolInvalid", code)
	}
}

// A failure envelope with nothing in it is still a failure. Reporting success
// because the server forgot to explain itself would be the worst of the three
// possible answers.
func TestAnEmptyFailureEnvelopeIsStillAFailure(t *testing.T) {
	client := answering(t, func(request Request) []byte {
		return mustEncode(t, Response{RPCVersion: Version,
			RequestID: request.RequestID, OK: false})
	})

	_, err := client.Call(t.Context(), "version.get", nil)
	if err == nil {
		t.Fatal("ok=false with no error payload was read as success")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeInternal {
		t.Errorf("code = %s, want Internal", code)
	}
}

// The request carries what the protocol requires, and the ids do not repeat:
// they are what correlates a log line with a call.
func TestTheRequestIsWellFormedAndItsIDIsFresh(t *testing.T) {
	seen := make(chan Request, 2)
	answer := func(request Request) []byte {
		seen <- request
		return mustEncode(t, Response{RPCVersion: Version,
			RequestID: request.RequestID, OK: true, Result: json.RawMessage(`1`)})
	}

	first := answering(t, answer)
	if _, err := first.Call(t.Context(), "version.get",
		map[string]string{"hello": "world"}); err != nil {
		t.Fatalf("call: %v", err)
	}
	second := answering(t, answer)
	if _, err := second.Call(t.Context(), "version.get", nil); err != nil {
		t.Fatalf("call: %v", err)
	}

	one, two := <-seen, <-seen
	if one.RPCVersion != Version || one.Method != "version.get" {
		t.Errorf("request = %+v", one)
	}
	if string(one.Params) != `{"hello":"world"}` {
		t.Errorf("params = %s", one.Params)
	}
	if len(two.Params) != 0 {
		t.Errorf("a call with no parameters sent %s", two.Params)
	}
	if one.RequestID == "" || one.RequestID == two.RequestID {
		t.Errorf("request ids repeat: %q and %q", one.RequestID, two.RequestID)
	}
}

// Parameters that cannot be encoded are a programming mistake caught before
// anything is sent, not a socket error to puzzle over afterwards.
func TestUnencodableParametersFailBeforeDialing(t *testing.T) {
	client := Client{Path: filepath.Join(t.TempDir(), "never-dialled.sock")}

	_, err := client.Call(t.Context(), "version.get", make(chan int))
	if err == nil {
		t.Fatal("unencodable parameters were sent")
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
		t.Errorf("code = %s, want InvalidArgument", code)
	}
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Error("the original encoding failure was not kept as the cause")
	}
}
