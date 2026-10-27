//! Engine-side validation of a loaded compensation plan.
//!
//! The engine is the system of record for money, so it can't assume the
//! upstream Go JSON-schema pipeline ran. A direct caller, the
//! out-of-process transport, or a pipeline bug could hand it a
//! structurally-valid but semantically-broken plan. `CompensationPlan::validate`
//! is the engine's own trust boundary: it turns out-of-range values (which
//! would otherwise produce silently-wrong payouts) and the >255-tier stairstep
//! case (which would panic in `walk_multi_tier_overrides`) into a loud, coded
//! rejection, regardless of caller.
//!
//! Scope is deliberately the money-path invariants the calculators assume, not
//! a field-by-field mirror of the JSON schema — that contract is HEU-513
//! codegen territory. Concretely: commission percents in `[0, 1]`, CV-to-dollar
//! multipliers positive, per-period caps and per-leg rank caps non-negative,
//! breakaway tiers non-empty and `<= 255`, matrix
//! parameters the engine can actually run (`width >= 2`, breadth-first
//! spillover only), and structure names unique across the plan so name-based
//! lookup is unambiguous.
//!
//! Streamline `dynamic_compression` is checked too: non-empty, a contiguous
//! ascending run from 1, and no longer than `commissionable_depth` (HEU-612).
//!
//! The pattern follows `CycleStepConfig::validate` (config/binary.rs): `&mut
//! self` so normalization can ride along with the checks, and `Result<(),
//! String>` where the string names the violated invariant. `handle_load_plan`
//! runs this before storing the plan and maps the error to the `INVALID_PLAN`
//! response code. The separate `plan.version` gate lives in the handler with
//! its own `UNSUPPORTED_PLAN_VERSION` code, because a valid future-version plan
//! is not a malformed one.

use std::collections::HashSet;

use super::binary::{BinaryCommissionConfig, BinaryCommissionMode, PairingConfig};
use super::board_plan::BoardPlanConfig;
use super::commission::LevelCommissionConfig;
use super::generation::GenerationCommissionConfig;
use super::matrix::{MatrixStructureParams, SpilloverDirection};
use super::payout::CapsConfig;
use super::rank::{RankQualification, StructureQualification};
use super::stairstep::{BreakawayConfig, MinOverride, OverrideMode, OverrideStrategy};
use super::streamline::StreamlineCommissionConfig;
use super::volume::VolumeConfig;
use super::{CompensationPlan, StructureConfig};
use crate::decimal::{AuthoredDec, Dec};

// --- numeric helpers ------------------------------------------------------

/// A value that must be a fraction within `[0, 1]`.
fn check_fraction(field: &str, v: AuthoredDec) -> Result<(), String> {
    if v.is_negative() || v.dec() > Dec::ONE {
        return Err(format!("{field} must be a fraction in [0.0, 1.0], got {v}"));
    }
    Ok(())
}

/// A multiplier or threshold that must be strictly positive.
fn check_positive(field: &str, v: AuthoredDec) -> Result<(), String> {
    if v <= AuthoredDec::ZERO {
        return Err(format!("{field} must be greater than 0, got {v}"));
    }
    Ok(())
}

/// A floor whose bounds depend on the unit it carries.
fn check_min_override(v: &MinOverride) -> Result<(), String> {
    match v {
        MinOverride::Rate { value } => check_fraction("differential min_override", *value),
        MinOverride::Currency { value } => check_non_negative("differential min_override", *value),
    }
}

/// A monetary amount or cap that may be zero but not negative.
fn check_non_negative(field: &str, v: AuthoredDec) -> Result<(), String> {
    if v.is_negative() {
        return Err(format!("{field} must be non-negative, got {v}"));
    }
    Ok(())
}

/// `None` means "fall back to the plan-level value" — absent, not invalid — so
/// an unset optional multiplier passes. Every commission fixture leaves the
/// per-structure `volume_to_dollar_multiplier` null, so treating `None` as a
/// failure would reject every real plan.
fn check_optional_positive(field: &str, v: Option<AuthoredDec>) -> Result<(), String> {
    match v {
        Some(x) => check_positive(field, x),
        None => Ok(()),
    }
}

/// Optional cap: absent is fine, present must be non-negative.
fn check_optional_non_negative(field: &str, v: Option<AuthoredDec>) -> Result<(), String> {
    match v {
        Some(x) => check_non_negative(field, x),
        None => Ok(()),
    }
}

// --- top-level orchestration ---------------------------------------------

impl CompensationPlan {
    /// Validate the money-path invariants the commission calculators assume.
    ///
    /// Called by the worker's `handle_load_plan` before the plan is stored, so
    /// an out-of-range plan is rejected at the boundary instead of producing
    /// wrong payouts (or panicking) later. Returns `Err` with a message naming
    /// the first violated invariant. Takes `&mut self` to match the
    /// `CycleStepConfig::validate` pattern, which normalizes (sorts) as it
    /// checks.
    ///
    /// Version compatibility is intentionally NOT checked here — the handler
    /// gates `version` separately with its own `UNSUPPORTED_PLAN_VERSION` code.
    pub fn validate(&mut self) -> Result<(), String> {
        self.volume.validate().map_err(|e| format!("volume: {e}"))?;
        self.caps.validate().map_err(|e| format!("caps: {e}"))?;
        self.check_currency()?;
        self.check_rank_ladder_non_empty()?;
        self.check_rank_name_and_ordinal_floor()?;
        self.check_base_rank_requires_nothing()?;
        self.check_per_leg_caps()?;
        self.check_unique_structure_names()?;
        for structure in &mut self.structures {
            structure.validate()?;
        }
        Ok(())
    }

    /// Reject a plan with no ranks.
    ///
    /// Every calculator validates its snapshot ranks against this ladder, so
    /// an empty one turns each calculation into an error that names the
    /// snapshot when the plan is what is wrong. The schema already says
    /// `minItems: 1`; this is the engine saying it too, for a caller that
    /// skipped the schema.
    fn check_rank_ladder_non_empty(&self) -> Result<(), String> {
        if self.ranks.is_empty() {
            return Err("ranks: the plan defines no ranks".to_string());
        }
        Ok(())
    }

