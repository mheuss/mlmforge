package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidPlanPassesAllRules(t *testing.T) {
	plan := minimalPlan()
	errs := validateBusinessRules(plan)
	assert.Empty(t, errs, "minimal valid plan should produce no validation errors")
}

func TestRankDuplicateOrdinalRejected(t *testing.T) {
	plan := minimalPlan()
	// Give both ranks the same ordinal.
	plan.Ranks[1].Ordinal = 1

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "ordering_violation", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
}

func TestRankQualifiedStructuresMustExist(t *testing.T) {
	plan := minimalPlan()
	// Reference a structure that does not exist in plan.Structures.
	plan.Ranks[0].QualifiedStructures = []string{"NonExistent"}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "qualified_structures")
}

func TestRankQualificationStructureMustExist(t *testing.T) {
	plan := minimalPlan()
	// Reference a structure in qualification that does not exist.
	plan.Ranks[0].Qualification.Structures = []StructureQualification{
		{Structure: "DoesNotExist"},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "qualification/structures")
}

func TestMaxGroupVolumePerLegMustNotExceedGroupVolume(t *testing.T) {
	plan := minimalPlan()
	// Set max_group_volume_per_leg higher than group_volume.
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:            "Primary",
			PersonalVolume:       100,
			GroupVolume:          5000,
			MaxGroupVolumePerLeg: 8000,
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_field_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
}

func TestPayOnceOnlyRequiresTrackAchievedRank(t *testing.T) {
	plan := minimalPlan()
	plan.RankTracking.TrackAchievedRank = false
	plan.Bonuses.RankAdvancement = &RankAdvancementBonusConfig{
		Amounts:     map[string]float64{"Silver": 500},
		PayOnceOnly: true,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_section_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
}

func TestPayoutLagWarning(t *testing.T) {
	plan := minimalPlan()
	plan.Period.PayoutLagDays = 45

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, SeverityWarning, errs[0].Severity)
	assert.Equal(t, "long_payout_lag", errs[0].Code)
}

func TestBoundaryRankMustExist(t *testing.T) {
	plan := minimalPlan()
	// Change structure to generation type with a boundary_rank that doesn't exist.
	plan.Structures[0].Name = "Gen"
	plan.Structures[0].Type = "generation"
	plan.Structures[0].resolvedCommission = &GenerationCommission{
		Generation: GenerationCommissionConfig{
			MaxGenerations: 3,
			BoundaryRank:   "Platinum",
		},
	}
	// Update rank references to match the new structure name.
	plan.Ranks[0].QualifiedStructures = []string{"Gen"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Gen"}}
	plan.Ranks[1].QualifiedStructures = []string{"Gen"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Gen", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "/structures/0")
}

func TestUnlimitedDepthTierMustBeLast(t *testing.T) {
	plan := minimalPlan()
	// An unlimited tier (MaxCommissionDepth == 0) that is NOT last.
	plan.CommissionEligibility.ActiveLegTiers = []ActiveLegTier{
		{MinActiveLegs: 0, MaxCommissionDepth: 0},
		{MinActiveLegs: 3, MaxCommissionDepth: 5},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "ordering_violation", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "active_leg_tiers")
}

func TestRateTableMissingRankWarning(t *testing.T) {
	plan := minimalPlan()
	// Set a resolved unilevel commission whose rate_table only covers Associate,
	// missing Silver.
	plan.Structures[0].resolvedCommission = &UnilevelCommission{
		RateTable: map[string]map[string]float64{
			"Associate": {"1": 0.05},
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, SeverityWarning, errs[0].Severity)
	assert.Equal(t, "incomplete_rate_table", errs[0].Code)
	assert.Contains(t, errs[0].Path, "/structures/0")
}

func TestDonatedPlacementEnabledRequiresRestriction(t *testing.T) {
	plan := minimalPlan()
	plan.Placement.DonatedPlacementEnabled = true
	plan.Placement.DonatedPlacementRestriction = nil

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_field_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "donated_placement")
}

func TestBreakawayThresholdRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Nonexistent",
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "breakaway/threshold_rank")
}

func TestBreakawayDifferentialRankRatesMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type:                overrideStrategySingleWalk,
				OverrideCalculation: "differential",
				Differential: &DifferentialConfig{
					RankRates: map[string]float64{
						"Silver": 0.05,
						"Typo":   0.08,
					},
				},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "overrides/differential/rank_rates")
}

// TestBreakawayGenerationMaxGenerationsMustBeAtLeastOne verifies that a
// breakaway single_walk generation override with max_generations = 0 is
// rejected. A zero excludes every earner in the override walk — the same trap
// the main generation config guards. The schema rejects it too, but this closes
// the schema-bypass path (HEU-513 final-review finding).
func TestBreakawayGenerationMaxGenerationsMustBeAtLeastOne(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type:                overrideStrategySingleWalk,
				OverrideCalculation: "generation",
				Generation: &BreakawayGenerationConfig{
					MaxGenerations:  0,
					GenerationRates: map[string]float64{"1": 0.10},
					BoundaryRank:    "Silver",
				},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "value_out_of_range", errs[0].Code)
	assert.Contains(t, errs[0].Path, "breakaway/overrides/generation/max_generations")
}

// TestBreakawayFixedOverrideRankRatesMustExist mirrors the differential check
// for fixed_override. The original validateStructureRefs had a parallel gap —
// fixed_override rank-rates were not cross-checked against the defined ranks,
// so a typo passed business-rule validation silently. CodeRabbit flagged this
// on the HEU-428 PR and the gap was closed alongside the multi-tier work.
func TestBreakawayFixedOverrideRankRatesMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type:                overrideStrategySingleWalk,
				OverrideCalculation: "fixed_override",
				FixedOverride: &FixedOverrideConfig{
					RankRates: map[string]float64{
						"Silver": 0.05,
						"Typo":   0.08,
					},
				},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "overrides/fixed_override/rank_rates")
}

