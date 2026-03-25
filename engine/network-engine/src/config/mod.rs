//! Compensation plan configuration types.
//!
//! These types define the contract between the Go application layer
//! and the Rust commission engine. The Go layer validates, stores,
//! and deserializes configuration. The engine receives fully typed
//! structs and produces commission results.
//!
//! Every type and field is documented with its business meaning.
//! This module IS the developer reference for the configuration
//! surface. Design document (untracked, relative to project root):
//! `docs/plans/2026-02-12-compensation-plan-config-design.md`

pub mod binary;
pub mod board_plan;
pub mod bonus;
pub mod commission;
pub mod eligibility;
pub mod generation;
pub mod matrix;
pub mod payout;
pub mod period;
pub mod placement;
pub mod rank;
pub mod stairstep;
pub mod streamline;
pub mod volume;

use serde::{Deserialize, Serialize};

pub use binary::BinaryCommissionConfig;
pub use board_plan::{BoardPlanConfig, ReEntryPosition};
pub use bonus::{BonusConfig, PassUpConfig};
pub use commission::{CompressionConfig, LevelCommissionConfig};
pub use eligibility::CommissionEligibility;
pub use generation::GenerationCommissionConfig;
pub use matrix::{MatrixStructureParams, PruningConfig};
pub use payout::{CapsConfig, PayoutConfig};
pub use period::PeriodConfig;
pub use placement::PlacementConfig;
pub use rank::{RankDefinition, RankFeaturesConfig, RankTrackingConfig};
pub use stairstep::BreakawayConfig;
pub use streamline::StreamlineCommissionConfig;
pub use volume::VolumeConfig;

// ---------------------------------------------------------------------------
// Top-level container
// ---------------------------------------------------------------------------

/// The root configuration object for a compensation plan.
///
/// Passed to the engine as a fully typed struct. The engine never parses
/// YAML or reads from the database. The Go application layer deserializes
/// the stored configuration into this struct and hands it to the engine.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CompensationPlan {
    /// Display name for this compensation plan.
    pub name: String,

    /// Schema version. Currently 1.
    pub version: u32,

    /// One or more compensation structures.
    ///
    /// A plan can combine multiple structures (e.g., unilevel + binary).
    /// Each structure defines its own commission rules. Distributors may
    /// qualify for commissions on some or all structures depending on rank.
    pub structures: Vec<StructureConfig>,

    /// Commission period timing.
    pub period: PeriodConfig,

    /// Volume configuration.
    pub volume: VolumeConfig,

    /// Rank ladder, shared across all structures.
    ///
    /// A distributor holds one rank that may qualify them for commissions
    /// on multiple structures. Ranks are evaluated lowest-to-highest ordinal.
    pub ranks: Vec<RankDefinition>,

    /// Whether to track highest-ever rank.
    pub rank_tracking: RankTrackingConfig,

    /// Admin rank constraint and override features.
    pub rank_features: RankFeaturesConfig,

    /// Who receives commissions.
    #[serde(rename = "commission_eligibility")]
    pub eligibility: CommissionEligibility,

    /// All bonus programs.
    pub bonuses: BonusConfig,

    /// Payout configuration.
    pub payout: PayoutConfig,

    /// Commission caps.
    pub caps: CapsConfig,

    /// Placement rules.
    pub placement: PlacementConfig,
}

// ---------------------------------------------------------------------------
// Structure enum
// ---------------------------------------------------------------------------

/// A compensation structure within the plan.
///
/// Adjacently tagged on "type" with content in "config". Each variant
/// holds structure-specific commission configuration. The type system
/// prevents invalid combinations. A plan can contain multiple structures
/// of different or same types.
///
/// Adjacent tagging (`tag` + `content`) is used instead of internal
/// tagging because serde's internal tagging buffers content into an
/// intermediate representation that cannot deserialize non-string map
/// keys (e.g., `BTreeMap<u8, f64>` in rate tables). Adjacent tagging
/// avoids this limitation.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", content = "config", rename_all = "snake_case")]
pub enum StructureConfig {
    /// Unilevel structure. Unlimited width, depth-limited commissions.
    Unilevel(UnilevelStructureConfig),

    /// Binary structure. Two legs per distributor.
    Binary(BinaryStructureConfig),

    /// Matrix structure. Fixed width and depth.
    Matrix(MatrixStructureConfig),

