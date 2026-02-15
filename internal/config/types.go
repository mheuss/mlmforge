package config

import "gopkg.in/yaml.v3"

// CompensationPlan is the root configuration for a compensation plan.
// Fields match the YAML wire format (JSON Schema field names).
type CompensationPlan struct {
	Name                  string                `yaml:"name"`
	Version               int                   `yaml:"version"`
	Period                PeriodConfig          `yaml:"period"`
	Volume                VolumeConfig          `yaml:"volume"`
	Ranks                 []RankDefinition      `yaml:"ranks"`
	RankTracking          RankTrackingConfig    `yaml:"rank_tracking"`
	RankFeatures          RankFeaturesConfig    `yaml:"rank_features"`
	CommissionEligibility CommissionEligibility `yaml:"commission_eligibility"`
	Structures            []StructureConfig     `yaml:"structures"`
	Bonuses               BonusConfig           `yaml:"bonuses"`
	Payout                PayoutConfig          `yaml:"payout"`
	Caps                  CapsConfig            `yaml:"caps"`
	Placement             PlacementConfig       `yaml:"placement"`
}

// --- Period ---

// PeriodConfig controls commission period timing and payout lag.
type PeriodConfig struct {
	Length        string  `yaml:"length"`
	StartDate     *string `yaml:"start_date"`
	PayoutLagDays int     `yaml:"payout_lag_days"`
}

// --- Volume ---

// VolumeConfig controls volume calculation and currency settings.
type VolumeConfig struct {
	InhibitSignupVolume      bool    `yaml:"inhibit_signup_volume"`
	BaseCurrency             string  `yaml:"base_currency"`
	VolumeToDollarMultiplier float64 `yaml:"volume_to_dollar_multiplier"`
	DeductQualifyingVolume   bool    `yaml:"deduct_qualifying_volume"`
}

// --- Ranks ---

// RankDefinition represents a single rank in the compensation plan hierarchy.
type RankDefinition struct {
	Name                string            `yaml:"name"`
	Ordinal             int               `yaml:"ordinal"`
	Qualification       RankQualification `yaml:"qualification"`
	QualifiedStructures []string          `yaml:"qualified_structures"`
	DemotionPolicy      DemotionPolicy    `yaml:"demotion_policy"`
}

// RankQualification holds the qualification requirements for achieving a rank.
type RankQualification struct {
	Structures       []StructureQualification `yaml:"structures"`
	RequiredProducts []string                 `yaml:"required_products"`
}

// StructureQualification holds qualification requirements for a specific structure.
type StructureQualification struct {
	Structure            string                       `yaml:"structure"`
	PersonalVolume       float64                      `yaml:"personal_volume"`
	GroupVolume          float64                      `yaml:"group_volume"`
	MaxGroupVolumePerLeg float64                      `yaml:"max_group_volume_per_leg"`
	MinRetailVolume      float64                      `yaml:"min_retail_volume"`
	DistributorCount     *DistributorCountRequirement `yaml:"distributor_count"`
}

// DistributorCountRequirement defines distributor count requirements per leg.
type DistributorCountRequirement struct {
	Count             int     `yaml:"count"`
	MinRank           string  `yaml:"min_rank"`
	SearchMode        string  `yaml:"search_mode"`
	SearchDepth       *int    `yaml:"search_depth"`
	TotalCount        int     `yaml:"total_count"`
	MinLegGroupVolume float64 `yaml:"min_leg_group_volume"`
}

// DemotionPolicy handles the YAML union: either the string "promotion_only"
// or an object with a grace field. Custom UnmarshalYAML handles both.
type DemotionPolicy struct {
	StringValue string       // "promotion_only" when it's a plain string
	Grace       *GracePeriod // non-nil when it's the grace variant
}

// UnmarshalYAML implements custom unmarshalling for the demotion policy union type.
func (d *DemotionPolicy) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
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

