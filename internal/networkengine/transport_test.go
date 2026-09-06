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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestStdioTransport_CloseIsIdempotent(t *testing.T) {
	transport, err := NewStdioTransport(findWorkerBinary(t))
	require.NoError(t, err)

	first := transport.Close()
	second := transport.Close()

	require.NoError(t, first)
	assert.NoError(t, second)
}

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

	before := readLoopGoroutines()
	transport, err := NewStdioTransport(fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = transport.Close() })

	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)

	closeErr := make(chan error, 1)
	go func() { closeErr <- transport.Close() }()

	// Well under closeGracePeriod, and asserting the worker was not killed.
	// Both matter: if draining regressed to a no-op the kill fallback would
	// still return, and a test that only waits for Close to return would pass
	// on the very path it exists to rule out.
	require.Greater(t, closeGracePeriod, 3*time.Second,
		"this test's bound assumes the grace period leaves room under it")
	select {
	case err := <-closeErr:
		require.NotErrorIs(t, err, ErrWorkerNotExited,
			"the worker should have exited once stdout was drained, not been killed")
	case <-time.After(closeGracePeriod - 2*time.Second):
		t.Fatal("Close did not return promptly; the chatty worker wedged it")
	}

	// The reader must be gone once Close returns. In this case it ends on its
	// own, because the worker exits and its pipe closes; TestDrainTail covers
	// the case where it does not.
	require.Eventually(t, func() bool { return readLoopGoroutines() <= before },
		5*time.Second, 50*time.Millisecond,
		"the reader goroutine was abandoned rather than released")
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
	t.Cleanup(func() { _ = transport.Close() })

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

// readLoopGoroutines counts the readers currently running, so a test can prove
// Close released the one it started.
func readLoopGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "networkengine.(*StdioTransport).readLoop")
}

// A worker whose child outlives it keeps the stderr pipe open, so waiting on the
// worker does not finish just because the worker was killed. Nothing else bounds
// that wait: the delay os/exec applies starts only once the process has exited,
// which never happens if the kill does not take. Remove the bound and this test
// hangs rather than failing.
func TestStdioTransport_CloseBoundsTheWaitForAnOrphanedChild(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "forking-worker.sh")
	script := "#!/bin/sh\n" +
		"IFS= read -r _line\n" +
		"printf '%s\\n' '{\"id\":\"req-1\",\"ok\":true,\"result\":{\"protocol_version\":1}}'\n" +
		"sleep 30 &\n" +
		"while true; do sleep 1; done\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))

	transport, err := NewStdioTransport(fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = transport.Close() })

	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)

	start := time.Now()
	closeErr := make(chan error, 1)
	go func() { closeErr <- transport.Close() }()

	select {
	case err := <-closeErr:
		assert.ErrorIs(t, err, ErrWorkerNotExited)
		// Comfortably under the orphan's own lifetime, so passing cannot mean
		// "we waited for the child to go away on its own".
		assert.Less(t, time.Since(start), 15*time.Second,
			"Close waited on the orphaned child rather than bounding the wait")
	case <-time.After(25 * time.Second):
		t.Fatal("Close did not return; the wait after the kill is unbounded")
	}
}

// The reap bound and cmd.WaitDelay are nested deadlines. When they were the
// same constant, killAndReap armed its timer immediately after the kill while
// os/exec armed WaitDelay only once Process.Wait observed the exit, so the outer
// deadline preempted the inner one and Close reported a reap failure for a reap
// that landed 50-150us later. That hit every caller, not just repeat ones, on
// essentially every orphaned-child shutdown (HEU-671).
func TestStdioTransport_CloseDoesNotReportAReapThatIsAboutToLand(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "forking-worker.sh")
	script := "#!/bin/sh\n" +
		"IFS= read -r _line\n" +
		"printf '%s\\n' '{\"id\":\"req-1\",\"ok\":true,\"result\":{\"protocol_version\":1}}'\n" +
		"sleep 30 &\n" +
		"while true; do sleep 1; done\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))

	transport, err := NewStdioTransport(fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = transport.Close() })

	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)

	closeErr := make(chan error, 1)
	go func() { closeErr <- transport.Close() }()

	select {
	case err := <-closeErr:
		assert.ErrorIs(t, err, ErrWorkerNotExited,
			"the worker ignored EOF and had to be killed; that stays reported")
		assert.NotErrorIs(t, err, ErrWorkerUnreaped,
			"the reap lands well inside reapBound; reporting it as failed is the false alarm")
	case <-time.After(25 * time.Second):
		t.Fatal("Close did not return; the wait after the kill is unbounded")
	}
}