func TestPoolQualificationMinRankMustExist(t *testing.T) {
	plan := minimalPlan()
	badRank := "Nonexistent"
	plan.Bonuses.Pool = []PoolBonusConfig{
		{
			Name:          "Leaders Pool",
			SourcePercent: 0.02,
			Qualification: PoolQualification{
				Mode:    "rank",
				MinRank: &badRank,
			},
			Shares: PoolShares{Mode: "equal"},
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "pool/0/qualification/min_rank")
}

func TestStreamlineDynamicCompressionMinRankMustExist(t *testing.T) {
	plan := minimalPlan()
	// Add streamline alongside the existing unilevel companion.
	plan.Structures = append(plan.Structures, StructureConfig{
		Name: "Stream",
		Type: "streamline",
		resolvedCommission: &StreamlineCommission{
			CommissionableDepth: 5,
			DynamicCompression: map[string]StreamlineLevel{
				"1": {MinRank: "Associate", Percent: 0.05},
				"2": {MinRank: "Nonexistent", Percent: 0.03},
			},
		},
	})

	errs := validateBusinessRules(plan)
	var foundUndefinedRef bool
	for _, e := range errs {
		if e.Code == "undefined_reference" {
			foundUndefinedRef = true
		}
	}
	assert.True(t, foundUndefinedRef, "should find undefined_reference for Nonexistent rank")
}

func TestCarryForwardCapRequiresCarryForward(t *testing.T) {
	plan := minimalPlan()
	// Change structure to binary with carry_forward_cap set but wrong volume_after_payout.
	cap := 5000.0
	plan.Structures[0].Name = "Binary"
	plan.Structures[0].Type = "binary"
	plan.Structures[0].resolvedCommission = &BinaryCommission{
		Mode: "pairing",
		Pairing: &PairingConfig{
			Percent:           10,
			Calculation:       "lesser_leg",
			VolumeAfterPayout: "flush",
			CarryForwardCap:   &cap,
		},
	}
	// Update rank references to match the new structure name.
	plan.Ranks[0].QualifiedStructures = []string{"Binary"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Binary"}}
	plan.Ranks[1].QualifiedStructures = []string{"Binary"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Binary", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_field_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "/structures/0")
}

func TestRankDescendingOrdinalRejected(t *testing.T) {
	plan := minimalPlan()
	// Ordinal 2 then 1 (descending, not ascending).
	plan.Ranks[0].Ordinal = 2
	plan.Ranks[1].Ordinal = 1

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "ordering_violation", errs[0].Code)
	assert.Contains(t, errs[0].Message, "not ascending")
}

func TestDistributorCountMinRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			DistributorCount: &DistributorCountRequirement{
				Count:   2,
				MinRank: "Nonexistent",
			},
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "distributor_count/min_rank")
}

func TestDistributorCountMinRankMustBeLowerOrdinal(t *testing.T) {
	plan := minimalPlan()
	// Silver (ordinal 2) requires min_rank Silver (same ordinal — not lower).
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			DistributorCount: &DistributorCountRequirement{
				Count:   2,
				MinRank: "Silver",
			},
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "ordering_violation", errs[0].Code)
	assert.Contains(t, errs[0].Path, "distributor_count/min_rank")
}

func TestLegQualityContainsRankMinRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			LegQuality: []LegQualityRequirement{
				{Count: 2, Predicate: LegPredicate{Type: "contains_rank", MinRank: "Nonexistent"}},
			},
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "leg_quality/0/predicate/min_rank")
}

func TestLegQualityContainsRankMinRankMustBeLowerOrdinal(t *testing.T) {
	plan := minimalPlan()
	// Silver (ordinal 2) references min_rank Silver (same ordinal — not lower).
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			LegQuality: []LegQualityRequirement{
				{Count: 1, Predicate: LegPredicate{Type: "contains_rank", MinRank: "Silver"}},
			},
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "ordering_violation", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "leg_quality/0/predicate/min_rank")
}

func TestLegQualityContainsRankValidMinRankPasses(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			LegQuality: []LegQualityRequirement{
				{Count: 2, Predicate: LegPredicate{Type: "contains_rank", MinRank: "Associate"}},
			},
		},
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestWindowThresholdRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Nonexistent",
		QualifyingPeriods: 2,
		WindowPeriods:     3,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Equal(t, "/ranks/1/qualification/window/threshold_rank", errs[0].Path)
}

func TestWindowQualifyingPeriodsExceedsWindowPeriods(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Associate",
		QualifyingPeriods: 4,
		WindowPeriods:     3,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_field_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Equal(t, "/ranks/1/qualification/window", errs[0].Path)
}

func TestWindowQualifyingPeriodsZeroRejected(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Associate",
		QualifyingPeriods: 0,
		WindowPeriods:     3,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_field_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Equal(t, "/ranks/1/qualification/window", errs[0].Path)
}

func TestWindowWindowPeriodsZeroRejected(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Associate",
		QualifyingPeriods: 1,
		WindowPeriods:     0,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_field_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Equal(t, "/ranks/1/qualification/window", errs[0].Path)
}

