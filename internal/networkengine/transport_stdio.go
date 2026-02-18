package networkengine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Compile-time check: StdioTransport implements EngineTransport.
var _ EngineTransport = (*StdioTransport)(nil)

// StdioTransport communicates with the Rust worker via stdin/stdout NDJSON.
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	mu     sync.Mutex
	nextID atomic.Int64
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
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	return &StdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		reader: bufio.NewReader(stdout),
	}, nil
}

// Call sends an operation to the Rust worker and waits for the response.
// The mutex ensures only one request is in flight at a time.
func (t *StdioTransport) Call(ctx context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := fmt.Sprintf("req-%d", t.nextID.Add(1))

	req := protocolRequest{ID: id, Op: op, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := fmt.Fprintf(t.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	line, err := t.reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp protocolResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if !resp.OK {
		code := "UNKNOWN"
		msg := "unknown error"
		if resp.Error != nil {
			code = resp.Error.Code
			msg = resp.Error.Message
		}
		return nil, fmt.Errorf("engine error [%s]: %s", code, msg)
	}

	return resp.Result, nil
}

// Close shuts down the worker process by closing stdin and waiting for exit.
func (t *StdioTransport) Close() error {
	stdinErr := t.stdin.Close()
	waitErr := t.cmd.Wait()
	if waitErr != nil {
		return waitErr
	}
	return stdinErr
}
