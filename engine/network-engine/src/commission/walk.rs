//! Shared level-commission walk logic.
//!
//! Extracts the common prep phase and walk loop used by unilevel,
//! matrix, and stairstep (Walk 1) commission calculators. Each
//! calculator remains a standalone public function that delegates
//! to these shared internals.
//!
//! Binary uses pairing mechanics, not level-based walks. It is
//! not a consumer of this module.

// These imports are consumed by Tasks 2-3 (generic eligibility and
// walk_level_commissions). Suppressed until then.
#![allow(unused_imports, dead_code)]

use std::collections::{BTreeMap, HashMap};
use uuid::Uuid;

use crate::config::CompensationPlan;
use crate::config::commission::{CompressionConfig, CompressionMode};
use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
use crate::tree::navigator::TreeNavigator;

use super::is_eligible;
use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/// Cached eligibility result for a single distributor.
///
/// Built during the prep phase and consumed by the walk phase via
/// O(1) HashMap lookups.
pub(crate) struct EligibilityResult {
    pub eligible: bool,
    /// Per-distributor earning depth limit from active leg tiers.
    /// None means no tier restriction (use config max_depth).
    pub max_earning_depth: Option<u8>,
}

/// Configuration for a level-based commission walk.
///
/// Built once per calculation from the structure config. Bundles
/// parameters that are identical across unilevel, matrix, and
/// stairstep Walk 1. The caller computes any plan-specific values
/// (e.g., matrix height ceiling) before constructing this struct.
pub(crate) struct LevelWalkConfig<'a> {
    pub max_depth: u8,
    pub broad_pct: f64,
    pub multiplier: f64,
    pub compression: Option<&'a CompressionConfig>,
    pub compression_enabled: bool,
    pub threshold_ordinal: Option<u16>,
    pub rank_ordinals: &'a HashMap<&'a str, u16>,
    pub rate_table: &'a BTreeMap<String, BTreeMap<u8, f64>>,
}

// ---------------------------------------------------------------------------
// Prep utilities (tree-agnostic)
// ---------------------------------------------------------------------------

/// Build rank name -> ordinal map from the plan's rank definitions.
///
/// Used for SkipBelowRank compression comparison and breakaway
/// threshold detection.
pub(crate) fn build_rank_ordinals(plan: &CompensationPlan) -> HashMap<&str, u16> {
    plan.ranks
        .iter()
        .map(|r| (r.name.as_str(), r.ordinal))
        .collect()
}

/// Resolve the SkipBelowRank threshold ordinal from compression config.
///
/// Returns `Some(ordinal)` when compression is configured with
/// SkipBelowRank mode and the threshold rank exists in the plan.
/// Returns `None` otherwise (with warnings for misconfiguration).
pub(crate) fn resolve_threshold_ordinal(
    compression: Option<&CompressionConfig>,
    rank_ordinals: &HashMap<&str, u16>,
) -> Option<u16> {
    compression.and_then(|c| {
        if matches!(c.mode, CompressionMode::SkipBelowRank) {
            match &c.rank_threshold {
                None => {
                    log::warn!(
                        "SkipBelowRank compression enabled but rank_threshold is not set; \
                         compression will have no effect"
                    );
                    None
                }
                Some(name) => {
                    let ordinal = rank_ordinals.get(name.as_str()).copied();
                    if ordinal.is_none() {
                        log::warn!(
                            "SkipBelowRank compression rank_threshold '{}' not found in \
                             plan ranks; compression will have no effect",
                            name
                        );
                    }
                    ordinal
                }
            }
        } else {
            None
        }
    })
}

/// Determine per-distributor max earning depth from active leg tiers.
///
/// Returns `Some(depth)` if a tier limits the distributor, or `None`
/// if no tier restriction applies (use config max_depth as ceiling).
///
/// Tiers must be sorted ascending by `min_active_legs`. The caller
/// (Go validation pipeline) enforces this via business rules requiring
/// a base tier with min_active_legs=0.
pub(crate) fn determine_max_depth(active_leg_count: u16, tiers: &[ActiveLegTier]) -> Option<u8> {
    if tiers.is_empty() {
        return None;
    }

    // Walk in reverse to find the highest qualifying tier.
    for tier in tiers.iter().rev() {
        if active_leg_count >= tier.min_active_legs {
            return if tier.max_commission_depth == 0 {
                None // unlimited
            } else {
                Some(u8::try_from(tier.max_commission_depth).unwrap_or(u8::MAX))
            };
        }
    }

    None // no tier matched, use config max_depth
}

/// Validate that a volume source's CV amount is finite and non-negative.
pub(crate) fn validate_cv(source: &VolumeSource) -> Result<(), CalculationError> {
    if !source.cv_amount.is_finite() || source.cv_amount < 0.0 {
        return Err(CalculationError::InvalidCvAmount(
            source.source_id,
            source.cv_amount,
        ));
    }
    Ok(())
}