    /// Reject an empty rank name or an ordinal below 1.
    fn check_rank_name_and_ordinal_floor(&self) -> Result<(), String> {
        for (idx, rank) in self.ranks.iter().enumerate() {
            if rank.name.is_empty() {
                return Err(format!("ranks: rank at index {idx} has an empty name"));
            }
            if rank.ordinal == 0 {
                return Err(format!(
                    "ranks: rank at index {idx} ('{}') has ordinal 0; ordinals start at 1",
                    rank.name
                ));
            }
        }
        Ok(())
    }

    /// Reject a lowest rank that sets any qualification requirement.
    fn check_base_rank_requires_nothing(&self) -> Result<(), String> {
        let Some(base) = self.ranks.iter().min_by_key(|r| r.ordinal) else {
            return Ok(());
        };
        let name = &base.name;
        let refuse = |what: String| -> Result<(), String> {
            Err(format!(
                "ranks: base rank '{name}' {what}; the lowest rank must require nothing"
            ))
        };
        // Exhaustive on purpose: a new qualification field fails to compile
        // here until someone decides whether the base rank may set it.
        let RankQualification {
            structures,
            required_products,
            window,
            tenure,
        } = &base.qualification;
        if !required_products.is_empty() {
            return refuse(format!("requires products {required_products:?}"));
        }
        if window.is_some() {
            return refuse("has a window".to_string());
        }
        if tenure.is_some() {
            return refuse("has a tenure requirement".to_string());
        }
        for StructureQualification {
            structure,
            personal_volume,
            group_volume,
            max_group_volume_per_leg: _,
            max_group_volume_per_leg_fraction: _,
            min_retail_volume,
            distributor_count,
            leg_quality,
        } in structures
        {
            for (field, value) in [
                ("personal_volume", personal_volume),
                ("group_volume", group_volume),
                ("min_retail_volume", min_retail_volume),
            ] {
                if !value.is_zero() {
                    return refuse(format!("sets {field} {value} on structure '{structure}'"));
                }
            }
            if distributor_count.is_some() {
                return refuse(format!("sets distributor_count on structure '{structure}'"));
            }
            if !leg_quality.is_empty() {
                return refuse(format!("sets leg_quality on structure '{structure}'"));
            }
        }
        Ok(())
    }

    /// Reject a per-leg cap or cap fraction out of range, or both caps set, on any rank.
    fn check_per_leg_caps(&self) -> Result<(), String> {
        for rank in &self.ranks {
            for sq in &rank.qualification.structures {
                let at = format!("ranks: rank '{}' structure '{}'", rank.name, sq.structure);
                check_non_negative(
                    &format!("{at} max_group_volume_per_leg"),
                    sq.max_group_volume_per_leg,
                )?;
                check_fraction(
                    &format!("{at} max_group_volume_per_leg_fraction"),
                    sq.max_group_volume_per_leg_fraction,
                )?;
                if sq.max_group_volume_per_leg > AuthoredDec::ZERO
                    && sq.max_group_volume_per_leg_fraction > AuthoredDec::ZERO
                {
                    return Err(format!(
                        "{at} sets both max_group_volume_per_leg and max_group_volume_per_leg_fraction"
                    ));
                }
            }
        }
        Ok(())
    }

    /// Reject two structures that share a name.
    ///
    /// Structure lookup is by name and takes the first match, so duplicate
    /// names make the payout depend on plan ordering. Go already rejects this
    /// in `validateStructureNames` (internal/config/rules.go). The engine
    /// repeats the check because it is its own trust boundary: a direct caller
    /// that skips the Go pipeline would otherwise load an ambiguous plan.
    ///
    /// The rule is uniqueness across the whole plan, not within a structure
    /// type. Two structures named `X`, one unilevel and one streamline, each
    /// resolve through their own lookup helper and would never collide at
    /// calculate time, but Go rejects that pair and the two layers have to
    /// agree on what a valid plan is. Names are compared exactly, also matching
    /// Go.
    ///
    /// Runs before the per-structure loop so that every later error, which
    /// names the structure it came from, points at exactly one structure.
    fn check_unique_structure_names(&self) -> Result<(), String> {
        let mut seen = HashSet::with_capacity(self.structures.len());
        for structure in &self.structures {
            let name = structure.name();
            if !seen.insert(name) {
                return Err(format!("structures: duplicate structure name '{name}'"));
            }
        }
        Ok(())
    }
}

impl StructureConfig {
    fn validate(&mut self) -> Result<(), String> {
        let name = self.name().to_string();
        let result = match self {
            StructureConfig::Unilevel(c) => c.level_commission.validate(),
            StructureConfig::Binary(c) => c.binary_commission.validate(),
            StructureConfig::Matrix(c) => c
                .matrix_params
                .validate()
                .and_then(|()| c.level_commission.validate()),
            StructureConfig::Stairstep(c) => {
                c.level_commission.validate()?;
                if let Some(breakaway) = &c.breakaway {
                    breakaway.validate()?;
                }
                Ok(())
            }
            StructureConfig::Generation(c) => {
                c.generation_commission.validate()?;
                if let Some(level) = &c.level_commission {
                    level.validate()?;
                }
                if c.level_commissions_enabled && c.level_commission.is_none() {
                    Err(
                        "level_commissions_enabled is true and level_commission is null or absent"
                            .to_string(),
                    )
                } else {
                    Ok(())
                }
            }
            StructureConfig::Streamline(c) => c.streamline_commission.validate(),
            StructureConfig::BoardPlan(c) => c.board_cycling.validate(),
        };
        result.map_err(|e| format!("structure '{name}': {e}"))
    }

    fn name(&self) -> &str {
        match self {
            StructureConfig::Unilevel(c) => &c.name,
            StructureConfig::Binary(c) => &c.name,
            StructureConfig::Matrix(c) => &c.name,
            StructureConfig::Stairstep(c) => &c.name,
            StructureConfig::Generation(c) => &c.name,
            StructureConfig::Streamline(c) => &c.name,
            StructureConfig::BoardPlan(c) => &c.name,
        }
    }
}

// --- per-structure commission configs ------------------------------------

