// Package networkengine wire_types.go defines DTOs that mirror the Rust engine's NDJSON wire format.
package networkengine

import "encoding/json"

// EngineNode represents a tree node returned by the Rust engine.
// Mirrors the wire format of the Rust NodeResponse struct.
type EngineNode struct {
	UserID     string `json:"user_id"`
	Depth      uint32 `json:"depth"`
	EnrolledAt int64  `json:"enrolled_at"`
}

// EnginePosition represents a full position snapshot for a user.
// Mirrors the wire format of the Rust get_position response.
type EnginePosition struct {
	UserID         string         `json:"user_id"`
	ParentUserID   *string        `json:"parent_user_id"`
	SponsorUserID  *string        `json:"sponsor_user_id"`
	Position       int            `json:"position"`
	Depth          uint32         `json:"depth"`
	ChildCount     int            `json:"child_count"`
	DownlineCounts map[string]int `json:"downline_counts"`
	EnrolledAt     int64          `json:"enrolled_at"`
}

// CalculateUnilevelRequest is the input for unilevel commission calculation.
// Wire field "structure" matches the Rust CalculateUnilevelParams serde rename.
type CalculateUnilevelRequest struct {
	StructureName string                            `json:"structure"`
	Snapshots     map[string]DistributorSnapshotDTO `json:"snapshots"`
	Volume        []VolumeSourceDTO                 `json:"volume"`
}

// DistributorSnapshotDTO is the wire format for distributor period data.
// Matches the Rust DistributorSnapshot struct.
type DistributorSnapshotDTO struct {
	Rank             string  `json:"rank"`
	PersonalVolume   float64 `json:"personal_volume"`
	Status           string  `json:"status"`
	HasOrderInPeriod bool    `json:"has_order_in_period"`
}

// VolumeSourceDTO is the wire format for a volume event.
// Matches the Rust VolumeSource struct.
type VolumeSourceDTO struct {
	SourceID string  `json:"source_id"`
	CVAmount float64 `json:"cv_amount"`
}

// CommissionEarningDTO is the wire format for a single commission earning.
// Matches the Rust CommissionEarning struct.
type CommissionEarningDTO struct {
	EarnerID     string  `json:"earner_id"`
	SourceID     string  `json:"source_id"`
	Level        int     `json:"level"`
	Rate         float64 `json:"rate"`
	CVAmount     float64 `json:"cv_amount"`
	DollarAmount float64 `json:"dollar_amount"`
}

// CalculateGenerationRequest is the input for generation commission calculation.
// Wire field "structure" matches the Rust CalculateGenerationParams serde rename.
type CalculateGenerationRequest struct {
	StructureName string                            `json:"structure"`
	Snapshots     map[string]DistributorSnapshotDTO `json:"snapshots"`
	Volume        []VolumeSourceDTO                 `json:"volume"`
}

// CalculateMatrixRequest is the input for matrix commission calculation.
// Wire field "structure" matches the Rust CalculateMatrixParams serde rename.
type CalculateMatrixRequest struct {
	StructureName string                            `json:"structure"`
	Snapshots     map[string]DistributorSnapshotDTO `json:"snapshots"`
	Volume        []VolumeSourceDTO                 `json:"volume"`
}

// CalculateStairstepRequest is the input for stairstep commission calculation.
// Wire field "structure" matches the Rust CalculateStairstepParams serde rename.
type CalculateStairstepRequest struct {
	StructureName string                            `json:"structure"`
	Snapshots     map[string]DistributorSnapshotDTO `json:"snapshots"`
	Volume        []VolumeSourceDTO                 `json:"volume"`
}

// CalculateBinaryPairingRequest is the input for binary pairing commission calculation.
// Wire field "structure" matches the Rust CalculateBinaryPairingParams serde rename.
type CalculateBinaryPairingRequest struct {
	StructureName string                            `json:"structure"`
	Snapshots     map[string]DistributorSnapshotDTO `json:"snapshots"`
	Volume        []VolumeSourceDTO                 `json:"volume"`
	CarryForward  map[string]LegVolumesDTO          `json:"carry_forward,omitempty"`
	Ownership     map[string]string                 `json:"ownership,omitempty"`
}

