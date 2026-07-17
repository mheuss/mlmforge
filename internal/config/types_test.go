package config

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestUnmarshalMinimalUnilevelPlan(t *testing.T) {
	yamlData := []byte(`
name: Minimal Unilevel
version: 1
period:
  length: month
  start_date: "2026-03-01"
  payout_lag_days: 14
volume:
  inhibit_signup_volume: false
  base_currency: USD
  volume_to_dollar_multiplier: 1.0
  deduct_qualifying_volume: false
ranks:
  - name: Associate
    ordinal: 1
    qualification:
      structures:
        - structure: Primary
          personal_volume: 0
          group_volume: 0
      required_products: []
    qualified_structures:
      - Primary
    demotion_policy: promotion_only
rank_tracking:
  track_achieved_rank: false
rank_features:
  constraints_enabled: false
  overrides_enabled: false
commission_eligibility:
  min_personal_volume: 0
  require_order_in_period: false
  eligible_statuses:
    - active
  active_leg_tiers: []
structures:
  - name: Primary
    type: unilevel
    commission:
      broad_commission_percent: 0.40
      commissionable_depth: 5
      rate_table:
        Associate:
          1: 0.05
          2: 0.04
          3: 0.03
bonuses:
  matching: null
  sponsor: null
  fast_start: null
  rank_advancement: null
  leadership_development: null
  infinity: null
  lifestyle: null
  pool: null
  matrix_completion: null
  position: null
  board_cycling: null
payout:
  base_currency: USD
  minimum_amount: 50.0
  split_payouts_enabled: false
  methods:
    - type: bank_transfer
      fee: 2.50
caps:
  company_payout_cap_percent: 0.42
  cap_enforcement: pro_rata
  clawback_on_refund: false
placement:
  donated_placement_enabled: false
`)
	var plan CompensationPlan
	err := yaml.Unmarshal(yamlData, &plan)
	require.NoError(t, err)

	assert.Equal(t, "Minimal Unilevel", plan.Name)
	assert.Equal(t, 1, plan.Version)
	assert.Equal(t, "month", plan.Period.Length)
	assert.Equal(t, uint8(14), plan.Period.PayoutLagDays)
	assert.Equal(t, "USD", plan.Volume.BaseCurrency)
	assert.Len(t, plan.Ranks, 1)
	assert.Equal(t, "Associate", plan.Ranks[0].Name)
	assert.Equal(t, 1, plan.Ranks[0].Ordinal)
	assert.Len(t, plan.Structures, 1)
	assert.Equal(t, "Primary", plan.Structures[0].Name)
	assert.Equal(t, "unilevel", plan.Structures[0].Type)
	assert.Equal(t, "USD", plan.Payout.BaseCurrency)
	assert.Equal(t, 50.0, plan.Payout.MinimumAmount)
	assert.Len(t, plan.Payout.Methods, 1)
	assert.False(t, plan.Placement.DonatedPlacementEnabled)
}

func TestUnmarshalDemotionPolicyVariants(t *testing.T) {
	// promotion_only variant
	yamlPromo := []byte("name: Test\nordinal: 1\nqualification:\n  structures: []\n  required_products: []\ndemotion_policy: promotion_only")
	var rank1 RankDefinition
	err := yaml.Unmarshal(yamlPromo, &rank1)
	require.NoError(t, err)
	assert.Equal(t, "promotion_only", rank1.DemotionPolicy.StringValue)

	// grace period variant
	yamlGrace := []byte(`
name: Test
ordinal: 1
qualification:
  structures: []
  required_products: []
demotion_policy:
  grace:
    count: 2
    unit: months
`)
	var rank2 RankDefinition
	err = yaml.Unmarshal(yamlGrace, &rank2)
	require.NoError(t, err)
	require.NotNil(t, rank2.DemotionPolicy.Grace)
	assert.Equal(t, uint16(2), rank2.DemotionPolicy.Grace.Count)
	assert.Equal(t, "months", rank2.DemotionPolicy.Grace.Unit)
}

func TestUnmarshalStructureCommissionDeferred(t *testing.T) {
	yamlData := []byte(`
name: Primary
type: unilevel
commission:
  broad_commission_percent: 0.40
  commissionable_depth: 5
  rate_table:
    Associate:
      1: 0.05
`)
	var s StructureConfig
	err := yaml.Unmarshal(yamlData, &s)
	require.NoError(t, err)
	assert.Equal(t, "unilevel", s.Type)
	assert.NotNil(t, s.CommissionRaw)
}

// TestResolveCommissionsUnilevel verifies that resolveCommissions correctly
// decodes a unilevel commission block from raw YAML into a typed struct.
func TestResolveCommissionsUnilevel(t *testing.T) {
	yamlData := []byte(`
name: Test Plan
version: 1
period: {length: month, payout_lag_days: 14}
volume: {base_currency: USD, volume_to_dollar_multiplier: 1.0}
ranks: []
rank_tracking: {track_achieved_rank: false}
rank_features: {constraints_enabled: false, overrides_enabled: false}
commission_eligibility: {eligible_statuses: [active]}
structures:
  - name: Primary
    type: unilevel
    commission:
      broad_commission_percent: 0.40
      commissionable_depth: 5
      rate_table:
        Associate:
          "1": 0.05
          "2": 0.04
bonuses: {}
payout: {base_currency: USD, minimum_amount: 50, methods: [{type: bank_transfer, fee: 2.50}]}
caps: {company_payout_cap_percent: 0.42, cap_enforcement: pro_rata}
placement: {donated_placement_enabled: false}
`)
	var plan CompensationPlan
	require.NoError(t, yaml.Unmarshal(yamlData, &plan))
	require.NoError(t, resolveCommissions(&plan))

	c, ok := plan.Structures[0].resolvedCommission.(*UnilevelCommission)
	require.True(t, ok, "expected *UnilevelCommission, got %T", plan.Structures[0].resolvedCommission)
	assert.Equal(t, 0.40, c.BroadCommissionPercent)
	assert.Equal(t, uint8(5), c.CommissionableDepth)
	assert.Equal(t, 0.05, c.RateTable["Associate"]["1"])
	assert.Equal(t, 0.04, c.RateTable["Associate"]["2"])
}

// TestCommissionableDepth_RejectsOverMax verifies the uint8 commissionable_depth
// field rejects out-of-range values at YAML unmarshal — the schema-bypass
// boundary HEU-513 closes. Covers max+1, a large overflow, and a negative, so a
// future clamping unmarshaler cannot silently weaken the type.
func TestCommissionableDepth_RejectsOverMax(t *testing.T) {
	for _, over := range []string{"256", "300", "-1"} {
		var c UnilevelCommission
		err := yaml.Unmarshal([]byte("commissionable_depth: "+over), &c)
		assert.Error(t, err, "commissionable_depth: %s must be rejected by uint8", over)
	}
}