impl LevelCommissionConfig {
    fn validate(&self) -> Result<(), String> {
        check_fraction("broad_commission_percent", self.broad_commission_percent)?;
        check_optional_positive(
            "volume_to_dollar_multiplier",
            self.volume_to_dollar_multiplier,
        )?;
        // A commissionable depth of 0 loads but silently pays no level
        // commissions — a config error the gate should catch (HEU-517 review).
        if self.max_depth < 1 {
            return Err(format!(
                "commissionable_depth must be >= 1, got {}",
                self.max_depth
            ));
        }
        for (rank, levels) in &self.rate_table {
            for (level, rate) in levels {
                check_fraction(&format!("rate_table[{rank}][{level}]"), *rate)?;
            }
        }
        Ok(())
    }
}

impl MatrixStructureParams {
    fn validate(&self) -> Result<(), String> {
        // Mirror `MatrixTree::new`: width < 2 is a streamline, not a matrix, and
        // depth-first spillover has no engine implementation yet. Rejecting here
        // turns a would-be tree-construction failure into a load-time rejection
        // (the engine half of HEU-535).
        if self.width < 2 {
            return Err(format!("matrix width must be >= 2, got {}", self.width));
        }
        if matches!(self.spillover, SpilloverDirection::DepthFirst) {
            return Err(
                "matrix spillover_direction 'depth_first' is not supported by the engine"
                    .to_string(),
            );
        }
        Ok(())
    }
}

impl BinaryCommissionConfig {
    fn validate(&mut self) -> Result<(), String> {
        check_optional_positive(
            "volume_to_dollar_multiplier",
            self.volume_to_dollar_multiplier,
        )?;
        match &mut self.mode {
            BinaryCommissionMode::Pairing(pairing) => pairing.validate(),
            // Reuse the existing calculate-time validator (config/binary.rs). It
            // also normalizes by sorting steps, which is why the chain is `&mut`.
            BinaryCommissionMode::CycleStep(cycle) => cycle.validate(),
        }
    }
}

impl PairingConfig {
    fn validate(&self) -> Result<(), String> {
        check_fraction("pairing percent", self.percent)?;
        check_optional_non_negative("cap_per_period", self.cap_per_period)?;
        check_optional_non_negative("carry_forward_cap", self.carry_forward_cap)?;
        Ok(())
    }
}

impl StreamlineCommissionConfig {
    fn validate(&self) -> Result<(), String> {
        check_optional_positive(
            "volume_to_dollar_multiplier",
            self.volume_to_dollar_multiplier,
        )?;
        // A commissionable depth of 0 loads but silently pays nothing — reject
        // it at the boundary like the level-commission depth (HEU-517 review).
        if self.max_depth < 1 {
            return Err(format!(
                "commissionable_depth must be >= 1, got {}",
                self.max_depth
            ));
        }
        // An empty table pays nothing at any level, which is a config error
        // rather than a valid plan.
        if self.levels.is_empty() {
            return Err("dynamic_compression must have at least one level".to_string());
        }
        // The threshold vector is indexed by declared level and the rate table
        // is keyed by the declared level, so the two agree only when the
        // declared levels are a contiguous ascending run starting at 1. This
        // gate does not trust that the caller checked it (HEU-517).
        //
        // The expected value is computed in usize so it cannot overflow: a
        // table longer than 255 entries must reject at position 255, not wrap.
        // The message names the offending entry rather than echoing the table,
        // which is attacker-sized here.
        //
        // A duplicate level fails this check at its second copy.
        for (idx, level) in self.levels.iter().enumerate() {
            if usize::from(level.level) != idx + 1 {
                return Err(format!(
                    "dynamic_compression levels must be a contiguous ascending run \
                     starting at 1, got {} at position {} (table has {} entries)",
                    level.level,
                    idx,
                    self.levels.len()
                ));
            }
        }
        // A depth below the level count can never reach the deepest declared
        // levels.
        if usize::from(self.max_depth) < self.levels.len() {
            return Err(format!(
                "commissionable_depth ({}) must be >= number of levels ({})",
                self.max_depth,
                self.levels.len()
            ));
        }
        // dynamic_compression percents scale CV directly (the streamline
        // dollar-value law, HEU-530), so each is a fraction in [0, 1].
        for level in &self.levels {
            check_fraction(
                &format!("dynamic_compression level {} percent", level.level),
                level.percent,
            )?;
        }
        Ok(())
    }
}

impl GenerationCommissionConfig {
    fn validate(&self) -> Result<(), String> {
        if self.max_generations < 1 {
            return Err(format!(
                "max_generations must be >= 1, got {}",
                self.max_generations
            ));
        }
        check_optional_positive(
            "volume_to_dollar_multiplier",
            self.volume_to_dollar_multiplier,
        )?;
        for (generation, rate) in &self.rates {
            check_fraction(&format!("generation_rates[{generation}]"), *rate)?;
        }
        Ok(())
    }
}

impl BreakawayConfig {
    fn validate(&self) -> Result<(), String> {
        match &self.overrides {
            OverrideStrategy::SingleWalk {
                mode,
                generation_overrides,
            } => {
                match mode {
                    OverrideMode::Differential(diff) => {
                        for (rank, rate) in &diff.rank_rates {
                            check_fraction(&format!("differential rank_rates[{rank}]"), *rate)?;
                        }
                        check_min_override(&diff.min_override)?;
                    }
                    OverrideMode::FixedOverride(fixed) => {
                        for (rank, rate) in &fixed.rank_rates {
                            check_fraction(&format!("fixed_override rank_rates[{rank}]"), *rate)?;
                        }
                    }
                }
                if let Some(gen_overrides) = generation_overrides {
                    if gen_overrides.max_generations < 1 {
                        return Err(format!(
                            "breakaway max_generations must be >= 1, got {}",
                            gen_overrides.max_generations
                        ));
                    }
                    for (generation, rate) in &gen_overrides.rates {
                        check_fraction(
                            &format!("breakaway generation_rates[{generation}]"),
                            *rate,
                        )?;
                    }
                }
                Ok(())
            }
            OverrideStrategy::MultiTier(multi) => {
                // A load-time bound of 255 makes the `u8::try_from(tier_index +
                // 1)` in `walk_multi_tier_overrides` unreachable (it would
                // otherwise panic on the 256th tier).
                if multi.tiers.is_empty() {
                    return Err("multi_tier breakaway must have at least one tier".to_string());
                }
                if multi.tiers.len() > 255 {
                    return Err(format!(
                        "multi_tier breakaway supports at most 255 tiers, got {}",
                        multi.tiers.len()
                    ));
                }
                for (index, tier) in multi.tiers.iter().enumerate() {
                    check_fraction(&format!("multi_tier tier {} rate", index + 1), tier.rate)?;
                }
                Ok(())
            }
        }
    }
}

