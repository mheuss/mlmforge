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
///
/// Wire format keeps `override_calculation` and `differential` as
/// separate fields for backwards compatibility with Go types and the
/// JSON schema. Rust collapses them into `OverrideMode` so invalid
/// states (e.g. Differential without config) are unrepresentable.
#[derive(Debug, Clone, Serialize)]
#[serde(into = "BreakawayConfigWire")]
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
    pub override_mode: OverrideMode,

    /// Optional multi-generation overrides on breakaway groups beyond
    /// the first.
    pub generation_overrides: Option<BreakawayGenerationConfig>,
}

/// How override commissions are calculated after breakaway.
///
/// Differential carries its configuration inline so the type system
/// prevents constructing a Differential mode without the required
/// rank rates and min override.
#[derive(Debug, Clone)]
pub enum OverrideMode {
    /// Override = sponsor's rank rate minus breakaway leader's rank rate.
    ///
    /// If equal rank, override is zero (floored at `min_override`).
    /// Never negative.
    Differential(DifferentialConfig),

    /// Fixed percentage per rank, not derived from rate differences.
    FixedOverride(FixedOverrideConfig),
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

/// Configuration for fixed override calculation.
///
/// Each rank has a flat override percentage applied to breakaway group
/// volume. Unlike Differential, the rate does not depend on the
/// breakaway leader's rank.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FixedOverrideConfig {
    /// Override rate per rank.
    ///
    /// The ancestor's rank determines the override rate directly.
    /// Keys are rank names. Values are percentages between 0.0 and 1.0.
    pub rank_rates: BTreeMap<String, f64>,
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
    #[serde(rename = "generation_rates")]
    pub rates: BTreeMap<u8, f64>,

    /// Rank that creates a generation boundary.
    ///
    /// Each leader at or above this rank in the breakaway chain starts
    /// a new generation.
    pub boundary_rank: String,
}

// ---------------------------------------------------------------------------
// Wire format bridge
//
// The Go layer and JSON schema use `override_calculation` (string enum)
// alongside a separate `differential` object. These types translate
// between that flat representation and the Rust OverrideMode enum.
// ---------------------------------------------------------------------------

/// Wire-format tag for `override_calculation`.
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
enum OverrideCalculationTag {
    Differential,
    FixedOverride,
}

/// Wire-format representation of `BreakawayConfig`. Matches the JSON
/// shape that Go produces and the JSON schema validates.
#[derive(Serialize, Deserialize)]
struct BreakawayConfigWire {
    threshold_rank: String,
    #[serde(rename = "group_volume_excludes_breakaway")]
    exclude_breakaway_gv: bool,
    override_calculation: OverrideCalculationTag,
    differential: Option<DifferentialConfig>,
    fixed_override: Option<FixedOverrideConfig>,
    #[serde(rename = "generation")]
    generation_overrides: Option<BreakawayGenerationConfig>,
}

impl<'de> serde::Deserialize<'de> for BreakawayConfig {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let wire = BreakawayConfigWire::deserialize(deserializer)?;
        let override_mode = match wire.override_calculation {
            OverrideCalculationTag::Differential => {
                if wire.fixed_override.is_some() {
                    return Err(serde::de::Error::custom(
                        "fixed_override config must be null when override_calculation is \"differential\"",
                    ));
                }
                let diff = wire.differential.ok_or_else(|| {
                    serde::de::Error::custom(
                        "differential config is required when override_calculation is \"differential\"",
                    )
                })?;
                OverrideMode::Differential(diff)
            }
            OverrideCalculationTag::FixedOverride => {
                if wire.differential.is_some() {
                    return Err(serde::de::Error::custom(
                        "differential config must be null when override_calculation is \"fixed_override\"",
                    ));
                }
                let fixed = wire.fixed_override.ok_or_else(|| {
                    serde::de::Error::custom(
                        "fixed_override config is required when override_calculation is \"fixed_override\"",
                    )
                })?;
                OverrideMode::FixedOverride(fixed)
            }
        };
        Ok(BreakawayConfig {
            threshold_rank: wire.threshold_rank,
            exclude_breakaway_gv: wire.exclude_breakaway_gv,
            override_mode,
            generation_overrides: wire.generation_overrides,
        })
    }
}

