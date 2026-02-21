//! Streamline commission configuration types.
//!
//! Streamline plans use linear chains where rank determines earning
//! position, not just percentage. Dynamic compression skips distributors
//! who don't meet their level's rank threshold. Higher ranks can unlock
//! additional streams, each an independent commission chain.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

/// Streamline commission configuration.
///
/// Linear chains where rank determines earning position, not just
/// percentage. Dynamic compression skips distributors who don't meet
/// their level's rank threshold. The `levels` table defines minimum
/// rank requirements and commission percentages for each depth level.
/// Optional multi-stream support allows higher-ranked distributors to
/// earn on multiple independent chains.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamlineCommissionConfig {
    /// Per-structure CV override. None uses the plan-level multiplier
    /// from `VolumeConfig`.
    pub volume_to_dollar_multiplier: Option<f64>,

    /// Maximum chain depth for commission payments.
    #[serde(rename = "commissionable_depth")]
    pub max_depth: u8,

    /// Dynamic compression table.
    ///
    /// Each entry defines a level with its minimum rank requirement
    /// and commission percentage. Levels are ordered by position
    /// (level 1 first). Non-decreasing rank thresholds. Higher levels
    /// require higher ranks.
    #[serde(rename = "dynamic_compression")]
    pub levels: Vec<StreamlineLevel>,

    /// Multi-stream configuration. None means single stream only.
    #[serde(rename = "streams")]
    pub stream_config: Option<StreamConfig>,
}

/// One level in the streamline dynamic compression table.
///
/// Each level has its own minimum rank requirement. Higher levels
/// require higher ranks. Distributors below the minimum rank for
/// their level are dynamically compressed (skipped). Non-decreasing
/// rank thresholds across the levels vector.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamlineLevel {
    /// The level number (1-indexed).
    ///
    /// Preserved from the Go translation layer. The Go side converts
    /// the map-keyed YAML format into a sorted vector and includes
    /// the level number on each entry.
    pub level: u8,

    /// Minimum rank required to earn at this level.
    ///
    /// Distributors below this rank are dynamically compressed
    /// (skipped).
    pub min_rank: String,

    /// Commission percentage for this level.
    ///
    /// Applied to CV * multiplier.
    pub percent: f64,
}

/// Multi-stream configuration for streamline plans.
///
/// Higher ranks earn additional streams, each an independent
/// commission chain. Excess streams freeze on rank demotion (not
/// destroyed). Frozen streams lose commission eligibility but retain
/// positions.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamConfig {
    /// Rank name to number of additional streams unlocked at that rank.
    ///
    /// Cumulative with lower ranks. For example, if "silver" unlocks 1
    /// and "gold" unlocks 2, a gold-ranked distributor has 1 + 2 = 3
    /// additional streams (plus the primary stream).
    #[serde(rename = "additional_per_rank")]
    pub additional_streams: BTreeMap<String, u8>,

    /// How new enrollees are assigned to streams.
    pub assignment_mode: StreamAssignmentMode,

    /// When true, new enrollees can choose which of their sponsor's
    /// streams to join. When false, assignment is automatic.
    #[serde(rename = "per_enrollment_choice")]
    pub enrollment_stream_choice: bool,

    /// When true, excess streams freeze on rank demotion.
    ///
    /// Frozen streams retain positions but lose commission eligibility.
    /// When false, excess streams are destroyed on demotion.
    pub freeze_on_demotion: bool,
}

