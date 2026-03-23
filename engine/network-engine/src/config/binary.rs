//! Binary commission configuration types.
//!
//! Binary plans place each distributor's downline into exactly two legs
//! (left and right). Commissions are based on matching volume between
//! the two legs. Two calculation modes exist: percentage-based pairing
//! (modern) and fixed-amount cycle/step (legacy).

use serde::{Deserialize, Serialize};

/// Binary commission configuration.
///
/// Controls how commissions are calculated on a binary tree structure.
/// Each distributor has two legs. The mode determines whether payouts
/// are percentage-based (pairing) or fixed-amount (cycle/step).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BinaryCommissionConfig {
    /// Per-structure CV override. None uses the plan-level multiplier
    /// from `VolumeConfig`.
    pub volume_to_dollar_multiplier: Option<f64>,

    /// Pairing or cycle/step. The two modes are mutually exclusive.
    pub mode: BinaryCommissionMode,
}

/// Two modes of binary commission calculation.
///
/// Percentage-based pairing is the modern approach. It pays a percentage
/// of matched leg volume. Cycle/step is the legacy approach. It pays
/// fixed dollar amounts at volume thresholds. The enum makes them
/// mutually exclusive. A plan uses one or the other, never both.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BinaryCommissionMode {
    /// Modern percentage-based binary commission.
    ///
    /// Pays a percentage of the weaker leg's volume as commission.
    /// Most binary plans launched after 2010 use this mode.
    Pairing(PairingConfig),

    /// Legacy fixed-amount binary commission.
    ///
    /// Pays fixed dollar amounts when both legs reach volume thresholds.
    /// Common in plans designed in the 1990s and 2000s.
    CycleStep(CycleStepConfig),
}

/// Percentage-based pairing commission configuration.
///
/// The core binary calculation. Matches volume between left and right
/// legs and pays a percentage of the matched amount. The `calculation`
/// field controls exactly how matching works.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PairingConfig {
    /// Percentage of weaker leg volume paid as commission.
    ///
    /// Typical range 10-12%. Applied after leg volumes are matched
    /// according to the `calculation` method.
    pub percent: f64,

    /// How to determine the payout amount from leg volumes.
    pub calculation: PairingCalculation,

    /// Maximum commission per period. None means uncapped.
    ///
    /// Protects the company from outsized payouts in a single period.
    /// Applied after the pairing calculation.
    pub cap_per_period: Option<f64>,

    /// What happens to leg volumes after the commission run.
    ///
    /// The most consequential choice in a binary plan. Determines
    /// whether volume accumulates across periods or resets.
    pub volume_after_payout: VolumeAfterPayout,

    /// Maximum volume that can carry forward between periods.
    ///
    /// Only applies when `volume_after_payout` is `carry_forward`.
    /// None means unlimited carry. Prevents excessive accumulation
    /// on the stronger leg.
    pub carry_forward_cap: Option<f64>,

    /// How caps apply when one owner has multiple positions.
    /// Defaults to PerPosition. Only meaningful when ownership map
    /// is provided.
    #[serde(default)]
    pub multi_position_cap_mode: MultiPositionCapMode,
}

/// How per-distributor commission caps apply in multi-position mode.
///
/// Only meaningful when an ownership map is provided. In single-position
/// mode (no ownership map), caps always apply per-node regardless of
/// this setting.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MultiPositionCapMode {
    /// Cap applies to each income center independently.
    #[default]
    PerPosition,
    /// Cap applies to the owner's aggregate across all positions.
    /// Pro-rata scaling preserves relative distribution.
    Aggregate,
}