// TestCommissionableDepth_AcceptsMax verifies the inclusive uint8 max (255) is
// accepted and lands in the field.
func TestCommissionableDepth_AcceptsMax(t *testing.T) {
	var c UnilevelCommission
	require.NoError(t, yaml.Unmarshal([]byte("commissionable_depth: 255"), &c))
	assert.Equal(t, uint8(255), c.CommissionableDepth)
}

// TestMatrixWidthHeight_RejectsOverMax verifies the uint8 matrix width and
// height fields reject out-of-range values at YAML unmarshal — the
// schema-bypass boundary HEU-513 closes. Covers max+1, a large overflow, and a
// negative, so a future clamping unmarshaler cannot silently weaken the type.
func TestMatrixWidthHeight_RejectsOverMax(t *testing.T) {
	for _, field := range []string{"width", "height"} {
		for _, over := range []string{"256", "300", "-1"} {
			var p MatrixStructureParams
			err := yaml.Unmarshal([]byte(field+": "+over), &p)
			assert.Error(t, err, "%s: %s must be rejected by uint8", field, over)
		}
	}
}

// TestMatrixWidthHeight_AcceptsMax verifies the inclusive uint8 max (255) is
// accepted and lands in both fields.
func TestMatrixWidthHeight_AcceptsMax(t *testing.T) {
	var p MatrixStructureParams
	require.NoError(t, yaml.Unmarshal([]byte("width: 255\nheight: 255"), &p))
	assert.Equal(t, uint8(255), p.Width)
	assert.Equal(t, uint8(255), p.Height)
}

// TestActiveLegTierFields_RejectsOverMax verifies the uint16 active-leg tier
// fields reject out-of-range values at YAML unmarshal — the schema-bypass
// boundary HEU-513 closes. Covers max+1, a large overflow, and a negative, so a
// future clamping unmarshaler cannot silently weaken the type.
func TestActiveLegTierFields_RejectsOverMax(t *testing.T) {
	for _, field := range []string{"min_active_legs", "max_commission_depth"} {
		for _, over := range []string{"65536", "70000", "-1"} {
			var tier ActiveLegTier
			err := yaml.Unmarshal([]byte(field+": "+over), &tier)
			assert.Error(t, err, "%s: %s must be rejected by uint16", field, over)
		}
	}
}

// TestActiveLegTierFields_AcceptsMax verifies the inclusive uint16 max (65535)
// is accepted and lands in both fields.
func TestActiveLegTierFields_AcceptsMax(t *testing.T) {
	var tier ActiveLegTier
	require.NoError(t, yaml.Unmarshal([]byte("min_active_legs: 65535\nmax_commission_depth: 65535"), &tier))
	assert.Equal(t, uint16(65535), tier.MinActiveLegs)
	assert.Equal(t, uint16(65535), tier.MaxCommissionDepth)
}

// TestBonusFields_RejectsOverMax verifies the uintN bonus config fields reject
// out-of-range values at YAML unmarshal — the schema-bypass boundary HEU-513
// closes. Covers max+1, a large overflow, and a negative per field, so a
// future clamping unmarshaler cannot silently weaken the types.
func TestBonusFields_RejectsOverMax(t *testing.T) {
	cases := []struct {
		field string
		over  []string
		dst   func() any
	}{
		{"depth", []string{"256", "300", "-1"}, func() any { return &MatchingBonusConfig{} }},
		{"depth", []string{"256", "300", "-1"}, func() any { return &LeadershipDevelopmentBonusConfig{} }},
		{"window_days", []string{"65536", "70000", "-1"}, func() any { return &FastStartBonusConfig{} }},
		{"grace_periods", []string{"256", "300", "-1"}, func() any { return &LifestyleTier{} }},
	}
	for _, tc := range cases {
		for _, over := range tc.over {
			dst := tc.dst()
			err := yaml.Unmarshal([]byte(tc.field+": "+over), dst)
			assert.Error(t, err, "%T %s: %s must be rejected", dst, tc.field, over)
		}
	}
}

// TestBonusFields_AcceptsMax verifies each bonus field's inclusive uintN max
// is accepted and lands in the field.
func TestBonusFields_AcceptsMax(t *testing.T) {
	var m MatchingBonusConfig
	require.NoError(t, yaml.Unmarshal([]byte("depth: 255"), &m))
	assert.Equal(t, uint8(255), m.Depth)

	var l LeadershipDevelopmentBonusConfig
	require.NoError(t, yaml.Unmarshal([]byte("depth: 255"), &l))
	assert.Equal(t, uint8(255), l.Depth)

	var f FastStartBonusConfig
	require.NoError(t, yaml.Unmarshal([]byte("window_days: 65535"), &f))
	assert.Equal(t, uint16(65535), f.WindowDays)

	var lt LifestyleTier
	require.NoError(t, yaml.Unmarshal([]byte("grace_periods: 255"), &lt))
	assert.Equal(t, uint8(255), lt.GracePeriods)
}

// TestPassUpCount_RejectsOverMax verifies the uint8 pass_up count field
// rejects out-of-range values at YAML unmarshal — the schema-bypass boundary
// HEU-513 closes. Covers max+1, a large overflow, and a negative, so a future
// clamping unmarshaler cannot silently weaken the type.
func TestPassUpCount_RejectsOverMax(t *testing.T) {
	for _, over := range []string{"256", "300", "-1"} {
		var p PassUpConfig
		err := yaml.Unmarshal([]byte("count: "+over), &p)
		assert.Error(t, err, "count: %s must be rejected by uint8", over)
	}
}

// TestPassUpCount_AcceptsMax verifies the inclusive uint8 max (255) is
// accepted and lands in the field.
func TestPassUpCount_AcceptsMax(t *testing.T) {
	var p PassUpConfig
	require.NoError(t, yaml.Unmarshal([]byte("count: 255"), &p))
	assert.Equal(t, uint8(255), p.Count)
}

// TestPayoutLagDays_RejectsOverMax verifies the uint8 payout_lag_days field
// rejects out-of-range values at YAML unmarshal — the schema-bypass boundary
// HEU-513 closes. Covers max+1, a large overflow, and a negative, so a future
// clamping unmarshaler cannot silently weaken the type.
func TestPayoutLagDays_RejectsOverMax(t *testing.T) {
	for _, over := range []string{"256", "300", "-1"} {
		var p PeriodConfig
		err := yaml.Unmarshal([]byte("payout_lag_days: "+over), &p)
		assert.Error(t, err, "payout_lag_days: %s must be rejected by uint8", over)
	}
}