impl BoardPlanConfig {
    fn validate(&self) -> Result<(), String> {
        // A cycle pays this fixed amount on cycle-out (the board dollar law,
        // HEU-530); a negative value would mint or corrupt money.
        check_non_negative("cycle_commission", self.cycle_commission)
    }
}

impl VolumeConfig {
    fn validate(&self) -> Result<(), String> {
        check_positive(
            "volume_to_dollar_multiplier",
            self.volume_to_dollar_multiplier,
        )
    }
}

impl CapsConfig {
    fn validate(&self) -> Result<(), String> {
        check_fraction(
            "company_payout_cap_percent",
            self.company_payout_cap_percent,
        )?;
        check_optional_non_negative("per_distributor cap", self.per_distributor_cap)?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::d;
    use std::collections::BTreeMap;
    use std::num::NonZeroU32;

    use crate::config::binary::{PairingCalculation, VolumeAfterPayout};
    use crate::config::board_plan::ReEntryPosition;
    use crate::config::eligibility::CommissionEligibility;
    use crate::config::generation::GenerationBoundaryMode;
    use crate::config::matrix::SpilloverDirection;
    use crate::config::payout::CapEnforcement;
    use crate::config::stairstep::{
        BreakawayGenerationConfig, BreakawayTier, FixedOverrideConfig, MultiTierConfig,
    };
    use crate::config::streamline::StreamlineLevel;
    use crate::config::{
        GenerationStructureConfig, StreamlineStructureConfig, UnilevelStructureConfig,
    };
    use crate::test_support::build_test_plan;

    // --- numeric helpers ---

    #[test]
    fn fraction_accepts_bounds_and_rejects_out_of_range() {
        assert!(check_fraction("f", d!(0.0)).is_ok());
        assert!(check_fraction("f", d!(1.0)).is_ok());
        assert!(check_fraction("f", d!(0.5)).is_ok());
        assert!(check_fraction("f", d!(-0.01)).is_err());
        assert!(check_fraction("f", d!(1.01)).is_err());
    }

    #[test]
    fn positive_rejects_zero_and_negative() {
        assert!(check_positive("m", d!(0.001)).is_ok());
        assert!(check_positive("m", d!(0.0)).is_err());
        assert!(check_positive("m", d!(-1.0)).is_err());
    }

    #[test]
    fn non_negative_allows_zero_but_not_negative() {
        assert!(check_min_override(&MinOverride::Currency { value: d!(250.0) }).is_ok());
        assert!(check_min_override(&MinOverride::Rate { value: d!(1.5) }).is_err());
        assert!(check_min_override(&MinOverride::Currency { value: d!(-1.0) }).is_err());
        assert!(check_min_override(&MinOverride::Rate { value: d!(0.02) }).is_ok());
        assert!(check_non_negative("c", d!(0.0)).is_ok());
        assert!(check_non_negative("c", d!(10.0)).is_ok());
        assert!(check_non_negative("c", d!(-0.01)).is_err());
    }

    #[test]
    fn optional_positive_treats_none_as_absent() {
        // The critical null-multiplier rule: None must pass.
        assert!(check_optional_positive("m", None).is_ok());
        assert!(check_optional_positive("m", Some(d!(1.0))).is_ok());
        assert!(check_optional_positive("m", Some(d!(0.0))).is_err());
    }

    // --- representative type validators ---

    fn level_config(
        broad: AuthoredDec,
        multiplier: Option<AuthoredDec>,
        rate: AuthoredDec,
    ) -> LevelCommissionConfig {
        let mut inner = BTreeMap::new();
        inner.insert(1u8, rate);
        let mut rate_table = BTreeMap::new();
        rate_table.insert("associate".to_string(), inner);
        LevelCommissionConfig {
            broad_commission_percent: broad,
            volume_to_dollar_multiplier: multiplier,
            max_depth: 3,
            rate_table,
        }
    }

    #[test]
    fn level_commission_happy_path() {
        assert!(level_config(d!(0.40), None, d!(0.05)).validate().is_ok());
        assert!(
            level_config(d!(0.40), Some(d!(1.0)), d!(0.05))
                .validate()
                .is_ok()
        );
    }

    #[test]
    fn level_commission_rejects_bad_broad_percent() {
        assert!(level_config(d!(1.5), None, d!(0.05)).validate().is_err());
    }

    #[test]
    fn level_commission_rejects_bad_rate_table_value() {
        assert!(level_config(d!(0.40), None, d!(1.5)).validate().is_err());
    }

    #[test]
    fn level_commission_rejects_zero_multiplier() {
        assert!(
            level_config(d!(0.40), Some(d!(0.0)), d!(0.05))
                .validate()
                .is_err()
        );
    }

    #[test]
    fn level_commission_rejects_zero_depth() {
        let mut config = level_config(d!(0.40), None, d!(0.05));
        config.max_depth = 0;
        assert!(config.validate().is_err());
    }

    fn generation_config(max_generations: u8) -> GenerationCommissionConfig {
        GenerationCommissionConfig {
            max_generations,
            max_generations_per_rank: BTreeMap::new(),
            rates: BTreeMap::from([(1u8, d!(0.10))]),
            boundary_mode: GenerationBoundaryMode::ThresholdRank,
            boundary_rank: "Silver".to_string(),
            empty_generation_consumes_number: false,
            volume_to_dollar_multiplier: None,
            ineligible_creates_boundary: true,
        }
    }

    #[test]
    fn generation_rejects_default_max_generations_of_zero() {
        // HEU-442: a default max_generations of 0 excludes every earner — reject
        // it (mirrors level_commission_rejects_zero_depth). This closes the
        // direct-engine bypass the Go check alone leaves open. Per-rank overrides
        // of 0 remain allowed.
        assert!(generation_config(0).validate().is_err());
        assert!(generation_config(1).validate().is_ok());

        let mut per_rank_zero = generation_config(3);
        per_rank_zero.max_generations_per_rank = BTreeMap::from([("Silver".to_string(), 0u8)]);
        assert!(per_rank_zero.validate().is_ok());
    }

    fn single_walk_generation_breakaway(max_generations: u8) -> BreakawayConfig {
        BreakawayConfig {
            threshold_rank: "gold".to_string(),
            exclude_breakaway_gv: false,
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::FixedOverride(FixedOverrideConfig {
                    rank_rates: BTreeMap::new(),
                }),
                generation_overrides: Some(BreakawayGenerationConfig {
                    max_generations,
                    rates: BTreeMap::new(),
                    boundary_rank: "gold".to_string(),
                }),
            },
        }
    }

