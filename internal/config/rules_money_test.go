package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mlmforge/mlmforge/internal/dec"
)

func TestRulesRefuseABaseCurrencyMissingFromTheTable(t *testing.T) {
	plan := minimalPlan()
	plan.Volume.BaseCurrency = "ZZZ"
	plan.Payout.BaseCurrency = "ZZZ"

	errs := validateBusinessRules(plan)

	require.Len(t, errs, 1)
	assert.Equal(t, "/volume/base_currency", errs[0].Path)
	assert.Equal(t, "unknown_currency", errs[0].Code)
	assert.Equal(t, `volume base_currency "ZZZ" is not in the minor-unit table`, errs[0].Message)
	assert.Equal(t, SeverityError, errs[0].Severity)
}

func TestRulesRefuseMoneyThatIsNotWholeCents(t *testing.T) {
	binary := func(c *BinaryCommission) StructureConfig {
		return StructureConfig{Name: "B", Type: "binary", resolvedCommission: c}
	}
	cases := []struct {
		name string
		path string
		set  func(p *CompensationPlan, v dec.Amount)
	}{
		{"pairing cap_per_period", "/structures/1/commission/pairing/cap_per_period", func(p *CompensationPlan, v dec.Amount) {
			p.Structures = append(p.Structures, binary(&BinaryCommission{Mode: "pairing", Pairing: &PairingConfig{CapPerPeriod: &v}}))
		}},
		{"cycle_step cap_per_period", "/structures/1/commission/cycle_step/cap_per_period", func(p *CompensationPlan, v dec.Amount) {
			p.Structures = append(p.Structures, binary(&BinaryCommission{Mode: "cycle_step", CycleStep: &CycleStepConfig{CapPerPeriod: &v, Steps: []CycleStep{{Amount: dec.MustParse("100")}}}}))
		}},
		{"cycle_step amount", "/structures/1/commission/cycle_step/steps/0/amount", func(p *CompensationPlan, v dec.Amount) {
			p.Structures = append(p.Structures, binary(&BinaryCommission{Mode: "cycle_step", CycleStep: &CycleStepConfig{Steps: []CycleStep{{Amount: v}}}}))
		}},
		{"board plan cycle_commission", "/structures/1/commission/board_cycling/cycle_commission", func(p *CompensationPlan, v dec.Amount) {
			p.Structures = append(p.Structures, StructureConfig{Name: "P", Type: "board_plan", resolvedCommission: &BoardPlanCommission{BoardCycling: BoardCyclingConfig{CycleCommission: v}}})
		}},
		{"bonus board_cycling cycle_commission", "/bonuses/board_cycling/cycle_commission", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.BoardCycling = &BoardCyclingConfig{CycleCommission: v}
		}},
		{"currency min_override", "/structures/1/commission/breakaway/overrides/differential/min_override/value", func(p *CompensationPlan, v dec.Amount) {
			p.Structures = append(p.Structures, StructureConfig{Name: "S", Type: "stairstep", resolvedCommission: &StairstepCommission{Breakaway: &BreakawayConfig{Overrides: OverrideStrategy{Differential: &DifferentialConfig{MinOverride: MinOverride{Type: "currency", Value: v}}}}}})
		}},
		{"matrix_completion full_matrix", "/bonuses/matrix_completion/full_matrix", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.MatrixCompletion = &MatrixCompletionBonusConfig{FullMatrix: v}
		}},
		{"matrix_completion per_level", "/bonuses/matrix_completion/per_level/1", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.MatrixCompletion = &MatrixCompletionBonusConfig{FullMatrix: dec.MustParse("100"), PerLevel: map[string]dec.Amount{"1": v}}
		}},
		{"rank_advancement amounts", "/bonuses/rank_advancement/amounts/Silver", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.RankAdvancement = &RankAdvancementBonusConfig{Amounts: map[string]dec.Amount{"Silver": v}}
		}},
		{"lifestyle tier amount", "/bonuses/lifestyle/tiers/0/amount", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.Lifestyle = &LifestyleBonusConfig{Tiers: []LifestyleTier{{MinRank: "Silver", Amount: v}}}
		}},
		{"fixed sponsor amount", "/bonuses/sponsor/amount", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.Sponsor = &SponsorBonusConfig{Amount: v, AmountType: "fixed"}
		}},
		{"fixed position amount", "/bonuses/position/amount", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.Position = &PositionBonusConfig{Amount: v, AmountType: "fixed"}
		}},
		{"equal_share_cap", "/bonuses/pool/0/shares/equal_share_cap", func(p *CompensationPlan, v dec.Amount) {
			p.Bonuses.Pool = []PoolBonusConfig{{Shares: PoolShares{EqualShareCap: &v}}}
		}},
		{"minimum_amount", "/payout/minimum_amount", func(p *CompensationPlan, v dec.Amount) {
			p.Payout.MinimumAmount = v
		}},
		{"per_distributor_per_period", "/caps/per_distributor_per_period", func(p *CompensationPlan, v dec.Amount) {
			p.Caps.PerDistributorPerPeriod = &v
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" refuses 2.005", func(t *testing.T) {
			plan := minimalPlan()
			tc.set(plan, dec.MustParse("2.005"))
			errs := validateMinorUnits(plan)
			require.Len(t, errs, 1)
			assert.Equal(t, tc.path, errs[0].Path)
			assert.Equal(t, "not_whole_minor_units", errs[0].Code)
			assert.Equal(t, "2.005 is not a whole number of USD minor units, which have 2 decimal places", errs[0].Message)
		})
		t.Run(tc.name+" accepts 2.01 and 2.010", func(t *testing.T) {
			for _, text := range []string{"2.01", "2.010"} {
				plan := minimalPlan()
				tc.set(plan, dec.MustParse(text))
				assert.Empty(t, validateMinorUnits(plan), text)
			}
		})
	}
}

