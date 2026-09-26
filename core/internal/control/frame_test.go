package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// dribbleReader hands out at most n bytes per Read, so a frame arrives split
// across many reads the way it does on a real socket.
type dribbleReader struct {
	data  []byte
	chunk int
}

func (d *dribbleReader) Read(p []byte) (int, error) {
	if len(d.data) == 0 {
		return 0, io.EOF
	}
	size := min(min(d.chunk, len(p)), len(d.data))
	copy(p, d.data[:size])
	d.data = d.data[size:]
	return size, nil
}

func validRequestLine(t *testing.T, method string, params any) []byte {
	t.Helper()
	request := map[string]any{
		"rpc_version": Version,
		"request_id":  "req-1",
		"method":      method,
	}
	if params != nil {
		request["params"] = params
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(encoded, '\n')
}

// T38 -- a request split across reads is one request, not a parse error.
func TestReadRequestReassemblesPartialFrames(t *testing.T) {
	line := validRequestLine(t, "status.get", nil)

	for _, chunk := range []int{1, 3, 7, len(line) - 1} {
		reader := bufio.NewReaderSize(&dribbleReader{data: line, chunk: chunk}, 16)
		request, err := ReadRequest(reader, MaxRequestBytes)
		if err != nil {
			t.Fatalf("chunk %d: %v", chunk, err)
		}
		if request.Method != "status.get" || request.RequestID != "req-1" {
			t.Fatalf("chunk %d: %+v", chunk, request)
		}
	}
}

// A frame without its newline is incomplete. Parsing it anyway would act on a
// request the peer had not finished sending.
func TestReadRequestRejectsAFrameWithNoNewline(t *testing.T) {
	line := bytes.TrimRight(validRequestLine(t, "status.get", nil), "\n")
	reader := bufio.NewReader(bytes.NewReader(line))

	_, err := ReadRequest(reader, MaxRequestBytes)
	if err == nil {
		t.Fatal("a request with no newline was accepted")
	}
	if !strings.Contains(err.Error(), "换行") {
		t.Fatalf("error %q does not explain the framing rule", err)
	}
}

// A peer that connects and leaves is not an error to report: nobody is waiting.
func TestReadRequestReportsACleanDisconnectDistinctly(t *testing.T) {
	reader := bufio.NewReader(bytes.NewReader(nil))
	_, err := ReadRequest(reader, MaxRequestBytes)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

// The limit stops the read. A peer that never sends a newline must not be able
// to make the daemon buffer an unbounded line first and check afterwards.
//
// The over-long frame is deliberately *valid* JSON: rejecting garbage proves
// nothing about the limit, since the parser would reject it anyway. The error
// has to be about the size.
func TestReadRequestEnforcesTheLimitWhileReading(t *testing.T) {
	padding := strings.Repeat("x", 8192)
	line := []byte(`{"rpc_version":1,"request_id":"r","method":"status.get",` +
		`"params":{"note":"` + padding + `"}}` + "\n")
	reader := bufio.NewReaderSize(bytes.NewReader(line), 64)

	_, err := ReadRequest(reader, 1024)
	if err == nil {
		t.Fatal("an over-long request was accepted")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("error %q is not about the size limit; a well-formed frame "+
			"over the limit must be refused for its size", err)
	}
	if code, _ := domain.CodeOf(err); code != domain.CodeInvalidArgument {
		t.Fatalf("code = %q, want InvalidArgument", code)
	}

	// A peer that never sends a newline at all must also be stopped.
	endless := bytes.Repeat([]byte("x"), 8192)
	if _, err := ReadRequest(bufio.NewReaderSize(bytes.NewReader(endless), 64), 1024); err == nil {
		t.Fatal("a frame with no newline and no end was accepted")
	}
}

func TestDecodeRequestRejectsMalformedFrames(t *testing.T) {
	cases := []struct {
		name string
		line string
		code domain.ErrorCode
	}{
		{"future protocol version", `{"rpc_version":2,"request_id":"r","method":"status.get"}`,
			domain.CodeUnsupportedCapability},
		{"missing protocol version", `{"request_id":"r","method":"status.get"}`,
			domain.CodeUnsupportedCapability},
		{"unknown envelope field", `{"rpc_version":1,"request_id":"r","method":"status.get","as_root":true}`,
			domain.CodeInvalidArgument},
		{"empty request id", `{"rpc_version":1,"request_id":"","method":"status.get"}`,
			domain.CodeInvalidArgument},
		{"empty method", `{"rpc_version":1,"request_id":"r","method":""}`,
			domain.CodeInvalidArgument},
		{"two objects on one line", `{"rpc_version":1,"request_id":"r","method":"status.get"}{"rpc_version":1}`,
			domain.CodeInvalidArgument},
		{"not an object", `["status.get"]`, domain.CodeInvalidArgument},
		{"not JSON", `status.get`, domain.CodeInvalidArgument},
		{"empty line", ``, domain.CodeInvalidArgument},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(testCase.line))
			if err == nil {
				t.Fatal("accepted")
			}
			code, ok := domain.CodeOf(err)
			if !ok || code != testCase.code {
				t.Fatalf("code = %q, want %q (err: %v)", code, testCase.code, err)
			}
		})
	}
}

