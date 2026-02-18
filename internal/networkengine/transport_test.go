package networkengine

import (
	"context"
	"encoding/json"
	"os"
	"testing"

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

// findWorkerBinary returns the path to the compiled Rust worker binary.
func findWorkerBinary(t *testing.T) string {
	t.Helper()
	path := "../../engine/target/debug/network-engine-worker"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("worker binary not found at %s (run 'cargo build --workspace' in engine/)", path)
	}
	return path
}