func TestWindowThresholdRankEqualOrHigherOrdinalAccepted(t *testing.T) {
	plan := minimalPlan()
	// Silver (ordinal 2) references threshold_rank Silver (same ordinal).
	// This must pass — threshold_rank is NOT required to be lower than the current rank.
	plan.Ranks[1].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Silver",
		QualifyingPeriods: 2,
		WindowPeriods:     3,
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestTenureThresholdRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Tenure = &TenureRequirement{
		ThresholdRank: "Nonexistent",
		Periods:       3,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Equal(t, "/ranks/1/qualification/tenure/threshold_rank", errs[0].Path)
}

func TestTenurePeriodsZeroRejected(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Tenure = &TenureRequirement{
		ThresholdRank: "Associate",
		Periods:       0,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "cross_field_dependency", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Equal(t, "/ranks/1/qualification/tenure", errs[0].Path)
}

func TestTenureThresholdRankEqualOrHigherOrdinalAccepted(t *testing.T) {
	plan := minimalPlan()
	// Silver (ordinal 2) references threshold_rank Silver (same ordinal).
	// Tenure does NOT require threshold_rank to be lower than the current rank.
	plan.Ranks[1].Qualification.Tenure = &TenureRequirement{
		ThresholdRank: "Silver",
		Periods:       3,
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestLegQualityContainsPersonalVolumeSkipsRankCheck(t *testing.T) {
	plan := minimalPlan()
	// A contains_personal_volume predicate must not trigger the rank check.
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			LegQuality: []LegQualityRequirement{
				{Count: 3, Predicate: LegPredicate{Type: "contains_personal_volume", MinPersonalVolume: 200}},
			},
		},
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestMatchedCommissionTypesMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Bonuses.Matching = &MatchingBonusConfig{
		Depth:                  2,
		Rates:                  map[string]float64{"1": 0.10},
		MatchedCommissionTypes: []string{"unilevel", "nonexistent"},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Message, "nonexistent")
}

func TestRankAdvancementAmountsMustReferenceDefinedRanks(t *testing.T) {
	plan := minimalPlan()
	plan.RankTracking.TrackAchievedRank = true
	plan.Bonuses.RankAdvancement = &RankAdvancementBonusConfig{
		Amounts:     map[string]float64{"Nonexistent": 500},
		PayOnceOnly: false,
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Message, "Nonexistent")
}

func TestLifestyleTierMinRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Bonuses.Lifestyle = &LifestyleBonusConfig{
		Tiers: []LifestyleTier{
			{MinRank: "Nonexistent", Amount: 100, GracePeriods: 1},
		},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "lifestyle/tiers/0/min_rank")
}

func TestActiveLegTiersMustBeAscending(t *testing.T) {
	plan := minimalPlan()
	// Descending order: 5, 3 (with base tier present to isolate sorting check).
	plan.CommissionEligibility.ActiveLegTiers = []ActiveLegTier{
		{MinActiveLegs: 0, MaxCommissionDepth: 2},
		{MinActiveLegs: 5, MaxCommissionDepth: 10},
		{MinActiveLegs: 3, MaxCommissionDepth: 5},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "ordering_violation", errs[0].Code)
	assert.Contains(t, errs[0].Path, "active_leg_tiers")
}

func TestActiveLegTiersMustIncludeBaseTier(t *testing.T) {
	plan := minimalPlan()
	// Tiers without a min_active_legs=0 base tier.
	plan.CommissionEligibility.ActiveLegTiers = []ActiveLegTier{
		{MinActiveLegs: 3, MaxCommissionDepth: 5},
		{MinActiveLegs: 5, MaxCommissionDepth: 7},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "missing_base_tier", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "active_leg_tiers")
}

func TestLargeMatrixWarning(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "BigMatrix"
	plan.Structures[0].Type = "matrix"
	plan.Structures[0].Structure = &MatrixStructureParams{
		Width:  10,
		Height: 7, // 10^7 = 10,000,000 > 1,000,000
	}
	plan.Structures[0].resolvedCommission = &MatrixCommission{
		CommissionableDepth: 7,
		RateTable:           map[string]map[string]float64{"Associate": {"1": 0.05}},
	}
	plan.Ranks[0].QualifiedStructures = []string{"BigMatrix"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "BigMatrix"}}
	plan.Ranks[1].QualifiedStructures = []string{"BigMatrix"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "BigMatrix", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "large_matrix" {
			found = true
			assert.Equal(t, SeverityWarning, e.Severity)
			break
		}
	}
	assert.True(t, found, "expected large_matrix warning, got: %v", errs)
}

// TestStreamlineStreamsRequireAdditionalPerRank verifies that a streamline
// structure with a streams block but a nil additional_per_rank is rejected at
// the Go bypass layer. A nil Go map marshals to JSON null, which the Rust engine
// rejects opaquely; the schema requires the field, so this guard mirrors that
// requirement for schema-bypassing callers (analogous to the start_date guard).
// An explicit empty map {} stays valid (HEU-513 Task 8A). Copilot flagged this
// on PR #53.
func TestStreamlineStreamsRequireAdditionalPerRank(t *testing.T) {
	newStreamPlan := func(apr map[string]uint8) *CompensationPlan {
		p := minimalPlan()
		p.Structures[0].Name = "Stream"
		p.Structures[0].Type = "streamline"
		p.Structures[0].resolvedCommission = &StreamlineCommission{Streams: &StreamConfig{AdditionalPerRank: apr}}
		p.Ranks[0].QualifiedStructures = []string{"Stream"}
		p.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stream"}}
		p.Ranks[1].QualifiedStructures = []string{"Stream"}
		p.Ranks[1].Qualification.Structures = []StructureQualification{
			{Structure: "Stream", PersonalVolume: 100, GroupVolume: 3000},
		}
		return p
	}
	missingErr := func(errs []ValidationError) bool {
		for _, e := range errs {
			if e.Code == "missing_required_field" && strings.Contains(e.Path, "streams/additional_per_rank") {
				return true
			}
		}
		return false
	}

	// nil (omitted) additional_per_rank -> rejected loud at Go, not opaque at Rust.
	assert.True(t, missingErr(validateBusinessRules(newStreamPlan(nil))),
		"nil additional_per_rank must be rejected")
	// explicit empty {} -> allowed (Task 8A: empty stays present).
	assert.False(t, missingErr(validateBusinessRules(newStreamPlan(map[string]uint8{}))),
		"explicit empty additional_per_rank must be allowed")
}

// TestMatrixSpilloverDepthFirstRejected verifies that a matrix structure whose
// spillover_direction is not breadth_first is rejected at business-rule
// validation. The schema restricts the value and the engine's MatrixTree::new
// rejects depth_first, but nothing guarded the Go bypass path before this
// (HEU-513 final-review finding).
func TestMatrixSpilloverDepthFirstRejected(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Grid"
	plan.Structures[0].Type = "matrix"
	plan.Structures[0].Structure = &MatrixStructureParams{
		Width:              3,
		Height:             4,
		SpilloverDirection: "depth_first",
	}
	plan.Structures[0].resolvedCommission = &MatrixCommission{
		CommissionableDepth: 4,
		RateTable:           map[string]map[string]float64{"Associate": {"1": 0.05}},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Grid"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Grid"}}
	plan.Ranks[1].QualifiedStructures = []string{"Grid"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Grid", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "invalid_value" && strings.Contains(e.Path, "structure/spillover_direction") {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
			break
		}
	}
	assert.True(t, found, "expected spillover_direction invalid_value error, got: %v", errs)
}