// GracePeriod defines a time window before a rank demotion takes effect.
type GracePeriod struct {
	Count int    `yaml:"count"`
	Unit  string `yaml:"unit"`
}

// --- Rank tracking and features ---

// RankTrackingConfig controls rank history tracking.
type RankTrackingConfig struct {
	TrackAchievedRank bool `yaml:"track_achieved_rank"`
}

// RankFeaturesConfig holds feature flags for rank-related behavior.
type RankFeaturesConfig struct {
	ConstraintsEnabled bool `yaml:"constraints_enabled"`
	OverridesEnabled   bool `yaml:"overrides_enabled"`
}

// --- Commission eligibility ---

// CommissionEligibility defines rules for commission eligibility.
type CommissionEligibility struct {
	MinPersonalVolume    float64         `yaml:"min_personal_volume"`
	RequireOrderInPeriod bool            `yaml:"require_order_in_period"`
	EligibleStatuses     []string        `yaml:"eligible_statuses"`
	ActiveLegTiers       []ActiveLegTier `yaml:"active_leg_tiers"`
}

// ActiveLegTier maps an active leg count to a commission depth.
type ActiveLegTier struct {
	MinActiveLegs      int `yaml:"min_active_legs"`
	MaxCommissionDepth int `yaml:"max_commission_depth"`
}

// --- Structures ---

// StructureConfig holds the flat YAML representation of a structure.
// The commission block varies by type and is deferred for two-pass parsing.
type StructureConfig struct {
	Name          string                 `yaml:"name"`
	Type          string                 `yaml:"type"`
	CommissionRaw *yaml.Node             `yaml:"commission"`
	Structure     *MatrixStructureParams `yaml:"structure"`
	Pruning       *PruningConfig         `yaml:"pruning"`
	// resolvedCommission holds the parsed commission config after type resolution.
	// Not exported. Set by resolveCommissions() during pipeline execution.
	resolvedCommission any
}

// Per-type commission configs. Populated by resolveCommissions() after
// initial unmarshal, based on the Type field.

// UnilevelCommission holds commission configuration for unilevel structures.
type UnilevelCommission struct {
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier"`
	CommissionableDepth      int                           `yaml:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression"`
}

// BinaryCommission holds commission configuration for binary structures.
type BinaryCommission struct {
	VolumeToDollarMultiplier *float64         `yaml:"volume_to_dollar_multiplier"`
	Mode                     string           `yaml:"mode"`
	Pairing                  *PairingConfig   `yaml:"pairing"`
	CycleStep                *CycleStepConfig `yaml:"cycle_step"`
}

// MatrixCommission holds commission configuration for matrix structures.
type MatrixCommission struct {
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier"`
	CommissionableDepth      int                           `yaml:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression"`
}

// StairstepCommission holds commission configuration for stairstep structures.
type StairstepCommission struct {
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier"`
	CommissionableDepth      int                           `yaml:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression"`
	Breakaway                *BreakawayConfig              `yaml:"breakaway"`
}

// GenerationCommission holds commission configuration for generation structures.
type GenerationCommission struct {
	LevelCommissionsEnabled  bool                          `yaml:"level_commissions_enabled"`
	BroadCommissionPercent   float64                       `yaml:"broad_commission_percent"`
	VolumeToDollarMultiplier *float64                      `yaml:"volume_to_dollar_multiplier"`
	CommissionableDepth      int                           `yaml:"commissionable_depth"`
	RateTable                map[string]map[string]float64 `yaml:"rate_table"`
	Compression              *CompressionConfig            `yaml:"compression"`
	Generation               GenerationCommissionConfig    `yaml:"generation"`
}

// StreamlineCommission holds commission configuration for streamline structures.
type StreamlineCommission struct {
	VolumeToDollarMultiplier *float64                   `yaml:"volume_to_dollar_multiplier"`
	CommissionableDepth      int                        `yaml:"commissionable_depth"`
	DynamicCompression       map[string]StreamlineLevel `yaml:"dynamic_compression"`
	Streams                  *StreamConfig              `yaml:"streams"`
}

