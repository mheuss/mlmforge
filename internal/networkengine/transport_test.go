package networkengine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStdioTransport_Ping(t *testing.T) {
	transport, err := NewStdioTransport(findWorkerBinary(t))
	require.NoError(t, err)
	defer transport.Close()

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
	defer transport.Close()

	_, err = transport.Call(context.Background(), "nonexistent", json.RawMessage("null"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNKNOWN_OP")
}

func TestStdioTransport_MultipleCalls(t *testing.T) {
	transport, err := NewStdioTransport(findWorkerBinary(t))
	require.NoError(t, err)
	defer transport.Close()

	for i := 0; i < 5; i++ {
		result, err := transport.Call(context.Background(), "ping", json.RawMessage("null"))
		require.NoError(t, err)

		var pong string
		err = json.Unmarshal(result, &pong)
		require.NoError(t, err)
		assert.Equal(t, "pong", pong)
	}
}

func TestStdioTransport_ContextCancellation(t *testing.T) {
	// Create a pipe pair for stdout. The test controls the write end,
	// so it can block the reader indefinitely by never writing.
	stdoutR, stdoutW := io.Pipe()

	// Create a pipe pair for stdin. The transport writes requests here.
	// We discard what it writes since we only care about the read side.
	stdinR, stdinW := io.Pipe()
	go func() {
		// Drain stdin so the transport write doesn't block.
		_, _ = io.Copy(io.Discard, stdinR)
	}()

	transport := &StdioTransport{
		cmd:    exec.Command("true"), // placeholder, never started
		stdin:  stdinW,
		reader: bufio.NewReader(stdoutR),
	}

	// Use a context that cancels quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := transport.Call(ctx, "ping", json.RawMessage("null"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"expected context.DeadlineExceeded, got: %v", err)

	// After cancellation the transport should be marked closed.
	// A subsequent call must return ErrTransportClosed, not race with
	// the orphaned reader goroutine.
	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTransportClosed)

	// Clean up the pipes so the orphaned goroutine exits.
	stdoutW.Close()
	stdinR.Close()
}

func TestStdioTransport_ContextAlreadyCancelled(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	go func() {
		_, _ = io.Copy(io.Discard, stdinR)
	}()

	transport := &StdioTransport{
		cmd:    exec.Command("true"),
		stdin:  stdinW,
		reader: bufio.NewReader(stdoutR),
	}

	// Cancel the context before calling.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := transport.Call(ctx, "ping", json.RawMessage("null"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled),
		"expected context.Canceled, got: %v", err)

	// Transport should be closed after context cancellation.
	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	assert.ErrorIs(t, err, ErrTransportClosed)

	stdoutW.Close()
	stdinR.Close()
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