// TestPayoutLagDays_AcceptsMax verifies the inclusive uint8 max (255) is
// accepted and lands in the field.
func TestPayoutLagDays_AcceptsMax(t *testing.T) {
	var p PeriodConfig
	require.NoError(t, yaml.Unmarshal([]byte("payout_lag_days: 255"), &p))
	assert.Equal(t, uint8(255), p.PayoutLagDays)
}

// TestBoardCyclingFields_RejectsOverMax verifies the three uint32 board_cycling
// fields reject out-of-range values at YAML unmarshal — the schema-bypass
// boundary HEU-513 closes. Covers max+1, a large overflow, and a negative, so a
// future clamping unmarshaler cannot silently weaken the type.
func TestBoardCyclingFields_RejectsOverMax(t *testing.T) {
	for _, field := range []string{"max_cycles_per_period", "max_cascade_depth", "stall_threshold_periods"} {
		for _, over := range []string{"4294967296", "9999999999", "-1"} {
			var c BoardCyclingConfig
			err := yaml.Unmarshal([]byte(field+": "+over), &c)
			assert.Error(t, err, "%s: %s must be rejected by uint32", field, over)
		}
	}
}

// TestBoardCyclingFields_AcceptsMax verifies the inclusive uint32 max
// (4294967295) is accepted and lands in each field.
func TestBoardCyclingFields_AcceptsMax(t *testing.T) {
	var c BoardCyclingConfig
	require.NoError(t, yaml.Unmarshal([]byte(
		"max_cycles_per_period: 4294967295\n"+
			"max_cascade_depth: 4294967295\n"+
			"stall_threshold_periods: 4294967295"), &c))
	assert.Equal(t, uint32(4294967295), c.MaxCyclesPerPeriod)
	assert.Equal(t, uint32(4294967295), c.MaxCascadeDepth)
	assert.Equal(t, uint32(4294967295), c.StallThresholdPeriods)
}

// TestBoardCyclingConfig_MaxCascadeDepthOmitempty verifies a zero MaxCascadeDepth
// is omitted from JSON so the Rust engine applies its serde default (10), and
// that a non-zero value is present. This must still hold after the uint32
// tightening — uint32's zero value is still 0. Assertions are key-level (decode
// to a map), not substring, so a future sibling key can't mask a regression —
// see docs/development/config-types.md ("wire shape" assertions).
func TestBoardCyclingConfig_MaxCascadeDepthOmitempty(t *testing.T) {
	zero, err := json.Marshal(BoardCyclingConfig{MaxCascadeDepth: 0})
	require.NoError(t, err)
	var zeroMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(zero, &zeroMap))
	assert.NotContains(t, zeroMap, "max_cascade_depth")

	nonzero, err := json.Marshal(BoardCyclingConfig{MaxCascadeDepth: 7})
	require.NoError(t, err)
	var nonzeroMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(nonzero, &nonzeroMap))
	assert.Contains(t, nonzeroMap, "max_cascade_depth")
}

// TestAdditionalPerRank_RejectsOverMax verifies the uint8 map VALUE on
// additional_per_rank rejects out-of-range values at YAML unmarshal — mirrors
// Rust BTreeMap<String, u8> (streamline.rs:85). Covers max+1, a large overflow,
// and a negative.
func TestAdditionalPerRank_RejectsOverMax(t *testing.T) {
	for _, over := range []string{"256", "300", "-1"} {
		var s StreamConfig
		err := yaml.Unmarshal([]byte("additional_per_rank:\n  gold: "+over), &s)
		assert.Error(t, err, "additional_per_rank value %s must be rejected by uint8", over)
	}
}

// TestAdditionalPerRank_AcceptsMax verifies the inclusive uint8 max (255) is
// accepted as a map value.
func TestAdditionalPerRank_AcceptsMax(t *testing.T) {
	var s StreamConfig
	require.NoError(t, yaml.Unmarshal([]byte("additional_per_rank:\n  gold: 255"), &s))
	assert.Equal(t, uint8(255), s.AdditionalPerRank["gold"])
}

// TestAdditionalPerRank_EmptyStaysPresent verifies an empty additional_per_rank
// map serializes to a present "{}", NOT omitted. Rust StreamConfig.additional_streams
// has no serde(default) (streamline.rs:85), so the field is required on the wire;
// a future omitempty would drop an empty map to absent and break deserialization.
func TestAdditionalPerRank_EmptyStaysPresent(t *testing.T) {
	var s StreamConfig
	require.NoError(t, yaml.Unmarshal([]byte(
		"additional_per_rank: {}\nassignment_mode: round_robin\n"+
			"per_enrollment_choice: false\nfreeze_on_demotion: false"), &s))
	data, err := json.Marshal(s)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Contains(t, m, "additional_per_rank")

	// A nil (absent) map marshals to JSON null, which Rust's BTreeMap rejects.
	// The schema now requires additional_per_rank (Task 10), so an omitted field
	// is caught at the schema gate before it can reach this marshal path.
	nilData, err := json.Marshal(StreamConfig{})
	require.NoError(t, err)
	var nilMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(nilData, &nilMap))
	require.Contains(t, nilMap, "additional_per_rank")
	assert.Equal(t, "null", string(nilMap["additional_per_rank"]))
}

// TestGracePeriodCount_RejectsOverMax verifies the uint16 count on GracePeriod
// rejects out-of-range values at YAML unmarshal — mirrors Rust u16 (rank.rs:243).
func TestGracePeriodCount_RejectsOverMax(t *testing.T) {
	for _, over := range []string{"65536", "99999999", "-1"} {
		var g GracePeriod
		err := yaml.Unmarshal([]byte("count: "+over), &g)
		assert.Error(t, err, "grace count %s must be rejected by uint16", over)
	}
}

// TestGracePeriodCount_AcceptsMax verifies the inclusive uint16 max (65535).
func TestGracePeriodCount_AcceptsMax(t *testing.T) {
	var g GracePeriod
	require.NoError(t, yaml.Unmarshal([]byte("count: 65535"), &g))
	assert.Equal(t, uint16(65535), g.Count)
}

// TestHoldingTankExpirationDays_RejectsOverMax verifies the uint8 expiration_days
// on HoldingTankConfig rejects out-of-range values — mirrors Rust u8 (placement.rs:70).
func TestHoldingTankExpirationDays_RejectsOverMax(t *testing.T) {
	for _, over := range []string{"256", "300", "-1"} {
		var h HoldingTankConfig
		err := yaml.Unmarshal([]byte("expiration_days: "+over), &h)
		assert.Error(t, err, "expiration_days %s must be rejected by uint8", over)
	}
}

