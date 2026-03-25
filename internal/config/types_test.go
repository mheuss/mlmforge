package config

import (
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
	assert.Equal(t, 14, plan.Period.PayoutLagDays)
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
	assert.Equal(t, 2, rank2.DemotionPolicy.Grace.Count)
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
	assert.Equal(t, 5, c.CommissionableDepth)
	assert.Equal(t, 0.05, c.RateTable["Associate"]["1"])
	assert.Equal(t, 0.04, c.RateTable["Associate"]["2"])
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
	assert.Equal(t, 10, c.CommissionableDepth)
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
	assert.Equal(t, 5, c.CommissionableDepth)
	assert.Equal(t, 4, c.Generation.MaxGenerations)
	assert.Equal(t, "threshold_rank", c.Generation.BoundaryMode)
	assert.Equal(t, "Executive", c.Generation.BoundaryRank)
	assert.True(t, c.Generation.EmptyGenerationConsumesNumber)
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
	assert.Equal(t, 6, c.CommissionableDepth)
	require.NotNil(t, c.Breakaway)
	assert.Equal(t, "Supervisor", c.Breakaway.ThresholdRank)
	assert.True(t, c.Breakaway.GroupVolumeExcludesBreakaway)
	assert.Equal(t, "differential", c.Breakaway.OverrideCalculation)
	require.NotNil(t, c.Breakaway.Differential)
	assert.Equal(t, 0.05, c.Breakaway.Differential.RankRates["Supervisor"])
	assert.Equal(t, 10.0, c.Breakaway.Differential.MinOverride)
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
	assert.Equal(t, 7, c.CommissionableDepth)
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
	assert.Equal(t, 5, c.BoardCycling.MaxCyclesPerPeriod)
	assert.Equal(t, 10, c.BoardCycling.MaxCascadeDepth)
	assert.Equal(t, 3, c.BoardCycling.StallThresholdPeriods)
	assert.True(t, c.BoardCycling.InactiveCompression)
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
	assert.Equal(t, 3, cfg.MaxCyclesPerPeriod)
	assert.Equal(t, 15, cfg.MaxCascadeDepth)
	assert.Equal(t, 6, cfg.StallThresholdPeriods)
	assert.False(t, cfg.InactiveCompression)
}
