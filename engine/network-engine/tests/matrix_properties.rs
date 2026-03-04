mod common;

use common::uuid_from_index;
use network_engine::config::matrix::SpilloverDirection;
use network_engine::tree::matrix::{MatrixTree, PruningMode};
use proptest::prelude::*;

/// Builds a random matrix tree by adding `node_count` nodes.
/// Each node is sponsored by a random existing node (determined by sponsor_hints).
fn build_random_matrix_tree(width: u8, node_count: usize, sponsor_hints: Vec<usize>) -> MatrixTree {
    let mut tree = MatrixTree::new(width, SpilloverDirection::BreadthFirst).unwrap();
    if node_count == 0 {
        return tree;
    }
    tree.set_root(uuid_from_index(0), 0).unwrap();
    for i in 1..node_count {
        let sponsor_hint = sponsor_hints.get(i).copied().unwrap_or(0);
        let sponsor_idx = sponsor_hint % i;
        tree.add_node(uuid_from_index(i), uuid_from_index(sponsor_idx), i as i64)
            .unwrap();
    }
    tree
}

proptest! {
    #[test]
    fn every_node_has_width_slots(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let pos = tree.get_position(uuid_from_index(i)).unwrap();
            prop_assert_eq!(
                pos.downline_counts.len(),
                width as usize,
                "node {} should have {} slot entries",
                i,
                width
            );
        }
    }

    #[test]
    fn parent_child_consistency(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let children = tree.get_children(uid).unwrap();
            for child in &children {
                let parent = tree.get_parent(child.user_id).unwrap();
                prop_assert_eq!(parent.unwrap().user_id, uid);
            }
        }
    }

    #[test]
    fn depth_consistency(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();
            if let Some(parent_id) = pos.parent_user_id {
                let parent_pos = tree.get_position(parent_id).unwrap();
                prop_assert_eq!(pos.depth, parent_pos.depth + 1);
            } else {
                prop_assert_eq!(pos.depth, 0);
            }
        }
    }

    #[test]
    fn bfs_spillover_fills_levels_before_going_deeper(
        node_count in 1usize..30,
        width in 2u8..5,
    ) {
        // All nodes sponsored by root — BFS should fill level by level.
        let mut tree = MatrixTree::new(width, SpilloverDirection::BreadthFirst).unwrap();
        if node_count == 0 {
            return Ok(());
        }
        tree.set_root(uuid_from_index(0), 0).unwrap();
        for i in 1..node_count {
            tree.add_node(uuid_from_index(i), uuid_from_index(0), i as i64).unwrap();
        }

        // Verify: no node at depth D has an open slot if any node at depth D+1 exists.
        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();
            let has_open_slot = pos.child_count < width as usize;

            if has_open_slot {
                for j in 0..node_count {
                    let other_pos = tree.get_position(uuid_from_index(j)).unwrap();
                    prop_assert!(
                        other_pos.depth <= pos.depth + 1
                            || tree.get_children(uid).unwrap().len() == width as usize,
                        "node at depth {} has open slot but node {} exists at depth {}",
                        pos.depth,
                        j,
                        other_pos.depth
                    );
                }
            }
        }
    }

    #[test]
    fn sponsor_independent_of_placement(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 1..node_count {
            let uid = uuid_from_index(i);
            let sponsor = tree.get_sponsor(uid).unwrap();
            let parent = tree.get_parent(uid).unwrap();
            prop_assert!(sponsor.is_some(), "non-root node must have sponsor");
            prop_assert!(parent.is_some(), "non-root node must have parent");
        }
    }

    #[test]
    fn max_children_never_exceeds_width(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let children = tree.get_children(uuid_from_index(i)).unwrap();
            prop_assert!(
                children.len() <= width as usize,
                "node {} has {} children but width is {}",
                i,
                children.len(),
                width
            );
        }
    }

    #[test]
    fn branch_partitioning(
        node_count in 1usize..50,
        width in 2u8..5,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let downline = tree.get_downline(uid, 0).unwrap();
            let mut branch_union = Vec::new();
            for pos in 0..width as usize {
                let branch = tree.get_branch(uid, pos).unwrap();
                branch_union.extend(branch.iter().map(|n| n.user_id));
            }
            prop_assert_eq!(
                downline.len(),
                branch_union.len(),
                "branch union should equal full downline for node {}",
                i
            );
        }
    }

    #[test]
    fn sponsor_consistency(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 1..node_count {
            let uid = uuid_from_index(i);
            let sponsor = tree.get_sponsor(uid).unwrap().unwrap();
            let sponsored = tree.get_sponsored(sponsor.user_id).unwrap();
            prop_assert!(
                sponsored.iter().any(|n| n.user_id == uid),
                "node {} should appear in sponsor's sponsored list",
                i
            );
        }
    }

    #[test]
    fn holding_tank_and_arena_are_disjoint(
        node_count in 5usize..30,
        width in 2u8..5,
        sponsor_hints in proptest::collection::vec(0usize..100, 30),
        remove_target in 1usize..5,
    ) {
        let mut tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        let target = remove_target % (node_count - 1) + 1; // skip root
        let uid = uuid_from_index(target);

        if tree.contains(uid) {
            let _ = tree.remove_node(uid, PruningMode::HoldingTank);
        }

        let tank = tree.get_holding_tank(None);
        for entry in &tank {
            prop_assert!(
                !tree.contains(entry.user_id),
                "holding tank entry {} should not be in arena",
                entry.user_id
            );
        }
    }
}
