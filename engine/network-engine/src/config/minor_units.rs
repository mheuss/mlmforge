//! Plan checks that tie money to the base currency's minor unit.

use super::binary::BinaryCommissionMode;
use super::bonus::AmountType;
use super::stairstep::{BreakawayConfig, MinOverride, OverrideMode, OverrideStrategy};
use super::{CompensationPlan, StructureConfig};
use crate::currency;
use crate::decimal::AuthoredDec;

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

    /// Reject a money value that is not a whole number of the base currency's minor units.
    pub(super) fn check_whole_minor_units(&self) -> Result<(), String> {
        let code = &self.volume.base_currency;
        let Some(places) = currency::minor_units(code) else {
            return Ok(());
        };
        let check = |field: &str, v: AuthoredDec| -> Result<(), String> {
            if v.dec().round_minor(places) == v.dec() {
                return Ok(());
            }
            Err(format!(
                "{field} {v} is not a whole number of {code} minor units, which have {places} decimal places"
            ))
        };
        for structure in &self.structures {
            match structure {
                StructureConfig::Binary(c) => {
                    let at = format!("structure '{}'", c.name);
                    match &c.binary_commission.mode {
                        BinaryCommissionMode::Pairing(p) => {
                            if let Some(cap) = p.cap_per_period {
                                check(&format!("{at}: pairing cap_per_period"), cap)?;
                            }
                        }
                        BinaryCommissionMode::CycleStep(cs) => {
                            if let Some(cap) = cs.cap_per_period {
                                check(&format!("{at}: cycle_step cap_per_period"), cap)?;
                            }
                            for (k, step) in cs.steps.iter().enumerate() {
                                check(&format!("{at}: cycle_step steps[{k}] amount"), step.amount)?;
                            }
                        }
                    }
                }
                StructureConfig::BoardPlan(c) => check(
                    &format!("structure '{}': board_cycling cycle_commission", c.name),
                    c.board_cycling.cycle_commission,
                )?,
                StructureConfig::Stairstep(c) => {
                    if let Some(BreakawayConfig {
                        overrides:
                            OverrideStrategy::SingleWalk {
                                mode: OverrideMode::Differential(diff),
                                ..
                            },
                        ..
                    }) = &c.breakaway
                    {
                        match diff.min_override {
                            MinOverride::Currency { value } => check(
                                &format!("structure '{}': differential min_override", c.name),
                                value,
                            )?,
                            MinOverride::Rate { .. } => {}
                        }
                    }
                }
                StructureConfig::Unilevel(_)
                | StructureConfig::Matrix(_)
                | StructureConfig::Generation(_)
                | StructureConfig::Streamline(_) => {}
            }
        }
        let bonuses = &self.bonuses;
        if let Some(mc) = &bonuses.matrix_completion {
            check(
                "bonuses: matrix_completion full_matrix",
                mc.full_matrix_amount,
            )?;
            for (level, amount) in &mc.per_level_amounts {
                check(
                    &format!("bonuses: matrix_completion per_level[{level}]"),
                    *amount,
                )?;
            }
        }
        if let Some(ra) = &bonuses.rank_advancement {
            for (rank, amount) in &ra.amounts {
                check(
                    &format!("bonuses: rank_advancement amounts[{rank}]"),
                    *amount,
                )?;
            }
        }
        if let Some(lifestyle) = &bonuses.lifestyle {
            for (k, tier) in lifestyle.tiers.iter().enumerate() {
                check(
                    &format!("bonuses: lifestyle tiers[{k}] amount"),
                    tier.amount,
                )?;
            }
        }
        if let Some(sponsor) = &bonuses.sponsor {
            if sponsor.amount_type == AmountType::Fixed {
                check("bonuses: sponsor amount", sponsor.amount)?;
            }
        }
        if let Some(position) = &bonuses.position {
            if position.amount_type == AmountType::Fixed {
                check("bonuses: position amount", position.amount)?;
            }
        }
        for (k, pool) in bonuses.pool.iter().flatten().enumerate() {
            if let Some(cap) = pool.shares.equal_share_cap {
                check(&format!("bonuses: pool[{k}] equal_share_cap"), cap)?;
            }
        }
        check("payout: minimum_amount", self.payout.minimum_payout)?;
        if let Some(cap) = self.caps.per_distributor_cap {
            check("caps: per_distributor_per_period", cap)?;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::num::NonZeroU32;

    use crate::commission::test_helpers::{build_test_plan, default_eligibility};
    use crate::config::binary::{
        BinaryCommissionConfig, BinaryCommissionMode, CycleStep, CycleStepConfig,
        MultiPositionCapMode, PairingCalculation, PairingConfig, VolumeAfterPayout,
    };
    use crate::config::board_plan::{BoardPlanConfig, ReEntryPosition};
    use crate::config::bonus::{
        AmountType, LifestyleBonusConfig, LifestyleTier, MatrixCompletionBonusConfig,
        PoolBonusConfig, PoolQualification, PoolQualificationMode, PoolShareMode, PoolShares,
        PositionBonusConfig, RankAdvancementBonusConfig, SponsorBonusConfig,
    };
    use crate::config::commission::LevelCommissionConfig;
    use crate::config::stairstep::{
        BreakawayConfig, DifferentialConfig, MinOverride, OverrideMode, OverrideStrategy,
    };
    use crate::config::{
        BinaryStructureConfig, BoardPlanStructureConfig, CompensationPlan,
        StairstepStructureConfig, StructureConfig, UnilevelStructureConfig,
    };
    use crate::d;
    use crate::decimal::AuthoredDec;

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

    fn whole_units(
        set: impl Fn(&mut CompensationPlan, AuthoredDec),
        value: AuthoredDec,
    ) -> Result<(), String> {
        let mut plan = usd_plan();
        set(&mut plan, value);
        plan.check_whole_minor_units()
    }

    fn assert_refuses_half_cents(set: impl Fn(&mut CompensationPlan, AuthoredDec), field: &str) {
        assert_eq!(
            whole_units(&set, d!(2.005)),
            Err(format!(
                "{field} 2.005 is not a whole number of USD minor units, which have 2 decimal places"
            ))
        );
        assert_eq!(whole_units(&set, d!(2.01)), Ok(()));
        assert_eq!(whole_units(&set, d!(2.010)), Ok(()));
    }

    fn level() -> LevelCommissionConfig {
        LevelCommissionConfig {
            broad_commission_percent: d!(0.40),
            volume_to_dollar_multiplier: None,
            max_depth: 1,
            rate_table: Default::default(),
        }
    }

    fn pairing(cap: Option<AuthoredDec>) -> StructureConfig {
        StructureConfig::Binary(BinaryStructureConfig {
            name: "B".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent: d!(0.10),
                    calculation: PairingCalculation::WeakerLeg,
                    cap_per_period: cap,
                    volume_after_payout: VolumeAfterPayout::FullFlush,
                    carry_forward_cap: None,
                    multi_position_cap_mode: MultiPositionCapMode::PerPosition,
                }),
            },
        })
    }

    fn cycle_step(cap: Option<AuthoredDec>, amount: AuthoredDec) -> StructureConfig {
        StructureConfig::Binary(BinaryStructureConfig {
            name: "B".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::CycleStep(CycleStepConfig {
                    steps: vec![CycleStep {
                        threshold: d!(100),
                        amount,
                    }],
                    volume_after_cycle: VolumeAfterPayout::FullFlush,
                    cap_per_period: cap,
                    carry_forward_cap: None,
                    multi_position_cap_mode: MultiPositionCapMode::PerPosition,
                }),
            },
        })
    }

    fn board(cycle_commission: AuthoredDec) -> StructureConfig {
        StructureConfig::BoardPlan(BoardPlanStructureConfig {
            name: "P".to_string(),
            width: 2,
            height: 2,
            board_cycling: BoardPlanConfig {
                cycle_commission,
                re_entry_enabled: true,
                re_entry_position: ReEntryPosition::Bottom,
                max_cycles_per_period: 3,
                max_cascade_depth: NonZeroU32::new(10).unwrap(),
                stall_threshold_periods: 3,
                inactive_compression: false,
            },
        })
    }

    fn stairstep(min_override: MinOverride) -> StructureConfig {
        StructureConfig::Stairstep(StairstepStructureConfig {
            name: "S".to_string(),
            level_commission: level(),
            compression: None,
            breakaway: Some(BreakawayConfig {
                threshold_rank: "associate".to_string(),
                exclude_breakaway_gv: false,
                overrides: OverrideStrategy::SingleWalk {
                    mode: OverrideMode::Differential(DifferentialConfig {
                        rank_rates: BTreeMap::new(),
                        min_override,
                    }),
                    generation_overrides: None,
                },
            }),
        })
    }

    fn pool(equal_share_cap: Option<AuthoredDec>) -> PoolBonusConfig {
        PoolBonusConfig {
            name: "pool".to_string(),
            source_percent: d!(0.01),
            qualification: PoolQualification {
                mode: PoolQualificationMode::RankBased,
                min_rank: Some("associate".to_string()),
                velocity: None,
            },
            shares: PoolShares {
                mode: PoolShareMode::EqualShare,
                equal_share_cap,
            },
            require_admin_confirmation: false,
        }
    }

    #[test]
    fn pairing_cap_per_period_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| p.structures.push(pairing(Some(v))),
            "structure 'B': pairing cap_per_period",
        );
    }

    #[test]
    fn cycle_step_cap_per_period_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| p.structures.push(cycle_step(Some(v), d!(100))),
            "structure 'B': cycle_step cap_per_period",
        );
    }

    #[test]
    fn cycle_step_amount_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| p.structures.push(cycle_step(None, v)),
            "structure 'B': cycle_step steps[0] amount",
        );
    }

    #[test]
    fn board_cycle_commission_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| p.structures.push(board(v)),
            "structure 'P': board_cycling cycle_commission",
        );
    }

    #[test]
    fn currency_min_override_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| {
                p.structures
                    .push(stairstep(MinOverride::Currency { value: v }))
            },
            "structure 'S': differential min_override",
        );
    }

    #[test]
    fn a_rate_min_override_is_not_a_money_value() {
        let set = |p: &mut CompensationPlan, v| {
            p.structures.push(stairstep(MinOverride::Rate { value: v }))
        };
        assert_eq!(whole_units(set, d!(0.015)), Ok(()));
    }

    #[test]
    fn matrix_completion_full_matrix_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| {
                p.bonuses.matrix_completion = Some(MatrixCompletionBonusConfig {
                    per_level_amounts: BTreeMap::new(),
                    full_matrix_amount: v,
                })
            },
            "bonuses: matrix_completion full_matrix",
        );
    }

    #[test]
    fn matrix_completion_per_level_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| {
                p.bonuses.matrix_completion = Some(MatrixCompletionBonusConfig {
                    per_level_amounts: BTreeMap::from([(1, v)]),
                    full_matrix_amount: d!(100),
                })
            },
            "bonuses: matrix_completion per_level[1]",
        );
    }

    #[test]
    fn rank_advancement_amounts_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| {
                p.bonuses.rank_advancement = Some(RankAdvancementBonusConfig {
                    amounts: BTreeMap::from([("associate".to_string(), v)]),
                    pay_once_only: false,
                })
            },
            "bonuses: rank_advancement amounts[associate]",
        );
    }

    #[test]
    fn lifestyle_tier_amount_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| {
                p.bonuses.lifestyle = Some(LifestyleBonusConfig {
                    tiers: vec![LifestyleTier {
                        min_rank: "associate".to_string(),
                        amount: v,
                        grace_periods: 0,
                    }],
                })
            },
            "bonuses: lifestyle tiers[0] amount",
        );
    }

    #[test]
    fn fixed_sponsor_amount_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| {
                p.bonuses.sponsor = Some(SponsorBonusConfig {
                    amount: v,
                    amount_type: AmountType::Fixed,
                    qualifying_products: vec![],
                })
            },
            "bonuses: sponsor amount",
        );
    }

    #[test]
    fn fixed_position_amount_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| {
                p.bonuses.position = Some(PositionBonusConfig {
                    amount: v,
                    amount_type: AmountType::Fixed,
                    sponsored_only: false,
                })
            },
            "bonuses: position amount",
        );
    }

    #[test]
    fn equal_share_cap_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| p.bonuses.pool = Some(vec![pool(Some(v))]),
            "bonuses: pool[0] equal_share_cap",
        );
    }

    #[test]
    fn minimum_payout_must_be_whole_cents() {
        assert_refuses_half_cents(|p, v| p.payout.minimum_payout = v, "payout: minimum_amount");
    }

    #[test]
    fn per_distributor_cap_must_be_whole_cents() {
        assert_refuses_half_cents(
            |p, v| p.caps.per_distributor_cap = Some(v),
            "caps: per_distributor_per_period",
        );
    }

    #[test]
    fn a_percentage_sponsor_amount_is_not_a_money_value() {
        let set = |p: &mut CompensationPlan, v| {
            p.bonuses.sponsor = Some(SponsorBonusConfig {
                amount: v,
                amount_type: AmountType::Percentage,
                qualifying_products: vec![],
            });
        };
        assert_eq!(whole_units(set, d!(0.015)), Ok(()));
    }

    #[test]
    fn a_percentage_position_amount_is_not_a_money_value() {
        let set = |p: &mut CompensationPlan, v| {
            p.bonuses.position = Some(PositionBonusConfig {
                amount: v,
                amount_type: AmountType::Percentage,
                sponsored_only: false,
            });
        };
        assert_eq!(whole_units(set, d!(0.015)), Ok(()));
    }

    #[test]
    fn a_jpy_plan_refuses_a_fractional_yen() {
        let mut plan = usd_plan();
        plan.volume.base_currency = "JPY".to_string();
        plan.payout.currency = "JPY".to_string();
        plan.payout.minimum_payout = d!(1.5);
        assert_eq!(
            plan.check_whole_minor_units(),
            Err("payout: minimum_amount 1.5 is not a whole number of JPY minor units, which have 0 decimal places".to_string())
        );
    }

    #[test]
    fn a_jpy_plan_accepts_a_whole_yen_written_with_a_decimal_place() {
        let mut plan = usd_plan();
        plan.volume.base_currency = "JPY".to_string();
        plan.payout.currency = "JPY".to_string();
        plan.payout.minimum_payout = d!(50.0);
        assert_eq!(plan.check_whole_minor_units(), Ok(()));
    }

    #[test]
    fn a_negative_half_cent_is_refused_and_a_negative_cent_accepted() {
        let set = |p: &mut CompensationPlan, v| p.payout.minimum_payout = v;
        assert_eq!(
            whole_units(set, d!(-2.005)),
            Err("payout: minimum_amount -2.005 is not a whole number of USD minor units, which have 2 decimal places".to_string())
        );
        assert_eq!(whole_units(set, d!(-2.01)), Ok(()));
    }

    #[test]
    fn the_test_plan_has_whole_money_values() {
        assert_eq!(usd_plan().check_whole_minor_units(), Ok(()));
    }

    #[test]
    fn validate_runs_the_whole_minor_unit_check() {
        let mut plan = usd_plan();
        plan.payout.minimum_payout = d!(2.005);
        assert!(
            plan.validate()
                .unwrap_err()
                .contains("payout: minimum_amount 2.005")
        );
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

    #[test]
    fn validate_reports_the_currency_before_a_fractional_value() {
        let mut plan = usd_plan();
        plan.payout.currency = "EUR".to_string();
        plan.payout.minimum_payout = d!(2.005);
        let err = plan.validate().unwrap_err();
        assert!(err.contains("does not match volume base_currency"), "{err}");
    }
}
