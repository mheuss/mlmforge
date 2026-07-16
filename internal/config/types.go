package config

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// CompensationPlan is the root configuration for a compensation plan.
// Fields match the YAML wire format (JSON Schema field names).
type CompensationPlan struct {
	Name                  string                `yaml:"name" json:"name"`
	Version               int                   `yaml:"version" json:"version"`
	Period                PeriodConfig          `yaml:"period" json:"period"`
	Volume                VolumeConfig          `yaml:"volume" json:"volume"`
	Ranks                 []RankDefinition      `yaml:"ranks" json:"ranks"`
	RankTracking          RankTrackingConfig    `yaml:"rank_tracking" json:"rank_tracking"`
	RankFeatures          RankFeaturesConfig    `yaml:"rank_features" json:"rank_features"`
	CommissionEligibility CommissionEligibility `yaml:"commission_eligibility" json:"commission_eligibility"`
	Structures            []StructureConfig     `yaml:"structures" json:"structures"`
	Bonuses               BonusConfig           `yaml:"bonuses" json:"bonuses"`
	Payout                PayoutConfig          `yaml:"payout" json:"payout"`
	Caps                  CapsConfig            `yaml:"caps" json:"caps"`
	Placement             PlacementConfig       `yaml:"placement" json:"placement"`
}

// --- Period ---

// PeriodConfig controls commission period timing and payout lag.
type PeriodConfig struct {
	Length        string  `yaml:"length" json:"length"`
	StartDate     *string `yaml:"start_date" json:"start_date"`
	PayoutLagDays int     `yaml:"payout_lag_days" json:"payout_lag_days"`
}

// --- Volume ---

