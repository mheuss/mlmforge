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
	assert.Equal(t, "error", errs[0].Severity)
}

func TestRankQualifiedStructuresMustExist(t *testing.T) {
	plan := minimalPlan()
	// Reference a structure that does not exist in plan.Structures.
	plan.Ranks[0].QualifiedStructures = []string{"NonExistent"}

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "undefined_reference", errs[0].Code)
	assert.Equal(t, "error", errs[0].Severity)
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
	assert.Equal(t, "error", errs[0].Severity)
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
	assert.Equal(t, "error", errs[0].Severity)
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
	assert.Equal(t, "error", errs[0].Severity)
}

func TestPayoutLagWarning(t *testing.T) {
	plan := minimalPlan()
	plan.Period.PayoutLagDays = 45

	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "warning", errs[0].Severity)
	assert.Equal(t, "long_payout_lag", errs[0].Code)
}
