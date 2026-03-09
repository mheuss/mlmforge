//! Generation counting utility for stairstep commission calculators.
//!
//! Walks upward through a unilevel tree from a starting node, counting
//! generation boundaries. A "generation" increments each time we encounter
//! a breakaway distributor who passes a boundary check (e.g., meets a
//! rank threshold). This module is shared by the stairstep calculator
//! and the standalone generation calculator.

use std::collections::HashSet;

use uuid::Uuid;

use crate::tree::unilevel::UnilevelTree;

/// A single generation entry produced by the upward walk.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GenerationEntry {
    /// The distributor who represents this generation boundary.
    pub earner_id: Uuid,
    /// The generation number (1-based).
    pub generation: u8,
}

/// Walk upward from `start_id` through the unilevel tree, counting
/// generation boundaries among breakaway distributors.
///
/// For each ancestor (excluding `start_id`):
/// - If not in `breakaway_set`, skip it entirely.
/// - If in `breakaway_set` and passes `boundary_check`: increment generation,
///   add to results.
/// - If in `breakaway_set` but fails `boundary_check`: a non-boundary
///   breakaway. When `empty_generation_consumes_number` is true, increment
///   the generation counter without adding to results. When false, skip.
///
/// Stops when `current_gen >= max_generations` or the upline is exhausted.
///
/// # Errors
///
/// Returns an empty Vec if `start_id` is not in the tree or has no upline.
pub fn count_generations_upward(
    tree: &UnilevelTree,
    start_id: Uuid,
    breakaway_set: &HashSet<Uuid>,
    boundary_check: &dyn Fn(Uuid) -> bool,
    max_generations: u8,
    empty_generation_consumes_number: bool,
) -> Vec<GenerationEntry> {
    let upline = match tree.get_upline(start_id, 0) {
        Ok(nodes) => nodes,
        Err(_) => return Vec::new(),
    };

    let mut results = Vec::new();
    let mut current_gen: u8 = 0;

    for node in &upline {
        if current_gen >= max_generations {
            break;
        }

        if !breakaway_set.contains(&node.user_id) {
            continue;
        }

        if boundary_check(node.user_id) {
            current_gen += 1;
            results.push(GenerationEntry {
                earner_id: node.user_id,
                generation: current_gen,
            });
        } else if empty_generation_consumes_number {
            current_gen += 1;
        }
    }

    results
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tree::unilevel::UnilevelTree;

    /// Deterministic UUID from index. Byte 15 is 0xFF to avoid
    /// collisions with the arena tombstone sentinel (Uuid::nil).
    fn uuid(i: usize) -> Uuid {
        let mut bytes = [0u8; 16];
        bytes[15] = 0xFF;
        // Encode the index into the first bytes for readability.
        let idx_bytes = (i as u32).to_le_bytes();
        bytes[0] = idx_bytes[0];
        bytes[1] = idx_bytes[1];
        bytes[2] = idx_bytes[2];
        bytes[3] = idx_bytes[3];
        Uuid::from_bytes(bytes)
    }

    /// Build a linear chain: 0 -> 1 -> 2 -> ... -> (len-1).
    /// Each node's parent and sponsor are the previous node.
    fn build_chain(len: usize) -> UnilevelTree {
        assert!(len >= 1, "chain must have at least one node");
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid(0), 0).unwrap();
        for i in 1..len {
            tree.add_node(uuid(i), uuid(i - 1), uuid(i - 1), i as i64)
                .unwrap();
        }
        tree
    }

    /// Chain: 0 -> 1 -> 2 -> 3 -> 4
    /// Breakaways: {0, 2}. All breakaways pass boundary_check.
    /// Start from 4. Expect gen 1 at node 2, gen 2 at node 0.
    #[test]
    fn threshold_rank_boundary_mode() {
        let tree = build_chain(5);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(2)].into_iter().collect();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(4), &breakaway_set, &boundary_check, 10, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 2
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3
    /// Breakaways: {0, 1, 2}. boundary_check returns true only for {0, 2}.
    /// Start from 3. Node 1 is breakaway but not a boundary.
    /// flag=false, so node 1 doesn't consume a generation number.
    /// Expect gen 1 at node 2, gen 2 at node 0.
    #[test]
    fn same_rank_boundary_mode() {
        let tree = build_chain(4);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(1), uuid(2)].into_iter().collect();
        let boundaries: HashSet<Uuid> = [uuid(0), uuid(2)].into_iter().collect();
        let boundary_check = move |id: Uuid| boundaries.contains(&id);

        let result =
            count_generations_upward(&tree, uuid(3), &breakaway_set, &boundary_check, 10, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 2
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3
    /// Breakaways: {0, 1, 2}. All pass boundary_check.
    /// flag=true. Start from 3.
    /// All three breakaways are boundaries, so we get gen 1, 2, 3.
    #[test]
    fn empty_generation_consumed() {
        let tree = build_chain(4);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(1), uuid(2)].into_iter().collect();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(3), &breakaway_set, &boundary_check, 10, true);

        assert_eq!(result.len(), 3);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(1),
                generation: 2
            }
        );
        assert_eq!(
            result[2],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 3
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3 -> 4
    /// Breakaways: {0, 1, 3}. boundary_check returns true for {0, 3}.
    /// flag=false. Start from 4.
    /// Node 1 is breakaway but not boundary. flag=false means it doesn't
    /// consume a generation number. Node 2 is not breakaway at all.
    /// Expect gen 1 at node 3, gen 2 at node 0.
    #[test]
    fn empty_generation_not_consumed() {
        let tree = build_chain(5);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(1), uuid(3)].into_iter().collect();
        let boundaries: HashSet<Uuid> = [uuid(0), uuid(3)].into_iter().collect();
        let boundary_check = move |id: Uuid| boundaries.contains(&id);

        let result =
            count_generations_upward(&tree, uuid(4), &breakaway_set, &boundary_check, 10, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(3),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 2
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3
    /// Empty breakaway_set. Start from 3.
    /// No breakaways means no generations.
    #[test]
    fn no_breakaways_zero_generations() {
        let tree = build_chain(4);
        let breakaway_set: HashSet<Uuid> = HashSet::new();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(3), &breakaway_set, &boundary_check, 10, false);

        assert!(result.is_empty());
    }

    /// Chain: 0 -> 1 -> 2 -> 3 -> 4
    /// Breakaways: {0, 1, 2, 3}. All pass boundary_check.
    /// max_generations=2. Start from 4.
    /// Should stop after 2 generations even though more breakaways exist.
    #[test]
    fn max_generations_caps_result() {
        let tree = build_chain(5);
        let breakaway_set: HashSet<Uuid> =
            [uuid(0), uuid(1), uuid(2), uuid(3)].into_iter().collect();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(4), &breakaway_set, &boundary_check, 2, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(3),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 2
            }
        );
    }
}
