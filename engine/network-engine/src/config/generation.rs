//! Generation commission configuration types.
//!
//! Generation plans pay commissions on generations of qualified leaders,
//! not tree depth. A generation boundary is created when a downline
//! leader meets a rank threshold. The earning depth grows as leaders
//! build leaders. There is no fixed depth limit.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

/// Generation commission configuration.
///
/// Pays on generations of qualified leaders, not tree depth. No fixed
/// depth limit. Earning depth grows as leaders build leaders. A
/// generation boundary is created each time a downline leader meets
/// the boundary rank criteria. Generation 1 is the first group of
/// leaders below the earner.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GenerationCommissionConfig {
    /// Maximum number of generations to pay.
    ///
    /// Generation 1 is the first group of leaders below the earner.
    /// Typical values: 3-7.
    pub max_generations: u8,

    /// Generation number (1-indexed) to commission percentage.
    ///
    /// Missing generation = no commission. Keys are generation numbers.
    /// Values are percentages between 0.0 and 1.0.
    #[serde(rename = "generation_rates")]
    pub rates: BTreeMap<u8, f64>,

    /// How generation boundaries are determined.
    pub boundary_mode: GenerationBoundaryMode,

    /// Rank that creates generation boundaries.
    ///
    /// Required for `ThresholdRank` mode. Each downline leader at or
    /// above this rank starts a new generation. For `SameRank` mode,
    /// each earner's own rank is used instead. This field is still
    /// present but ignored in `SameRank` mode.
    pub boundary_rank: String,

    /// Whether empty generations consume a generation number.
    ///
    /// When true, two adjacent boundary leaders create a zero-volume
    /// generation that still uses a generation number. This pushes
    /// deeper generations to lower-paying tiers. When false, empty
    /// generations are skipped and do not consume a number.
    pub empty_generation_consumes_number: bool,

    /// Per-structure CV override. None uses the plan-level multiplier
    /// from `VolumeConfig`.
    pub volume_to_dollar_multiplier: Option<f64>,

    /// Whether ineligible boundary-rank distributors still create generation
    /// boundaries.
    ///
    /// When true (default): rank defines structure, eligibility defines payout.
    /// An inactive Director still separates Gen 1 from Gen 2, but doesn't earn.
    /// When false: ineligible boundary-rank nodes are invisible to the generation
    /// walk. Their boundary behavior is governed by `empty_generation_consumes_number`.
    #[serde(default = "default_ineligible_creates_boundary")]
    pub ineligible_creates_boundary: bool,
}

fn default_ineligible_creates_boundary() -> bool {
    true
}

