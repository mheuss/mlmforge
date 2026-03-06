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

// --- Tree lifecycle methods ---

// CreateTree creates a named tree instance in the engine.
// treeType must be "unilevel" or "binary".
// For matrix trees, use CreateMatrixTree instead.
func (c *EngineClient) CreateTree(ctx context.Context, structure, treeType string) error {
	_, err := c.call(ctx, "create_tree", map[string]string{
		"structure": structure,
		"tree_type": treeType,
	})
	return err
}

// CreateMatrixTree creates a named matrix tree instance in the engine.
// Width is the fixed number of child slots per node (must be >= 2).
// Spillover must be "breadth_first".
func (c *EngineClient) CreateMatrixTree(ctx context.Context, structure string, width int, spillover string) error {
	_, err := c.call(ctx, "create_tree", map[string]any{
		"structure": structure,
		"tree_type": "matrix",
		"width":     width,
		"spillover": spillover,
	})
	return err
}

// --- Tree mutation methods ---

// AddRoot creates the root node of a tree in the engine.
func (c *EngineClient) AddRoot(ctx context.Context, structure, userID string, enrolledAt int64) error {
	_, err := c.call(ctx, "add_root", map[string]any{
		"structure":   structure,
		"user_id":     userID,
		"enrolled_at": enrolledAt,
	})
	return err
}

// AddNodeOption configures optional parameters for AddNode.
type AddNodeOption func(map[string]any)

// WithPosition sets the child position (required for binary trees).
func WithPosition(position int) AddNodeOption {
	return func(params map[string]any) {
		params["position"] = position
	}
}

// AddNode adds a child node under parentID in the engine's tree.
// For binary trees, use WithPosition to specify the slot.
func (c *EngineClient) AddNode(ctx context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error {
	params := map[string]any{
		"structure":   structure,
		"user_id":     userID,
		"parent_id":   parentID,
		"sponsor_id":  sponsorID,
		"enrolled_at": enrolledAt,
	}
	for _, opt := range opts {
		opt(params)
	}
	_, err := c.call(ctx, "add_node", params)
	return err
}

// RemoveNode removes a leaf node from the tree. The Rust engine
// rejects removal of nodes that have children.
func (c *EngineClient) RemoveNode(ctx context.Context, structure, userID string) error {
	_, err := c.call(ctx, "remove_node", map[string]any{
		"structure": structure,
		"user_id":   userID,
	})
	return err
}

// AddMatrixNode adds a node to a matrix tree using automatic spillover placement.
// The engine determines the placement parent via BFS within the sponsor's subtree.
func (c *EngineClient) AddMatrixNode(ctx context.Context, structure, userID, sponsorID string, enrolledAt int64) error {
	_, err := c.call(ctx, "add_node", map[string]any{
		"structure":   structure,
		"user_id":     userID,
		"sponsor_id":  sponsorID,
		"enrolled_at": enrolledAt,
	})
	return err
}

// AddNodeAt places a node at an explicit position in a matrix tree.
// This is an admin override that bypasses spillover.
func (c *EngineClient) AddNodeAt(ctx context.Context, structure, userID, sponsorID, parentID string, position int, enrolledAt int64) error {
	_, err := c.call(ctx, "add_node_at", map[string]any{
		"structure":   structure,
		"user_id":     userID,
		"sponsor_id":  sponsorID,
		"parent_id":   parentID,
		"position":    position,
		"enrolled_at": enrolledAt,
	})
	return err
}

// RemoveMatrixNode removes a node from a matrix tree using the specified pruning mode.
// pruningMode must be "promote_earliest" or "holding_tank".
func (c *EngineClient) RemoveMatrixNode(ctx context.Context, structure, userID, pruningMode string) (*MatrixRemovalResult, error) {
	result, err := c.call(ctx, "remove_node", map[string]any{
		"structure":    structure,
		"user_id":      userID,
		"pruning_mode": pruningMode,
	})
	if err != nil {
		return nil, err
	}
	var removal MatrixRemovalResult
	if err := json.Unmarshal(result, &removal); err != nil {
		return nil, fmt.Errorf("unmarshal removal result: %w", err)
	}
	return &removal, nil
}

// PlaceFromTank moves a holding tank entry back into the matrix tree
// at the specified parent and position.
func (c *EngineClient) PlaceFromTank(ctx context.Context, structure, userID, parentID string, position int) error {
	_, err := c.call(ctx, "place_from_tank", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"parent_id": parentID,
		"position":  position,
	})
	return err
}

