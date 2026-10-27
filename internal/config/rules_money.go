package config

import (
	"fmt"
	"maps"
	"slices"

	"github.com/mlmforge/mlmforge/internal/currency"
	"github.com/mlmforge/mlmforge/internal/dec"
)

// validateCurrency refuses a base currency the minor-unit table does not hold.
func validateCurrency(plan *CompensationPlan) []ValidationError {
	code := plan.Volume.BaseCurrency
	if _, ok := currency.MinorUnits(code); ok {
		return nil
	}
	return []ValidationError{{
		Path:     "/volume/base_currency",
		Code:     "unknown_currency",
		Message:  fmt.Sprintf("volume base_currency %q is not in the minor-unit table", code),
		Severity: SeverityError,
	}}
}

// validateMinorUnits refuses a plan money value that is not a whole number of the base currency's minor units.
func validateMinorUnits(plan *CompensationPlan) []ValidationError {
	code := plan.Volume.BaseCurrency
	places, ok := currency.MinorUnits(code)
	if !ok {
		return nil
	}
	var errs []ValidationError
	check := func(path string, a dec.Amount) {
		if a.Equal(a.Round(places)) {
			return
		}
		errs = append(errs, ValidationError{
			Path:     path,
			Code:     "not_whole_minor_units",
			Message:  fmt.Sprintf("%s is not a whole number of %s minor units, which have %d decimal places", a.String(), code, places),
			Severity: SeverityError,
		})
	}
	checkPtr := func(path string, a *dec.Amount) {
		if a != nil {
			check(path, *a)
		}
	}
	for i, s := range plan.Structures {
		at := fmt.Sprintf("/structures/%d/commission", i)
		switch rc := s.resolvedCommission.(type) {
		case *BinaryCommission:
			if rc.Mode == "pairing" && rc.Pairing != nil {
				checkPtr(at+"/pairing/cap_per_period", rc.Pairing.CapPerPeriod)
			}
			if rc.Mode == "cycle_step" && rc.CycleStep != nil {
				checkPtr(at+"/cycle_step/cap_per_period", rc.CycleStep.CapPerPeriod)
				for k, step := range rc.CycleStep.Steps {
					check(fmt.Sprintf("%s/cycle_step/steps/%d/amount", at, k), step.Amount)
				}
			}
		case *BoardPlanCommission:
			check(at+"/board_cycling/cycle_commission", rc.BoardCycling.CycleCommission)
		case *StairstepCommission:
			if rc.Breakaway != nil && rc.Breakaway.Overrides.Type == overrideStrategySingleWalk && rc.Breakaway.Overrides.OverrideCalculation == "differential" {
				if diff := rc.Breakaway.Overrides.Differential; diff != nil && diff.MinOverride.Type == "currency" {
					check(at+"/breakaway/overrides/differential/min_override/value", diff.MinOverride.Value)
				}
			}
		}
	}
	b := plan.Bonuses
	if b.BoardCycling != nil {
		check("/bonuses/board_cycling/cycle_commission", b.BoardCycling.CycleCommission)
	}
	if b.MatrixCompletion != nil {
		check("/bonuses/matrix_completion/full_matrix", b.MatrixCompletion.FullMatrix)
		for _, level := range slices.Sorted(maps.Keys(b.MatrixCompletion.PerLevel)) {
			check("/bonuses/matrix_completion/per_level/"+level, b.MatrixCompletion.PerLevel[level])
		}
	}
	if b.RankAdvancement != nil {
		for _, rank := range slices.Sorted(maps.Keys(b.RankAdvancement.Amounts)) {
			check("/bonuses/rank_advancement/amounts/"+rank, b.RankAdvancement.Amounts[rank])
		}
	}
	if b.Lifestyle != nil {
		for k, tier := range b.Lifestyle.Tiers {
			check(fmt.Sprintf("/bonuses/lifestyle/tiers/%d/amount", k), tier.Amount)
		}
	}
	if b.Sponsor != nil && b.Sponsor.AmountType == "fixed" {
		check("/bonuses/sponsor/amount", b.Sponsor.Amount)
	}
	if b.Position != nil && b.Position.AmountType == "fixed" {
		check("/bonuses/position/amount", b.Position.Amount)
	}
	for k, pool := range b.Pool {
		checkPtr(fmt.Sprintf("/bonuses/pool/%d/shares/equal_share_cap", k), pool.Shares.EqualShareCap)
	}
	check("/payout/minimum_amount", plan.Payout.MinimumAmount)
	checkPtr("/caps/per_distributor_per_period", plan.Caps.PerDistributorPerPeriod)
	return errs
}
