# Unilevel Commission Calculator Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Rust function that calculates unilevel commissions given a tree, config, distributor snapshots, and volume events.

**Architecture:** New `commission` module in the network engine. Single public function `calculate_unilevel` with two internal phases: prep (eligibility evaluation + caching) and walk (upline traversal per volume source). No traits, no abstractions. Types are simple structs.

**Tech Stack:** Rust (existing network-engine crate, uuid, thiserror)

**Status:** Pending
**Progress:** 0 complete, 0 implemented, 0 pending

---

### Task 1: Module scaffolding and types [Pending]

**Files:**
- Create: `engine/network-engine/src/commission/mod.rs`
- Create: `engine/network-engine/src/commission/types.rs`
- Create: `engine/network-engine/src/commission/unilevel.rs`
- Modify: `engine/network-engine/src/lib.rs`

**Step 1: Create the commission module directory**

```bash
mkdir -p engine/network-engine/src/commission
```

**Step 2: Write types.rs**

```rust
//! Types for commission calculation.

use thiserror::Error;
use uuid::Uuid;

/// Point-in-time facts about a distributor for a commission period.
///
/// Contains only observable data. The calculator derives all eligibility
/// and depth decisions from the compensation plan config.
#[derive(Debug, Clone)]
pub struct DistributorSnapshot {
    /// Current rank name. Must match a rank in the plan's rank ladder.
    pub rank: String,

    /// Personal volume generated this period.
    pub personal_volume: f64,

    /// Distributor's current status (e.g., "active", "grace", "suspended").
    pub status: String,

    /// Whether the distributor placed at least one order this period.
    pub has_order_in_period: bool,
}

/// A volume event that triggers commission calculation.
///
/// Each volume source produces one upline walk. The walk pays
/// commissions to eligible ancestors based on the rate table.
#[derive(Debug, Clone)]
pub struct VolumeSource {
    /// The distributor who generated this volume.
    pub source_id: Uuid,

    /// Commission volume points generated.
    pub cv_amount: f64,
}

/// A single commission earning. One entry per earner per volume source.
///
/// The dollar amount formula:
/// `cv_amount * broad_commission_percent * volume_to_dollar_multiplier * rate`
#[derive(Debug, Clone, PartialEq)]
pub struct CommissionEarning {
    /// The distributor who earned this commission.
    pub earner_id: Uuid,

    /// The distributor whose volume triggered the earning.
    pub source_id: Uuid,

    /// Level in the (possibly compressed) upline walk. 1-indexed.
    pub level: u8,

    /// Rate table value applied at this level for this rank.
    pub rate: f64,

    /// Input commission volume from the source.
    pub cv_amount: f64,

    /// Final payout amount in the plan's base currency.
    pub dollar_amount: f64,
}

/// Errors that halt the entire commission calculation.
///
/// These indicate data integrity problems in the caller's input.
/// Recoverable issues (missing upline snapshots) are handled
/// defensively within the calculation.
#[derive(Debug, Error)]
pub enum CalculationError {
    /// A volume source references a distributor not in the tree.
    #[error("volume source {0} not found in tree")]
    SourceNotInTree(Uuid),

    /// A volume source references a distributor with no snapshot data.
    #[error("volume source {0} not found in snapshot data")]
    SourceNotInSnapshot(Uuid),
}
```

**Step 3: Write unilevel.rs (stub only)**

```rust
//! Unilevel commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::{CompensationPlan, UnilevelStructureConfig};
use crate::tree::unilevel::UnilevelTree;

use super::types::{
    CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource,
};

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
    tree: &UnilevelTree,
    plan: &CompensationPlan,
    structure: &UnilevelStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    todo!()
}
```

**Step 4: Write mod.rs**

```rust
//! Commission calculation.

pub mod types;
pub mod unilevel;

pub use types::{
    CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource,
};
pub use unilevel::calculate_unilevel;
```

**Step 5: Register the module in lib.rs**

Add `pub mod commission;` to `engine/network-engine/src/lib.rs`:

```rust
pub mod commission;
pub mod config;
pub mod tree;
pub mod types;
```

**Step 6: Verify it compiles**

Run: `cargo build -p network-engine`
Expected: compiles with no errors (the `todo!()` is fine for compilation)

**Step 7: Commit**

```bash
git add -f engine/network-engine/src/commission/ engine/network-engine/src/lib.rs
git commit -m "feat(engine): scaffold commission module with types"
```

---

### Task 2: Eligibility evaluation [Pending]

**Files:**
- Modify: `engine/network-engine/src/commission/unilevel.rs`

This task builds the prep phase: evaluating eligibility for all distributors and caching results. All code goes in `unilevel.rs` as private functions tested from the inline test module.

**Step 1: Write failing tests for basic eligibility**

Add to the bottom of `unilevel.rs`:

```rust
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
}
```

**Step 2: Run tests to verify they fail**

Run: `cargo test -p network-engine is_eligible -- --nocapture`
Expected: FAIL — `is_eligible` function doesn't exist yet.

**Step 3: Implement is_eligible**

Add above the test module in `unilevel.rs`:

```rust
/// Check if a distributor meets basic commission eligibility.
fn is_eligible(
    snapshot: &DistributorSnapshot,
    eligibility: &CommissionEligibility,
) -> bool {
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
```

Add the necessary import at the top of the file:

```rust
use crate::config::eligibility::CommissionEligibility;
```

**Step 4: Run tests to verify they pass**

Run: `cargo test -p network-engine is_eligible -- --nocapture`
Expected: all 6 tests PASS

**Step 5: Write failing tests for active leg counting**

Add to the test module:

```rust
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

        assert_eq!(count_active_legs(&tree, test_uuid(99), &snapshots, &elig), 0);
    }
```

**Step 6: Run tests to verify they fail**

Run: `cargo test -p network-engine count_active_legs -- --nocapture`
Expected: FAIL — `count_active_legs` function doesn't exist yet.

**Step 7: Implement count_active_legs**

Add above the test module:

```rust
/// Count how many direct children of a distributor are commission-eligible.
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
```

**Step 8: Run tests to verify they pass**

Run: `cargo test -p network-engine count_active_legs -- --nocapture`
Expected: all 3 tests PASS

**Step 9: Write failing tests for tier depth determination**

Add to the test module:

```rust
    #[test]
    fn determine_max_depth_no_tiers() {
        assert_eq!(determine_max_depth(5, &[]), None);
    }

    #[test]
    fn determine_max_depth_matches_highest_qualifying_tier() {
        let tiers = vec![
            ActiveLegTier { min_active_legs: 2, max_commission_depth: 3 },
            ActiveLegTier { min_active_legs: 5, max_commission_depth: 5 },
            ActiveLegTier { min_active_legs: 8, max_commission_depth: 0 },
        ];

        // 6 legs qualifies for tier 2 (min 5) but not tier 3 (min 8)
        assert_eq!(determine_max_depth(6, &tiers), Some(5));
    }

    #[test]
    fn determine_max_depth_unlimited_tier() {
        let tiers = vec![
            ActiveLegTier { min_active_legs: 2, max_commission_depth: 3 },
            ActiveLegTier { min_active_legs: 8, max_commission_depth: 0 },
        ];

        // 10 legs qualifies for unlimited tier (depth 0)
        assert_eq!(determine_max_depth(10, &tiers), None);
    }

    #[test]
    fn determine_max_depth_no_tier_matches() {
        let tiers = vec![
            ActiveLegTier { min_active_legs: 5, max_commission_depth: 3 },
        ];

        // 2 legs doesn't meet min 5
        assert_eq!(determine_max_depth(2, &tiers), None);
    }

    #[test]
    fn determine_max_depth_exact_match() {
        let tiers = vec![
            ActiveLegTier { min_active_legs: 3, max_commission_depth: 4 },
        ];

        assert_eq!(determine_max_depth(3, &tiers), Some(4));
    }
```

**Step 10: Run tests to verify they fail**

Run: `cargo test -p network-engine determine_max_depth -- --nocapture`
Expected: FAIL — `determine_max_depth` function doesn't exist yet.

**Step 11: Implement determine_max_depth**

Add above the test module:

```rust
/// Determine per-distributor max earning depth from active leg tiers.
///
/// Returns `Some(depth)` if a tier limits the distributor, or `None`
/// if no tier restriction applies (use config max_depth as ceiling).
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
                Some(tier.max_commission_depth as u8)
            };
        }
    }

    None // no tier matched, use config max_depth
}
```

Add the import:

```rust
use crate::config::eligibility::ActiveLegTier;
```

**Step 12: Run tests to verify they pass**

Run: `cargo test -p network-engine determine_max_depth -- --nocapture`
Expected: all 5 tests PASS

**Step 13: Write failing test for full evaluate_eligibility**

Add to the test module:

```rust
    #[test]
    fn evaluate_eligibility_builds_cache_for_all_distributors() {
        let mut tree = UnilevelTree::new();
        let root = test_uuid(1);
        tree.add_root(root, 0).unwrap();
        tree.add_node(test_uuid(2), root, 0).unwrap();
        tree.add_node(test_uuid(3), root, 0).unwrap();

        let elig = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![
                ActiveLegTier { min_active_legs: 1, max_commission_depth: 3 },
                ActiveLegTier { min_active_legs: 2, max_commission_depth: 0 },
            ],
        };

        let mut snapshots = HashMap::new();
        // Root has 2 eligible children -> tier 2 (unlimited)
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
```

**Step 14: Run test to verify it fails**

Run: `cargo test -p network-engine evaluate_eligibility_builds -- --nocapture`
Expected: FAIL — `evaluate_eligibility` and `EligibilityResult` don't exist yet.

**Step 15: Implement evaluate_eligibility and EligibilityResult**

Add above the test module:

```rust
/// Cached eligibility result for a single distributor.
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

        results.insert(*user_id, EligibilityResult {
            eligible,
            max_earning_depth,
        });
    }

    results
}
```

**Step 16: Run tests to verify they pass**

Run: `cargo test -p network-engine evaluate_eligibility -- --nocapture`
Expected: PASS

**Step 17: Run all tests**