/// How generation boundaries are determined.
///
/// Two strategies with different performance characteristics.
/// `ThresholdRank` uses a fixed rank for all earners, allowing one
/// tree walk to serve everyone. `SameRank` uses each earner's own
/// rank, requiring a per-earner walk at higher computational cost.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum GenerationBoundaryMode {
    /// A fixed rank determines boundaries.
    ///
    /// One tree walk serves all earners. Lower computational cost.
    /// Every downline leader at or above the configured `boundary_rank`
    /// creates a new generation.
    ThresholdRank,

    /// Each earner's own rank determines boundaries.
    ///
    /// Per-earner walk required. Higher computational cost but rewards
    /// rank achievement dynamically. A leader only sees generation
    /// boundaries at leaders who match their own rank.
    SameRank,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_generation_config_threshold() {
        let json = r#"{
            "max_generations": 4,
            "generation_rates": {
                "1": 0.10,
                "2": 0.06,
                "3": 0.04,
                "4": 0.02
            },
            "boundary_mode": "threshold_rank",
            "boundary_rank": "director",
            "empty_generation_consumes_number": true,
            "volume_to_dollar_multiplier": 1.25
        }"#;
        let config: GenerationCommissionConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.max_generations, 4);
        assert_eq!(config.rates.len(), 4);
        assert_eq!(config.rates[&1], 0.10);
        assert_eq!(config.rates[&2], 0.06);
        assert_eq!(config.rates[&3], 0.04);
        assert_eq!(config.rates[&4], 0.02);
        assert!(matches!(
            config.boundary_mode,
            GenerationBoundaryMode::ThresholdRank
        ));
        assert_eq!(config.boundary_rank, "director");
        assert!(config.empty_generation_consumes_number);
        assert_eq!(config.volume_to_dollar_multiplier, Some(1.25));
        assert!(config.ineligible_creates_boundary);
    }

    #[test]
    fn deserialize_generation_config_same_rank() {
        let json = r#"{
            "max_generations": 3,
            "generation_rates": {
                "1": 0.08,
                "2": 0.05,
                "3": 0.03
            },
            "boundary_mode": "same_rank",
            "boundary_rank": "ignored_in_same_rank_mode",
            "empty_generation_consumes_number": false,
            "volume_to_dollar_multiplier": null
        }"#;
        let config: GenerationCommissionConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.max_generations, 3);
        assert_eq!(config.rates.len(), 3);
        assert_eq!(config.rates[&1], 0.08);
        assert_eq!(config.rates[&2], 0.05);
        assert_eq!(config.rates[&3], 0.03);
        assert!(matches!(
            config.boundary_mode,
            GenerationBoundaryMode::SameRank
        ));
        assert_eq!(config.boundary_rank, "ignored_in_same_rank_mode");
        assert!(!config.empty_generation_consumes_number);
        assert!(config.volume_to_dollar_multiplier.is_none());
        assert!(config.ineligible_creates_boundary);
    }

    #[test]
    fn deserialize_boundary_modes() {
        let json_threshold = r#""threshold_rank""#;
        let mode: GenerationBoundaryMode = serde_json::from_str(json_threshold).unwrap();
        assert!(matches!(mode, GenerationBoundaryMode::ThresholdRank));

        let json_same = r#""same_rank""#;
        let mode: GenerationBoundaryMode = serde_json::from_str(json_same).unwrap();
        assert!(matches!(mode, GenerationBoundaryMode::SameRank));
    }

    #[test]
    fn deserialize_empty_generation_flag() {
        let json_true = r#"{
            "max_generations": 2,
            "generation_rates": {
                "1": 0.07
            },
            "boundary_mode": "threshold_rank",
            "boundary_rank": "senior_director",
            "empty_generation_consumes_number": true,
            "volume_to_dollar_multiplier": null
        }"#;
        let config: GenerationCommissionConfig = serde_json::from_str(json_true).unwrap();
        assert!(config.empty_generation_consumes_number);
        assert!(config.ineligible_creates_boundary);

        let json_false = r#"{
            "max_generations": 2,
            "generation_rates": {
                "1": 0.07
            },
            "boundary_mode": "threshold_rank",
            "boundary_rank": "senior_director",
            "empty_generation_consumes_number": false,
            "volume_to_dollar_multiplier": null
        }"#;
        let config: GenerationCommissionConfig = serde_json::from_str(json_false).unwrap();
        assert!(!config.empty_generation_consumes_number);
        assert!(config.ineligible_creates_boundary);
    }

    #[test]
    fn ineligible_creates_boundary_defaults_to_true() {
        let json = r#"{
            "max_generations": 2,
            "generation_rates": { "1": 0.05 },
            "boundary_mode": "threshold_rank",
            "boundary_rank": "director",
            "empty_generation_consumes_number": false,
            "volume_to_dollar_multiplier": null
        }"#;
        let config: GenerationCommissionConfig = serde_json::from_str(json).unwrap();
        assert!(config.ineligible_creates_boundary);
    }

    #[test]
    fn ineligible_creates_boundary_explicit_false() {
        let json = r#"{
            "max_generations": 2,
            "generation_rates": { "1": 0.05 },
            "boundary_mode": "threshold_rank",
            "boundary_rank": "director",
            "empty_generation_consumes_number": false,
            "volume_to_dollar_multiplier": null,
            "ineligible_creates_boundary": false
        }"#;
        let config: GenerationCommissionConfig = serde_json::from_str(json).unwrap();
        assert!(!config.ineligible_creates_boundary);
    }
}