func TestRulesDoNotCheckAPercentageSponsorOrPositionAmount(t *testing.T) {
	plan := minimalPlan()
	plan.Bonuses.Sponsor = &SponsorBonusConfig{Amount: dec.MustParse("0.015"), AmountType: "percentage"}
	plan.Bonuses.Position = &PositionBonusConfig{Amount: dec.MustParse("0.015"), AmountType: "percentage"}
	assert.Empty(t, validateBusinessRules(plan))
}

func TestRulesDoNotCheckARateMinOverride(t *testing.T) {
	plan := minimalPlan()
	plan.Structures = append(plan.Structures, StructureConfig{Name: "S", Type: "stairstep", resolvedCommission: &StairstepCommission{Breakaway: &BreakawayConfig{Overrides: OverrideStrategy{Differential: &DifferentialConfig{MinOverride: MinOverride{Type: "rate", Value: dec.MustParse("0.015")}}}}}})
	assert.Empty(t, validateMinorUnits(plan))
}

func TestRulesRefuseAFractionalYen(t *testing.T) {
	plan := minimalPlan()
	plan.Volume.BaseCurrency = "JPY"
	plan.Payout.BaseCurrency = "JPY"
	plan.Payout.MinimumAmount = dec.MustParse("1.5")
	errs := validateMinorUnits(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "1.5 is not a whole number of JPY minor units, which have 0 decimal places", errs[0].Message)
}

func TestRulesAcceptAWholeYenWrittenWithADecimalPlace(t *testing.T) {
	plan := minimalPlan()
	plan.Volume.BaseCurrency = "JPY"
	plan.Payout.BaseCurrency = "JPY"
	plan.Payout.MinimumAmount = dec.MustParse("50.0")
	assert.Empty(t, validateMinorUnits(plan))
}

func TestRulesRefuseANegativeHalfCent(t *testing.T) {
	plan := minimalPlan()
	plan.Payout.MinimumAmount = dec.MustParse("-2.005")
	errs := validateMinorUnits(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "-2.005 is not a whole number of USD minor units, which have 2 decimal places", errs[0].Message)
}

func TestBusinessRulesRunTheMinorUnitCheck(t *testing.T) {
	plan := minimalPlan()
	plan.Payout.MinimumAmount = dec.MustParse("50.005")
	errs := validateBusinessRules(plan)
	require.Len(t, errs, 1)
	assert.Equal(t, "/payout/minimum_amount", errs[0].Path)
}

func TestRulesAcceptEveryTestCurrency(t *testing.T) {
	for _, code := range []string{"USD", "JPY", "KWD"} {
		plan := minimalPlan()
		plan.Volume.BaseCurrency = code
		plan.Payout.BaseCurrency = code
		assert.Empty(t, validateCurrency(plan), code)
	}
}
