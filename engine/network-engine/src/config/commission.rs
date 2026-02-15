//! Level commission and compression types.
//!
//! These types are shared across unilevel, matrix, and stairstep
//! structures. Each structure composes these into its own config wrapper.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

/// Level commission configuration.
///
/// The bread and butter of most compensation plans. Pays commissions
/// on multiple levels of depth below the earning distributor. Each
/// rank unlocks a different set of rates per level.
///
/// Commission formula:
/// `CV * broad_commission_percent * volume_to_dollar_multiplier * rate_table[rank][level]`
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LevelCommissionConfig {
    /// The base commission percentage allocated to the entire level
    /// commission pool.
    ///
    /// Typical range 30-50%. Represents the fraction of commissionable
    /// volume set aside for level commissions before per-rank rates
    /// are applied.
    pub broad_commission_percent: f64,

    /// Per-structure override for converting CV points to currency.
    ///
    /// When `None`, the plan-level multiplier from `VolumeConfig` is
    /// used. Allows individual structures to adjust the CV-to-dollar
    /// ratio independently.
    pub volume_to_dollar_multiplier: Option<f64>,

    /// Maximum number of levels deep commissions are paid.
    ///
    /// Determines the rate table size. A distributor can never earn
    /// beyond this depth regardless of rank. Typical values: 3-10.
    #[serde(rename = "commissionable_depth")]
    pub max_depth: u8,

    /// Commission rates by rank and level.
    ///
    /// Outer key is rank name. Inner key is level (1-indexed). Value
    /// is a percentage between 0.0 and 1.0. A missing rank means that
    /// rank earns no level commissions. A missing level within a rank
    /// means no commission at that depth.
    ///
    /// Example: `rate_table["silver"][3] = 0.05` means Silver-ranked
    /// distributors earn 5% on level 3.
    pub rate_table: BTreeMap<String, BTreeMap<u8, f64>>,
}

/// Compression configuration for level commissions.
///
/// Compression removes unqualified distributors from the payout chain
/// so commissions pass through to the next qualified upline. Skipping
/// does NOT consume a level. If levels 3-5 are unqualified, the next
/// qualified person earns at level 3's rate. This preserves the full
/// commissionable depth.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CompressionConfig {
    /// Master switch for compression.
    ///
    /// When false, unqualified distributors simply create gaps in the
    /// payout chain. Their commission is forfeited, not redistributed.
    pub enabled: bool,

    /// Which distributors get skipped during compression.
    pub mode: CompressionMode,

    /// Required when mode is `SkipBelowRank`. Distributors below this
    /// rank are compressed out of the payout chain.
    ///
    /// Must reference a rank name defined in the plan. Ignored when
    /// mode is `SkipInactive`.
    pub rank_threshold: Option<String>,
}

