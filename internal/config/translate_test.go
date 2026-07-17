package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranslateStructureTagging verifies that a unilevel structure is
// wrapped in adjacent-tagged format: {"type": "unilevel", "config": {...}}.
func TestTranslateStructureTagging(t *testing.T) {
	plan := minimalPlanWithCommission()

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures, ok := doc["structures"].([]any)
	require.True(t, ok, "structures should be an array")
	require.Len(t, structures, 1)

	s := structures[0].(map[string]any)
	assert.Equal(t, "unilevel", s["type"])

	cfg, ok := s["config"].(map[string]any)
	require.True(t, ok, "structure should have a config object")
	assert.Equal(t, "Primary", cfg["name"])

	lc, ok := cfg["level_commission"].(map[string]any)
	require.True(t, ok, "unilevel config should have level_commission")
	assert.Equal(t, 0.40, lc["broad_commission_percent"])
	assert.Equal(t, float64(5), lc["commissionable_depth"])
}

// TestTranslateDonatedPlacementEnabled verifies that when
// donated_placement_enabled is true, donated_placement becomes the
// restriction string value.
func TestTranslateDonatedPlacementEnabled(t *testing.T) {
	plan := minimalPlanWithCommission()
	plan.Placement.DonatedPlacementEnabled = true
	restriction := "own_downline"
	plan.Placement.DonatedPlacementRestriction = &restriction

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	placement := doc["placement"].(map[string]any)
	assert.Equal(t, "own_downline", placement["donated_placement"])
}

// TestTranslateDonatedPlacementDisabled verifies that when
// donated_placement_enabled is false, donated_placement becomes null.
func TestTranslateDonatedPlacementDisabled(t *testing.T) {
	plan := minimalPlanWithCommission()
	plan.Placement.DonatedPlacementEnabled = false

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	placement := doc["placement"].(map[string]any)
	assert.Nil(t, placement["donated_placement"])
}

