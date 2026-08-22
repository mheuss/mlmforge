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
/// in that group and earns overrides on total group volume instead.
///
/// The override strategy lives in `overrides`. It is either a single
/// override walk or a multi-tier ladder. Binding the strategy to one
/// enum makes invalid combinations unrepresentable.
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

    /// How overrides are paid on Split-Out Group volume.
    pub overrides: OverrideStrategy,
}

/// How a breakaway plan pays overrides on Split-Out Group volume.
///
/// `SingleWalk` is the original behavior: one override walk where the
/// first qualifying ancestor earns, optionally extended deeper by
/// generation overrides. `MultiTier` is an ordered ladder of independent
/// override tiers (Oriflame-style).
#[derive(Debug, Clone)]
pub enum OverrideStrategy {
    /// One override walk. The first qualifying ancestor earns, optionally
    /// extended deeper by generation overrides.
    SingleWalk {
        mode: OverrideMode,
        generation_overrides: Option<BreakawayGenerationConfig>,
    },
    /// An ordered ladder of independent override tiers (Oriflame-style).
    MultiTier(MultiTierConfig),
}

/// An ordered ladder of breakaway override tiers.
///
/// Tiers are listed in depth order. The first tier is split-out depth 1,
/// the second is depth 2, and so on. Capped at 255 tiers so each tier's
/// 1-based depth floor fits in `u8` (`walk_multi_tier_overrides` would
/// panic the 256th tier otherwise).
///
/// Non-empty and at-most-255 `tiers` is enforced by the JSON schema and the
/// Go validator (HEU-428 Tasks 7 and final review), not by this type.
///
/// The struct exists as a one-field wrapper rather than
/// `MultiTier(Vec<BreakawayTier>)` so the internally-tagged wire form
/// nests `tiers` under the discriminator (`{"type": "multi_tier",
/// "tiers": [...]}`). Newtype-around-`Vec` would emit the array
/// inline at the variant root, breaking the cross-language contract.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MultiTierConfig {
    pub tiers: Vec<BreakawayTier>,
}

/// One tier of a multi-tier breakaway override ladder.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BreakawayTier {
    /// Minimum First-Line Split-Out Groups the earner must have to
    /// receive this tier's override.
    pub min_split_out_groups: u8,
    /// Override rate applied to Split-Out Group volume. 0.0-1.0.
    pub rate: f64,
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
    ///
    /// Deserialized via `crate::serde_helpers::u8_keyed_map` so the keys
    /// parse whether or not serde buffered this value.
    #[serde(
        rename = "generation_rates",
        deserialize_with = "crate::serde_helpers::u8_keyed_map"
    )]
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
// The Go layer and JSON schema put the override strategy under an
// `overrides` object tagged by `type`. Single-walk uses
// `override_calculation` (string enum) alongside a separate `differential`
// object. These types translate between that wire representation and the
// Rust `OverrideStrategy` enum.
//
// `OverrideStrategyWire` is internally tagged. Go marshals every variant
// field, zero-valued for the non-selected variant. Internal tagging reads
// `type` first and ignores the other variant's fields, so Go's
// empty-string `override_calculation` on a multi-tier object parses
// cleanly.
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
    overrides: OverrideStrategyWire,
}

/// Wire-format representation of `OverrideStrategy`.
///
/// Internally tagged by `type`. Internal tagging is required so the
/// non-selected variant's fields, which the Go flat struct always emits,
/// are ignored rather than parsed.
#[derive(Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
enum OverrideStrategyWire {
    SingleWalk(SingleWalkWire),
    MultiTier(MultiTierConfig),
}

/// Wire-format representation of `OverrideStrategy::SingleWalk`.
///
/// Keeps `override_calculation` and `differential` as separate fields for
/// backwards compatibility with Go types and the JSON schema. The
/// conversion to `OverrideMode` collapses them and validates that the
/// non-selected mode's config is null.
#[derive(Serialize, Deserialize)]
struct SingleWalkWire {
    override_calculation: OverrideCalculationTag,
    differential: Option<DifferentialConfig>,
    fixed_override: Option<FixedOverrideConfig>,
    #[serde(rename = "generation")]
    generation: Option<BreakawayGenerationConfig>,
}

