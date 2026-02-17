use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use uuid::Uuid;

/// Generates a deterministic UUID from an index.
fn uuid_from_index(i: usize) -> Uuid {
    let bytes = (i as u128).to_be_bytes();
    Uuid::from_bytes(bytes)
}

/// Builds a random unilevel tree with `node_count` nodes.
/// Each non-root node picks a random parent from existing nodes.
fn build_random_tree(node_count: usize, parent_choices: &[usize]) -> UnilevelTree {
    let mut tree = UnilevelTree::new();
    if node_count == 0 {
        return tree;
    }

    tree.add_root(uuid_from_index(0), 0).unwrap();

    for i in 1..node_count {
        // Pick a parent from nodes already in the tree (indices 0..i)
        let parent_idx = if i - 1 < parent_choices.len() {
            parent_choices[i - 1] % i
        } else {
            0
        };
        tree.add_node(uuid_from_index(i), uuid_from_index(parent_idx), i as i64)
            .unwrap();
    }

    tree
}

proptest! {
    /// Property 1: Parent-child consistency.
    /// Every node with parent P appears in P's children list.
    /// Every child in a node's children list has that node as parent.
    #[test]
    fn parent_child_consistency(
        node_count in 1usize..100,
        parent_choices in prop::collection::vec(0usize..1000, 0..100),
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let children = tree.get_children(uid).unwrap();

            for child in &children {
                let parent = tree.get_parent(child.user_id).unwrap();
                prop_assert_eq!(parent.unwrap().user_id, uid);
            }

            if let Some(parent) = tree.get_parent(uid).unwrap() {
                let siblings = tree.get_children(parent.user_id).unwrap();
                prop_assert!(
                    siblings.iter().any(|s| s.user_id == uid),
                    "Node {} not found in parent's children",
                    uid
                );
            }
        }
    }

    /// Property 2: Depth consistency.
    /// Every node's depth equals parent's depth + 1. Root has depth 0.
    #[test]
    fn depth_consistency(
        node_count in 1usize..100,
        parent_choices in prop::collection::vec(0usize..1000, 0..100),
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

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
        node_count in 1usize..100,
        parent_choices in prop::collection::vec(0usize..1000, 0..100),
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

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

    /// Property 4: Downline containment.
    /// Every node in get_downline(user, 0) satisfies is_descendant_of.
    #[test]
    fn downline_containment(
        node_count in 1usize..50,
        parent_choices in prop::collection::vec(0usize..1000, 0..50),
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

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

    /// Property 5: Count matches collection.
    /// count_downline equals get_downline.len() for any depth.
    #[test]
    fn count_matches_collection(
        node_count in 1usize..100,
        parent_choices in prop::collection::vec(0usize..1000, 0..100),
        depth in 0u32..10,
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

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

    /// Property 6: Branch partitioning.
    /// The union of all branches equals the full downline.
    /// No duplicates. No missing nodes.
    #[test]
    fn branch_partitioning(
        node_count in 1usize..50,
        parent_choices in prop::collection::vec(0usize..1000, 0..50),
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let children = tree.get_children(uid).unwrap();
            let full_downline = tree.get_downline(uid, 0).unwrap();

            let mut branch_union: Vec<Uuid> = Vec::new();
            for pos in 0..children.len() {
                let branch = tree.get_branch(uid, pos).unwrap();
                for node in &branch {
                    branch_union.push(node.user_id);
                }
            }

            branch_union.sort();
            let mut downline_ids: Vec<Uuid> = full_downline.iter().map(|n| n.user_id).collect();
            downline_ids.sort();

            prop_assert_eq!(
                branch_union,
                downline_ids,
                "Branch union != full downline for node {}",
                uid
            );
        }
    }
}
