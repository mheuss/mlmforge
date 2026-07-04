package networkengine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/trace"
)

// ErrTransportClosed is returned when Call is invoked on a closed transport.
var ErrTransportClosed = errors.New("transport is closed")

// Compile-time check: StdioTransport implements EngineTransport.
var _ EngineTransport = (*StdioTransport)(nil)

// StdioTransport communicates with the Rust worker via stdin/stdout NDJSON.
//
// A single readLoop goroutine owns stdout for the life of the transport and
// pushes every line onto lines. Call demuxes that stream: signal frames
// (structured log/metric/trace events) are forwarded to signalHandler, and the
// next protocol response is returned. Draining stdout continuously — rather than
// one read per Call — keeps the worker from blocking on a full stdout pipe while
// Go is busy elsewhere (design-rationale 019, D4).
type StdioTransport struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stderr        bytes.Buffer
	lines         chan json.RawMessage
	signalHandler func(json.RawMessage)
	mu            sync.Mutex
	nextID        atomic.Int64
	closed        atomic.Bool
	readErrMu     sync.Mutex
	readErr       error
}

// TransportOption configures a StdioTransport at construction time.
type TransportOption func(*StdioTransport)

// WithSignalHandler registers h to receive each signal line the worker emits on
// stdout. Signals are demuxed from protocol responses by Call and forwarded to h
// synchronously. Observability is opt-in: with no handler, signal lines are
// still drained (so responses stay in sync) but discarded.
func WithSignalHandler(h func(json.RawMessage)) TransportOption {
	return func(t *StdioTransport) { t.signalHandler = h }
}

type protocolRequest struct {
	ID string `json:"id"`
	Op string `json:"op"`
	// TraceID and SpanID carry the caller's OTel span context to the engine so
	// engine-side signals can be correlated with the Go request. Populated by
	// Call from the context; omitempty keeps them absent when there is no span,
	// which protects contract fixtures that assert exact request bytes (D7).
	TraceID string          `json:"trace_id,omitempty"`
	SpanID  string          `json:"span_id,omitempty"`
	Params  json.RawMessage `json:"params"`
}

type protocolResponse struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *protocolError  `json:"error,omitempty"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EngineError represents an error returned by the Rust worker.
// Use errors.As to inspect the error code programmatically.
type EngineError struct {
	Code    string
	Message string
}

func (e *EngineError) Error() string {
	return fmt.Sprintf("engine error [%s]: %s", e.Code, e.Message)
}

// NewStdioTransport spawns the Rust worker binary and returns a transport
// that communicates with it via NDJSON over stdin/stdout.
func NewStdioTransport(binaryPath string, opts ...TransportOption) (*StdioTransport, error) {
	cmd := exec.Command(binaryPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	transport := &StdioTransport{
		cmd:   cmd,
		stdin: stdin,
		lines: make(chan json.RawMessage, 64),
	}
	for _, opt := range opts {
		opt(transport)
	}
	cmd.Stderr = &transport.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	go transport.readLoop(stdout)
	return transport, nil
}

// readLoop is the sole reader of the worker's stdout. It runs for the life of
// the transport, pushing every non-empty line onto t.lines so Call can demux
// signals from responses without a second goroutine ever touching stdout. On any
// read error (including EOF when the worker exits) it records the error and
// closes t.lines to wake a waiting Call.
func (t *StdioTransport) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		// A read may return data and an error together (final line before EOF),
		// so deliver the line first, then handle the error.
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			t.lines <- json.RawMessage(trimmed)
		}
		if err != nil {
			t.readErrMu.Lock()
			t.readErr = err
			t.readErrMu.Unlock()
			close(t.lines)
			return
		}
	}
}

// Call sends an operation to the Rust worker and waits for the response.
// The mutex ensures only one request is in flight at a time.
func (t *StdioTransport) Call(ctx context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed.Load() {
		return nil, ErrTransportClosed
	}

	id := fmt.Sprintf("req-%d", t.nextID.Add(1))

	req := protocolRequest{ID: id, Op: op, Params: params}
	// Propagate the caller's OTel span context so engine-side signals can be
	// correlated with this request (D7). The omitempty tags keep both fields
	// absent when there is no active span, protecting contract fixtures.
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		req.TraceID = sc.TraceID().String()
		req.SpanID = sc.SpanID().String()
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := fmt.Fprintf(t.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	for {
		select {
		case line, ok := <-t.lines:
			if !ok {
				return nil, t.readError()
			}
			// Signals are log/metric/trace events, not responses. Forward and
			// keep reading so a signal emitted mid-request is never mistaken for
			// the response.
			if isSignal(line) {
				if t.signalHandler != nil {
					t.signalHandler(line)
				}
				continue
			}

			var resp protocolResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				return nil, fmt.Errorf("unmarshal response: %w", err)
			}
			if resp.ID != id {
				return nil, fmt.Errorf("response ID mismatch: sent %q, got %q", id, resp.ID)
			}
			if !resp.OK {
				code := "UNKNOWN"
				msg := "unknown error"
				if resp.Error != nil {
					code = resp.Error.Code
					msg = resp.Error.Message
				}
				return nil, &EngineError{Code: code, Message: msg}
			}
			return resp.Result, nil
		case <-ctx.Done():
			// The response for this request may still arrive on t.lines after we
			// return, which would desync the stream for the next Call. Mark the
			// transport closed so it is not reused (matches the pre-async
			// behavior for an abandoned in-flight request).
			t.closed.Store(true)
			return nil, ctx.Err()
		}
	}
}

// readError builds the error returned when t.lines closes before a response.
// The read error captured by readLoop is safe to read here: closing t.lines
// happens-after the store, and the closed-channel receive happens-after the
// close.
func (t *StdioTransport) readError() error {
	t.readErrMu.Lock()
	err := t.readErr
	t.readErrMu.Unlock()
	if err == nil {
		err = io.EOF
	}
	if stderrOut := t.stderr.String(); stderrOut != "" {
		return fmt.Errorf("read response: %w\nworker stderr: %s", err, stderrOut)
	}
	return fmt.Errorf("read response: %w", err)
}

// Close shuts down the worker process by closing stdin and waiting for exit.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed.Store(true)
	stdinErr := t.stdin.Close()
	waitErr := t.cmd.Wait()
	return errors.Join(stdinErr, waitErr)
}
