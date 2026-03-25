use std::collections::VecDeque;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// A single board in the board plan.
///
/// Positions are stored as a flat BFS-ordered array. Index 0 is the
/// top (root) position. Children of position `i` in a board with
/// width `w` are at indices `w*i + 1` through `w*i + w`.
///
/// Each slot is `Option<Uuid>`: `Some` means the position is filled
/// by a member, `None` means the position is open.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Board {
    /// Unique identifier for this board.
    pub id: Uuid,

    /// Flat BFS-ordered position array. Index 0 is the top position.
    pub positions: Vec<Option<Uuid>>,

    /// Unix timestamp (seconds) when this board was created.
    pub created_at: i64,

    /// Unix timestamp (seconds) of the most recent activity on this board.
    pub last_activity_at: i64,

    /// The board this one was split from, if any.
    pub parent_board_id: Option<Uuid>,
}

impl Board {
    /// Creates a new empty board with the given number of positions.
    pub fn new(total_positions: usize, timestamp: i64, parent_board_id: Option<Uuid>) -> Self {
        Self {
            id: Uuid::new_v4(),
            positions: vec![None; total_positions],
            created_at: timestamp,
            last_activity_at: timestamp,
            parent_board_id,
        }
    }

    /// Returns true when every position in the board is occupied.
    pub fn is_full(&self) -> bool {
        self.positions.iter().all(|p| p.is_some())
    }

    /// Returns the number of occupied positions.
    pub fn filled_count(&self) -> usize {
        self.positions.iter().filter(|p| p.is_some()).count()
    }

    /// Returns the index of the first open (None) position, or None
    /// if the board is full.
    pub fn first_open_position(&self) -> Option<usize> {
        self.positions.iter().position(|p| p.is_none())
    }

    /// Returns a Vec of all member UUIDs currently on this board,
    /// in BFS order (skipping empty slots).
    pub fn members(&self) -> Vec<Uuid> {
        self.positions.iter().filter_map(|p| *p).collect()
    }
}

/// Computes the total number of positions in a complete tree with
/// the given width and height.
///
/// Formula: sum of w^k for k = 0..=h. Board plan dimensions are
/// validated at engine creation (width 2-5, height 1-4), so the
/// maximum result is 781. Overflow is impossible for valid inputs.
pub fn total_positions(width: u8, height: u8) -> usize {
    let w = width as usize;
    let mut total = 0usize;
    let mut level_size = 1usize;
    for _ in 0..=height {
        total += level_size;
        level_size *= w;
    }
    total
}

/// Returns the parent index for a given position in a tree with the
/// given width.
///
/// Returns `None` for the root (index 0).
pub fn parent_index(index: usize, width: u8) -> Option<usize> {
    if index == 0 {
        return None;
    }
    Some((index - 1) / width as usize)
}

/// Returns the child indices for a given position in a tree with the
/// given width.
///
/// No upper-bound check is performed. Callers must verify that the
/// returned indices fall within the board's position array.
pub fn child_indices(index: usize, width: u8) -> Vec<usize> {
    let w = width as usize;
    let first = w * index + 1;
    (first..first + w).collect()
}

/// Collects all indices in the subtree rooted at `root_index`,
/// including `root_index` itself.
///
/// Uses iterative BFS with `VecDeque`. Indices beyond `max_index`
/// are excluded.
pub fn subtree_indices(root_index: usize, width: u8, max_index: usize) -> Vec<usize> {
    let mut result = Vec::new();
    let mut queue = VecDeque::new();
    queue.push_back(root_index);

    while let Some(idx) = queue.pop_front() {
        if idx > max_index {
            continue;
        }
        result.push(idx);
        for child in child_indices(idx, width) {
            if child <= max_index {
                queue.push_back(child);
            }
        }
    }

    result
}

#[cfg(test)]
mod tests {
    use super::*;

    // --- total_positions tests ---

