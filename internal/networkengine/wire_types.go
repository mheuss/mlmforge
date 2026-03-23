// Package networkengine wire_types.go defines DTOs that mirror the Rust engine's NDJSON wire format.
package networkengine

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