// ErrWorkerUnreaped records what was observed when the reap deadline elapsed,
// not a standing fact. Close abandons the cmd.Wait goroutine on that path and
// the channel it sends to is buffered, so a reap can land after Close has
// already returned. A later Close must observe that rather than replay the
// earlier verdict (HEU-671).
//
// Driven through the struct rather than a subprocess. Once reapBound outlasts
// cmd.WaitDelay, reaching the unreaped path for real needs a worker that
// survives SIGKILL, which is not something a test can arrange portably. The
// revision logic still needs pinning, so this drives it at the seam.
func TestStdioTransport_RepeatCloseReObservesTheReap(t *testing.T) {
	newClosed := func(waited chan error) *StdioTransport {
		tr := &StdioTransport{waited: waited}
		// Burn the latch so Close replays rather than running shutdown.
		tr.closeOnce.Do(func() {})
		tr.closeErr = errors.Join(ErrWorkerNotExited, ErrWorkerUnreaped)
		tr.closeErrIfReaped = errors.Join(ErrWorkerNotExited)
		return tr
	}

	t.Run("keeps the verdict while the reap has not landed", func(t *testing.T) {
		tr := newClosed(make(chan error, 1))

		for i := range 3 {
			err := tr.Close()
			assert.ErrorIs(t, err, ErrWorkerUnreaped,
				"call %d: nothing was observed, so the first call's verdict stands", i)
			assert.ErrorIs(t, err, ErrWorkerNotExited)
		}
	})

	t.Run("drops the sentinel once the reap lands, and latches", func(t *testing.T) {
		waited := make(chan error, 1)
		tr := newClosed(waited)

		require.ErrorIs(t, tr.Close(), ErrWorkerUnreaped, "not reaped yet")

		waited <- errors.New("signal: killed")

		err := tr.Close()
		assert.NotErrorIs(t, err, ErrWorkerUnreaped, "the reap landed; stop reporting it as failed")
		assert.ErrorIs(t, err, ErrWorkerNotExited, "it still did not exit in time; that stays true")
		assert.ErrorContains(t, err, "signal: killed",
			"the wait error we just consumed should be reported, not discarded")

		// The receive consumed the value. A third call that re-derived from
		// scratch would find the channel empty and wrongly re-add the sentinel:
		// the same read-once-replay-as-current mistake this fix exists to
		// correct, reappearing inside the fix.
		for i := range 3 {
			assert.NotErrorIs(t, tr.Close(), ErrWorkerUnreaped, "call %d did not latch", i)
		}
	})

	t.Run("leaves a clean close alone", func(t *testing.T) {
		tr := &StdioTransport{waited: make(chan error, 1)}
		tr.closeOnce.Do(func() {})
		// closeErrIfReaped stays nil: nothing to revise.
		assert.NoError(t, tr.Close())
		assert.NoError(t, tr.Close())
	})
}

// A worker can exit promptly and still leave a child holding the stderr pipe,
// and waiting on the worker does not finish until that pipe closes. Without a
// bound on the post-exit wait, Close sits until the grace period, kills a
// process that already exited, and reports it as not having exited.
func TestStdioTransport_CloseReturnsCleanlyWhenAChildOutlivesTheWorker(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "forking-exiter.sh")
	script := "#!/bin/sh\n" +
		"IFS= read -r _line\n" +
		"printf '%s\\n' '{\"id\":\"req-1\",\"ok\":true,\"result\":{\"protocol_version\":1}}'\n" +
		"sleep 30 &\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))

	transport, err := NewStdioTransport(fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = transport.Close() })

	_, err = transport.Call(context.Background(), "ping", json.RawMessage("null"))
	require.NoError(t, err)

	start := time.Now()
	closeErr := make(chan error, 1)
	go func() { closeErr <- transport.Close() }()

	select {
	case err := <-closeErr:
		assert.NotErrorIs(t, err, ErrWorkerNotExited,
			"the worker exited on its own; only its child lingered")
		assert.Less(t, time.Since(start), closeGracePeriod,
			"Close waited for the grace period on a worker that had already exited")
	case <-time.After(25 * time.Second):
		t.Fatal("Close did not return; the post-exit wait is unbounded")
	}
}

// drainTail keeps Close from abandoning a reader that is blocked on a full
// channel, and must give up rather than block if the reader never stops.
func TestDrainTail(t *testing.T) {
	t.Run("returns once the reader closes the channel", func(t *testing.T) {
		lines := make(chan json.RawMessage, 4)
		lines <- json.RawMessage(`{"a":1}`)
		lines <- json.RawMessage(`{"b":2}`)
		close(lines)

		require.NoError(t, drainTail(lines))
	})

	t.Run("gives up when the reader does not stop", func(t *testing.T) {
		// Never written to and never closed, standing in for a reader that
		// outlived the process it was reading.
		lines := make(chan json.RawMessage)

		start := time.Now()
		err := drainTail(lines)

		require.ErrorIs(t, err, errReaderStillRunning)
		assert.GreaterOrEqual(t, time.Since(start), drainTailBound,
			"it must wait the bound out rather than returning early")
	})

	t.Run("a nil channel is nothing to drain", func(t *testing.T) {
		require.NoError(t, drainTail(nil))
	})
}