    #[test]
    fn breakaway_generation_rejects_max_generations_of_zero() {
        // A breakaway single_walk generation override with max_generations = 0
        // excludes every earner in the override walk — the same zero-depth trap
        // GenerationCommissionConfig guards. Close the direct-engine bypass for
        // the breakaway sibling too (HEU-513 final review).
        assert!(single_walk_generation_breakaway(0).validate().is_err());
        assert!(single_walk_generation_breakaway(1).validate().is_ok());
    }

    #[test]
    fn matrix_params_enforces_width_and_spillover() {
        let ok = MatrixStructureParams {
            width: 3,
            height: 5,
            spillover: SpilloverDirection::BreadthFirst,
        };
        assert!(ok.validate().is_ok());

        let narrow = MatrixStructureParams {
            width: 1,
            ..ok.clone()
        };
        assert!(narrow.validate().is_err());

        let depth_first = MatrixStructureParams {
            spillover: SpilloverDirection::DepthFirst,
            ..ok
        };
        assert!(depth_first.validate().is_err());
    }

    #[test]
    fn streamline_percent_is_a_fraction() {
        let mut config = StreamlineCommissionConfig {
            volume_to_dollar_multiplier: None,
            max_depth: 3,
            levels: vec![StreamlineLevel {
                level: 1,
                min_rank: "associate".to_string(),
                percent: d!(0.1),
            }],
            stream_config: None,
        };
        assert!(config.validate().is_ok());

        config.levels[0].percent = d!(5.0); // whole-number percent — rejected
        assert!(config.validate().is_err());
    }

    /// Builds a streamline config with the given declared levels. Every entry
    /// uses the same rank and percent, so only the level numbers vary.
    fn streamline_with_levels(levels: &[u8], max_depth: u8) -> StreamlineCommissionConfig {
        StreamlineCommissionConfig {
            volume_to_dollar_multiplier: None,
            max_depth,
            levels: levels
                .iter()
                .map(|l| StreamlineLevel {
                    level: *l,
                    min_rank: "associate".to_string(),
                    percent: d!(0.1),
                })
                .collect(),
            stream_config: None,
        }
    }

    #[test]
    fn streamline_rejects_empty_compression_table() {
        // An empty table pays nothing at any level, which is a config error.
        let config = streamline_with_levels(&[], 3);
        let err = config.validate().unwrap_err();
        assert!(
            err.contains("at least one level"),
            "unexpected message: {err}"
        );
    }

    #[test]
    fn streamline_rejects_non_contiguous_levels() {
        // One check catches all three malformed shapes: out of order fails at
        // the first transposed entry, a gap at the entry after it, and a
        // duplicate at the second copy.
        let err = streamline_with_levels(&[2, 1], 5).validate().unwrap_err();
        assert!(
            err.contains("contiguous ascending run"),
            "a different rule fired: {err}"
        );
        assert!(streamline_with_levels(&[1, 3], 5).validate().is_err());
        assert!(streamline_with_levels(&[1, 1], 5).validate().is_err());
        assert!(streamline_with_levels(&[2, 3], 5).validate().is_err());

        // The contiguous run is still accepted.
        assert!(streamline_with_levels(&[1, 2, 3], 5).validate().is_ok());
    }

    #[test]
    fn streamline_rejects_depth_below_level_count() {
        let err = streamline_with_levels(&[1, 2], 1).validate().unwrap_err();
        assert!(
            err.contains("commissionable_depth"),
            "unexpected message: {err}"
        );

        // Depth equal to the level count is fine.
        assert!(streamline_with_levels(&[1, 2], 2).validate().is_ok());
    }

    #[test]
    fn streamline_error_message_is_bounded() {
        // 255 valid sequential entries followed by a 256th. The mismatch lands
        // at position 255, where the expected value is 256 and no u8 can match
        // it. That pins the late mismatch rather than a first-entry bail, and
        // the bounded message on a long table. It does not distinguish usize
        // from the u16 form: both compare 255 against 256 and error. What it
        // does rule out is a naive u8 expected value, which would overflow on
        // 255 + 1 before ever reaching the comparison.
        //
        // The contiguity check runs before the depth check, so depth 255
        // against 256 entries is fine here: contiguity rejects first.
        let mut levels: Vec<u8> = (1..=255u8).collect();
        levels.push(255);
        let err = streamline_with_levels(&levels, 255).validate().unwrap_err();

        assert!(
            err.contains("position 255") && err.contains("256 entries"),
            "should reject at the 256th entry: {err}"
        );
        assert!(
            err.len() < 200,
            "message should stay short, got {} chars: {err}",
            err.len()
        );
    }

    #[test]
    fn streamline_rejects_zero_depth() {
        let config = StreamlineCommissionConfig {
            volume_to_dollar_multiplier: None,
            max_depth: 0,
            levels: vec![],
            stream_config: None,
        };
        assert!(config.validate().is_err());
    }

