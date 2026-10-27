mod common;
use common::{
    build_multi_tier_stairstep_plan, build_stairstep_plan, cents, member_snapshot, uuid_from_index,
    whole,
};

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_stairstep};
use network_engine::config::commission::LevelCommissionConfig;
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::stairstep::{
    BreakawayConfig, BreakawayTier, DifferentialConfig, MinOverride, OverrideMode, OverrideStrategy,
};
use network_engine::config::{CompensationPlan, StairstepStructureConfig, StructureConfig};
use network_engine::d;
use network_engine::decimal::{AuthoredDec, Dec};
use network_engine::test_support::{build_test_plan, make_rank, test_plan_identity};
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use std::collections::{BTreeMap, BTreeSet, HashMap};
use uuid::Uuid;

proptest! {
    /// No earning should have a level exceeding max_depth.
    #[test]
    fn no_earning_beyond_max_depth(
        tree_size in 3..50usize,
        max_depth in 1..10u8,
    ) {
        let (plan, structure) = build_stairstep_plan(max_depth);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                member_snapshot(),
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: d!(100.0),
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        for earning in &result {
            prop_assert!(
                earning.level <= max_depth,
                "Earning at level {} exceeds max_depth {}",
                earning.level, max_depth
            );
        }
    }

    /// All earnings have non-negative dollar_amount.
    #[test]
    fn all_earnings_non_negative(
        tree_size in 2..30usize,
        cv in cents(0, 1_000_000),
    ) {
        let (plan, structure) = build_stairstep_plan(5);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                member_snapshot(),
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        for earning in &result {
            prop_assert!(
                earning.dollar_amount >= d!(0.0),
                "Negative dollar_amount: {}",
                earning.dollar_amount
            );
        }
    }

    /// No duplicate (source_id, earner_id) pairs.
    #[test]
    fn no_duplicate_earners(
        tree_size in 2..30usize,
    ) {
        let (plan, structure) = build_stairstep_plan(5);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                member_snapshot(),
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: d!(100.0),
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        let mut seen = std::collections::HashSet::new();
        for earning in &result {
            prop_assert!(
                seen.insert((earning.source_id, earning.earner_id)),
                "Duplicate earning for source {:?} earner {:?}",
                earning.source_id, earning.earner_id
            );
        }
    }

    /// Every earner exists in the tree.
    #[test]
    fn every_earner_in_tree(
        tree_size in 2..30usize,
    ) {
        let (plan, structure) = build_stairstep_plan(5);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                member_snapshot(),
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: d!(100.0),
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        for earning in &result {
            prop_assert!(
                tree.contains(earning.earner_id),
                "Earner {:?} not in tree",
                earning.earner_id
            );
        }
    }

    /// Total payout is bounded.
    #[test]
    fn total_payout_bounded(
        tree_size in 2..30usize,
        cv in cents(100, 1_000_000),
    ) {
        let (plan, structure) = build_stairstep_plan(5);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let max_rate: Dec = d!(0.10); // max of level rate (0.05) and override rate (0.10)
        let max_depth = structure.level_commission.max_depth;

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                member_snapshot(),
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        let total = Dec::sum(result.iter().map(|e| e.dollar_amount)).unwrap();
        // Level comms: up to max_depth levels * 0.05 each
        // Override: up to 0.10 on group volume (bounded by total CV)
        let upper_bound = cv
            .dec()
            .try_mul(broad_pct)
            .unwrap()
            .try_mul(multiplier)
            .unwrap()
            .try_mul(max_rate)
            .unwrap()
            .try_mul(whole::<Dec>(max_depth + 1))
            .unwrap();
        prop_assert!(
            total <= upper_bound,
            "Total payout {} exceeds upper bound {}",
            total, upper_bound
        );
    }

    /// Total payout is bounded even with breakaway overrides active.
    #[test]
    fn total_payout_bounded_with_overrides(
        tree_size in 3..30usize,
        cv in cents(100, 1_000_000),
    ) {
        use network_engine::config::stairstep::{
            BreakawayConfig, DifferentialConfig, MinOverride, OverrideMode, OverrideStrategy,
        };

        let (plan, mut structure) = build_stairstep_plan(5);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let max_depth = structure.level_commission.max_depth;

        structure.breakaway = Some(BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::Differential(DifferentialConfig {
                    rank_rates: {
                        let mut m = std::collections::BTreeMap::new();
                        m.insert("member".to_string(), d!(0.05));
                        m.insert("director".to_string(), d!(0.10));
                        m
                    },
                    min_override: MinOverride::Rate { value: d!(0.02) },
                }),
                generation_overrides: None,
            },
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i % 3 == 0 { "director" } else { "member" };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(100.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        let total = Dec::sum(result.iter().map(|e| e.dollar_amount)).unwrap();
        let level_rate: Dec = d!(0.05);
        let override_rate: Dec = d!(0.10);
        // Level bound: up to max_depth levels at 0.05 each, based on CV.
        let level_bound = cv
            .dec()
            .try_mul(broad_pct)
            .unwrap()
            .try_mul(multiplier)
            .unwrap()
            .try_mul(level_rate)
            .unwrap()
            .try_mul(whole::<Dec>(max_depth))
            .unwrap();
        // Override bound: overrides use group volume (personal_volume sums),
        // not the CV from volume sources. Total group volume across all
        // breakaways equals total personal volume = tree_size * 100.
        let total_pv: Dec = whole(tree_size * 100);
        let override_bound = total_pv
            .try_mul(broad_pct)
            .unwrap()
            .try_mul(multiplier)
            .unwrap()
            .try_mul(override_rate)
            .unwrap();
        let upper_bound = level_bound.try_add(override_bound).unwrap();
        prop_assert!(
            total <= upper_bound,
            "Total payout {} exceeds upper bound {}",
            total, upper_bound
        );
    }

    /// A currency floor never pays more than the leg it is drawn from, and an
    /// earning it produced carries no rate.
    ///
    /// The unit tests for the currency arm use fixed 3- and 4-node chains. This
    /// covers arbitrary shapes and floor sizes, which is where the pool gate's
    /// two duplicated copies could diverge.
    #[test]
    fn currency_floor_never_exceeds_its_leg_and_reports_no_rate(
        tree_size in 3..30usize,
        floor in cents(10, 50_000),
    ) {
        use network_engine::config::stairstep::{
            BreakawayConfig, DifferentialConfig, MinOverride, OverrideMode, OverrideStrategy,
        };

        let (plan, mut structure) = build_stairstep_plan(5);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;

        structure.breakaway = Some(BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::Differential(DifferentialConfig {
                    rank_rates: {
                        let mut m = std::collections::BTreeMap::new();
                        // Flat ladder, so every gap is zero and the floor is
                        // what decides. A gap would mask the arm under test.
                        m.insert("member".to_string(), d!(0.10));
                        m.insert("director".to_string(), d!(0.10));
                        m
                    },
                    min_override: MinOverride::Currency { value: floor },
                }),
                generation_overrides: None,
            },
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i % 3 == 0 { "director" } else { "member" };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(100.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: uuid_from_index(tree_size - 1),
            cv_amount: d!(100.0),
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        for earning in result.iter().filter(|e| e.rate.is_none()) {
            // A floor-produced earning pays exactly the floor, and only when
            // its own leg's pool could cover it.
            prop_assert!(
                earning.dollar_amount == floor.dec(),
                "a floor-produced earning paid {} rather than the {} floor: {:?}",
                earning.dollar_amount, floor, earning
            );
            let pool = earning
                .cv_amount
                .try_mul(broad_pct)
                .unwrap()
                .try_mul(multiplier)
                .unwrap();
            prop_assert!(
                earning.dollar_amount <= pool,
                "paid {} out of a pool of {} on cv {}: {:?}",
                earning.dollar_amount, pool, earning.cv_amount, earning
            );
        }
    }

    /// Level and override earnings partition cleanly: no earner receives
    /// both a level commission and an override on the same volume source.
    #[test]
    fn level_and_override_partition_cleanly(
        tree_size in 3..30usize,
    ) {
        use network_engine::config::stairstep::{
            BreakawayConfig, DifferentialConfig, MinOverride, OverrideMode, OverrideStrategy,
        };

        let (plan, mut structure) = build_stairstep_plan(5);

        // Set up breakaway config so overrides can happen.
        // Threshold rank = "director". Give every other node "director" rank.
        structure.breakaway = Some(BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::Differential(DifferentialConfig {
                    rank_rates: {
                        let mut m = std::collections::BTreeMap::new();
                        m.insert("member".to_string(), d!(0.05));
                        m.insert("director".to_string(), d!(0.10));
                        m
                    },
                    min_override: MinOverride::Rate { value: d!(0.02) },
                }),
                generation_overrides: None,
            },
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i % 2 == 0 { "director" } else { "member" };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(100.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: d!(100.0),
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        // Partition level earners and override earners.
        // Level earnings: source_id == original volume source.
        // Override earnings: source_id == a breakaway distributor.
        let mut level_earners = std::collections::HashSet::new();
        let mut override_earners = std::collections::HashSet::new();

        for earning in &result {
            if earning.source_id == uuid_from_index(source_idx) {
                level_earners.insert(earning.earner_id);
            } else {
                // Override source_id should be a breakaway
                let is_breakaway = snapshots
                    .get(&earning.source_id)
                    .map(|s| s.rank == "director")
                    .unwrap_or(false);
                prop_assert!(
                    is_breakaway,
                    "Override source {:?} is not a breakaway distributor",
                    earning.source_id
                );
                override_earners.insert(earning.earner_id);
            }
        }

        // The partition property: no earner receives both a level
        // commission and an override commission. Walk 1 stays within
        // the source's personal group while Walk 2 pays ancestors
        // above breakaway boundaries.
        let overlap: Vec<_> = level_earners.intersection(&override_earners).collect();
        prop_assert!(
            overlap.is_empty(),
            "Earners received both level and override commissions: {:?}",
            overlap
        );
    }

    /// Multi-tier walk pays at most one earner per (source, level).
    ///
    /// Each tier in a Split-Out Group pays exactly one earner, so no two
    /// emitted earnings may share the same `(source_id, level)`.
    #[test]
    fn multi_tier_no_double_pay_within_a_tier(
        tree_size in 3..30usize,
        tiers in prop::collection::vec(
            (1u8..=4u8, cents(0, 101)).prop_map(|(g, r)| BreakawayTier {
                min_split_out_groups: g,
                rate: r,
            }),
            1..=6,
        ),
    ) {
        let (plan, structure) = build_multi_tier_stairstep_plan(tiers);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let source_idx = tree_size - 1;

        // Force the volume source to be "member" so override earnings
        // (source_id == breakaway distributor) never collide with the
        // level earnings' source_id (== volume source). Other distributors
        // alternate ranks to create Split-Out Groups.
        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i == source_idx {
                "member"
            } else if i % 2 == 0 {
                "director"
            } else {
                "member"
            };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(100.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: d!(100.0),
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        let mut seen = std::collections::HashSet::new();
        for earning in &result {
            prop_assert!(
                seen.insert((earning.source_id, earning.level)),
                "Two earnings share (source_id, level) = ({:?}, {}): {:?}",
                earning.source_id, earning.level, earning
            );
        }
    }

    /// Two runs of `calculate_stairstep` on identical inputs produce the
    /// same earnings vector, element-wise.
    ///
    /// The Task 1 `level` tiebreaker plus `sort_earnings` inside the
    /// calculator gives full ordering. A failure means a real
    /// non-determinism bug — stop and investigate.
    #[test]
    fn multi_tier_output_is_deterministic(
        tree_size in 3..30usize,
        tiers in prop::collection::vec(
            (1u8..=4u8, cents(0, 101)).prop_map(|(g, r)| BreakawayTier {
                min_split_out_groups: g,
                rate: r,
            }),
            1..=6,
        ),
    ) {
        let (plan, structure) = build_multi_tier_stairstep_plan(tiers);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        // Many directors so multiple Split-Out Groups exist and the
        // multi-tier walk actually emits earnings to sort.
        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i % 2 == 0 { "director" } else { "member" };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(100.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: d!(100.0),
        }];

        let first = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;
        let second = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;
        prop_assert_eq!(first, second);
    }

    /// Every multi-tier earning's dollar amount matches
    /// `cv_amount * multiplier * rate`, and its rate is one of the
    /// configured tier rates.
    #[test]
    fn multi_tier_rate_bound(
        tree_size in 3..30usize,
        tiers in prop::collection::vec(
            (1u8..=4u8, cents(0, 101)).prop_map(|(g, r)| BreakawayTier {
                min_split_out_groups: g,
                rate: r,
            }),
            1..=6,
        ),
    ) {
        let (plan, structure) = build_multi_tier_stairstep_plan(tiers.clone());
        let multiplier = plan.volume.volume_to_dollar_multiplier;

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let source_idx = tree_size - 1;

        // Force the volume source to be "member" so override earnings
        // (source_id == breakaway distributor) and level earnings
        // (source_id == volume source) never overlap on source_id.
        // Other distributors alternate ranks to create Split-Out Groups.
        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i == source_idx {
                "member"
            } else if i % 2 == 0 {
                "director"
            } else {
                "member"
            };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(100.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: d!(100.0),
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume, &network_engine::test_support::test_plan_identity()).unwrap().earnings;

        // Multi-tier earnings have source_id == a breakaway distributor;
        // level commissions have source_id == the original volume source.
        // Restrict the property to the override earnings.
        for earning in result.iter().filter(|e| e.source_id != uuid_from_index(source_idx)) {
            // Every multi-tier override is rate-produced, so a null rate here
            // is itself a failure rather than a case to skip.
            let rate = earning.rate.expect("a multi-tier override earning carries a rate");
            let expected = earning
                .cv_amount
                .try_mul(multiplier)
                .unwrap()
                .try_mul(rate)
                .unwrap();
            prop_assert!(
                earning.dollar_amount == expected,
                "dollar_amount {} != cv_amount {} * multiplier {} * rate {} (= {}) for earning {:?}",
                earning.dollar_amount, earning.cv_amount, multiplier, rate, expected, earning
            );
            prop_assert!(
                tiers.iter().any(|t| t.rate.dec() == rate),
                "earning rate {} is not one of the configured tier rates {:?}",
                rate, tiers.iter().map(|t| t.rate).collect::<Vec<_>>()
            );
        }
    }
}

/// Single-node tree produces no earnings.
#[test]
fn single_node_no_earnings() {
    let (plan, structure) = build_stairstep_plan(5);

    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();

    let mut snapshots = HashMap::new();
    snapshots.insert(
        uuid_from_index(0),
        DistributorSnapshot {
            rank: "member".to_string(),
            personal_volume: d!(100.0),
            status: "active".to_string(),
            has_order_in_period: true,
        },
    );

    let volume = vec![VolumeSource {
        source_id: uuid_from_index(0),
        cv_amount: d!(100.0),
    }];

    let result = calculate_stairstep(
        &tree,
        &plan,
        &structure,
        &snapshots,
        &volume,
        &network_engine::test_support::test_plan_identity(),
    )
    .unwrap()
    .earnings;
    assert!(result.is_empty());
}

/// A stairstep plan with associate, director and senior_director ranks and a
/// differential override on director breakaways.
fn breakaway_stairstep_plan() -> (CompensationPlan, StairstepStructureConfig) {
    let mut rate_table = BTreeMap::new();
    for rank in ["associate", "director"] {
        let rates: BTreeMap<u8, AuthoredDec> = (1u8..=5).map(|level| (level, d!(0.05))).collect();
        rate_table.insert(rank.to_string(), rates);
    }
    let structure = StairstepStructureConfig {
        name: "Test".to_string(),
        level_commission: LevelCommissionConfig {
            broad_commission_percent: d!(0.40),
            volume_to_dollar_multiplier: None,
            max_depth: 5,
            rate_table,
        },
        compression: None,
        breakaway: Some(BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: true,
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::Differential(DifferentialConfig {
                    rank_rates: BTreeMap::from([
                        ("director".to_string(), d!(0.10)),
                        ("senior_director".to_string(), d!(0.15)),
                    ]),
                    min_override: MinOverride::Rate { value: d!(0.02) },
                }),
                generation_overrides: None,
            },
        }),
    };
    let eligibility = CommissionEligibility {
        minimum_pv: d!(100.0),
        require_order_in_period: false,
        eligible_statuses: vec!["active".to_string()],
        active_leg_tiers: vec![],
    };
    let mut plan = build_test_plan(
        eligibility,
        StructureConfig::Stairstep(structure.clone()),
        "Test",
    );
    plan.ranks = vec![
        make_rank("associate", 1, vec!["Test".to_string()]),
        make_rank("director", 2, vec!["Test".to_string()]),
        make_rank("senior_director", 3, vec!["Test".to_string()]),
    ];
    (plan, structure)
}

/// A chain of nodes 0 to 4, each sponsored and placed under the one before.
fn breakaway_chain() -> UnilevelTree {
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    for i in 1..5 {
        tree.add_node(
            uuid_from_index(i),
            uuid_from_index(i - 1),
            uuid_from_index(i - 1),
            i as i64,
        )
        .unwrap();
    }
    tree
}

/// Snapshots for the chain: senior_director, director, then three associates.
fn breakaway_chain_snapshots(pvs: [AuthoredDec; 5]) -> HashMap<Uuid, DistributorSnapshot> {
    let ranks = [
        "senior_director",
        "director",
        "associate",
        "associate",
        "associate",
    ];
    (0..5)
        .map(|i| {
            (
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: ranks[i].to_string(),
                    personal_volume: pvs[i],
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            )
        })
        .collect()
}

#[test]
fn stairstep_walk_two_amounts_do_not_depend_on_snapshot_map_order() {
    let tree = breakaway_chain();
    let (plan, structure) = breakaway_stairstep_plan();
    let volume = vec![VolumeSource {
        source_id: uuid_from_index(4),
        cv_amount: d!(100.0),
    }];
    let mut seen = BTreeSet::new();
    for _ in 0..256 {
        let snapshots = breakaway_chain_snapshots([d!(150.0), d!(0.1), d!(0.2), d!(0.3), d!(0.7)]);
        let earnings = calculate_stairstep(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &test_plan_identity(),
        )
        .unwrap()
        .earnings;
        let from_breakaway: Vec<_> = earnings
            .iter()
            .filter(|e| e.source_id == uuid_from_index(1))
            .collect();
        assert_eq!(from_breakaway.len(), 1);
        assert_eq!(from_breakaway[0].earner_id, uuid_from_index(0));
        seen.insert((from_breakaway[0].cv_amount, from_breakaway[0].dollar_amount));
    }
    assert_eq!(
        seen,
        BTreeSet::from([(d!(1.3), d!(0.03))]),
        "distinct (cv_amount, dollar_amount) across runs: {seen:?}"
    );
}

/// Five nodes in a chain, placed so that id order differs from depth order:
/// 0, then 1, 4, 2 and 3 going down.
fn breakaway_chain_out_of_id_order() -> UnilevelTree {
    let order = [0, 1, 4, 2, 3];
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(order[0]), 0).unwrap();
    for depth in 1..order.len() {
        let parent = uuid_from_index(order[depth - 1]);
        tree.add_node(uuid_from_index(order[depth]), parent, parent, depth as i64)
            .unwrap();
    }
    tree
}

proptest! {
    #[test]
    fn stairstep_breakaway_group_volume_sums_in_user_id_order(
        pvs in prop::array::uniform4(cents(1, 100_000)),
    ) {
        let tree = breakaway_chain_out_of_id_order();
        let (plan, structure) = breakaway_stairstep_plan();
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(4),
            cv_amount: d!(100.0),
        }];
        let snapshots = breakaway_chain_snapshots([d!(150.0), pvs[0], pvs[1], pvs[2], pvs[3]]);
        let mut members: Vec<Uuid> = [1, 4, 2, 3].map(uuid_from_index).to_vec();
        members.sort();
        let mut expected = Dec::ZERO;
        for id in &members {
            expected = expected.try_add(snapshots[id].personal_volume).unwrap();
        }
        let earnings = calculate_stairstep(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &test_plan_identity(),
        )
        .unwrap()
        .earnings;
        let from_breakaway: Vec<_> = earnings
            .iter()
            .filter(|e| e.source_id == uuid_from_index(1))
            .collect();
        prop_assert_eq!(from_breakaway.len(), 1);
        prop_assert_eq!(from_breakaway[0].earner_id, uuid_from_index(0));
        prop_assert_eq!(from_breakaway[0].cv_amount, expected);
    }
}