/// Sort earnings by (earner_id, source_id) for deterministic output.
///
/// Without sorting, the order depends on BFS traversal and volume
/// source iteration, both of which can vary across runs.
pub(crate) fn sort_earnings(earnings: &mut [CommissionEarning]) {
    earnings.sort_by(|a, b| {
        a.earner_id
            .cmp(&b.earner_id)
            .then_with(|| a.source_id.cmp(&b.source_id))
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::uuid_from_index;
    use crate::config::commission::{CompressionConfig, CompressionMode};
    use crate::config::eligibility::ActiveLegTier;

    // --- build_rank_ordinals ---

    #[test]
    fn build_rank_ordinals_maps_names_to_ordinals() {
        let plan = crate::commission::test_helpers::build_test_plan(
            crate::commission::test_helpers::default_eligibility(),
            crate::config::StructureConfig::Unilevel(crate::config::UnilevelStructureConfig {
                name: "Test".to_string(),
                level_commission: crate::config::commission::LevelCommissionConfig {
                    broad_commission_percent: 0.40,
                    volume_to_dollar_multiplier: None,
                    max_depth: 5,
                    rate_table: std::collections::BTreeMap::new(),
                },
                compression: None,
            }),
            "Test",
        );

        let ordinals = build_rank_ordinals(&plan);
        assert_eq!(ordinals.get("associate"), Some(&1));
    }

    // --- resolve_threshold_ordinal ---

    #[test]
    fn resolve_threshold_ordinal_none_when_no_compression() {
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(None, &ordinals).is_none());
    }

    #[test]
    fn resolve_threshold_ordinal_none_for_skip_inactive() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        };
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(Some(&config), &ordinals).is_none());
    }

    #[test]
    fn resolve_threshold_ordinal_returns_ordinal_for_skip_below_rank() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        };
        let mut ordinals = HashMap::new();
        ordinals.insert("silver", 2u16);
        assert_eq!(resolve_threshold_ordinal(Some(&config), &ordinals), Some(2));
    }

    #[test]
    fn resolve_threshold_ordinal_none_when_rank_not_found() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("gold".to_string()),
        };
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(Some(&config), &ordinals).is_none());
    }

    #[test]
    fn resolve_threshold_ordinal_none_when_no_rank_threshold_set() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: None,
        };
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(Some(&config), &ordinals).is_none());
    }

    // --- determine_max_depth ---

    #[test]
    fn determine_max_depth_no_tiers() {
        assert_eq!(determine_max_depth(5, &[]), None);
    }

    #[test]
    fn determine_max_depth_matches_highest_qualifying_tier() {
        let tiers = vec![
            ActiveLegTier {
                min_active_legs: 0,
                max_commission_depth: 3,
            },
            ActiveLegTier {
                min_active_legs: 3,
                max_commission_depth: 5,
            },
        ];
        assert_eq!(determine_max_depth(3, &tiers), Some(5));
        assert_eq!(determine_max_depth(2, &tiers), Some(3));
    }

    #[test]
    fn determine_max_depth_zero_means_unlimited() {
        let tiers = vec![ActiveLegTier {
            min_active_legs: 0,
            max_commission_depth: 0,
        }];
        assert_eq!(determine_max_depth(0, &tiers), None);
    }

    // --- validate_cv ---

    #[test]
    fn validate_cv_accepts_positive() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: 100.0,
        };
        assert!(validate_cv(&source).is_ok());
    }

    #[test]
    fn validate_cv_accepts_zero() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: 0.0,
        };
        assert!(validate_cv(&source).is_ok());
    }

    #[test]
    fn validate_cv_rejects_negative() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: -1.0,
        };
        assert!(validate_cv(&source).is_err());
    }

    #[test]
    fn validate_cv_rejects_nan() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: f64::NAN,
        };
        assert!(validate_cv(&source).is_err());
    }

    #[test]
    fn validate_cv_rejects_infinity() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: f64::INFINITY,
        };
        assert!(validate_cv(&source).is_err());
    }

    // --- sort_earnings ---

    #[test]
    fn sort_earnings_by_earner_then_source() {
        let mut earnings = vec![
            CommissionEarning {
                earner_id: uuid_from_index(2),
                source_id: uuid_from_index(1),
                level: 1,
                rate: 0.05,
                cv_amount: 100.0,
                dollar_amount: 2.0,
            },
            CommissionEarning {
                earner_id: uuid_from_index(1),
                source_id: uuid_from_index(2),
                level: 1,
                rate: 0.05,
                cv_amount: 100.0,
                dollar_amount: 2.0,
            },
            CommissionEarning {
                earner_id: uuid_from_index(1),
                source_id: uuid_from_index(1),
                level: 1,
                rate: 0.05,
                cv_amount: 100.0,
                dollar_amount: 2.0,
            },
        ];

        sort_earnings(&mut earnings);

        assert_eq!(earnings[0].earner_id, uuid_from_index(1));
        assert_eq!(earnings[0].source_id, uuid_from_index(1));
        assert_eq!(earnings[1].earner_id, uuid_from_index(1));
        assert_eq!(earnings[1].source_id, uuid_from_index(2));
        assert_eq!(earnings[2].earner_id, uuid_from_index(2));
    }
}