// The request id is echoed into every response and every log line about the
// call. An unbounded one lets a sender decide how large those become.
func TestDecodeRequestRejectsAnOverlongRequestID(t *testing.T) {
	atTheLimit := `{"rpc_version":1,"request_id":"` + strings.Repeat("r", 128) +
		`","method":"status.get"}`
	if _, err := DecodeRequest([]byte(atTheLimit)); err != nil {
		t.Fatalf("128 characters is the limit, not one over it: %v", err)
	}

	overIt := `{"rpc_version":1,"request_id":"` + strings.Repeat("r", 129) +
		`","method":"status.get"}`
	if _, err := DecodeRequest([]byte(overIt)); err == nil {
		t.Fatal("a 129-character request id was accepted")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) {
	return 0, errors.New("read unix @->/var/run: connection reset by peer")
}

// A connection that broke is not a malformed request. Dressing it up as one
// would blame the sender for the network dropping under it.
func TestReadRequestPropagatesATransportError(t *testing.T) {
	_, err := ReadRequest(bufio.NewReader(brokenReader{}), MaxRequestBytes)
	if err == nil {
		t.Fatal("a broken connection was read as a frame")
	}
	if errors.Is(err, ErrClosed) {
		t.Fatal("a reset connection was reported as a clean disconnect")
	}
	if _, classified := domain.CodeOf(err); classified {
		t.Fatalf("err = %v; a transport failure must reach the caller as itself, "+
			"not as a protocol complaint", err)
	}
}

func TestDecodeRequestRejectsInvalidUTF8(t *testing.T) {
	line := []byte(`{"rpc_version":1,"request_id":"r","method":"status.get"}`)
	line[20] = 0xff
	if _, err := DecodeRequest(line); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}

// A response must be exactly one line: a raw newline inside would desynchronise
// the peer, which reads until the next newline.
func TestWriteResponseNeverEmitsARawNewlineInsideTheFrame(t *testing.T) {
	response, err := NewSuccess("req-1", map[string]string{
		"message": "第一行\n第二行\r\n带制表符\t",
	})
	if err != nil {
		t.Fatalf("NewSuccess: %v", err)
	}

	var buffer bytes.Buffer
	if err := WriteResponse(&buffer, response); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	frame := buffer.Bytes()
	if bytes.Count(frame, []byte("\n")) != 1 || frame[len(frame)-1] != '\n' {
		t.Fatalf("frame is not exactly one line: %q", frame)
	}

	var decoded Response
	if err := json.Unmarshal(frame[:len(frame)-1], &decoded); err != nil {
		t.Fatalf("frame does not parse: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(decoded.Result, &payload); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if payload["message"] != "第一行\n第二行\r\n带制表符\t" {
		t.Fatalf("newlines did not survive escaping: %q", payload["message"])
	}
}

// shortWriter accepts one byte per call, like a socket under pressure.
type shortWriter struct{ buffer bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return w.buffer.Write(p[:1])
}

func TestWriteResponseCompletesShortWrites(t *testing.T) {
	response, _ := NewSuccess("req-1", map[string]int{"count": 42})
	writer := &shortWriter{}

	if err := WriteResponse(writer, response); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	frame := writer.buffer.Bytes()
	if frame[len(frame)-1] != '\n' {
		t.Fatalf("frame was truncated by short writes: %q", frame)
	}
	var decoded Response
	if err := json.Unmarshal(frame[:len(frame)-1], &decoded); err != nil {
		t.Fatalf("frame does not parse: %v", err)
	}
}

// An over-sized result becomes an error response, not a truncated one: half a
// JSON object would be read by the peer as a complete answer.
func TestWriteResponseReplacesAnOversizedResultWithAnError(t *testing.T) {
	response, err := NewSuccess("req-1", strings.Repeat("x", MaxResponseBytes))
	if err != nil {
		t.Fatalf("NewSuccess: %v", err)
	}

	var buffer bytes.Buffer
	if err := WriteResponse(&buffer, response); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	if buffer.Len() > MaxResponseBytes {
		t.Fatalf("frame is %d bytes, over the %d limit", buffer.Len(), MaxResponseBytes)
	}

	var decoded Response
	if err := json.Unmarshal(bytes.TrimRight(buffer.Bytes(), "\n"), &decoded); err != nil {
		t.Fatalf("frame does not parse: %v", err)
	}
	if decoded.OK {
		t.Fatal("an over-sized response was reported as a success")
	}
	if string(decoded.Result) != "null" {
		t.Fatalf("result = %s, want null on a failure", decoded.Result)
	}
}

// A Result holding bytes that are not JSON cannot be marshalled. What goes out
// still has to be a complete, parseable failure the caller can match to its
// request.
func TestWriteResponseReplacesAResponseItCannotMarshal(t *testing.T) {
	unencodable := Response{
		RPCVersion: Version,
		RequestID:  "req-1",
		OK:         true,
		Result:     json.RawMessage(`{"unterminated":`),
	}

	var buffer bytes.Buffer
	if err := WriteResponse(&buffer, unencodable); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}

	var decoded Response
	if err := json.Unmarshal(bytes.TrimRight(buffer.Bytes(), "\n"), &decoded); err != nil {
		t.Fatalf("the replacement frame does not parse: %v (%q)", err, buffer.Bytes())
	}
	if decoded.OK {
		t.Fatal("a response that could not be encoded went out as a success")
	}
	if decoded.RequestID != "req-1" {
		t.Fatalf("request_id = %q, want it echoed so the caller can match the failure",
			decoded.RequestID)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("peer gone") }

func TestWriteResponseReportsAPeerThatDisappeared(t *testing.T) {
	response, _ := NewSuccess("req-1", nil)
	if err := WriteResponse(failingWriter{}, response); err == nil {
		t.Fatal("writing to a dead peer reported success")
	}
}