// TestBreakawayMultiTierEmptyTiersRejected verifies that a multi-tier
// breakaway with an empty Tiers slice produces an invalid_value error.
// The schema also enforces minItems: 1, but rules.go owns the structural
// invariant alongside the existing breakaway reference checks.
func TestBreakawayMultiTierEmptyTiersRejected(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type:  overrideStrategyMultiTier,
				Tiers: []BreakawayTier{},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "invalid_value", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "/structures/0/commission/breakaway/overrides/tiers")
}

// TestBreakawayMultiTierRateOutOfRangeRejected verifies that a tier with a
// rate outside [0, 1] produces an invalid_value error pointing at the
// offending tier's rate field.
func TestBreakawayMultiTierRateOutOfRangeRejected(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type: overrideStrategyMultiTier,
				Tiers: []BreakawayTier{
					{MinSplitOutGroups: 1, Rate: 0.05},
					{MinSplitOutGroups: 2, Rate: 1.5}, // out of range
				},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "invalid_value", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "/structures/0/commission/breakaway/overrides/tiers/1/rate")
}

// TestBreakawayMultiTierRateAtBoundsAccepted pins the inclusive contract:
// rates of exactly 0.0 and 1.0 are valid. A future change to strict
// inequalities (e.g., `< 0 || >= 1`) would break this test.
func TestBreakawayMultiTierRateAtBoundsAccepted(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type: overrideStrategyMultiTier,
				Tiers: []BreakawayTier{
					{MinSplitOutGroups: 1, Rate: 0.0},
					{MinSplitOutGroups: 2, Rate: 1.0},
				},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

// TestBreakawayMultiTierRateJustOverOneRejected pins the upper bound by
// rejecting a rate one ULP above 1. Paired with the at-bounds test above to
// guard against an off-by-epsilon regression.
func TestBreakawayMultiTierRateJustOverOneRejected(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type: overrideStrategyMultiTier,
				Tiers: []BreakawayTier{
					{MinSplitOutGroups: 1, Rate: 1.0000001},
				},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "invalid_value", errs[0].Code)
	assert.Contains(t, errs[0].Path, "/structures/0/commission/breakaway/overrides/tiers/0/rate")
}

// TestBreakawayMultiTierTooManyTiersRejected verifies the 255-tier cap.
// The cap exists because the engine's multi-tier walk derives each tier's
// 1-based depth floor as a u8; a 256th tier would overflow the conversion
// and panic. The schema enforces maxItems: 255 and rules.go mirrors it.
func TestBreakawayMultiTierTooManyTiersRejected(t *testing.T) {
	const tooMany = 256

	tiers := make([]BreakawayTier, tooMany)
	for i := range tiers {
		tiers[i] = BreakawayTier{MinSplitOutGroups: 1, Rate: 0.01}
	}

	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type:  overrideStrategyMultiTier,
				Tiers: tiers,
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "invalid_value", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Path, "/structures/0/commission/breakaway/overrides/tiers")
}

// TestBreakawayMultiTierExactly255TiersAccepted pins the inclusive upper
// bound: 255 tiers is the maximum and must not be rejected.
func TestBreakawayMultiTierExactly255TiersAccepted(t *testing.T) {
	tiers := make([]BreakawayTier, 255)
	for i := range tiers {
		tiers[i] = BreakawayTier{MinSplitOutGroups: 1, Rate: 0.01}
	}

	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type:  overrideStrategyMultiTier,
				Tiers: tiers,
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestStairstepBreakawayGenerationBoundaryRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Overrides: OverrideStrategy{
				Type: overrideStrategySingleWalk,
				Generation: &BreakawayGenerationConfig{
					MaxGenerations: 1,
					BoundaryRank:   "Nonexistent",
				},
			},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stairs"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stairs"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stairs", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "breakaway/overrides/generation/boundary_rank")
}

func TestValidation_DuplicateRankName(t *testing.T) {
	plan := minimalPlan()
	// Duplicate the first rank name on the second rank.
	plan.Ranks[1].Name = "Associate"
	plan.Ranks[1].Ordinal = 2

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "duplicate_rank_name" {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
			assert.Contains(t, e.Message, "Associate")
			break
		}
	}
	assert.True(t, found, "expected duplicate_rank_name error, got: %v", errs)
}

func TestValidation_DuplicateStructureName(t *testing.T) {
	plan := minimalPlan()
	// Add a second structure with the same name.
	plan.Structures = append(plan.Structures, StructureConfig{
		Name: "Primary",
		Type: "binary",
	})

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "duplicate_structure_name" {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
			assert.Contains(t, e.Message, "Primary")
			break
		}
	}
	assert.True(t, found, "expected duplicate_structure_name error, got: %v", errs)
}

func TestValidation_HoldingTankApplicableStructuresMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Placement.HoldingTank = &HoldingTankConfig{
		Enabled:              true,
		ExpirationDays:       30,
		ApplicableStructures: []string{"Primary", "Nonexistent"},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Message, "Nonexistent")
}

