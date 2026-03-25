//! Board plan engine — manages all boards for one board plan structure.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::config::board_plan::BoardPlanConfig;

use super::board::{self, Board};
use super::error::BoardPlanError;
use super::types::BoardSummary;

/// Manages all boards for a single board plan structure.
///
/// Tracks board membership, sponsor relationships, and displaced
/// members awaiting reassignment. All mutations return result types
/// that describe what happened.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoardPlanEngine {
    /// Tree branching factor (2-5).
    width: u8,

    /// Tree depth (1-4).
    height: u8,

    /// Cached total positions per board.
    total_positions: usize,

    /// All boards keyed by board id.
    boards: HashMap<Uuid, Board>,

    /// Maps each member to the board they are currently on.
    member_boards: HashMap<Uuid, Uuid>,

    /// Maps each member to their sponsor.
    sponsor_map: HashMap<Uuid, Uuid>,

    /// Members displaced by dissolution who need reassignment.
    displaced_members: Vec<Uuid>,

    /// Board plan cycling configuration.
    config: BoardPlanConfig,
}

impl BoardPlanEngine {
    /// Creates a new engine with the given dimensions and config.
    ///
    /// Validates that width is 2-5 and height is 1-4. Creates one
    /// initial empty board.
    pub fn new(
        width: u8,
        height: u8,
        config: BoardPlanConfig,
        timestamp: i64,
    ) -> Result<Self, BoardPlanError> {
        if !(2..=5).contains(&width) {
            return Err(BoardPlanError::InvalidDimensions {
                width,
                height,
                reason: format!("width must be 2-5 (got {width})"),
            });
        }
        if !(1..=4).contains(&height) {
            return Err(BoardPlanError::InvalidDimensions {
                width,
                height,
                reason: format!("height must be 1-4 (got {height})"),
            });
        }

        let total_positions = board::total_positions(width, height);
        let initial_board = Board::new(total_positions, timestamp, None);

        let mut boards = HashMap::new();
        boards.insert(initial_board.id, initial_board);

        Ok(Self {
            width,
            height,
            total_positions,
            boards,
            member_boards: HashMap::new(),
            sponsor_map: HashMap::new(),
            displaced_members: Vec::new(),
            config,
        })
    }

    /// Returns the (width, height) dimensions.
    pub fn dimensions(&self) -> (u8, u8) {
        (self.width, self.height)
    }

    /// Returns the number of boards currently managed.
    pub fn board_count(&self) -> usize {
        self.boards.len()
    }

    /// Returns a reference to the board with the given id, if it exists.
    pub fn get_board(&self, board_id: Uuid) -> Option<&Board> {
        self.boards.get(&board_id)
    }

    /// Returns the board id for the given member, if they are on a board.
    pub fn get_member_board(&self, user_id: Uuid) -> Option<Uuid> {
        self.member_boards.get(&user_id).copied()
    }

    /// Returns summaries of all boards, sorted by created_at ascending.
    pub fn list_boards(&self) -> Vec<BoardSummary> {
        let mut summaries: Vec<BoardSummary> = self
            .boards
            .values()
            .map(|b| BoardSummary {
                id: b.id,
                filled_count: b.filled_count(),
                total_positions: self.total_positions,
                created_at: b.created_at,
                last_activity_at: b.last_activity_at,
                parent_board_id: b.parent_board_id,
            })
            .collect();
        summaries.sort_by_key(|s| s.created_at);
        summaries
    }

    /// Returns the list of displaced members awaiting reassignment.
    pub fn displaced_members(&self) -> &[Uuid] {
        &self.displaced_members
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::board_plan::ReEntryPosition;

    /// Creates a standard BoardPlanConfig for testing.
    fn test_config() -> BoardPlanConfig {
        BoardPlanConfig {
            cycle_commission: 500.0,
            re_entry_enabled: true,
            re_entry_position: ReEntryPosition::Bottom,
            max_cycles_per_period: 4,
            max_cascade_depth: 10,
            stall_threshold_periods: 3,
            inactive_compression: false,
        }
    }

    #[test]
    fn new_creates_engine_with_initial_board() {
        let engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();

        assert_eq!(engine.dimensions(), (2, 2));
        assert_eq!(engine.board_count(), 1);
        assert!(engine.displaced_members().is_empty());

        let boards = engine.list_boards();
        assert_eq!(boards.len(), 1);
        assert_eq!(boards[0].filled_count, 0);
        // 2x2 tree: 1 + 2 + 4 = 7
        assert_eq!(boards[0].total_positions, 7);
        assert_eq!(boards[0].created_at, 1000);
        assert!(boards[0].parent_board_id.is_none());
    }

    #[test]
    fn new_rejects_invalid_width_zero() {
        let result = BoardPlanEngine::new(0, 2, test_config(), 1000);
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert!(matches!(
            err,
            BoardPlanError::InvalidDimensions { width: 0, .. }
        ));
    }

    #[test]
    fn new_rejects_invalid_width_one() {
        let result = BoardPlanEngine::new(1, 2, test_config(), 1000);
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert!(matches!(
            err,
            BoardPlanError::InvalidDimensions { width: 1, .. }
        ));
    }

    #[test]
    fn new_rejects_invalid_width_six() {
        let result = BoardPlanEngine::new(6, 2, test_config(), 1000);
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert!(matches!(
            err,
            BoardPlanError::InvalidDimensions { width: 6, .. }
        ));
    }

    #[test]
    fn new_rejects_invalid_height_zero() {
        let result = BoardPlanEngine::new(2, 0, test_config(), 1000);
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert!(matches!(
            err,
            BoardPlanError::InvalidDimensions { height: 0, .. }
        ));
    }

    #[test]
    fn new_rejects_invalid_height_five() {
        let result = BoardPlanEngine::new(2, 5, test_config(), 1000);
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert!(matches!(
            err,
            BoardPlanError::InvalidDimensions { height: 5, .. }
        ));
    }

    #[test]
    fn new_accepts_boundary_dimensions_2x1() {
        let engine = BoardPlanEngine::new(2, 1, test_config(), 1000).unwrap();
        assert_eq!(engine.dimensions(), (2, 1));
        // 2x1 tree: 1 + 2 = 3
        let boards = engine.list_boards();
        assert_eq!(boards[0].total_positions, 3);
    }

    #[test]
    fn new_accepts_boundary_dimensions_5x4() {
        let engine = BoardPlanEngine::new(5, 4, test_config(), 1000).unwrap();
        assert_eq!(engine.dimensions(), (5, 4));
        // 5x4 tree: 1 + 5 + 25 + 125 + 625 = 781
        let boards = engine.list_boards();
        assert_eq!(boards[0].total_positions, 781);
    }

    #[test]
    fn list_boards_returns_sorted_summaries() {
        // Create an engine and manually add boards with different timestamps
        // to verify sort order.
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();

        let board_b = Board::new(engine.total_positions, 3000, None);
        let board_a = Board::new(engine.total_positions, 2000, None);

        engine.boards.insert(board_b.id, board_b);
        engine.boards.insert(board_a.id, board_a);

        let summaries = engine.list_boards();
        assert_eq!(summaries.len(), 3);
        assert_eq!(summaries[0].created_at, 1000);
        assert_eq!(summaries[1].created_at, 2000);
        assert_eq!(summaries[2].created_at, 3000);
    }
}
