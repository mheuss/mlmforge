mod common;

use common::{build_generation_plan, uuid_from_index};

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_generation};
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use std::collections::{HashMap, HashSet};

// ===========================================================================
// Integration test: reference tree from design-rationale 013
// ===========================================================================

/// Reference tree from design-rationale 013.
///
/// ```text
/// Alice (Director, rank 8/ordinal 2)
/// ├── Bob (Associate, rank 3/ordinal 1)
/// │   ├── Carol (Associate, rank 2/ordinal 1)
/// │   │   └── Dave (Director, rank 8/ordinal 2)
/// │   │       ├── Eve (Associate, rank 1/ordinal 1)
/// │   │       └── Frank (Director, rank 8/ordinal 2)
/// │   └── Grace (Associate, rank 4/ordinal 1)
/// └── Henry (Associate, rank 1/ordinal 1)
/// ```
///
/// Note: the plan uses two ranks (associate ordinal 1, director ordinal 2).
/// The "rank 8" in the diagram refers to the design doc's notation; in our
/// plan, director = ordinal 2, associate = ordinal 1.
#[test]
fn reference_tree_threshold_mode() {
    // Build the tree. Using indices for readability:
    // 0=Alice, 1=Bob, 2=Carol, 3=Dave, 4=Eve, 5=Frank, 6=Grace, 7=Henry
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap(); // Alice
    tree.add_node(
        uuid_from_index(1),
        uuid_from_index(0),
        uuid_from_index(0),
        1,
    )
    .unwrap(); // Bob under Alice
    tree.add_node(
        uuid_from_index(7),
        uuid_from_index(0),
        uuid_from_index(0),
        2,
    )
    .unwrap(); // Henry under Alice
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(1),
        uuid_from_index(1),
        3,
    )
    .unwrap(); // Carol under Bob
    tree.add_node(
        uuid_from_index(6),
        uuid_from_index(1),
        uuid_from_index(1),
        4,
    )
    .unwrap(); // Grace under Bob
    tree.add_node(
        uuid_from_index(3),
        uuid_from_index(2),
        uuid_from_index(2),
        5,
    )
    .unwrap(); // Dave under Carol
    tree.add_node(
        uuid_from_index(4),
        uuid_from_index(3),
        uuid_from_index(3),
        6,
    )
    .unwrap(); // Eve under Dave
    tree.add_node(
        uuid_from_index(5),
        uuid_from_index(3),
        uuid_from_index(3),
        7,
    )
    .unwrap(); // Frank under Dave

    let (plan, structure) = build_generation_plan(4);

    let director = DistributorSnapshot {
        rank: "director".to_string(),
        personal_volume: 150.0,
        status: "active".to_string(),
        has_order_in_period: true,
    };
    let associate = DistributorSnapshot {
        rank: "associate".to_string(),
        personal_volume: 150.0,
        status: "active".to_string(),
        has_order_in_period: true,
    };

    let mut snapshots = HashMap::new();
    snapshots.insert(uuid_from_index(0), director.clone()); // Alice - Director
    snapshots.insert(uuid_from_index(1), associate.clone()); // Bob
    snapshots.insert(uuid_from_index(2), associate.clone()); // Carol
    snapshots.insert(uuid_from_index(3), director.clone()); // Dave - Director
    snapshots.insert(uuid_from_index(4), associate.clone()); // Eve
    snapshots.insert(uuid_from_index(5), director.clone()); // Frank - Director
    snapshots.insert(uuid_from_index(6), associate.clone()); // Grace
    snapshots.insert(uuid_from_index(7), associate.clone()); // Henry

    // --- Volume from Eve (100 CV) ---
    // Walk from Eve upward: Dave (Director, gen 1), Carol (skip), Bob (skip),
    // Alice (Director, gen 2).
    // Dave earns gen 1 at 0.10: 100 * 1.0 * 0.10 = 10.0
    // Alice earns gen 2 at 0.07: 100 * 1.0 * 0.07 = 7.0
    let volume_eve = vec![VolumeSource {
        source_id: uuid_from_index(4),
        cv_amount: 100.0,
    }];
    let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume_eve).unwrap();

    assert_eq!(result.len(), 2, "Eve volume: expected 2 earners");
    let dave = result
        .iter()
        .find(|e| e.earner_id == uuid_from_index(3))
        .expect("Dave should earn");
    assert_eq!(dave.level, 1);
    assert!((dave.dollar_amount - 10.0).abs() < 1e-10);

    let alice = result
        .iter()
        .find(|e| e.earner_id == uuid_from_index(0))
        .expect("Alice should earn");
    assert_eq!(alice.level, 2);
    assert!((alice.dollar_amount - 7.0).abs() < 1e-10);

    // --- Volume from Henry (50 CV) ---
    // Walk from Henry upward: Alice (Director, gen 1).
    // Alice earns gen 1 at 0.10: 50 * 1.0 * 0.10 = 5.0
    let volume_henry = vec![VolumeSource {
        source_id: uuid_from_index(7),
        cv_amount: 50.0,
    }];
    let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume_henry).unwrap();

    assert_eq!(result.len(), 1, "Henry volume: expected 1 earner");
    assert_eq!(result[0].earner_id, uuid_from_index(0));
    assert_eq!(result[0].level, 1);
    assert!((result[0].dollar_amount - 5.0).abs() < 1e-10);

    // --- Volume from Grace (75 CV) ---
    // Walk from Grace upward: Bob (skip), Alice (Director, gen 1).
    // Alice earns gen 1 at 0.10: 75 * 1.0 * 0.10 = 7.5
    let volume_grace = vec![VolumeSource {
        source_id: uuid_from_index(6),
        cv_amount: 75.0,
    }];
    let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume_grace).unwrap();

    assert_eq!(result.len(), 1, "Grace volume: expected 1 earner");
    assert_eq!(result[0].earner_id, uuid_from_index(0));
    assert_eq!(result[0].level, 1);
    assert!((result[0].dollar_amount - 7.5).abs() < 1e-10);

    // --- Volume from Bob (200 CV) ---
    // Walk from Bob upward: Alice (Director, gen 1).
    // Alice earns gen 1 at 0.10: 200 * 1.0 * 0.10 = 20.0
    let volume_bob = vec![VolumeSource {
        source_id: uuid_from_index(1),
        cv_amount: 200.0,
    }];
    let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume_bob).unwrap();

    assert_eq!(result.len(), 1, "Bob volume: expected 1 earner");
    assert_eq!(result[0].earner_id, uuid_from_index(0));
    assert_eq!(result[0].level, 1);
    assert!((result[0].dollar_amount - 20.0).abs() < 1e-10);

    // --- Multiple sources at once ---
    // Eve (100) + Henry (50): should produce earnings for both sources combined.
    let volume_multi = vec![
        VolumeSource {
            source_id: uuid_from_index(4),
            cv_amount: 100.0,
        },
        VolumeSource {
            source_id: uuid_from_index(7),
            cv_amount: 50.0,
        },
    ];
    let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume_multi).unwrap();

    // Alice earns on both sources (gen 2 from Eve, gen 1 from Henry).
    // Dave earns gen 1 from Eve only.
    assert_eq!(result.len(), 3, "Multi-source: expected 3 earnings");

    let alice_earnings: Vec<_> = result
        .iter()
        .filter(|e| e.earner_id == uuid_from_index(0))
        .collect();
    assert_eq!(alice_earnings.len(), 2, "Alice should have 2 earnings");

    let alice_total: f64 = alice_earnings.iter().map(|e| e.dollar_amount).sum();
    // 7.0 (gen 2 from Eve) + 5.0 (gen 1 from Henry) = 12.0
    assert!(
        (alice_total - 12.0).abs() < 1e-10,
        "Alice total: expected 12.0, got {}",
        alice_total
    );
}