/// How pairing commission is calculated from leg volumes.
///
/// `weaker_leg` pays on `min(left, right)`. Simple and common.
/// `volume_ratio` adds a balance penalty: scales the payout by the
/// ratio of the weaker leg to the stronger leg (`min/max`). This
/// incentivizes balanced tree building.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PairingCalculation {
    /// Commission = min(left, right) * percent.
    ///
    /// Simple and common. Pays on the full weaker leg volume.
    WeakerLeg,

    /// Commission = weaker_leg * percent * (min/max ratio).
    ///
    /// Adds a balance penalty. A 50/50 split pays full commission.
    /// A 90/10 split pays only 11% of what weaker_leg alone would.
    VolumeRatio,
}

/// What happens to leg volumes after the commission run.
///
/// The most consequential choice in a binary plan. `carry_forward` is
/// the industry standard. It creates momentum because the stronger leg
/// accumulates volume across periods. `full_flush` forces rebuilding
/// every period. `net_off` is the middle ground.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum VolumeAfterPayout {
    /// Both legs reset to zero after each commission run.
    ///
    /// Forces rebuilding every period. Rare in modern plans because
    /// it discourages long-term volume building.
    FullFlush,

    /// Subtract matched volume from both legs. Carry the remainder
    /// on the stronger leg.
    ///
    /// Middle ground between flush and carry. The weaker leg resets
    /// to zero. The stronger leg keeps only the unmatched excess.
    NetOff,

    /// Subtract payout volume from the weaker leg. The full stronger
    /// leg carries forward.
    ///
    /// Industry standard. Creates momentum because the stronger leg
    /// accumulates indefinitely (subject to `carry_forward_cap`).
    CarryForward,
}

/// Legacy fixed-amount binary commission configuration.
///
/// Instead of percentages, pays fixed dollar amounts when both legs
/// reach volume thresholds. A "cycle" is one pass through all steps.
/// Once the highest step is reached, the cycle may reset.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CycleStepConfig {
    /// Volume thresholds and fixed payouts, ordered ascending.
    ///
    /// Each step defines a volume threshold that must be met on both
    /// legs and a fixed dollar amount paid when reached. Steps are
    /// evaluated in order from lowest to highest threshold.
    pub steps: Vec<CycleStep>,

    /// Whether to reset leg volumes after completing a full cycle.
    ///
    /// When true, both legs flush to zero after the highest step is
    /// reached. When false, excess volume carries into the next cycle.
    pub flush_after_cycle: bool,
}

