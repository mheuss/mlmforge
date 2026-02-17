//! Unilevel commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
use crate::config::{CompensationPlan, UnilevelStructureConfig};
use crate::tree::unilevel::UnilevelTree;

use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};

/// Calculate unilevel commissions for a set of volume events.
///
/// Walks the upline from each volume source, applying the rate table,
/// compression, eligibility, and depth limits from the plan config.
///
/// # Errors
///
/// Returns `CalculationError` if a volume source is not found in the
/// tree or snapshot data.
pub fn calculate_unilevel(
    _tree: &UnilevelTree,
    _plan: &CompensationPlan,
    _structure: &UnilevelStructureConfig,
    _snapshots: &HashMap<Uuid, DistributorSnapshot>,
    _volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    todo!()
}

/// Check if a distributor meets basic commission eligibility.
#[allow(dead_code)] // Used by calculate_unilevel in Task 3
fn is_eligible(snapshot: &DistributorSnapshot, eligibility: &CommissionEligibility) -> bool {
    if snapshot.personal_volume < eligibility.minimum_pv {
        return false;
    }

    if eligibility.require_order_in_period && !snapshot.has_order_in_period {
        return false;
    }

    if !eligibility.eligible_statuses.is_empty()
        && !eligibility.eligible_statuses.contains(&snapshot.status)
    {
        return false;
    }

    true
}

/// Count how many direct children of a distributor are commission-eligible.
#[allow(dead_code)] // Used by calculate_unilevel in Task 3
fn count_active_legs(
    tree: &UnilevelTree,
    user_id: Uuid,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    eligibility: &CommissionEligibility,
) -> u16 {
    let children = match tree.get_children(user_id) {
        Ok(children) => children,
        Err(_) => return 0,
    };

    children
        .iter()
        .filter(|child| {
            snapshots
                .get(&child.user_id)
                .map(|s| is_eligible(s, eligibility))
                .unwrap_or(false)
        })
        .count() as u16
}

/// Determine per-distributor max earning depth from active leg tiers.
///
/// Returns `Some(depth)` if a tier limits the distributor, or `None`
/// if no tier restriction applies (use config max_depth as ceiling).
#[allow(dead_code)] // Used by calculate_unilevel in Task 3
fn determine_max_depth(active_leg_count: u16, tiers: &[ActiveLegTier]) -> Option<u8> {
    if tiers.is_empty() {
        return None;
    }

    // Tiers are sorted ascending by min_active_legs.
    // Walk in reverse to find the highest qualifying tier.
    for tier in tiers.iter().rev() {
        if active_leg_count >= tier.min_active_legs {
            return if tier.max_commission_depth == 0 {
                None // unlimited
            } else {
                debug_assert!(
                    tier.max_commission_depth <= u8::MAX as u16,
                    "max_commission_depth {} exceeds u8 range",
                    tier.max_commission_depth
                );
                Some(tier.max_commission_depth as u8)
            };
        }
    }

    None // no tier matched, use config max_depth
}

/// Cached eligibility result for a single distributor.
#[allow(dead_code)] // Used by calculate_unilevel in Task 3
struct EligibilityResult {
    eligible: bool,
    /// Per-distributor earning depth limit from active leg tiers.
    /// None means no tier restriction (use config max_depth).
    max_earning_depth: Option<u8>,
}