// VolumeConfig controls volume calculation and currency settings.
type VolumeConfig struct {
	InhibitSignupVolume      bool    `yaml:"inhibit_signup_volume" json:"inhibit_signup_volume"`
	BaseCurrency             string  `yaml:"base_currency" json:"base_currency"`
	VolumeToDollarMultiplier float64 `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	DeductQualifyingVolume   bool    `yaml:"deduct_qualifying_volume" json:"deduct_qualifying_volume"`
}

// --- Ranks ---

// RankDefinition represents a single rank in the compensation plan hierarchy.
type RankDefinition struct {
	Name                string            `yaml:"name" json:"name"`
	Ordinal             int               `yaml:"ordinal" json:"ordinal"`
	Qualification       RankQualification `yaml:"qualification" json:"qualification"`
	QualifiedStructures []string          `yaml:"qualified_structures" json:"qualified_structures"`
	DemotionPolicy      DemotionPolicy    `yaml:"demotion_policy" json:"demotion_policy"`
}

// RankQualification holds the qualification requirements for achieving a rank.
type RankQualification struct {
	Structures       []StructureQualification `yaml:"structures" json:"structures"`
	RequiredProducts []string                 `yaml:"required_products" json:"required_products"`
	Window           *RankQualificationWindow `yaml:"window,omitempty" json:"window,omitempty"`
	Tenure           *TenureRequirement       `yaml:"tenure,omitempty" json:"tenure,omitempty"`
}

// RankQualificationWindow defines a windowed N-of-M gate: the distributor must
// qualify in at least QualifyingPeriods out of the most recent WindowPeriods
// before advancing to ThresholdRank. Mirrors the Rust RankQualificationWindow.
type RankQualificationWindow struct {
	ThresholdRank     string `yaml:"threshold_rank" json:"threshold_rank"`
	QualifyingPeriods uint8  `yaml:"qualifying_periods" json:"qualifying_periods"`
	WindowPeriods     uint8  `yaml:"window_periods" json:"window_periods"`
}

// TenureRequirement defines a strict consecutive-period gate: the distributor
// must have achieved ThresholdRank for Periods consecutive prior periods.
// Mirrors the Rust TenureRequirement.
type TenureRequirement struct {
	ThresholdRank string `yaml:"threshold_rank" json:"threshold_rank"`
	Periods       uint8  `yaml:"periods" json:"periods"`
}

// StructureQualification holds qualification requirements for a specific structure.
type StructureQualification struct {
	Structure            string                       `yaml:"structure" json:"structure"`
	PersonalVolume       float64                      `yaml:"personal_volume" json:"personal_volume"`
	GroupVolume          float64                      `yaml:"group_volume" json:"group_volume"`
	MaxGroupVolumePerLeg float64                      `yaml:"max_group_volume_per_leg" json:"max_group_volume_per_leg"`
	MinRetailVolume      float64                      `yaml:"min_retail_volume" json:"min_retail_volume"`
	DistributorCount     *DistributorCountRequirement `yaml:"distributor_count" json:"distributor_count"`
	LegQuality           []LegQualityRequirement      `yaml:"leg_quality,omitempty" json:"leg_quality,omitempty"`
}

// DistributorCountRequirement defines distributor count requirements per leg.
//
// Field widths mirror the Rust DistributorCountRequirement (rank.rs):
// Count and TotalCount are uint16 (Rust u16), SearchDepth is *uint8
// (Rust Option<u8>). Matching byte-widths catches an out-of-range value at
// the Go unmarshal layer instead of truncating at the Rust FFI boundary.
// See docs/development/config-types.md "Field types must match byte-widths".
type DistributorCountRequirement struct {
	Count             uint16  `yaml:"count" json:"count"`
	MinRank           string  `yaml:"min_rank" json:"min_rank"`
	SearchMode        string  `yaml:"search_mode" json:"search_mode"`
	SearchDepth       *uint8  `yaml:"search_depth" json:"search_depth"`
	TotalCount        uint16  `yaml:"total_count" json:"total_count"`
	MinLegGroupVolume float64 `yaml:"min_leg_group_volume" json:"min_leg_group_volume"`
}

// LegQualityRequirement defines a per-leg structural requirement: at least
// Count of the distributor's frontline legs must each contain a node matching
// Predicate. Mirrors the Rust LegQualityRequirement.
type LegQualityRequirement struct {
	Count     uint16       `yaml:"count" json:"count"`
	Predicate LegPredicate `yaml:"predicate" json:"predicate"`
}

// LegPredicate is the flat-struct Go mirror of the Rust internally-tagged
// LegPredicate enum. Type selects the variant ("contains_rank" or
// "contains_personal_volume"); only the matching variant's field is
// meaningful.
//
// No omitempty on MinRank / MinPersonalVolume: the non-selected field is
// emitted as a zero value, which Rust serde ignores when deserializing the
// internally-tagged enum. omitempty would wrongly drop a legitimate
// min_personal_volume of 0, and Rust has no serde(default) on that field.
type LegPredicate struct {
	Type              string  `yaml:"type" json:"type"`
	MinRank           string  `yaml:"min_rank" json:"min_rank"`
	MinPersonalVolume float64 `yaml:"min_personal_volume" json:"min_personal_volume"`
}

// DemotionPolicy handles the YAML union: either the string "promotion_only"
// or an object with a grace field. Custom UnmarshalYAML handles both.
// Custom MarshalJSON produces the correct Rust-side format.
type DemotionPolicy struct {
	StringValue string       // "promotion_only" when it's a plain string
	Grace       *GracePeriod // non-nil when it's the grace variant
}

// UnmarshalYAML implements custom unmarshalling for the demotion policy union type.
func (d *DemotionPolicy) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		// String value validation (allowed values: "promotion_only", etc.) is
		// delegated to the JSON Schema enum constraint in schema validation.
		d.StringValue = value.Value
		return nil
	}
	var obj struct {
		Grace GracePeriod `yaml:"grace"`
	}
	if err := value.Decode(&obj); err != nil {
		return err
	}
	d.Grace = &obj.Grace
	return nil
}

// MarshalJSON produces the Rust-compatible JSON format for the demotion policy
// union type. Outputs either a bare string ("promotion_only") or an object
// ({"grace": {"count": 2, "unit": "months"}}).
func (d DemotionPolicy) MarshalJSON() ([]byte, error) {
	if d.Grace != nil {
		return json.Marshal(map[string]any{"grace": d.Grace})
	}
	return json.Marshal(d.StringValue)
}

// GracePeriod defines a time window before a rank demotion takes effect.
type GracePeriod struct {
	Count int    `yaml:"count" json:"count"`
	Unit  string `yaml:"unit" json:"unit"`
}

// --- Rank tracking and features ---

// RankTrackingConfig controls rank history tracking.
type RankTrackingConfig struct {
	TrackAchievedRank bool `yaml:"track_achieved_rank" json:"track_achieved_rank"`
}

// RankFeaturesConfig holds feature flags for rank-related behavior.
type RankFeaturesConfig struct {
	ConstraintsEnabled bool `yaml:"constraints_enabled" json:"constraints_enabled"`
	OverridesEnabled   bool `yaml:"overrides_enabled" json:"overrides_enabled"`
}

// --- Commission eligibility ---

// CommissionEligibility defines rules for commission eligibility.
type CommissionEligibility struct {
	MinPersonalVolume    float64         `yaml:"min_personal_volume" json:"min_personal_volume"`
	RequireOrderInPeriod bool            `yaml:"require_order_in_period" json:"require_order_in_period"`
	EligibleStatuses     []string        `yaml:"eligible_statuses" json:"eligible_statuses"`
	ActiveLegTiers       []ActiveLegTier `yaml:"active_leg_tiers" json:"active_leg_tiers"`
}

// ActiveLegTier maps an active leg count to a commission depth.
type ActiveLegTier struct {
	MinActiveLegs      uint16 `yaml:"min_active_legs" json:"min_active_legs"`
	MaxCommissionDepth uint16 `yaml:"max_commission_depth" json:"max_commission_depth"`
}

// --- Commission interface ---

// Commission is a marker interface for typed commission configurations.
// It replaces the previous `any` type on StructureConfig.resolvedCommission,
// providing compile-time safety that only commission types are stored.
type Commission interface {
	isCommission()
}

// Marker method implementations for all commission types.
func (*UnilevelCommission) isCommission()   {}
func (*BinaryCommission) isCommission()     {}
func (*MatrixCommission) isCommission()     {}
func (*StairstepCommission) isCommission()  {}
func (*GenerationCommission) isCommission() {}
func (*StreamlineCommission) isCommission() {}
func (*BoardPlanCommission) isCommission()  {}

// --- Structures ---

// StructureConfig holds the flat YAML representation of a structure.
// The commission block varies by type and is deferred for two-pass parsing.
// CommissionRaw stores the untyped commission map from initial unmarshal.
// resolveCommissions() re-marshals and decodes it into the correct type.
type StructureConfig struct {
	Name          string                 `yaml:"name" json:"name"`
	Type          string                 `yaml:"type" json:"type"`
	CommissionRaw any                    `yaml:"commission" json:"-"`
	Structure     *MatrixStructureParams `yaml:"structure" json:"structure,omitempty"`
	Pruning       *PruningConfig         `yaml:"pruning" json:"pruning,omitempty"`
	// resolvedCommission holds the parsed commission config after type resolution.
	// Not exported. Set by resolveCommissions() during pipeline execution.
	resolvedCommission Commission
}

// Per-type commission configs. Populated by resolveCommissions() after
// initial unmarshal, based on the Type field.

// UnilevelCommission holds commission configuration for unilevel structures.
type UnilevelCommission struct {
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent" json:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	CommissionableDepth      uint8                         `yaml:"commissionable_depth" json:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table" json:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression" json:"compression"`
	PassUp                   *PassUpConfig                 `yaml:"pass_up" json:"pass_up"`
}

// BinaryCommission holds commission configuration for binary structures.
type BinaryCommission struct {
	VolumeToDollarMultiplier *float64         `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	Mode                     string           `yaml:"mode" json:"mode"`
	Pairing                  *PairingConfig   `yaml:"pairing" json:"pairing,omitempty"`
	CycleStep                *CycleStepConfig `yaml:"cycle_step" json:"cycle_step,omitempty"`
}