// TestHoldingTankExpirationDays_AcceptsMax verifies the inclusive uint8 max (255).
func TestHoldingTankExpirationDays_AcceptsMax(t *testing.T) {
	var h HoldingTankConfig
	require.NoError(t, yaml.Unmarshal([]byte("expiration_days: 255"), &h))
	assert.Equal(t, uint8(255), h.ExpirationDays)
}

// TestResolveCommissionsBinary verifies that resolveCommissions correctly
// decodes a binary commission block with pairing mode.
func TestResolveCommissionsBinary(t *testing.T) {
	yamlData := []byte(`
name: Test Plan
version: 1
period: {length: month, payout_lag_days: 14}
volume: {base_currency: USD, volume_to_dollar_multiplier: 1.0}
ranks: []
rank_tracking: {track_achieved_rank: false}
rank_features: {constraints_enabled: false, overrides_enabled: false}
commission_eligibility: {eligible_statuses: [active]}
structures:
  - name: BinaryTree
    type: binary
    commission:
      mode: pairing
      pairing:
        percent: 0.10
        calculation: weaker_leg
        cap_per_period: 5000.00
        volume_after_payout: carry_forward
        carry_forward_cap: 20000.00
bonuses: {}
payout: {base_currency: USD, minimum_amount: 50, methods: [{type: bank_transfer, fee: 2.50}]}
caps: {company_payout_cap_percent: 0.42, cap_enforcement: pro_rata}
placement: {donated_placement_enabled: false}
`)
	var plan CompensationPlan
	require.NoError(t, yaml.Unmarshal(yamlData, &plan))
	require.NoError(t, resolveCommissions(&plan))

	c, ok := plan.Structures[0].resolvedCommission.(*BinaryCommission)
	require.True(t, ok, "expected *BinaryCommission, got %T", plan.Structures[0].resolvedCommission)
	assert.Equal(t, "pairing", c.Mode)
	require.NotNil(t, c.Pairing)
	assert.Equal(t, 0.10, c.Pairing.Percent)
	assert.Equal(t, "weaker_leg", c.Pairing.Calculation)
	assert.Equal(t, 5000.0, *c.Pairing.CapPerPeriod)
	assert.Equal(t, "carry_forward", c.Pairing.VolumeAfterPayout)
	assert.Equal(t, 20000.0, *c.Pairing.CarryForwardCap)
}

// TestResolveCommissionsStreamline verifies that resolveCommissions correctly
// decodes a streamline commission block with dynamic compression levels.
func TestResolveCommissionsStreamline(t *testing.T) {
	yamlData := []byte(`
name: Test Plan
version: 1
period: {length: month, payout_lag_days: 14}
volume: {base_currency: USD, volume_to_dollar_multiplier: 1.0}
ranks: []
rank_tracking: {track_achieved_rank: false}
rank_features: {constraints_enabled: false, overrides_enabled: false}
commission_eligibility: {eligible_statuses: [active]}
structures:
  - name: StreamlineTree
    type: streamline
    commission:
      commissionable_depth: 10
      dynamic_compression:
        "1":
          min_rank: Affiliate
          percent: 0.10
        "2":
          min_rank: Team Lead
          percent: 0.07
bonuses: {}
payout: {base_currency: USD, minimum_amount: 50, methods: [{type: bank_transfer, fee: 2.50}]}
caps: {company_payout_cap_percent: 0.42, cap_enforcement: pro_rata}
placement: {donated_placement_enabled: false}
`)
	var plan CompensationPlan
	require.NoError(t, yaml.Unmarshal(yamlData, &plan))
	require.NoError(t, resolveCommissions(&plan))

	c, ok := plan.Structures[0].resolvedCommission.(*StreamlineCommission)
	require.True(t, ok, "expected *StreamlineCommission, got %T", plan.Structures[0].resolvedCommission)
	assert.Equal(t, uint8(10), c.CommissionableDepth)
	require.Len(t, c.DynamicCompression, 2)
	assert.Equal(t, "Affiliate", c.DynamicCompression["1"].MinRank)
	assert.Equal(t, 0.10, c.DynamicCompression["1"].Percent)
	assert.Equal(t, "Team Lead", c.DynamicCompression["2"].MinRank)
	assert.Equal(t, 0.07, c.DynamicCompression["2"].Percent)
}

// TestResolveCommissionsGeneration verifies that resolveCommissions correctly
// decodes a generation commission block with generation config.
func TestResolveCommissionsGeneration(t *testing.T) {
	yamlData := []byte(`
name: Test Plan
version: 1
period: {length: month, payout_lag_days: 14}
volume: {base_currency: USD, volume_to_dollar_multiplier: 1.0}
ranks: []
rank_tracking: {track_achieved_rank: false}
rank_features: {constraints_enabled: false, overrides_enabled: false}
commission_eligibility: {eligible_statuses: [active]}
structures:
  - name: GenTree
    type: generation
    commission:
      level_commissions_enabled: true
      commissionable_depth: 5
      rate_table:
        Associate:
          "1": 0.05
      generation:
        max_generations: 4
        generation_rates:
          "1": 0.05
          "2": 0.04
        boundary_mode: threshold_rank
        boundary_rank: Executive
        empty_generation_consumes_number: true
        ineligible_creates_boundary: true
bonuses: {}
payout: {base_currency: USD, minimum_amount: 50, methods: [{type: bank_transfer, fee: 2.50}]}
caps: {company_payout_cap_percent: 0.42, cap_enforcement: pro_rata}
placement: {donated_placement_enabled: false}
`)
	var plan CompensationPlan
	require.NoError(t, yaml.Unmarshal(yamlData, &plan))
	require.NoError(t, resolveCommissions(&plan))

	c, ok := plan.Structures[0].resolvedCommission.(*GenerationCommission)
	require.True(t, ok, "expected *GenerationCommission, got %T", plan.Structures[0].resolvedCommission)
	assert.True(t, c.LevelCommissionsEnabled)
	assert.Equal(t, uint8(5), c.CommissionableDepth)
	assert.Equal(t, uint8(4), c.Generation.MaxGenerations)
	assert.Equal(t, "threshold_rank", c.Generation.BoundaryMode)
	assert.Equal(t, "Executive", c.Generation.BoundaryRank)
	assert.True(t, c.Generation.EmptyGenerationConsumesNumber)
	require.NotNil(t, c.Generation.IneligibleCreatesBoundary)
	assert.True(t, *c.Generation.IneligibleCreatesBoundary)
	assert.Equal(t, 0.05, c.Generation.GenerationRates["1"])
	assert.Equal(t, 0.04, c.Generation.GenerationRates["2"])
}

