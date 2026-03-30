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
		_ = transport.Close()
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
// Spillover must be "breadth_first" (the engine rejects "depth_first").
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

// CalculateGeneration runs commission calculation for a generation structure.
// Sends snapshots and volume to the engine and returns the earnings.
func (c *EngineClient) CalculateGeneration(ctx context.Context, req CalculateGenerationRequest) ([]CommissionEarningDTO, error) {
	result, err := c.call(ctx, "calculate_generation", req)
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

// --- Board plan methods ---

// CreateBoardPlan creates a board plan structure in the engine.
// Width and height define the board dimensions. Config is the raw JSON
// for BoardPlanConfig (cycle commission, re-entry rules, etc.).
func (c *EngineClient) CreateBoardPlan(ctx context.Context, structure string, width, height int, config json.RawMessage) error {
	_, err := c.call(ctx, "create_board_plan", map[string]any{
		"structure": structure,
		"width":     width,
		"height":    height,
		"config":    json.RawMessage(config),
	})
	return err
}

// BoardAddMember adds a member to a board plan structure.
// The engine places the member using BFS into the oldest board with space.
func (c *EngineClient) BoardAddMember(ctx context.Context, structure, userID, sponsorID string, timestamp int64) (*BoardAddMemberResultDTO, error) {
	result, err := c.call(ctx, "board_add_member", map[string]any{
		"structure":  structure,
		"user_id":    userID,
		"sponsor_id": sponsorID,
		"timestamp":  timestamp,
	})
	if err != nil {
		return nil, err
	}
	var addResult BoardAddMemberResultDTO
	if err := json.Unmarshal(result, &addResult); err != nil {
		return nil, fmt.Errorf("unmarshal board add member result: %w", err)
	}
	return &addResult, nil
}

// BoardRemoveMember removes a member from a board plan structure.
// Remaining members compact upward to fill the gap.
func (c *EngineClient) BoardRemoveMember(ctx context.Context, structure, userID string, timestamp int64) (*BoardRemoveMemberResultDTO, error) {
	result, err := c.call(ctx, "board_remove_member", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"timestamp": timestamp,
	})
	if err != nil {
		return nil, err
	}
	var removeResult BoardRemoveMemberResultDTO
	if err := json.Unmarshal(result, &removeResult); err != nil {
		return nil, fmt.Errorf("unmarshal board remove member result: %w", err)
	}
	return &removeResult, nil
}

// BoardCompressInactive compresses inactive members out of their boards.
// Returns which members were removed and any cycle events from compaction.
func (c *EngineClient) BoardCompressInactive(ctx context.Context, structure string, memberIDs []string, timestamp int64) (*CompressionResultDTO, error) {
	result, err := c.call(ctx, "board_compress_inactive", map[string]any{
		"structure":  structure,
		"member_ids": memberIDs,
		"timestamp":  timestamp,
	})
	if err != nil {
		return nil, err
	}
	var compResult CompressionResultDTO
	if err := json.Unmarshal(result, &compResult); err != nil {
		return nil, fmt.Errorf("unmarshal compression result: %w", err)
	}
	return &compResult, nil
}

// BoardDetectStalled finds boards with no activity since the cutoff timestamp.
func (c *EngineClient) BoardDetectStalled(ctx context.Context, structure string, cutoffTimestamp int64) ([]StalledBoardDTO, error) {
	result, err := c.call(ctx, "board_detect_stalled", map[string]any{
		"structure":        structure,
		"cutoff_timestamp": cutoffTimestamp,
	})
	if err != nil {
		return nil, err
	}
	var stalled []StalledBoardDTO
	if err := json.Unmarshal(result, &stalled); err != nil {
		return nil, fmt.Errorf("unmarshal stalled boards: %w", err)
	}
	return stalled, nil
}

// BoardDissolve dissolves a stalled board, displacing its members.
func (c *EngineClient) BoardDissolve(ctx context.Context, structure, boardID string, timestamp int64) (*DissolutionResultDTO, error) {
	result, err := c.call(ctx, "board_dissolve", map[string]any{
		"structure": structure,
		"board_id":  boardID,
		"timestamp": timestamp,
	})
	if err != nil {
		return nil, err
	}
	var dissolution DissolutionResultDTO
	if err := json.Unmarshal(result, &dissolution); err != nil {
		return nil, fmt.Errorf("unmarshal dissolution result: %w", err)
	}
	return &dissolution, nil
}