/// Evaluate eligibility for all distributors in the snapshot map.
///
/// Builds an internal cache used during the walk phase. Runs once
/// before any upline walks begin.
#[allow(dead_code)] // Used by calculate_unilevel in Task 3
fn evaluate_eligibility(
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    tree: &UnilevelTree,
    eligibility: &CommissionEligibility,
) -> HashMap<Uuid, EligibilityResult> {
    let mut results = HashMap::with_capacity(snapshots.len());

    for (user_id, snapshot) in snapshots {
        let eligible = is_eligible(snapshot, eligibility);

        let active_leg_count = if eligible && !eligibility.active_leg_tiers.is_empty() {
            count_active_legs(tree, *user_id, snapshots, eligibility)
        } else {
            0
        };

        let max_earning_depth =
            determine_max_depth(active_leg_count, &eligibility.active_leg_tiers);

        results.insert(
            *user_id,
            EligibilityResult {
                eligible,
                max_earning_depth,
            },
        );
    }

    results
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};

    fn test_uuid(n: u8) -> Uuid {
        Uuid::from_bytes([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, n])
    }

    fn default_eligibility() -> CommissionEligibility {
        CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![],
        }
    }

    fn eligible_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "silver".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    // --- is_eligible tests ---

    #[test]
    fn eligible_distributor_passes_all_checks() {
        let elig = default_eligibility();
        let snap = eligible_snapshot();
        assert!(is_eligible(&snap, &elig));
    }

    #[test]
    fn eligible_at_exact_minimum_pv() {
        let elig = default_eligibility(); // minimum_pv = 100.0
        let snap = DistributorSnapshot {
            personal_volume: 100.0,
            ..eligible_snapshot()
        };
        assert!(is_eligible(&snap, &elig));
    }

    #[test]
    fn ineligible_below_minimum_pv() {
        let elig = default_eligibility();
        let snap = DistributorSnapshot {
            personal_volume: 50.0,
            ..eligible_snapshot()
        };
        assert!(!is_eligible(&snap, &elig));
    }

    #[test]
    fn ineligible_wrong_status() {
        let elig = default_eligibility();
        let snap = DistributorSnapshot {
            status: "suspended".to_string(),
            ..eligible_snapshot()
        };
        assert!(!is_eligible(&snap, &elig));
    }

    #[test]
    fn eligible_when_status_list_empty() {
        let elig = CommissionEligibility {
            eligible_statuses: vec![],
            ..default_eligibility()
        };
        let snap = DistributorSnapshot {
            status: "anything".to_string(),
            ..eligible_snapshot()
        };
        assert!(is_eligible(&snap, &elig));
    }

    #[test]
    fn ineligible_no_order_when_required() {
        let elig = CommissionEligibility {
            require_order_in_period: true,
            ..default_eligibility()
        };
        let snap = DistributorSnapshot {
            has_order_in_period: false,
            ..eligible_snapshot()
        };
        assert!(!is_eligible(&snap, &elig));
    }

    #[test]
    fn eligible_no_order_when_not_required() {
        let elig = CommissionEligibility {
            require_order_in_period: false,
            ..default_eligibility()
        };
        let snap = DistributorSnapshot {
            has_order_in_period: false,
            ..eligible_snapshot()
        };
        assert!(is_eligible(&snap, &elig));
    }

    // --- count_active_legs tests ---

    #[test]
    fn count_active_legs_with_mixed_children() {
        let mut tree = UnilevelTree::new();
        let root = test_uuid(1);
        tree.add_root(root, 0).unwrap();
        tree.add_node(test_uuid(2), root, 0).unwrap(); // eligible child
        tree.add_node(test_uuid(3), root, 0).unwrap(); // ineligible child
        tree.add_node(test_uuid(4), root, 0).unwrap(); // eligible child

        let elig = default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(root, eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(
            test_uuid(3),
            DistributorSnapshot {
                personal_volume: 0.0, // below min
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(4), eligible_snapshot());

        assert_eq!(count_active_legs(&tree, root, &snapshots, &elig), 2);
    }

    #[test]
    fn count_active_legs_missing_child_snapshot() {
        let mut tree = UnilevelTree::new();
        let root = test_uuid(1);
        tree.add_root(root, 0).unwrap();
        tree.add_node(test_uuid(2), root, 0).unwrap();
        tree.add_node(test_uuid(3), root, 0).unwrap();

        let elig = default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(root, eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        // test_uuid(3) has no snapshot — counts as not active

        assert_eq!(count_active_legs(&tree, root, &snapshots, &elig), 1);
    }

    #[test]
    fn count_active_legs_user_not_in_tree() {
        let tree = UnilevelTree::new();
        let elig = default_eligibility();
        let snapshots = HashMap::new();

        assert_eq!(
            count_active_legs(&tree, test_uuid(99), &snapshots, &elig),
            0
        );
    }

    // --- determine_max_depth tests ---

    #[test]
    fn determine_max_depth_no_tiers() {
        assert_eq!(determine_max_depth(5, &[]), None);
    }

    #[test]
    fn determine_max_depth_matches_highest_qualifying_tier() {
        let tiers = vec![
            ActiveLegTier {
                min_active_legs: 2,
                max_commission_depth: 3,
            },
            ActiveLegTier {
                min_active_legs: 5,
                max_commission_depth: 5,
            },
            ActiveLegTier {
                min_active_legs: 8,
                max_commission_depth: 0,
            },
        ];

        // 6 legs qualifies for tier 2 (min 5) but not tier 3 (min 8)
        assert_eq!(determine_max_depth(6, &tiers), Some(5));
    }

    #[test]
    fn determine_max_depth_unlimited_tier() {
        let tiers = vec![
            ActiveLegTier {
                min_active_legs: 2,
                max_commission_depth: 3,
            },
            ActiveLegTier {
                min_active_legs: 8,
                max_commission_depth: 0,
            },
        ];

        // 10 legs qualifies for unlimited tier (depth 0)
        assert_eq!(determine_max_depth(10, &tiers), None);
    }

    #[test]
    fn determine_max_depth_no_tier_matches() {
        let tiers = vec![ActiveLegTier {
            min_active_legs: 5,
            max_commission_depth: 3,
        }];

        // 2 legs doesn't meet min 5
        assert_eq!(determine_max_depth(2, &tiers), None);
    }

    #[test]
    fn determine_max_depth_exact_match() {
        let tiers = vec![ActiveLegTier {
            min_active_legs: 3,
            max_commission_depth: 4,
        }];

        assert_eq!(determine_max_depth(3, &tiers), Some(4));
    }

    // --- evaluate_eligibility tests ---

    #[test]
    fn evaluate_eligibility_builds_cache_for_all_distributors() {
        let mut tree = UnilevelTree::new();
        let root = test_uuid(1);
        tree.add_root(root, 0).unwrap();
        tree.add_node(test_uuid(2), root, 0).unwrap();
        tree.add_node(test_uuid(3), root, 0).unwrap();
        tree.add_node(test_uuid(4), root, 0).unwrap();

        let elig = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![
                ActiveLegTier {
                    min_active_legs: 1,
                    max_commission_depth: 3,
                },
                ActiveLegTier {
                    min_active_legs: 2,
                    max_commission_depth: 0,
                },
            ],
        };

        let mut snapshots = HashMap::new();
        // Root has 2 eligible children (uuid(2), uuid(4)) -> tier 2 (unlimited)
        snapshots.insert(root, eligible_snapshot());
        // Child 2 is eligible, no children -> 0 legs -> no tier
        snapshots.insert(test_uuid(2), eligible_snapshot());
        // Child 3 is ineligible (low PV)
        snapshots.insert(
            test_uuid(3),
            DistributorSnapshot {
                personal_volume: 10.0,
                ..eligible_snapshot()
            },
        );
        // Child 4 is eligible, no children -> 0 legs -> no tier
        snapshots.insert(test_uuid(4), eligible_snapshot());

        let cache = evaluate_eligibility(&snapshots, &tree, &elig);

        let root_elig = &cache[&root];
        assert!(root_elig.eligible);
        assert_eq!(root_elig.max_earning_depth, None); // unlimited tier

        let child2_elig = &cache[&test_uuid(2)];
        assert!(child2_elig.eligible);
        assert_eq!(child2_elig.max_earning_depth, None); // no legs, no tier

        let child3_elig = &cache[&test_uuid(3)];
        assert!(!child3_elig.eligible);
    }
}