// TestResolveCommissionsStairstep verifies that resolveCommissions correctly
// decodes a stairstep commission block with breakaway config.
func TestResolveCommissionsStairstep(t *testing.T) {
	yamlData := []byte(`
name: Test Plan
version: 1
period: {length: month, payout_lag_days: 14}
volume: {base_currency: USD, volume_to_dollar_multiplier: 1.0}
ranks: []
rank_tracking: {track_achieved_rank: false}
rank_features: {constraints_enabled: false, overrides_enabled: false}
commission_eligibility: {eligible_statuses: [active]}
structures:
  - name: StairstepTree
    type: stairstep
    commission:
      commissionable_depth: 6
      rate_table:
        Distributor:
          "1": 0.05
      breakaway:
        threshold_rank: Supervisor
        group_volume_excludes_breakaway: true
        overrides:
          type: single_walk
          override_calculation: differential
          differential:
            rank_rates:
              Supervisor: 0.05
            min_override: 10.00
bonuses: {}
payout: {base_currency: USD, minimum_amount: 50, methods: [{type: bank_transfer, fee: 2.50}]}
caps: {company_payout_cap_percent: 0.42, cap_enforcement: pro_rata}
placement: {donated_placement_enabled: false}
`)
	var plan CompensationPlan
	require.NoError(t, yaml.Unmarshal(yamlData, &plan))
	require.NoError(t, resolveCommissions(&plan))

	c, ok := plan.Structures[0].resolvedCommission.(*StairstepCommission)
	require.True(t, ok, "expected *StairstepCommission, got %T", plan.Structures[0].resolvedCommission)
	assert.Equal(t, uint8(6), c.CommissionableDepth)
	require.NotNil(t, c.Breakaway)
	assert.Equal(t, "Supervisor", c.Breakaway.ThresholdRank)
	assert.True(t, c.Breakaway.GroupVolumeExcludesBreakaway)
	assert.Equal(t, "single_walk", c.Breakaway.Overrides.Type)
	assert.Equal(t, "differential", c.Breakaway.Overrides.OverrideCalculation)
	require.NotNil(t, c.Breakaway.Overrides.Differential)
	assert.Equal(t, 0.05, c.Breakaway.Overrides.Differential.RankRates["Supervisor"])
	assert.Equal(t, 10.0, c.Breakaway.Overrides.Differential.MinOverride)
}

// TestBreakawayConfig_SingleWalk_MarshalsGoEmittedWireShape verifies the
// JSON wire shape Go sends to the Rust engine for a single_walk breakaway.
// Every variant field is present and the non-selected multi_tier tiers
// slice marshals as null (no omitempty). Rust's internally-tagged
// OverrideStrategy ignores the non-selected variant's fields when it
// deserializes. The cross-language contract is pinned by Rust's
// deserialize_go_emitted_overrides test.
func TestBreakawayConfig_SingleWalk_MarshalsGoEmittedWireShape(t *testing.T) {
	cfg := BreakawayConfig{
		ThresholdRank:                "director",
		GroupVolumeExcludesBreakaway: true,
		Overrides: OverrideStrategy{
			Type:                overrideStrategySingleWalk,
			OverrideCalculation: "differential",
			Differential: &DifferentialConfig{
				RankRates:   map[string]float64{"director": 0.10},
				MinOverride: 0.02,
			},
		},
	}

	jsonBytes, err := json.Marshal(cfg)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &got))

	assert.Equal(t, "director", got["threshold_rank"])
	assert.Equal(t, true, got["group_volume_excludes_breakaway"])

	ov, ok := got["overrides"].(map[string]any)
	require.True(t, ok, "overrides should be an object")
	assert.Equal(t, "single_walk", ov["type"])
	assert.Equal(t, "differential", ov["override_calculation"])

	diff, ok := ov["differential"].(map[string]any)
	require.True(t, ok, "differential should be an object")
	rates := diff["rank_rates"].(map[string]any)
	assert.Equal(t, 0.10, rates["director"])
	assert.Equal(t, 0.02, diff["min_override"])

	// Non-selected variant fields must be present and zero-valued. The Rust
	// internally-tagged enum reads `type` and ignores these. assert.Contains
	// checks key presence before assert.Nil checks the value, so an
	// omitempty regression on any variant field would drop the key entirely
	// and fail Contains.
	assert.Contains(t, ov, "fixed_override")
	assert.Nil(t, ov["fixed_override"])
	assert.Contains(t, ov, "generation")
	assert.Nil(t, ov["generation"])
	assert.Contains(t, ov, "tiers")
	assert.Nil(t, ov["tiers"])
}

// TestBreakawayConfig_MultiTier_MarshalsGoEmittedWireShape verifies the
// JSON wire shape Go sends to the Rust engine for a multi_tier breakaway.
// Every single_walk variant field is present and zero-valued (empty
// override_calculation, nil differential, nil fixed_override, nil
// generation). The empty-string override_calculation is what keeps the
// schema's oneOf unambiguous: it fails the single_walk branch's enum
// constraint, so only the multi_tier branch matches.
func TestBreakawayConfig_MultiTier_MarshalsGoEmittedWireShape(t *testing.T) {
	cfg := BreakawayConfig{
		ThresholdRank:                "director",
		GroupVolumeExcludesBreakaway: true,
		Overrides: OverrideStrategy{
			Type: overrideStrategyMultiTier,
			Tiers: []BreakawayTier{
				{MinSplitOutGroups: 1, Rate: 0.05},
				{MinSplitOutGroups: 2, Rate: 0.02},
			},
		},
	}

	jsonBytes, err := json.Marshal(cfg)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &got))

	ov, ok := got["overrides"].(map[string]any)
	require.True(t, ok, "overrides should be an object")
	assert.Equal(t, "multi_tier", ov["type"])

	// Non-selected single_walk fields are present and zero-valued.
	// assert.Contains pins key presence so an omitempty regression on any
	// variant field would drop the key entirely and fail Contains. The
	// override_calculation field is gated by the explicit "" equality check.
	assert.Equal(t, "", ov["override_calculation"])
	assert.Contains(t, ov, "differential")
	assert.Nil(t, ov["differential"])
	assert.Contains(t, ov, "fixed_override")
	assert.Nil(t, ov["fixed_override"])
	assert.Contains(t, ov, "generation")
	assert.Nil(t, ov["generation"])

	tiers, ok := ov["tiers"].([]any)
	require.True(t, ok, "tiers should be an array")
	require.Len(t, tiers, 2)
	t0 := tiers[0].(map[string]any)
	assert.Equal(t, float64(1), t0["min_split_out_groups"])
	assert.Equal(t, 0.05, t0["rate"])
	t1 := tiers[1].(map[string]any)
	assert.Equal(t, float64(2), t1["min_split_out_groups"])
	assert.Equal(t, 0.02, t1["rate"])
}

