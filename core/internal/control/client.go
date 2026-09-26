package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// DialTimeout bounds connecting to the socket.
//
// Short on purpose: the peer is on the same machine, so anything slower than
// this is the daemon not being there rather than the network being slow. The
// CLI turns that into "service not running" instead of a pause the user has to
// interpret.
const DialTimeout = 2 * time.Second

// Client speaks the control protocol to a running daemon.
//
// The mirror of ServeConn, and the only client: the CLI and the lifecycle
// helper both go through it, so there is one place that knows the framing, the
// limits and how an error envelope becomes a Go error. A second client would
// eventually disagree with this one about one of the three.
type Client struct {
	// Path is the socket. Empty uses the contract's location.
	Path string
	// Timeout bounds the whole call. Zero uses the read budget the protocol
	// fixes for a response.
	Timeout time.Duration
}

// requestCounter makes request ids unique within a process. They are echoed
// back and used to correlate a log line with a call; they are not secrets and
// nothing authorises on them.
var requestCounter atomic.Uint64

func nextRequestID() string {
	return "req-" + strconv.Itoa(os.Getpid()) + "-" +
		strconv.FormatUint(requestCounter.Add(1), 10)
}

func (c Client) path() string {
	if c.Path != "" {
		return c.Path
	}
	return SocketPath
}

func (c Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return RequestReadDeadline
}

// Call makes one request and returns the raw result.
//
// Raw, because the caller knows what shape it asked for and this layer does
// not. Decoding here would mean a type switch over every method.
func (c Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	encoded, err := encodeParams(params)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	dialer := net.Dialer{Timeout: DialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", c.path())
	if err != nil {
		// Not being able to reach the socket is the ordinary "it is not
		// running" case, and it has its own code so the CLI can exit 3 rather
		// than report a transport fault the user cannot act on.
		return nil, domain.Errorf(domain.CodeServiceStopped,
			"认证服务未在运行（无法连接 %s）", c.path()).Wrap(err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		// One deadline for the whole exchange. A per-operation one would let a
		// peer that answers a byte at a time hold the call open indefinitely.
		_ = conn.SetDeadline(deadline)
	}

	request := Request{RPCVersion: Version, RequestID: nextRequestID(),
		Method: method, Params: encoded}
	if err := writeRequest(conn, request); err != nil {
		return nil, err
	}
	return readResponse(conn, request.RequestID)
}

func encodeParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"无法编码请求参数").Wrap(err)
	}
	return encoded, nil
}

func writeRequest(conn net.Conn, request Request) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return domain.Errorf(domain.CodeInvalidArgument, "无法编码请求").Wrap(err)
	}
	if len(encoded)+1 > MaxRequestBytes {
		return domain.Errorf(domain.CodeInvalidArgument,
			"请求 %d 字节，超过 %d 上限", len(encoded)+1, MaxRequestBytes)
	}
	frame := append(encoded, '\n')
	for len(frame) > 0 {
		written, err := conn.Write(frame)
		if err != nil {
			return domain.Errorf(domain.CodeServiceStopped,
				"无法发送请求").Wrap(err)
		}
		frame = frame[written:]
	}
	return nil
}

func readResponse(conn net.Conn, requestID string) (json.RawMessage, error) {
	reader := bufio.NewReader(conn)
	line, err := readLine(reader, MaxResponseBytes)
	if err != nil {
		return nil, domain.Errorf(domain.CodeServiceStopped,
			"未能读到完整响应").Wrap(err)
	}

	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, domain.Errorf(domain.CodeProtocolInvalid,
			"响应不是合法的 JSON").Wrap(err)
	}
	if response.RPCVersion != Version {
		return nil, domain.Errorf(domain.CodeProtocolInvalid,
			"服务端协议版本 %d，本程序说 %d", response.RPCVersion, Version)
	}
	// A mismatched id means the answer belongs to somebody else's call. One
	// request per connection makes that impossible over a healthy socket, which
	// is exactly why seeing it means something is wrong enough not to trust the
	// payload.
	//
	// The one exception is a failure with no id at all. Some refusals happen
	// before the request has been read -- the daemon is at its connection cap,
	// or the caller is not entitled to the socket -- and there is no id to echo
	// because nothing has been parsed. Accepting that only for a failure keeps
	// the rule where it matters: no *result* is ever credited to a call that did
	// not ask for it.
	if response.RequestID != requestID && !(response.RequestID == "" && !response.OK) {
		return nil, domain.Errorf(domain.CodeProtocolInvalid,
			"响应的 request_id 与请求不符")
	}
	if !response.OK {
		return nil, errorFromPayload(response.Error)
	}
	return response.Result, nil
}

// errorFromPayload turns the wire envelope back into an error carrying the same
// stable code, so a caller can branch on it exactly as if the failure had
// happened locally.
func errorFromPayload(payload *ErrorPayload) error {
	if payload == nil {
		return domain.Errorf(domain.CodeInternal, "服务返回了失败但没有说明原因")
	}
	code := domain.ErrorCode(payload.Code)
	if code == "" {
		code = domain.CodeInternal
	}
	message := payload.Message
	if message == "" {
		message = "服务内部错误"
	}
	return domain.Errorf(code, "%s", message)
}
