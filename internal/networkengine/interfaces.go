package networkengine

import "context"

// TreeNavigator provides read-only tree traversal, cross-structure queries,
// and holding tank access. All tree types (binary, matrix, unilevel,
// stairstep, streamline) are accessed through the same interface.
// "Position" is zero-indexed and tree-type-specific: binary 0=left/1=right,
// matrix 0..width-1, unilevel by child index.
type TreeNavigator interface {
	// --- Basic traversal ---

	// GetParent returns the parent node. This is the generalized sponsor lookup.
	GetParent(ctx context.Context, structureID string, userID string) (TreeNode, error)

	// GetChildren returns direct children of a node.
	GetChildren(ctx context.Context, structureID string, userID string) ([]TreeNode, error)

	// GetUpline walks upward N levels (0 = to root).
	GetUpline(ctx context.Context, structureID string, userID string, depth int) ([]TreeNode, error)

	// GetDownline walks downward N levels.
	GetDownline(ctx context.Context, structureID string, userID string, depth int) ([]TreeNode, error)

	// GetPosition returns full position metadata for a user on a structure.
	GetPosition(ctx context.Context, structureID string, userID string) (TreePosition, error)

	// --- Branch operations (generalized for all positional trees) ---

	// GetBranch returns the subtree under a specific child position.
	GetBranch(ctx context.Context, structureID string, userID string, position int) ([]TreeNode, error)

	// CountDownline returns total descendant count without returning nodes.
	CountDownline(ctx context.Context, structureID string, userID string, depth int) (int, error)

	// CountBranch returns descendant count under a specific position.
	// Critical for binary balancing and matrix fill metrics.
	CountBranch(ctx context.Context, structureID string, userID string, position int) (int, error)

	// --- Cross-structure and filtered queries ---

	// GetStructuresForUser returns all structures a user is placed on.
	GetStructuresForUser(ctx context.Context, userID string) ([]TreePosition, error)

	// IsDescendantOf checks if userID is anywhere in ancestorUserID's downline.
	// Quick boolean — the engine can short-circuit without building the full path.
	IsDescendantOf(ctx context.Context, structureID string, userID string, ancestorUserID string) (bool, error)

	// QueryTree executes filtered and cross-structure queries in the Rust engine.
	// Supports conditions like "binary descendants who are also unilevel
	// descendants of the same user." Returns enriched results with volume/rank
	// data when requested.
	QueryTree(ctx context.Context, query TreeQuery) ([]QueryResult, error)

	// --- Holding tank ---

	// GetHoldingTank returns users awaiting manual placement on a structure,
	// scoped to a sponsor. Applies to positional trees (binary, matrix).
	GetHoldingTank(ctx context.Context, structureID string, sponsorUserID string) ([]HoldingTankEntry, error)

	// GetPendingPlacements returns all pending placements across all structures
	// for a sponsor. The back office dashboard view.
	GetPendingPlacements(ctx context.Context, sponsorUserID string) ([]HoldingTankEntry, error)
}

// RankProvider exposes rank data and qualification progress.
// All methods are scoped by rank group — organizations can have multiple
// ranking systems (global ranks, per-structure ranks, different evaluation
// modes). Rank group definitions live in PlanConfiguration.
type RankProvider interface {
	// GetCurrentRank returns a user's current rank in a specific rank group.
	GetCurrentRank(ctx context.Context, userID string, rankGroupID string) (Rank, error)

	// GetRankHistory returns all rank achievements for a user in a rank group.
	GetRankHistory(ctx context.Context, userID string, rankGroupID string) ([]RankEvent, error)

	// GetQualificationProgress returns per-rule breakdown of progress
	// toward the next rank. Shows "you need 500 PV, you have 350" level detail.
	GetQualificationProgress(ctx context.Context, userID string, rankGroupID string) (QualificationStatus, error)

	// ListRankDefinitions returns all rank levels in a rank group
	// with their qualification criteria.
	ListRankDefinitions(ctx context.Context, rankGroupID string) ([]RankDefinition, error)
}

// VolumeRecorder is the command interface for recording volume from orders.
// Commerce passes order details with pre-assigned CV points. The Network
// Engine applies comp plan routing rules (product class to structure mappings)
// to determine which structures receive volume. Commerce owns pricing and
// CV assignment. Network Engine owns the routing rules.
type VolumeRecorder interface {
	// RecordVolume attributes order volume to tree positions based on
	// comp plan routing rules. Returns where volume was placed.
	RecordVolume(ctx context.Context, source VolumeSource) ([]VolumeAttribution, error)
}

