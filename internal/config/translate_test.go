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
