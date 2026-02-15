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
  pass_up: null
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