    fn multi_tier_breakaway(tiers: Vec<BreakawayTier>) -> BreakawayConfig {
        BreakawayConfig {
            threshold_rank: "gold".to_string(),
            exclude_breakaway_gv: false,
            overrides: OverrideStrategy::MultiTier(MultiTierConfig { tiers }),
        }
    }

    #[test]
    fn multi_tier_rejects_empty() {
        assert!(multi_tier_breakaway(vec![]).validate().is_err());
    }

    #[test]
    fn multi_tier_rejects_more_than_255_tiers() {
        let tiers: Vec<BreakawayTier> = (0..256)
            .map(|_| BreakawayTier {
                min_split_out_groups: 1,
                rate: d!(0.05),
            })
            .collect();
        assert!(multi_tier_breakaway(tiers).validate().is_err());
    }

    #[test]
    fn multi_tier_accepts_exactly_255_tiers() {
        let tiers: Vec<BreakawayTier> = (0..255)
            .map(|_| BreakawayTier {
                min_split_out_groups: 1,
                rate: d!(0.05),
            })
            .collect();
        assert!(multi_tier_breakaway(tiers).validate().is_ok());
    }

    #[test]
    fn multi_tier_rejects_bad_tier_rate() {
        let tiers = vec![BreakawayTier {
            min_split_out_groups: 1,
            rate: d!(2.0),
        }];
        assert!(multi_tier_breakaway(tiers).validate().is_err());
    }

    #[test]
    fn volume_multiplier_must_be_positive() {
        let mut v = VolumeConfig {
            inhibit_signup_volume: false,
            base_currency: "USD".to_string(),
            volume_to_dollar_multiplier: d!(1.0),
            deduct_qualifying_volume: false,
        };
        assert!(v.validate().is_ok());
        v.volume_to_dollar_multiplier = d!(0.0);
        assert!(v.validate().is_err());
    }

    #[test]
    fn caps_percent_is_a_fraction() {
        let mut caps = CapsConfig {
            per_distributor_cap: None,
            company_payout_cap_percent: d!(0.42),
            enforcement: CapEnforcement::ProRata,
            enable_clawback: false,
        };
        assert!(caps.validate().is_ok());
        caps.company_payout_cap_percent = d!(1.5);
        assert!(caps.validate().is_err());
    }

    #[test]
    fn pairing_percent_is_a_fraction() {
        let ok = PairingConfig {
            percent: d!(0.10),
            calculation: PairingCalculation::WeakerLeg,
            cap_per_period: Some(d!(1000.0)),
            volume_after_payout: VolumeAfterPayout::CarryForward,
            carry_forward_cap: None,
            multi_position_cap_mode: Default::default(),
        };
        assert!(ok.validate().is_ok());

        let bad = PairingConfig {
            percent: d!(1.2),
            ..ok
        };
        assert!(bad.validate().is_err());
    }

    #[test]
    fn board_cycle_commission_must_be_non_negative() {
        let ok = BoardPlanConfig {
            cycle_commission: d!(500.0),
            re_entry_enabled: true,
            re_entry_position: ReEntryPosition::Bottom,
            max_cycles_per_period: 3,
            max_cascade_depth: NonZeroU32::new(10).unwrap(),
            stall_threshold_periods: 2,
            inactive_compression: false,
        };
        assert!(ok.validate().is_ok());

        let negative = BoardPlanConfig {
            cycle_commission: d!(-1.0),
            ..ok
        };
        assert!(negative.validate().is_err());
    }

    // --- plan-level rules ---

    fn test_eligibility() -> CommissionEligibility {
        CommissionEligibility {
            minimum_pv: d!(100.0),
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![],
        }
    }

    fn named_streamline(name: &str) -> StructureConfig {
        StructureConfig::Streamline(StreamlineStructureConfig {
            name: name.to_string(),
            streamline_commission: StreamlineCommissionConfig {
                volume_to_dollar_multiplier: None,
                max_depth: 3,
                levels: vec![StreamlineLevel {
                    level: 1,
                    min_rank: "associate".to_string(),
                    percent: d!(0.1),
                }],
                stream_config: None,
            },
        })
    }

    fn named_unilevel(name: &str) -> StructureConfig {
        StructureConfig::Unilevel(UnilevelStructureConfig {
            name: name.to_string(),
            level_commission: level_config(d!(0.40), None, d!(0.05)),
            compression: None,
            pass_up: None,
        })
    }

    /// Build a plan whose structures are exactly `structures`.
    fn plan_with_structures(structures: Vec<StructureConfig>) -> CompensationPlan {
        let mut plan = build_test_plan(test_eligibility(), named_streamline("seed"), "seed");
        plan.structures = structures;
        plan
    }

    fn named_generation(
        name: &str,
        enabled: bool,
        level: Option<LevelCommissionConfig>,
    ) -> StructureConfig {
        StructureConfig::Generation(GenerationStructureConfig {
            name: name.to_string(),
            level_commission: level,
            compression: None,
            generation_commission: generation_config(1),
            level_commissions_enabled: enabled,
        })
    }

    #[test]
    fn plan_rejects_level_commissions_enabled_with_no_level_commission() {
        let mut plan = plan_with_structures(vec![named_generation("gen", true, None)]);
        assert_eq!(
            plan.validate(),
            Err(
                "structure 'gen': level_commissions_enabled is true and level_commission is null or absent"
                    .to_string()
            )
        );
    }

    #[test]
    fn plan_accepts_level_commissions_disabled_with_no_level_commission() {
        let mut plan = plan_with_structures(vec![named_generation("gen", false, None)]);
        assert_eq!(plan.validate(), Ok(()));
    }

    #[test]
    fn plan_accepts_level_commissions_disabled_with_a_level_commission() {
        let mut plan = plan_with_structures(vec![named_generation(
            "gen",
            false,
            Some(level_config(d!(0.40), None, d!(0.05))),
        )]);
        assert_eq!(plan.validate(), Ok(()));
    }

    #[test]
    fn plan_rejects_an_invalid_level_commission_even_when_disabled() {
        let mut plan = plan_with_structures(vec![named_generation(
            "gen",
            false,
            Some(level_config(d!(1.5), None, d!(0.05))),
        )]);
        assert!(plan.validate().is_err());
    }