Run: `cargo test -p network-engine`
Expected: all tests PASS (14 new tests + existing tests)

**Step 18: Commit**

```bash
git add -f engine/network-engine/src/commission/unilevel.rs
git commit -m "feat(engine): implement eligibility evaluation for commission prep phase"
```

---

### Task 3: Basic upline walk [Pending]

**Files:**
- Modify: `engine/network-engine/src/commission/unilevel.rs`

This task implements `calculate_unilevel` with the basic walk: rate lookup, depth limits, dollar amount formula. No compression yet.

**Step 1: Write test helper functions**

Add to the test module (above the individual tests):

```rust
    use crate::config::commission::{CompressionConfig, LevelCommissionConfig};
    use crate::config::rank::{
        DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification,
        RankTrackingConfig,
    };
    use crate::config::volume::VolumeConfig;
    use crate::config::{
        CompensationPlan, StructureConfig, UnilevelStructureConfig,
    };
    use std::collections::BTreeMap;

    fn test_rate_table() -> BTreeMap<String, BTreeMap<u8, f64>> {
        let mut table = BTreeMap::new();

        let mut associate = BTreeMap::new();
        associate.insert(1, 0.05);
        associate.insert(2, 0.04);
        associate.insert(3, 0.03);
        table.insert("associate".to_string(), associate);

        let mut silver = BTreeMap::new();
        silver.insert(1, 0.07);
        silver.insert(2, 0.06);
        silver.insert(3, 0.05);
        silver.insert(4, 0.04);
        silver.insert(5, 0.03);
        table.insert("silver".to_string(), silver);

        table
    }

    fn test_structure(
        rate_table: BTreeMap<String, BTreeMap<u8, f64>>,
    ) -> UnilevelStructureConfig {
        UnilevelStructureConfig {
            name: "Test Unilevel".to_string(),
            level_commission: LevelCommissionConfig {
                broad_commission_percent: 0.40,
                volume_to_dollar_multiplier: None,
                max_depth: 5,
                rate_table,
            },
            compression: None,
        }
    }

    fn test_plan(eligibility: CommissionEligibility) -> CompensationPlan {
        let structure = test_structure(test_rate_table());
        test_plan_with_structure(eligibility, structure)
    }

    fn test_plan_with_structure(
        eligibility: CommissionEligibility,
        structure: UnilevelStructureConfig,
    ) -> CompensationPlan {
        use crate::config::bonus::BonusConfig;
        use crate::config::payout::{CapsConfig, PayoutConfig};
        use crate::config::period::{PeriodConfig, PeriodLength};
        use crate::config::placement::PlacementConfig;

        CompensationPlan {
            name: "Test Plan".to_string(),
            version: 1,
            structures: vec![StructureConfig::Unilevel(structure)],
            period: PeriodConfig {
                length: PeriodLength::Month,
                start_date: chrono::NaiveDate::from_ymd_opt(2026, 1, 1).unwrap(),
                payout_lag_days: 14,
            },
            volume: VolumeConfig {
                inhibit_signup_volume: false,
                base_currency: "USD".to_string(),
                volume_to_dollar_multiplier: 1.0,
                deduct_qualifying_volume: false,
            },
            ranks: vec![
                RankDefinition {
                    name: "associate".to_string(),
                    ordinal: 1,
                    qualification: RankQualification {
                        structures: vec![],
                        required_products: vec![],
                    },
                    qualified_structures: vec!["Test Unilevel".to_string()],
                    demotion_policy: DemotionPolicy::PromotionOnly,
                },
                RankDefinition {
                    name: "silver".to_string(),
                    ordinal: 2,
                    qualification: RankQualification {
                        structures: vec![],
                        required_products: vec![],
                    },
                    qualified_structures: vec!["Test Unilevel".to_string()],
                    demotion_policy: DemotionPolicy::PromotionOnly,
                },
            ],
            rank_tracking: RankTrackingConfig {
                track_achieved_rank: false,
            },
            rank_features: RankFeaturesConfig {
                constraints_enabled: false,
                overrides_enabled: false,
            },
            eligibility,
            bonuses: BonusConfig::default(),
            payout: PayoutConfig::default(),
            caps: CapsConfig::default(),
            placement: PlacementConfig::default(),
        }
    }
```

Note: `BonusConfig::default()`, `PayoutConfig::default()`, `CapsConfig::default()`, and `PlacementConfig::default()` may not exist. If they don't, the implementing agent should check the actual struct definitions and construct them manually with reasonable test defaults. The test helpers are building a minimal `CompensationPlan` — only `eligibility`, `volume`, `ranks`, and `structures` are used by the calculator.

**Step 2: Write failing test for basic 3-node walk**

