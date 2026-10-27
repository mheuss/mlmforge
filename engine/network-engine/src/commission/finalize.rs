//! The finalize step: each earning's money is rounded to the plan currency's minor unit.

use crate::config::CompensationPlan;
use crate::currency;

use super::types::{CalculationError, CommissionEarning};

/// The decimal places of the plan base currency's minor unit.
pub(crate) fn plan_minor_units(plan: &CompensationPlan) -> Result<u32, CalculationError> {
    currency::minor_units(&plan.volume.base_currency)
        .ok_or_else(|| CalculationError::UnknownCurrency(plan.volume.base_currency.clone()))
}

/// Rounds each earning's `dollar_amount` to `minor_units` decimal places.
pub(crate) fn round_earnings(earnings: &mut [CommissionEarning], minor_units: u32) {
    for earning in earnings {
        earning.dollar_amount = earning.dollar_amount.round_minor(minor_units);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::{build_test_plan, default_eligibility};
    use crate::config::commission::LevelCommissionConfig;
    use crate::config::{StructureConfig, UnilevelStructureConfig};
    use crate::d;

    fn plan_in(code: &str) -> CompensationPlan {
        let structure = StructureConfig::Unilevel(UnilevelStructureConfig {
            name: "U".to_string(),
            level_commission: LevelCommissionConfig {
                broad_commission_percent: d!(0.40),
                volume_to_dollar_multiplier: None,
                max_depth: 1,
                rate_table: Default::default(),
            },
            compression: None,
            pass_up: None,
        });
        let mut plan = build_test_plan(default_eligibility(), structure, "U");
        plan.volume.base_currency = code.to_string();
        plan
    }

    #[test]
    fn a_plan_currency_resolves_to_its_minor_units() {
        assert_eq!(plan_minor_units(&plan_in("JPY")), Ok(0));
        assert_eq!(plan_minor_units(&plan_in("KWD")), Ok(3));
    }

    #[test]
    fn an_unknown_plan_currency_is_refused() {
        assert_eq!(
            plan_minor_units(&plan_in("ZZZ")),
            Err(CalculationError::UnknownCurrency("ZZZ".to_string()))
        );
    }

    #[test]
    fn rounding_sends_each_earnings_ties_away_from_zero() {
        let earning = |amount| CommissionEarning {
            earner_id: uuid::Uuid::nil(),
            source_id: uuid::Uuid::nil(),
            level: 1,
            rate: None,
            cv_amount: d!(0.25),
            dollar_amount: amount,
            walk: None,
        };
        let mut earnings = vec![earning(d!(0.005)), earning(d!(-0.005)), earning(d!(0.0049))];
        round_earnings(&mut earnings, 2);
        let amounts: Vec<_> = earnings.iter().map(|e| e.dollar_amount).collect();
        assert_eq!(amounts, vec![d!(0.01), d!(-0.01), d!(0)]);
        assert!(earnings.iter().all(|e| e.cv_amount == d!(0.25)));
    }
}