    /// Stairstep breakaway structure. Groups break away at rank thresholds.
    Stairstep(StairstepStructureConfig),

    /// Generation structure. Commissions on generations of leaders.
    Generation(GenerationStructureConfig),

    /// Streamline structure. Linear chains with rank-based positioning.
    Streamline(StreamlineStructureConfig),

    /// Board plan structure. Small cycling matrix with fixed payouts.
    BoardPlan(BoardPlanStructureConfig),
}

// ---------------------------------------------------------------------------
// Per-structure wrapper structs
// ---------------------------------------------------------------------------

/// Unilevel structure configuration.
///
/// Unlimited width. Commissions are paid on multiple levels of depth
/// below the earning distributor. Optional compression removes unqualified
/// distributors from the payout chain.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UnilevelStructureConfig {
    /// Display name for this structure instance.
    pub name: String,

    /// Level commission configuration.
    pub level_commission: LevelCommissionConfig,

    /// Optional compression removes unqualified distributors from the
    /// payout chain.
    pub compression: Option<CompressionConfig>,

    /// Optional Australian X-Up pass-up. First N sponsored recruits
    /// are credited to the distributor's upline sponsor rather than the
    /// distributor, for commission purposes.
    ///
    /// `serde(default)` because existing JSON payloads predate this field.
    /// Other `Option` fields on this struct don't need it because they
    /// were present from the initial schema.
    #[serde(default)]
    pub pass_up: Option<PassUpConfig>,
}

/// Binary structure configuration.
///
/// Two legs per distributor. Commissions are based on matching volume
/// between legs.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BinaryStructureConfig {
    /// Display name for this structure instance.
    pub name: String,

    /// Binary commission configuration.
    pub binary_commission: BinaryCommissionConfig,
}

/// Matrix structure configuration.
///
/// Fixed-width, fixed-depth tree with spillover placement. Combines
/// matrix-specific parameters with level commissions. Optional pruning
/// handles node removal.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MatrixStructureConfig {
    /// Display name for this structure instance.
    pub name: String,

    /// Fixed-width, fixed-depth matrix parameters.
    pub matrix_params: MatrixStructureParams,

    /// Level commission configuration.
    pub level_commission: LevelCommissionConfig,

    /// Optional compression removes unqualified distributors from the
    /// payout chain.
    pub compression: Option<CompressionConfig>,

    /// Optional pruning handles node removal from the matrix.
    pub pruning: Option<PruningConfig>,
}

/// Stairstep breakaway structure configuration.
///
/// Groups break away when their leader reaches a threshold rank.
/// Combines level commissions with breakaway overrides.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StairstepStructureConfig {
    /// Display name for this structure instance.
    pub name: String,

    /// Level commission configuration.
    pub level_commission: LevelCommissionConfig,

    /// Optional compression removes unqualified distributors from the
    /// payout chain.
    pub compression: Option<CompressionConfig>,

    /// Breakaway configuration.
    pub breakaway: Option<BreakawayConfig>,
}

/// Generation structure configuration.
///
/// Commissions on generations of qualified leaders. Optionally combines
/// level commissions with generation commissions.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GenerationStructureConfig {
    /// Display name for this structure instance.
    pub name: String,

    /// Optional level commission configuration.
    ///
    /// Some generation plans use only generation commissions. When
    /// `level_commissions_enabled` is false, this field is ignored
    /// even if present.
    pub level_commission: Option<LevelCommissionConfig>,

    /// Optional compression removes unqualified distributors from the
    /// payout chain. Only applies when level commissions are enabled.
    pub compression: Option<CompressionConfig>,

    /// Generation commission configuration.
    pub generation_commission: GenerationCommissionConfig,

    /// Master switch for level commissions within this structure.
    ///
    /// When false, only generation commissions are paid regardless
    /// of the `level_commission` field's presence.
    pub level_commissions_enabled: bool,
}

/// Streamline structure configuration.
///
/// Linear chains where rank determines earning position. Dynamic
/// compression skips distributors below rank thresholds.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamlineStructureConfig {
    /// Display name for this structure instance.
    pub name: String,

    /// Streamline commission configuration.
    pub streamline_commission: StreamlineCommissionConfig,
}

