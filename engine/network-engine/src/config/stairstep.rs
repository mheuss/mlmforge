//! Stairstep breakaway commission types.
//!
//! In a stairstep plan, downline groups "break away" when their leader
//! reaches a threshold rank. The upline stops earning level commissions
//! on individual orders and instead earns differential overrides on
//! total group volume. Optional generation overrides pay on breakaway
//! groups beyond the first.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

/// Breakaway configuration for a stairstep compensation plan.
///
/// When a downline leader reaches `threshold_rank`, their group breaks
/// away. The upline stops earning level commissions on individual orders
/// in that group and earns differential overrides on total group volume
/// instead.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BreakawayConfig {
    /// Rank at which a downline group breaks away.
    ///
    /// When a leader reaches this rank, their entire group detaches
    /// from the upline's level commissions.
    pub threshold_rank: String,

    /// When true, breakaway group volume is excluded from upline's
    /// group volume for rank qualification.
    pub exclude_breakaway_gv: bool,

    /// How override commissions are calculated after breakaway.
    pub override_calculation: OverrideCalculation,

    /// Differential override settings.
    ///
    /// Required when `override_calculation` is `Differential`. Ignored
    /// for `FixedOverride`.
    pub differential: Option<DifferentialConfig>,

    /// Optional multi-generation overrides on breakaway groups beyond
    /// the first.
    pub generation_overrides: Option<BreakawayGenerationConfig>,
}

/// How override commissions are calculated after breakaway.
///
/// Two mutually exclusive approaches. Differential derives the override
/// from rate differences between ranks. Fixed override uses a flat
/// percentage per rank.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OverrideCalculation {
    /// Override = sponsor's rank rate minus breakaway leader's rank rate.
    ///
    /// If equal rank, override is zero (floored at `min_override`).
    /// Never negative.
    Differential,

    /// Fixed percentage per rank, not derived from rate differences.
    FixedOverride,
}

/// Configuration for differential override calculation.
///
/// Override = sponsor's rank rate minus breakaway leader's rank rate.
/// If equal rank, override is zero. Never negative. The `min_override`
/// field provides a floor to prevent zero overrides at equal rank.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DifferentialConfig {
    /// Override rate per rank.
    ///
    /// Override = this rank's rate minus breakaway leader's rate.
    /// Keys are rank names. Values are percentages between 0.0 and 1.0.
    pub rank_rates: BTreeMap<String, f64>,

    /// Floor for override percentage.
    ///
    /// Prevents zero overrides at equal rank. Typical range 0.01-0.03
    /// (1-3%).
    pub min_override: f64,
}

/// Multi-generation override configuration for breakaway groups.
///
/// Shares the generation counting model with standalone generation
/// plans. Each leader at or above `boundary_rank` in the breakaway
/// chain creates a new generation boundary.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BreakawayGenerationConfig {
    /// Maximum number of breakaway generations to pay overrides on.
    ///
    /// Generation 1 is the first breakaway group.
    pub max_generations: u8,

    /// Generation number (1-indexed) to override percentage.
    ///
    /// Missing generation = no override. Keys are generation numbers.
    /// Values are percentages between 0.0 and 1.0.
    pub rates: BTreeMap<u8, f64>,

    /// Rank that creates a generation boundary.
    ///
    /// Each leader at or above this rank in the breakaway chain starts
    /// a new generation.
    pub boundary_rank: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_breakaway_config() {
        let json = r#"{
            "threshold_rank": "director",
            "exclude_breakaway_gv": true,
            "override_calculation": "differential",
            "differential": {
                "rank_rates": {
                    "director": 0.10,
                    "senior_director": 0.15,
                    "executive": 0.20
                },
                "min_override": 0.02
            },
            "generation_overrides": {
                "max_generations": 3,
                "rates": {
                    "1": 0.05,
                    "2": 0.03,
                    "3": 0.01
                },
                "boundary_rank": "director"
            }
        }"#;
        let config: BreakawayConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.threshold_rank, "director");
        assert!(config.exclude_breakaway_gv);
        assert!(matches!(
            config.override_calculation,
            OverrideCalculation::Differential
        ));

        let diff = config.differential.as_ref().unwrap();
        assert_eq!(diff.rank_rates.len(), 3);
        assert_eq!(diff.rank_rates["director"], 0.10);
        assert_eq!(diff.rank_rates["senior_director"], 0.15);
        assert_eq!(diff.rank_rates["executive"], 0.20);
        assert_eq!(diff.min_override, 0.02);

        let gen_cfg = config.generation_overrides.as_ref().unwrap();
        assert_eq!(gen_cfg.max_generations, 3);
        assert_eq!(gen_cfg.rates.len(), 3);
        assert_eq!(gen_cfg.rates[&1], 0.05);
        assert_eq!(gen_cfg.rates[&2], 0.03);
        assert_eq!(gen_cfg.rates[&3], 0.01);
        assert_eq!(gen_cfg.boundary_rank, "director");
    }

    #[test]
    fn deserialize_override_calculation_variants() {
        let json_diff = r#""differential""#;
        let calc: OverrideCalculation = serde_json::from_str(json_diff).unwrap();
        assert!(matches!(calc, OverrideCalculation::Differential));

        let json_fixed = r#""fixed_override""#;
        let calc: OverrideCalculation = serde_json::from_str(json_fixed).unwrap();
        assert!(matches!(calc, OverrideCalculation::FixedOverride));
    }

    #[test]
    fn deserialize_differential_config() {
        let json = r#"{
            "rank_rates": {
                "silver": 0.08,
                "gold": 0.12,
                "platinum": 0.18,
                "diamond": 0.25
            },
            "min_override": 0.01
        }"#;
        let config: DifferentialConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.rank_rates.len(), 4);
        assert_eq!(config.rank_rates["silver"], 0.08);
        assert_eq!(config.rank_rates["gold"], 0.12);
        assert_eq!(config.rank_rates["platinum"], 0.18);
        assert_eq!(config.rank_rates["diamond"], 0.25);
        assert_eq!(config.min_override, 0.01);
    }

    #[test]
    fn deserialize_breakaway_generation_config() {
        let json = r#"{
            "max_generations": 5,
            "rates": {
                "1": 0.06,
                "2": 0.04,
                "3": 0.02,
                "5": 0.01
            },
            "boundary_rank": "senior_director"
        }"#;
        let config: BreakawayGenerationConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.max_generations, 5);
        assert_eq!(config.rates.len(), 4);
        assert_eq!(config.rates[&1], 0.06);
        assert_eq!(config.rates[&2], 0.04);
        assert_eq!(config.rates[&3], 0.02);
        assert_eq!(config.rates[&5], 0.01);
        assert_eq!(config.boundary_rank, "senior_director");
    }

    #[test]
    fn deserialize_breakaway_minimal() {
        let json = r#"{
            "threshold_rank": "director",
            "exclude_breakaway_gv": false,
            "override_calculation": "fixed_override",
            "differential": null,
            "generation_overrides": null
        }"#;
        let config: BreakawayConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.threshold_rank, "director");
        assert!(!config.exclude_breakaway_gv);
        assert!(matches!(
            config.override_calculation,
            OverrideCalculation::FixedOverride
        ));
        assert!(config.differential.is_none());
        assert!(config.generation_overrides.is_none());
    }
}