```rust
    #[test]
    fn basic_walk_three_node_chain() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // Volume source: leaf(3) generates 100 CV
        // Expected: mid(2) earns at level 1, root(1) earns at level 2
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), DistributorSnapshot {
            rank: "associate".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 2);

        // mid(2) is associate at level 1: 100 * 0.40 * 1.0 * 0.05 = 2.0
        let mid_earning = result.iter().find(|e| e.earner_id == test_uuid(2)).unwrap();
        assert_eq!(mid_earning.level, 1);
        assert_eq!(mid_earning.rate, 0.05);
        assert!((mid_earning.dollar_amount - 2.0).abs() < f64::EPSILON);

        // root(1) is silver at level 2: 100 * 0.40 * 1.0 * 0.06 = 2.4
        let root_earning = result.iter().find(|e| e.earner_id == test_uuid(1)).unwrap();
        assert_eq!(root_earning.level, 2);
        assert_eq!(root_earning.rate, 0.06);
        assert!((root_earning.dollar_amount - 2.4).abs() < f64::EPSILON);
    }
```

**Step 3: Run test to verify it fails**

Run: `cargo test -p network-engine basic_walk_three_node -- --nocapture`
Expected: FAIL — `calculate_unilevel` still has `todo!()`

**Step 4: Implement calculate_unilevel**

Replace the `todo!()` stub with the full implementation:

```rust
use crate::config::commission::CompressionMode;

pub fn calculate_unilevel(
    tree: &UnilevelTree,
    plan: &CompensationPlan,
    structure: &UnilevelStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    // Build rank name -> ordinal map for SkipBelowRank comparison
    let rank_ordinals: HashMap<&str, u16> = plan
        .ranks
        .iter()
        .map(|r| (r.name.as_str(), r.ordinal))
        .collect();

    // Prep phase: evaluate eligibility for all distributors
    let eligibility_cache = evaluate_eligibility(snapshots, tree, &plan.eligibility);

    // Walk config
    let max_depth = structure.level_commission.max_depth;
    let broad_pct = structure.level_commission.broad_commission_percent;
    let multiplier = structure
        .level_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    let compression = structure.compression.as_ref();
    let compression_enabled = compression.is_some_and(|c| c.enabled);

    let threshold_ordinal = compression.and_then(|c| {
        if matches!(c.mode, CompressionMode::SkipBelowRank) {
            c.rank_threshold
                .as_ref()
                .and_then(|name| rank_ordinals.get(name.as_str()).copied())
        } else {
            None
        }
    });

    let mut all_earnings = Vec::new();

    for source in volume {
        // Validate source exists in tree
        tree.get_upline(source.source_id, 0)
            .map_err(|_| CalculationError::SourceNotInTree(source.source_id))?;

        // Validate source exists in snapshots
        if !snapshots.contains_key(&source.source_id) {
            return Err(CalculationError::SourceNotInSnapshot(source.source_id));
        }

        // Get full upline (parent to root)
        let upline = tree.get_upline(source.source_id, 0).unwrap();

        let mut level: u8 = 1;

        for node in &upline {
            if level > max_depth {
                break;
            }

            let snapshot = match snapshots.get(&node.user_id) {
                Some(s) => s,
                None => {
                    // Missing snapshot: treat as ineligible
                    if compression_enabled {
                        continue; // compressed out, no level consumed
                    }
                    level = level.saturating_add(1);
                    continue;
                }
            };

            let elig = eligibility_cache.get(&node.user_id);
            let node_eligible = elig.is_some_and(|e| e.eligible);

            // Compression check
            let should_compress = if compression_enabled {
                let compress = compression.unwrap();
                match compress.mode {
                    CompressionMode::SkipInactive => !node_eligible,
                    CompressionMode::SkipBelowRank => {
                        let dist_ordinal = rank_ordinals
                            .get(snapshot.rank.as_str())
                            .copied()
                            .unwrap_or(0);
                        threshold_ordinal
                            .map(|t| dist_ordinal < t)
                            .unwrap_or(false)
                    }
                }
            } else {
                false
            };

            if should_compress {
                continue; // skip without consuming level
            }

            // Not compressed. Check if eligible.
            if !node_eligible {
                level = level.saturating_add(1); // forfeit level
                continue;
            }

            // Check per-distributor depth limit from active leg tiers
            if let Some(max_personal) = elig.and_then(|e| e.max_earning_depth) {
                if level > max_personal {
                    level = level.saturating_add(1);
                    continue;
                }
            }

            // Rate table lookup
            let rate = structure
                .level_commission
                .rate_table
                .get(&snapshot.rank)
                .and_then(|levels| levels.get(&level))
                .copied()
                .unwrap_or(0.0);

            if rate > 0.0 {
                all_earnings.push(CommissionEarning {
                    earner_id: node.user_id,
                    source_id: source.source_id,
                    level,
                    rate,
                    cv_amount: source.cv_amount,
                    dollar_amount: source.cv_amount * broad_pct * multiplier * rate,
                });
            }

            level = level.saturating_add(1);
        }
    }

    Ok(all_earnings)
}
```

**Step 5: Run test to verify it passes**

Run: `cargo test -p network-engine basic_walk_three_node -- --nocapture`
Expected: PASS

**Step 6: Write tests for rate lookup edge cases and depth limit**