    #[test]
    fn total_positions_width2_height2() {
        // 1 + 2 + 4 = 7
        assert_eq!(total_positions(2, 2), 7);
    }

    #[test]
    fn total_positions_width3_height2() {
        // 1 + 3 + 9 = 13
        assert_eq!(total_positions(3, 2), 13);
    }

    #[test]
    fn total_positions_width2_height3() {
        // 1 + 2 + 4 + 8 = 15
        assert_eq!(total_positions(2, 3), 15);
    }

    #[test]
    fn total_positions_width3_height3() {
        // 1 + 3 + 9 + 27 = 40
        assert_eq!(total_positions(3, 3), 40);
    }

    #[test]
    fn total_positions_width1_height0() {
        // Just the root.
        assert_eq!(total_positions(1, 0), 1);
    }

    #[test]
    fn total_positions_width2_height0() {
        // Just the root.
        assert_eq!(total_positions(2, 0), 1);
    }

    // --- parent_index tests ---

    #[test]
    fn parent_index_root_returns_none() {
        assert_eq!(parent_index(0, 2), None);
        assert_eq!(parent_index(0, 3), None);
    }

    #[test]
    fn parent_index_width2_children_of_root() {
        // Children of root (0) are at indices 1 and 2.
        assert_eq!(parent_index(1, 2), Some(0));
        assert_eq!(parent_index(2, 2), Some(0));
    }

    #[test]
    fn parent_index_width2_deeper() {
        // Children of node 1 are at 3, 4.
        assert_eq!(parent_index(3, 2), Some(1));
        assert_eq!(parent_index(4, 2), Some(1));
        // Children of node 2 are at 5, 6.
        assert_eq!(parent_index(5, 2), Some(2));
        assert_eq!(parent_index(6, 2), Some(2));
    }

    #[test]
    fn parent_index_width3_children_of_root() {
        // Children of root (0) are at 1, 2, 3.
        assert_eq!(parent_index(1, 3), Some(0));
        assert_eq!(parent_index(2, 3), Some(0));
        assert_eq!(parent_index(3, 3), Some(0));
    }

    #[test]
    fn parent_index_width3_deeper() {
        // Children of node 1 are at 4, 5, 6.
        assert_eq!(parent_index(4, 3), Some(1));
        assert_eq!(parent_index(5, 3), Some(1));
        assert_eq!(parent_index(6, 3), Some(1));
        // Children of node 2 are at 7, 8, 9.
        assert_eq!(parent_index(7, 3), Some(2));
        assert_eq!(parent_index(8, 3), Some(2));
        assert_eq!(parent_index(9, 3), Some(2));
    }

    // --- child_indices tests ---

    #[test]
    fn child_indices_width2_root() {
        assert_eq!(child_indices(0, 2), vec![1, 2]);
    }

    #[test]
    fn child_indices_width2_node1() {
        assert_eq!(child_indices(1, 2), vec![3, 4]);
    }

    #[test]
    fn child_indices_width2_node2() {
        assert_eq!(child_indices(2, 2), vec![5, 6]);
    }

    #[test]
    fn child_indices_width3_root() {
        assert_eq!(child_indices(0, 3), vec![1, 2, 3]);
    }

    #[test]
    fn child_indices_width3_node1() {
        assert_eq!(child_indices(1, 3), vec![4, 5, 6]);
    }

    #[test]
    fn child_indices_width3_node3() {
        assert_eq!(child_indices(3, 3), vec![10, 11, 12]);
    }

    // --- subtree_indices tests ---

    #[test]
    fn subtree_indices_root_is_all_width2_height2() {
        // 7 positions total (indices 0..6).
        let result = subtree_indices(0, 2, 6);
        assert_eq!(result.len(), 7);
        // BFS order.
        assert_eq!(result, vec![0, 1, 2, 3, 4, 5, 6]);
    }

