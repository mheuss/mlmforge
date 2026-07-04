package networkengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestStdioTransport_Ping(t *testing.T) {
	transport, err := NewStdioTransport(findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = transport.Close() }()

	result, err := transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)

	var pong string
	err = json.Unmarshal(result, &pong)
	require.NoError(t, err)
	assert.Equal(t, "pong", pong)
}

func TestStdioTransport_UnknownOp(t *testing.T) {
	transport, err := NewStdioTransport(findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = transport.Close() }()

	_, err = transport.Call(context.Background(), "nonexistent", json.RawMessage("null"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNKNOWN_OP")
}

func TestStdioTransport_MultipleCalls(t *testing.T) {
	transport, err := NewStdioTransport(findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = transport.Close() }()

	for range 5 {
		result, err := transport.Call(context.Background(), "ping", json.RawMessage("null"))
		require.NoError(t, err)

		var pong string
		err = json.Unmarshal(result, &pong)
		require.NoError(t, err)
		assert.Equal(t, "pong", pong)
	}
}

// TestStdioTransport_SignalDemux drives the demux without a real worker: a
// signal line arrives before the response, and Call must forward the signal and
// still return the response.
func TestStdioTransport_SignalDemux(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, stdinR) }()
	stdoutR, stdoutW := io.Pipe()

	var mu sync.Mutex
	var signals []string
	transport := newTestTransport(stdinW, stdoutR, func(line json.RawMessage) {
		mu.Lock()
		signals = append(signals, string(line))
		mu.Unlock()
	})

	const signal = `{"type":"signal","level":"warn","target":"t","message":"heads up"}`
	// A fresh transport's first Call generates id "req-1".
	go func() {
		_, _ = io.WriteString(stdoutW, signal+"\n")
		_, _ = io.WriteString(stdoutW, `{"id":"req-1","ok":true,"result":"pong"}`+"\n")
	}()

	result, err := transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)
	var pong string
	require.NoError(t, json.Unmarshal(result, &pong))
	assert.Equal(t, "pong", pong)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, signals, 1, "handler should receive exactly the signal line")
	assert.JSONEq(t, signal, signals[0])

	_ = stdoutW.Close()
	_ = stdinR.Close()
}

// TestStdioTransport_SignalDemux_Multiple verifies several signals ahead of the
// response are each forwarded, and the response is still returned.
func TestStdioTransport_SignalDemux_Multiple(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, stdinR) }()
	stdoutR, stdoutW := io.Pipe()

	var mu sync.Mutex
	var count int
	transport := newTestTransport(stdinW, stdoutR, func(json.RawMessage) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	go func() {
		_, _ = io.WriteString(stdoutW, `{"type":"signal","level":"info","message":"one"}`+"\n")
		_, _ = io.WriteString(stdoutW, `{"type":"signal","level":"warn","message":"two"}`+"\n")
		_, _ = io.WriteString(stdoutW, `{"id":"req-1","ok":true,"result":"pong"}`+"\n")
	}()

	result, err := transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)
	var pong string
	require.NoError(t, json.Unmarshal(result, &pong))
	assert.Equal(t, "pong", pong)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, count, "both signals should be forwarded before the response")

	_ = stdoutW.Close()
	_ = stdinR.Close()
}

// TestStdioTransport_SignalHandlerPanic verifies a panicking best-effort handler
// is contained: Call recovers and still returns the response.
func TestStdioTransport_SignalHandlerPanic(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, stdinR) }()
	stdoutR, stdoutW := io.Pipe()

	transport := newTestTransport(stdinW, stdoutR, func(json.RawMessage) {
		panic("handler boom")
	})

	go func() {
		_, _ = io.WriteString(stdoutW, `{"type":"signal","level":"warn","message":"boom"}`+"\n")
		_, _ = io.WriteString(stdoutW, `{"id":"req-1","ok":true,"result":"pong"}`+"\n")
	}()

	result, err := transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)
	var pong string
	require.NoError(t, json.Unmarshal(result, &pong))
	assert.Equal(t, "pong", pong)

	_ = stdoutW.Close()
	_ = stdinR.Close()
}