// LegVolumesDTO is the wire format for binary carry-forward leg volumes.
// Matches the Rust LegVolumes struct.
type LegVolumesDTO struct {
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
}

// BinaryCommissionEarningDTO is the wire format for a single binary pairing
// or cycle-step earning. Matches the Rust BinaryCommissionEarning struct.
type BinaryCommissionEarningDTO struct {
	EarnerID      string  `json:"earner_id"`
	PositionID    string  `json:"position_id"`
	LeftVolume    float64 `json:"left_volume"`
	RightVolume   float64 `json:"right_volume"`
	MatchedVolume float64 `json:"matched_volume"`
	// Ratio echoes the balance ratio used for the match: 1.0 for weaker-leg,
	// the leg min/max ratio for volume-ratio mode. Cycle-step mode reports 0.0.
	Ratio float64 `json:"ratio"`
	// Percent echoes the configured pairing commission rate as a fraction in [0, 1] (0.10 = 10%).
	// Cycle-step mode has no per-earning rate and reports 0.0.
	Percent      float64 `json:"percent"`
	DollarAmount float64 `json:"dollar_amount"`
	Capped       bool    `json:"capped"`
}

// BinaryCalculationResultDTO is the wire format for binary pairing calculation results.
// Matches the Rust BinaryCalculationResponse struct.
type BinaryCalculationResultDTO struct {
	Earnings     []BinaryCommissionEarningDTO `json:"earnings"`
	CarryForward map[string]LegVolumesDTO     `json:"carry_forward"`
}

// MatrixRemovalResult is the wire format for matrix node removal results.
// Matches the Rust RemovalResult struct serialized in handle_remove_node.
type MatrixRemovalResult struct {
	Removed      string   `json:"removed"`
	Promoted     *string  `json:"promoted"`
	Repositioned []string `json:"repositioned"`
	MovedToTank  []string `json:"moved_to_tank"`
}

// HoldingTankEntryDTO is the wire format for a matrix holding tank entry.
// Matches the Rust get_holding_tank response items.
type HoldingTankEntryDTO struct {
	UserID        string  `json:"user_id"`
	SponsorUserID *string `json:"sponsor_user_id"`
	EnrolledAt    int64   `json:"enrolled_at"`
}

// --- Board plan wire types ---

// BoardAddMemberResultDTO is the wire format for adding a member to a board.
// Matches the Rust AddMemberResult struct.
type BoardAddMemberResultDTO struct {
	BoardID     string          `json:"board_id"`
	Position    int             `json:"position"`
	CycleEvents []CycleEventDTO `json:"cycle_events"`
}

// CycleEventDTO is the wire format for a board cycle event.
// Matches the Rust CycleEvent struct.
type CycleEventDTO struct {
	BoardID      string   `json:"board_id"`
	CycledMember string   `json:"cycled_member"`
	NewBoards    []string `json:"new_boards"`
	ReEntryBoard *string  `json:"re_entry_board"`
}

// BoardRemoveMemberResultDTO is the wire format for removing a member from a board.
// Matches the Rust RemoveMemberResult struct.
type BoardRemoveMemberResultDTO struct {
	Compacted   []string        `json:"compacted"`
	CycleEvents []CycleEventDTO `json:"cycle_events"`
}

// BoardSummaryDTO is the wire format for a board summary.
// Matches the Rust BoardSummary struct.
type BoardSummaryDTO struct {
	ID             string  `json:"id"`
	FilledCount    int     `json:"filled_count"`
	TotalPositions int     `json:"total_positions"`
	CreatedAt      int64   `json:"created_at"`
	LastActivityAt int64   `json:"last_activity_at"`
	ParentBoardID  *string `json:"parent_board_id"`
}

// StalledBoardDTO is the wire format for a stalled board.
// Matches the Rust StalledBoard struct.
type StalledBoardDTO struct {
	BoardID         string   `json:"board_id"`
	LastActivityAt  int64    `json:"last_activity_at"`
	FilledPositions int      `json:"filled_positions"`
	TotalPositions  int      `json:"total_positions"`
	Members         []string `json:"members"`
}