impl SingleWalkWire {
    /// Convert to `OverrideStrategy::SingleWalk`, validating that the
    /// non-selected mode's config is null.
    fn into_strategy<E: serde::de::Error>(self) -> Result<OverrideStrategy, E> {
        let mode = match self.override_calculation {
            OverrideCalculationTag::Differential => {
                if self.fixed_override.is_some() {
                    return Err(E::custom(
                        "fixed_override config must be null when override_calculation is \"differential\"",
                    ));
                }
                let diff = self.differential.ok_or_else(|| {
                    E::custom(
                        "differential config is required when override_calculation is \"differential\"",
                    )
                })?;
                OverrideMode::Differential(diff)
            }
            OverrideCalculationTag::FixedOverride => {
                if self.differential.is_some() {
                    return Err(E::custom(
                        "differential config must be null when override_calculation is \"fixed_override\"",
                    ));
                }
                let fixed = self.fixed_override.ok_or_else(|| {
                    E::custom(
                        "fixed_override config is required when override_calculation is \"fixed_override\"",
                    )
                })?;
                OverrideMode::FixedOverride(fixed)
            }
        };
        Ok(OverrideStrategy::SingleWalk {
            mode,
            generation_overrides: self.generation,
        })
    }
}

/// Build a `SingleWalkWire` from the inner fields of a
/// `OverrideStrategy::SingleWalk` variant.
fn single_walk_to_wire(
    mode: OverrideMode,
    generation: Option<BreakawayGenerationConfig>,
) -> SingleWalkWire {
    let (override_calculation, differential, fixed_override) = match mode {
        OverrideMode::Differential(diff) => {
            (OverrideCalculationTag::Differential, Some(diff), None)
        }
        OverrideMode::FixedOverride(fixed) => {
            (OverrideCalculationTag::FixedOverride, None, Some(fixed))
        }
    };
    SingleWalkWire {
        override_calculation,
        differential,
        fixed_override,
        generation,
    }
}

impl<'de> serde::Deserialize<'de> for BreakawayConfig {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let wire = BreakawayConfigWire::deserialize(deserializer)?;
        let overrides = match wire.overrides {
            OverrideStrategyWire::SingleWalk(single) => single.into_strategy::<D::Error>()?,
            OverrideStrategyWire::MultiTier(multi) => OverrideStrategy::MultiTier(multi),
        };
        Ok(BreakawayConfig {
            threshold_rank: wire.threshold_rank,
            exclude_breakaway_gv: wire.exclude_breakaway_gv,
            overrides,
        })
    }
}

impl From<BreakawayConfig> for BreakawayConfigWire {
    fn from(config: BreakawayConfig) -> Self {
        let overrides = match config.overrides {
            OverrideStrategy::SingleWalk {
                mode,
                generation_overrides,
            } => OverrideStrategyWire::SingleWalk(single_walk_to_wire(mode, generation_overrides)),
            OverrideStrategy::MultiTier(multi) => OverrideStrategyWire::MultiTier(multi),
        };
        BreakawayConfigWire {
            threshold_rank: config.threshold_rank,
            exclude_breakaway_gv: config.exclude_breakaway_gv,
            overrides,
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
            "overrides": {
                "type": "single_walk",
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
            }
        }"#;
        let config: BreakawayConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.threshold_rank, "director");
        assert!(config.exclude_breakaway_gv);

        let (mode, generation_overrides) = match &config.overrides {
            OverrideStrategy::SingleWalk {
                mode,
                generation_overrides,
            } => (mode, generation_overrides),
            OverrideStrategy::MultiTier(_) => panic!("expected SingleWalk"),
        };

        let diff = match mode {
            OverrideMode::Differential(d) => d,
            OverrideMode::FixedOverride(_) => panic!("expected Differential"),
        };
        assert_eq!(diff.rank_rates.len(), 3);
        assert_eq!(diff.rank_rates["director"], 0.10);
        assert_eq!(diff.rank_rates["senior_director"], 0.15);
        assert_eq!(diff.rank_rates["executive"], 0.20);
        assert_eq!(diff.min_override, 0.02);