// --- Shared commission sub-types ---

// CompressionConfig controls skipping inactive distributors in commission calculations.
type CompressionConfig struct {
	Enabled       bool    `yaml:"enabled"`
	Mode          string  `yaml:"mode"`
	RankThreshold *string `yaml:"rank_threshold"`
}

// PairingConfig holds binary pairing commission configuration.
type PairingConfig struct {
	Percent           float64  `yaml:"percent"`
	Calculation       string   `yaml:"calculation"`
	CapPerPeriod      *float64 `yaml:"cap_per_period"`
	VolumeAfterPayout string   `yaml:"volume_after_payout"`
	CarryForwardCap   *float64 `yaml:"carry_forward_cap"`
}

// CycleStepConfig holds binary cycle/step commission configuration.
type CycleStepConfig struct {
	Steps           []CycleStep `yaml:"steps"`
	FlushAfterCycle bool        `yaml:"flush_after_cycle"`
}

// CycleStep represents a single step in a binary cycle/step commission.
type CycleStep struct {
	Threshold float64 `yaml:"threshold"`
	Amount    float64 `yaml:"amount"`
}

// BreakawayConfig holds stairstep breakaway configuration.
type BreakawayConfig struct {
	ThresholdRank                string                     `yaml:"threshold_rank"`
	GroupVolumeExcludesBreakaway bool                       `yaml:"group_volume_excludes_breakaway"`
	OverrideCalculation          string                     `yaml:"override_calculation"`
	Differential                 *DifferentialConfig        `yaml:"differential"`
	Generation                   *BreakawayGenerationConfig `yaml:"generation"`
}

// DifferentialConfig holds differential override commission configuration.
type DifferentialConfig struct {
	RankRates   map[string]float64 `yaml:"rank_rates"`
	MinOverride float64            `yaml:"min_override"`
}

// BreakawayGenerationConfig holds generation override configuration for breakaway groups.
type BreakawayGenerationConfig struct {
	MaxGenerations  int                `yaml:"max_generations"`
	GenerationRates map[string]float64 `yaml:"generation_rates"`
	BoundaryRank    string             `yaml:"boundary_rank"`
}

// GenerationCommissionConfig holds configuration for generation-based commissions.
type GenerationCommissionConfig struct {
	MaxGenerations                int                `yaml:"max_generations"`
	GenerationRates               map[string]float64 `yaml:"generation_rates"`
	BoundaryMode                  string             `yaml:"boundary_mode"`
	BoundaryRank                  string             `yaml:"boundary_rank"`
	EmptyGenerationConsumesNumber bool               `yaml:"empty_generation_consumes_number"`
	VolumeToDollarMultiplier      *float64           `yaml:"volume_to_dollar_multiplier"`
}

// StreamlineLevel holds commission configuration for a single streamline level.
type StreamlineLevel struct {
	MinRank string  `yaml:"min_rank"`
	Percent float64 `yaml:"percent"`
}

// StreamConfig holds multi-stream configuration for streamline plans.
type StreamConfig struct {
	AdditionalPerRank   map[string]int `yaml:"additional_per_rank"`
	AssignmentMode      string         `yaml:"assignment_mode"`
	PerEnrollmentChoice bool           `yaml:"per_enrollment_choice"`
	FreezeOnDemotion    bool           `yaml:"freeze_on_demotion"`
}

// --- Matrix ---

// MatrixStructureParams defines matrix tree shape parameters.
type MatrixStructureParams struct {
	Width              int    `yaml:"width"`
	Height             int    `yaml:"height"`
	SpilloverDirection string `yaml:"spillover_direction"`
}

// PruningConfig controls handling of inactive nodes in a matrix.
type PruningConfig struct {
	Mode string `yaml:"mode"`
}