// TestBreakawayTier_MinSplitOutGroups_BoundaryValues verifies the uint8
// boundaries at YAML unmarshal time. Values in [0, 255] succeed at the Go
// layer; values outside fail. The contract: 0 passes the Go uint8 type
// (uint8 is unsigned and 0 is a valid value), but the schema's
// minimum: 1 constraint rejects it. The schema is the gate, not Go.
// See multi-tier-min-split-out-zero.yaml for the schema-level test.
func TestBreakawayTier_MinSplitOutGroups_BoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expectErr bool
	}{
		{"zero", "0", false},
		{"max_uint8", "255", false},
		{"one_above_max", "256", true},
		{"large_overflow", "300", true},
		{"negative", "-1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlInput := fmt.Sprintf(`
min_split_out_groups: %s
rate: 0.05
`, tc.value)
			var tier BreakawayTier
			err := yaml.Unmarshal([]byte(yamlInput), &tier)
			if tc.expectErr {
				require.Error(t, err, "expected unmarshal to reject min_split_out_groups=%s", tc.value)
			} else {
				require.NoError(t, err, "expected unmarshal to accept min_split_out_groups=%s", tc.value)
			}
		})
	}
}

// TestResolveCommissionsMatrix verifies that resolveCommissions correctly
// decodes a matrix commission block.
func TestResolveCommissionsMatrix(t *testing.T) {
	yamlData := []byte(`
name: Test Plan
version: 1
period: {length: month, payout_lag_days: 14}
volume: {base_currency: USD, volume_to_dollar_multiplier: 1.0}
ranks: []
rank_tracking: {track_achieved_rank: false}
rank_features: {constraints_enabled: false, overrides_enabled: false}
commission_eligibility: {eligible_statuses: [active]}
structures:
  - name: Matrix
    type: matrix
    structure:
      width: 3
      height: 7
      spillover_direction: breadth_first
    commission:
      commissionable_depth: 7
      rate_table:
        Starter:
          "1": 0.05
          "2": 0.03
bonuses: {}
payout: {base_currency: USD, minimum_amount: 50, methods: [{type: bank_transfer, fee: 2.50}]}
caps: {company_payout_cap_percent: 0.42, cap_enforcement: pro_rata}
placement: {donated_placement_enabled: false}
`)
	var plan CompensationPlan
	require.NoError(t, yaml.Unmarshal(yamlData, &plan))
	require.NoError(t, resolveCommissions(&plan))

	c, ok := plan.Structures[0].resolvedCommission.(*MatrixCommission)
	require.True(t, ok, "expected *MatrixCommission, got %T", plan.Structures[0].resolvedCommission)
	assert.Equal(t, uint8(7), c.CommissionableDepth)
	assert.Equal(t, 0.05, c.RateTable["Starter"]["1"])
	assert.Equal(t, 0.03, c.RateTable["Starter"]["2"])
}

// TestResolveCommissionsUnknownType verifies that resolveCommissions returns
// an error for an unknown structure type.
func TestResolveCommissionsUnknownType(t *testing.T) {
	plan := &CompensationPlan{
		Structures: []StructureConfig{
			{Name: "Bad", Type: "pyramid", CommissionRaw: map[string]any{"foo": "bar"}},
		},
	}
	err := resolveCommissions(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown structure type: pyramid")
}

// TestResolveCommissionsNilCommissionReturnsError verifies that structures
// without a commission block return an error.
func TestResolveCommissionsNilCommissionReturnsError(t *testing.T) {
	plan := &CompensationPlan{
		Structures: []StructureConfig{
			{Name: "Empty", Type: "unilevel", CommissionRaw: nil},
		},
	}
	err := resolveCommissions(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Empty")
	assert.Contains(t, err.Error(), "no commission block")
}

// TestResolveCommissionsBoardPlan verifies that resolveCommissions correctly
// decodes a board plan commission block with board cycling config.
func TestResolveCommissionsBoardPlan(t *testing.T) {
	yamlData := []byte(`
name: Test Plan
version: 1
period: {length: month, payout_lag_days: 14}
volume: {base_currency: USD, volume_to_dollar_multiplier: 1.0}
ranks: []
rank_tracking: {track_achieved_rank: false}
rank_features: {constraints_enabled: false, overrides_enabled: false}
commission_eligibility: {eligible_statuses: [active]}
structures:
  - name: Sales Board
    type: board_plan
    structure:
      width: 2
      height: 2
    commission:
      board_cycling:
        cycle_commission: 500.0
        re_entry_enabled: true
        re_entry_position: bottom
        max_cycles_per_period: 5
        max_cascade_depth: 10
        stall_threshold_periods: 3
        inactive_compression: true
bonuses: {}
payout: {base_currency: USD, minimum_amount: 50, methods: [{type: bank_transfer, fee: 2.50}]}
caps: {company_payout_cap_percent: 0.42, cap_enforcement: pro_rata}
placement: {donated_placement_enabled: false}
`)
	var plan CompensationPlan
	require.NoError(t, yaml.Unmarshal(yamlData, &plan))
	require.NoError(t, resolveCommissions(&plan))

	c, ok := plan.Structures[0].resolvedCommission.(*BoardPlanCommission)
	require.True(t, ok, "expected *BoardPlanCommission, got %T", plan.Structures[0].resolvedCommission)
	assert.Equal(t, 500.0, c.BoardCycling.CycleCommission)
	assert.True(t, c.BoardCycling.ReEntryEnabled)
	assert.Equal(t, "bottom", c.BoardCycling.ReEntryPosition)
	assert.Equal(t, uint32(5), c.BoardCycling.MaxCyclesPerPeriod)
	assert.Equal(t, uint32(10), c.BoardCycling.MaxCascadeDepth)
	assert.Equal(t, uint32(3), c.BoardCycling.StallThresholdPeriods)
	assert.True(t, c.BoardCycling.InactiveCompression)
}

// TestGenerationCommissionConfig_MaxGenerationsPerRank_RoundTrip verifies that
// the per-rank generation depth override deserializes from YAML and round-trips
// through JSON, matching the Rust BTreeMap<String, u8> mirror.
func TestGenerationCommissionConfig_MaxGenerationsPerRank_RoundTrip(t *testing.T) {
	yamlInput := `
max_generations: 4
max_generations_per_rank:
  silver: 3
  diamond: 8
generation_rates:
  "1": 0.10
boundary_mode: threshold_rank
boundary_rank: director
empty_generation_consumes_number: false
volume_to_dollar_multiplier: null
`
	var cfg GenerationCommissionConfig
	require.NoError(t, yaml.Unmarshal([]byte(yamlInput), &cfg))
	require.Equal(t, uint8(3), cfg.MaxGenerationsPerRank["silver"])
	require.Equal(t, uint8(8), cfg.MaxGenerationsPerRank["diamond"])

	// Round-trip JSON to validate Rust-bound serialization.
	jsonBytes, err := json.Marshal(cfg)
	require.NoError(t, err)
	var decoded GenerationCommissionConfig
	require.NoError(t, json.Unmarshal(jsonBytes, &decoded))
	require.Equal(t, uint8(3), decoded.MaxGenerationsPerRank["silver"])
}

// TestGenerationCommissionConfig_MaxGenerations_BoundaryValues verifies the
// uint8 boundaries at YAML unmarshal time. Values in [0, 255] succeed; values
// outside fail. Without this guard, a Go-authored config could set values that
// silently truncate or fail when round-tripped to the Rust engine.
func TestGenerationCommissionConfig_MaxGenerations_BoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expectErr bool
	}{
		{"zero", "0", false},
		{"max_uint8", "255", false},
		{"one_above_max", "256", true},
		{"large_overflow", "300", true},
		{"negative", "-1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlInput := fmt.Sprintf(`
max_generations: %s
generation_rates:
  "1": 0.10
boundary_mode: threshold_rank
boundary_rank: director
empty_generation_consumes_number: false
`, tc.value)
			var cfg GenerationCommissionConfig
			err := yaml.Unmarshal([]byte(yamlInput), &cfg)
			if tc.expectErr {
				require.Error(t, err, "expected unmarshal to reject max_generations=%s", tc.value)
			} else {
				require.NoError(t, err, "expected unmarshal to accept max_generations=%s", tc.value)
			}
		})
	}
}

// TestBreakawayGenerationConfig_MaxGenerations_BoundaryValues verifies the
// uint8 boundaries at YAML unmarshal time. Values in [0, 255] succeed; values
// outside fail. Without this guard, a Go-authored config could set values that
// silently truncate or fail when round-tripped to the Rust engine.
func TestBreakawayGenerationConfig_MaxGenerations_BoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expectErr bool
	}{
		{"zero", "0", false},
		{"max_uint8", "255", false},
		{"one_above_max", "256", true},
		{"large_overflow", "300", true},
		{"negative", "-1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlInput := fmt.Sprintf(`
max_generations: %s
generation_rates:
  "1": 0.10
boundary_rank: director
`, tc.value)
			var cfg BreakawayGenerationConfig
			err := yaml.Unmarshal([]byte(yamlInput), &cfg)
			if tc.expectErr {
				require.Error(t, err, "expected unmarshal to reject max_generations=%s", tc.value)
			} else {
				require.NoError(t, err, "expected unmarshal to accept max_generations=%s", tc.value)
			}
		})
	}
}

// TestGenerationCommissionConfig_MaxGenerationsPerRank_DefaultsToNil verifies
// that when the per-rank override is absent from YAML, the field is empty so
// downstream code falls back to the uniform max_generations value.
func TestGenerationCommissionConfig_MaxGenerationsPerRank_DefaultsToNil(t *testing.T) {
	yamlInput := `
max_generations: 4
generation_rates:
  "1": 0.10
boundary_mode: threshold_rank
boundary_rank: director
empty_generation_consumes_number: false
`
	var cfg GenerationCommissionConfig
	require.NoError(t, yaml.Unmarshal([]byte(yamlInput), &cfg))
	require.Empty(t, cfg.MaxGenerationsPerRank)
}

// TestLegQualityRoundTrip verifies that leg_quality requirements deserialize
// from YAML and round-trip through JSON, matching the Rust internally-tagged
// LegPredicate enum. The Go side is a flat struct; the non-selected variant
// field carries a zero value, which serde ignores on the Rust side.
func TestLegQualityRoundTrip(t *testing.T) {
	yamlInput := `
structure: Primary
personal_volume: 300
group_volume: 10000
leg_quality:
  - count: 12
    predicate:
      type: contains_rank
      min_rank: Gold
  - count: 3
    predicate:
      type: contains_personal_volume
      min_personal_volume: 200
`
	var sq StructureQualification
	require.NoError(t, yaml.Unmarshal([]byte(yamlInput), &sq))
	require.Len(t, sq.LegQuality, 2)
	assert.Equal(t, uint16(12), sq.LegQuality[0].Count)
	assert.Equal(t, "contains_rank", sq.LegQuality[0].Predicate.Type)
	assert.Equal(t, "Gold", sq.LegQuality[0].Predicate.MinRank)
	assert.Equal(t, "contains_personal_volume", sq.LegQuality[1].Predicate.Type)
	assert.Equal(t, float64(200), sq.LegQuality[1].Predicate.MinPersonalVolume)

	// Round-trip JSON to validate Rust-bound serialization.
	jsonBytes, err := json.Marshal(sq)
	require.NoError(t, err)

	// Wire-shape guard: the flat-struct mirror must emit the non-selected
	// variant field as a zero value (no omitempty). Rust serde ignores it,
	// but omitempty here would silently drop a legitimate min_personal_volume
	// of 0. These assertions make the no-omitempty tags on LegPredicate a
	// tested contract, not just a doc comment.
	assert.Contains(t, string(jsonBytes), `"min_personal_volume":0`)
	assert.Contains(t, string(jsonBytes), `"min_rank":""`)

	var decoded StructureQualification
	require.NoError(t, json.Unmarshal(jsonBytes, &decoded))
	require.Len(t, decoded.LegQuality, 2)
	assert.Equal(t, uint16(12), decoded.LegQuality[0].Count)
	assert.Equal(t, "Gold", decoded.LegQuality[0].Predicate.MinRank)
	assert.Equal(t, float64(200), decoded.LegQuality[1].Predicate.MinPersonalVolume)
}

// TestLegQualityDefaultsToEmpty verifies that an absent leg_quality
// unmarshals to a nil slice, matching the Rust serde(default).
func TestLegQualityDefaultsToEmpty(t *testing.T) {
	yamlInput := `
structure: Primary
personal_volume: 100
group_volume: 5000
`
	var sq StructureQualification
	require.NoError(t, yaml.Unmarshal([]byte(yamlInput), &sq))
	assert.Empty(t, sq.LegQuality)
}

// TestLegQualityRequirementCountBoundaryValues verifies the uint16 boundaries
// at YAML unmarshal time. Values in [0, 65535] succeed; values outside fail,
// so an out-of-range count produces a clear Go unmarshal error instead of
// silently truncating when round-tripped to the Rust u16. Mirrors
// TestGenerationCommissionConfig_MaxGenerations_BoundaryValues.
func TestLegQualityRequirementCountBoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expectErr bool
	}{
		{"zero", "0", false},
		{"max_uint16", "65535", false},
		{"one_above_max", "65536", true},
		{"large_overflow", "70000", true},
		{"negative", "-1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlInput := fmt.Sprintf(`
count: %s
predicate:
  type: contains_rank
  min_rank: Gold
`, tc.value)
			var req LegQualityRequirement
			err := yaml.Unmarshal([]byte(yamlInput), &req)
			if tc.expectErr {
				require.Error(t, err, "expected unmarshal to reject count=%s", tc.value)
			} else {
				require.NoError(t, err, "expected unmarshal to accept count=%s", tc.value)
			}
		})
	}
}

// TestDistributorCountRequirementCountBoundaryValues verifies the uint16
// boundaries on count at YAML unmarshal time. Values in [0, 65535] succeed;
// values outside fail, so an out-of-range count produces a clear Go unmarshal
// error instead of silently truncating when round-tripped to the Rust u16
// (rank.rs count: u16). Mirrors TestLegQualityRequirementCountBoundaryValues.
func TestDistributorCountRequirementCountBoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expectErr bool
	}{
		{"zero", "0", false},
		{"max_uint16", "65535", false},
		{"one_above_max", "65536", true},
		{"large_overflow", "70000", true},
		{"negative", "-1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlInput := fmt.Sprintf("count: %s\n", tc.value)
			var req DistributorCountRequirement
			err := yaml.Unmarshal([]byte(yamlInput), &req)
			if tc.expectErr {
				require.Error(t, err, "expected unmarshal to reject count=%s", tc.value)
			} else {
				require.NoError(t, err, "expected unmarshal to accept count=%s", tc.value)
			}
		})
	}
}

// TestDistributorCountRequirementTotalCountBoundaryValues verifies the uint16
// boundaries on total_count at YAML unmarshal time. Values in [0, 65535]
// succeed; values outside fail, mirroring the Rust total_count: u16
// (rank.rs). Mirrors TestDistributorCountRequirementCountBoundaryValues.
func TestDistributorCountRequirementTotalCountBoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expectErr bool
	}{
		{"zero", "0", false},
		{"max_uint16", "65535", false},
		{"one_above_max", "65536", true},
		{"large_overflow", "70000", true},
		{"negative", "-1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlInput := fmt.Sprintf("total_count: %s\n", tc.value)
			var req DistributorCountRequirement
			err := yaml.Unmarshal([]byte(yamlInput), &req)
			if tc.expectErr {
				require.Error(t, err, "expected unmarshal to reject total_count=%s", tc.value)
			} else {
				require.NoError(t, err, "expected unmarshal to accept total_count=%s", tc.value)
			}
		})
	}
}

// TestDistributorCountRequirementSearchDepthBoundaryValues verifies the uint8
// boundaries on search_depth (*uint8) at YAML unmarshal time. Values in
// [0, 255] succeed at the Go layer; values outside fail. The contract: 0
// passes the Go uint8 type (uint8 is unsigned and 0 is a valid value), but
// the schema's minimum: 1 constraint rejects it. The schema is the gate at
// that bound, not Go. Mirrors search_depth to the Rust Option<u8>
// (rank.rs) and follows TestBreakawayTier_MinSplitOutGroups_BoundaryValues.
func TestDistributorCountRequirementSearchDepthBoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expectErr bool
	}{
		{"zero", "0", false},
		{"min_schema", "1", false},
		{"max_uint8", "255", false},
		{"one_above_max", "256", true},
		{"large_overflow", "300", true},
		{"negative", "-1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlInput := fmt.Sprintf("search_depth: %s\n", tc.value)
			var req DistributorCountRequirement
			err := yaml.Unmarshal([]byte(yamlInput), &req)
			if tc.expectErr {
				require.Error(t, err, "expected unmarshal to reject search_depth=%s", tc.value)
			} else {
				require.NoError(t, err, "expected unmarshal to accept search_depth=%s", tc.value)
			}
		})
	}
}

// TestBoardCyclingConfigDeserialization verifies that BoardCyclingConfig
// deserializes correctly from YAML with all fields populated.
func TestBoardCyclingConfigDeserialization(t *testing.T) {
	yamlData := []byte(`
cycle_commission: 250.0
re_entry_enabled: false
re_entry_position: sponsor_board
max_cycles_per_period: 3
max_cascade_depth: 15
stall_threshold_periods: 6
inactive_compression: false
`)
	var cfg BoardCyclingConfig
	err := yaml.Unmarshal(yamlData, &cfg)
	require.NoError(t, err)

	assert.Equal(t, 250.0, cfg.CycleCommission)
	assert.False(t, cfg.ReEntryEnabled)
	assert.Equal(t, "sponsor_board", cfg.ReEntryPosition)
	assert.Equal(t, uint32(3), cfg.MaxCyclesPerPeriod)
	assert.Equal(t, uint32(15), cfg.MaxCascadeDepth)
	assert.Equal(t, uint32(6), cfg.StallThresholdPeriods)
	assert.False(t, cfg.InactiveCompression)
}

// TestOverrideStrategyConstValues pins each override-strategy const to its
// wire-format string. Production code and config-construction tests use the
// const for readability; this test is the single source of truth that
// guarantees a typo in the const value fails fast.
func TestOverrideStrategyConstValues(t *testing.T) {
	assert.Equal(t, "single_walk", overrideStrategySingleWalk)
	assert.Equal(t, "multi_tier", overrideStrategyMultiTier)
}

func TestRankQualification_ParsesWindow(t *testing.T) {
	y := []byte("structures: []\nrequired_products: []\n" +
		"window:\n  threshold_rank: Director\n  qualifying_periods: 6\n  window_periods: 12\n")
	var q RankQualification
	require.NoError(t, yaml.Unmarshal(y, &q))
	require.NotNil(t, q.Window)
	assert.Equal(t, "Director", q.Window.ThresholdRank)
	assert.Equal(t, uint8(6), q.Window.QualifyingPeriods)
	assert.Equal(t, uint8(12), q.Window.WindowPeriods)
}

func TestRankQualification_ParsesTenure(t *testing.T) {
	y := []byte("structures: []\nrequired_products: []\n" +
		"tenure:\n  threshold_rank: Director\n  periods: 12\n")
	var q RankQualification
	require.NoError(t, yaml.Unmarshal(y, &q))
	require.NotNil(t, q.Tenure)
	assert.Equal(t, "Director", q.Tenure.ThresholdRank)
	assert.Equal(t, uint8(12), q.Tenure.Periods)
}
