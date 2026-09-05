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
	"time"

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
	stderr        syncBuffer
	lines         chan json.RawMessage
	signalHandler func(json.RawMessage)
	mu            sync.Mutex
	nextID        atomic.Int64
	closed        atomic.Bool
	closeOnce     sync.Once
	closeErr      error
	readErrMu     sync.Mutex
	readErr       error
}

// syncBuffer is a bytes.Buffer safe for concurrent use. os/exec copies the
// worker's stderr into it from an internal goroutine that runs until Wait
// returns, while readError may read it on the failure path before then.
// bytes.Buffer is not safe for concurrent read/write, so both sides go through
// the mutex.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	// Bounds the wait for stderr copying after the process exits. Stderr is a
	// buffer rather than a file, so os/exec copies it on a goroutine that ends
	// only when every write end of that pipe closes. Killing the worker does
	// not close one an orphaned grandchild inherited, so without this the wait
	// outlives the kill that was supposed to bound it.
	cmd.WaitDelay = waitIODelay
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
				t.deliverSignal(line)
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

// deliverSignal forwards a signal line to the registered handler, recovering
// from any panic. Signal forwarding is best-effort, and the handler runs inline
// on the request path (under the Call mutex), so a misbehaving handler must not
// be able to crash the caller — or the process.
func (t *StdioTransport) deliverSignal(line json.RawMessage) {
	if t.signalHandler == nil {
		return
	}
	defer func() { _ = recover() }()
	t.signalHandler(line)
}

// closeGracePeriod bounds how long Close waits for the worker to exit on its
// own after stdin is closed. A worker that ignores EOF, or that is wedged for
// any other reason, is killed rather than blocking the caller forever.
const closeGracePeriod = 5 * time.Second

// waitIODelay bounds the wait for the process's I/O to finish once it has
// exited. See where it is set for why that is a separate deadline.
const waitIODelay = 2 * time.Second

// drainTailBound caps the wait for the reader to finish after the process is
// gone. Reaching it means the reader did not stop when its input closed, which
// should not happen; the bound is here so that surprise cannot hang a caller.
const drainTailBound = 2 * time.Second

// ErrWorkerNotExited reports that the worker was still running when the close
// grace period elapsed, so a kill was attempted. It says what was observed;
// whether the kill succeeded is reported separately by the joined error.
var ErrWorkerNotExited = errors.New("worker did not exit within the close grace period")

// ErrWorkerUnreaped reports that the worker had still not been reaped after it
// was killed and the reap deadline elapsed. Close gives up and returns rather
// than holding the caller for a process it cannot stop.
var ErrWorkerUnreaped = errors.New("worker was not reaped after being killed")

// Close shuts down the worker process by closing stdin and waiting for exit.
//
// It keeps draining stdout while it waits. readLoop is the only sender on
// lines and blocks once that channel is full, and nothing else drains it after
// the last Call returns. A worker that keeps writing therefore stops readLoop,
// fills the stdout pipe, and blocks in write without ever reaching its stdin
// read -- so it never sees the EOF this function just sent, and waiting on it
// alone would never return.
//
// Lines drained here are discarded rather than forwarded to signalHandler. The
// transport is shutting down, and a handler running after Close was called
// would be a surprise to the caller.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeOnce.Do(func() { t.closeErr = t.shutdown() })
	return t.closeErr
}

// shutdown runs the close sequence once. It needs a latch of its own rather
// than closed, which Call also sets to retire a transport with an abandoned
// request still in flight.
//
// Running it twice starts a second cmd.Wait, which is not allowed while the
// first is still running, and re-closes stdin.
func (t *StdioTransport) shutdown() error {
	t.closed.Store(true)
	stdinErr := t.stdin.Close()

	waited := make(chan error, 1)
	go func() { waited <- t.cmd.Wait() }()

	// A nil channel blocks forever in select, which is what should happen once
	// readLoop has closed lines and there is nothing left to drain.
	lines := t.lines
	timer := time.NewTimer(closeGracePeriod)
	defer timer.Stop()

	for {
		select {
		case _, ok := <-lines:
			if !ok {
				lines = nil
			}
		case waitErr := <-waited:
			return errors.Join(stdinErr, waitErr, drainTail(lines))
		case <-timer.C:
			// The timer and the wait can become ready together, and select picks
			// uniformly between them. Take the wait when it is already there:
			// the worker did exit in time, and killing it and reporting
			// otherwise would name a cause that was not observed.
			select {
			case waitErr := <-waited:
				return errors.Join(stdinErr, waitErr, drainTail(lines))
			default:
			}
			return errors.Join(stdinErr, t.killAndReap(lines, waited))
		}
	}
}

// killAndReap stops a worker that outlasted the grace period, then waits a
// bounded time to reap it.
//
// The reap needs its own bound because nothing else supplies one. WaitDelay
// starts counting once the process has exited, so it does nothing while the
// process is still running -- which is the case here whenever the kill did not
// take. Giving up leaves the wait running on its goroutine; the channel it
// sends to is buffered, so that goroutine still finishes on its own.
func (t *StdioTransport) killAndReap(lines <-chan json.RawMessage, waited <-chan error) error {
	killErr := t.kill()
	select {
	case waitErr := <-waited:
		return errors.Join(killErr, waitErr, ErrWorkerNotExited, drainTail(lines))
	case <-time.After(waitIODelay):
		return errors.Join(killErr, ErrWorkerNotExited, ErrWorkerUnreaped, drainTail(lines))
	}
}

// kill stops the worker. Process is nil only for a transport that was never
// started, which NewStdioTransport cannot produce.
func (t *StdioTransport) kill() error {
	if t.cmd.Process == nil {
		return nil
	}
	return t.cmd.Process.Kill()
}

// drainTail keeps receiving until the reader closes lines, so a reader blocked
// on a full channel is released rather than abandoned with its goroutine and
// buffered messages held for the life of the process.
//
// It terminates on its own: waiting on the process closes the read end of the
// pipe the reader is using, so its next read fails and it closes the channel.
func drainTail(lines <-chan json.RawMessage) error {
	if lines == nil {
		return nil
	}
	timer := time.NewTimer(drainTailBound)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return nil
			}
		case <-timer.C:
			return errReaderStillRunning
		}
	}
}

// errReaderStillRunning reports that the reader had not stopped once the
// process was gone and the drain deadline elapsed.
var errReaderStillRunning = errors.New("stdout reader still running after the worker exited")
