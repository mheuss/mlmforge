//! Board plan engine — manages all boards for one board plan structure.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::config::board_plan::{BoardPlanConfig, ReEntryPosition};

use super::board::{self, Board};
use super::error::BoardPlanError;
use super::types::{AddMemberResult, BoardSummary, CycleEvent, RemoveMemberResult};

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

    /// Adds a member to a board.
    ///
    /// Validates the member is not already placed and the sponsor exists.
    /// First member is bootstrapped automatically. Displaced members are
    /// placed before the new member. Placement board depends on the
    /// configured `re_entry_position`.
    pub fn add_member(
        &mut self,
        user_id: Uuid,
        sponsor_id: Uuid,
        timestamp: i64,
    ) -> Result<AddMemberResult, BoardPlanError> {
        // Validate: member not already on a board.
        if self.member_boards.contains_key(&user_id) {
            return Err(BoardPlanError::MemberAlreadyExists(user_id));
        }

        // Bootstrap: first member auto-registers the sponsor.
        if self.member_boards.is_empty() && self.sponsor_map.is_empty() {
            self.sponsor_map.insert(user_id, sponsor_id);
        } else {
            // Validate: sponsor must exist in sponsor_map.
            if !self.sponsor_map.contains_key(&sponsor_id)
                && !self.member_boards.contains_key(&sponsor_id)
            {
                return Err(BoardPlanError::SponsorNotFound(sponsor_id));
            }
            self.sponsor_map.insert(user_id, sponsor_id);
        }

        // Place displaced members first.
        self.place_displaced_members(timestamp)?;

        // Find the board to place this member on.
        let board_id = self.find_placement_board(sponsor_id)?;

        // Place member at first open slot.
        let board = self
            .boards
            .get_mut(&board_id)
            .ok_or(BoardPlanError::BoardNotFound(board_id))?;
        let position = board
            .first_open_position()
            .ok_or(BoardPlanError::NoBoardsAvailable)?;
        board.positions[position] = Some(user_id);
        board.last_activity_at = timestamp;

        // Track membership.
        self.member_boards.insert(user_id, board_id);

        // Check if board is full. If so, trigger cycling (stub).
        let mut cycle_events = Vec::new();
        if board.is_full() {
            self.process_cycles(board_id, timestamp, &mut cycle_events, 0);
        }

        Ok(AddMemberResult {
            board_id,
            position,
            cycle_events,
        })
    }

    /// Finds the board to place a member on based on config.
    ///
    /// With `Bottom` mode, returns the oldest board with an opening.
    /// With `SponsorBoard` mode, tries the sponsor's current board
    /// first. Falls back to the oldest board if the sponsor's board
    /// is full or the sponsor has no board.
    fn find_placement_board(&self, sponsor_id: Uuid) -> Result<Uuid, BoardPlanError> {
        match self.config.re_entry_position {
            ReEntryPosition::Bottom => self.oldest_board_with_opening(),
            ReEntryPosition::SponsorBoard => {
                if let Some(&sponsor_board_id) = self.member_boards.get(&sponsor_id) {
                    if let Some(board) = self.boards.get(&sponsor_board_id) {
                        if board.first_open_position().is_some() {
                            return Ok(sponsor_board_id);
                        }
                    }
                }
                // Fall back to oldest board with opening.
                self.oldest_board_with_opening()
            }
        }
    }

    /// Returns the id of the oldest board that has at least one open
    /// position. Boards are compared by `created_at` ascending.
    fn oldest_board_with_opening(&self) -> Result<Uuid, BoardPlanError> {
        self.boards
            .values()
            .filter(|b| b.first_open_position().is_some())
            .min_by_key(|b| b.created_at)
            .map(|b| b.id)
            .ok_or(BoardPlanError::NoBoardsAvailable)
    }

    /// Drains displaced members and places each in the oldest board
    /// with an opening.
    fn place_displaced_members(&mut self, timestamp: i64) -> Result<(), BoardPlanError> {
        let displaced = std::mem::take(&mut self.displaced_members);
        for member_id in displaced {
            let board_id = self.oldest_board_with_opening()?;
            let board = self
                .boards
                .get_mut(&board_id)
                .ok_or(BoardPlanError::BoardNotFound(board_id))?;
            if let Some(pos) = board.first_open_position() {
                board.positions[pos] = Some(member_id);
                board.last_activity_at = timestamp;
                self.member_boards.insert(member_id, board_id);
            }
        }
        Ok(())
    }

    /// Removes a member from their board and compacts the remaining
    /// members to fill the gap.
    ///
    /// Returns the list of members who were shifted during compaction
    /// and any cycle events triggered if compaction filled the board.
    pub fn remove_member(
        &mut self,
        user_id: Uuid,
        timestamp: i64,
    ) -> Result<RemoveMemberResult, BoardPlanError> {
        // Find the member's board.
        let board_id = self
            .member_boards
            .get(&user_id)
            .copied()
            .ok_or(BoardPlanError::MemberNotFound(user_id))?;

        // Find their position and clear it.
        let board = self
            .boards
            .get_mut(&board_id)
            .ok_or(BoardPlanError::BoardNotFound(board_id))?;
        if let Some(pos) = board.positions.iter().position(|p| *p == Some(user_id)) {
            board.positions[pos] = None;
        }

        // Remove from member_boards.
        self.member_boards.remove(&user_id);

        // Update last activity.
        let board = self
            .boards
            .get_mut(&board_id)
            .ok_or(BoardPlanError::BoardNotFound(board_id))?;
        board.last_activity_at = timestamp;

        // Compact the board.
        let compacted = self.compact_board(board_id);

        // Check if compaction filled the board.
        let mut cycle_events = Vec::new();
        let board = self
            .boards
            .get(&board_id)
            .ok_or(BoardPlanError::BoardNotFound(board_id))?;
        if board.is_full() {
            self.process_cycles(board_id, timestamp, &mut cycle_events, 0);
        }

        Ok(RemoveMemberResult {
            compacted,
            cycle_events,
        })
    }

    /// Compacts a board after a removal by shifting remaining members
    /// to fill gaps.
    ///
    /// Collects all members in position order, clears all positions,
    /// then re-places members sequentially from position 0. Returns
    /// the list of members whose position changed.
    fn compact_board(&mut self, board_id: Uuid) -> Vec<Uuid> {
        let board = match self.boards.get(&board_id) {
            Some(b) => b,
            None => return Vec::new(),
        };

        // Collect members in their current position order, noting
        // their original positions.
        let members_with_positions: Vec<(usize, Uuid)> = board
            .positions
            .iter()
            .enumerate()
            .filter_map(|(i, p)| p.map(|uid| (i, uid)))
            .collect();

        // Clear all positions.
        let board = self.boards.get_mut(&board_id).unwrap();
        for pos in board.positions.iter_mut() {
            *pos = None;
        }

        // Re-place members sequentially and track who moved.
        let mut moved = Vec::new();
        for (new_pos, &(old_pos, member_id)) in members_with_positions.iter().enumerate() {
            board.positions[new_pos] = Some(member_id);
            self.member_boards.insert(member_id, board_id);
            if new_pos != old_pos {
                moved.push(member_id);
            }
        }

        moved
    }

    /// Processes cycling when a board fills.
    ///
    /// Stub for now. Cycling logic is implemented in Task 6.
    #[allow(unused_variables, clippy::ptr_arg)]
    fn process_cycles(
        &mut self,
        board_id: Uuid,
        timestamp: i64,
        cycle_events: &mut Vec<CycleEvent>,
        depth: u32,
    ) {
        // Cycling is implemented in Task 6.
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

    // --- add_member tests ---

    /// Helper to create deterministic UUIDs for tests.
    fn test_uuid(n: u8) -> Uuid {
        Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
    }

    /// Creates a BoardPlanConfig with SponsorBoard re-entry.
    fn test_config_sponsor_board() -> BoardPlanConfig {
        BoardPlanConfig {
            cycle_commission: 500.0,
            re_entry_enabled: true,
            re_entry_position: ReEntryPosition::SponsorBoard,
            max_cycles_per_period: 4,
            max_cascade_depth: 10,
            stall_threshold_periods: 3,
            inactive_compression: false,
        }
    }

    #[test]
    fn add_member_places_at_position_zero() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);
        let member = test_uuid(2);

        let result = engine.add_member(member, sponsor, 2000).unwrap();

        assert_eq!(result.position, 0);
        assert!(result.cycle_events.is_empty());
        assert_eq!(engine.get_member_board(member), Some(result.board_id));
    }

    #[test]
    fn add_member_fills_bfs_order() {
        // 2x2 board has 7 positions. Add 6 members (0-5) to fill
        // positions 0-5 sequentially. Don't fill all 7 because
        // cycling is still a stub.
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // First member bootstraps the sponsor relationship.
        let r0 = engine.add_member(test_uuid(10), sponsor, 2000).unwrap();
        assert_eq!(r0.position, 0);

        // Subsequent members use the first member as sponsor since
        // sponsor_map tracks the first member now.
        let r1 = engine
            .add_member(test_uuid(11), test_uuid(10), 2001)
            .unwrap();
        assert_eq!(r1.position, 1);

        let r2 = engine
            .add_member(test_uuid(12), test_uuid(10), 2002)
            .unwrap();
        assert_eq!(r2.position, 2);

        let r3 = engine
            .add_member(test_uuid(13), test_uuid(10), 2003)
            .unwrap();
        assert_eq!(r3.position, 3);

        let r4 = engine
            .add_member(test_uuid(14), test_uuid(10), 2004)
            .unwrap();
        assert_eq!(r4.position, 4);

        let r5 = engine
            .add_member(test_uuid(15), test_uuid(10), 2005)
            .unwrap();
        assert_eq!(r5.position, 5);

        // All placed on the same board.
        let board_id = r0.board_id;
        assert_eq!(r1.board_id, board_id);
        assert_eq!(r2.board_id, board_id);
        assert_eq!(r3.board_id, board_id);
        assert_eq!(r4.board_id, board_id);
        assert_eq!(r5.board_id, board_id);

        // Board has 6 of 7 slots filled.
        let board = engine.get_board(board_id).unwrap();
        assert_eq!(board.filled_count(), 6);
    }

    #[test]
    fn add_member_rejects_duplicate() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);
        let member = test_uuid(2);

        engine.add_member(member, sponsor, 2000).unwrap();
        let result = engine.add_member(member, sponsor, 3000);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            BoardPlanError::MemberAlreadyExists(id) if id == member
        ));
    }

    #[test]
    fn add_member_rejects_unknown_sponsor() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Bootstrap a first member so the engine is no longer empty.
        engine.add_member(test_uuid(10), sponsor, 2000).unwrap();

        // Now try to add with an unknown sponsor.
        let unknown_sponsor = test_uuid(99);
        let result = engine.add_member(test_uuid(11), unknown_sponsor, 3000);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            BoardPlanError::SponsorNotFound(id) if id == unknown_sponsor
        ));
    }

    #[test]
    fn add_member_sponsor_board_mode_prefers_sponsors_board() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config_sponsor_board(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add sponsor as the first member (bootstrap).
        let sponsor_result = engine.add_member(sponsor, test_uuid(99), 2000).unwrap();
        let sponsor_board = sponsor_result.board_id;

        // Add a new member with this sponsor. SponsorBoard mode
        // should place on the sponsor's board.
        let member = test_uuid(2);
        let result = engine.add_member(member, sponsor, 3000).unwrap();

        assert_eq!(result.board_id, sponsor_board);
        assert_eq!(result.position, 1);
    }

    #[test]
    fn add_member_sponsor_board_mode_falls_back_to_oldest() {
        let mut engine = BoardPlanEngine::new(2, 1, test_config_sponsor_board(), 1000).unwrap();
        // 2x1 board has 3 positions (root + 2 children).

        let sponsor = test_uuid(1);

        // Fill the initial board completely to trigger cycling stub.
        // Position 0: sponsor (bootstrap).
        engine.add_member(sponsor, test_uuid(99), 2000).unwrap();
        // Position 1.
        engine.add_member(test_uuid(2), sponsor, 2001).unwrap();
        // Position 2 — board is now full (cycling stub does nothing).
        engine.add_member(test_uuid(3), sponsor, 2002).unwrap();

        // Sponsor's board is full. Add a second board manually so
        // there's somewhere to fall back to.
        let fallback_board = Board::new(engine.total_positions, 3000, None);
        let fallback_id = fallback_board.id;
        engine.boards.insert(fallback_id, fallback_board);

        // New member with sponsor whose board is full.
        let member = test_uuid(4);
        let result = engine.add_member(member, sponsor, 4000).unwrap();

        // Should fall back to the oldest board with an opening,
        // which is the manually added fallback board (the original
        // board is full).
        assert_eq!(result.board_id, fallback_id);
        assert_eq!(result.position, 0);
    }

    // --- remove_member tests ---

    #[test]
    fn remove_member_clears_position_and_compacts() {
        // 2x2 board has 7 positions.
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add 3 members at positions 0, 1, 2.
        let r0 = engine.add_member(test_uuid(10), sponsor, 2000).unwrap();
        let board_id = r0.board_id;
        engine
            .add_member(test_uuid(11), test_uuid(10), 2001)
            .unwrap();
        engine
            .add_member(test_uuid(12), test_uuid(10), 2002)
            .unwrap();

        // Remove member at position 1.
        let result = engine.remove_member(test_uuid(11), 3000).unwrap();

        // Member at position 2 should compact into position 1.
        assert!(result.compacted.contains(&test_uuid(12)));
        assert!(result.cycle_events.is_empty());

        // Removed member is no longer on any board.
        assert_eq!(engine.get_member_board(test_uuid(11)), None);

        // Board has 2 members at positions 0 and 1.
        let board = engine.get_board(board_id).unwrap();
        assert_eq!(board.positions[0], Some(test_uuid(10)));
        assert_eq!(board.positions[1], Some(test_uuid(12)));
        assert_eq!(board.positions[2], None);
        assert_eq!(board.filled_count(), 2);
    }

    #[test]
    fn remove_member_not_found() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let nonexistent = test_uuid(99);

        let result = engine.remove_member(nonexistent, 3000);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            BoardPlanError::MemberNotFound(id) if id == nonexistent
        ));
    }

    #[test]
    fn remove_last_member_no_compaction_needed() {
        // 2x2 board has 7 positions.
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add 3 members at positions 0, 1, 2.
        engine.add_member(test_uuid(10), sponsor, 2000).unwrap();
        engine
            .add_member(test_uuid(11), test_uuid(10), 2001)
            .unwrap();
        engine
            .add_member(test_uuid(12), test_uuid(10), 2002)
            .unwrap();

        // Remove the last member (position 2). No gap to compact.
        let result = engine.remove_member(test_uuid(12), 3000).unwrap();

        assert!(result.compacted.is_empty());
        assert!(result.cycle_events.is_empty());

        // Remaining members still in positions 0 and 1.
        let board_id = engine.get_member_board(test_uuid(10)).unwrap();
        let board = engine.get_board(board_id).unwrap();
        assert_eq!(board.positions[0], Some(test_uuid(10)));
        assert_eq!(board.positions[1], Some(test_uuid(11)));
        assert_eq!(board.positions[2], None);
    }

    #[test]
    fn compact_preserves_member_board_mapping() {
        // 2x2 board has 7 positions.
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add 4 members at positions 0, 1, 2, 3.
        engine.add_member(test_uuid(10), sponsor, 2000).unwrap();
        engine
            .add_member(test_uuid(11), test_uuid(10), 2001)
            .unwrap();
        engine
            .add_member(test_uuid(12), test_uuid(10), 2002)
            .unwrap();
        engine
            .add_member(test_uuid(13), test_uuid(10), 2003)
            .unwrap();

        let board_id = engine.get_member_board(test_uuid(10)).unwrap();

        // Remove member at position 1 to create a gap.
        engine.remove_member(test_uuid(11), 3000).unwrap();

        // All remaining members should still map to this board.
        assert_eq!(engine.get_member_board(test_uuid(10)), Some(board_id));
        assert_eq!(engine.get_member_board(test_uuid(12)), Some(board_id));
        assert_eq!(engine.get_member_board(test_uuid(13)), Some(board_id));

        // Verify positions are correctly compacted.
        let board = engine.get_board(board_id).unwrap();
        assert_eq!(board.positions[0], Some(test_uuid(10)));
        assert_eq!(board.positions[1], Some(test_uuid(12)));
        assert_eq!(board.positions[2], Some(test_uuid(13)));
        assert_eq!(board.positions[3], None);
    }
}