// --- Bonuses ---

// BonusConfig holds all bonus program configurations.
type BonusConfig struct {
	Matching              *MatchingBonusConfig              `yaml:"matching"`
	Sponsor               *SponsorBonusConfig               `yaml:"sponsor"`
	FastStart             *FastStartBonusConfig             `yaml:"fast_start"`
	RankAdvancement       *RankAdvancementBonusConfig       `yaml:"rank_advancement"`
	LeadershipDevelopment *LeadershipDevelopmentBonusConfig `yaml:"leadership_development"`
	Infinity              *InfinityBonusConfig              `yaml:"infinity"`
	Lifestyle             *LifestyleBonusConfig             `yaml:"lifestyle"`
	Pool                  []PoolBonusConfig                 `yaml:"pool"`
	MatrixCompletion      *MatrixCompletionBonusConfig      `yaml:"matrix_completion"`
	Position              *PositionBonusConfig              `yaml:"position"`
	BoardCycling          *BoardCyclingConfig               `yaml:"board_cycling"`
	PassUp                *PassUpConfig                     `yaml:"pass_up"`
}

// MatchingBonusConfig pays a percentage of downline commissions.
type MatchingBonusConfig struct {
	Depth                  int                `yaml:"depth"`
	Rates                  map[string]float64 `yaml:"rates"`
	MatchedCommissionTypes []string           `yaml:"matched_commission_types"`
}

// SponsorBonusConfig pays a bonus when a new distributor enrolls.
type SponsorBonusConfig struct {
	Amount             float64  `yaml:"amount"`
	AmountType         string   `yaml:"amount_type"`
	QualifyingProducts []string `yaml:"qualifying_products"`
}

// FastStartBonusConfig provides enhanced commissions during the early enrollment window.
type FastStartBonusConfig struct {
	WindowDays int                           `yaml:"window_days"`
	RateTable  map[string]map[string]float64 `yaml:"rate_table"`
}

// RankAdvancementBonusConfig pays a one-time bonus for reaching a new rank.
type RankAdvancementBonusConfig struct {
	Amounts     map[string]float64 `yaml:"amounts"`
	PayOnceOnly bool               `yaml:"pay_once_only"`
}

// LeadershipDevelopmentBonusConfig pays override commissions on downline leaders.
type LeadershipDevelopmentBonusConfig struct {
	Depth        int                `yaml:"depth"`
	Rates        map[string]float64 `yaml:"rates"`
	RankSkipMode string             `yaml:"rank_skip_mode"`
}

// InfinityBonusConfig pays commissions to unlimited depth until blocked.
type InfinityBonusConfig struct {
	BlockerMode     string             `yaml:"blocker_mode"`
	FlatRate        *float64           `yaml:"flat_rate"`
	DecreasingRates map[string]float64 `yaml:"decreasing_rates"`
	RateMode        string             `yaml:"rate_mode"`
}

// LifestyleBonusConfig pays a recurring bonus for maintaining qualifying rank.
type LifestyleBonusConfig struct {
	Tiers []LifestyleTier `yaml:"tiers"`
}

// LifestyleTier represents a single tier in the lifestyle bonus program.
type LifestyleTier struct {
	MinRank      string  `yaml:"min_rank"`
	Amount       float64 `yaml:"amount"`
	GracePeriods int     `yaml:"grace_periods"`
}

// PoolBonusConfig defines a revenue pool shared among qualifying distributors.
type PoolBonusConfig struct {
	Name                     string            `yaml:"name"`
	SourcePercent            float64           `yaml:"source_percent"`
	Qualification            PoolQualification `yaml:"qualification"`
	Shares                   PoolShares        `yaml:"shares"`
	RequireAdminConfirmation bool              `yaml:"require_admin_confirmation"`
}

// PoolQualification defines qualification rules for pool bonus participation.
type PoolQualification struct {
	Mode     string                 `yaml:"mode"`
	MinRank  *string                `yaml:"min_rank"`
	Velocity *VelocityQualification `yaml:"velocity"`
}

