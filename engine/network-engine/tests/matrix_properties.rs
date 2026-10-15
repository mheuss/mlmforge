mod common;

use std::collections::{HashMap, HashSet};

use common::uuid_from_index;
use network_engine::config::matrix::SpilloverDirection;
use network_engine::tree::error::TreeError;
use network_engine::tree::matrix::{MatrixTree, PruningMode};
use proptest::prelude::*;
use proptest::test_runner::TestCaseError;
use uuid::Uuid;

/// Builds a random matrix tree by adding `node_count` nodes.
/// Each node is sponsored by a random existing node (determined by sponsor_hints).
fn build_random_matrix_tree(width: u8, node_count: usize, sponsor_hints: Vec<usize>) -> MatrixTree {
    let mut tree = MatrixTree::new(width, SpilloverDirection::BreadthFirst).unwrap();
    if node_count == 0 {
        return tree;
    }
    tree.add_root(uuid_from_index(0), 0).unwrap();
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
        tree.add_root(uuid_from_index(0), 0).unwrap();
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

    /// Property: Upline completeness.
    /// get_upline(node, 0) returns exactly `depth` nodes and ends at root.
    #[test]
    fn upline_completeness(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();
            let upline = tree.get_upline(uid, 0).unwrap();

            prop_assert_eq!(
                upline.len(),
                pos.depth as usize,
                "Upline length should equal depth for node {}",
                uid
            );

            if !upline.is_empty() {
                let last = upline.last().unwrap();
                let last_parent = tree.get_parent(last.user_id).unwrap();
                prop_assert!(
                    last_parent.is_none(),
                    "Last node in upline should be root"
                );
            }
        }
    }

    /// Property: Downline containment.
    /// Every node in get_downline(user, 0) satisfies is_descendant_of.
    #[test]
    fn downline_containment(
        node_count in 1usize..30,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let downline = tree.get_downline(uid, 0).unwrap();

            for desc in &downline {
                prop_assert!(
                    tree.is_descendant_of(desc.user_id, uid).unwrap(),
                    "{} in downline of {} but is_descendant_of returned false",
                    desc.user_id,
                    uid
                );
            }
        }
    }

    /// Property: Count matches collection.
    /// count_downline equals get_downline.len() for any depth.
    #[test]
    fn count_matches_collection(
        node_count in 1usize..50,
        width in 2u8..6,
        sponsor_hints in proptest::collection::vec(0usize..100, 50),
        depth in 0u32..10,
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let count = tree.count_downline(uid, depth).unwrap();
            let downline = tree.get_downline(uid, depth).unwrap();
            prop_assert_eq!(
                count,
                downline.len(),
                "count_downline != get_downline.len() for node {} at depth {}",
                uid,
                depth
            );
        }
    }

    /// Property: PromoteEarliest removal preserves tree invariants.
    /// After removing a non-root node with PromoteEarliest, the remaining
    /// tree still satisfies parent-child consistency, depth consistency,
    /// and width constraints.
    #[test]
    fn promote_earliest_preserves_invariants(
        node_count in 5usize..30,
        width in 2u8..5,
        sponsor_hints in proptest::collection::vec(0usize..100, 30),
        remove_target in 1usize..5,
    ) {
        let mut tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        let target = remove_target % (node_count - 1) + 1; // skip root
        let uid = uuid_from_index(target);

        if !tree.contains(uid) {
            return Ok(());
        }

        let result = tree.remove_node(uid, PruningMode::PromoteEarliest);
        // Remove can fail if it's the root (we skip root) or other edge cases.
        // If it succeeds, verify invariants.
        if let Ok(removal) = result {
            // Removed node should be gone.
            prop_assert!(!tree.contains(removal.removed));

            // Collect all remaining nodes by walking downline from root.
            let root_id = uuid_from_index(0);
            prop_assert!(tree.contains(root_id), "root should still exist");

            let downline = tree.get_downline(root_id, 0).unwrap();
            let all_nodes: Vec<_> = std::iter::once(root_id)
                .chain(downline.iter().map(|n| n.user_id))
                .collect();

            for &nid in &all_nodes {
                // Parent-child consistency: every child's parent should be this node.
                let children = tree.get_children(nid).unwrap();
                for child in &children {
                    let parent = tree.get_parent(child.user_id).unwrap();
                    prop_assert_eq!(
                        parent.unwrap().user_id,
                        nid,
                        "child {}'s parent should be {}",
                        child.user_id,
                        nid
                    );
                }

                // Width constraint: no node exceeds the matrix width.
                prop_assert!(
                    children.len() <= width as usize,
                    "node {} has {} children but width is {}",
                    nid,
                    children.len(),
                    width
                );

                // Depth consistency: child depth = parent depth + 1.
                let pos = tree.get_position(nid).unwrap();
                if let Some(parent_id) = pos.parent_user_id {
                    let parent_pos = tree.get_position(parent_id).unwrap();
                    prop_assert_eq!(
                        pos.depth,
                        parent_pos.depth + 1,
                        "depth mismatch for node {} after removal",
                        nid
                    );
                } else {
                    prop_assert_eq!(pos.depth, 0, "root depth should be 0");
                }
            }
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

/// One placement choice per node after the root.
#[derive(Debug, Clone)]
struct PlacementPick {
    slot: usize,
    sponsor: usize,
    enrolled_at: i64,
}

fn placement_picks() -> impl Strategy<Value = Vec<PlacementPick>> {
    proptest::collection::vec(
        (0usize..10_000, 0usize..10_000, 0i64..50).prop_map(|(slot, sponsor, enrolled_at)| {
            PlacementPick {
                slot,
                sponsor,
                enrolled_at,
            }
        }),
        0..60,
    )
}

/// Every free (parent, slot) among the first `count` users.
fn open_slots(tree: &MatrixTree, count: usize) -> Vec<(Uuid, u8)> {
    let mut open = Vec::new();
    for i in 0..count {
        let parent = uuid_from_index(i);
        let taken: HashSet<usize> = tree
            .get_children(parent)
            .unwrap()
            .iter()
            .map(|c| tree.get_position(c.user_id).unwrap().position)
            .collect();
        for slot in 0..tree.width() {
            if !taken.contains(&(slot as usize)) {
                open.push((parent, slot));
            }
        }
    }
    open
}

/// Builds a matrix tree by explicit placement. Each node takes the open slot
/// its pick names, anywhere in the tree.
fn build_placed_matrix_tree(width: u8, picks: &[PlacementPick]) -> MatrixTree {
    let mut tree = MatrixTree::new(width, SpilloverDirection::BreadthFirst).unwrap();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    for (i, pick) in picks.iter().enumerate() {
        let user = i + 1;
        let open = open_slots(&tree, user);
        let (parent, position) = open[pick.slot % open.len()];
        tree.add_node_at(
            uuid_from_index(user),
            uuid_from_index(pick.sponsor % user),
            parent,
            position,
            pick.enrolled_at,
        )
        .unwrap();
    }
    tree
}

/// Each live node's parent, slot and depth, keyed by user.
fn placements(tree: &MatrixTree) -> HashMap<Uuid, (Option<Uuid>, usize, u32)> {
    let root = uuid_from_index(0);
    std::iter::once(root)
        .chain(
            tree.get_downline(root, 0)
                .unwrap()
                .iter()
                .map(|n| n.user_id),
        )
        .map(|u| {
            let p = tree.get_position(u).unwrap();
            (u, (p.parent_user_id, p.position, p.depth))
        })
        .collect()
}

/// `user` and everyone below it.
fn subtree(tree: &MatrixTree, user: Uuid) -> HashSet<Uuid> {
    std::iter::once(user)
        .chain(
            tree.get_downline(user, 0)
                .unwrap()
                .iter()
                .map(|n| n.user_id),
        )
        .collect()
}

fn mode_of(holding: bool) -> PruningMode {
    if holding {
        PruningMode::HoldingTank
    } else {
        PruningMode::PromoteEarliest
    }
}

/// Removes `user` in `mode` and checks what the removal reports against what it
/// did to the tree.
fn assert_removal_properties(
    mut tree: MatrixTree,
    user: Uuid,
    mode: PruningMode,
) -> Result<(), TestCaseError> {
    let before = placements(&tree);
    let leaving = if tree.contains(user) {
        subtree(&tree, user)
    } else {
        HashSet::new()
    };
    let checked = tree.check_remove_node(user, mode);
    let removed = tree.remove_node(user, mode);

    // The check and the op agree.
    prop_assert_eq!(checked.as_ref().err(), removed.as_ref().err());
    // The promotion never runs out of slots.
    prop_assert!(
        !matches!(removed, Err(TreeError::SubtreeFull(_))),
        "SUBTREE_FULL fired removing {}",
        user
    );
    let Ok(result) = removed else {
        return Ok(());
    };

    // Exactly the removed users left the tree.
    let after = placements(&tree);
    let gone: HashSet<Uuid> = match mode {
        PruningMode::PromoteEarliest => HashSet::from([user]),
        PruningMode::HoldingTank => leaving.clone(),
    };
    let expected: HashSet<Uuid> = before
        .keys()
        .filter(|u| !gone.contains(*u))
        .copied()
        .collect();
    let actual: HashSet<Uuid> = after.keys().copied().collect();
    prop_assert_eq!(actual, expected, "the survivor set changed");

    // relocated names every survivor whose placement changed, and no other.
    let mut diffed: Vec<(Uuid, Uuid, usize, u32)> = after
        .iter()
        .filter(|(u, now)| before.get(*u) != Some(*now))
        .map(|(u, (parent, position, depth))| {
            (
                *u,
                parent.expect("only the root has no parent"),
                *position,
                *depth,
            )
        })
        .collect();
    diffed.sort();
    let mut reported: Vec<(Uuid, Uuid, usize, u32)> = result
        .relocated
        .iter()
        .map(|r| (r.user_id, r.parent_id, r.position as usize, r.depth))
        .collect();
    reported.sort();
    prop_assert_eq!(reported, diffed);

    // Each move lands on a slot the tombstones freed, and a depth-only move
    // keeps its own slot.
    let removed_users: HashSet<Uuid> = match mode {
        PruningMode::PromoteEarliest => HashSet::from([user]),
        PruningMode::HoldingTank => result.moved_to_tank.iter().copied().collect(),
    };
    let occupied: HashMap<(Uuid, usize), Uuid> = before
        .iter()
        .filter(|(u, _)| !removed_users.contains(*u))
        .filter_map(|(u, (parent, position, _))| parent.map(|p| ((p, *position), *u)))
        .collect();
    let slot_before = |u: &Uuid| {
        let (parent, position, _) = before[u];
        (parent.expect("only the root has no parent"), position)
    };
    let vacated: HashSet<(Uuid, usize)> = result
        .relocated
        .iter()
        .map(|r| (slot_before(&r.user_id), (r.parent_id, r.position as usize)))
        .filter(|(from, to)| from != to)
        .map(|(from, _)| from)
        .collect();
    let kept: HashSet<(Uuid, usize)> = result
        .relocated
        .iter()
        .filter(|r| slot_before(&r.user_id) == (r.parent_id, r.position as usize))
        .map(|r| (r.parent_id, r.position as usize))
        .collect();
    for r in &result.relocated {
        let target = (r.parent_id, r.position as usize);
        if slot_before(&r.user_id) == target {
            prop_assert_eq!(occupied.get(&target), Some(&r.user_id));
            continue;
        }
        prop_assert!(
            !kept.contains(&target),
            "relocation of {} targets {:?}, which a depth-only relocation holds",
            r.user_id,
            target
        );
        prop_assert!(
            !occupied.contains_key(&target),
            "relocation of {} targets {:?}, which {:?} holds after the tombstone step",
            r.user_id,
            target,
            occupied.get(&target)
        );
        prop_assert!(
            !vacated.contains(&target),
            "relocation of {} targets {:?}, which another relocation vacates",
            r.user_id,
            target
        );
    }

    // The tank holds exactly the removed subtree, once each.
    if mode == PruningMode::HoldingTank {
        let tank: HashSet<Uuid> = result.moved_to_tank.iter().copied().collect();
        prop_assert_eq!(
            tank.len(),
            result.moved_to_tank.len(),
            "moved_to_tank repeats a user"
        );
        prop_assert_eq!(tank, leaving);
    }
    Ok(())
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(2000))]

    #[test]
    fn a_removal_from_a_placed_tree_reports_what_it_moved(
        width in 2u8..6,
        picks in placement_picks(),
        target in 0usize..10_000,
        holding in any::<bool>(),
    ) {
        let tree = build_placed_matrix_tree(width, &picks);
        // The root, each placed node, or an id one past the last.
        let user = uuid_from_index(target % (picks.len() + 2));
        assert_removal_properties(tree, user, mode_of(holding))?;
    }

    #[test]
    fn a_removal_from_a_spillover_tree_reports_what_it_moved(
        width in 2u8..6,
        node_count in 1usize..=60,
        sponsor_hints in proptest::collection::vec(0usize..100, 60),
        target in 0usize..10_000,
        holding in any::<bool>(),
    ) {
        let tree = build_random_matrix_tree(width, node_count, sponsor_hints);
        // The root, each placed node, or an id one past the last.
        let user = uuid_from_index(target % (node_count + 1));
        assert_removal_properties(tree, user, mode_of(holding))?;
    }
}
