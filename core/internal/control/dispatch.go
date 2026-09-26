package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Handler runs one method. Params are the raw request parameters; the handler
// decodes them into its own type with DecodeParams.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Registry binds declared method names to handlers.
//
// Only names from the catalogue can be bound, and each exactly once. The
// dispatcher looks a name up in this map -- it never derives a function, a
// command or a path from the request.
type Registry struct {
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler, len(catalogue))}
}

// Register binds a handler. It panics on an undeclared or duplicate name:
// both are wiring mistakes that must fail at startup, not serve a request.
func (r *Registry) Register(name string, handler Handler) {
	if _, declared := Lookup(name); !declared {
		panic("control: registering undeclared method " + name)
	}
	if _, duplicate := r.handlers[name]; duplicate {
		panic("control: method registered twice: " + name)
	}
	if handler == nil {
		panic("control: nil handler for " + name)
	}
	r.handlers[name] = handler
}

// Registered reports whether a handler is bound.
func (r *Registry) Registered(name string) bool {
	_, ok := r.handlers[name]
	return ok
}

// Missing lists declared methods with no handler, so a startup check can refuse
// to serve a half-wired protocol instead of returning NotFound at runtime for
// something the client was told exists.
func (r *Registry) Missing() []string {
	var out []string
	for _, name := range MethodNames() {
		if !r.Registered(name) {
			out = append(out, name)
		}
	}
	return out
}

// Dispatch runs one request and always produces a response.
//
// Errors become error responses rather than propagating: the peer is waiting
// for exactly one line, and dropping the connection on an unexpected error
// would leave it to time out with no idea what happened.
func (r *Registry) Dispatch(ctx context.Context, request Request) Response {
	method, declared := Lookup(request.Method)
	if !declared {
		return NewFailure(request.RequestID, domain.Errorf(domain.CodeNotFound,
			"未知方法 %q", request.Method))
	}
	handler, bound := r.handlers[method.Name]
	if !bound {
		return NewFailure(request.RequestID,
			domain.Errorf(domain.CodeUnsupportedCapability,
				"方法 %q 在本次构建中尚未实现", method.Name))
	}

	// Reads are bounded here rather than in each handler, so a read added later
	// cannot forget to be. Mutations and tasks keep the caller's deadline: a
	// login or an install is bounded by its own use case, and cutting one off
	// after a poll's three seconds would look like a network failure.
	if method.Kind == KindRead {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ReadResponseDeadline)
		defer cancel()
	}

	result, err := handler(ctx, request.Params)
	if err != nil {
		return NewFailure(request.RequestID, err)
	}
	response, err := NewSuccess(request.RequestID, result)
	if err != nil {
		return NewFailure(request.RequestID, err)
	}
	return response
}

// DecodeParams decodes request parameters into a typed value.
//
// Unknown fields are refused. That is what stops a caller from attaching a
// parameter one method honours to another that would ignore it -- the shape of
// what a method accepts is part of its contract, not a suggestion.
func DecodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.Errorf(domain.CodeInvalidArgument,
			"参数无效：%v", err).Wrap(err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return domain.Errorf(domain.CodeInvalidArgument, "params 之后还有内容")
	}
	return nil
}

// ServeConn handles one connection: one request in, one response out.
//
// One request per connection keeps the protocol free of pipelining, ordering
// and half-closed-stream questions that nothing here needs. A peer that
// disconnects before completing its request gets no response, because there is
// nobody left to read one.
func ServeConn(ctx context.Context, registry *Registry, stream io.ReadWriter) error {
	timed, _ := stream.(deadlineStream)
	if timed != nil {
		if err := timed.SetReadDeadline(time.Now().Add(RequestReadDeadline)); err != nil {
			return err
		}
	}

	reader := bufio.NewReaderSize(stream, 4096)
	request, err := ReadRequest(reader, MaxRequestBytes)
	if err != nil {
		if errors.Is(err, ErrClosed) {
			return nil
		}
		// The request id is unknown when parsing failed, so the reply carries
		// an empty one. The peer still learns why it was refused.
		return WriteResponse(stream, NewFailure("", err))
	}

	// The response budget starts once the request is understood and covers the
	// handler and the write together, so a read cannot spend its three seconds
	// working and then take three more to answer.
	respondBy := time.Now().Add(ReadResponseDeadline)
	response := registry.Dispatch(ctx, request)

	if timed != nil {
		if method, declared := Lookup(request.Method); declared && method.Kind == KindRead {
			if err := timed.SetWriteDeadline(respondBy); err != nil {
				return err
			}
		}
	}
	return WriteResponse(stream, response)
}
