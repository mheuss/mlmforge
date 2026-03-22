//! Bonus program configuration types.
//!
//! Bonus programs supplement the core level commission structure.
//! Each bonus is independently optional. A plan can activate any
//! combination by populating the corresponding field in `BonusConfig`.

use std::collections::BTreeMap;

use serde::{Deserialize, Deserializer, Serialize};

/// Deserializes a value, treating JSON `null` or a missing field as
/// `Default::default()`. Needed because Go serializes nil slices as
/// JSON `null`, and serde's `#[serde(default)]` only handles missing
/// fields, not explicit `null`.
fn deserialize_null_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    let opt = Option::deserialize(deserializer)?;
    Ok(opt.unwrap_or_default())
}

// ---------------------------------------------------------------------------
// Top-level container
// ---------------------------------------------------------------------------

/// Container for all bonus programs in a compensation plan.
///
/// Each field is optional. A plan activates a bonus by populating the
/// corresponding field. Multiple bonuses can coexist. The engine
/// evaluates each active bonus independently during a commission run.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BonusConfig {
    /// Matching bonus. Walks the sponsor chain and pays a percentage
    /// of downline commissions to upline sponsors.
    pub matching: Option<MatchingBonusConfig>,

    /// Sponsor bonus. One-time payment on recruitment. Separate from
    /// level commissions.
    pub sponsor: Option<SponsorBonusConfig>,

    /// Fast start bonus. Enhanced commission rates during a new
    /// distributor's enrollment window.
    pub fast_start: Option<FastStartBonusConfig>,

    /// Rank advancement bonus. One-time or recurring payment when a
    /// distributor achieves a new rank.
    pub rank_advancement: Option<RankAdvancementBonusConfig>,

    /// Leadership development bonus. Pays upline when downline
    /// distributors advance in rank.
    pub leadership_development: Option<LeadershipDevelopmentBonusConfig>,

    /// Infinity bonus. Walks the sponsor chain with no depth limit.
    /// A blocker condition stops the walk.
    pub infinity: Option<InfinityBonusConfig>,

    /// Lifestyle bonus. Tiered monthly bonus based on rank.
    pub lifestyle: Option<LifestyleBonusConfig>,

    /// Pool bonuses. Multiple pools can coexist. Each pool allocates
    /// a percentage of company volume to qualified distributors.
    pub pool: Option<Vec<PoolBonusConfig>>,

    /// Matrix completion bonus. Pays when levels of a forced matrix
    /// are fully filled.
    pub matrix_completion: Option<MatrixCompletionBonusConfig>,

    /// Position bonus. Fixed or percentage amount paid per qualifying
    /// position in the tree.
    pub position: Option<PositionBonusConfig>,

    /// Board cycling bonus. Deferred from initial release.
    pub board_cycling: Option<BoardCyclingConfig>,
}

// ---------------------------------------------------------------------------
// Matching bonus
// ---------------------------------------------------------------------------

/// Matching bonus configuration.
///
/// Walks the SPONSOR chain (not the placement tree). Rewards mentorship
/// by paying upline sponsors a percentage of their downline's commissions.
/// Level 1 is the direct sponsor.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MatchingBonusConfig {
    /// How many sponsor-chain levels deep to pay matching bonuses.
    #[serde(rename = "depth")]
    pub max_depth: u8,

    /// Level (1-indexed) to matching percentage. Level 1 is the direct
    /// sponsor. Levels not listed receive no matching bonus.
    pub rates: BTreeMap<u8, f64>,

    /// Which commission types are matched. References commission type
    /// names such as "level" or "binary".
    pub matched_commission_types: Vec<String>,
}

// ---------------------------------------------------------------------------
// Sponsor bonus
// ---------------------------------------------------------------------------

/// Sponsor bonus configuration.
///
/// One-time payment triggered by recruitment. Separate from level
/// commissions. The amount can be a fixed dollar value or a percentage
/// of the recruit's initial order volume.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SponsorBonusConfig {
    /// Fixed dollar amount or percentage, depending on `amount_type`.
    pub amount: f64,

    /// Whether `amount` is a fixed dollar value or a percentage.
    pub amount_type: AmountType,

    /// Product IDs that qualify for the sponsor bonus. Empty means
    /// all products qualify.
    #[serde(default, deserialize_with = "deserialize_null_default")]
    pub qualifying_products: Vec<String>,
}

