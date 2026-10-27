//! Plan checks that tie money to the base currency's minor unit.

use super::CompensationPlan;
use crate::currency;

impl CompensationPlan {
    /// Reject a base currency the minor-unit table does not hold, or a payout
    /// currency that differs from it.
    pub(super) fn check_currency(&self) -> Result<(), String> {
        let base = &self.volume.base_currency;
        if currency::minor_units(base).is_none() {
            return Err(format!(
                "volume: base_currency {base:?} is not in the minor-unit table"
            ));
        }
        if self.payout.currency != *base {
            return Err(format!(
                "payout: base_currency {:?} does not match volume base_currency {base:?}",
                self.payout.currency
            ));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use crate::commission::test_helpers::{build_test_plan, default_eligibility};
    use crate::config::commission::LevelCommissionConfig;
    use crate::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
    use crate::d;

    fn usd_plan() -> CompensationPlan {
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
        build_test_plan(default_eligibility(), structure, "U")
    }

    #[test]
    fn a_base_currency_missing_from_the_table_is_refused() {
        let mut plan = usd_plan();
        plan.volume.base_currency = "ZZZ".to_string();
        plan.payout.currency = "ZZZ".to_string();
        assert_eq!(
            plan.check_currency(),
            Err(r#"volume: base_currency "ZZZ" is not in the minor-unit table"#.to_string())
        );
    }

    #[test]
    fn a_payout_currency_that_differs_from_the_base_is_refused() {
        let mut plan = usd_plan();
        plan.payout.currency = "EUR".to_string();
        assert_eq!(
            plan.check_currency(),
            Err(
                r#"payout: base_currency "EUR" does not match volume base_currency "USD""#
                    .to_string()
            )
        );
    }

    #[test]
    fn a_known_matching_currency_passes() {
        assert_eq!(usd_plan().check_currency(), Ok(()));
    }

    #[test]
    fn validate_runs_the_currency_check() {
        let mut plan = usd_plan();
        plan.payout.currency = "EUR".to_string();
        assert!(
            plan.validate()
                .unwrap_err()
                .contains("does not match volume base_currency")
        );
    }
}
