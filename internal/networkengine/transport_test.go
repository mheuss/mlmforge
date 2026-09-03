package networkengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

	assert.JSONEq(t, `{"protocol_version":1}`, string(result))
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

		assert.JSONEq(t, `{"protocol_version":1}`, string(result))
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
		_, _ = io.WriteString(stdoutW, `{"id":"req-1","ok":true,"result":{"demux":"payload"}}`+"\n")
	}()

	result, err := transport.Call(context.Background(), "demux_probe", json.RawMessage("null"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"demux":"payload"}`, string(result))

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
		_, _ = io.WriteString(stdoutW, `{"id":"req-1","ok":true,"result":{"demux":"payload"}}`+"\n")
	}()

	result, err := transport.Call(context.Background(), "demux_probe", json.RawMessage("null"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"demux":"payload"}`, string(result))

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
		_, _ = io.WriteString(stdoutW, `{"id":"req-1","ok":true,"result":{"demux":"payload"}}`+"\n")
	}()

	result, err := transport.Call(context.Background(), "demux_probe", json.RawMessage("null"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"demux":"payload"}`, string(result))

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

// workerBinaryPath is the compiled Rust worker, relative to this package.
const workerBinaryPath = "../../engine/target/debug/network-engine-worker"

// engineSourceRoot is the Rust workspace whose sources back that binary.
const engineSourceRoot = "../../engine"

var (
	workerFreshnessOnce  sync.Once
	workerFreshnessNewer string
	workerFreshnessErr   error
)

// staleWorkerBinary returns the newest Rust source under sourceRoot that
// postdates binPath, or "" when the binary is current. Sources under
// sourceRoot/target are skipped, because cargo regenerates them during the
// same build that produces the binary.
func staleWorkerBinary(binPath, sourceRoot string) (string, error) {
	binary, err := os.Stat(binPath)
	if err != nil {
		return "", err
	}
	built := binary.ModTime()
	generated := filepath.Join(sourceRoot, "target")

	var newest string
	var newestMod time.Time
	walkErr := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == generated {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".rs" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(built) && info.ModTime().After(newestMod) {
			newest, newestMod = path, info.ModTime()
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	return newest, nil
}

// findWorkerBinary returns the path to the compiled Rust worker binary.
//
// An absent binary skips. Any other stat error fails, because a skip on a
// permission or I/O error is indistinguishable from a pass. A binary older
// than the Rust sources fails too: tests that run against a stale worker
// assert about engine code that is no longer on the branch, and a green Go
// suite then means nothing (HEU-615).
func findWorkerBinary(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(workerBinaryPath); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			require.NoError(t, err, "could not stat worker binary %s", workerBinaryPath)
		}
		t.Skipf("worker binary not found at %s (run 'cargo build --workspace' in engine/)", workerBinaryPath)
	}
	workerFreshnessOnce.Do(func() {
		workerFreshnessNewer, workerFreshnessErr = staleWorkerBinary(workerBinaryPath, engineSourceRoot)
	})
	require.NoError(t, workerFreshnessErr, "could not tell whether %s is current", workerBinaryPath)
	if workerFreshnessNewer != "" {
		t.Fatalf(
			"worker binary %s is older than %s (run 'cargo build --workspace' in engine/ before the Go suite)",
			workerBinaryPath, workerFreshnessNewer,
		)
	}
	return workerBinaryPath
}

// writeRustSource writes a .rs file at rel under root and stamps it with mod.
func writeRustSource(t *testing.T, root, rel string, mod time.Time) string {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("fn main() {}\n"), 0o644))
	require.NoError(t, os.Chtimes(path, mod, mod))
	return path
}

// writeWorkerBinary writes a stand-in for the compiled worker and stamps it
// with mod. Only the modification time matters to staleWorkerBinary.
func writeWorkerBinary(t *testing.T, dir string, mod time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "network-engine-worker")
	require.NoError(t, os.WriteFile(path, []byte("binary"), 0o755))
	require.NoError(t, os.Chtimes(path, mod, mod))
	return path
}

func TestStaleWorkerBinary_ReportsSourceNewerThanBinary(t *testing.T) {
	root := t.TempDir()
	built := time.Now().Add(-time.Hour)
	binary := writeWorkerBinary(t, root, built)
	edited := writeRustSource(t, root, "network-engine/src/lib.rs", built.Add(time.Minute))

	newer, err := staleWorkerBinary(binary, root)

	require.NoError(t, err)
	assert.Equal(t, edited, newer)
}

func TestStaleWorkerBinary_ReportsNothingWhenBinaryIsCurrent(t *testing.T) {
	root := t.TempDir()
	built := time.Now()
	binary := writeWorkerBinary(t, root, built)
	writeRustSource(t, root, "network-engine/src/lib.rs", built.Add(-time.Minute))

	newer, err := staleWorkerBinary(binary, root)

	require.NoError(t, err)
	assert.Empty(t, newer)
}

// Cargo regenerates sources under target/ as part of the same build that
// produces the binary. Counting them would report the binary as stale against
// its own build output.
func TestStaleWorkerBinary_IgnoresGeneratedSourcesUnderTarget(t *testing.T) {
	root := t.TempDir()
	built := time.Now().Add(-time.Hour)
	binary := writeWorkerBinary(t, root, built)
	writeRustSource(t, root, "target/debug/build/generated.rs", built.Add(time.Minute))

	newer, err := staleWorkerBinary(binary, root)

	require.NoError(t, err)
	assert.Empty(t, newer)
}

func TestStaleWorkerBinary_ErrorsWhenBinaryIsMissing(t *testing.T) {
	root := t.TempDir()

	_, err := staleWorkerBinary(filepath.Join(root, "absent"), root)

	require.Error(t, err)
}

// A worker that keeps writing after answering must not be able to wedge Close.
//
// readLoop is the only reader of stdout and blocks once the lines channel is
// full. Nothing drains that channel after Call returns, so a chatty worker
// fills it, readLoop stops reading, the stdout pipe fills, and the worker
// blocks in write without ever reaching its stdin read -- never seeing the EOF
// Close just sent. A bare cmd.Wait() then never returns.
//
// The payload has to exceed the OS pipe buffer as well as the channel: a few
// hundred short lines fit in the pipe and exit cleanly, proving nothing.
func TestStdioTransport_CloseDoesNotHangOnAChattyWorker(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "chatty-worker.sh")
	script := "#!/bin/sh\n" +
		"pad=xxxxxxxxxxxxxxxxxxxxxxxxx\n" +
		"pad=$pad$pad$pad; pad=$pad$pad$pad\n" +
		"while IFS= read -r _line; do\n" +
		"  printf '%s\\n' '{\"id\":\"req-1\",\"ok\":true,\"result\":{\"protocol_version\":1}}'\n" +
		"  i=0\n" +
		"  while [ $i -lt 5000 ]; do\n" +
		"    printf '%s\\n' \"{\\\"signal\\\":\\\"$pad\\\"}\"\n" +
		"    i=$((i+1))\n" +
		"  done\n" +
		"done\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))

	transport, err := NewStdioTransport(fake)
	require.NoError(t, err)

	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)

	closeErr := make(chan error, 1)
	go func() { closeErr <- transport.Close() }()

	// Well under closeGracePeriod, and asserting the worker was not killed.
	// Both matter: if draining regressed to a no-op the kill fallback would
	// still return, and a test that only waits for Close to return would pass
	// on the very path it exists to rule out.
	select {
	case err := <-closeErr:
		require.NotErrorIs(t, err, ErrWorkerNotExited,
			"the worker should have exited once stdout was drained, not been killed")
	case <-time.After(closeGracePeriod - 2*time.Second):
		t.Fatal("Close did not return promptly; the chatty worker wedged it")
	}
}

// A worker that stops reading stdin cannot be shut down by closing it, so Close
// falls back to killing the process. Without the fallback this blocks forever.
func TestStdioTransport_CloseKillsAWorkerThatIgnoresStdin(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "deaf-worker.sh")
	script := "#!/bin/sh\n" +
		"IFS= read -r _line\n" +
		"printf '%s\\n' '{\"id\":\"req-1\",\"ok\":true,\"result\":{\"protocol_version\":1}}'\n" +
		"while true; do sleep 1; done\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))

	transport, err := NewStdioTransport(fake)
	require.NoError(t, err)

	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)

	closeErr := make(chan error, 1)
	go func() { closeErr <- transport.Close() }()

	select {
	case err := <-closeErr:
		assert.ErrorIs(t, err, ErrWorkerNotExited, "an unresponsive worker must be reported as not having exited")
	case <-time.After(closeGracePeriod + 10*time.Second):
		t.Fatal("Close did not return; the kill fallback did not fire")
	}
}