        let gen_cfg = generation_overrides.as_ref().unwrap();
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
            "overrides": {
                "type": "single_walk",
                "override_calculation": "fixed_override",
                "differential": null,
                "fixed_override": {
                    "rank_rates": {
                        "director": 0.05,
                        "senior_director": 0.08
                    }
                },
                "generation": null
            }
        }"#;
        let config: BreakawayConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.threshold_rank, "director");
        assert!(!config.exclude_breakaway_gv);
        let (mode, generation_overrides) = match &config.overrides {
            OverrideStrategy::SingleWalk {
                mode,
                generation_overrides,
            } => (mode, generation_overrides),
            OverrideStrategy::MultiTier(_) => panic!("expected SingleWalk"),
        };
        let fixed = match mode {
            OverrideMode::FixedOverride(f) => f,
            OverrideMode::Differential(_) => panic!("expected FixedOverride"),
        };
        assert_eq!(fixed.rank_rates.len(), 2);
        assert_eq!(fixed.rank_rates["director"], 0.05);
        assert_eq!(fixed.rank_rates["senior_director"], 0.08);
        assert!(generation_overrides.is_none());
    }

    #[test]
    fn round_trip_differential() {
        let config = BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: true,
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::Differential(DifferentialConfig {
                    rank_rates: BTreeMap::from([("director".to_string(), 0.10)]),
                    min_override: 0.02,
                }),
                generation_overrides: None,
            },
        };
        let json = serde_json::to_string(&config).unwrap();
        // Wire format tags the strategy and keeps separate single-walk fields.
        assert!(json.contains("\"type\":\"single_walk\""));
        assert!(json.contains("\"override_calculation\":\"differential\""));
        assert!(json.contains("\"differential\":{"));

        let restored: BreakawayConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(restored.threshold_rank, "director");
        let mode = match &restored.overrides {
            OverrideStrategy::SingleWalk { mode, .. } => mode,
            OverrideStrategy::MultiTier(_) => panic!("expected SingleWalk"),
        };
        assert!(matches!(mode, OverrideMode::Differential(_)));
    }

    #[test]
    fn round_trip_fixed_override() {
        let config = BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::FixedOverride(FixedOverrideConfig {
                    rank_rates: BTreeMap::from([("director".to_string(), 0.05)]),
                }),
                generation_overrides: None,
            },
        };
        let json = serde_json::to_string(&config).unwrap();
        assert!(json.contains("\"type\":\"single_walk\""));
        assert!(json.contains("\"override_calculation\":\"fixed_override\""));
        assert!(json.contains("\"differential\":null"));
        assert!(json.contains("\"fixed_override\":{"));

        let restored: BreakawayConfig = serde_json::from_str(&json).unwrap();
        let mode = match &restored.overrides {
            OverrideStrategy::SingleWalk { mode, .. } => mode,
            OverrideStrategy::MultiTier(_) => panic!("expected SingleWalk"),
        };
        assert!(matches!(mode, OverrideMode::FixedOverride(_)));
    }

    #[test]
    fn differential_without_config_returns_error() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "overrides": {
                "type": "single_walk",
                "override_calculation": "differential",
                "differential": null,
                "generation": null
            }
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
            "overrides": {
                "type": "single_walk",
                "override_calculation": "fixed_override",
                "differential": {
                    "rank_rates": { "director": 0.10 },
                    "min_override": 0.02
                },
                "fixed_override": {
                    "rank_rates": { "director": 0.05 }
                },
                "generation": null
            }
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
            "overrides": {
                "type": "single_walk",
                "override_calculation": "fixed_override",
                "differential": null,
                "fixed_override": null,
                "generation": null
            }
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
    fn round_trip_multi_tier() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "overrides": {
                "type": "multi_tier",
                "tiers": [
                    { "min_split_out_groups": 1, "rate": 0.05 },
                    { "min_split_out_groups": 2, "rate": 0.02 }
                ]
            }
        }"#;
        let config: BreakawayConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.threshold_rank, "director");
        assert!(config.exclude_breakaway_gv);

        let tiers = match &config.overrides {
            OverrideStrategy::MultiTier(mt) => &mt.tiers,
            OverrideStrategy::SingleWalk { .. } => panic!("expected MultiTier"),
        };
        assert_eq!(tiers.len(), 2);
        assert_eq!(tiers[0].min_split_out_groups, 1);
        assert_eq!(tiers[0].rate, 0.05);
        assert_eq!(tiers[1].min_split_out_groups, 2);
        assert_eq!(tiers[1].rate, 0.02);

        // Round-trip: serialize then deserialize again, expect the same shape.
        let serialized = serde_json::to_string(&config).unwrap();
        let restored: BreakawayConfig = serde_json::from_str(&serialized).unwrap();
        let restored_tiers = match &restored.overrides {
            OverrideStrategy::MultiTier(mt) => &mt.tiers,
            OverrideStrategy::SingleWalk { .. } => panic!("expected MultiTier"),
        };
        assert_eq!(restored_tiers.len(), 2);
        assert_eq!(restored_tiers[0].min_split_out_groups, 1);
        assert_eq!(restored_tiers[0].rate, 0.05);
        assert_eq!(restored_tiers[1].min_split_out_groups, 2);
        assert_eq!(restored_tiers[1].rate, 0.02);
    }

    #[test]
    fn round_trip_single_walk() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "overrides": {
                "type": "single_walk",
                "override_calculation": "differential",
                "differential": {
                    "rank_rates": { "director": 0.10 },
                    "min_override": 0.02
                }
            }
        }"#;
        let config: BreakawayConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.threshold_rank, "director");
        assert!(config.exclude_breakaway_gv);

        let mode = match &config.overrides {
            OverrideStrategy::SingleWalk { mode, .. } => mode,
            OverrideStrategy::MultiTier(_) => panic!("expected SingleWalk"),
        };
        let diff = match mode {
            OverrideMode::Differential(d) => d,
            OverrideMode::FixedOverride(_) => panic!("expected Differential"),
        };
        assert_eq!(diff.rank_rates["director"], 0.10);
        assert_eq!(diff.min_override, 0.02);

        // Round-trip: serialize then deserialize again, expect the same shape.
        let serialized = serde_json::to_string(&config).unwrap();
        let restored: BreakawayConfig = serde_json::from_str(&serialized).unwrap();
        let restored_mode = match &restored.overrides {
            OverrideStrategy::SingleWalk { mode, .. } => mode,
            OverrideStrategy::MultiTier(_) => panic!("expected SingleWalk"),
        };
        assert!(matches!(restored_mode, OverrideMode::Differential(_)));
    }

    #[test]
    fn deserialize_go_emitted_overrides() {
        // Go marshals every variant field, zero-valued for the non-selected
        // variant. The internally-tagged OverrideStrategyWire reads `type`
        // first and ignores the other variant's fields. This test pins that
        // cross-language contract.
        let multi_tier_go = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "overrides": {
                "type": "multi_tier",
                "override_calculation": "",
                "differential": null,
                "fixed_override": null,
                "generation": null,
                "tiers": [
                    { "min_split_out_groups": 1, "rate": 0.05 },
                    { "min_split_out_groups": 2, "rate": 0.02 }
                ]
            }
        }"#;
        let config: BreakawayConfig = serde_json::from_str(multi_tier_go).unwrap();
        let tiers = match &config.overrides {
            OverrideStrategy::MultiTier(mt) => &mt.tiers,
            OverrideStrategy::SingleWalk { .. } => panic!("expected MultiTier"),
        };
        assert_eq!(tiers.len(), 2);
        assert_eq!(tiers[0].min_split_out_groups, 1);
        assert_eq!(tiers[1].rate, 0.02);

        let single_walk_go = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "overrides": {
                "type": "single_walk",
                "override_calculation": "differential",
                "differential": {
                    "rank_rates": { "director": 0.10 },
                    "min_override": 0.02
                },
                "fixed_override": null,
                "generation": null,
                "tiers": null
            }
        }"#;
        let config: BreakawayConfig = serde_json::from_str(single_walk_go).unwrap();
        let mode = match &config.overrides {
            OverrideStrategy::SingleWalk { mode, .. } => mode,
            OverrideStrategy::MultiTier(_) => panic!("expected SingleWalk"),
        };
        let OverrideMode::Differential(diff) = mode else {
            panic!("expected Differential override mode");
        };
        assert_eq!(diff.rank_rates["director"], 0.10);
        assert_eq!(diff.min_override, 0.02);
    }

    #[test]
    fn differential_with_fixed_override_returns_error() {
        let json = r#"{
            "threshold_rank": "director",
            "group_volume_excludes_breakaway": true,
            "overrides": {
                "type": "single_walk",
                "override_calculation": "differential",
                "differential": {
                    "rank_rates": { "director": 0.10 },
                    "min_override": 0.02
                },
                "fixed_override": {
                    "rank_rates": { "director": 0.05 }
                },
                "generation": null
            }
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