func TestValidation_HoldingTankValidStructuresPass(t *testing.T) {
	plan := minimalPlan()
	plan.Placement.HoldingTank = &HoldingTankConfig{
		Enabled:              true,
		ExpirationDays:       30,
		ApplicableStructures: []string{"Primary"},
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestValidation_StreamlineAdditionalPerRankMustExist(t *testing.T) {
	plan := minimalPlan()
	// Add streamline structure while keeping the companion unilevel.
	plan.Structures = append(plan.Structures, StructureConfig{
		Name: "Stream",
		Type: "streamline",
		resolvedCommission: &StreamlineCommission{
			CommissionableDepth: 5,
			DynamicCompression: map[string]StreamlineLevel{
				"1": {MinRank: "Associate", Percent: 0.05},
			},
			Streams: &StreamConfig{
				AdditionalPerRank: map[string]uint8{
					"Silver":      2,
					"Nonexistent": 3,
				},
				AssignmentMode: "round_robin",
			},
		},
	})

	errs := validateBusinessRules(plan)
	// Rank reference errors come from validateStructureRefs.
	var foundUndefinedRef bool
	for _, e := range errs {
		if e.Code == "undefined_reference" {
			foundUndefinedRef = true
		}
	}
	assert.True(t, foundUndefinedRef, "should find undefined_reference for Nonexistent rank in additional_per_rank")
}

// TestRateKeyWidth_RejectsKeysOverU8 verifies the u8 key-width guard on every
// level/depth-keyed rate map (HEU-513 Part B). These maps are Go
// map[string]float64 but Rust BTreeMap<u8, f64>; a key over 255 passes Go and
// dies opaquely at Rust serde. Each case sets one out-of-range key and expects
// an invalid_value error at the exact offending path.
func TestRateKeyWidth_RejectsKeysOverU8(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(*CompensationPlan)
		wantPath string
	}{
		{"matching rates", func(p *CompensationPlan) {
			p.Bonuses.Matching = &MatchingBonusConfig{Rates: map[string]float64{"300": 0.5}}
		}, "/bonuses/matching/rates/300"},
		{"leadership rates", func(p *CompensationPlan) {
			p.Bonuses.LeadershipDevelopment = &LeadershipDevelopmentBonusConfig{Rates: map[string]float64{"256": 0.5}}
		}, "/bonuses/leadership_development/rates/256"},
		{"infinity decreasing_rates", func(p *CompensationPlan) {
			p.Bonuses.Infinity = &InfinityBonusConfig{DecreasingRates: map[string]float64{"999": 0.5}}
		}, "/bonuses/infinity/decreasing_rates/999"},
		{"matrix_completion per_level", func(p *CompensationPlan) {
			p.Bonuses.MatrixCompletion = &MatrixCompletionBonusConfig{PerLevel: map[string]float64{"256": 100.0}}
		}, "/bonuses/matrix_completion/per_level/256"},
		{"fast_start rate_table inner", func(p *CompensationPlan) {
			p.Bonuses.FastStart = &FastStartBonusConfig{RateTable: map[string]map[string]float64{"Associate": {"256": 0.5}}}
		}, "/bonuses/fast_start/rate_table/Associate/256"},
		{"generation rates", func(p *CompensationPlan) {
			p.Structures[0].resolvedCommission = &GenerationCommission{
				Generation: GenerationCommissionConfig{GenerationRates: map[string]float64{"256": 0.1}},
			}
		}, "/structures/0/commission/generation/generation_rates/256"},
		{"stairstep breakaway generation_rates", func(p *CompensationPlan) {
			p.Structures[0].resolvedCommission = &StairstepCommission{
				Breakaway: &BreakawayConfig{
					Overrides: OverrideStrategy{
						Generation: &BreakawayGenerationConfig{GenerationRates: map[string]float64{"256": 0.1}},
					},
				},
			}
		}, "/structures/0/commission/breakaway/overrides/generation/generation_rates/256"},
		{"commission rate_table inner", func(p *CompensationPlan) {
			p.Structures[0].resolvedCommission = &UnilevelCommission{
				RateTable: map[string]map[string]float64{
					"Associate": {"256": 0.1},
					"Silver":    {"1": 0.1},
				},
			}
		}, "/structures/0/commission/rate_table/Associate/256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := minimalPlan()
			tc.setup(plan)
			errs := validateBusinessRules(plan)
			found := false
			for _, e := range errs {
				if e.Code == "invalid_value" && e.Path == tc.wantPath {
					found = true
					assert.Equal(t, SeverityError, e.Severity)
				}
			}
			assert.True(t, found, "expected invalid_value at %s, got: %v", tc.wantPath, errs)
		})
	}
}

// TestRateKeyWidth_RejectsNonNumericAndNegative verifies non-numeric, signed,
// and out-of-range rate keys are rejected — a u8 key must be digits only in
// [0, 255], matching the schema propertyNames pattern. "-0" and "+5" cover the
// signed forms Atoi would have leaked past the Rust u8 boundary.
func TestRateKeyWidth_RejectsNonNumericAndNegative(t *testing.T) {
	for _, key := range []string{"-1", "-0", "+5", "abc", "256"} {
		plan := minimalPlan()
		plan.Bonuses.Matching = &MatchingBonusConfig{Rates: map[string]float64{key: 0.5}}
		errs := validateBusinessRules(plan)
		found := false
		for _, e := range errs {
			if e.Code == "invalid_value" && e.Path == "/bonuses/matching/rates/"+key {
				found = true
			}
		}
		assert.True(t, found, "expected invalid_value for matching rate key %q, got: %v", key, errs)
	}
}

// TestRateKeyWidth_AcceptsU8Range verifies validateRateKeyWidths produces no
// error for keys within the u8 range (0-255). Rank-name-keyed outer keys are
// not checked. Calls the validator directly so an unrelated rule emitting
// invalid_value on the baseline plan cannot break this test spuriously.
func TestRateKeyWidth_AcceptsU8Range(t *testing.T) {
	plan := minimalPlan()
	plan.Bonuses.Matching = &MatchingBonusConfig{Rates: map[string]float64{"0": 0.1, "1": 0.2, "255": 0.3}}
	plan.Structures[0].resolvedCommission = &UnilevelCommission{
		RateTable: map[string]map[string]float64{
			"Associate": {"1": 0.1, "255": 0.2},
			"Silver":    {"1": 0.1, "255": 0.2},
		},
	}
	assert.Empty(t, validateRateKeyWidths(plan), "valid u8 keys should produce no errors")
}