```rust
    #[test]
    fn walk_rank_not_in_rate_table() {
        // Distributor with rank "bronze" which has no entry in rate table
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "bronze".to_string(), // not in rate table
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert!(result.is_empty()); // no rate found, no earning
    }

    #[test]
    fn walk_level_not_in_rate_table() {
        // Associate only has rates for levels 1-3, walk goes to level 4
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(4), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(test_uuid(id), DistributorSnapshot {
                rank: "associate".to_string(),
                ..eligible_snapshot()
            });
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // Associate has rates for levels 1-3 only.
        // uuid(4) at level 1, uuid(3) at level 2, uuid(2) at level 3,
        // uuid(1) at level 4 — no rate for associate at level 4
        assert_eq!(result.len(), 3);
        assert!(result.iter().all(|e| e.level <= 3));
    }

    #[test]
    fn walk_stops_at_max_depth() {
        // max_depth=2, tree is 5 deep
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 0).unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.level_commission.max_depth = 2;
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        for id in 1..=4 {
            snapshots.insert(test_uuid(id), DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            });
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(4),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // Only levels 1 and 2 should have earnings
        assert_eq!(result.len(), 2);
        assert!(result.iter().all(|e| e.level <= 2));
    }

    #[test]
    fn walk_dollar_amount_formula() {
        // Verify: cv * broad_pct * multiplier * rate
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.level_commission.broad_commission_percent = 0.50;
        structure.level_commission.volume_to_dollar_multiplier = Some(0.80);
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 200.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 1);
        // 200.0 * 0.50 * 0.80 * 0.07 (silver level 1) = 5.6
        let expected = 200.0 * 0.50 * 0.80 * 0.07;
        assert!((result[0].dollar_amount - expected).abs() < f64::EPSILON);
    }

    #[test]
    fn walk_multiplier_falls_back_to_plan_level() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.level_commission.volume_to_dollar_multiplier = None; // fallback
        let mut plan = test_plan(default_eligibility());
        plan.volume.volume_to_dollar_multiplier = 0.75;

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // 100 * 0.40 * 0.75 * 0.07 = 2.1
        let expected = 100.0 * 0.40 * 0.75 * 0.07;
        assert!((result[0].dollar_amount - expected).abs() < f64::EPSILON);
    }
```

**Step 7: Run tests to verify they pass**

Run: `cargo test -p network-engine walk_ -- --nocapture`
Expected: all PASS (the implementation from Step 4 should handle these)

**Step 8: Commit**

```bash
git add -f engine/network-engine/src/commission/unilevel.rs
git commit -m "feat(engine): implement basic unilevel commission walk with rate lookup"
```

---

### Task 4: Compression [Pending]

**Files:**
- Modify: `engine/network-engine/src/commission/unilevel.rs`

**Step 1: Write failing test for SkipInactive compression**

```rust
    #[test]
    fn compression_skip_inactive_preserves_level() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // mid(2) is ineligible (low PV), compression enabled
        // Expected: root(1) earns at level 1 (not level 2)
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), DistributorSnapshot {
            personal_volume: 0.0, // ineligible
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 1);
        // root earns at level 1 (mid was compressed out)
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1);
    }
```

**Step 2: Run test to verify it passes**

Run: `cargo test -p network-engine compression_skip_inactive -- --nocapture`
Expected: PASS (compression logic was implemented in Task 3, Step 4).

If it fails, debug and fix the compression logic.

**Step 3: Write test for SkipBelowRank compression**

```rust
    #[test]
    fn compression_skip_below_rank() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // mid(2) is "associate" (ordinal 1), threshold is "silver" (ordinal 2)
        // mid gets compressed out, root earns at level 1
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        });
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), DistributorSnapshot {
            rank: "associate".to_string(), // below threshold
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1); // level preserved
    }
```

**Step 4: Write test for no compression (level forfeited)**

```rust
    #[test]
    fn no_compression_ineligible_forfeits_level() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // mid(2) is ineligible, no compression
        // Expected: root(1) earns at level 2 (level 1 forfeited)
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

        let structure = test_structure(test_rate_table()); // no compression
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), DistributorSnapshot {
            personal_volume: 0.0, // ineligible
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 2); // level 1 forfeited
    }
```

**Step 5: Write test for compression with missing snapshot**

```rust
    #[test]
    fn compression_missing_snapshot_compressed_out() {
        // mid(2) has no snapshot. With compression, they're skipped.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        // test_uuid(2) intentionally missing
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1); // compressed, level preserved
    }

    #[test]
    fn no_compression_missing_snapshot_forfeits_level() {
        // mid(2) has no snapshot. Without compression, level forfeited.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        // test_uuid(2) missing
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].level, 2); // level 1 forfeited
    }
```

**Step 6: Run all compression tests**

Run: `cargo test -p network-engine compression -- --nocapture`
Expected: all PASS

**Step 7: Commit**

```bash
git add -f engine/network-engine/src/commission/unilevel.rs
git commit -m "test(engine): add compression tests for unilevel commission walk"
```

---

### Task 5: Active leg tier depth limits in walk [Pending]

**Files:**
- Modify: `engine/network-engine/src/commission/unilevel.rs`

**Step 1: Write test for per-distributor depth limit**

