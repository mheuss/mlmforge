package networkengine

import "time"

// --- Tree types ---

// TreeNode is the lightweight node returned in list results.
// Kept lean — no volume data. Use QueryTree for enriched results.
type TreeNode struct {
	UserID       string
	ParentUserID string
	Position     int // Which slot under parent
	Depth        int // Distance from root
	EnrolledAt   time.Time
}

// TreePosition is full position metadata for a user on a specific structure.
// Generalized for all tree types. Branch counts and volumes are keyed
// by position index, not named legs.
//
// Fields populated by the Rust engine via StdioTransport:
//
//	UserID, ParentUserID, Position, Depth, ChildCount, BranchCounts
//
// Fields enriched by the Go application layer:
//
//	StructureID, StructureType, Width, BranchVolumes,
//	PersonalVolume, GroupVolume, OpenPositions
type TreePosition struct {
	StructureID    string
	StructureType  string // "unilevel", "binary", "matrix", "stairstep", "streamline"
	UserID         string
	ParentUserID   string
	Position       int
	Depth          int
	Width          int             // Max child positions (2 for binary, N for matrix, unbounded for unilevel)
	ChildCount     int             // Currently placed children
	BranchCounts   map[int]int     // Nodes per position. Binary: {0: 150, 1: 143}
	BranchVolumes  map[int]float64 // CV volume per position
	PersonalVolume float64         // User's own CV
	GroupVolume    float64         // Sum of all branch volumes + personal
	OpenPositions  []int           // Unfilled child positions. N/A for unilevel.
}

// TreeQuery defines a filtered or cross-structure query.
type TreeQuery struct {
	PrimaryStructureID string
	RootUserID         string
	MaxDepth           int
	Filters            []QueryFilter // Can reference other structures, rank, volume thresholds
}

// QueryFilter is a single condition in a tree query.
type QueryFilter struct {
	Field    string // "structure", "rank", "personal_volume", "group_volume", "depth"
	Operator string // "eq", "gt", "lt", "gte", "lte", "in", "descendant_of"
	Value    string // Interpreted based on field type
}

// QueryResult extends TreeNode with requested enrichment fields.
type QueryResult struct {
	TreeNode
	PersonalVolume float64 // Included when query requests volume
	GroupVolume    float64
	RankID         string // Included when query requests rank
}

// HoldingTankEntry represents a user awaiting manual placement.
type HoldingTankEntry struct {
	UserID             string
	StructureID        string
	StructureType      string
	SponsorUserID      string
	EnrolledAt         time.Time
	AvailablePositions []int // Valid placement options based on current tree state
}

// --- Rank types ---

// Rank represents a rank level in a ranking system.
type Rank struct {
	ID    string
	Name  string
	Level int // Numeric ordering
}

// RankGroup defines a ranking system. Scope determines whether it's
// global (evaluates across all structures) or structure-specific.
// EvaluationMode determines how qualification is measured over time.
// Defined in PlanConfiguration, consumed by RankProvider.
type RankGroup struct {
	ID             string
	Name           string
	Scope          string // "global" or "structure_specific"
	StructureID    string // If structure-specific
	EvaluationMode string // "current_period_only", "cumulative", "highest_ever"
}

// RankDefinition describes a rank level and its qualification criteria.
type RankDefinition struct {
	ID                 string
	Name               string
	Level              int
	RankGroupID        string
	QualificationRules []QualificationRule
}

// QualificationRule is a single criterion for rank qualification.
type QualificationRule struct {
	RuleName string
	RuleType string  // "personal_volume", "group_volume", "active_legs", "personally_enrolled", "leg_rank_minimum", etc.
	Required float64 // Threshold
}

// QualificationStatus provides full per-rule breakdown, not just a percentage.
type QualificationStatus struct {
	CurrentRank     Rank
	NextRank        Rank
	Rules           []RuleProgress
	OverallProgress float64 // Percentage toward next rank
}

// RuleProgress shows exactly where a user stands on a single qualification rule.
type RuleProgress struct {
	RuleName string
	RuleType string  // "personal_volume", "group_volume", "active_legs", "personally_enrolled", "leg_rank_minimum", etc.
	Required float64 // Threshold
	Current  float64 // Actual value
	Met      bool
}