    #[test]
    fn subtree_indices_left_child_width2() {
        // Subtree rooted at index 1 in a width-2, height-2 board (max_index 6).
        // Node 1 has children 3, 4.
        let result = subtree_indices(1, 2, 6);
        assert_eq!(result, vec![1, 3, 4]);
    }

    #[test]
    fn subtree_indices_right_child_width2() {
        // Subtree rooted at index 2 in a width-2, height-2 board (max_index 6).
        // Node 2 has children 5, 6.
        let result = subtree_indices(2, 2, 6);
        assert_eq!(result, vec![2, 5, 6]);
    }

    #[test]
    fn subtree_indices_leaf_returns_single() {
        // A leaf node has no children within bounds.
        let result = subtree_indices(6, 2, 6);
        assert_eq!(result, vec![6]);
    }

    #[test]
    fn subtree_indices_width3_node1() {
        // Width 3, max_index 12 (height 2, 13 positions).
        // Node 1 has children 4, 5, 6.
        let result = subtree_indices(1, 3, 12);
        assert_eq!(result, vec![1, 4, 5, 6]);
    }

    #[test]
    fn subtree_indices_respects_max_index() {
        // Only include indices up to max_index.
        // Width 2, root subtree, but max_index is 4 (cutting off 5, 6).
        let result = subtree_indices(0, 2, 4);
        assert_eq!(result, vec![0, 1, 2, 3, 4]);
    }

    // --- Board struct tests ---

    #[test]
    fn new_board_is_empty() {
        let board = Board::new(7, 1000, None);
        assert_eq!(board.positions.len(), 7);
        assert!(board.positions.iter().all(|p| p.is_none()));
        assert!(!board.is_full());
        assert_eq!(board.filled_count(), 0);
        assert_eq!(board.first_open_position(), Some(0));
        assert!(board.members().is_empty());
        assert_eq!(board.created_at, 1000);
        assert_eq!(board.last_activity_at, 1000);
        assert!(board.parent_board_id.is_none());
    }

    #[test]
    fn board_fill_detection() {
        let mut board = Board::new(3, 1000, None);

        let a = Uuid::new_v4();
        let b = Uuid::new_v4();
        let c = Uuid::new_v4();

        board.positions[0] = Some(a);
        assert_eq!(board.filled_count(), 1);
        assert!(!board.is_full());
        assert_eq!(board.first_open_position(), Some(1));

        board.positions[1] = Some(b);
        assert_eq!(board.filled_count(), 2);
        assert!(!board.is_full());
        assert_eq!(board.first_open_position(), Some(2));

        board.positions[2] = Some(c);
        assert_eq!(board.filled_count(), 3);
        assert!(board.is_full());
        assert_eq!(board.first_open_position(), None);
    }

    #[test]
    fn board_members_returns_filled_in_order() {
        let mut board = Board::new(5, 1000, None);

        let a = Uuid::new_v4();
        let b = Uuid::new_v4();

        board.positions[0] = Some(a);
        board.positions[2] = Some(b);
        // Positions 1, 3, 4 are empty.

        let members = board.members();
        assert_eq!(members, vec![a, b]);
    }

    #[test]
    fn board_parent_board_id_set() {
        let parent_id = Uuid::new_v4();
        let board = Board::new(7, 2000, Some(parent_id));
        assert_eq!(board.parent_board_id, Some(parent_id));
    }

    // --- parent/child round-trip tests ---

    #[test]
    fn parent_child_round_trip_width2() {
        // For every child of root, parent_index should return root.
        for child in child_indices(0, 2) {
            assert_eq!(parent_index(child, 2), Some(0));
        }
        // For every child of node 1.
        for child in child_indices(1, 2) {
            assert_eq!(parent_index(child, 2), Some(1));
        }
    }

    #[test]
    fn parent_child_round_trip_width3() {
        for child in child_indices(0, 3) {
            assert_eq!(parent_index(child, 3), Some(0));
        }
        for child in child_indices(2, 3) {
            assert_eq!(parent_index(child, 3), Some(2));
        }
    }
}