/// How new enrollees are assigned to a sponsor's streams.
///
/// Two assignment strategies with different distribution
/// characteristics. Sponsor stream is simple and predictable.
/// Round robin balances stream sizes automatically.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum StreamAssignmentMode {
    /// New enrollees join their sponsor's primary stream.
    ///
    /// Simple and predictable. All new enrollees go to the same
    /// stream unless manually moved.
    SponsorStream,

    /// New enrollees are distributed across the sponsor's streams
    /// evenly.
    ///
    /// Balances stream sizes automatically. Reduces manual management.
    RoundRobin,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_streamline_config() {
        let json = r#"{
            "volume_to_dollar_multiplier": 1.5,
            "commissionable_depth": 7,
            "dynamic_compression": [
                { "level": 1, "min_rank": "active", "percent": 0.05 },
                { "level": 2, "min_rank": "bronze", "percent": 0.04 },
                { "level": 3, "min_rank": "silver", "percent": 0.03 },
                { "level": 4, "min_rank": "gold", "percent": 0.02 },
                { "level": 5, "min_rank": "platinum", "percent": 0.01 }
            ],
            "streams": {
                "additional_per_rank": {
                    "silver": 1,
                    "gold": 2,
                    "platinum": 4
                },
                "assignment_mode": "round_robin",
                "per_enrollment_choice": true,
                "freeze_on_demotion": true
            }
        }"#;
        let config: StreamlineCommissionConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.volume_to_dollar_multiplier, Some(1.5));
        assert_eq!(config.max_depth, 7);
        assert_eq!(config.levels.len(), 5);
        assert_eq!(config.levels[0].level, 1);
        assert_eq!(config.levels[0].min_rank, "active");
        assert_eq!(config.levels[0].percent, 0.05);
        assert_eq!(config.levels[4].level, 5);
        assert_eq!(config.levels[4].min_rank, "platinum");
        assert_eq!(config.levels[4].percent, 0.01);

        let stream_cfg = config.stream_config.as_ref().unwrap();
        assert_eq!(stream_cfg.additional_streams.len(), 3);
        assert_eq!(stream_cfg.additional_streams["silver"], 1);
        assert_eq!(stream_cfg.additional_streams["gold"], 2);
        assert_eq!(stream_cfg.additional_streams["platinum"], 4);
        assert!(matches!(
            stream_cfg.assignment_mode,
            StreamAssignmentMode::RoundRobin
        ));
        assert!(stream_cfg.enrollment_stream_choice);
        assert!(stream_cfg.freeze_on_demotion);
    }

    #[test]
    fn deserialize_streamline_level() {
        let json = r#"{
            "level": 4,
            "min_rank": "gold",
            "percent": 0.03
        }"#;
        let level: StreamlineLevel = serde_json::from_str(json).unwrap();
        assert_eq!(level.level, 4);
        assert_eq!(level.min_rank, "gold");
        assert_eq!(level.percent, 0.03);
    }

    #[test]
    fn deserialize_stream_config() {
        let json = r#"{
            "additional_per_rank": {
                "bronze": 1,
                "silver": 2,
                "gold": 3,
                "diamond": 5
            },
            "assignment_mode": "sponsor_stream",
            "per_enrollment_choice": false,
            "freeze_on_demotion": false
        }"#;
        let config: StreamConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.additional_streams.len(), 4);
        assert_eq!(config.additional_streams["bronze"], 1);
        assert_eq!(config.additional_streams["silver"], 2);
        assert_eq!(config.additional_streams["gold"], 3);
        assert_eq!(config.additional_streams["diamond"], 5);
        assert!(matches!(
            config.assignment_mode,
            StreamAssignmentMode::SponsorStream
        ));
        assert!(!config.enrollment_stream_choice);
        assert!(!config.freeze_on_demotion);
    }

    #[test]
    fn deserialize_stream_assignment_modes() {
        let json_sponsor = r#""sponsor_stream""#;
        let mode: StreamAssignmentMode = serde_json::from_str(json_sponsor).unwrap();
        assert!(matches!(mode, StreamAssignmentMode::SponsorStream));

        let json_robin = r#""round_robin""#;
        let mode: StreamAssignmentMode = serde_json::from_str(json_robin).unwrap();
        assert!(matches!(mode, StreamAssignmentMode::RoundRobin));
    }

    #[test]
    fn deserialize_streamline_minimal() {
        let json = r#"{
            "volume_to_dollar_multiplier": null,
            "commissionable_depth": 5,
            "dynamic_compression": [
                { "level": 1, "min_rank": "active", "percent": 0.10 },
                { "level": 2, "min_rank": "bronze", "percent": 0.05 }
            ],
            "streams": null
        }"#;
        let config: StreamlineCommissionConfig = serde_json::from_str(json).unwrap();
        assert!(config.volume_to_dollar_multiplier.is_none());
        assert_eq!(config.max_depth, 5);
        assert_eq!(config.levels.len(), 2);
        assert_eq!(config.levels[0].level, 1);
        assert_eq!(config.levels[0].min_rank, "active");
        assert_eq!(config.levels[0].percent, 0.10);
        assert_eq!(config.levels[1].level, 2);
        assert_eq!(config.levels[1].min_rank, "bronze");
        assert_eq!(config.levels[1].percent, 0.05);
        assert!(config.stream_config.is_none());
    }
}