```rust
    #[test]
    fn active_leg_tier_limits_earning_depth() {
        // 5-node chain. root(1) has 1 active leg -> tier gives depth 2.
        // Volume from node 5.
        // root(1) is at level 4 from source, but personal limit is 2.
        // root should NOT earn.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(4), 0).unwrap();

        let elig = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![
                ActiveLegTier { min_active_legs: 1, max_commission_depth: 2 },
            ],
        };
        let structure = test_structure(test_rate_table());
        let plan = test_plan_with_structure(elig, structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(test_uuid(id), DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            });
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // node 4 at level 1: has 0 active legs (child 5 is source, not relevant for legs)
        //   Actually, node 5 IS an eligible child of node 4, so node 4 has 1 active leg -> depth 2.
        //   Node 4 earns at level 1. Level 1 <= 2. Earns.
        // node 3 at level 2: has 1 active leg (node 4) -> depth 2.
        //   Level 2 <= 2. Earns.
        // node 2 at level 3: has 1 active leg (node 3) -> depth 2.
        //   Level 3 > 2. Does NOT earn. Walk continues.
        // node 1 at level 4: has 1 active leg (node 2) -> depth 2.
        //   Level 4 > 2. Does NOT earn.
        assert_eq!(result.len(), 2);
        assert!(result.iter().all(|e| e.level <= 2));
    }
```

**Step 2: Run test**

Run: `cargo test -p network-engine active_leg_tier_limits -- --nocapture`
Expected: PASS (active leg tier logic was implemented in Task 3, Step 4).

**Step 3: Write test for unlimited tier**

```rust
    #[test]
    fn active_leg_tier_unlimited_earns_full_depth() {
        // root(1) has 3 active legs -> tier with depth 0 (unlimited)
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 0).unwrap();

        let elig = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![
                ActiveLegTier { min_active_legs: 3, max_commission_depth: 0 },
            ],
        };
        let structure = test_structure(test_rate_table());
        let plan = test_plan_with_structure(elig, structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(test_uuid(id), DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            });
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // node 2 at level 1, node 1 at level 2. Both should earn.
        // root(1) has 3 active legs -> unlimited depth
        assert_eq!(result.len(), 2);
    }
```

**Step 4: Run tests**

Run: `cargo test -p network-engine active_leg_tier -- --nocapture`
Expected: all PASS

**Step 5: Commit**

```bash
git add -f engine/network-engine/src/commission/unilevel.rs
git commit -m "test(engine): add active leg tier depth limit tests"
```

---

### Task 6: Error handling [Pending]

**Files:**
- Modify: `engine/network-engine/src/commission/unilevel.rs`

**Step 1: Write error handling tests**

```rust
    #[test]
    fn error_source_not_in_tree() {
        let tree = UnilevelTree::new();
        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let snapshots = HashMap::new();

        let volume = vec![VolumeSource {
            source_id: test_uuid(99),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        );

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::SourceNotInTree(id) if id == test_uuid(99)
        ));
    }

    #[test]
    fn error_source_not_in_snapshot() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let snapshots = HashMap::new(); // empty — source has no snapshot

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        );

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::SourceNotInSnapshot(id) if id == test_uuid(1)
        ));
    }
```

**Step 2: Run tests**

Run: `cargo test -p network-engine error_source -- --nocapture`
Expected: PASS

**Step 3: Commit**

```bash
git add -f engine/network-engine/src/commission/unilevel.rs
git commit -m "test(engine): add error handling tests for commission calculator"
```

---

### Task 7: Edge cases [Pending]

**Files:**
- Modify: `engine/network-engine/src/commission/unilevel.rs`

**Step 1: Write edge case tests**

```rust
    #[test]
    fn empty_volume_returns_empty_earnings() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &[],
        ).unwrap();

        assert!(result.is_empty());
    }

    #[test]
    fn root_as_source_no_upline() {
        // Root generates volume. No parent to walk to. No earnings.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert!(result.is_empty());
    }

    #[test]
    fn multiple_volume_sources_produce_separate_earnings() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource { source_id: test_uuid(2), cv_amount: 100.0 },
            VolumeSource { source_id: test_uuid(3), cv_amount: 200.0 },
        ];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // root earns from both sources
        assert_eq!(result.len(), 2);
        let from_2: Vec<_> = result.iter()
            .filter(|e| e.source_id == test_uuid(2))
            .collect();
        let from_3: Vec<_> = result.iter()
            .filter(|e| e.source_id == test_uuid(3))
            .collect();
        assert_eq!(from_2.len(), 1);
        assert_eq!(from_3.len(), 1);
        assert!((from_2[0].cv_amount - 100.0).abs() < f64::EPSILON);
        assert!((from_3[0].cv_amount - 200.0).abs() < f64::EPSILON);
    }

    #[test]
    fn ineligible_source_still_generates_walk() {
        // Source doesn't need to be eligible. They generate volume,
        // they don't earn from it.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "silver".to_string(),
            ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), DistributorSnapshot {
            personal_volume: 0.0, // ineligible, but still a valid source
            ..eligible_snapshot()
        });

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // root should still earn from the volume
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
    }
```

**Step 2: Run edge case tests**

