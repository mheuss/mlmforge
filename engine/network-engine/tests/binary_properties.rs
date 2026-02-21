mod common;
use common::uuid_from_index;

use network_engine::tree::binary::BinaryTree;
use proptest::prelude::*;
use uuid::Uuid;

/// Builds a random binary tree. Each non-root node picks a random parent
/// and a random position (0 or 1). If the chosen position is occupied,
/// tries the other. If both are full, picks a different parent.
fn build_random_binary_tree(node_count: usize, choices: &[(usize, usize)]) -> BinaryTree {
    let mut tree = BinaryTree::new();
    if node_count == 0 {
        return tree;
    }

    tree.add_root(uuid_from_index(0), 0).unwrap();

    for i in 1..node_count {
        let (parent_hint, pos_hint) = if i - 1 < choices.len() {
            (choices[i - 1].0 % i, choices[i - 1].1 % 2)
        } else {
            (0, 0)
        };

        let mut placed = false;
        for offset in 0..i {
            let parent_idx = (parent_hint + offset) % i;
            let parent_id = uuid_from_index(parent_idx);
            for pos_offset in 0..2 {
                let position = (pos_hint + pos_offset) % 2;
                if tree
                    .add_node(
                        uuid_from_index(i),
                        parent_id,
                        position,
                        uuid_from_index(0),
                        i as i64,
                    )
                    .is_ok()
                {
                    placed = true;
                    break;
                }
            }
            if placed {
                break;
            }
        }
        assert!(placed, "Could not place node {}", i);
    }

    tree
}

proptest! {
    /// Property 1: Parent-child consistency.
    /// Every node with parent P appears in P's children list.
    /// Every child in a node's children list has that node as parent.
    #[test]
    fn parent_child_consistency(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let children = tree.get_children(uid).unwrap();

            for child in &children {
                let parent = tree.get_parent(child.user_id).unwrap();
                prop_assert_eq!(parent.unwrap().user_id, uid);
            }

            if let Some(parent) = tree.get_parent(uid).unwrap() {
                let siblings = tree.get_children(parent.user_id).unwrap();
                prop_assert!(siblings.iter().any(|s| s.user_id == uid));
            }
        }
    }

    /// Property 2: Depth consistency.
    /// Every node's depth equals parent's depth + 1. Root has depth 0.
    #[test]
    fn depth_consistency(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();

            if let Some(parent_uid) = pos.parent_user_id {
                let parent_pos = tree.get_position(parent_uid).unwrap();
                prop_assert_eq!(pos.depth, parent_pos.depth + 1);
            } else {
                prop_assert_eq!(pos.depth, 0);
            }
        }
    }

    /// Property 3: Upline completeness.
    /// get_upline(node, 0) returns exactly `depth` nodes and ends at root.
    #[test]
    fn upline_completeness(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();
            let upline = tree.get_upline(uid, 0).unwrap();

            prop_assert_eq!(upline.len(), pos.depth as usize);

            if !upline.is_empty() {
                let last = upline.last().unwrap();
                let last_parent = tree.get_parent(last.user_id).unwrap();
                prop_assert!(last_parent.is_none());
            }
        }
    }

    /// Property 4: Downline containment.
    /// Every node in get_downline(user, 0) satisfies is_descendant_of.
    #[test]
    fn downline_containment(
        node_count in 1usize..30,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..30),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let downline = tree.get_downline(uid, 0).unwrap();

            for desc in &downline {
                prop_assert!(tree.is_descendant_of(desc.user_id, uid).unwrap());
            }
        }
    }

    /// Property 5: Count matches collection.
    /// count_downline equals get_downline.len() for any depth.
    #[test]
    fn count_matches_collection(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
        depth in 0u32..10,
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let count = tree.count_downline(uid, depth).unwrap();
            let downline = tree.get_downline(uid, depth).unwrap();
            prop_assert_eq!(count, downline.len());
        }
    }

    /// Property 6: Branch partitioning.
    /// The union of all branches equals the full downline.
    /// No duplicates. No missing nodes.
    #[test]
    fn branch_partitioning(
        node_count in 1usize..30,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..30),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let full_downline = tree.get_downline(uid, 0).unwrap();

            let mut branch_union: Vec<Uuid> = Vec::new();
            for pos in 0..2 {
                if let Ok(branch) = tree.get_branch(uid, pos) {
                    for node in &branch {
                        branch_union.push(node.user_id);
                    }
                }
            }

            branch_union.sort();
            let mut downline_ids: Vec<Uuid> = full_downline.iter().map(|n| n.user_id).collect();
            downline_ids.sort();

            prop_assert_eq!(branch_union, downline_ids);
        }
    }

    /// Property 7: Max two children.
    /// No node in a binary tree has more than two children.
    #[test]
    fn max_two_children(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let children = tree.get_children(uid).unwrap();
            prop_assert!(children.len() <= 2, "Node {} has {} children", uid, children.len());
        }
    }

    /// Property 8: Position integrity.
    /// Every node's position is 0 or 1.
    #[test]
    fn position_integrity(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();
            prop_assert!(pos.position <= 1, "Node {} has position {}", uid, pos.position);
        }
    }

    /// Property 9: Sponsor-sponsored consistency.
    /// Every non-root node appears in its sponsor's sponsored list.
    #[test]
    fn sponsor_consistency(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 1..node_count {
            let uid = uuid_from_index(i);
            if let Some(sponsor) = tree.get_sponsor(uid).unwrap() {
                let sponsored = tree.get_sponsored(sponsor.user_id).unwrap();
                prop_assert!(sponsored.iter().any(|s| s.user_id == uid));
            }
        }
    }

    /// Property 10: Sponsor upline completeness.
    /// Walking sponsor links from any node ends at a node with no sponsor.
    #[test]
    fn sponsor_upline_completeness(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let upline = tree.get_sponsor_upline(uid, 0).unwrap();

            if !upline.is_empty() {
                let last = upline.last().unwrap();
                let last_sponsor = tree.get_sponsor(last.user_id).unwrap();
                prop_assert!(last_sponsor.is_none());
            }
        }
    }
}
