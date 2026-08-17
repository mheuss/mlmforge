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
// Spawns the worker and verifies it responds to ping. Transport options are
// forwarded to the underlying StdioTransport, so a caller can opt into
// observability with WithSignalHandler(observer.HandleSignal). Observability
// stays opt-in — no handler is installed by default.
func NewEngineClient(ctx context.Context, binaryPath string, opts ...TransportOption) (*EngineClient, error) {
	transport, err := NewStdioTransport(binaryPath, opts...)
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

// callInto calls an engine operation and unmarshals the result into T.
// Use for operations that return a typed response.
func callInto[T any](c *EngineClient, ctx context.Context, op string, params any) (T, error) {
	var zero T
	result, err := c.call(ctx, op, params)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(result, &out); err != nil {
		return zero, fmt.Errorf("unmarshal %s: %w", op, err)
	}
	return out, nil
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
// It is the normal matrix path: the live consumer and reload both route
// through it, and it is the path an admin override would take to bypass
// spillover.
//
// The leading parameters match AddNode so the two read the same way at a
// call site. parentID and sponsorID are both strings, so transposing them
// still compiles. TestEngineClient_AddNodeAt_WireParams pins the mapping
// inside this method; call sites need their own assertion.
func (c *EngineClient) AddNodeAt(ctx context.Context, structure, userID, parentID, sponsorID string, position int, enrolledAt int64) error {
	_, err := c.call(ctx, "add_node_at", map[string]any{
		"structure":   structure,
		"user_id":     userID,
		"parent_id":   parentID,
		"sponsor_id":  sponsorID,
		"position":    position,
		"enrolled_at": enrolledAt,
	})
	return err
}

// RemoveMatrixNode removes a node from a matrix tree using the specified pruning mode.
// pruningMode must be "promote_earliest" or "holding_tank".
func (c *EngineClient) RemoveMatrixNode(ctx context.Context, structure, userID, pruningMode string) (*MatrixRemovalResult, error) {
	r, err := callInto[MatrixRemovalResult](c, ctx, "remove_node", map[string]any{
		"structure":    structure,
		"user_id":      userID,
		"pruning_mode": pruningMode,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
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
	return callInto[[]HoldingTankEntryDTO](c, ctx, "get_holding_tank", params)
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
	return callInto[[]EngineNode](c, ctx, "get_children", map[string]string{
		"structure": structure,
		"user_id":   userID,
	})
}

// GetUpline returns ancestors from closest to farthest.
// Depth 0 means unlimited. Depth N returns at most N ancestors.
func (c *EngineClient) GetUpline(ctx context.Context, structure, userID string, depth uint32) ([]EngineNode, error) {
	return callInto[[]EngineNode](c, ctx, "get_upline", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"depth":     depth,
	})
}

// GetDownline returns descendants in breadth-first order.
// Depth 0 means unlimited. Depth N returns descendants up to N levels deep.
func (c *EngineClient) GetDownline(ctx context.Context, structure, userID string, depth uint32) ([]EngineNode, error) {
	return callInto[[]EngineNode](c, ctx, "get_downline", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"depth":     depth,
	})
}

// GetPosition returns a full position snapshot for a user, including
// derived data like downline counts and child count.
func (c *EngineClient) GetPosition(ctx context.Context, structure, userID string) (*EnginePosition, error) {
	pos, err := callInto[EnginePosition](c, ctx, "get_position", map[string]string{
		"structure": structure,
		"user_id":   userID,
	})
	if err != nil {
		return nil, err
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
	return callInto[[]EngineNode](c, ctx, "get_sponsor_upline", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"depth":     depth,
	})
}

// GetSponsored returns the nodes directly sponsored by the given user.
func (c *EngineClient) GetSponsored(ctx context.Context, structure, userID string) ([]EngineNode, error) {
	return callInto[[]EngineNode](c, ctx, "get_sponsored", map[string]string{
		"structure": structure,
		"user_id":   userID,
	})
}

// --- Commission calculation ---

// CalculateUnilevel runs commission calculation for a unilevel structure.
// Sends snapshots and volume to the engine and returns the earnings.
func (c *EngineClient) CalculateUnilevel(ctx context.Context, req CalculateUnilevelRequest) ([]CommissionEarningDTO, error) {
	return callInto[[]CommissionEarningDTO](c, ctx, "calculate_unilevel", req)
}

// CalculateGeneration runs commission calculation for a generation structure.
// Sends snapshots and volume to the engine and returns the earnings.
// Generation runs on a unilevel tree, so a tree-type mismatch error names
// "unilevel" (e.g. "is a binary tree, not a unilevel tree").
func (c *EngineClient) CalculateGeneration(ctx context.Context, req CalculateGenerationRequest) ([]CommissionEarningDTO, error) {
	return callInto[[]CommissionEarningDTO](c, ctx, "calculate_generation", req)
}

// CalculateMatrix runs commission calculation for a matrix structure.
// Sends snapshots and volume to the engine and returns the earnings.
func (c *EngineClient) CalculateMatrix(ctx context.Context, req CalculateMatrixRequest) ([]CommissionEarningDTO, error) {
	return callInto[[]CommissionEarningDTO](c, ctx, "calculate_matrix", req)
}

// CalculateStairstep runs commission calculation for a stairstep structure.
// Sends snapshots and volume to the engine and returns the earnings.
// Stairstep runs on a unilevel tree, so a tree-type mismatch error names
// "unilevel" (e.g. "is a binary tree, not a unilevel tree").
func (c *EngineClient) CalculateStairstep(ctx context.Context, req CalculateStairstepRequest) ([]CommissionEarningDTO, error) {
	return callInto[[]CommissionEarningDTO](c, ctx, "calculate_stairstep", req)
}

// CalculateBinaryPairing runs binary pairing commission calculation.
// Sends snapshots, volume, and optional carry-forward state to the engine.
// Returns earnings and updated carry-forward state.
func (c *EngineClient) CalculateBinaryPairing(ctx context.Context, req CalculateBinaryPairingRequest) (*BinaryCalculationResultDTO, error) {
	r, err := callInto[BinaryCalculationResultDTO](c, ctx, "calculate_binary_pairing", req)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// EvaluateRanks asks the worker to compute the qualified rank for every
// distributor in the input set against the loaded plan.
//
// By default the call is stateless (current behavior). Passing
// WithPersistence(periodID, store) causes the result to be persisted to
// the supplied QualificationHistoryStore as a complete replacement of any
// prior result for periodID, in one transaction.
//
// Error contract:
//   - Engine failure: returns (nil, error).
//   - Engine success, persistence not requested: returns (result, nil).
//   - Engine success, store write success: returns (result, nil).
//   - Engine success, store write failure: returns (result, wrapped error).
//     The caller has the engine result and can decide whether to retry the
//     store write. NFR4 guarantees no half-replaced data — the prior
//     period rows are intact.
//   - WithPersistence with empty periodID or nil store: returns (result,
//     wrapped error). No write attempted.
func (c *EngineClient) EvaluateRanks(
	ctx context.Context,
	req EvaluateRanksRequest,
	opts ...EvaluateRanksOption,
) (*EvaluationResultDTO, error) {
	var cfg evaluateRanksConfig
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	r, err := callInto[EvaluationResultDTO](c, ctx, "evaluate_ranks", req)
	if err != nil {
		return nil, err
	}
	if !cfg.persistRequested {
		return &r, nil
	}
	if cfg.persistPeriodID == "" {
		return &r, fmt.Errorf("evaluate_ranks: WithPersistence requires non-empty period_id")
	}
	if cfg.persistStore == nil {
		return &r, fmt.Errorf("evaluate_ranks: WithPersistence requires non-nil store")
	}
	entries, err := evaluationResultToHistoryEntries(&r)
	if err != nil {
		return &r, fmt.Errorf("evaluate_ranks: build history entries: %w", err)
	}
	if err := cfg.persistStore.SaveResult(ctx, cfg.persistPeriodID, entries); err != nil {
		return &r, fmt.Errorf("evaluate_ranks succeeded but history save failed: %w", err)
	}
	return &r, nil
}

// EvaluateRanksOption configures optional behavior of EvaluateRanks.
type EvaluateRanksOption interface {
	apply(*evaluateRanksConfig)
}

type evaluateRanksConfig struct {
	persistRequested bool
	persistPeriodID  string
	persistStore     QualificationHistoryStore
}

// WithPersistence directs EvaluateRanks to persist the result to store
// under periodID after a successful evaluation. periodID must be non-empty
// and a sortable, zero-padded string (e.g., "2026-05", "2026-W21"). store
// must be non-nil. The option is recorded as "persistence requested" even
// when periodID or store are zero values, so an invalid WithPersistence
// call surfaces a wrapped error rather than silently no-op'ing.
func WithPersistence(periodID string, store QualificationHistoryStore) EvaluateRanksOption {
	return persistenceOption{periodID: periodID, store: store}
}

type persistenceOption struct {
	periodID string
	store    QualificationHistoryStore
}

func (o persistenceOption) apply(c *evaluateRanksConfig) {
	c.persistRequested = true
	c.persistPeriodID = o.periodID
	c.persistStore = o.store
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
	r, err := callInto[BoardAddMemberResultDTO](c, ctx, "board_add_member", map[string]any{
		"structure":  structure,
		"user_id":    userID,
		"sponsor_id": sponsorID,
		"timestamp":  timestamp,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// BoardRemoveMember removes a member from a board plan structure.
// Remaining members compact upward to fill the gap.
func (c *EngineClient) BoardRemoveMember(ctx context.Context, structure, userID string, timestamp int64) (*BoardRemoveMemberResultDTO, error) {
	r, err := callInto[BoardRemoveMemberResultDTO](c, ctx, "board_remove_member", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"timestamp": timestamp,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// BoardCompressInactive compresses inactive members out of their boards.
// Returns which members were removed and any cycle events from compaction.
func (c *EngineClient) BoardCompressInactive(ctx context.Context, structure string, memberIDs []string, timestamp int64) (*CompressionResultDTO, error) {
	r, err := callInto[CompressionResultDTO](c, ctx, "board_compress_inactive", map[string]any{
		"structure":  structure,
		"member_ids": memberIDs,
		"timestamp":  timestamp,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// BoardDetectStalled finds boards with no activity since the cutoff timestamp.
func (c *EngineClient) BoardDetectStalled(ctx context.Context, structure string, cutoffTimestamp int64) ([]StalledBoardDTO, error) {
	return callInto[[]StalledBoardDTO](c, ctx, "board_detect_stalled", map[string]any{
		"structure":        structure,
		"cutoff_timestamp": cutoffTimestamp,
	})
}

// BoardDissolve dissolves a stalled board, displacing its members.
func (c *EngineClient) BoardDissolve(ctx context.Context, structure, boardID string, timestamp int64) (*DissolutionResultDTO, error) {
	r, err := callInto[DissolutionResultDTO](c, ctx, "board_dissolve", map[string]any{
		"structure": structure,
		"board_id":  boardID,
		"timestamp": timestamp,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
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
	return callInto[[]BoardSummaryDTO](c, ctx, "board_list", map[string]any{
		"structure": structure,
	})
}

// CalculateBoardCommissions computes cycle commissions for a set of board cycle events.
// The named structure's cycling config comes from the loaded plan, so LoadPlan
// must run first. Pass cycle events and prior counts.
func (c *EngineClient) CalculateBoardCommissions(ctx context.Context, req CalculateBoardCommissionsRequest) (*BoardCommissionResultDTO, error) {
	r, err := callInto[BoardCommissionResultDTO](c, ctx, "board_calculate_commissions", req)
	if err != nil {
		return nil, err
	}
	return &r, nil
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
	r, err := callInto[StreamlineAddMemberResultDTO](c, ctx, "streamline_add_member", params)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// StreamlineRemoveMember removes a member from all streams.
func (c *EngineClient) StreamlineRemoveMember(ctx context.Context, structure, userID string, timestamp int64) (*StreamlineRemoveMemberResultDTO, error) {
	r, err := callInto[StreamlineRemoveMemberResultDTO](c, ctx, "streamline_remove_member", map[string]any{
		"structure": structure,
		"user_id":   userID,
		"timestamp": timestamp,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// StreamlineExpandStreams expands a user's stream count on rank promotion.
func (c *EngineClient) StreamlineExpandStreams(ctx context.Context, structure string, req StreamlineExpandRequest) (*StreamlineExpandResultDTO, error) {
	r, err := callInto[StreamlineExpandResultDTO](c, ctx, "streamline_expand_streams", map[string]any{
		"structure":     structure,
		"user_id":       req.UserID,
		"total_allowed": req.TotalAllowed,
		"timestamp":     req.Timestamp,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// StreamlineUpdateAllowance freezes/unfreezes streams on rank change.
func (c *EngineClient) StreamlineUpdateAllowance(ctx context.Context, structure string, req StreamlineUpdateAllowanceRequest) (*StreamlineFreezeResultDTO, error) {
	r, err := callInto[StreamlineFreezeResultDTO](c, ctx, "streamline_update_allowance", map[string]any{
		"structure":     structure,
		"user_id":       req.UserID,
		"total_allowed": req.TotalAllowed,
		"timestamp":     req.Timestamp,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// StreamlineListStreams returns summaries of all streams.
func (c *EngineClient) StreamlineListStreams(ctx context.Context, structure string) ([]StreamSummaryDTO, error) {
	return callInto[[]StreamSummaryDTO](c, ctx, "streamline_list_streams", map[string]any{
		"structure": structure,
	})
}

// StreamlineGetMember returns a member's positions across all streams.
func (c *EngineClient) StreamlineGetMember(ctx context.Context, structure, userID string) (*StreamlineMemberInfoDTO, error) {
	r, err := callInto[StreamlineMemberInfoDTO](c, ctx, "streamline_get_member", map[string]any{
		"structure": structure,
		"user_id":   userID,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// StreamlineGetStream returns a single stream's summary.
func (c *EngineClient) StreamlineGetStream(ctx context.Context, structure string, streamID int) (*StreamSummaryDTO, error) {
	r, err := callInto[StreamSummaryDTO](c, ctx, "streamline_get_stream", map[string]any{
		"structure": structure,
		"stream_id": streamID,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CalculateStreamline runs streamline commission calculation.
// Requires LoadPlan first: the plan and structure config come from worker state,
// not the request (HEU-583). Without a loaded plan the worker returns NO_PLAN.
func (c *EngineClient) CalculateStreamline(ctx context.Context, req CalculateStreamlineRequest) ([]CommissionEarningDTO, error) {
	return callInto[[]CommissionEarningDTO](c, ctx, "calculate_streamline", req)
}

// TakeSnapshot serializes a structure's state for persistence.
// Returns the snapshot result containing tree type and serialized data.
func (c *EngineClient) TakeSnapshot(ctx context.Context, structure string) (*SnapshotResultDTO, error) {
	r, err := callInto[SnapshotResultDTO](c, ctx, "take_snapshot", map[string]any{
		"structure": structure,
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
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
