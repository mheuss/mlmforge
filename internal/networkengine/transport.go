package networkengine

import (
	"context"
	"encoding/json"
)

// EngineTransport abstracts communication with the Rust Network Engine.
// StdioTransport talks to a subprocess. GRPCTransport (future) talks over the network.
type EngineTransport interface {
	Call(ctx context.Context, op string, params json.RawMessage) (json.RawMessage, error)
	Close() error
}