// CommissionResult provides read-only access to commission data.
// Includes finalized results from commission runs and on-demand projections.
// All monetary amounts are in the company's base currency. Financial
// handles conversion to payout currency at disbursement.
type CommissionResult interface {
	// --- Individual / back office ---

	// GetForUser returns all commission records for a user in a period.
	GetForUser(ctx context.Context, userID string, period string) ([]Commission, error)

	// GetSummary returns a dashboard summary for a user in a period.
	GetSummary(ctx context.Context, userID string, period string) (CommissionSummary, error)

	// GetProjection returns an on-demand mid-period estimate for a user.
	GetProjection(ctx context.Context, userID string, period string) (CommissionSummary, error)

	// --- Bulk / admin ---

	// ListCommissions returns paginated, filterable commission records.
	// For admin tables and cross-cutting reports.
	ListCommissions(ctx context.Context, filter CommissionFilter) (CommissionPage, error)

	// GetPeriodSummary returns pre-computed rolled-up totals for a period.
	GetPeriodSummary(ctx context.Context, period string) (PeriodSummary, error)

	// GetPeriodProjection returns an aggregate on-demand estimate for budgeting.
	GetPeriodProjection(ctx context.Context, period string) (PeriodSummary, error)

	// --- Lifecycle ---

	// GetPeriodStatus returns the current state of a commission period.
	GetPeriodStatus(ctx context.Context, period string) (PeriodStatus, error)
}

// CommissionAdmin is the command interface for commission run lifecycle.
// Separated from CommissionResult to maintain read/write split.
type CommissionAdmin interface {
	// RunCommissions executes commission calculation for a period.
	RunCommissions(ctx context.Context, period string) error

	// ApproveCommissions marks a period's commissions as approved for payout.
	ApproveCommissions(ctx context.Context, period string) error

	// RequestProjection triggers an interim projection snapshot.
	RequestProjection(ctx context.Context, period string) error
}

// StructurePlacer handles placement and qualification for tree structures.
// Evaluates per-structure qualification requirements and routes to holding
// tank when manual placement is needed. Supports both enrollment-time
// placement and post-enrollment qualification (e.g., user upgrades product).
type StructurePlacer interface {
	// PlaceUser evaluates all structures during enrollment. Per structure:
	// auto-places if qualified + auto-placeable, routes to holding tank if
	// qualified + position choice needed, skips if not qualified.
	PlaceUser(ctx context.Context, req PlacementRequest) (PlacementResult, error)

	// PlaceFromHoldingTank places a held user into a specific position.
	// Called by sponsor or admin from the back office.
	PlaceFromHoldingTank(ctx context.Context, req HoldingTankPlacement) (TreePosition, error)

	// EvaluateQualification checks what structures a user currently qualifies for.
	// For upgrade flows, admin tools, periodic re-evaluation.
	EvaluateQualification(ctx context.Context, userID string) ([]StructureQualification, error)

	// QualifyAndPlace handles post-enrollment qualification.
	// User bought Gold Package — check stairstep requirements and place/hold if qualified.
	QualifyAndPlace(ctx context.Context, userID string, structureID string) (PlacementResult, error)
}

// PlanConfiguration provides read-only access to compensation plan
// configuration. The single place to ask "what does this plan look like?"
// Covers structures, signup products, volume routing, placement
// requirements, and rank groups.
type PlanConfiguration interface {
	GetSignupProduct(ctx context.Context, productID string) (SignupProduct, error)
	ListSignupProducts(ctx context.Context) ([]SignupProduct, error)
	GetStructureConfig(ctx context.Context, structureID string) (StructureConfig, error)
	ListStructures(ctx context.Context) ([]StructureConfig, error)

	// GetVolumeRoutingRules returns product class to structure mappings.
	GetVolumeRoutingRules(ctx context.Context) ([]VolumeRoutingRule, error)

	// GetPlacementRequirements returns qualification gates for a structure.
	GetPlacementRequirements(ctx context.Context, structureID string) ([]QualificationRequirement, error)

	// ListRankGroups returns all ranking systems in the comp plan.
	ListRankGroups(ctx context.Context) ([]RankGroup, error)
}
