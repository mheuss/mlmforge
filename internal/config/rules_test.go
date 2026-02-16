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

func TestRankOrdinalsMustBeAscending(t *testing.T) {
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
		{MinActiveLegs: 1, MaxCommissionDepth: 0},
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