// DissolutionResultDTO is the wire format for a board dissolution.
// Matches the Rust DissolutionResult struct.
type DissolutionResultDTO struct {
	DissolvedBoardID string   `json:"dissolved_board_id"`
	DisplacedMembers []string `json:"displaced_members"`
}

// CompressionResultDTO is the wire format for inactive compression results.
// Matches the Rust CompressionResult struct.
type CompressionResultDTO struct {
	Compressed  []CompressedMemberDTO `json:"compressed"`
	CycleEvents []CycleEventDTO       `json:"cycle_events"`
}

// CompressedMemberDTO is the wire format for a compressed member entry.
// Matches the Rust CompressedMember struct.
type CompressedMemberDTO struct {
	UserID  string `json:"user_id"`
	BoardID string `json:"board_id"`
}

// BoardMemberInfoDTO is the wire format for a member's board placement.
// Returned by the board_get_member handler.
type BoardMemberInfoDTO struct {
	BoardID string `json:"board_id"`
}

// BoardCycleEarningDTO is the wire format for a single board cycle earning.
// Matches the Rust BoardCycleEarning struct.
type BoardCycleEarningDTO struct {
	EarnerID     string  `json:"earner_id"`
	BoardID      string  `json:"board_id"`
	DollarAmount float64 `json:"dollar_amount"`
	CycleNumber  int     `json:"cycle_number"`
	Capped       bool    `json:"capped"`
}

// BoardCommissionResultDTO is the wire format for board commission calculation results.
// Matches the Rust BoardCommissionResult struct.
type BoardCommissionResultDTO struct {
	Earnings           []BoardCycleEarningDTO `json:"earnings"`
	UpdatedCycleCounts map[string]int         `json:"updated_cycle_counts"`
}

// CalculateBoardCommissionsRequest is the input for board cycle commission calculation.
// The named structure's board_cycling config comes from the loaded plan (HEU-603),
// so LoadPlan must run first.
// Wire field "structure" matches the Rust Params serde rename.
type CalculateBoardCommissionsRequest struct {
	StructureName string          `json:"structure"`
	CycleEvents   []CycleEventDTO `json:"cycle_events"`
	// omitempty because a first period legitimately has no prior counts, and the
	// Rust side pairs serde default with null_as_empty here, so absent and null
	// both mean "no counts". The engine has read null fine since HEU-603 — this
	// tag is about keeping the wire tidy, not about avoiding a rejection.
	// Mirrors CarryForward above.
	PeriodCycleCounts map[string]int `json:"period_cycle_counts,omitempty"`
}

// --- Streamline wire types ---

// StreamlineAddMemberRequest is the input for adding a member to a streamline.
type StreamlineAddMemberRequest struct {
	UserID           string  `json:"user_id"`
	SponsorID        string  `json:"sponsor_id"`
	Timestamp        int64   `json:"timestamp"`
	StreamIDOverride *uint32 `json:"stream_id_override,omitempty"`
}

// StreamlineAddMemberResultDTO is the result of adding a member.
type StreamlineAddMemberResultDTO struct {
	StreamID int `json:"stream_id"`
	Position int `json:"position"`
}

// StreamlineExpandRequest is the input for expanding a user's streams.
type StreamlineExpandRequest struct {
	UserID       string `json:"user_id"`
	TotalAllowed int    `json:"total_allowed"`
	Timestamp    int64  `json:"timestamp"`
}

// StreamlineExpandResultDTO is the result of stream expansion.
type StreamlineExpandResultDTO struct {
	NewStreamIDs []int `json:"new_stream_ids"`
}

// StreamlineUpdateAllowanceRequest is the input for freeze/unfreeze.
type StreamlineUpdateAllowanceRequest struct {
	UserID       string `json:"user_id"`
	TotalAllowed int    `json:"total_allowed"`
	Timestamp    int64  `json:"timestamp"`
}

// StreamlineFreezeResultDTO is the result of updating stream allowance.
type StreamlineFreezeResultDTO struct {
	Frozen    []int `json:"frozen"`
	Unfrozen  []int `json:"unfrozen"`
	Created   []int `json:"created"`
	Destroyed []int `json:"destroyed"`
}

// StreamlineRemoveMemberResultDTO is the result of removing a member.
type StreamlineRemoveMemberResultDTO struct {
	RemovedFrom []int `json:"removed_from"`
}