Run: `cargo test -p network-engine -- --nocapture`
Expected: all PASS

**Step 3: Commit**

```bash
git add -f engine/network-engine/src/commission/unilevel.rs
git commit -m "test(engine): add edge case tests for commission calculator"
```

---

### Task 8: Integration test with realistic config [Pending]

**Files:**
- Modify: `engine/network-engine/src/commission/unilevel.rs`

**Step 1: Write integration test**

This test builds a realistic scenario with the Acme Wellness Plan config from the existing test in `config/mod.rs`. The tree has multiple branches and ranks.

```rust
    #[test]
    fn realistic_scenario_acme_wellness() {
        // Tree structure:
        //   company(1) [gold]
        //     ├── leader_a(2) [silver]
        //     │   ├── rep_a1(4) [associate]
        //     │   │   └── rep_a1a(7) [associate]
        //     │   └── rep_a2(5) [associate]
        //     └── leader_b(3) [silver]
        //         └── rep_b1(6) [associate]
        //
        // Volume: rep_a1a(7) generates 100 CV
        // Walk from 7 upward:
        //   Level 1: rep_a1(4) — associate, rate 0.05
        //   Level 2: leader_a(2) — silver, rate 0.06
        //   Level 3: company(1) — gold, rate 0.06
        //
        // broad_commission_percent = 0.40, multiplier = 1.0

        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(6), test_uuid(3), 0).unwrap();
        tree.add_node(test_uuid(7), test_uuid(4), 0).unwrap();

        let mut rate_table = BTreeMap::new();
        let mut associate_rates = BTreeMap::new();
        associate_rates.insert(1, 0.05);
        associate_rates.insert(2, 0.04);
        associate_rates.insert(3, 0.03);
        rate_table.insert("associate".to_string(), associate_rates);

        let mut silver_rates = BTreeMap::new();
        silver_rates.insert(1, 0.07);
        silver_rates.insert(2, 0.06);
        silver_rates.insert(3, 0.05);
        silver_rates.insert(4, 0.04);
        silver_rates.insert(5, 0.03);
        rate_table.insert("silver".to_string(), silver_rates);

        let mut gold_rates = BTreeMap::new();
        gold_rates.insert(1, 0.08);
        gold_rates.insert(2, 0.07);
        gold_rates.insert(3, 0.06);
        gold_rates.insert(4, 0.05);
        gold_rates.insert(5, 0.04);
        gold_rates.insert(6, 0.03);
        gold_rates.insert(7, 0.02);
        rate_table.insert("gold".to_string(), gold_rates);

        let structure = test_structure(rate_table);
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), DistributorSnapshot {
            rank: "gold".to_string(), ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(2), DistributorSnapshot {
            rank: "silver".to_string(), ..eligible_snapshot()
        });
        snapshots.insert(test_uuid(3), DistributorSnapshot {
            rank: "silver".to_string(), ..eligible_snapshot()
        });
        for id in 4..=7 {
            snapshots.insert(test_uuid(id), DistributorSnapshot {
                rank: "associate".to_string(), ..eligible_snapshot()
            });
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(7),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        assert_eq!(result.len(), 3);

        // rep_a1(4) at level 1: associate rate = 0.05
        let e4 = result.iter().find(|e| e.earner_id == test_uuid(4)).unwrap();
        assert_eq!(e4.level, 1);
        assert!((e4.dollar_amount - 100.0 * 0.40 * 1.0 * 0.05).abs() < f64::EPSILON);

        // leader_a(2) at level 2: silver rate = 0.06
        let e2 = result.iter().find(|e| e.earner_id == test_uuid(2)).unwrap();
        assert_eq!(e2.level, 2);
        assert!((e2.dollar_amount - 100.0 * 0.40 * 1.0 * 0.06).abs() < f64::EPSILON);

        // company(1) at level 3: gold rate = 0.06
        let e1 = result.iter().find(|e| e.earner_id == test_uuid(1)).unwrap();
        assert_eq!(e1.level, 3);
        assert!((e1.dollar_amount - 100.0 * 0.40 * 1.0 * 0.06).abs() < f64::EPSILON);
    }
```

**Step 2: Run test**

Run: `cargo test -p network-engine realistic_scenario -- --nocapture`
Expected: PASS

**Step 3: Commit**

```bash
git add -f engine/network-engine/src/commission/unilevel.rs
git commit -m "test(engine): add realistic integration test for unilevel commissions"
```

---

### Task 9: Property-based tests [Pending]

**Files:**
- Create: `engine/network-engine/tests/unilevel_commission_properties.rs`

**Step 1: Write property-based tests**