func TestValidation_SearchModeFirstLevelsWithoutDepthWarning(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			DistributorCount: &DistributorCountRequirement{
				Count:      2,
				MinRank:    "Associate",
				SearchMode: "first_levels",
				// SearchDepth intentionally nil.
			},
		},
	}

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "missing_search_depth" {
			found = true
			assert.Equal(t, SeverityWarning, e.Severity)
			assert.Contains(t, e.Message, "first_levels")
			break
		}
	}
	assert.True(t, found, "expected missing_search_depth warning, got: %v", errs)
}

func TestValidation_SearchModeFirstLevelsWithDepthPasses(t *testing.T) {
	plan := minimalPlan()
	depth := uint8(3)
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{
			Structure:      "Primary",
			PersonalVolume: 100,
			GroupVolume:    3000,
			DistributorCount: &DistributorCountRequirement{
				Count:       2,
				MinRank:     "Associate",
				SearchMode:  "first_levels",
				SearchDepth: &depth,
			},
		},
	}

	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestValidation_CurrencyMismatch(t *testing.T) {
	plan := minimalPlan()
	plan.Payout.BaseCurrency = "EUR"
	plan.Volume.BaseCurrency = "USD"

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "currency_mismatch" {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
			assert.Contains(t, e.Message, "EUR")
			assert.Contains(t, e.Message, "USD")
			break
		}
	}
	assert.True(t, found, "expected currency_mismatch error, got: %v", errs)
}

func TestValidation_MatchingCurrencyPasses(t *testing.T) {
	plan := minimalPlan()
	// Both already "USD" in minimalPlan, just verify no error.
	errs := validateBusinessRules(plan)
	assert.Empty(t, errs)
}

func TestValidatePassUp_CountZero(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].resolvedCommission = &UnilevelCommission{
		CommissionableDepth: 5,
		RateTable: map[string]map[string]float64{
			"Associate": {"1": 0.05},
		},
		PassUp: &PassUpConfig{
			Count:               0,
			IncludesCommissions: false,
		},
	}

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "value_out_of_range" {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
			assert.Contains(t, e.Path, "pass_up/count")
			assert.Contains(t, e.Message, ">= 1")
			break
		}
	}
	assert.True(t, found, "expected value_out_of_range error for pass_up count=0, got: %v", errs)
}

func TestValidatePassUp_ValidConfig(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].resolvedCommission = &UnilevelCommission{
		CommissionableDepth: 5,
		RateTable: map[string]map[string]float64{
			"Associate": {"1": 0.05},
			"Silver":    {"1": 0.07, "2": 0.05},
		},
		PassUp: &PassUpConfig{
			Count:               2,
			IncludesCommissions: false,
		},
	}

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		assert.NotEqual(t, "value_out_of_range", e.Code,
			"valid pass_up config should not produce value_out_of_range, got: %s", e.Message)
	}
}

func TestValidatePassUp_NilPassUpIsValid(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].resolvedCommission = &UnilevelCommission{
		CommissionableDepth: 5,
		RateTable: map[string]map[string]float64{
			"Associate": {"1": 0.05},
			"Silver":    {"1": 0.07},
		},
	}

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		assert.NotContains(t, e.Path, "pass_up",
			"nil pass_up should not produce pass_up errors, got: %s", e.Message)
	}
}

func TestValidatePassUp_SkippedOnNonUnilevel(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Binary"
	plan.Structures[0].Type = "binary"
	plan.Structures[0].resolvedCommission = &BinaryCommission{
		Mode: "pairing",
		Pairing: &PairingConfig{
			Percent:           10,
			Calculation:       "weaker_leg",
			VolumeAfterPayout: "carry_forward",
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Binary"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Binary"}}
	plan.Ranks[1].QualifiedStructures = []string{"Binary"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Binary", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		assert.NotContains(t, e.Path, "pass_up",
			"pass_up validation should not fire on binary structures without raw pass_up, got: %s", e.Message)
	}
}

func TestValidatePassUp_RejectedOnNonUnilevelViaRaw(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Binary"
	plan.Structures[0].Type = "binary"
	plan.Structures[0].CommissionRaw = map[string]any{
		"mode": "pairing",
		"pass_up": map[string]any{
			"count":                2,
			"includes_commissions": false,
		},
	}
	plan.Structures[0].resolvedCommission = &BinaryCommission{
		Mode: "pairing",
		Pairing: &PairingConfig{
			Percent:           10,
			Calculation:       "weaker_leg",
			VolumeAfterPayout: "carry_forward",
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Binary"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Binary"}}
	plan.Ranks[1].QualifiedStructures = []string{"Binary"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Binary", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "unsupported_field" {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
			assert.Contains(t, e.Path, "pass_up")
			assert.Contains(t, e.Message, "only supported on unilevel")
			break
		}
	}
	assert.True(t, found, "expected unsupported_field error for pass_up on binary, got: %v", errs)
}

func TestValidation_MatchedCommissionTypesIncludesValidTypes(t *testing.T) {
	plan := minimalPlan()
	plan.Bonuses.Matching = &MatchingBonusConfig{
		Depth:                  2,
		Rates:                  map[string]float64{"1": 0.10},
		MatchedCommissionTypes: []string{"nonexistent"},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "valid types:")
	assert.Contains(t, errs[0].Message, "unilevel")
}

func TestValidation_BoardPlanWithoutUnilevelRejected(t *testing.T) {
	plan := minimalPlan()
	// Replace the unilevel structure with a board_plan.
	plan.Structures = []StructureConfig{
		{
			Name:               "Sales Board",
			Type:               "board_plan",
			resolvedCommission: &BoardPlanCommission{},
		},
	}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Sales Board"}}
	plan.Ranks[0].QualifiedStructures = nil
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Sales Board", PersonalVolume: 100, GroupVolume: 3000},
	}
	plan.Ranks[1].QualifiedStructures = []string{"Sales Board"}

	errs := validateBusinessRules(plan)
	var found bool
	for _, e := range errs {
		if e.Code == "missing_companion_structure" {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
			assert.Contains(t, e.Message, "companion unilevel")
			break
		}
	}
	assert.True(t, found, "expected missing_companion_structure error, got: %v", errs)
}