/// Whether an amount is fixed or percentage-based.
///
/// Shared by sponsor and position bonuses.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AmountType {
    /// A fixed dollar amount.
    Fixed,

    /// A percentage of the triggering volume.
    Percentage,
}

// ---------------------------------------------------------------------------
// Fast start bonus
// ---------------------------------------------------------------------------

/// Fast start bonus configuration.
///
/// Enhanced commission rates that apply during a new distributor's
/// enrollment window. After the window expires, standard rates from
/// the level commission rate table take effect. Levels not listed in
/// the enhanced table fall back to standard rates immediately.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FastStartBonusConfig {
    /// Number of days after enrollment during which enhanced rates
    /// apply.
    pub window_days: u16,

    /// Enhanced rate table. Same structure as the level commission
    /// rate table. Outer key is rank name. Inner key is level
    /// (1-indexed). Value is the enhanced percentage.
    #[serde(rename = "rate_table")]
    pub enhanced_rate_table: BTreeMap<String, BTreeMap<u8, f64>>,
}

// ---------------------------------------------------------------------------
// Rank advancement bonus
// ---------------------------------------------------------------------------

/// Rank advancement bonus configuration.
///
/// Pays a bonus when a distributor achieves a new rank. When
/// `pay_once_only` is true, each rank bonus is paid only the first
/// time achieved. Requires `track_achieved_rank` in `RankTrackingConfig`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RankAdvancementBonusConfig {
    /// Rank name to bonus amount paid when reaching that rank.
    pub amounts: BTreeMap<String, f64>,

    /// When true, each rank bonus is paid only the first time achieved.
    /// Requires `track_achieved_rank` in `RankTrackingConfig`.
    pub pay_once_only: bool,
}

// ---------------------------------------------------------------------------
// Leadership development bonus
// ---------------------------------------------------------------------------

/// Leadership development bonus configuration.
///
/// Pays upline distributors when their downline advances in rank.
/// Walks the sponsor chain up to `max_depth` levels.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LeadershipDevelopmentBonusConfig {
    /// How many sponsor-chain levels deep to pay.
    #[serde(rename = "depth")]
    pub max_depth: u8,

    /// Level (1-indexed) to bonus amount or percentage. Consistent
    /// with MatchingBonusConfig.rates which also uses `u8` keys.
    pub rates: BTreeMap<u8, f64>,

    /// How to handle multi-rank jumps.
    pub rank_skip_mode: RankSkipMode,
}

/// How to handle multi-rank jumps in leadership development bonuses.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RankSkipMode {
    /// Only pay for the final rank achieved. Simpler. Costs less.
    HighestOnly,

    /// Pay for every rank passed through. More generous. Higher cost.
    EachRankPassed,
}

// ---------------------------------------------------------------------------
// Infinity bonus
// ---------------------------------------------------------------------------

/// Infinity bonus configuration.
///
/// Walks the sponsor chain with no depth limit. A blocker condition
/// stops the walk. The rate can be flat (same at every generation)
/// or decreasing (rate decreases per generation).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InfinityBonusConfig {
    /// What stops the infinity walk.
    pub blocker_mode: BlockerMode,

    /// Used when `rate_mode` is `Flat`. The same rate at every
    /// generation.
    pub flat_rate: Option<f64>,

    /// Used when `rate_mode` is `Decreasing`. Generation (1-indexed)
    /// to rate. Rate decreases per generation.
    pub decreasing_rates: Option<BTreeMap<u8, f64>>,

    /// Whether rates are flat or decreasing per generation.
    pub rate_mode: InfinityRateMode,
}

/// What stops the infinity bonus walk.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BlockerMode {
    /// Walk stops when encountering a distributor at the same rank.
    SameRank,

    /// Walk stops at a higher rank.
    HigherRank,

    /// Walk stops at same or higher rank. Most common.
    SameOrHigher,
}

/// Whether infinity bonus rates are flat or decreasing.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum InfinityRateMode {
    /// Same rate at every generation.
    Flat,

    /// Rate decreases per generation.
    Decreasing,
}

// ---------------------------------------------------------------------------
// Lifestyle bonus
// ---------------------------------------------------------------------------