/// A single step in a cycle/step binary commission.
///
/// When both legs reach the threshold, the fixed amount is paid.
/// Steps are ordered by ascending threshold within a `CycleStepConfig`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CycleStep {
    /// Volume threshold that must be met on both legs.
    pub threshold: f64,

    /// Fixed dollar amount paid when the threshold is reached.
    pub amount: f64,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_pairing_config() {
        let json = r#"{
            "percent": 0.10,
            "calculation": "weaker_leg",
            "cap_per_period": 5000.0,
            "volume_after_payout": "carry_forward",
            "carry_forward_cap": 100000.0
        }"#;
        let config: PairingConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.percent, 0.10);
        assert!(matches!(config.calculation, PairingCalculation::WeakerLeg));
        assert_eq!(config.cap_per_period, Some(5000.0));
        assert!(matches!(
            config.volume_after_payout,
            VolumeAfterPayout::CarryForward
        ));
        assert_eq!(config.carry_forward_cap, Some(100000.0));
    }

    #[test]
    fn deserialize_cycle_step_config() {
        let json = r#"{
            "steps": [
                { "threshold": 300.0, "amount": 25.0 },
                { "threshold": 600.0, "amount": 50.0 },
                { "threshold": 1200.0, "amount": 100.0 }
            ],
            "flush_after_cycle": true
        }"#;
        let config: CycleStepConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.steps.len(), 3);
        assert_eq!(config.steps[0].threshold, 300.0);
        assert_eq!(config.steps[0].amount, 25.0);
        assert_eq!(config.steps[1].threshold, 600.0);
        assert_eq!(config.steps[1].amount, 50.0);
        assert_eq!(config.steps[2].threshold, 1200.0);
        assert_eq!(config.steps[2].amount, 100.0);
        assert!(config.flush_after_cycle);
    }

    #[test]
    fn deserialize_binary_mode_pairing() {
        let json = r#"{
            "pairing": {
                "percent": 0.12,
                "calculation": "volume_ratio",
                "cap_per_period": null,
                "volume_after_payout": "net_off",
                "carry_forward_cap": null
            }
        }"#;
        let mode: BinaryCommissionMode = serde_json::from_str(json).unwrap();
        match mode {
            BinaryCommissionMode::Pairing(config) => {
                assert_eq!(config.percent, 0.12);
                assert!(matches!(
                    config.calculation,
                    PairingCalculation::VolumeRatio
                ));
                assert!(config.cap_per_period.is_none());
                assert!(matches!(
                    config.volume_after_payout,
                    VolumeAfterPayout::NetOff
                ));
                assert!(config.carry_forward_cap.is_none());
            }
            _ => panic!("expected Pairing variant"),
        }
    }

    #[test]
    fn deserialize_binary_mode_cycle_step() {
        let json = r#"{
            "cycle_step": {
                "steps": [
                    { "threshold": 500.0, "amount": 40.0 }
                ],
                "flush_after_cycle": false
            }
        }"#;
        let mode: BinaryCommissionMode = serde_json::from_str(json).unwrap();
        match mode {
            BinaryCommissionMode::CycleStep(config) => {
                assert_eq!(config.steps.len(), 1);
                assert_eq!(config.steps[0].threshold, 500.0);
                assert_eq!(config.steps[0].amount, 40.0);
                assert!(!config.flush_after_cycle);
            }
            _ => panic!("expected CycleStep variant"),
        }
    }

    #[test]
    fn deserialize_volume_after_payout_variants() {
        let json_full_flush = r#""full_flush""#;
        let vap: VolumeAfterPayout = serde_json::from_str(json_full_flush).unwrap();
        assert!(matches!(vap, VolumeAfterPayout::FullFlush));

        let json_net_off = r#""net_off""#;
        let vap: VolumeAfterPayout = serde_json::from_str(json_net_off).unwrap();
        assert!(matches!(vap, VolumeAfterPayout::NetOff));

        let json_carry_forward = r#""carry_forward""#;
        let vap: VolumeAfterPayout = serde_json::from_str(json_carry_forward).unwrap();
        assert!(matches!(vap, VolumeAfterPayout::CarryForward));
    }

    #[test]
    fn deserialize_pairing_calculation_variants() {
        let json_weaker = r#""weaker_leg""#;
        let calc: PairingCalculation = serde_json::from_str(json_weaker).unwrap();
        assert!(matches!(calc, PairingCalculation::WeakerLeg));

        let json_ratio = r#""volume_ratio""#;
        let calc: PairingCalculation = serde_json::from_str(json_ratio).unwrap();
        assert!(matches!(calc, PairingCalculation::VolumeRatio));
    }

    #[test]
    fn deserialize_multi_position_cap_mode_variants() {
        let json_pp = r#""per_position""#;
        let mode: MultiPositionCapMode = serde_json::from_str(json_pp).unwrap();
        assert_eq!(mode, MultiPositionCapMode::PerPosition);

        let json_agg = r#""aggregate""#;
        let mode: MultiPositionCapMode = serde_json::from_str(json_agg).unwrap();
        assert_eq!(mode, MultiPositionCapMode::Aggregate);
    }

    #[test]
    fn multi_position_cap_mode_default_is_per_position() {
        let config: PairingConfig = serde_json::from_str(
            r#"{
                "percent": 0.10,
                "calculation": "weaker_leg",
                "volume_after_payout": "full_flush"
            }"#,
        )
        .unwrap();
        assert_eq!(
            config.multi_position_cap_mode,
            MultiPositionCapMode::PerPosition
        );
    }
}