```rust
use network_engine::commission::{
    calculate_unilevel, CommissionEarning, DistributorSnapshot, VolumeSource,
};
use network_engine::config::commission::LevelCommissionConfig;
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::rank::{
    DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification,
    RankTrackingConfig,
};
use network_engine::config::volume::VolumeConfig;
use network_engine::config::{
    CompensationPlan, StructureConfig, UnilevelStructureConfig,
};
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use std::collections::{BTreeMap, HashMap};
use uuid::Uuid;

fn uuid_from_index(i: usize) -> Uuid {
    let bytes = (i as u128).to_be_bytes();
    Uuid::from_bytes(bytes)
}

fn build_test_plan(max_depth: u8) -> (CompensationPlan, UnilevelStructureConfig) {
    let mut rate_table = BTreeMap::new();
    let mut rates = BTreeMap::new();
    for level in 1..=max_depth {
        rates.insert(level, 0.05);
    }
    rate_table.insert("member".to_string(), rates);

    let structure = UnilevelStructureConfig {
        name: "Test".to_string(),
        level_commission: LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth,
            rate_table,
        },
        compression: None,
    };

    // Build plan. The implementing agent should construct a minimal
    // CompensationPlan here, matching the pattern from test helpers
    // in the unit tests. Key fields: volume.volume_to_dollar_multiplier = 1.0,
    // eligibility with minimum_pv = 0.0 (all eligible), ranks with "member".
    // Use the same approach as test_plan_with_structure from Task 3.
    todo!("Construct minimal CompensationPlan — match unit test helper pattern")
}

proptest! {
    #[test]
    fn no_earning_beyond_max_depth(
        tree_size in 3..50usize,
        max_depth in 1..10u8,
    ) {
        let (plan, structure) = build_test_plan(max_depth);

        // Build a chain: 0 -> 1 -> 2 -> ... -> tree_size-1
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), 0).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(uuid_from_index(i), DistributorSnapshot {
                rank: "member".to_string(),
                personal_volume: 100.0,
                status: "active".to_string(),
                has_order_in_period: true,
            });
        }

        // Volume from the deepest node
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(tree_size - 1),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        // No earning should have level > max_depth
        for earning in &result {
            prop_assert!(
                earning.level <= max_depth,
                "Earning at level {} exceeds max_depth {}",
                earning.level,
                max_depth
            );
        }
    }

    #[test]
    fn dollar_amount_matches_formula(
        cv in 1.0..10000.0f64,
    ) {
        let (plan, structure) = build_test_plan(3);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let rate = 0.05; // all levels use 0.05

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        tree.add_node(uuid_from_index(1), uuid_from_index(0), 0).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid_from_index(0), DistributorSnapshot {
            rank: "member".to_string(),
            personal_volume: 100.0,
            status: "active".to_string(),
            has_order_in_period: true,
        });
        snapshots.insert(uuid_from_index(1), DistributorSnapshot {
            rank: "member".to_string(),
            personal_volume: 100.0,
            status: "active".to_string(),
            has_order_in_period: true,
        });

        let volume = vec![VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: cv,
        }];

        let result = calculate_unilevel(
            &tree, &plan, &structure, &snapshots, &volume,
        ).unwrap();

        prop_assert_eq!(result.len(), 1);
        let expected = cv * broad_pct * multiplier * rate;
        let diff = (result[0].dollar_amount - expected).abs();
        prop_assert!(
            diff < 1e-10,
            "Dollar amount {} != expected {}",
            result[0].dollar_amount,
            expected
        );
    }
}
```

Note: The `build_test_plan` function has a `todo!()` placeholder. The implementing agent must construct a minimal `CompensationPlan` matching the unit test helper pattern. All config types need valid values. Check the actual struct definitions and construct them. If `Default` impls exist, use them for bonus, payout, caps, and placement configs.

**Step 2: Run property tests**

Run: `cargo test -p network-engine --test unilevel_commission_properties`
Expected: PASS

**Step 3: Commit**

```bash
git add -f engine/network-engine/tests/unilevel_commission_properties.rs
git commit -m "test(engine): add property-based tests for unilevel commission calculator"
```

---

### Task 10: ADR-017 [Pending]

**Files:**
- Create: `decisions/017-commission-calculation-architecture.md`

**Step 1: Write ADR-017**

Extract the 6 architectural decisions from the design doc into a formal ADR. Follow the existing ADR format in `decisions/`. The content is already written in the design doc's "Architectural Decisions (ADR-017)" section.

The implementing agent should:
1. Read an existing ADR file (e.g., `decisions/016-eventstore-design.md`) to match the format
2. Write `decisions/017-commission-calculation-architecture.md` with all 6 decisions
3. Update any ADR index/README if one exists in `decisions/`

**Step 2: Commit**

```bash
git add -f decisions/017-commission-calculation-architecture.md
git commit -m "docs(decisions): add ADR-017 commission calculation architecture"
```

---

### Task 11: Final cleanup and verification [Pending]

**Step 1: Run full test suite**

Run: `cargo test -p network-engine`
Expected: ALL tests pass (existing + new)

**Step 2: Run formatter and linter**

Run: `cargo fmt -p network-engine -- --check`
Expected: no formatting issues

Run: `cargo clippy -p network-engine -- -D warnings`
Expected: no warnings

**Step 3: Fix any issues found**

If formatter or linter flags issues, fix them and re-run.

**Step 4: Update BUGS_AND_TODOS.md**

Add any new items discovered during implementation. Remove or update items that are no longer relevant.

**Step 5: Final commit**

```bash
git add -A
git commit -m "chore(engine): final cleanup for unilevel commission calculator"
```
