package networkengine

import (
	"context"
	"encoding/json"
	"fmt"
)

// EngineClient manages the Rust Network Engine subprocess and provides
// typed methods for all engine operations.
type EngineClient struct {
	transport EngineTransport
}

// NewEngineClient creates a client backed by a subprocess at binaryPath.
// Spawns the worker and verifies it responds to ping.
func NewEngineClient(ctx context.Context, binaryPath string) (*EngineClient, error) {
	transport, err := NewStdioTransport(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("create transport: %w", err)
	}
	client := &EngineClient{transport: transport}

	// Verify the worker is alive.
	if err := client.Ping(ctx); err != nil {
		transport.Close()
		return nil, fmt.Errorf("initial ping failed: %w", err)
	}

	return client, nil
}

// NewEngineClientWithTransport creates a client with a custom transport.
// Used for testing with mock transports.
func NewEngineClientWithTransport(transport EngineTransport) *EngineClient {
	return &EngineClient{transport: transport}
}

// Ping sends a ping to the worker and returns an error if it does not respond.
func (c *EngineClient) Ping(ctx context.Context) error {
	_, err := c.transport.Call(ctx, "ping", json.RawMessage("null"))
	return err
}

// LoadPlan sends a compensation plan to the worker.
func (c *EngineClient) LoadPlan(ctx context.Context, planJSON json.RawMessage) error {
	_, err := c.transport.Call(ctx, "load_plan", planJSON)
	return err
}

// Stop shuts down the worker process.
func (c *EngineClient) Stop() error {
	return c.transport.Close()
}

// call is the generic dispatch method. Op-specific methods use this
// to marshal params and delegate to the transport.
func (c *EngineClient) call(ctx context.Context, op string, params any) (json.RawMessage, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	return c.transport.Call(ctx, op, data)
}

// --- Tree mutation methods ---

// AddRoot creates the root node of a unilevel tree in the engine.
func (c *EngineClient) AddRoot(ctx context.Context, userID string, enrolledAt int64) error {
	_, err := c.call(ctx, "add_root", map[string]any{
		"user_id":     userID,
		"enrolled_at": enrolledAt,
	})
	return err
}

// AddNode adds a child node under parentID in the engine's tree.
func (c *EngineClient) AddNode(ctx context.Context, userID, parentID string, enrolledAt int64) error {
	_, err := c.call(ctx, "add_node", map[string]any{
		"user_id":     userID,
		"parent_id":   parentID,
		"enrolled_at": enrolledAt,
	})
	return err
}

// RemoveNode removes a leaf node from the tree. The Rust engine
// rejects removal of nodes that have children.
func (c *EngineClient) RemoveNode(ctx context.Context, userID string) error {
	_, err := c.call(ctx, "remove_node", map[string]any{
		"user_id": userID,
	})
	return err
}

// --- Tree query methods ---

// GetParent returns the parent of a node, or nil if the node is the root.
func (c *EngineClient) GetParent(ctx context.Context, userID string) (*EngineNode, error) {
	result, err := c.call(ctx, "get_parent", map[string]string{
		"user_id": userID,
	})
	if err != nil {
		return nil, err
	}

	// The Rust handler returns JSON null for root nodes.
	if string(result) == "null" {
		return nil, nil
	}

	var node EngineNode
	if err := json.Unmarshal(result, &node); err != nil {
		return nil, fmt.Errorf("unmarshal parent: %w", err)
	}
	return &node, nil
}

// GetChildren returns the direct children of a node in position order.
func (c *EngineClient) GetChildren(ctx context.Context, userID string) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_children", map[string]string{
		"user_id": userID,
	})
	if err != nil {
		return nil, err
	}

	var nodes []EngineNode
	if err := json.Unmarshal(result, &nodes); err != nil {
		return nil, fmt.Errorf("unmarshal children: %w", err)
	}
	return nodes, nil
}

// GetUpline returns ancestors from closest to farthest.
// Depth 0 means unlimited. Depth N returns at most N ancestors.
func (c *EngineClient) GetUpline(ctx context.Context, userID string, depth uint32) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_upline", map[string]any{
		"user_id": userID,
		"depth":   depth,
	})
	if err != nil {
		return nil, err
	}

	var nodes []EngineNode
	if err := json.Unmarshal(result, &nodes); err != nil {
		return nil, fmt.Errorf("unmarshal upline: %w", err)
	}
	return nodes, nil
}

// GetDownline returns descendants in breadth-first order.
// Depth 0 means unlimited. Depth N returns descendants up to N levels deep.
func (c *EngineClient) GetDownline(ctx context.Context, userID string, depth uint32) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_downline", map[string]any{
		"user_id": userID,
		"depth":   depth,
	})
	if err != nil {
		return nil, err
	}

	var nodes []EngineNode
	if err := json.Unmarshal(result, &nodes); err != nil {
		return nil, fmt.Errorf("unmarshal downline: %w", err)
	}
	return nodes, nil
}

// GetPosition returns a full position snapshot for a user, including
// derived data like downline counts and child count.
func (c *EngineClient) GetPosition(ctx context.Context, userID string) (*EnginePosition, error) {
	result, err := c.call(ctx, "get_position", map[string]string{
		"user_id": userID,
	})
	if err != nil {
		return nil, err
	}

	var pos EnginePosition
	if err := json.Unmarshal(result, &pos); err != nil {
		return nil, fmt.Errorf("unmarshal position: %w", err)
	}
	return &pos, nil
}

// IsDescendantOf checks whether userID is a descendant of ancestorID.
func (c *EngineClient) IsDescendantOf(ctx context.Context, userID, ancestorID string) (bool, error) {
	result, err := c.call(ctx, "is_descendant_of", map[string]string{
		"user_id":     userID,
		"ancestor_id": ancestorID,
	})
	if err != nil {
		return false, err
	}

	var resp struct {
		IsDescendant bool `json:"is_descendant"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return false, fmt.Errorf("unmarshal is_descendant: %w", err)
	}
	return resp.IsDescendant, nil
}

// --- Commission calculation ---

// CalculateUnilevel runs commission calculation for a unilevel structure.
// Sends snapshots and volume to the engine and returns the earnings.
func (c *EngineClient) CalculateUnilevel(ctx context.Context, req CalculateUnilevelRequest) ([]CommissionEarningDTO, error) {
	result, err := c.call(ctx, "calculate_unilevel", req)
	if err != nil {
		return nil, err
	}
	var earnings []CommissionEarningDTO
	if err := json.Unmarshal(result, &earnings); err != nil {
		return nil, fmt.Errorf("unmarshal earnings: %w", err)
	}
	return earnings, nil
}