/// Board plan structure configuration.
///
/// Small fixed-size cycling matrix. When a board fills, the top position
/// cycles out and earns a fixed commission. Requires a companion unilevel
/// structure for sponsor-based commissions.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoardPlanStructureConfig {
    /// Display name for this structure instance.
    pub name: String,

    /// Board dimensions: width (children per node).
    pub width: u8,

    /// Board depth in levels.
    pub height: u8,

    /// Board cycling configuration.
    pub board_cycling: BoardPlanConfig,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_structure_config_unilevel() {
        let json = r#"{
            "type": "unilevel",
            "config": {
                "name": "Primary Unilevel",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 5,
                    "rate_table": {
                        "associate": { "1": 0.05, "2": 0.04, "3": 0.03 },
                        "silver": { "1": 0.07, "2": 0.06, "3": 0.05, "4": 0.04, "5": 0.03 }
                    }
                },
                "compression": {
                    "enabled": true,
                    "mode": "skip_inactive",
                    "rank_threshold": null
                }
            }
        }"#;
        let config: StructureConfig = serde_json::from_str(json).unwrap();
        match config {
            StructureConfig::Unilevel(uni) => {
                assert_eq!(uni.name, "Primary Unilevel");
                assert_eq!(uni.level_commission.max_depth, 5);
                assert!(uni.compression.is_some());
            }
            _ => panic!("expected Unilevel variant"),
        }
    }

    #[test]
    fn deserialize_structure_config_binary() {
        let json = r#"{
            "type": "binary",
            "config": {
                "name": "Binary Bonus",
                "binary_commission": {
                    "volume_to_dollar_multiplier": null,
                    "mode": {
                        "pairing": {
                            "percent": 0.10,
                            "calculation": "weaker_leg",
                            "cap_per_period": 5000.0,
                            "volume_after_payout": "carry_forward",
                            "carry_forward_cap": 100000.0
                        }
                    }
                }
            }
        }"#;
        let config: StructureConfig = serde_json::from_str(json).unwrap();
        match config {
            StructureConfig::Binary(bin) => {
                assert_eq!(bin.name, "Binary Bonus");
            }
            _ => panic!("expected Binary variant"),
        }
    }

    #[test]
    fn deserialize_structure_config_matrix() {
        let json = r#"{
            "type": "matrix",
            "config": {
                "name": "3x9 Matrix",
                "matrix_params": {
                    "width": 3,
                    "height": 9,
                    "spillover_direction": "breadth_first"
                },
                "level_commission": {
                    "broad_commission_percent": 0.35,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 9,
                    "rate_table": {
                        "associate": { "1": 0.05 }
                    }
                },
                "compression": null,
                "pruning": {
                    "mode": "promote_earliest"
                }
            }
        }"#;
        let config: StructureConfig = serde_json::from_str(json).unwrap();
        match config {
            StructureConfig::Matrix(mat) => {
                assert_eq!(mat.name, "3x9 Matrix");
                assert_eq!(mat.matrix_params.width, 3);
                assert_eq!(mat.matrix_params.height, 9);
                assert!(mat.compression.is_none());
                assert!(mat.pruning.is_some());
            }
            _ => panic!("expected Matrix variant"),
        }
    }

    #[test]
    fn deserialize_structure_config_generation() {
        let json = r#"{
            "type": "generation",
            "config": {
                "name": "Generation Plan",
                "level_commission": null,
                "compression": null,
                "generation_commission": {
                    "max_generations": 4,
                    "generation_rates": { "1": 0.10, "2": 0.06, "3": 0.04, "4": 0.02 },
                    "boundary_mode": "threshold_rank",
                    "boundary_rank": "director",
                    "empty_generation_consumes_number": true,
                    "volume_to_dollar_multiplier": null
                },
                "level_commissions_enabled": false
            }
        }"#;
        let config: StructureConfig = serde_json::from_str(json).unwrap();
        match config {
            StructureConfig::Generation(gen_config) => {
                assert_eq!(gen_config.name, "Generation Plan");
                assert!(gen_config.level_commission.is_none());
                assert!(!gen_config.level_commissions_enabled);
                assert_eq!(gen_config.generation_commission.max_generations, 4);
            }
            _ => panic!("expected Generation variant"),
        }
    }

    #[test]
    fn deserialize_multi_structure_plan() {
        let json = r#"{
            "name": "Hybrid Plan",
            "version": 1,
            "structures": [
                {
                    "type": "unilevel",
                    "config": {
                        "name": "Primary",
                        "level_commission": {
                            "broad_commission_percent": 0.40,
                            "volume_to_dollar_multiplier": null,
                            "commissionable_depth": 5,
                            "rate_table": {
                                "associate": { "1": 0.05 }
                            }
                        },
                        "compression": null
                    }
                },
                {
                    "type": "binary",
                    "config": {
                        "name": "Binary Bonus",
                        "binary_commission": {
                            "volume_to_dollar_multiplier": null,
                            "mode": {
                                "pairing": {
                                    "percent": 0.10,
                                    "calculation": "weaker_leg",
                                    "cap_per_period": null,
                                    "volume_after_payout": "carry_forward",
                                    "carry_forward_cap": null
                                }
                            }
                        }
                    }
                }
            ],
            "period": {
                "length": "month",
                "start_date": "2026-03-01",
                "payout_lag_days": 14
            },
            "volume": {
                "inhibit_signup_volume": false,
                "base_currency": "USD",
                "volume_to_dollar_multiplier": 1.0,
                "deduct_qualifying_volume": false
            },
            "ranks": [
                {
                    "name": "Associate",
                    "ordinal": 1,
                    "qualification": {
                        "structures": [{
                            "structure": "Primary",
                            "personal_volume": 0.0,
                            "group_volume": 0.0,
                            "max_group_volume_per_leg": 0.0,
                            "min_retail_volume": 0.0,
                            "distributor_count": null
                        }],
                        "required_products": []
                    },
                    "qualified_structures": ["Primary"],
                    "demotion_policy": "promotion_only"
                }
            ],
            "rank_tracking": { "track_achieved_rank": false },
            "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
            "commission_eligibility": {
                "min_personal_volume": 100.0,
                "require_order_in_period": false,
                "eligible_statuses": ["active"],
                "active_leg_tiers": []
            },
            "bonuses": {
                "matching": null,
                "sponsor": null,
                "fast_start": null,
                "rank_advancement": null,
                "leadership_development": null,
                "infinity": null,
                "lifestyle": null,
                "pool": null,
                "matrix_completion": null,
                "position": null,
                "board_cycling": null
            },
            "payout": {
                "base_currency": "USD",
                "minimum_amount": 50.0,
                "split_payouts_enabled": true,
                "methods": [
                    { "type": "bank_transfer", "fee": 2.50 }
                ]
            },
            "caps": {
                "per_distributor_per_period": null,
                "company_payout_cap_percent": 0.42,
                "cap_enforcement": "pro_rata",
                "clawback_on_refund": false
            },
            "placement": {
                "donated_placement": null,
                "holding_tank": null,
                "binary_placement": {
                    "default_placement": "balanced",
                    "per_user_preference": true,
                    "spillover_enabled": true
                }
            }
        }"#;
        let plan: CompensationPlan = serde_json::from_str(json).unwrap();
        assert_eq!(plan.name, "Hybrid Plan");
        assert_eq!(plan.version, 1);
        assert_eq!(plan.structures.len(), 2);
        assert!(matches!(plan.structures[0], StructureConfig::Unilevel(_)));
        assert!(matches!(plan.structures[1], StructureConfig::Binary(_)));
    }

    #[test]
    fn deserialize_full_compensation_plan() {
        let json = r#"{
            "name": "Acme Wellness Plan",
            "version": 1,
            "structures": [
                {
                    "type": "unilevel",
                    "config": {
                        "name": "Primary Unilevel",
                        "level_commission": {
                            "broad_commission_percent": 0.40,
                            "volume_to_dollar_multiplier": null,
                            "commissionable_depth": 7,
                            "rate_table": {
                                "associate": {
                                    "1": 0.05, "2": 0.04, "3": 0.03
                                },
                                "silver": {
                                    "1": 0.07, "2": 0.06, "3": 0.05, "4": 0.04, "5": 0.03
                                },
                                "gold": {
                                    "1": 0.08, "2": 0.07, "3": 0.06, "4": 0.05, "5": 0.04, "6": 0.03, "7": 0.02
                                }
                            }
                        },
                        "compression": {
                            "enabled": true,
                            "mode": "skip_inactive",
                            "rank_threshold": null
                        }
                    }
                }
            ],
            "period": {
                "length": "month",
                "start_date": "2026-04-01",
                "payout_lag_days": 14
            },
            "volume": {
                "inhibit_signup_volume": false,
                "base_currency": "USD",
                "volume_to_dollar_multiplier": 1.0,
                "deduct_qualifying_volume": false
            },
            "ranks": [
                {
                    "name": "Associate",
                    "ordinal": 1,
                    "qualification": {
                        "structures": [{
                            "structure": "Primary Unilevel",
                            "personal_volume": 0.0,
                            "group_volume": 0.0,
                            "max_group_volume_per_leg": 0.0,
                            "min_retail_volume": 0.0,
                            "distributor_count": null
                        }],
                        "required_products": []
                    },
                    "qualified_structures": ["Primary Unilevel"],
                    "demotion_policy": "promotion_only"
                },
                {
                    "name": "Silver",
                    "ordinal": 2,
                    "qualification": {
                        "structures": [{
                            "structure": "Primary Unilevel",
                            "personal_volume": 100.0,
                            "group_volume": 3000.0,
                            "max_group_volume_per_leg": 2000.0,
                            "min_retail_volume": 50.0,
                            "distributor_count": {
                                "count": 2,
                                "min_rank": "Associate",
                                "search_mode": "first_levels",
                                "search_depth": 3,
                                "total_count": 5,
                                "min_leg_group_volume": 500.0
                            }
                        }],
                        "required_products": []
                    },
                    "qualified_structures": ["Primary Unilevel"],
                    "demotion_policy": {
                        "grace": { "count": 2, "unit": "months" }
                    }
                },
                {
                    "name": "Gold",
                    "ordinal": 3,
                    "qualification": {
                        "structures": [{
                            "structure": "Primary Unilevel",
                            "personal_volume": 150.0,
                            "group_volume": 10000.0,
                            "max_group_volume_per_leg": 6000.0,
                            "min_retail_volume": 100.0,
                            "distributor_count": {
                                "count": 3,
                                "min_rank": "Silver",
                                "search_mode": "first_levels",
                                "search_depth": 5,
                                "total_count": 15,
                                "min_leg_group_volume": 2000.0
                            }
                        }],
                        "required_products": ["premium-starter-kit"]
                    },
                    "qualified_structures": ["Primary Unilevel"],
                    "demotion_policy": {
                        "grace": { "count": 3, "unit": "months" }
                    }
                }
            ],
            "rank_tracking": {
                "track_achieved_rank": true
            },
            "rank_features": {
                "constraints_enabled": true,
                "overrides_enabled": false
            },
            "commission_eligibility": {
                "min_personal_volume": 100.0,
                "require_order_in_period": true,
                "eligible_statuses": ["active", "grace"],
                "active_leg_tiers": [
                    { "min_active_legs": 2, "max_commission_depth": 3 },
                    { "min_active_legs": 5, "max_commission_depth": 5 },
                    { "min_active_legs": 8, "max_commission_depth": 0 }
                ]
            },
            "bonuses": {
                "matching": {
                    "depth": 3,
                    "rates": { "1": 0.50, "2": 0.25, "3": 0.10 },
                    "matched_commission_types": ["unilevel"]
                },
                "sponsor": {
                    "amount": 25.0,
                    "amount_type": "fixed",
                    "qualifying_products": ["starter-kit"]
                },
                "fast_start": null,
                "rank_advancement": {
                    "amounts": {
                        "Silver": 200.0,
                        "Gold": 500.0
                    },
                    "pay_once_only": true
                },
                "leadership_development": null,
                "infinity": null,
                "lifestyle": null,
                "pool": null,
                "matrix_completion": null,
                "position": null,
                "board_cycling": null
            },
            "payout": {
                "base_currency": "USD",
                "minimum_amount": 50.0,
                "split_payouts_enabled": true,
                "methods": [
                    { "type": "bank_transfer", "fee": 2.50 },
                    { "type": "ewallet", "fee": 0.00 }
                ]
            },
            "caps": {
                "per_distributor_per_period": 10000.0,
                "company_payout_cap_percent": 0.42,
                "cap_enforcement": "pro_rata",
                "clawback_on_refund": false
            },
            "placement": {
                "donated_placement": "own_downline",
                "holding_tank": null,
                "binary_placement": null
            }
        }"#;
        let plan: CompensationPlan = serde_json::from_str(json).unwrap();

        // Top-level fields
        assert_eq!(plan.name, "Acme Wellness Plan");
        assert_eq!(plan.version, 1);

        // Structure
        assert_eq!(plan.structures.len(), 1);
        match &plan.structures[0] {
            StructureConfig::Unilevel(uni) => {
                assert_eq!(uni.name, "Primary Unilevel");
                assert_eq!(uni.level_commission.broad_commission_percent, 0.40);
                assert_eq!(uni.level_commission.max_depth, 7);
                assert_eq!(uni.level_commission.rate_table.len(), 3);
                assert!(uni.compression.is_some());
            }
            _ => panic!("expected Unilevel variant"),
        }

        // Period
        assert_eq!(plan.period.payout_lag_days, 14);

        // Volume
        assert!(!plan.volume.inhibit_signup_volume);
        assert_eq!(plan.volume.base_currency, "USD");
        assert_eq!(plan.volume.volume_to_dollar_multiplier, 1.0);

        // Ranks
        assert_eq!(plan.ranks.len(), 3);
        assert_eq!(plan.ranks[0].name, "Associate");
        assert_eq!(plan.ranks[0].ordinal, 1);
        assert_eq!(plan.ranks[1].name, "Silver");
        assert_eq!(plan.ranks[1].ordinal, 2);
        assert_eq!(plan.ranks[2].name, "Gold");
        assert_eq!(plan.ranks[2].ordinal, 3);

        // Rank tracking
        assert!(plan.rank_tracking.track_achieved_rank);

        // Rank features
        assert!(plan.rank_features.constraints_enabled);
        assert!(!plan.rank_features.overrides_enabled);

        // Eligibility
        assert_eq!(plan.eligibility.minimum_pv, 100.0);
        assert!(plan.eligibility.require_order_in_period);
        assert_eq!(plan.eligibility.eligible_statuses, vec!["active", "grace"]);
        assert_eq!(plan.eligibility.active_leg_tiers.len(), 3);

        // Bonuses
        assert!(plan.bonuses.matching.is_some());
        let matching = plan.bonuses.matching.as_ref().unwrap();
        assert_eq!(matching.max_depth, 3);
        assert!(plan.bonuses.sponsor.is_some());
        assert!(plan.bonuses.fast_start.is_none());
        assert!(plan.bonuses.rank_advancement.is_some());

        // Payout
        assert_eq!(plan.payout.currency, "USD");
        assert_eq!(plan.payout.minimum_payout, 50.0);
        assert!(plan.payout.allow_partial_payout);
        assert_eq!(plan.payout.payment_methods.len(), 2);

        // Caps
        assert_eq!(plan.caps.per_distributor_cap, Some(10000.0));
        assert_eq!(plan.caps.company_payout_cap_percent, 0.42);
        assert!(!plan.caps.enable_clawback);

        // Placement
        assert!(plan.placement.donated_placement.is_some());
        assert!(plan.placement.holding_tank.is_none());
        assert!(plan.placement.binary_placement.is_none());
    }

    #[test]
    fn deserialize_structure_config_board_plan() {
        let json = r#"{
            "type": "board_plan",
            "config": {
                "name": "Sales Board",
                "width": 2,
                "height": 2,
                "board_cycling": {
                    "cycle_commission": 500.0,
                    "re_entry_enabled": true,
                    "re_entry_position": "bottom",
                    "max_cycles_per_period": 5,
                    "max_cascade_depth": 10,
                    "stall_threshold_periods": 3,
                    "inactive_compression": true
                }
            }
        }"#;
        let config: StructureConfig = serde_json::from_str(json).unwrap();
        match config {
            StructureConfig::BoardPlan(bp) => {
                assert_eq!(bp.name, "Sales Board");
                assert_eq!(bp.width, 2);
                assert_eq!(bp.height, 2);
                assert_eq!(bp.board_cycling.cycle_commission, 500.0);
                assert!(bp.board_cycling.re_entry_enabled);
                assert_eq!(bp.board_cycling.re_entry_position, ReEntryPosition::Bottom);
                assert_eq!(bp.board_cycling.max_cycles_per_period, 5);
            }
            _ => panic!("expected BoardPlan variant"),
        }
    }
}