// RankEvent records a rank achievement.
type RankEvent struct {
	RankID      string
	RankGroupID string
	AchievedAt  time.Time
	Period      string
}

// --- Volume types ---
//
// Monetary values use float64 throughout the system. IEEE 754 rounding errors
// are acceptable during intermediate calculations (tree walks, commission rates).
// Final rounding to currency precision happens at the payout stage (ADR-013).
// If sub-cent precision becomes a requirement, migrate to a decimal type at
// that boundary only.

// VolumeSource carries order details for volume recording.
type VolumeSource struct {
	UserID    string
	OrderID   string
	OrderType string // "signup", "store", "autoship"
	Items     []VolumeSourceItem
}

// VolumeSourceItem carries currency-neutral CV points pre-assigned
// by Commerce from the regional product catalog.
type VolumeSourceItem struct {
	ProductID string
	Quantity  int
	CVPoints  float64 // Currency-neutral commissionable volume
}

// VolumeAttribution shows where volume was placed after routing rules applied.
type VolumeAttribution struct {
	StructureID string
	ProductID   string
	CVPoints    float64
	Period      string
}

// --- Commission types ---

// Commission represents a single earned commission record.
type Commission struct {
	ID           string
	UserID       string
	Amount       float64 // In base currency
	BaseCurrency string
	Reason       string // "direct", "override", "pool", "bonus"
	SourceUserID string // Who generated this commission
	Period       string
	Status       string // "pending", "approved", "paid", "voided"
}

// CommissionSummary is a dashboard-level summary of commission data.
type CommissionSummary struct {
	TotalPending  float64
	TotalApproved float64
	TotalPaid     float64
	BaseCurrency  string
	ByReason      map[string]float64
}

// CommissionPage is a paginated result of commissions.
type CommissionPage struct {
	Commissions []Commission
	TotalCount  int
	Page        int
	PageSize    int
}

// PeriodSummary is a rolled-up view of an entire commission period.
type PeriodSummary struct {
	Period           string
	TotalCommissions float64
	BaseCurrency     string
	TotalByType      map[string]float64
	TotalByRankTier  map[string]float64
	PayeeCount       int
}

// PeriodStatus describes the lifecycle state of a commission period.
type PeriodStatus struct {
	Period           string
	State            string // "open", "calculating", "calculated", "approved", "paid"
	LastRunAt        time.Time
	LastProjectionAt time.Time
	CommissionTotal  float64
	BaseCurrency     string
	PayeeCount       int
}

// CommissionFilter supports filtering commission records.
type CommissionFilter struct {
	Period      string
	RankID      string  // Optional
	StructureID string  // Optional
	Status      string  // Optional
	MinAmount   float64 // Optional
	MaxAmount   float64 // Optional
	ReasonType  string  // Optional
	Page        int
	PageSize    int
}

// --- Placement types ---

// PlacementRequest carries enrollment-time placement data.
type PlacementRequest struct {
	UserID              string
	SponsorUserID       string
	SignupProductID     string
	PreferredPlacements map[string]int // Optional. Structure ID to preferred position.
}

// PlacementResult is the aggregate outcome of placement across all structures.
type PlacementResult struct {
	Placements []StructurePlacement
}

// StructurePlacement shows the outcome for a single structure during placement.
type StructurePlacement struct {
	StructureID         string
	Outcome             string // "placed", "held", "not_qualified", "already_placed"
	Position            TreePosition
	MissingRequirements []QualificationRequirement
}

// HoldingTankPlacement is the request to place a user from the holding tank.
type HoldingTankPlacement struct {
	UserID      string
	StructureID string
	Position    int
}

// PlacementQualification shows whether a user qualifies for placement in a structure.
type PlacementQualification struct {
	StructureID   string
	StructureName string
	Qualified     bool
	Requirements  []QualificationRequirement
}

// QualificationRequirement describes a single gate for structure placement.
type QualificationRequirement struct {
	Name     string
	Type     string // "product_purchase", "volume_threshold", "rank_minimum", etc.
	Required string // What's needed (product ID, volume amount, rank name)
	Current  string // Where the user stands
	Met      bool
}
