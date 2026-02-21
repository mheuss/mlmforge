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
)

// ErrTransportClosed is returned when Call is invoked on a closed transport.
var ErrTransportClosed = errors.New("transport is closed")

// Compile-time check: StdioTransport implements EngineTransport.
var _ EngineTransport = (*StdioTransport)(nil)

// StdioTransport communicates with the Rust worker via stdin/stdout NDJSON.
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	stderr bytes.Buffer
	mu     sync.Mutex
	nextID atomic.Int64
	closed atomic.Bool
}

type protocolRequest struct {
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params"`
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

// readResult holds the outcome of a ReadBytes call run in a goroutine.
type readResult struct {
	line []byte
	err  error
}

// NewStdioTransport spawns the Rust worker binary and returns a transport
// that communicates with it via NDJSON over stdin/stdout.
func NewStdioTransport(binaryPath string) (*StdioTransport, error) {
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
		cmd:    cmd,
		stdin:  stdin,
		reader: bufio.NewReader(stdout),
	}
	cmd.Stderr = &transport.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	return transport, nil
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
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := fmt.Fprintf(t.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	ch := make(chan readResult, 1)
	// On context cancellation, this goroutine remains blocked on ReadBytes
	// until the worker writes a response or the process exits. The transport
	// is marked closed after cancellation, preventing further calls.
	// Residual goroutine cleanup occurs when Close() kills the subprocess.
	go func() {
		line, readErr := t.reader.ReadBytes('\n')
		ch <- readResult{line, readErr}
	}()

	var line []byte
	select {
	case res := <-ch:
		if res.err != nil {
			stderrOut := t.stderr.String()
			if stderrOut != "" {
				return nil, fmt.Errorf("read response: %w\nworker stderr: %s", res.err, stderrOut)
			}
			return nil, fmt.Errorf("read response: %w", res.err)
		}
		line = res.line
	case <-ctx.Done():
		// The goroutine reading from t.reader is still blocked and will remain
		// so until the worker writes a response. If we allowed another Call,
		// two goroutines would read from the same bufio.Reader, causing a data
		// race. Mark the transport as unusable so subsequent calls fail cleanly.
		t.closed.Store(true)
		return nil, ctx.Err()
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
