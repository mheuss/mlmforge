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

// BinaryCommissionEarningDTO is the wire format for a single binary pairing earning.
// Matches the Rust BinaryCommissionEarning struct.
type BinaryCommissionEarningDTO struct {
	EarnerID      string  `json:"earner_id"`
	PositionID    string  `json:"position_id"`
	LeftVolume    float64 `json:"left_volume"`
	RightVolume   float64 `json:"right_volume"`
	MatchedVolume float64 `json:"matched_volume"`
	Ratio         float64 `json:"ratio"`
	Percent       float64 `json:"percent"`
	DollarAmount  float64 `json:"dollar_amount"`
	Capped        bool    `json:"capped"`
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
	BoardID          string   `json:"board_id"`
	CycledMember     string   `json:"cycled_member"`
	EarnedCommission bool     `json:"earned_commission"`
	NewBoards        []string `json:"new_boards"`
	ReEntryBoard     *string  `json:"re_entry_board"`
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
// The handler is stateless: it takes cycle events, prior counts, and config directly.
type CalculateBoardCommissionsRequest struct {
	CycleEvents       []CycleEventDTO `json:"cycle_events"`
	PeriodCycleCounts map[string]int  `json:"period_cycle_counts"`
	Config            json.RawMessage `json:"config"`
}

// SnapshotResultDTO is the wire format for a take_snapshot response.
// Contains the tree type and the serialized engine state.
type SnapshotResultDTO struct {
	TreeType string          `json:"tree_type"`
	Data     json.RawMessage `json:"data"`
}
