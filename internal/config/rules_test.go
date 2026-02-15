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
	require.NotEmpty(t, errs)

	found := false
	for _, e := range errs {
		if e.Code == "ordering_violation" {
			found = true
			assert.Equal(t, "error", e.Severity)
		}
	}
	assert.True(t, found, "expected ordering_violation error for duplicate ordinal")
}

func TestRankQualifiedStructuresMustExist(t *testing.T) {
	plan := minimalPlan()
	// Reference a structure that does not exist in plan.Structures.
	plan.Ranks[0].QualifiedStructures = []string{"NonExistent"}

	errs := validateBusinessRules(plan)
	require.NotEmpty(t, errs)

	found := false
	for _, e := range errs {
		if e.Code == "undefined_reference" {
			found = true
			assert.Equal(t, "error", e.Severity)
		}
	}
	assert.True(t, found, "expected undefined_reference error for nonexistent qualified_structures entry")
}

func TestRankQualificationStructureMustExist(t *testing.T) {
	plan := minimalPlan()
	// Reference a structure in qualification that does not exist.
	plan.Ranks[0].Qualification.Structures = []StructureQualification{
		{Structure: "DoesNotExist"},
	}

	errs := validateBusinessRules(plan)
	require.NotEmpty(t, errs)

	found := false
	for _, e := range errs {
		if e.Code == "undefined_reference" {
			found = true
			assert.Equal(t, "error", e.Severity)
		}
	}
	assert.True(t, found, "expected undefined_reference error for nonexistent qualification structure")
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
	require.NotEmpty(t, errs)

	found := false
	for _, e := range errs {
		if e.Code == "cross_field_dependency" {
			found = true
			assert.Equal(t, "error", e.Severity)
		}
	}
	assert.True(t, found, "expected cross_field_dependency error when max_group_volume_per_leg > group_volume")
}

func TestPayOnceOnlyRequiresTrackAchievedRank(t *testing.T) {
	plan := minimalPlan()
	plan.RankTracking.TrackAchievedRank = false
	plan.Bonuses.RankAdvancement = &RankAdvancementBonusConfig{
		Amounts:     map[string]float64{"Silver": 500},
		PayOnceOnly: true,
	}

	errs := validateBusinessRules(plan)
	require.NotEmpty(t, errs)

	found := false
	for _, e := range errs {
		if e.Code == "cross_section_dependency" {
			found = true
			assert.Equal(t, "error", e.Severity)
		}
	}
	assert.True(t, found, "expected cross_section_dependency error when pay_once_only is true without track_achieved_rank")
}

func TestPayoutLagWarning(t *testing.T) {
	plan := minimalPlan()
	plan.Period.PayoutLagDays = 45

	errs := validateBusinessRules(plan)
	require.NotEmpty(t, errs)

	found := false
	for _, e := range errs {
		if e.Severity == "warning" {
			found = true
		}
	}
	assert.True(t, found, "expected a warning when payout_lag_days > 30")
}