// MatrixCommission holds commission configuration for matrix structures.
type MatrixCommission struct {
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent" json:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	CommissionableDepth      uint8                         `yaml:"commissionable_depth" json:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table" json:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression" json:"compression"`
}

// StairstepCommission holds commission configuration for stairstep structures.
type StairstepCommission struct {
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent" json:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	CommissionableDepth      uint8                         `yaml:"commissionable_depth" json:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table" json:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression" json:"compression"`
	Breakaway                *BreakawayConfig              `yaml:"breakaway" json:"breakaway"`
}

// GenerationCommission holds commission configuration for generation structures.
type GenerationCommission struct {
	LevelCommissionsEnabled  bool                          `yaml:"level_commissions_enabled" json:"level_commissions_enabled"`
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent" json:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	CommissionableDepth      uint8                         `yaml:"commissionable_depth" json:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table" json:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression" json:"compression"`
	Generation               GenerationCommissionConfig    `yaml:"generation" json:"generation"`
}

// StreamlineCommission holds commission configuration for streamline structures.
type StreamlineCommission struct {
	VolumeToDollarMultiplier *float64                   `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	CommissionableDepth      uint8                      `yaml:"commissionable_depth" json:"commissionable_depth"`
	DynamicCompression       map[string]StreamlineLevel `yaml:"dynamic_compression" json:"dynamic_compression"`
	Streams                  *StreamConfig              `yaml:"streams" json:"streams"`
}

// BoardPlanCommission holds commission configuration for board plan structures.
// Board plans use cycling-based commissions instead of level-based rate tables.
// The board_cycling block defines the fixed payout and cycling behavior.
type BoardPlanCommission struct {
	BoardCycling BoardCyclingConfig `yaml:"board_cycling" json:"board_cycling"`
}

// --- Shared commission sub-types ---

// CompressionConfig controls skipping inactive distributors in commission calculations.
type CompressionConfig struct {
	Enabled       bool    `yaml:"enabled" json:"enabled"`
	Mode          string  `yaml:"mode" json:"mode"`
	RankThreshold *string `yaml:"rank_threshold" json:"rank_threshold"`
}

// Multi-position cap mode constants for PairingConfig.
const (
	MultiPositionCapModePerPosition = "per_position"
	MultiPositionCapModeAggregate   = "aggregate"
)

// PairingConfig holds binary pairing commission configuration.
type PairingConfig struct {
	Percent           float64  `yaml:"percent" json:"percent"`
	Calculation       string   `yaml:"calculation" json:"calculation"`
	CapPerPeriod      *float64 `yaml:"cap_per_period" json:"cap_per_period"`
	VolumeAfterPayout string   `yaml:"volume_after_payout" json:"volume_after_payout"`
	CarryForwardCap   *float64 `yaml:"carry_forward_cap" json:"carry_forward_cap"`
	// Empty string means the Rust engine's serde default ("per_position") applies.
	// Go callers should use GetMultiPositionCapMode() for a consistent default.
	MultiPositionCapMode string `yaml:"multi_position_cap_mode,omitempty" json:"multi_position_cap_mode,omitempty"`
}

// GetMultiPositionCapMode returns the cap mode, defaulting to PerPosition if empty.
func (p *PairingConfig) GetMultiPositionCapMode() string {
	if p.MultiPositionCapMode == "" {
		return MultiPositionCapModePerPosition
	}
	return p.MultiPositionCapMode
}

// CycleStepConfig holds binary cycle/step commission configuration.
type CycleStepConfig struct {
	Steps                []CycleStep `yaml:"steps" json:"steps"`
	VolumeAfterCycle     string      `yaml:"volume_after_cycle" json:"volume_after_cycle"`
	CapPerPeriod         *float64    `yaml:"cap_per_period" json:"cap_per_period"`
	CarryForwardCap      *float64    `yaml:"carry_forward_cap" json:"carry_forward_cap"`
	MultiPositionCapMode string      `yaml:"multi_position_cap_mode,omitempty" json:"multi_position_cap_mode,omitempty"`
}

// CycleStep represents a single step in a binary cycle/step commission.
type CycleStep struct {
	Threshold float64 `yaml:"threshold" json:"threshold"`
	Amount    float64 `yaml:"amount" json:"amount"`
}

// BreakawayConfig holds stairstep breakaway configuration. The override
// strategy nests under Overrides so single_walk and multi_tier share the
// same wire shape: a discriminated object with a `type` tag.
type BreakawayConfig struct {
	ThresholdRank                string           `yaml:"threshold_rank" json:"threshold_rank"`
	GroupVolumeExcludesBreakaway bool             `yaml:"group_volume_excludes_breakaway" json:"group_volume_excludes_breakaway"`
	Overrides                    OverrideStrategy `yaml:"overrides" json:"overrides"`
}

// Override strategy wire-string values. These mirror the Rust enum variants
// in OverrideStrategy and must match the JSON schema enum. The const values
// are pinned to literals by TestOverrideStrategyConstValues so a typo here
// fails fast.
const (
	overrideStrategySingleWalk = "single_walk"
	overrideStrategyMultiTier  = "multi_tier"
)

// OverrideStrategy is the flat Go mirror of the Rust OverrideStrategy enum
// (internally tagged on `type`). Type selects the variant. Non-selected
// variant fields marshal as zero values; the Rust internally-tagged enum
// reads `type` first and ignores the other variant's fields. See
// docs/development/config-types.md for the mirror pattern.
//
// No omitempty on the variant fields. A zero value must round-trip so the
// Rust side sees a consistent shape on every payload. See
// TestBreakawayConfig_SingleWalk_MarshalsGoEmittedWireShape and
// TestBreakawayConfig_MultiTier_MarshalsGoEmittedWireShape for the
// wire-shape regression pin.
type OverrideStrategy struct {
	// Type selects the variant. Valid values: "single_walk", "multi_tier"
	// (see overrideStrategySingleWalk and overrideStrategyMultiTier above).
	Type string `yaml:"type" json:"type"`

	// single_walk fields
	OverrideCalculation string                     `yaml:"override_calculation" json:"override_calculation"`
	Differential        *DifferentialConfig        `yaml:"differential" json:"differential"`
	FixedOverride       *FixedOverrideConfig       `yaml:"fixed_override" json:"fixed_override"`
	Generation          *BreakawayGenerationConfig `yaml:"generation" json:"generation"`

	// Tiers: nil marshals as JSON null. Do not default to an empty slice.
	// That changes the wire shape Rust expects.
	Tiers []BreakawayTier `yaml:"tiers" json:"tiers"`
}

// BreakawayTier is one tier of a multi-tier breakaway override ladder.
// MinSplitOutGroups uses uint8 to mirror the Rust u8 exactly, so negative
// or out-of-range values fail at Go unmarshal time instead of silently
// truncating downstream.
type BreakawayTier struct {
	MinSplitOutGroups uint8   `yaml:"min_split_out_groups" json:"min_split_out_groups"`
	Rate              float64 `yaml:"rate" json:"rate"`
}

// DifferentialConfig holds differential override commission configuration.
type DifferentialConfig struct {
	RankRates   map[string]float64 `yaml:"rank_rates" json:"rank_rates"`
	MinOverride float64            `yaml:"min_override" json:"min_override"`
}

// FixedOverrideConfig holds fixed override commission configuration.
// Each rank has a flat override percentage applied to breakaway group volume.
type FixedOverrideConfig struct {
	RankRates map[string]float64 `yaml:"rank_rates" json:"rank_rates"`
}

// BreakawayGenerationConfig holds generation override configuration for breakaway groups.
//
// MaxGenerations uses uint8 to mirror the Rust u8 field exactly. This makes
// negative or out-of-range values fail at Go unmarshal time instead of silently
// truncating or erroring downstream when the config round-trips to the engine.
type BreakawayGenerationConfig struct {
	MaxGenerations  uint8              `yaml:"max_generations" json:"max_generations"`
	GenerationRates map[string]float64 `yaml:"generation_rates" json:"generation_rates"`
	BoundaryRank    string             `yaml:"boundary_rank" json:"boundary_rank"`
}

// GenerationCommissionConfig holds configuration for generation-based commissions.
//
// MaxGenerations and MaxGenerationsPerRank both use uint8 to mirror the Rust
// u8 fields exactly. This makes negative or out-of-range values fail at Go
// unmarshal time instead of silently truncating or erroring downstream when
// the config round-trips to the engine. The omitempty tag on the per-rank map
// matches the Rust serde(default) behavior so an empty map round-trips cleanly.
type GenerationCommissionConfig struct {
	MaxGenerations                uint8              `yaml:"max_generations" json:"max_generations"`
	MaxGenerationsPerRank         map[string]uint8   `yaml:"max_generations_per_rank,omitempty" json:"max_generations_per_rank,omitempty"`
	GenerationRates               map[string]float64 `yaml:"generation_rates" json:"generation_rates"`
	BoundaryMode                  string             `yaml:"boundary_mode" json:"boundary_mode"`
	BoundaryRank                  string             `yaml:"boundary_rank" json:"boundary_rank"`
	EmptyGenerationConsumesNumber bool               `yaml:"empty_generation_consumes_number" json:"empty_generation_consumes_number"`
	VolumeToDollarMultiplier      *float64           `yaml:"volume_to_dollar_multiplier" json:"volume_to_dollar_multiplier"`
	IneligibleCreatesBoundary     *bool              `yaml:"ineligible_creates_boundary" json:"ineligible_creates_boundary,omitempty"`
}

// StreamlineLevel holds commission configuration for a single streamline level.
type StreamlineLevel struct {
	MinRank string  `yaml:"min_rank" json:"min_rank"`
	Percent float64 `yaml:"percent" json:"percent"`
}

// StreamConfig holds multi-stream configuration for streamline plans.
type StreamConfig struct {
	AdditionalPerRank   map[string]int `yaml:"additional_per_rank" json:"additional_per_rank"`
	AssignmentMode      string         `yaml:"assignment_mode" json:"assignment_mode"`
	PerEnrollmentChoice bool           `yaml:"per_enrollment_choice" json:"per_enrollment_choice"`
	FreezeOnDemotion    bool           `yaml:"freeze_on_demotion" json:"freeze_on_demotion"`
}

// --- Matrix ---

// MatrixStructureParams defines matrix tree shape parameters.
type MatrixStructureParams struct {
	Width              uint8  `yaml:"width" json:"width"`
	Height             uint8  `yaml:"height" json:"height"`
	SpilloverDirection string `yaml:"spillover_direction" json:"spillover_direction"`
}

// PruningConfig controls handling of inactive nodes in a matrix.
type PruningConfig struct {
	Mode string `yaml:"mode" json:"mode"`
}

// --- Bonuses ---

// BonusConfig holds all bonus program configurations.
type BonusConfig struct {
	Matching              *MatchingBonusConfig              `yaml:"matching" json:"matching"`
	Sponsor               *SponsorBonusConfig               `yaml:"sponsor" json:"sponsor"`
	FastStart             *FastStartBonusConfig             `yaml:"fast_start" json:"fast_start"`
	RankAdvancement       *RankAdvancementBonusConfig       `yaml:"rank_advancement" json:"rank_advancement"`
	LeadershipDevelopment *LeadershipDevelopmentBonusConfig `yaml:"leadership_development" json:"leadership_development"`
	Infinity              *InfinityBonusConfig              `yaml:"infinity" json:"infinity"`
	Lifestyle             *LifestyleBonusConfig             `yaml:"lifestyle" json:"lifestyle"`
	Pool                  []PoolBonusConfig                 `yaml:"pool" json:"pool"`
	MatrixCompletion      *MatrixCompletionBonusConfig      `yaml:"matrix_completion" json:"matrix_completion"`
	Position              *PositionBonusConfig              `yaml:"position" json:"position"`
	BoardCycling          *BoardCyclingConfig               `yaml:"board_cycling" json:"board_cycling"`
}

// MatchingBonusConfig pays a percentage of downline commissions.
type MatchingBonusConfig struct {
	Depth                  uint8              `yaml:"depth" json:"depth"`
	Rates                  map[string]float64 `yaml:"rates" json:"rates"`
	MatchedCommissionTypes []string           `yaml:"matched_commission_types" json:"matched_commission_types"`
}

// SponsorBonusConfig pays a bonus when a new distributor enrolls.
type SponsorBonusConfig struct {
	Amount             float64  `yaml:"amount" json:"amount"`
	AmountType         string   `yaml:"amount_type" json:"amount_type"`
	QualifyingProducts []string `yaml:"qualifying_products" json:"qualifying_products"`
}

// FastStartBonusConfig provides enhanced commissions during the early enrollment window.
type FastStartBonusConfig struct {
	WindowDays uint16                        `yaml:"window_days" json:"window_days"`
	RateTable  map[string]map[string]float64 `yaml:"rate_table" json:"rate_table"`
}

// RankAdvancementBonusConfig pays a one-time bonus for reaching a new rank.
type RankAdvancementBonusConfig struct {
	Amounts     map[string]float64 `yaml:"amounts" json:"amounts"`
	PayOnceOnly bool               `yaml:"pay_once_only" json:"pay_once_only"`
}

// LeadershipDevelopmentBonusConfig pays override commissions on downline leaders.
type LeadershipDevelopmentBonusConfig struct {
	Depth        uint8              `yaml:"depth" json:"depth"`
	Rates        map[string]float64 `yaml:"rates" json:"rates"`
	RankSkipMode string             `yaml:"rank_skip_mode" json:"rank_skip_mode"`
}

// InfinityBonusConfig pays commissions to unlimited depth until blocked.
type InfinityBonusConfig struct {
	BlockerMode     string             `yaml:"blocker_mode" json:"blocker_mode"`
	FlatRate        *float64           `yaml:"flat_rate" json:"flat_rate"`
	DecreasingRates map[string]float64 `yaml:"decreasing_rates" json:"decreasing_rates"`
	RateMode        string             `yaml:"rate_mode" json:"rate_mode"`
}

// LifestyleBonusConfig pays a recurring bonus for maintaining qualifying rank.
type LifestyleBonusConfig struct {
	Tiers []LifestyleTier `yaml:"tiers" json:"tiers"`
}

// LifestyleTier represents a single tier in the lifestyle bonus program.
type LifestyleTier struct {
	MinRank      string  `yaml:"min_rank" json:"min_rank"`
	Amount       float64 `yaml:"amount" json:"amount"`
	GracePeriods uint8   `yaml:"grace_periods" json:"grace_periods"`
}

// PoolBonusConfig defines a revenue pool shared among qualifying distributors.
type PoolBonusConfig struct {
	Name                     string            `yaml:"name" json:"name"`
	SourcePercent            float64           `yaml:"source_percent" json:"source_percent"`
	Qualification            PoolQualification `yaml:"qualification" json:"qualification"`
	Shares                   PoolShares        `yaml:"shares" json:"shares"`
	RequireAdminConfirmation bool              `yaml:"require_admin_confirmation" json:"require_admin_confirmation"`
}

// PoolQualification defines qualification rules for pool bonus participation.
type PoolQualification struct {
	Mode     string                 `yaml:"mode" json:"mode"`
	MinRank  *string                `yaml:"min_rank" json:"min_rank"`
	Velocity *VelocityQualification `yaml:"velocity" json:"velocity"`
}

// VelocityQualification defines volume velocity requirements for pool bonuses.
type VelocityQualification struct {
	VolumeTarget  float64 `yaml:"volume_target" json:"volume_target"`
	Timeframe     string  `yaml:"timeframe" json:"timeframe"`
	TimeframeDays *int    `yaml:"timeframe_days" json:"timeframe_days"`
}

// PoolShares defines how pool bonus payouts are divided among qualifiers.
type PoolShares struct {
	Mode          string   `yaml:"mode" json:"mode"`
	EqualShareCap *float64 `yaml:"equal_share_cap" json:"equal_share_cap"`
}

// MatrixCompletionBonusConfig pays a bonus for completing matrix levels.
type MatrixCompletionBonusConfig struct {
	PerLevel   map[string]float64 `yaml:"per_level" json:"per_level"`
	FullMatrix float64            `yaml:"full_matrix" json:"full_matrix"`
}

// PositionBonusConfig pays a bonus based on tree position.
type PositionBonusConfig struct {
	Amount        float64 `yaml:"amount" json:"amount"`
	AmountType    string  `yaml:"amount_type" json:"amount_type"`
	SponsoredOnly bool    `yaml:"sponsored_only" json:"sponsored_only"`
}

// BoardCyclingConfig controls cycling behavior for board plan structures.
type BoardCyclingConfig struct {
	CycleCommission       float64 `yaml:"cycle_commission" json:"cycle_commission"`
	ReEntryEnabled        bool    `yaml:"re_entry_enabled" json:"re_entry_enabled"`
	ReEntryPosition       string  `yaml:"re_entry_position" json:"re_entry_position"`
	MaxCyclesPerPeriod    int     `yaml:"max_cycles_per_period" json:"max_cycles_per_period"`
	MaxCascadeDepth       int     `yaml:"max_cascade_depth" json:"max_cascade_depth,omitempty"`
	StallThresholdPeriods int     `yaml:"stall_threshold_periods" json:"stall_threshold_periods"`
	InactiveCompression   bool    `yaml:"inactive_compression" json:"inactive_compression"`
}

// PassUpConfig defines pass-up bonus behavior where initial sales go to the upline.
type PassUpConfig struct {
	Count               int  `yaml:"count" json:"count"`
	IncludesCommissions bool `yaml:"includes_commissions" json:"includes_commissions"`
}

// --- Payout ---

// PayoutConfig controls payout processing and payment methods.
type PayoutConfig struct {
	BaseCurrency        string         `yaml:"base_currency" json:"base_currency"`
	MinimumAmount       float64        `yaml:"minimum_amount" json:"minimum_amount"`
	SplitPayoutsEnabled bool           `yaml:"split_payouts_enabled" json:"split_payouts_enabled"`
	Methods             []PayoutMethod `yaml:"methods" json:"methods"`
}

// PayoutMethod represents a payment method available for commission payouts.
type PayoutMethod struct {
	Type string  `yaml:"type" json:"type"`
	Fee  float64 `yaml:"fee" json:"fee"`
}

// --- Caps ---

// CapsConfig controls commission caps and clawback behavior.
type CapsConfig struct {
	PerDistributorPerPeriod *float64 `yaml:"per_distributor_per_period" json:"per_distributor_per_period"`
	CompanyPayoutCapPercent float64  `yaml:"company_payout_cap_percent" json:"company_payout_cap_percent"`
	CapEnforcement          string   `yaml:"cap_enforcement" json:"cap_enforcement"`
	ClawbackOnRefund        bool     `yaml:"clawback_on_refund" json:"clawback_on_refund"`
}

// --- Placement ---

// PlacementConfig defines placement rules for new distributors.
// The YAML format uses donated_placement_enabled + donated_placement_restriction.
// Translation to Rust format collapses these into a single donated_placement field.
type PlacementConfig struct {
	DonatedPlacementEnabled     bool                   `yaml:"donated_placement_enabled" json:"donated_placement_enabled"`
	DonatedPlacementRestriction *string                `yaml:"donated_placement_restriction" json:"donated_placement_restriction,omitempty"`
	HoldingTank                 *HoldingTankConfig     `yaml:"holding_tank" json:"holding_tank"`
	Binary                      *BinaryPlacementConfig `yaml:"binary" json:"binary,omitempty"`
	Matrix                      *MatrixPlacementConfig `yaml:"matrix" json:"matrix,omitempty"`
}

// HoldingTankConfig defines holding tank behavior for deferred placement.
type HoldingTankConfig struct {
	Enabled              bool     `yaml:"enabled" json:"enabled"`
	ExpirationDays       int      `yaml:"expiration_days" json:"expiration_days"`
	AllowSponsorChange   bool     `yaml:"allow_sponsor_change" json:"allow_sponsor_change"`
	ApplicableStructures []string `yaml:"applicable_structures" json:"applicable_structures"`
}

// BinaryPlacementConfig defines binary-specific placement rules.
type BinaryPlacementConfig struct {
	DefaultPlacement  string `yaml:"default_placement" json:"default_placement"`
	PerUserPreference bool   `yaml:"per_user_preference" json:"per_user_preference"`
	SpilloverEnabled  bool   `yaml:"spillover_enabled" json:"spillover_enabled"`
}

// MatrixPlacementConfig defines matrix-specific placement settings.
type MatrixPlacementConfig struct {
	SpilloverDirection string `yaml:"spillover_direction" json:"spillover_direction"`
}

// resolveCommissions does the second-pass unmarshal of structure commission
// blocks. After the initial YAML unmarshal, CommissionRaw holds the untyped
// map. This function re-marshals it to YAML bytes and decodes into the
// correct typed struct based on the structure's Type field.
func resolveCommissions(plan *CompensationPlan) error {
	for i := range plan.Structures {
		s := &plan.Structures[i]
		if s.CommissionRaw == nil {
			return fmt.Errorf("structure %q has no commission block", s.Name)
		}
		rawBytes, err := yaml.Marshal(s.CommissionRaw)
		if err != nil {
			return fmt.Errorf("structure %q commission marshal: %w", s.Name, err)
		}
		switch s.Type {
		case "unilevel":
			var c UnilevelCommission
			err = yaml.Unmarshal(rawBytes, &c)
			s.resolvedCommission = &c
		case "binary":
			var c BinaryCommission
			err = yaml.Unmarshal(rawBytes, &c)
			s.resolvedCommission = &c
		case "matrix":
			var c MatrixCommission
			err = yaml.Unmarshal(rawBytes, &c)
			s.resolvedCommission = &c
		case "stairstep":
			var c StairstepCommission
			err = yaml.Unmarshal(rawBytes, &c)
			s.resolvedCommission = &c
		case "generation":
			var c GenerationCommission
			err = yaml.Unmarshal(rawBytes, &c)
			s.resolvedCommission = &c
		case "streamline":
			var c StreamlineCommission
			err = yaml.Unmarshal(rawBytes, &c)
			s.resolvedCommission = &c
		case "board_plan":
			var c BoardPlanCommission
			err = yaml.Unmarshal(rawBytes, &c)
			s.resolvedCommission = &c
		default:
			err = fmt.Errorf("unknown structure type: %s", s.Type)
		}
		if err != nil {
			return fmt.Errorf("structure %q commission: %w", s.Name, err)
		}
	}
	return nil
}