    #[test]
    fn plan_accepts_level_commissions_enabled_with_a_level_commission() {
        let mut plan = plan_with_structures(vec![named_generation(
            "gen",
            true,
            Some(level_config(d!(0.40), None, d!(0.05))),
        )]);
        assert_eq!(plan.validate(), Ok(()));
    }

    #[test]
    fn plan_rejects_an_empty_rank_ladder() {
        // Every calculator now validates snapshot ranks against this ladder,
        // so an empty one rejects every calculation instead of loading badly
        // once. The schema says minItems 1; the engine is its own trust
        // boundary and has to say so too. HEU-608.
        let mut plan = plan_with_structures(vec![named_streamline("main")]);
        plan.ranks = vec![];
        assert!(plan.validate().is_err());
    }

    /// A two-rank ladder whose second entry is the one a test breaks.
    ///
    /// The offender is second so an assertion on the reported index fails if
    /// the index is hardcoded or off by one.
    fn plan_with_two_ranks() -> CompensationPlan {
        let mut plan = plan_with_structures(vec![named_streamline("main")]);
        let mut gold = plan.ranks[0].clone();
        gold.name = "gold".to_string();
        gold.ordinal = 2;
        plan.ranks.push(gold);
        plan
    }

    #[test]
    fn plan_rejects_a_rank_with_ordinal_zero() {
        let mut plan = plan_with_two_ranks();
        plan.ranks[1].ordinal = 0;
        let err = plan.validate().unwrap_err();
        assert!(err.contains("index 1"), "unexpected error: {err}");
        assert!(err.contains("'gold'"), "unexpected error: {err}");
        assert!(err.contains("ordinal 0"), "unexpected error: {err}");
    }

    #[test]
    fn plan_rejects_a_rank_with_an_empty_name() {
        let mut plan = plan_with_two_ranks();
        plan.ranks[1].name.clear();
        let err = plan.validate().unwrap_err();
        assert!(err.contains("index 1"), "unexpected error: {err}");
        assert!(err.contains("empty name"), "unexpected error: {err}");
    }

    #[test]
    fn plan_rejects_duplicate_structure_names() {
        // Structure lookup is by name and takes the first match, so two
        // structures sharing a name make the payout depend on plan ordering.
        // Go rejects this (validateStructureNames in internal/config/rules.go);
        // the engine has to reject it too, or a direct engine caller that skips
        // the Go pipeline loads an ambiguous plan.
        let mut plan = plan_with_structures(vec![named_streamline("main"), named_unilevel("main")]);
        assert!(plan.validate().is_err());
    }

    #[test]
    fn plan_accepts_distinct_structure_names() {
        let mut plan =
            plan_with_structures(vec![named_streamline("stream"), named_unilevel("uni")]);
        assert!(plan.validate().is_ok());
    }

    // --- base rank ---

    /// A structure entry that sets no threshold.
    fn zero_threshold(structure: &str) -> crate::config::rank::StructureQualification {
        crate::config::rank::StructureQualification {
            structure: structure.to_string(),
            personal_volume: d!(0.0),
            group_volume: d!(0.0),
            max_group_volume_per_leg: d!(0.0),
            max_group_volume_per_leg_fraction: d!(0.0),
            min_retail_volume: d!(0.0),
            distributor_count: None,
            leg_quality: vec![],
        }
    }

    #[test]
    fn plan_accepts_a_base_rank_with_no_structure_entries() {
        let mut plan = plan_with_two_ranks();
        assert!(plan.validate().is_ok());
    }

    #[test]
    fn plan_accepts_a_base_rank_whose_structure_entries_set_no_threshold() {
        let mut plan = plan_with_two_ranks();
        plan.ranks[0].qualification.structures = vec![zero_threshold("main")];
        assert!(plan.validate().is_ok());
    }

    #[test]
    fn plan_accepts_a_per_leg_cap_on_the_base_rank() {
        let mut plan = plan_with_two_ranks();
        let mut entry = zero_threshold("main");
        entry.max_group_volume_per_leg = d!(500.0);
        plan.ranks[0].qualification.structures = vec![entry];
        assert!(plan.validate().is_ok());
    }

    #[test]
    fn plan_rejects_each_base_rank_requirement() {
        use crate::config::rank::{
            DistributorCountRequirement, LegPredicate, LegQualityRequirement, RankDefinition,
            RankQualificationWindow, SearchMode, TenureRequirement,
        };
        type Mutate = fn(&mut RankDefinition);
        let cases: Vec<(Mutate, &str)> = vec![
            (
                |r| r.qualification.structures[0].personal_volume = d!(50.0),
                "sets personal_volume 50 on structure 'main'",
            ),
            (
                |r| r.qualification.structures[0].group_volume = d!(1000.0),
                "sets group_volume 1000 on structure 'main'",
            ),
            (
                |r| r.qualification.structures[0].min_retail_volume = d!(10.0),
                "sets min_retail_volume 10 on structure 'main'",
            ),
            (
                |r| {
                    r.qualification.structures[0].distributor_count =
                        Some(DistributorCountRequirement {
                            count: 1,
                            min_rank: "associate".to_string(),
                            search_mode: SearchMode::AnyLevel,
                            search_depth: None,
                            total_count: 1,
                            min_leg_group_volume: d!(0.0),
                        })
                },
                "sets distributor_count on structure 'main'",
            ),
            (
                |r| {
                    r.qualification.structures[0].leg_quality = vec![LegQualityRequirement {
                        count: 1,
                        predicate: LegPredicate::ContainsPersonalVolume {
                            min_personal_volume: d!(100.0),
                        },
                    }]
                },
                "sets leg_quality on structure 'main'",
            ),
            (
                |r| r.qualification.required_products = vec!["starter-kit".to_string()],
                "requires products",
            ),
            (
                |r| {
                    r.qualification.window = Some(RankQualificationWindow {
                        threshold_rank: "associate".to_string(),
                        qualifying_periods: 1,
                        window_periods: 1,
                    })
                },
                "has a window",
            ),
            (
                |r| {
                    r.qualification.tenure = Some(TenureRequirement {
                        threshold_rank: "associate".to_string(),
                        periods: 1,
                    })
                },
                "has a tenure requirement",
            ),
        ];
        for (mutate, expected) in cases {
            let mut plan = plan_with_two_ranks();
            plan.ranks[0].qualification.structures = vec![zero_threshold("main")];
            mutate(&mut plan.ranks[0]);
            let err = plan.validate().unwrap_err();
            assert!(
                err.contains("base rank 'associate'"),
                "unexpected error: {err}"
            );
            assert!(err.contains(expected), "expected {expected:?}, got: {err}");
        }
    }