/// Determines which distributors are compressed out of the payout chain.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CompressionMode {
    /// Skip distributors who don't meet commission eligibility.
    ///
    /// The simplest and most common mode. A distributor who fails the
    /// eligibility check (minimum PV, active status, etc.) is removed
    /// from the chain and their level is not consumed.
    SkipInactive,

    /// Skip distributors below a specified rank.
    ///
    /// Used to concentrate commissions among qualified leaders.
    /// Requires `rank_threshold` on `CompressionConfig` to specify
    /// the minimum rank for participation.
    SkipBelowRank,
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;

    #[test]
    fn deserialize_level_commission_config() {
        let json = r#"{
            "broad_commission_percent": 0.40,
            "volume_to_dollar_multiplier": 1.5,
            "commissionable_depth": 5,
            "rate_table": {
                "associate": {
                    "1": 0.05,
                    "2": 0.04,
                    "3": 0.03
                },
                "silver": {
                    "1": 0.07,
                    "2": 0.06,
                    "3": 0.05,
                    "4": 0.04,
                    "5": 0.03
                }
            }
        }"#;
        let config: LevelCommissionConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.broad_commission_percent, 0.40);
        assert_eq!(config.volume_to_dollar_multiplier, Some(1.5));
        assert_eq!(config.max_depth, 5);
        assert_eq!(config.rate_table.len(), 2);

        let associate = &config.rate_table["associate"];
        assert_eq!(associate.len(), 3);
        assert_eq!(associate[&1], 0.05);
        assert_eq!(associate[&2], 0.04);
        assert_eq!(associate[&3], 0.03);

        let silver = &config.rate_table["silver"];
        assert_eq!(silver.len(), 5);
        assert_eq!(silver[&1], 0.07);
        assert_eq!(silver[&5], 0.03);
    }

    #[test]
    fn deserialize_level_commission_config_no_multiplier() {
        let json = r#"{
            "broad_commission_percent": 0.35,
            "volume_to_dollar_multiplier": null,
            "commissionable_depth": 3,
            "rate_table": {
                "associate": {
                    "1": 0.05
                }
            }
        }"#;
        let config: LevelCommissionConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.broad_commission_percent, 0.35);
        assert_eq!(config.volume_to_dollar_multiplier, None);
        assert_eq!(config.max_depth, 3);
    }

    #[test]
    fn deserialize_compression_skip_inactive() {
        let json = r#"{
            "enabled": true,
            "mode": "skip_inactive",
            "rank_threshold": null
        }"#;
        let config: CompressionConfig = serde_json::from_str(json).unwrap();
        assert!(config.enabled);
        assert!(matches!(config.mode, CompressionMode::SkipInactive));
        assert!(config.rank_threshold.is_none());
    }

    #[test]
    fn deserialize_compression_skip_below_rank() {
        let json = r#"{
            "enabled": true,
            "mode": "skip_below_rank",
            "rank_threshold": "silver"
        }"#;
        let config: CompressionConfig = serde_json::from_str(json).unwrap();
        assert!(config.enabled);
        assert!(matches!(config.mode, CompressionMode::SkipBelowRank));
        assert_eq!(config.rank_threshold, Some("silver".to_string()));
    }

    #[test]
    fn deserialize_compression_disabled() {
        let json = r#"{
            "enabled": false,
            "mode": "skip_inactive",
            "rank_threshold": null
        }"#;
        let config: CompressionConfig = serde_json::from_str(json).unwrap();
        assert!(!config.enabled);
    }

    #[test]
    fn deserialize_compression_modes() {
        let json_inactive = r#""skip_inactive""#;
        let mode: CompressionMode = serde_json::from_str(json_inactive).unwrap();
        assert!(matches!(mode, CompressionMode::SkipInactive));

        let json_below_rank = r#""skip_below_rank""#;
        let mode: CompressionMode = serde_json::from_str(json_below_rank).unwrap();
        assert!(matches!(mode, CompressionMode::SkipBelowRank));
    }

    #[test]
    fn round_trip_level_commission() {
        let mut associate_rates = BTreeMap::new();
        associate_rates.insert(1, 0.05);
        associate_rates.insert(2, 0.04);
        associate_rates.insert(3, 0.03);

        let mut rate_table = BTreeMap::new();
        rate_table.insert("associate".to_string(), associate_rates);

        let config = LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: Some(1.0),
            max_depth: 3,
            rate_table,
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: LevelCommissionConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized.broad_commission_percent, 0.40);
        assert_eq!(deserialized.volume_to_dollar_multiplier, Some(1.0));
        assert_eq!(deserialized.max_depth, 3);
        assert_eq!(deserialized.rate_table["associate"][&1], 0.05);
        assert_eq!(deserialized.rate_table["associate"][&3], 0.03);
    }

    #[test]
    fn round_trip_compression_config() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("gold".to_string()),
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: CompressionConfig = serde_json::from_str(&json).unwrap();
        assert!(deserialized.enabled);
        assert!(matches!(deserialized.mode, CompressionMode::SkipBelowRank));
        assert_eq!(deserialized.rank_threshold, Some("gold".to_string()));
    }
}