func TestValidation_BoardPlanWithUnilevelPasses(t *testing.T) {
	plan := minimalPlan()
	// Add a board_plan alongside the existing unilevel.
	plan.Structures = append(plan.Structures, StructureConfig{
		Name:               "Sales Board",
		Type:               "board_plan",
		resolvedCommission: &BoardPlanCommission{},
	})

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		assert.NotEqual(t, "missing_companion_structure", e.Code,
			"board_plan with companion unilevel should not produce missing_companion_structure, got: %s", e.Message)
	}
}

func TestValidation_NoBoardPlanSkipsCompanionCheck(t *testing.T) {
	plan := minimalPlan()
	// Default plan has only unilevel, no board_plan.

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		assert.NotEqual(t, "missing_companion_structure", e.Code,
			"plan without board_plan should not produce missing_companion_structure, got: %s", e.Message)
	}
}

// --- Streamline validation tests ---

func streamlinePlan() *CompensationPlan {
	plan := minimalPlan()
	plan.Ranks = append(plan.Ranks, RankDefinition{
		Name:    "Bronze",
		Ordinal: 3,
		Qualification: RankQualification{
			Structures:       []StructureQualification{},
			RequiredProducts: []string{},
		},
		DemotionPolicy: DemotionPolicy{StringValue: "promotion_only"},
	})
	plan.Structures = append(plan.Structures, StructureConfig{
		Name: "Stream",
		Type: "streamline",
		resolvedCommission: &StreamlineCommission{
			CommissionableDepth: 5,
			DynamicCompression: map[string]StreamlineLevel{
				"1": {MinRank: "Associate", Percent: 0.05},
				"2": {MinRank: "Silver", Percent: 0.04},
				"3": {MinRank: "Bronze", Percent: 0.03},
			},
		},
	})
	return plan
}

func TestValidation_StreamlineRequiresCompanionUnilevel(t *testing.T) {
	plan := streamlinePlan()
	// Remove the unilevel structure.
	plan.Structures = []StructureConfig{plan.Structures[1]}

	errs := validateBusinessRules(plan)
	found := false
	for _, e := range errs {
		if e.Code == "missing_companion_structure" {
			found = true
		}
	}
	assert.True(t, found, "streamline without unilevel should produce missing_companion_structure")
}

func TestValidation_StreamlineEmptyCompressionTableFails(t *testing.T) {
	plan := streamlinePlan()
	c := plan.Structures[1].resolvedCommission.(*StreamlineCommission)
	c.DynamicCompression = map[string]StreamlineLevel{}

	errs := validateBusinessRules(plan)
	found := false
	for _, e := range errs {
		if e.Code == "empty_compression_table" {
			found = true
		}
	}
	assert.True(t, found, "empty dynamic_compression should produce empty_compression_table")
}

func TestValidation_StreamlineNonSequentialLevelsFails(t *testing.T) {
	plan := streamlinePlan()
	c := plan.Structures[1].resolvedCommission.(*StreamlineCommission)
	c.DynamicCompression = map[string]StreamlineLevel{
		"1": {MinRank: "Associate", Percent: 0.05},
		"3": {MinRank: "Silver", Percent: 0.04},
		"4": {MinRank: "Bronze", Percent: 0.03},
	}

	errs := validateBusinessRules(plan)
	found := false
	for _, e := range errs {
		if e.Code == "non_sequential_levels" {
			found = true
		}
	}
	assert.True(t, found, "non-sequential levels should produce non_sequential_levels")
}

func TestValidation_StreamlineInvalidRankReferenceFails(t *testing.T) {
	plan := streamlinePlan()
	c := plan.Structures[1].resolvedCommission.(*StreamlineCommission)
	c.DynamicCompression["1"] = StreamlineLevel{MinRank: "Nonexistent", Percent: 0.05}

	errs := validateBusinessRules(plan)
	found := false
	for _, e := range errs {
		// Rank reference errors come from validateStructureRefs.
		if e.Code == "undefined_reference" {
			found = true
		}
	}
	assert.True(t, found, "invalid min_rank should produce undefined_reference")
}

func TestValidation_StreamlineDepthLessThanLevelsFails(t *testing.T) {
	plan := streamlinePlan()
	c := plan.Structures[1].resolvedCommission.(*StreamlineCommission)
	c.CommissionableDepth = 2 // 3 levels but only 2 depth

	errs := validateBusinessRules(plan)
	found := false
	for _, e := range errs {
		if e.Code == "depth_less_than_levels" {
			found = true
		}
	}
	assert.True(t, found, "depth < levels should produce depth_less_than_levels")
}

func TestValidation_StreamlineValidConfigPasses(t *testing.T) {
	plan := streamlinePlan()

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		if e.Severity == SeverityError {
			assert.Failf(t, "unexpected error", "valid streamline config produced error: %s (%s)", e.Message, e.Code)
		}
	}
}

func TestGenerationMaxGenerationsPerRank_UnknownRankFails(t *testing.T) {
	plan := minimalPlanWithGenerationStructure()
	gen := plan.Structures[0].resolvedCommission.(*GenerationCommission)
	gen.Generation.MaxGenerationsPerRank = map[string]uint8{"NonexistentRank": 5}

	errs := validateBusinessRules(plan)

	found := false
	for _, e := range errs {
		if e.Code == "undefined_reference" &&
			strings.Contains(e.Message, "max_generations_per_rank") &&
			strings.Contains(e.Message, "NonexistentRank") {
			found = true
			break
		}
	}
	require.True(t, found, "expected undefined_reference error for max_generations_per_rank, got: %v", errs)
}