impl From<BreakawayConfig> for BreakawayConfigWire {
    fn from(config: BreakawayConfig) -> Self {
        let (override_calculation, differential, fixed_override) = match config.override_mode {
            OverrideMode::Differential(diff) => {
                (OverrideCalculationTag::Differential, Some(diff), None)
            }
            OverrideMode::FixedOverride(fixed) => {
                (OverrideCalculationTag::FixedOverride, None, Some(fixed))
            }
        };
        BreakawayConfigWire {
            threshold_rank: config.threshold_rank,
            exclude_breakaway_gv: config.exclude_breakaway_gv,
            override_calculation,
            differential,
            fixed_override,
            generation_overrides: config.generation_overrides,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_breakaway_config() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "override_calculation": "differential",
            "differential": {
                "rank_rates": {
                    "director": 0.10,
                    "senior_director": 0.15,
                    "executive": 0.20
                },
                "min_override": 0.02
            },
            "generation": {
                "max_generations": 3,
                "generation_rates": {
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

        let diff = match &config.override_mode {
            OverrideMode::Differential(d) => d,
            OverrideMode::FixedOverride(_) => panic!("expected Differential"),
        };
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
        let calc: OverrideCalculationTag = serde_json::from_str(json_diff).unwrap();
        assert!(matches!(calc, OverrideCalculationTag::Differential));

        let json_fixed = r#""fixed_override""#;
        let calc: OverrideCalculationTag = serde_json::from_str(json_fixed).unwrap();
        assert!(matches!(calc, OverrideCalculationTag::FixedOverride));
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
            "generation_rates": {
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
    fn deserialize_breakaway_fixed_override() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": false,
            "override_calculation": "fixed_override",
            "differential": null,
            "fixed_override": {
                "rank_rates": {
                    "director": 0.05,
                    "senior_director": 0.08
                }
            },
            "generation": null
        }"#;
        let config: BreakawayConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.threshold_rank, "director");
        assert!(!config.exclude_breakaway_gv);
        let fixed = match &config.override_mode {
            OverrideMode::FixedOverride(f) => f,
            OverrideMode::Differential(_) => panic!("expected FixedOverride"),
        };
        assert_eq!(fixed.rank_rates.len(), 2);
        assert_eq!(fixed.rank_rates["director"], 0.05);
        assert_eq!(fixed.rank_rates["senior_director"], 0.08);
        assert!(config.generation_overrides.is_none());
    }

    #[test]
    fn round_trip_differential() {
        let config = BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: true,
            override_mode: OverrideMode::Differential(DifferentialConfig {
                rank_rates: BTreeMap::from([("director".to_string(), 0.10)]),
                min_override: 0.02,
            }),
            generation_overrides: None,
        };
        let json = serde_json::to_string(&config).unwrap();
        // Wire format should contain separate fields
        assert!(json.contains("\"override_calculation\":\"differential\""));
        assert!(json.contains("\"differential\":{"));

        let restored: BreakawayConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(restored.threshold_rank, "director");
        assert!(matches!(
            restored.override_mode,
            OverrideMode::Differential(_)
        ));
    }

    #[test]
    fn round_trip_fixed_override() {
        let config = BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            override_mode: OverrideMode::FixedOverride(FixedOverrideConfig {
                rank_rates: BTreeMap::from([("director".to_string(), 0.05)]),
            }),
            generation_overrides: None,
        };
        let json = serde_json::to_string(&config).unwrap();
        assert!(json.contains("\"override_calculation\":\"fixed_override\""));
        assert!(json.contains("\"differential\":null"));
        assert!(json.contains("\"fixed_override\":{"));

        let restored: BreakawayConfig = serde_json::from_str(&json).unwrap();
        assert!(matches!(
            restored.override_mode,
            OverrideMode::FixedOverride(_)
        ));
    }

    #[test]
    fn differential_without_config_returns_error() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "override_calculation": "differential",
            "differential": null,
            "generation": null
        }"#;
        let result: Result<BreakawayConfig, _> = serde_json::from_str(json);
        assert!(result.is_err());
        let err = result.unwrap_err().to_string();
        assert!(
            err.contains("differential config is required"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn fixed_override_with_differential_returns_error() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": false,
            "override_calculation": "fixed_override",
            "differential": {
                "rank_rates": { "director": 0.10 },
                "min_override": 0.02
            },
            "fixed_override": {
                "rank_rates": { "director": 0.05 }
            },
            "generation": null
        }"#;
        let result: Result<BreakawayConfig, _> = serde_json::from_str(json);
        assert!(result.is_err());
        let err = result.unwrap_err().to_string();
        assert!(
            err.contains("differential config must be null"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn fixed_override_without_config_returns_error() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": false,
            "override_calculation": "fixed_override",
            "differential": null,
            "fixed_override": null,
            "generation": null
        }"#;
        let result: Result<BreakawayConfig, _> = serde_json::from_str(json);
        assert!(result.is_err());
        let err = result.unwrap_err().to_string();
        assert!(
            err.contains("fixed_override config is required"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn deserialize_fixed_override_config() {
        let json = r#"{
            "rank_rates": {
                "director": 0.05,
                "senior_director": 0.08,
                "executive": 0.12
            }
        }"#;
        let config: FixedOverrideConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.rank_rates.len(), 3);
        assert_eq!(config.rank_rates["director"], 0.05);
        assert_eq!(config.rank_rates["senior_director"], 0.08);
        assert_eq!(config.rank_rates["executive"], 0.12);
    }

    #[test]
    fn differential_with_fixed_override_returns_error() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "override_calculation": "differential",
            "differential": {
                "rank_rates": { "director": 0.10 },
                "min_override": 0.02
            },
            "fixed_override": {
                "rank_rates": { "director": 0.05 }
            },
            "generation": null
        }"#;
        let result: Result<BreakawayConfig, _> = serde_json::from_str(json);
        assert!(result.is_err());
        let err = result.unwrap_err().to_string();
        assert!(
            err.contains("fixed_override config must be null"),
            "unexpected error: {err}"
        );
    }
}
