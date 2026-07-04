package networkengine

import (
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