// StreamlineMemberInfoDTO contains a member's positions across streams.
type StreamlineMemberInfoDTO struct {
	Streams []StreamPositionDTO `json:"streams"`
}

// StreamPositionDTO is a member's position in a single stream.
type StreamPositionDTO struct {
	StreamID int  `json:"stream_id"`
	Position int  `json:"position"`
	Frozen   bool `json:"frozen"`
}

// StreamSummaryDTO summarizes a single stream.
type StreamSummaryDTO struct {
	ID          int    `json:"id"`
	OwnerID     string `json:"owner_id"`
	MemberCount int    `json:"member_count"`
	Frozen      bool   `json:"frozen"`
	CreatedAt   int64  `json:"created_at"`
}

// CalculateStreamlineRequest is the input for streamline commission calculation.
// The plan and structure config come from the worker's loaded plan, not the
// request (HEU-583), so this mirrors CalculateUnilevelRequest exactly.
// Wire field "structure" matches the serde rename on handle_calculate_streamline's
// local Params struct (engine/network-engine-worker/src/handlers/streamline.rs).
type CalculateStreamlineRequest struct {
	StructureName string                            `json:"structure"`
	Snapshots     map[string]DistributorSnapshotDTO `json:"snapshots"`
	Volume        []VolumeSourceDTO                 `json:"volume"`
}

// SnapshotResultDTO is the wire format for a take_snapshot response.
// Contains the tree type and the serialized engine state.
type SnapshotResultDTO struct {
	TreeType string          `json:"tree_type"`
	Data     json.RawMessage `json:"data"`
}

// --- Rank evaluation wire types (HEU-443) ---

// EvaluateRanksRequest is the input for the evaluate_ranks op.
// Mirrors the Rust EvaluationInputs struct.
type EvaluateRanksRequest struct {
	Distributors  map[string]DistributorPrimitivesDTO `json:"distributors"`
	VolumeSources []VolumeSourceDTO                   `json:"volume_sources"`
	// HistoryWindow lists the prior period labels in order (e.g. ["2026-05", "2026-04"]).
	// Omitted when empty (omitempty) so callers without history data stay backward-compatible.
	HistoryWindow []string `json:"history_window,omitempty"`
	// History maps distributor UUID strings to per-period rank ordinals.
	// A nil *uint16 value marshals to JSON null, representing "Unranked" (evaluated, did not qualify).
	// A plain uint16 would collapse Unranked to 0, which is a valid ordinal — keep the pointer.
	History map[string]map[string]*uint16 `json:"history,omitempty"`
}

// DistributorPrimitivesDTO is the per-distributor input for rank evaluation.
// Mirrors the Rust DistributorPrimitives struct.
type DistributorPrimitivesDTO struct {
	PersonalVolume   float64  `json:"personal_volume"`
	RetailVolume     float64  `json:"retail_volume"`
	Status           string   `json:"status"`
	HasOrderInPeriod bool     `json:"has_order_in_period"`
	ActiveProducts   []string `json:"active_products"`
}

// EvaluatedRankDTO is one distributor's evaluated rank.
// Mirrors the Rust EvaluatedRank enum (kind-tagged).
//
// When Kind == "qualified", Rank and Ordinal are populated.
// When Kind == "unranked", Rank and Ordinal are zero values.
type EvaluatedRankDTO struct {
	Kind string `json:"kind"`
	Rank string `json:"rank,omitempty"`
	// Ordinal is uint16 with omitempty. This DTO is receive-only on the Go side
	// today; if a future caller marshals a Qualified rank with Ordinal == 0,
	// the resulting JSON will omit the ordinal field and the Rust deserializer
	// will reject the variant (Qualified requires both rank and ordinal).
	// If marshaling becomes load-bearing, change Ordinal to *uint16 so the
	// presence/absence is explicit rather than collapsed by zero value.
	Ordinal uint16 `json:"ordinal,omitempty"`
}

// EvaluationResultDTO is the full per-distributor rank map for a period.
// Mirrors the Rust EvaluationResult struct.
type EvaluationResultDTO struct {
	Ranks map[string]EvaluatedRankDTO `json:"ranks"`
}