/// Lifestyle bonus configuration.
///
/// Tiered monthly bonus based on rank. A distributor earns the highest
/// qualifying tier only, not cumulative across tiers.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LifestyleBonusConfig {
    /// Lifestyle tiers, ordered by `min_rank` ascending.
    pub tiers: Vec<LifestyleTier>,
}

/// A single lifestyle bonus tier.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LifestyleTier {
    /// Minimum rank to qualify for this tier.
    pub min_rank: String,

    /// Monthly (per-period) bonus amount.
    pub amount: f64,

    /// Number of periods the bonus continues after falling below rank.
    /// 0 means no grace.
    pub grace_periods: u8,
}

// ---------------------------------------------------------------------------
// Pool bonus
// ---------------------------------------------------------------------------

/// Pool bonus configuration.
///
/// A percentage of company volume is allocated to a pool and divided
/// among qualified distributors. Multiple pools can coexist in a plan.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PoolBonusConfig {
    /// Display name for this pool.
    pub name: String,

    /// Percentage of company volume allocated to this pool.
    pub source_percent: f64,

    /// How distributors qualify for the pool.
    pub qualification: PoolQualification,

    /// How pool funds are divided among qualified distributors.
    pub shares: PoolShares,

    /// Whether an admin must approve pool payouts.
    pub require_admin_confirmation: bool,
}

/// How distributors qualify for a pool bonus.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PoolQualification {
    /// How qualification is determined.
    pub mode: PoolQualificationMode,

    /// Minimum rank for rank-based qualification. Required when mode
    /// is `RankBased` or `Combined`.
    pub min_rank: Option<String>,

    /// Velocity criteria. Required when mode is `VelocityBased` or
    /// `Combined`.
    pub velocity: Option<VelocityQualification>,
}

/// How pool qualification is determined.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PoolQualificationMode {
    /// Qualify by reaching a minimum rank.
    RankBased,

    /// Qualify by hitting volume targets in a time window.
    VelocityBased,

    /// Both rank and velocity required.
    Combined,
}

/// Velocity criteria for pool qualification.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VelocityQualification {
    /// Volume that must be reached.
    pub volume_target: f64,

    /// Time window for the target.
    pub timeframe: VelocityTimeframe,

    /// Number of days when `timeframe` is `Days`. Ignored otherwise.
    pub timeframe_days: Option<u16>,
}

/// Time window for velocity qualification.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum VelocityTimeframe {
    /// Within a single commission period.
    Period,

    /// Within a specific number of days.
    Days,
}

/// How pool funds are divided among qualified distributors.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PoolShares {
    /// How shares are calculated.
    pub mode: PoolShareMode,

    /// Maximum per-distributor share when mode is `EqualShare`.
    pub equal_share_cap: Option<f64>,
}

/// How pool shares are calculated.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PoolShareMode {
    /// Divide equally among all qualified distributors.
    EqualShare,

    /// Weight shares by rank. Higher rank receives more shares.
    RankWeighted,

    /// Weight shares by personal volume.
    VolumeWeighted,
}

// ---------------------------------------------------------------------------
// Matrix completion bonus
// ---------------------------------------------------------------------------

/// Matrix completion bonus configuration.
///
/// Pays bonuses when levels of a forced matrix are fully filled.
/// A separate bonus is paid when the entire matrix is complete.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MatrixCompletionBonusConfig {
    /// Level (1-indexed) to bonus amount when that level is fully
    /// filled.
    #[serde(rename = "per_level")]
    pub per_level_amounts: BTreeMap<u8, f64>,

    /// Bonus paid when the entire matrix is complete.
    #[serde(rename = "full_matrix")]
    pub full_matrix_amount: f64,
}

// ---------------------------------------------------------------------------
// Position bonus
// ---------------------------------------------------------------------------

/// Position bonus configuration.
///
/// Pays a fixed or percentage amount per qualifying position in the
/// tree.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PositionBonusConfig {
    /// Fixed or percentage amount.
    pub amount: f64,

    /// Whether `amount` is a fixed dollar value or a percentage.
    pub amount_type: AmountType,

    /// When true, only personally sponsored positions qualify.
    pub sponsored_only: bool,
}

// ---------------------------------------------------------------------------
// Board cycling (deferred)
// ---------------------------------------------------------------------------

/// Board cycling configuration. Deferred from initial release.
///
/// This is a placeholder struct. Fields will be added when board cycling
/// is implemented.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoardCyclingConfig {
    /// Placeholder for future board cycling implementation.
    #[serde(default)]
    pub _reserved: Option<bool>,
}