    #[test]
    fn plan_finds_the_base_rank_by_ordinal_not_position() {
        let mut plan = plan_with_two_ranks();
        plan.ranks.swap(0, 1);
        let mut entry = zero_threshold("main");
        entry.personal_volume = d!(50.0);
        plan.ranks[1].qualification.structures = vec![entry];
        let err = plan.validate().unwrap_err();
        assert!(
            err.contains("base rank 'associate'"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn plan_takes_the_first_of_tied_lowest_ordinals_as_the_base() {
        let mut entry = zero_threshold("main");
        entry.personal_volume = d!(50.0);

        let mut second_tied = plan_with_two_ranks();
        second_tied.ranks[1].ordinal = 1;
        second_tied.ranks[1].qualification.structures = vec![entry.clone()];
        assert!(second_tied.validate().is_ok());

        let mut first_tied = plan_with_two_ranks();
        first_tied.ranks[1].ordinal = 1;
        first_tied.ranks[0].qualification.structures = vec![entry];
        let err = first_tied.validate().unwrap_err();
        assert!(
            err.contains("base rank 'associate'"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn plan_rejects_a_requirement_on_any_base_rank_structure_entry() {
        let mut plan = plan_with_two_ranks();
        let mut second = zero_threshold("second");
        second.personal_volume = d!(5.0);
        plan.ranks[0].qualification.structures = vec![zero_threshold("main"), second];
        let err = plan.validate().unwrap_err();
        assert!(
            err.contains("sets personal_volume 5 on structure 'second'"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn plan_accepts_a_requirement_on_a_rank_above_the_base() {
        let mut plan = plan_with_two_ranks();
        plan.ranks.swap(0, 1);
        let mut entry = zero_threshold("main");
        entry.personal_volume = d!(100.0);
        plan.ranks[0].qualification.structures = vec![entry];
        assert!(plan.validate().is_ok());
    }

    // --- per-leg cap ---

    #[test]
    fn plan_rejects_a_per_leg_cap_that_is_negative() {
        for (value, shown) in [(d!(-1.0), "-1"), (d!(-0.01), "-0.01")] {
            let mut plan = plan_with_two_ranks();
            let mut entry = zero_threshold("main");
            entry.max_group_volume_per_leg = value;
            plan.ranks[1].qualification.structures = vec![entry];
            let err = plan.validate().unwrap_err();
            let want = format!(
                "rank 'gold' structure 'main' max_group_volume_per_leg must be non-negative, got {shown}"
            );
            assert!(err.contains(&want), "expected {want:?}, got: {err}");
        }
    }

    #[test]
    fn plan_rejects_a_negative_per_leg_cap_on_a_later_structure_entry() {
        let mut plan = plan_with_two_ranks();
        let mut second = zero_threshold("second");
        second.max_group_volume_per_leg = d!(-1.0);
        plan.ranks[1].qualification.structures = vec![zero_threshold("main"), second];
        let err = plan.validate().unwrap_err();
        assert!(
            err.contains("rank 'gold' structure 'second' max_group_volume_per_leg must be non-negative, got -1"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn plan_rejects_a_negative_per_leg_cap_on_the_base_rank() {
        let mut plan = plan_with_two_ranks();
        let mut entry = zero_threshold("main");
        entry.max_group_volume_per_leg = d!(-1.0);
        plan.ranks[0].qualification.structures = vec![entry];
        let err = plan.validate().unwrap_err();
        assert!(
            err.contains("rank 'associate' structure 'main' max_group_volume_per_leg must be non-negative, got -1"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn plan_rejects_a_per_leg_cap_fraction_outside_zero_to_one() {
        for (value, shown) in [(d!(-0.1), "-0.1"), (d!(1.1), "1.1")] {
            let mut plan = plan_with_two_ranks();
            let mut entry = zero_threshold("main");
            entry.max_group_volume_per_leg_fraction = value;
            plan.ranks[1].qualification.structures = vec![entry];
            let err = plan.validate().unwrap_err();
            let want = format!(
                "rank 'gold' structure 'main' max_group_volume_per_leg_fraction must be a fraction in [0.0, 1.0], got {shown}"
            );
            assert!(err.contains(&want), "expected {want:?}, got: {err}");
        }
    }

    #[test]
    fn plan_accepts_a_per_leg_cap_fraction_from_zero_to_one() {
        for value in [d!(0.0), d!(0.6), d!(1.0)] {
            let mut plan = plan_with_two_ranks();
            let mut entry = zero_threshold("main");
            entry.max_group_volume_per_leg_fraction = value;
            plan.ranks[1].qualification.structures = vec![entry];
            assert!(plan.validate().is_ok(), "fraction {value} was refused");
        }
    }

    #[test]
    fn plan_accepts_a_per_leg_cap_fraction_on_the_base_rank() {
        let mut plan = plan_with_two_ranks();
        let mut entry = zero_threshold("main");
        entry.max_group_volume_per_leg_fraction = d!(0.5);
        plan.ranks[0].qualification.structures = vec![entry];
        assert!(plan.validate().is_ok());
    }

    #[test]
    fn plan_rejects_both_per_leg_caps_set() {
        let mut plan = plan_with_two_ranks();
        let mut entry = zero_threshold("main");
        entry.max_group_volume_per_leg = d!(6000.0);
        entry.max_group_volume_per_leg_fraction = d!(0.6);
        plan.ranks[1].qualification.structures = vec![entry];
        let err = plan.validate().unwrap_err();
        assert!(
            err.contains("rank 'gold' structure 'main' sets both max_group_volume_per_leg and max_group_volume_per_leg_fraction"),
            "unexpected error: {err}"
        );
    }
}