func TestGenerationMaxGenerationsPerRank_KnownRanksPass(t *testing.T) {
	plan := minimalPlanWithGenerationStructure()
	gen := plan.Structures[0].resolvedCommission.(*GenerationCommission)
	gen.Generation.MaxGenerationsPerRank = map[string]uint8{
		"Associate": 3,
		"Silver":    5,
	}

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		require.False(t,
			e.Code == "undefined_reference" && strings.Contains(e.Message, "max_generations_per_rank"),
			"unexpected per-rank validation error: %v", e,
		)
	}
}

func TestGenerationMaxGenerationsPerRank_EmptyPasses(t *testing.T) {
	plan := minimalPlanWithGenerationStructure()
	gen := plan.Structures[0].resolvedCommission.(*GenerationCommission)
	gen.Generation.MaxGenerationsPerRank = nil

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		require.False(t,
			e.Code == "undefined_reference" && strings.Contains(e.Message, "max_generations_per_rank"),
		)
	}
}

// TestGenerationMaxGenerations_DefaultBelowOneFails pins HEU-442: the scalar
// default max_generations must be >= 1 (a 0 default would exclude every earner).
// Per-rank overrides of 0 are still allowed (tested separately) — only the
// default is guarded here.
func TestGenerationMaxGenerations_DefaultBelowOneFails(t *testing.T) {
	plan := minimalPlanWithGenerationStructure()
	gen := plan.Structures[0].resolvedCommission.(*GenerationCommission)
	gen.Generation.MaxGenerations = 0

	errs := validateBusinessRules(plan)
	found := false
	for _, e := range errs {
		if e.Code == "value_out_of_range" && strings.Contains(e.Message, "max_generations") {
			assert.Equal(t, SeverityError, e.Severity)
			found = true
		}
	}
	require.True(t, found, "expected value_out_of_range for max_generations=0, got: %v", errs)
}

func TestGenerationMaxGenerations_DefaultOnePasses(t *testing.T) {
	plan := minimalPlanWithGenerationStructure()
	gen := plan.Structures[0].resolvedCommission.(*GenerationCommission)
	gen.Generation.MaxGenerations = 1

	errs := validateBusinessRules(plan)
	for _, e := range errs {
		require.False(t,
			e.Code == "value_out_of_range" && strings.Contains(e.Message, "max_generations"),
			"unexpected max_generations error: %v", e)
	}
}

// TestValidatePeriodRequiresStartDate pins HEU-507: start_date is required. Rust
// requires it (NaiveDate); the Go schema also requires it, but a bypass path
// (programmatic builder, direct validateBusinessRules) could otherwise reach the
// engine with a nil start_date. minimalPlan() now sets a valid one.
func TestValidatePeriodRequiresStartDate(t *testing.T) {
	plan := minimalPlan()
	plan.Period.StartDate = nil

	errs := validateBusinessRules(plan)
	found := false
	for _, e := range errs {
		if e.Path == "/period/start_date" && e.Code == "missing_required_field" {
			assert.Equal(t, SeverityError, e.Severity)
			found = true
		}
	}
	require.True(t, found, "expected missing_required_field at /period/start_date, got: %v", errs)
}

func TestValidatePeriodAcceptsStartDate(t *testing.T) {
	plan := minimalPlan() // now sets a valid StartDate
	for _, e := range validateBusinessRules(plan) {
		require.NotEqual(t, "/period/start_date", e.Path,
			"a valid start_date should not produce an error: %v", e)
	}

	empty := ""
	plan.Period.StartDate = &empty
	found := false
	for _, e := range validateBusinessRules(plan) {
		if e.Path == "/period/start_date" {
			found = true
		}
	}
	require.True(t, found, "an empty start_date should also be rejected")
}

// TestValidatePeriodRejectsMalformedStartDate pins HEU-507's format half. The
// schema's "format": "date" is annotation-only under Draft 2020-12 because
// pipeline.go never calls AssertFormat, so a malformed date clears the schema
// gate and the presence check, then dies at the Rust NaiveDate boundary with an
// opaque error. Parse it at the config layer instead.
//
// "2026-7-4" is a deliberate divergence. chrono accepts unpadded components;
// Go's 2006-01-02 layout does not. Strict wins here: the schema declares
// RFC 3339 full-date, and being stricter than the engine guarantees that
// anything Go accepts, Rust also accepts. The looser direction would let the
// opaque failure back in.
func TestValidatePeriodRejectsMalformedStartDate(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"month and day out of range", "2026-13-45"},
		{"wrong shape", "07/24/2026"},
		{"day not in month", "2026-02-30"},
		{"unpadded components", "2026-7-4"},
		{"datetime not date", "2026-07-24T00:00:00Z"},
		{"no separators", "20260724"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := minimalPlan()
			v := tc.value
			plan.Period.StartDate = &v

			found := false
			for _, e := range validateBusinessRules(plan) {
				if e.Path == "/period/start_date" && e.Code == "invalid_value" {
					assert.Equal(t, SeverityError, e.Severity)
					// The offending value must survive into the message. Without
					// it the operator is back to guessing which field was bad,
					// which is the opaque failure this check exists to replace.
					assert.Contains(t, e.Message, tc.value,
						"error message should name the rejected value")
					found = true
				}
			}
			assert.True(t, found,
				"expected invalid_value at /period/start_date for %q", tc.value)
		})
	}
}

// TestValidatePeriodAcceptsWellFormedStartDate guards the other direction, so
// the format check cannot be tightened into rejecting real dates.
func TestValidatePeriodAcceptsWellFormedStartDate(t *testing.T) {
	for _, v := range []string{"2026-07-24", "2026-01-01", "2026-12-31", "2024-02-29"} {
		t.Run(v, func(t *testing.T) {
			plan := minimalPlan()
			s := v
			plan.Period.StartDate = &s
			for _, e := range validateBusinessRules(plan) {
				assert.NotEqual(t, "/period/start_date", e.Path,
					"%q is a valid date but produced: %v", v, e)
			}
		})
	}
}