// TestTranslateBinaryPlacementKeyRename verifies that the YAML "binary"
// key in placement becomes "binary_placement" in the output JSON.
func TestTranslateBinaryPlacementKeyRename(t *testing.T) {
	plan := minimalPlanWithCommission()
	plan.Placement.Binary = &BinaryPlacementConfig{
		DefaultPlacement:  "balanced",
		PerUserPreference: true,
		SpilloverEnabled:  true,
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	placement := doc["placement"].(map[string]any)

	// "binary" key must not exist.
	_, hasBinary := placement["binary"]
	assert.False(t, hasBinary, "placement should not have a 'binary' key")

	// "binary_placement" key must exist.
	bp, hasBinaryPlacement := placement["binary_placement"]
	require.True(t, hasBinaryPlacement, "placement should have a 'binary_placement' key")

	bpMap := bp.(map[string]any)
	assert.Equal(t, "balanced", bpMap["default_placement"])
	assert.Equal(t, true, bpMap["per_user_preference"])
	assert.Equal(t, true, bpMap["spillover_enabled"])
}

// TestTranslateStreamlineLevelsMapToVec verifies that the dynamic_compression
// map (keyed by level number string) is converted to a sorted vector with a
// level field on each entry.
func TestTranslateStreamlineLevelsMapToVec(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Type = "streamline"
	plan.Structures[0].resolvedCommission = &StreamlineCommission{
		CommissionableDepth: 5,
		DynamicCompression: map[string]StreamlineLevel{
			"2": {MinRank: "gold", Percent: 0.08},
			"1": {MinRank: "silver", Percent: 0.05},
			"3": {MinRank: "platinum", Percent: 0.10},
		},
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures := doc["structures"].([]any)
	s := structures[0].(map[string]any)
	cfg := s["config"].(map[string]any)
	sc := cfg["streamline_commission"].(map[string]any)

	levels, ok := sc["dynamic_compression"].([]any)
	require.True(t, ok, "dynamic_compression should be an array")
	require.Len(t, levels, 3)

	// Sorted by level ascending.
	level1 := levels[0].(map[string]any)
	assert.Equal(t, float64(1), level1["level"])
	assert.Equal(t, "silver", level1["min_rank"])
	assert.Equal(t, 0.05, level1["percent"])

	level2 := levels[1].(map[string]any)
	assert.Equal(t, float64(2), level2["level"])
	assert.Equal(t, "gold", level2["min_rank"])
	assert.Equal(t, 0.08, level2["percent"])

	level3 := levels[2].(map[string]any)
	assert.Equal(t, float64(3), level3["level"])
	assert.Equal(t, "platinum", level3["min_rank"])
	assert.Equal(t, 0.10, level3["percent"])
}

// TestTranslateBinaryModeTagging verifies that binary commission mode
// is converted from flat YAML format (mode: "pairing", pairing: {...})
// to externally tagged format (mode: {"pairing": {...}}).
func TestTranslateBinaryModeTagging(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Type = "binary"
	plan.Structures[0].resolvedCommission = &BinaryCommission{
		Mode: "pairing",
		Pairing: &PairingConfig{
			Percent:           0.10,
			Calculation:       "weaker_leg",
			VolumeAfterPayout: "carry_forward",
		},
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures := doc["structures"].([]any)
	s := structures[0].(map[string]any)
	assert.Equal(t, "binary", s["type"])

	cfg := s["config"].(map[string]any)
	bc := cfg["binary_commission"].(map[string]any)

	// mode should be an object, not a string.
	mode, ok := bc["mode"].(map[string]any)
	require.True(t, ok, "mode should be an object (externally tagged)")

	pairing, ok := mode["pairing"].(map[string]any)
	require.True(t, ok, "mode should contain pairing key")
	assert.Equal(t, 0.10, pairing["percent"])
	assert.Equal(t, "weaker_leg", pairing["calculation"])
	assert.Equal(t, "carry_forward", pairing["volume_after_payout"])
}

// TestTranslateDemotionPolicyJSON verifies that DemotionPolicy serializes
// correctly: string variant becomes a bare string, grace variant becomes
// an object with a grace key.
func TestTranslateDemotionPolicyJSON(t *testing.T) {
	plan := minimalPlanWithCommission()

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	ranks := doc["ranks"].([]any)
	require.Len(t, ranks, 2)

	// Both ranks use "promotion_only" string variant.
	r0 := ranks[0].(map[string]any)
	assert.Equal(t, "promotion_only", r0["demotion_policy"])

	r1 := ranks[1].(map[string]any)
	assert.Equal(t, "promotion_only", r1["demotion_policy"])
}

// TestTranslateDemotionPolicyGraceJSON verifies the grace period variant
// serializes as {"grace": {"count": N, "unit": "..."}}.
func TestTranslateDemotionPolicyGraceJSON(t *testing.T) {
	plan := minimalPlanWithCommission()
	plan.Ranks[1].DemotionPolicy = DemotionPolicy{
		Grace: &GracePeriod{Count: 2, Unit: "months"},
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	ranks := doc["ranks"].([]any)
	r1 := ranks[1].(map[string]any)
	dp := r1["demotion_policy"].(map[string]any)
	grace := dp["grace"].(map[string]any)
	assert.Equal(t, float64(2), grace["count"])
	assert.Equal(t, "months", grace["unit"])
}

// TestSortStreamlineLevelsEmptyMap verifies that sortStreamlineLevels
// returns an error when the dynamic_compression map is empty.
func TestSortStreamlineLevelsEmptyMap(t *testing.T) {
	levels := map[string]StreamlineLevel{}
	_, err := sortStreamlineLevels(levels)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must have at least one level")
}

// TestSortStreamlineLevelsInvalidKey verifies that sortStreamlineLevels
// returns an error when a map key is not a valid integer.
func TestSortStreamlineLevelsInvalidKey(t *testing.T) {
	levels := map[string]StreamlineLevel{
		"1":   {MinRank: "silver", Percent: 0.05},
		"abc": {MinRank: "gold", Percent: 0.08},
	}
	_, err := sortStreamlineLevels(levels)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"abc" is not a valid level number`)
}

// TestTranslateBinaryCommissionUnknownMode verifies that
// translateBinaryCommission returns an error for an unknown mode.
func TestTranslateBinaryCommissionUnknownMode(t *testing.T) {
	c := &BinaryCommission{Mode: "unknown_mode"}
	_, err := translateBinaryCommission(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown binary commission mode: unknown_mode")
}

// TestTranslateBinaryCommissionEmptyMode verifies that
// translateBinaryCommission returns an error for an empty mode string.
func TestTranslateBinaryCommissionEmptyMode(t *testing.T) {
	c := &BinaryCommission{Mode: ""}
	_, err := translateBinaryCommission(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown binary commission mode:")
}

// TestTranslateCycleStepNetOffRejected verifies that
// translateBinaryCommission returns an error when cycle_step uses net_off.
func TestTranslateCycleStepNetOffRejected(t *testing.T) {
	c := &BinaryCommission{
		Mode: "cycle_step",
		CycleStep: &CycleStepConfig{
			VolumeAfterCycle: "net_off",
			Steps:            []CycleStep{{Threshold: 300, Amount: 25}},
		},
	}
	_, err := translateBinaryCommission(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "net_off is not supported for cycle_step")
}

// TestTranslateBoardPlanConfig verifies that a board plan structure is
// translated with name, width, height, and board_cycling at the top level.
func TestTranslateBoardPlanConfig(t *testing.T) {
	plan := minimalPlan()
	// Add a board_plan structure alongside the existing unilevel.
	plan.Structures = append(plan.Structures, StructureConfig{
		Name: "Sales Board",
		Type: "board_plan",
		Structure: &MatrixStructureParams{
			Width:  2,
			Height: 2,
		},
		resolvedCommission: &BoardPlanCommission{
			BoardCycling: BoardCyclingConfig{
				CycleCommission:       500.0,
				ReEntryEnabled:        true,
				ReEntryPosition:       "bottom",
				MaxCyclesPerPeriod:    5,
				MaxCascadeDepth:       10,
				StallThresholdPeriods: 3,
				InactiveCompression:   true,
			},
		},
	})
	// The first structure (unilevel) needs a resolved commission for translation.
	plan.Structures[0].resolvedCommission = &UnilevelCommission{
		BroadCommissionPercent: 0.40,
		CommissionableDepth:    5,
		RateTable: map[string]map[string]float64{
			"Associate": {"1": 0.05},
		},
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures := doc["structures"].([]any)
	require.Len(t, structures, 2)

	// Second structure should be board_plan.
	s := structures[1].(map[string]any)
	assert.Equal(t, "board_plan", s["type"])

	cfg := s["config"].(map[string]any)
	assert.Equal(t, "Sales Board", cfg["name"])
	assert.Equal(t, float64(2), cfg["width"])
	assert.Equal(t, float64(2), cfg["height"])

	bc := cfg["board_cycling"].(map[string]any)
	assert.Equal(t, 500.0, bc["cycle_commission"])
	assert.Equal(t, true, bc["re_entry_enabled"])
	assert.Equal(t, "bottom", bc["re_entry_position"])
	assert.Equal(t, float64(5), bc["max_cycles_per_period"])
	assert.Equal(t, float64(10), bc["max_cascade_depth"])
	assert.Equal(t, float64(3), bc["stall_threshold_periods"])
	assert.Equal(t, true, bc["inactive_compression"])
}

// TestTranslateBoardPlanConfigTypeMismatch verifies that
// translateBoardPlanConfig returns an error when given the wrong commission type.
func TestTranslateBoardPlanConfigTypeMismatch(t *testing.T) {
	s := &StructureConfig{
		Name:               "Bad",
		Type:               "board_plan",
		resolvedCommission: &UnilevelCommission{},
	}
	_, err := translateStructureConfig(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected *BoardPlanCommission")
}

// TestTranslateStructureConfigUnknownType verifies that
// translateStructureConfig returns an error for an unknown structure type.
func TestTranslateStructureConfigUnknownType(t *testing.T) {
	s := &StructureConfig{Name: "Bad", Type: "pyramid"}
	_, err := translateStructureConfig(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown structure type: pyramid")
}

// TestTranslateStructureConfigTypeMismatch verifies that each structure type
// returns an error when given the wrong commission type.
func TestTranslateStructureConfigTypeMismatch(t *testing.T) {
	wrongCommission := &UnilevelCommission{}
	tests := []struct {
		structType  string
		expectedMsg string
	}{
		{"binary", "expected *BinaryCommission"},
		{"matrix", "expected *MatrixCommission"},
		{"stairstep", "expected *StairstepCommission"},
		{"generation", "expected *GenerationCommission"},
		{"streamline", "expected *StreamlineCommission"},
	}
	for _, tt := range tests {
		t.Run(tt.structType, func(t *testing.T) {
			s := &StructureConfig{
				Name:               "Bad",
				Type:               tt.structType,
				resolvedCommission: wrongCommission,
			}
			_, err := translateStructureConfig(s)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedMsg)
		})
	}
}

func TestTranslateStreamlineConfigWithStreams(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Type = "streamline"
	plan.Structures[0].resolvedCommission = &StreamlineCommission{
		CommissionableDepth:      7,
		VolumeToDollarMultiplier: floatPtr(1.5),
		DynamicCompression: map[string]StreamlineLevel{
			"1": {MinRank: "active", Percent: 0.05},
			"2": {MinRank: "bronze", Percent: 0.04},
		},
		Streams: &StreamConfig{
			AdditionalPerRank:   map[string]uint8{"silver": 1, "gold": 2},
			AssignmentMode:      "round_robin",
			PerEnrollmentChoice: true,
			FreezeOnDemotion:    true,
		},
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures := doc["structures"].([]any)
	s := structures[0].(map[string]any)
	cfg := s["config"].(map[string]any)

	assert.Equal(t, "Primary", cfg["name"])

	sc := cfg["streamline_commission"].(map[string]any)
	assert.Equal(t, float64(7), sc["commissionable_depth"])
	assert.Equal(t, 1.5, sc["volume_to_dollar_multiplier"])

	streams := sc["streams"].(map[string]any)
	assert.Equal(t, "round_robin", streams["assignment_mode"])
	assert.Equal(t, true, streams["per_enrollment_choice"])
	assert.Equal(t, true, streams["freeze_on_demotion"])

	apr := streams["additional_per_rank"].(map[string]any)
	assert.Equal(t, float64(1), apr["silver"])
	assert.Equal(t, float64(2), apr["gold"])
}

func TestTranslateStreamlineConfigNilStreams(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Type = "streamline"
	plan.Structures[0].resolvedCommission = &StreamlineCommission{
		CommissionableDepth: 5,
		DynamicCompression: map[string]StreamlineLevel{
			"1": {MinRank: "active", Percent: 0.10},
		},
		Streams: nil,
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures := doc["structures"].([]any)
	s := structures[0].(map[string]any)
	cfg := s["config"].(map[string]any)
	sc := cfg["streamline_commission"].(map[string]any)

	// streams should be null when stream_config is nil.
	assert.Nil(t, sc["streams"], "streams should be null when stream_config is nil")
}

// TestTranslateGenerationIneligibleCreatesBoundary verifies that the
// ineligible_creates_boundary field on GenerationCommissionConfig is
// included in the translated output.
func TestTranslateGenerationIneligibleCreatesBoundary(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Type = "generation"
	icb := false
	plan.Structures[0].resolvedCommission = &GenerationCommission{
		LevelCommissionsEnabled: false,
		Generation: GenerationCommissionConfig{
			MaxGenerations: 3,
			GenerationRates: map[string]float64{
				"1": 0.05,
			},
			BoundaryMode:                  "threshold_rank",
			BoundaryRank:                  "Executive",
			EmptyGenerationConsumesNumber: true,
			IneligibleCreatesBoundary:     &icb,
		},
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures := doc["structures"].([]any)
	s := structures[0].(map[string]any)
	cfg := s["config"].(map[string]any)
	gc := cfg["generation_commission"].(map[string]any)

	assert.Equal(t, false, gc["ineligible_creates_boundary"])
}

func TestTranslateGenerationIneligibleCreatesBoundaryNil(t *testing.T) {
	plan := minimalPlan()
	plan.Structures[0].Name = "Gen"
	plan.Structures[0].Type = "generation"
	plan.Structures[0].resolvedCommission = &GenerationCommission{
		LevelCommissionsEnabled: false,
		Generation: GenerationCommissionConfig{
			MaxGenerations: 3,
			GenerationRates: map[string]float64{
				"1": 0.05,
			},
			BoundaryMode:                  "threshold_rank",
			BoundaryRank:                  "Executive",
			EmptyGenerationConsumesNumber: true,
			IneligibleCreatesBoundary:     nil, // user omitted the field
		},
	}

	out, err := translateToEngine(plan)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))

	structures := doc["structures"].([]any)
	s := structures[0].(map[string]any)
	cfg := s["config"].(map[string]any)
	gc := cfg["generation_commission"].(map[string]any)

	// With omitempty, nil *bool is omitted from JSON entirely.
	// Rust's serde(default) kicks in, defaulting to true.
	_, exists := gc["ineligible_creates_boundary"]
	assert.False(t, exists, "nil *bool should be omitted from JSON so Rust serde default applies")
}

func floatPtr(f float64) *float64 { return &f }