// VelocityQualification defines volume velocity requirements for pool bonuses.
type VelocityQualification struct {
	VolumeTarget  float64 `yaml:"volume_target"`
	Timeframe     string  `yaml:"timeframe"`
	TimeframeDays *int    `yaml:"timeframe_days"`
}

// PoolShares defines how pool bonus payouts are divided among qualifiers.
type PoolShares struct {
	Mode          string   `yaml:"mode"`
	EqualShareCap *float64 `yaml:"equal_share_cap"`
}

// MatrixCompletionBonusConfig pays a bonus for completing matrix levels.
type MatrixCompletionBonusConfig struct {
	PerLevel   map[string]float64 `yaml:"per_level"`
	FullMatrix float64            `yaml:"full_matrix"`
}

// PositionBonusConfig pays a bonus based on tree position.
type PositionBonusConfig struct {
	Amount        float64 `yaml:"amount"`
	AmountType    string  `yaml:"amount_type"`
	SponsoredOnly bool    `yaml:"sponsored_only"`
}

// BoardCyclingConfig is reserved for future board cycling implementation.
type BoardCyclingConfig struct {
	Reserved bool `yaml:"_reserved"`
}

// PassUpConfig defines pass-up bonus behavior where initial sales go to the upline.
type PassUpConfig struct {
	Count               int  `yaml:"count"`
	IncludesCommissions bool `yaml:"includes_commissions"`
}

// --- Payout ---

// PayoutConfig controls payout processing and payment methods.
type PayoutConfig struct {
	BaseCurrency        string          `yaml:"base_currency"`
	MinimumAmount       float64         `yaml:"minimum_amount"`
	SplitPayoutsEnabled bool            `yaml:"split_payouts_enabled"`
	Methods             []PaymentMethod `yaml:"methods"`
}

// PaymentMethod represents a payment method available for commission payouts.
type PaymentMethod struct {
	Type string  `yaml:"type"`
	Fee  float64 `yaml:"fee"`
}

// --- Caps ---

// CapsConfig controls commission caps and clawback behavior.
type CapsConfig struct {
	PerDistributorPerPeriod *float64 `yaml:"per_distributor_per_period"`
	CompanyPayoutCapPercent float64  `yaml:"company_payout_cap_percent"`
	CapEnforcement          string   `yaml:"cap_enforcement"`
	ClawbackOnRefund        bool     `yaml:"clawback_on_refund"`
}

// --- Placement ---

// PlacementConfig defines placement rules for new distributors.
type PlacementConfig struct {
	DonatedPlacementEnabled     bool                   `yaml:"donated_placement_enabled"`
	DonatedPlacementRestriction *string                `yaml:"donated_placement_restriction"`
	HoldingTank                 *HoldingTankConfig     `yaml:"holding_tank"`
	Binary                      *BinaryPlacementConfig `yaml:"binary"`
	Matrix                      *MatrixPlacementConfig `yaml:"matrix"`
}

// HoldingTankConfig defines holding tank behavior for deferred placement.
type HoldingTankConfig struct {
	Enabled              bool     `yaml:"enabled"`
	ExpirationDays       int      `yaml:"expiration_days"`
	AllowSponsorChange   bool     `yaml:"allow_sponsor_change"`
	ApplicableStructures []string `yaml:"applicable_structures"`
}

// BinaryPlacementConfig defines binary-specific placement rules.
type BinaryPlacementConfig struct {
	DefaultPlacement  string `yaml:"default_placement"`
	PerUserPreference bool   `yaml:"per_user_preference"`
	SpilloverEnabled  bool   `yaml:"spillover_enabled"`
}

// MatrixPlacementConfig defines matrix-specific placement settings.
type MatrixPlacementConfig struct {
	SpilloverDirection string `yaml:"spillover_direction"`
}