func TestStdioTransport_ContextCancellation(t *testing.T) {
	// Never write to stdout, so the reader blocks and Call must give up on the
	// context deadline.
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, stdinR) }()

	transport := newTestTransport(stdinW, stdoutR, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := transport.Call(ctx, "ping", json.RawMessage("null"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"expected context.DeadlineExceeded, got: %v", err)

	// After cancellation the transport is marked closed: the abandoned request's
	// response may still land on the channel, so reuse would desync the stream.
	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTransportClosed)

	// Unblock and drain the reader goroutine.
	_ = stdoutW.Close()
	_ = stdinR.Close()
}

func TestStdioTransport_ContextAlreadyCancelled(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, stdinR) }()

	transport := newTestTransport(stdinW, stdoutR, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := transport.Call(ctx, "ping", json.RawMessage("null"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled),
		"expected context.Canceled, got: %v", err)

	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	assert.ErrorIs(t, err, ErrTransportClosed)

	_ = stdoutW.Close()
	_ = stdinR.Close()
}

// TestStdioTransport_TraceContext asserts on the raw request bytes: with an OTel
// span in context, Call marshals trace_id/span_id; with a plain context the
// omitempty tags keep both fields absent (which protects contract fixtures that
// assert exact request bytes).
func TestStdioTransport_TraceContext(t *testing.T) {
	withSpan := func() context.Context {
		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xa, 0xb, 0xc, 0xd, 0xe, 0xf, 0x10},
			SpanID:  trace.SpanID{0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8},
		})
		return trace.ContextWithSpanContext(context.Background(), sc)
	}

	tests := []struct {
		name      string
		ctx       context.Context
		wantTrace bool
	}{
		{"valid span context", withSpan(), true},
		{"plain context", context.Background(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdin := &stdinCapture{}
			stdoutR, stdoutW := io.Pipe()
			transport := newTestTransport(stdin, stdoutR, nil)

			// Feed the response for the fresh transport's first id (req-1).
			go func() {
				_, _ = io.WriteString(stdoutW, `{"id":"req-1","ok":true,"result":null}`+"\n")
			}()

			_, err := transport.Call(tt.ctx, "ping", json.RawMessage("null"))
			require.NoError(t, err)

			sent := stdin.String()
			if tt.wantTrace {
				assert.Contains(t, sent, `"trace_id":`)
				assert.Contains(t, sent, `"span_id":`)
			} else {
				assert.NotContains(t, sent, `"trace_id":`)
				assert.NotContains(t, sent, `"span_id":`)
			}

			_ = stdoutW.Close()
		})
	}
}

// stdinCapture is an io.WriteCloser that records everything written, so tests
// can assert on the exact request bytes Call sends.
type stdinCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *stdinCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *stdinCapture) Close() error { return nil }

func (c *stdinCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// newTestTransport builds a StdioTransport around injected pipes (no real
// subprocess) and starts its readLoop, so the signal-demux path can be unit
// tested without spawning the worker. handler may be nil.
func newTestTransport(stdin io.WriteCloser, stdout io.Reader, handler func(json.RawMessage)) *StdioTransport {
	transport := &StdioTransport{
		cmd:           exec.Command("true"), // placeholder, never started
		stdin:         stdin,
		lines:         make(chan json.RawMessage, 64),
		signalHandler: handler,
	}
	go transport.readLoop(stdout)
	return transport
}

// findWorkerBinary returns the path to the compiled Rust worker binary.
func findWorkerBinary(t *testing.T) string {
	t.Helper()
	path := "../../engine/target/debug/network-engine-worker"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("worker binary not found at %s (run 'cargo build --workspace' in engine/)", path)
	}
	return path
}
