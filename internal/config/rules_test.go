package config

import (
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
			BoundaryRank: "Platinum",
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
			ThresholdRank:       "Silver",
			OverrideCalculation: "differential",
			Differential: &DifferentialConfig{
				RankRates: map[string]float64{
					"Silver": 0.05,
					"Typo":   0.08,
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
	assert.Contains(t, errs[0].Path, "differential/rank_rates")
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
	plan.Structures[0].Name = "Stream"
	plan.Structures[0].Type = "streamline"
	plan.Structures[0].resolvedCommission = &StreamlineCommission{
		CommissionableDepth: 5,
		DynamicCompression: map[string]StreamlineLevel{
			"1": {MinRank: "Associate", Percent: 0.05},
			"2": {MinRank: "Nonexistent", Percent: 0.03},
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stream"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stream"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stream"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stream", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Contains(t, errs[0].Path, "dynamic_compression")
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

func TestStairstepBreakawayGenerationBoundaryRankMustExist(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Stairs"
	plan.Structures[0].Type = "stairstep"
	plan.Structures[0].resolvedCommission = &StairstepCommission{
		Breakaway: &BreakawayConfig{
			ThresholdRank: "Silver",
			Generation: &BreakawayGenerationConfig{
				BoundaryRank: "Nonexistent",
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
	assert.Contains(t, errs[0].Path, "breakaway/generation/boundary_rank")
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
	assert.Equal(t, "unknown_structure_ref", errs[0].Code)
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
	plan.Structures[0].Name = "Stream"
	plan.Structures[0].Type = "streamline"
	plan.Structures[0].resolvedCommission = &StreamlineCommission{
		CommissionableDepth: 5,
		DynamicCompression: map[string]StreamlineLevel{
			"1": {MinRank: "Associate", Percent: 0.05},
		},
		Streams: &StreamConfig{
			AdditionalPerRank: map[string]int{
				"Silver":      2,
				"Nonexistent": 3,
			},
			AssignmentMode: "round_robin",
		},
	}
	plan.Ranks[0].QualifiedStructures = []string{"Stream"}
	plan.Ranks[0].Qualification.Structures = []StructureQualification{{Structure: "Stream"}}
	plan.Ranks[1].QualifiedStructures = []string{"Stream"}
	plan.Ranks[1].Qualification.Structures = []StructureQualification{
		{Structure: "Stream", PersonalVolume: 100, GroupVolume: 3000},
	}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "unknown_rank_ref", errs[0].Code)
	assert.Equal(t, SeverityError, errs[0].Severity)
	assert.Contains(t, errs[0].Message, "Nonexistent")
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
	depth := 3
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