// ---------------------------------------------------------------------------
// Pass-up
// ---------------------------------------------------------------------------

/// Pass-up configuration.
///
/// First N recruits are passed to the sponsor. Only for unilevel and
/// generation structures.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PassUpConfig {
    /// Number of first recruits passed to sponsor.
    pub count: u8,

    /// When true, commissions from passed-up recruits also go to
    /// the sponsor.
    pub includes_commissions: bool,
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;

    #[test]
    fn deserialize_bonus_config() {
        let json = r#"{
            "matching": {
                "depth": 3,
                "rates": { "1": 0.50, "2": 0.25, "3": 0.10 },
                "matched_commission_types": ["unilevel", "binary"]
            },
            "sponsor": {
                "amount": 25.0,
                "amount_type": "fixed",
                "qualifying_products": ["starter-kit"]
            },
            "fast_start": null,
            "rank_advancement": null,
            "leadership_development": null,
            "infinity": null,
            "lifestyle": null,
            "pool": [
                {
                    "name": "Global Leadership Pool",
                    "source_percent": 0.02,
                    "qualification": {
                        "mode": "rank_based",
                        "min_rank": "diamond",
                        "velocity": null
                    },
                    "shares": {
                        "mode": "equal_share",
                        "equal_share_cap": 10000.0
                    },
                    "require_admin_confirmation": false
                }
            ],
            "matrix_completion": null,
            "position": null,
            "board_cycling": null
        }"#;
        let config: BonusConfig = serde_json::from_str(json).unwrap();
        assert!(config.matching.is_some());
        assert!(config.sponsor.is_some());
        assert!(config.fast_start.is_none());
        assert!(config.rank_advancement.is_none());
        assert!(config.leadership_development.is_none());
        assert!(config.infinity.is_none());
        assert!(config.lifestyle.is_none());
        assert!(config.pool.is_some());
        assert_eq!(config.pool.unwrap().len(), 1);
        assert!(config.matrix_completion.is_none());
        assert!(config.position.is_none());
        assert!(config.board_cycling.is_none());
    }

    #[test]
    fn deserialize_matching_bonus() {
        let json = r#"{
            "depth": 5,
            "rates": { "1": 0.50, "2": 0.25, "3": 0.10, "4": 0.05, "5": 0.02 },
            "matched_commission_types": ["unilevel"]
        }"#;
        let config: MatchingBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.max_depth, 5);
        assert_eq!(config.rates.len(), 5);
        assert_eq!(config.rates[&1], 0.50);
        assert_eq!(config.rates[&2], 0.25);
        assert_eq!(config.rates[&3], 0.10);
        assert_eq!(config.rates[&4], 0.05);
        assert_eq!(config.rates[&5], 0.02);
        assert_eq!(config.matched_commission_types, vec!["unilevel"]);
    }

    #[test]
    fn deserialize_sponsor_bonus() {
        let json = r#"{
            "amount": 50.0,
            "amount_type": "fixed",
            "qualifying_products": []
        }"#;
        let config: SponsorBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.amount, 50.0);
        assert!(matches!(config.amount_type, AmountType::Fixed));
        assert!(config.qualifying_products.is_empty());
    }

    #[test]
    fn deserialize_sponsor_bonus_null_qualifying_products() {
        let json = r#"{
            "amount": 25.0,
            "amount_type": "percentage",
            "qualifying_products": null
        }"#;
        let config: SponsorBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.amount, 25.0);
        assert!(matches!(config.amount_type, AmountType::Percentage));
        assert!(config.qualifying_products.is_empty());
    }

    #[test]
    fn deserialize_sponsor_bonus_missing_qualifying_products() {
        let json = r#"{
            "amount": 25.0,
            "amount_type": "percentage"
        }"#;
        let config: SponsorBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.amount, 25.0);
        assert!(matches!(config.amount_type, AmountType::Percentage));
        assert!(config.qualifying_products.is_empty());
    }

    #[test]
    fn deserialize_fast_start_bonus() {
        let json = r#"{
            "window_days": 90,
            "rate_table": {
                "associate": {
                    "1": 0.10,
                    "2": 0.08,
                    "3": 0.06
                },
                "silver": {
                    "1": 0.12,
                    "2": 0.10
                }
            }
        }"#;
        let config: FastStartBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.window_days, 90);
        assert_eq!(config.enhanced_rate_table.len(), 2);

        let associate = &config.enhanced_rate_table["associate"];
        assert_eq!(associate.len(), 3);
        assert_eq!(associate[&1], 0.10);
        assert_eq!(associate[&3], 0.06);

        let silver = &config.enhanced_rate_table["silver"];
        assert_eq!(silver.len(), 2);
        assert_eq!(silver[&1], 0.12);
    }

    #[test]
    fn deserialize_pool_bonus() {
        let json = r#"{
            "name": "Velocity Pool",
            "source_percent": 0.03,
            "qualification": {
                "mode": "velocity_based",
                "min_rank": null,
                "velocity": {
                    "volume_target": 5000.0,
                    "timeframe": "days",
                    "timeframe_days": 30
                }
            },
            "shares": {
                "mode": "volume_weighted",
                "equal_share_cap": null
            },
            "require_admin_confirmation": true
        }"#;
        let config: PoolBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.name, "Velocity Pool");
        assert_eq!(config.source_percent, 0.03);
        assert!(matches!(
            config.qualification.mode,
            PoolQualificationMode::VelocityBased
        ));
        assert!(config.qualification.min_rank.is_none());
        let velocity = config.qualification.velocity.as_ref().unwrap();
        assert_eq!(velocity.volume_target, 5000.0);
        assert!(matches!(velocity.timeframe, VelocityTimeframe::Days));
        assert_eq!(velocity.timeframe_days, Some(30));
        assert!(matches!(config.shares.mode, PoolShareMode::VolumeWeighted));
        assert!(config.shares.equal_share_cap.is_none());
        assert!(config.require_admin_confirmation);
    }

    #[test]
    fn deserialize_infinity_bonus() {
        let json = r#"{
            "blocker_mode": "same_or_higher",
            "flat_rate": null,
            "decreasing_rates": { "1": 0.05, "2": 0.04, "3": 0.03 },
            "rate_mode": "decreasing"
        }"#;
        let config: InfinityBonusConfig = serde_json::from_str(json).unwrap();
        assert!(matches!(config.blocker_mode, BlockerMode::SameOrHigher));
        assert!(config.flat_rate.is_none());
        let rates = config.decreasing_rates.as_ref().unwrap();
        assert_eq!(rates.len(), 3);
        assert_eq!(rates[&1], 0.05);
        assert_eq!(rates[&3], 0.03);
        assert!(matches!(config.rate_mode, InfinityRateMode::Decreasing));
    }

    #[test]
    fn deserialize_amount_types() {
        let json_fixed = r#""fixed""#;
        let at: AmountType = serde_json::from_str(json_fixed).unwrap();
        assert!(matches!(at, AmountType::Fixed));

        let json_pct = r#""percentage""#;
        let at: AmountType = serde_json::from_str(json_pct).unwrap();
        assert!(matches!(at, AmountType::Percentage));
    }

    #[test]
    fn deserialize_blocker_modes() {
        let json_same = r#""same_rank""#;
        let bm: BlockerMode = serde_json::from_str(json_same).unwrap();
        assert!(matches!(bm, BlockerMode::SameRank));

        let json_higher = r#""higher_rank""#;
        let bm: BlockerMode = serde_json::from_str(json_higher).unwrap();
        assert!(matches!(bm, BlockerMode::HigherRank));

        let json_both = r#""same_or_higher""#;
        let bm: BlockerMode = serde_json::from_str(json_both).unwrap();
        assert!(matches!(bm, BlockerMode::SameOrHigher));
    }

    #[test]
    fn deserialize_rank_advancement_bonus() {
        let json = r#"{
            "amounts": {
                "silver": 500.0,
                "gold": 1000.0,
                "diamond": 5000.0
            },
            "pay_once_only": true
        }"#;
        let config: RankAdvancementBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.amounts.len(), 3);
        assert_eq!(config.amounts["silver"], 500.0);
        assert_eq!(config.amounts["gold"], 1000.0);
        assert_eq!(config.amounts["diamond"], 5000.0);
        assert!(config.pay_once_only);
    }

    #[test]
    fn deserialize_leadership_development_bonus() {
        let json = r#"{
            "depth": 4,
            "rates": {
                "1": 100.0,
                "2": 250.0,
                "3": 500.0
            },
            "rank_skip_mode": "highest_only"
        }"#;
        let config: LeadershipDevelopmentBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.max_depth, 4);
        assert_eq!(config.rates.len(), 3);
        assert_eq!(config.rates[&1], 100.0);
        assert_eq!(config.rates[&2], 250.0);
        assert_eq!(config.rates[&3], 500.0);
        assert!(matches!(config.rank_skip_mode, RankSkipMode::HighestOnly));
    }

    #[test]
    fn deserialize_rank_skip_modes() {
        let json_highest = r#""highest_only""#;
        let mode: RankSkipMode = serde_json::from_str(json_highest).unwrap();
        assert!(matches!(mode, RankSkipMode::HighestOnly));

        let json_each = r#""each_rank_passed""#;
        let mode: RankSkipMode = serde_json::from_str(json_each).unwrap();
        assert!(matches!(mode, RankSkipMode::EachRankPassed));
    }

    #[test]
    fn deserialize_lifestyle_bonus() {
        let json = r#"{
            "tiers": [
                { "min_rank": "gold", "amount": 500.0, "grace_periods": 2 },
                { "min_rank": "diamond", "amount": 1500.0, "grace_periods": 3 }
            ]
        }"#;
        let config: LifestyleBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.tiers.len(), 2);
        assert_eq!(config.tiers[0].min_rank, "gold");
        assert_eq!(config.tiers[0].amount, 500.0);
        assert_eq!(config.tiers[0].grace_periods, 2);
        assert_eq!(config.tiers[1].min_rank, "diamond");
        assert_eq!(config.tiers[1].amount, 1500.0);
        assert_eq!(config.tiers[1].grace_periods, 3);
    }

    #[test]
    fn deserialize_matrix_completion_bonus() {
        let json = r#"{
            "per_level": { "1": 100.0, "2": 200.0, "3": 500.0 },
            "full_matrix": 2000.0
        }"#;
        let config: MatrixCompletionBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.per_level_amounts.len(), 3);
        assert_eq!(config.per_level_amounts[&1], 100.0);
        assert_eq!(config.per_level_amounts[&2], 200.0);
        assert_eq!(config.per_level_amounts[&3], 500.0);
        assert_eq!(config.full_matrix_amount, 2000.0);
    }

    #[test]
    fn deserialize_position_bonus() {
        let json = r#"{
            "amount": 0.05,
            "amount_type": "percentage",
            "sponsored_only": true
        }"#;
        let config: PositionBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.amount, 0.05);
        assert!(matches!(config.amount_type, AmountType::Percentage));
        assert!(config.sponsored_only);
    }

    #[test]
    fn deserialize_board_cycling() {
        let json = r#"{}"#;
        let _config: BoardCyclingConfig = serde_json::from_str(json).unwrap();
    }

    #[test]
    fn deserialize_pass_up() {
        let json = r#"{
            "count": 2,
            "includes_commissions": true
        }"#;
        let config: PassUpConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.count, 2);
        assert!(config.includes_commissions);
    }

    #[test]
    fn deserialize_infinity_bonus_flat_rate() {
        let json = r#"{
            "blocker_mode": "same_rank",
            "flat_rate": 0.03,
            "decreasing_rates": null,
            "rate_mode": "flat"
        }"#;
        let config: InfinityBonusConfig = serde_json::from_str(json).unwrap();
        assert!(matches!(config.blocker_mode, BlockerMode::SameRank));
        assert_eq!(config.flat_rate, Some(0.03));
        assert!(config.decreasing_rates.is_none());
        assert!(matches!(config.rate_mode, InfinityRateMode::Flat));
    }

    #[test]
    fn deserialize_infinity_rate_modes() {
        let json_flat = r#""flat""#;
        let mode: InfinityRateMode = serde_json::from_str(json_flat).unwrap();
        assert!(matches!(mode, InfinityRateMode::Flat));

        let json_dec = r#""decreasing""#;
        let mode: InfinityRateMode = serde_json::from_str(json_dec).unwrap();
        assert!(matches!(mode, InfinityRateMode::Decreasing));
    }

    #[test]
    fn deserialize_pool_qualification_modes() {
        let json_rank = r#""rank_based""#;
        let mode: PoolQualificationMode = serde_json::from_str(json_rank).unwrap();
        assert!(matches!(mode, PoolQualificationMode::RankBased));

        let json_velocity = r#""velocity_based""#;
        let mode: PoolQualificationMode = serde_json::from_str(json_velocity).unwrap();
        assert!(matches!(mode, PoolQualificationMode::VelocityBased));

        let json_combined = r#""combined""#;
        let mode: PoolQualificationMode = serde_json::from_str(json_combined).unwrap();
        assert!(matches!(mode, PoolQualificationMode::Combined));
    }

    #[test]
    fn deserialize_velocity_timeframes() {
        let json_period = r#""period""#;
        let tf: VelocityTimeframe = serde_json::from_str(json_period).unwrap();
        assert!(matches!(tf, VelocityTimeframe::Period));

        let json_days = r#""days""#;
        let tf: VelocityTimeframe = serde_json::from_str(json_days).unwrap();
        assert!(matches!(tf, VelocityTimeframe::Days));
    }

    #[test]
    fn deserialize_pool_share_modes() {
        let json_equal = r#""equal_share""#;
        let mode: PoolShareMode = serde_json::from_str(json_equal).unwrap();
        assert!(matches!(mode, PoolShareMode::EqualShare));

        let json_rank = r#""rank_weighted""#;
        let mode: PoolShareMode = serde_json::from_str(json_rank).unwrap();
        assert!(matches!(mode, PoolShareMode::RankWeighted));

        let json_volume = r#""volume_weighted""#;
        let mode: PoolShareMode = serde_json::from_str(json_volume).unwrap();
        assert!(matches!(mode, PoolShareMode::VolumeWeighted));
    }

    #[test]
    fn deserialize_pool_combined_qualification() {
        let json = r#"{
            "name": "Combined Pool",
            "source_percent": 0.01,
            "qualification": {
                "mode": "combined",
                "min_rank": "gold",
                "velocity": {
                    "volume_target": 10000.0,
                    "timeframe": "period",
                    "timeframe_days": null
                }
            },
            "shares": {
                "mode": "rank_weighted",
                "equal_share_cap": null
            },
            "require_admin_confirmation": false
        }"#;
        let config: PoolBonusConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.name, "Combined Pool");
        assert!(matches!(
            config.qualification.mode,
            PoolQualificationMode::Combined
        ));
        assert_eq!(config.qualification.min_rank, Some("gold".to_string()));
        let velocity = config.qualification.velocity.as_ref().unwrap();
        assert_eq!(velocity.volume_target, 10000.0);
        assert!(matches!(velocity.timeframe, VelocityTimeframe::Period));
        assert!(velocity.timeframe_days.is_none());
        assert!(matches!(config.shares.mode, PoolShareMode::RankWeighted));
    }

    #[test]
    fn round_trip_bonus_config() {
        let mut rates = BTreeMap::new();
        rates.insert(1, 0.50);
        rates.insert(2, 0.25);

        let config = BonusConfig {
            matching: Some(MatchingBonusConfig {
                max_depth: 2,
                rates,
                matched_commission_types: vec!["unilevel".to_string()],
            }),
            sponsor: None,
            fast_start: None,
            rank_advancement: None,
            leadership_development: None,
            infinity: None,
            lifestyle: None,
            pool: None,
            matrix_completion: None,
            position: None,
            board_cycling: None,
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: BonusConfig = serde_json::from_str(&json).unwrap();
        let matching = deserialized.matching.unwrap();
        assert_eq!(matching.max_depth, 2);
        assert_eq!(matching.rates[&1], 0.50);
        assert_eq!(matching.rates[&2], 0.25);
        assert_eq!(matching.matched_commission_types, vec!["unilevel"]);
    }

    #[test]
    fn round_trip_sponsor_bonus() {
        let config = SponsorBonusConfig {
            amount: 100.0,
            amount_type: AmountType::Percentage,
            qualifying_products: vec!["premium-kit".to_string()],
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: SponsorBonusConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized.amount, 100.0);
        assert!(matches!(deserialized.amount_type, AmountType::Percentage));
        assert_eq!(deserialized.qualifying_products, vec!["premium-kit"]);
    }

    #[test]
    fn round_trip_pass_up() {
        let config = PassUpConfig {
            count: 3,
            includes_commissions: false,
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: PassUpConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized.count, 3);
        assert!(!deserialized.includes_commissions);
    }
}
