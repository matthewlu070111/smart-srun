package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ErrClosed reports that the peer went away before sending a complete frame.
// It is not an error to report to a user: nobody is left to read it.
var ErrClosed = errors.New("control: peer closed before a complete frame")

// ReadRequest reads exactly one newline-terminated JSON request.
//
// The three hard parts of a line protocol, handled explicitly:
//
//   - A short read is not a frame. bufio.Reader.ReadSlice returns what it has
//     when the buffer fills; the loop continues until a newline actually
//     arrives, so a request split across packets is assembled, not rejected.
//   - A missing newline is not a frame either. EOF with bytes buffered means a
//     truncated request, which must not be parsed as if it were complete.
//   - The limit is enforced while reading, not after. A peer that never sends a
//     newline must not be able to make the daemon buffer an unbounded line.
func ReadRequest(reader *bufio.Reader, limit int) (Request, error) {
	line, err := readLine(reader, limit)
	if err != nil {
		return Request{}, err
	}
	return DecodeRequest(line)
}

func readLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var buffer bytes.Buffer
	for {
		chunk, err := reader.ReadSlice('\n')
		if buffer.Len()+len(chunk) > limit {
			return nil, domain.Errorf(domain.CodeInvalidArgument,
				"请求超过 %d KiB 上限", limit/1024)
		}
		buffer.Write(chunk)

		switch {
		case err == nil:
			return bytes.TrimRight(buffer.Bytes(), "\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			// Partial frame: keep reading.
			continue
		case errors.Is(err, io.EOF):
			if buffer.Len() == 0 {
				return nil, ErrClosed
			}
			return nil, domain.Errorf(domain.CodeInvalidArgument,
				"请求在换行符之前结束；一个请求必须是一行 JSON 加换行")
		default:
			return nil, err
		}
	}
}

// DecodeRequest parses one frame's bytes.
//
// The version is checked before the body is interpreted: a client speaking a
// future protocol must be told so, not served a best-effort reading of fields
// that happen to have the same names.
func DecodeRequest(line []byte) (Request, error) {
	if !utf8.Valid(line) {
		return Request{}, domain.Errorf(domain.CodeInvalidArgument,
			"请求不是有效的 UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, domain.Errorf(domain.CodeInvalidArgument,
			"请求不是有效的 JSON 对象").Wrap(err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Request{}, domain.Errorf(domain.CodeInvalidArgument,
			"一行里只能有一个请求对象")
	}

	if request.RPCVersion != Version {
		return Request{}, domain.Errorf(domain.CodeUnsupportedCapability,
			"不支持的协议版本 %d，本服务只接受 %d", request.RPCVersion, Version)
	}
	if request.RequestID == "" {
		return Request{}, domain.Errorf(domain.CodeInvalidArgument,
			"request_id 不能为空")
	}
	if len(request.RequestID) > 128 {
		return Request{}, domain.Errorf(domain.CodeInvalidArgument,
			"request_id 过长")
	}
	if request.Method == "" {
		return Request{}, domain.Errorf(domain.CodeInvalidArgument,
			"method 不能为空")
	}
	return request, nil
}

// WriteResponse writes one frame.
//
// A response that would exceed the limit is replaced by an error response
// rather than truncated: half a JSON object on the wire desynchronises the
// peer, and a client that read it would act on a partial answer.
func WriteResponse(writer io.Writer, response Response) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		encoded, err = json.Marshal(NewFailure(response.RequestID,
			domain.Errorf(domain.CodeInvalidArgument, "响应无法序列化")))
		if err != nil {
			return err
		}
	}
	if len(encoded)+1 > MaxResponseBytes {
		encoded, err = json.Marshal(NewFailure(response.RequestID,
			domain.Errorf(domain.CodeInvalidArgument,
				"响应超过 %d KiB 上限", MaxResponseBytes/1024)))
		if err != nil {
			return err
		}
	}
	// encoding/json escapes control characters inside strings, so a marshalled
	// object never contains a raw newline. Asserting it here means a future
	// change to custom marshalling cannot silently break framing.
	if bytes.ContainsRune(encoded, '\n') {
		return domain.Errorf(domain.CodeInvalidArgument,
			"响应内部含有未转义换行，会破坏分帧")
	}

	// io.Writer may write short. Loop until the whole frame is out, so a
	// partially written response is an error rather than a silent truncation.
	frame := append(encoded, '\n')
	for len(frame) > 0 {
		written, err := writer.Write(frame)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}