// ===========================================================================
// Property-based tests
// ===========================================================================

/// Build a random chain tree with optional director nodes.
fn build_random_chain(
    size: usize,
    director_positions: &[usize],
) -> (UnilevelTree, HashMap<uuid::Uuid, DistributorSnapshot>) {
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    for i in 1..size {
        tree.add_node(
            uuid_from_index(i),
            uuid_from_index(i - 1),
            uuid_from_index(i - 1),
            i as i64,
        )
        .unwrap();
    }

    let director_set: HashSet<usize> = director_positions.iter().copied().collect();
    let mut snapshots = HashMap::new();
    for i in 0..size {
        let rank = if director_set.contains(&i) {
            "director"
        } else {
            "associate"
        };
        snapshots.insert(
            uuid_from_index(i),
            DistributorSnapshot {
                rank: rank.to_string(),
                personal_volume: 150.0,
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );
    }

    (tree, snapshots)
}

/// Strategy: generate a tree size and a set of director positions within it.
fn generation_inputs() -> impl Strategy<Value = (usize, Vec<usize>, f64, u8)> {
    (3..30usize).prop_flat_map(|size| {
        let directors = proptest::collection::vec(0..size, 0..size / 2 + 1);
        let cv = 0.0..10000.0f64;
        let max_gen = 1..6u8;
        (Just(size), directors, cv, max_gen)
    })
}

proptest! {
    /// No earning has a generation number exceeding max_generations.
    #[test]
    fn generation_bounded(
        (size, directors, cv, max_gen) in generation_inputs()
    ) {
        let (plan, structure) = build_generation_plan(max_gen);
        let (tree, snapshots) = build_random_chain(size, &directors);
        let source_idx = size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        for earning in &result {
            prop_assert!(
                earning.level <= max_gen,
                "Earning at gen {} exceeds max {}",
                earning.level, max_gen
            );
        }
    }

    /// All earnings have non-negative dollar amounts.
    #[test]
    fn all_earnings_non_negative(
        (size, directors, cv, max_gen) in generation_inputs()
    ) {
        let (plan, structure) = build_generation_plan(max_gen);
        let (tree, snapshots) = build_random_chain(size, &directors);
        let source_idx = size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        for earning in &result {
            prop_assert!(
                earning.dollar_amount >= 0.0,
                "Negative earning: {}",
                earning.dollar_amount
            );
        }
    }

    /// No duplicate (earner, source) pairs in the result.
    #[test]
    fn no_duplicate_earner_source_pairs(
        (size, directors, cv, max_gen) in generation_inputs()
    ) {
        let (plan, structure) = build_generation_plan(max_gen);
        let (tree, snapshots) = build_random_chain(size, &directors);
        let source_idx = size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        let mut seen = HashSet::new();
        for earning in &result {
            let key = (earning.earner_id, earning.source_id);
            prop_assert!(
                seen.insert(key),
                "Duplicate (earner, source): ({}, {})",
                earning.earner_id, earning.source_id
            );
        }
    }

    /// Every earner_id in results has a node in the tree.
    #[test]
    fn every_earner_exists_in_tree(
        (size, directors, cv, max_gen) in generation_inputs()
    ) {
        let (plan, structure) = build_generation_plan(max_gen);
        let (tree, snapshots) = build_random_chain(size, &directors);
        let source_idx = size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        let tree_ids: HashSet<_> = (0..size).map(uuid_from_index).collect();
        for earning in &result {
            prop_assert!(
                tree_ids.contains(&earning.earner_id),
                "Earner {} not in tree",
                earning.earner_id
            );
        }
    }

    /// If at least one director exists above the source, at least one
    /// earning is produced (may be zero-rate, which is filtered, so we
    /// check for non-negative count).
    #[test]
    fn at_least_one_earner_when_boundary_exists(
        size in 3..20usize,
        cv in 1.0..1000.0f64,
    ) {
        // Force a director at position 0 (root) to guarantee a boundary.
        let directors = vec![0];
        let (plan, structure) = build_generation_plan(4);
        let (tree, snapshots) = build_random_chain(size, &directors);
        let source_idx = size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        prop_assert!(
            !result.is_empty(),
            "Expected at least one earning with director at root, got 0"
        );
    }

    /// Total payout is bounded by sum of generation rates * total CV * multiplier.
    #[test]
    fn total_payout_bounded(
        (size, directors, cv, max_gen) in generation_inputs()
    ) {
        let (plan, structure) = build_generation_plan(max_gen);
        let (tree, snapshots) = build_random_chain(size, &directors);
        let source_idx = size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        let total_payout: f64 = result.iter().map(|e| e.dollar_amount).sum();

        // Upper bound: cv * multiplier * sum(all gen rates)
        let rate_sum: f64 = structure.generation_commission.rates.values().sum();
        let multiplier = structure
            .generation_commission
            .volume_to_dollar_multiplier
            .unwrap_or(plan.volume.volume_to_dollar_multiplier);
        let upper_bound = cv * multiplier * rate_sum;

        prop_assert!(
            total_payout <= upper_bound + 1e-6,
            "Total payout {} exceeds upper bound {}",
            total_payout, upper_bound
        );
    }
}