// BoardGetState returns the full state of a board as raw JSON.
func (c *EngineClient) BoardGetState(ctx context.Context, structure, boardID string) (json.RawMessage, error) {
	result, err := c.call(ctx, "board_get_state", map[string]any{
		"structure": structure,
		"board_id":  boardID,
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// BoardGetMember returns which board a member is on, or nil if not found.
func (c *EngineClient) BoardGetMember(ctx context.Context, structure, userID string) (*BoardMemberInfoDTO, error) {
	result, err := c.call(ctx, "board_get_member", map[string]any{
		"structure": structure,
		"user_id":   userID,
	})
	if err != nil {
		return nil, err
	}

	// The handler returns JSON null when the member is not on any board.
	if string(result) == "null" {
		return nil, nil
	}

	var info BoardMemberInfoDTO
	if err := json.Unmarshal(result, &info); err != nil {
		return nil, fmt.Errorf("unmarshal board member info: %w", err)
	}
	return &info, nil
}

// BoardListBoards returns summaries of all boards in a board plan structure.
func (c *EngineClient) BoardListBoards(ctx context.Context, structure string) ([]BoardSummaryDTO, error) {
	result, err := c.call(ctx, "board_list", map[string]any{
		"structure": structure,
	})
	if err != nil {
		return nil, err
	}
	var boards []BoardSummaryDTO
	if err := json.Unmarshal(result, &boards); err != nil {
		return nil, fmt.Errorf("unmarshal board list: %w", err)
	}
	return boards, nil
}

// CalculateBoardCommissions computes cycle commissions for a set of board cycle events.
// This is a stateless calculation: pass cycle events, prior counts, and config.
func (c *EngineClient) CalculateBoardCommissions(ctx context.Context, req CalculateBoardCommissionsRequest) (*BoardCommissionResultDTO, error) {
	result, err := c.call(ctx, "board_calculate_commissions", req)
	if err != nil {
		return nil, err
	}
	var commResult BoardCommissionResultDTO
	if err := json.Unmarshal(result, &commResult); err != nil {
		return nil, fmt.Errorf("unmarshal board commission result: %w", err)
	}
	return &commResult, nil
}

// --- Streamline methods ---

// CreateStreamline creates a streamline structure in the engine.
func (c *EngineClient) CreateStreamline(ctx context.Context, structure string, assignmentMode string, enrollmentStreamChoice bool, freezeOnDemotion bool, timestamp int64) error {
	_, err := c.call(ctx, "create_streamline", map[string]any{
		"structure":                structure,
		"assignment_mode":          assignmentMode,
		"enrollment_stream_choice": enrollmentStreamChoice,
		"freeze_on_demotion":       freezeOnDemotion,
		"timestamp":                timestamp,
	})
	return err
}

// StreamlineAddMember adds a member to a streamline structure.
func (c *EngineClient) StreamlineAddMember(ctx context.Context, structure string, req StreamlineAddMemberRequest) (*StreamlineAddMemberResultDTO, error) {
	params := map[string]any{
		"structure":  structure,
		"user_id":    req.UserID,
		"sponsor_id": req.SponsorID,
		"timestamp":  req.Timestamp,
	}
	if req.StreamIDOverride != nil {
		params["stream_id_override"] = *req.StreamIDOverride
	}
	result, err := c.call(ctx, "streamline_add_member", params)
	if err != nil {
		return nil, err
	}
	var addResult StreamlineAddMemberResultDTO
	if err := json.Unmarshal(result, &addResult); err != nil {
		return nil, fmt.Errorf("unmarshal streamline add member result: %w", err)
	}
	return &addResult, nil
}

// StreamlineRemoveMember removes a member from all streams.
func (c *EngineClient) StreamlineRemoveMember(ctx context.Context, structure, userID string, timestamp int64) (*StreamlineRemoveMemberResultDTO, error) {
	result, err := c.call(ctx, "streamline_remove_member", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"timestamp": timestamp,
	})
	if err != nil {
		return nil, err
	}
	var removeResult StreamlineRemoveMemberResultDTO
	if err := json.Unmarshal(result, &removeResult); err != nil {
		return nil, fmt.Errorf("unmarshal streamline remove member result: %w", err)
	}
	return &removeResult, nil
}

// StreamlineExpandStreams expands a user's stream count on rank promotion.
func (c *EngineClient) StreamlineExpandStreams(ctx context.Context, structure string, req StreamlineExpandRequest) (*StreamlineExpandResultDTO, error) {
	result, err := c.call(ctx, "streamline_expand_streams", map[string]any{
		"structure":     structure,
		"user_id":       req.UserID,
		"total_allowed": req.TotalAllowed,
		"timestamp":     req.Timestamp,
	})
	if err != nil {
		return nil, err
	}
	var expandResult StreamlineExpandResultDTO
	if err := json.Unmarshal(result, &expandResult); err != nil {
		return nil, fmt.Errorf("unmarshal streamline expand result: %w", err)
	}
	return &expandResult, nil
}

// StreamlineUpdateAllowance freezes/unfreezes streams on rank change.
func (c *EngineClient) StreamlineUpdateAllowance(ctx context.Context, structure string, req StreamlineUpdateAllowanceRequest) (*StreamlineFreezeResultDTO, error) {
	result, err := c.call(ctx, "streamline_update_allowance", map[string]any{
		"structure":     structure,
		"user_id":       req.UserID,
		"total_allowed": req.TotalAllowed,
		"timestamp":     req.Timestamp,
	})
	if err != nil {
		return nil, err
	}
	var freezeResult StreamlineFreezeResultDTO
	if err := json.Unmarshal(result, &freezeResult); err != nil {
		return nil, fmt.Errorf("unmarshal streamline freeze result: %w", err)
	}
	return &freezeResult, nil
}

// StreamlineListStreams returns summaries of all streams.
func (c *EngineClient) StreamlineListStreams(ctx context.Context, structure string) ([]StreamSummaryDTO, error) {
	result, err := c.call(ctx, "streamline_list_streams", map[string]any{
		"structure": structure,
	})
	if err != nil {
		return nil, err
	}
	var streams []StreamSummaryDTO
	if err := json.Unmarshal(result, &streams); err != nil {
		return nil, fmt.Errorf("unmarshal streamline list streams: %w", err)
	}
	return streams, nil
}

// StreamlineGetMember returns a member's positions across all streams.
func (c *EngineClient) StreamlineGetMember(ctx context.Context, structure, userID string) (*StreamlineMemberInfoDTO, error) {
	result, err := c.call(ctx, "streamline_get_member", map[string]any{
		"structure": structure,
		"user_id":   userID,
	})
	if err != nil {
		return nil, err
	}
	var info StreamlineMemberInfoDTO
	if err := json.Unmarshal(result, &info); err != nil {
		return nil, fmt.Errorf("unmarshal streamline member info: %w", err)
	}
	return &info, nil
}

// StreamlineGetStream returns a single stream's summary.
func (c *EngineClient) StreamlineGetStream(ctx context.Context, structure string, streamID int) (*StreamSummaryDTO, error) {
	result, err := c.call(ctx, "streamline_get_stream", map[string]any{
		"structure": structure,
		"stream_id": streamID,
	})
	if err != nil {
		return nil, err
	}
	var summary StreamSummaryDTO
	if err := json.Unmarshal(result, &summary); err != nil {
		return nil, fmt.Errorf("unmarshal streamline stream summary: %w", err)
	}
	return &summary, nil
}

// CalculateStreamline runs streamline commission calculation.
func (c *EngineClient) CalculateStreamline(ctx context.Context, req CalculateStreamlineRequest) ([]CommissionEarningDTO, error) {
	result, err := c.call(ctx, "calculate_streamline", req)
	if err != nil {
		return nil, err
	}
	var earnings []CommissionEarningDTO
	if err := json.Unmarshal(result, &earnings); err != nil {
		return nil, fmt.Errorf("unmarshal streamline earnings: %w", err)
	}
	return earnings, nil
}

// TakeSnapshot serializes a structure's state for persistence.
// Returns the snapshot result containing tree type and serialized data.
func (c *EngineClient) TakeSnapshot(ctx context.Context, structure string) (*SnapshotResultDTO, error) {
	result, err := c.call(ctx, "take_snapshot", map[string]any{
		"structure": structure,
	})
	if err != nil {
		return nil, err
	}
	var snapshot SnapshotResultDTO
	if err := json.Unmarshal(result, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return &snapshot, nil
}

// RestoreSnapshot restores a structure from a previously taken snapshot.
func (c *EngineClient) RestoreSnapshot(ctx context.Context, structure, treeType string, data json.RawMessage) error {
	_, err := c.call(ctx, "restore_snapshot", map[string]any{
		"structure": structure,
		"tree_type": treeType,
		"data":      json.RawMessage(data),
	})
	return err
}