// GetHoldingTank returns holding tank entries for a matrix tree.
// If sponsorID is non-empty, only entries sponsored by that user are returned.
func (c *EngineClient) GetHoldingTank(ctx context.Context, structure, sponsorID string) ([]HoldingTankEntryDTO, error) {
	params := map[string]any{
		"structure": structure,
	}
	if sponsorID != "" {
		params["sponsor_id"] = sponsorID
	}
	result, err := c.call(ctx, "get_holding_tank", params)
	if err != nil {
		return nil, err
	}
	var entries []HoldingTankEntryDTO
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("unmarshal holding tank: %w", err)
	}
	return entries, nil
}

// --- Tree query methods ---

// GetParent returns the parent of a node, or nil if the node is the root.
func (c *EngineClient) GetParent(ctx context.Context, structure, userID string) (*EngineNode, error) {
	result, err := c.call(ctx, "get_parent", map[string]string{
		"structure": structure,
		"user_id":   userID,
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
func (c *EngineClient) GetChildren(ctx context.Context, structure, userID string) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_children", map[string]string{
		"structure": structure,
		"user_id":   userID,
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
func (c *EngineClient) GetUpline(ctx context.Context, structure, userID string, depth uint32) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_upline", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"depth":     depth,
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
func (c *EngineClient) GetDownline(ctx context.Context, structure, userID string, depth uint32) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_downline", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"depth":     depth,
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
func (c *EngineClient) GetPosition(ctx context.Context, structure, userID string) (*EnginePosition, error) {
	result, err := c.call(ctx, "get_position", map[string]string{
		"structure": structure,
		"user_id":   userID,
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
func (c *EngineClient) IsDescendantOf(ctx context.Context, structure, userID, ancestorID string) (bool, error) {
	result, err := c.call(ctx, "is_descendant_of", map[string]string{
		"structure":   structure,
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

// --- Sponsor query methods ---

// GetSponsor returns the sponsor of a node, or nil if the node has no sponsor.
func (c *EngineClient) GetSponsor(ctx context.Context, structure, userID string) (*EngineNode, error) {
	result, err := c.call(ctx, "get_sponsor", map[string]string{
		"structure": structure,
		"user_id":   userID,
	})
	if err != nil {
		return nil, err
	}

	// The Rust handler returns JSON null for the root node (no sponsor).
	if string(result) == "null" {
		return nil, nil
	}

	var node EngineNode
	if err := json.Unmarshal(result, &node); err != nil {
		return nil, fmt.Errorf("unmarshal sponsor: %w", err)
	}
	return &node, nil
}

// GetSponsorUpline returns sponsor ancestors from closest to farthest.
// Depth 0 means unlimited. Depth N returns at most N sponsor ancestors.
func (c *EngineClient) GetSponsorUpline(ctx context.Context, structure, userID string, depth uint32) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_sponsor_upline", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"depth":     depth,
	})
	if err != nil {
		return nil, err
	}

	var nodes []EngineNode
	if err := json.Unmarshal(result, &nodes); err != nil {
		return nil, fmt.Errorf("unmarshal sponsor upline: %w", err)
	}
	return nodes, nil
}

// GetSponsored returns the nodes directly sponsored by the given user.
func (c *EngineClient) GetSponsored(ctx context.Context, structure, userID string) ([]EngineNode, error) {
	result, err := c.call(ctx, "get_sponsored", map[string]string{
		"structure": structure,
		"user_id":   userID,
	})
	if err != nil {
		return nil, err
	}

	var nodes []EngineNode
	if err := json.Unmarshal(result, &nodes); err != nil {
		return nil, fmt.Errorf("unmarshal sponsored: %w", err)
	}
	return nodes, nil
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

// CalculateBinaryPairing runs binary pairing commission calculation.
// Sends snapshots, volume, and optional carry-forward state to the engine.
// Returns earnings and updated carry-forward state.
func (c *EngineClient) CalculateBinaryPairing(ctx context.Context, req CalculateBinaryPairingRequest) (*BinaryCalculationResultDTO, error) {
	result, err := c.call(ctx, "calculate_binary_pairing", req)
	if err != nil {
		return nil, err
	}
	var calcResult BinaryCalculationResultDTO
	if err := json.Unmarshal(result, &calcResult); err != nil {
		return nil, fmt.Errorf("unmarshal binary calculation result: %w", err)
	}
	return &calcResult, nil
}
