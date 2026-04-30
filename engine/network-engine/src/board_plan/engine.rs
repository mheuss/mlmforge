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
            // Validate: sponsor must be an active participant. Check
            // member_boards (on a board) or displaced_members (awaiting
            // reassignment). The sponsor_map is permanent enrollment
            // data and can't be used for validation because removed
            // members remain in it for re-entry routing.
            if !self.member_boards.contains_key(&sponsor_id)
                && !self.displaced_members.contains(&sponsor_id)
            {
                return Err(BoardPlanError::SponsorNotFound(sponsor_id));
            }
            self.sponsor_map.insert(user_id, sponsor_id);
        }

        // Place displaced members first.
        let mut cycle_events = Vec::new();
        self.place_displaced_members(timestamp, &mut cycle_events);

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

        // Check if board is full. If so, trigger cycling.
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
    /// with an opening. If a placement fills a board, cycling is
    /// triggered and the resulting events are appended to
    /// `cycle_events`.
    ///
    /// Members that cannot be placed (no boards available, board not
    /// found, no open position) are pushed back into
    /// `self.displaced_members` so they are retried on the next call.
    fn place_displaced_members(&mut self, timestamp: i64, cycle_events: &mut Vec<CycleEvent>) {
        let displaced = std::mem::take(&mut self.displaced_members);
        for member_id in displaced {
            let placed = if let Ok(board_id) = self.oldest_board_with_opening() {
                if let Some(board) = self.boards.get_mut(&board_id) {
                    if let Some(pos) = board.first_open_position() {
                        board.positions[pos] = Some(member_id);
                        board.last_activity_at = timestamp;
                        self.member_boards.insert(member_id, board_id);
                        if board.is_full() {
                            self.process_cycles(board_id, timestamp, cycle_events, 0);
                        }
                        true
                    } else {
                        false
                    }
                } else {
                    false
                }
            } else {
                false
            };

            if !placed {
                self.displaced_members.push(member_id);
            }
        }
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
        let board = self
            .boards
            .get_mut(&board_id)
            .expect("board_id validated by preceding get()");
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
    /// When a board is full, the root (position 0) cycles out and
    /// earns commission. The board is split into `width` new boards,
    /// one per child subtree of the root. The original board is
    /// removed.
    ///
    /// If re-entry is enabled, the cycled member is placed on a
    /// board with an open slot. If no slot is available, they go to
    /// displaced_members.
    ///
    /// Recursion is bounded by `max_cascade_depth` from config.
    fn process_cycles(
        &mut self,
        board_id: Uuid,
        timestamp: i64,
        cycle_events: &mut Vec<CycleEvent>,
        depth: u32,
    ) {
        // Cascade bound: prevent runaway chain reactions.
        if depth >= self.config.max_cascade_depth {
            return;
        }

        // Get the board and verify it is full.
        let board = match self.boards.get(&board_id) {
            Some(b) if b.is_full() => b.clone(),
            _ => return,
        };

        // Root (position 0) cycles out.
        let cycled_member = match board.positions[0] {
            Some(member) => member,
            None => return,
        };

        // Split the board into child subtree boards.
        let new_board_ids = self.split_board(&board, timestamp);

        // Remove the original board.
        self.boards.remove(&board_id);

        // Remove cycled member from member_boards.
        self.member_boards.remove(&cycled_member);

        // Handle re-entry or displacement.
        let re_entry_board = if self.config.re_entry_enabled {
            let sponsor_id = self
                .sponsor_map
                .get(&cycled_member)
                .copied()
                .unwrap_or(cycled_member);
            match self.find_placement_board(sponsor_id) {
                Ok(target_board_id) => {
                    let target = self.boards.get_mut(&target_board_id);
                    if let Some(target) = target {
                        if let Some(pos) = target.first_open_position() {
                            target.positions[pos] = Some(cycled_member);
                            target.last_activity_at = timestamp;
                            self.member_boards.insert(cycled_member, target_board_id);
                            Some(target_board_id)
                        } else {
                            self.displaced_members.push(cycled_member);
                            None
                        }
                    } else {
                        self.displaced_members.push(cycled_member);
                        None
                    }
                }
                Err(_) => {
                    self.displaced_members.push(cycled_member);
                    None
                }
            }
        } else {
            self.displaced_members.push(cycled_member);
            None
        };

        // Emit cycle event.
        cycle_events.push(CycleEvent {
            board_id,
            cycled_member,
            new_boards: new_board_ids.clone(),
            re_entry_board,
        });

        // Cascade: check if the re-entry board is now full.
        if let Some(re_board_id) = re_entry_board {
            let is_full = self
                .boards
                .get(&re_board_id)
                .map(|b| b.is_full())
                .unwrap_or(false);
            if is_full {
                self.process_cycles(re_board_id, timestamp, cycle_events, depth + 1);
            }
        }

        // Cascade: check if any new board is now full.
        for new_id in &new_board_ids {
            let is_full = self
                .boards
                .get(new_id)
                .map(|b| b.is_full())
                .unwrap_or(false);
            if is_full {
                self.process_cycles(*new_id, timestamp, cycle_events, depth + 1);
            }
        }
    }

    /// Splits a full board into `width` new boards, one per child
    /// subtree of the root.
    ///
    /// Each child of the root becomes position 0 in a new board.
    /// The subtree members are mapped to sequential BFS positions
    /// in the new board. The original board's members have their
    /// `member_boards` entries updated to point to the new board.
    ///
    /// Returns the IDs of the newly created boards.
    fn split_board(&mut self, board: &Board, timestamp: i64) -> Vec<Uuid> {
        let w = self.width as usize;
        let max_index = self.total_positions - 1;
        let mut new_board_ids = Vec::with_capacity(w);

        for child_index in 1..=w {
            // Get the subtree indices in BFS order.
            let subtree = board::subtree_indices(child_index, self.width, max_index);

            // Create a new board with the same total_positions.
            let mut new_board = Board::new(self.total_positions, timestamp, Some(board.id));

            // Map each subtree member to sequential positions in the
            // new board, preserving BFS order.
            for (new_pos, &old_index) in subtree.iter().enumerate() {
                if let Some(member_id) = board.positions[old_index] {
                    new_board.positions[new_pos] = Some(member_id);
                    self.member_boards.insert(member_id, new_board.id);
                }
            }

            new_board_ids.push(new_board.id);
            self.boards.insert(new_board.id, new_board);
        }

        new_board_ids
    }

    /// Detects boards that have not had activity since the cutoff
    /// timestamp.
    ///
    /// Returns a list of `StalledBoard` summaries for boards where
    /// `last_activity_at < cutoff_timestamp`. The Go layer converts
    /// the `stall_threshold_periods` config value into a concrete
    /// timestamp before calling this method.
    pub fn detect_stalled_boards(&self, cutoff_timestamp: i64) -> Vec<super::types::StalledBoard> {
        self.boards
            .values()
            .filter(|b| b.last_activity_at < cutoff_timestamp)
            .map(|b| super::types::StalledBoard {
                board_id: b.id,
                last_activity_at: b.last_activity_at,
                filled_positions: b.filled_count(),
                total_positions: self.total_positions,
                members: b.members(),
            })
            .collect()
    }

    /// Dissolves a board, moving all its members to the displaced pool.
    ///
    /// Removes the board from the engine and pushes every occupant
    /// into `displaced_members`. Returns a `DissolutionResult`
    /// listing the dissolved board and the displaced members.
    pub fn dissolve_board(
        &mut self,
        board_id: Uuid,
        _timestamp: i64,
    ) -> Result<super::types::DissolutionResult, BoardPlanError> {
        let board = self
            .boards
            .remove(&board_id)
            .ok_or(BoardPlanError::BoardNotFound(board_id))?;

        let members = board.members();
        for &member in &members {
            self.member_boards.remove(&member);
            self.displaced_members.push(member);
        }

        Ok(super::types::DissolutionResult {
            dissolved_board_id: board_id,
            displaced_members: members,
        })
    }

    /// Removes inactive members from their boards and compacts the
    /// remaining members.
    ///
    /// For each member in `inactive_members`: if they occupy a board
    /// position, `remove_member` is called (which handles compaction
    /// and may trigger cycling). Members not on any board are silently
    /// skipped.
    pub fn compress_inactive(
        &mut self,
        inactive_members: Vec<Uuid>,
        timestamp: i64,
    ) -> Result<super::types::CompressionResult, BoardPlanError> {
        let mut compressed = Vec::new();
        let mut cycle_events = Vec::new();

        for member_id in inactive_members {
            // Skip members not on any board.
            let board_id = match self.member_boards.get(&member_id) {
                Some(&bid) => bid,
                None => continue,
            };

            let result = self.remove_member(member_id, timestamp)?;
            compressed.push(super::types::CompressedMember {
                user_id: member_id,
                board_id,
            });
            cycle_events.extend(result.cycle_events);
        }

        Ok(super::types::CompressionResult {
            compressed,
            cycle_events,
        })
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
        // a full board triggers cycling.
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
        let mut engine = BoardPlanEngine::new(2, 2, test_config_sponsor_board(), 1000).unwrap();
        // 2x2 board has 7 positions.

        let sponsor = test_uuid(1);

        // Add sponsor as first member (bootstrap).
        let sponsor_result = engine.add_member(sponsor, test_uuid(99), 2000).unwrap();
        let sponsor_board = sponsor_result.board_id;

        // Manually fill the rest of the sponsor's board without
        // going through add_member (which would trigger cycling).
        // This simulates a full board for fallback testing.
        let board = engine.boards.get_mut(&sponsor_board).unwrap();
        for i in 1..engine.total_positions {
            let filler = test_uuid(50 + i as u8);
            board.positions[i] = Some(filler);
            engine.member_boards.insert(filler, sponsor_board);
            engine.sponsor_map.insert(filler, sponsor);
        }

        // Add a fallback board with open slots.
        let fallback_board = Board::new(engine.total_positions, 3000, None);
        let fallback_id = fallback_board.id;
        engine.boards.insert(fallback_id, fallback_board);

        // New member with sponsor whose board is full.
        let member = test_uuid(4);
        let result = engine.add_member(member, sponsor, 4000).unwrap();

        // SponsorBoard mode tries sponsor's board first, but it's
        // full, so it falls back to the oldest board with an opening.
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

    // --- cycling tests ---

    /// Creates a config with re-entry disabled.
    fn test_config_no_reentry() -> BoardPlanConfig {
        BoardPlanConfig {
            cycle_commission: 500.0,
            re_entry_enabled: false,
            re_entry_position: ReEntryPosition::Bottom,
            max_cycles_per_period: 4,
            max_cascade_depth: 10,
            stall_threshold_periods: 3,
            inactive_compression: false,
        }
    }

    /// Fills a 2x2 board (7 positions) by adding members one at a
    /// time. Returns (members, last_result) where members is the
    /// ordered list of UUIDs placed at positions 0-6, and
    /// last_result is the AddMemberResult from the 7th (final) add.
    fn fill_2x2_board(engine: &mut BoardPlanEngine) -> (Vec<Uuid>, AddMemberResult) {
        let sponsor = test_uuid(1);
        let mut members = Vec::new();
        let mut last_result = None;

        for i in 0..7u8 {
            let member = test_uuid(10 + i);
            let s = if i == 0 { sponsor } else { test_uuid(10) };
            let result = engine.add_member(member, s, 2000 + i as i64).unwrap();
            members.push(member);
            last_result = Some(result);
        }

        (members, last_result.unwrap())
    }

    #[test]
    fn full_board_triggers_cycle() {
        // 2x2 board, re-entry enabled. Add 7 members to fill it.
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let (members, last_result) = fill_2x2_board(&mut engine);

        // The 7th add_member should return at least one cycle event.
        assert!(
            !last_result.cycle_events.is_empty(),
            "filling the board should trigger at least one cycle event"
        );

        let event = &last_result.cycle_events[0];

        // The cycled member should be the root (first member placed).
        assert_eq!(
            event.cycled_member, members[0],
            "root (position 0) should cycle out"
        );

        // The event should list 2 new boards (width=2).
        assert_eq!(
            event.new_boards.len(),
            2,
            "split should create width (2) new boards"
        );

        // Since re_entry_enabled is true, root should have been
        // re-entered on one of the new boards.
        assert!(
            event.re_entry_board.is_some(),
            "with re-entry enabled, root should have a re-entry board"
        );
        assert!(
            engine.get_member_board(members[0]).is_some(),
            "with re-entry enabled, root should be on a board"
        );

        // Original board replaced by 2 new boards.
        assert_eq!(engine.board_count(), 2);
    }

    #[test]
    fn split_creates_correct_subtrees() {
        // Use re_entry_enabled=false to keep the cycled root out.
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();
        let (members, _) = fill_2x2_board(&mut engine);

        // After cycling: root (members[0]) is displaced.
        assert!(
            engine.displaced_members().contains(&members[0]),
            "root should be displaced when re-entry is disabled"
        );

        // 2 new boards, each with 3 members.
        assert_eq!(engine.board_count(), 2);

        // Identify boards by their root member (position 0) since
        // board ordering in list_boards is not deterministic when
        // timestamps are equal.
        let left_board_id = engine.get_member_board(members[1]).unwrap();
        let right_board_id = engine.get_member_board(members[2]).unwrap();
        assert_ne!(
            left_board_id, right_board_id,
            "subtrees go to different boards"
        );

        let left_board = engine.get_board(left_board_id).unwrap();
        let right_board = engine.get_board(right_board_id).unwrap();

        assert_eq!(left_board.filled_count(), 3);
        assert_eq!(right_board.filled_count(), 3);

        // Left subtree: members[1] (root), members[3], members[4].
        assert_eq!(left_board.positions[0], Some(members[1]));
        assert_eq!(left_board.positions[1], Some(members[3]));
        assert_eq!(left_board.positions[2], Some(members[4]));

        // Right subtree: members[2] (root), members[5], members[6].
        assert_eq!(right_board.positions[0], Some(members[2]));
        assert_eq!(right_board.positions[1], Some(members[5]));
        assert_eq!(right_board.positions[2], Some(members[6]));
    }

    #[test]
    fn cycle_with_reentry_places_cycled_member() {
        // Default config has re_entry_enabled=true, Bottom mode.
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let (members, _) = fill_2x2_board(&mut engine);

        // Root (members[0]) should have been re-entered.
        let root_board = engine.get_member_board(members[0]);
        assert!(
            root_board.is_some(),
            "cycled root should be re-entered when re_entry_enabled"
        );

        // Root should be on one of the new boards, not displaced.
        assert!(
            engine.displaced_members().is_empty(),
            "no members should be displaced with re-entry enabled"
        );

        // Verify root is actually in a board's positions.
        let board_id = root_board.unwrap();
        let board = engine.get_board(board_id).unwrap();
        assert!(
            board.positions.iter().any(|p| *p == Some(members[0])),
            "root should appear in the board's position array"
        );
    }

    #[test]
    fn cycle_without_reentry_displaces_member() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();
        let (members, _) = fill_2x2_board(&mut engine);

        // Root should be displaced, not on any board.
        assert_eq!(
            engine.get_member_board(members[0]),
            None,
            "cycled root should not be on any board"
        );
        assert!(
            engine.displaced_members().contains(&members[0]),
            "cycled root should be in displaced_members"
        );
    }

    #[test]
    fn new_boards_have_correct_lineage() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();

        // Capture the original board ID before cycling destroys it.
        let original_board_id = engine.list_boards()[0].id;

        fill_2x2_board(&mut engine);

        // After cycling, both new boards should have parent_board_id
        // pointing to the original board.
        let boards = engine.list_boards();
        assert_eq!(boards.len(), 2);

        for board_summary in &boards {
            assert_eq!(
                board_summary.parent_board_id,
                Some(original_board_id),
                "new board should reference the original as parent"
            );
        }
    }

    #[test]
    fn cycle_reentry_sponsor_board_mode_uses_sponsors_board() {
        // SponsorBoard re-entry: the cycled root should land on their
        // sponsor's board, not just the oldest board with an opening.
        let mut engine = BoardPlanEngine::new(2, 1, test_config_sponsor_board(), 1000).unwrap();

        // 2x1 board has 3 positions (root + 2 children).
        // Sponsor sits on a separate board so we can verify the
        // cycled root lands there instead of falling back to Bottom.

        let sponsor = test_uuid(1);
        let root = test_uuid(10);

        // Bootstrap: root's sponsor is `sponsor`.
        engine.add_member(root, sponsor, 2000).unwrap();
        let _root_board = engine.get_member_board(root).unwrap();

        // The sponsor was auto-registered in sponsor_map by the
        // bootstrap path but is not on any board yet. Place the
        // sponsor on a second board so SponsorBoard mode has a
        // target to find.
        let sponsor_board = Board::new(engine.total_positions, 1500, None);
        let sponsor_board_id = sponsor_board.id;
        engine.boards.insert(sponsor_board_id, sponsor_board);
        engine.boards.get_mut(&sponsor_board_id).unwrap().positions[0] = Some(sponsor);
        engine.member_boards.insert(sponsor, sponsor_board_id);

        // Fill the remaining 2 positions on root's board to trigger
        // cycling.
        let child_a = test_uuid(11);
        let child_b = test_uuid(12);
        engine.add_member(child_a, root, 3000).unwrap();
        let result = engine.add_member(child_b, root, 4000).unwrap();

        // Cycling should have fired.
        assert!(
            !result.cycle_events.is_empty(),
            "filling the board should trigger cycling"
        );

        let event = &result.cycle_events[0];
        assert_eq!(event.cycled_member, root, "root should cycle out");

        // The root should re-enter on the sponsor's board, not one
        // of the newly split boards.
        assert_eq!(
            event.re_entry_board,
            Some(sponsor_board_id),
            "SponsorBoard mode should place cycled root on sponsor's board"
        );
        assert_eq!(
            engine.get_member_board(root),
            Some(sponsor_board_id),
            "root should be tracked on the sponsor's board"
        );

        // Verify root is actually in the sponsor board's positions.
        let sb = engine.get_board(sponsor_board_id).unwrap();
        assert!(
            sb.positions.contains(&Some(root)),
            "root should appear in sponsor board's position array"
        );
    }

    // --- cascade and edge case tests ---

    #[test]
    fn cascade_bound_stops_at_max_depth() {
        // 2x1 board: 3 positions, cycles fast.
        // max_cascade_depth=1 limits chained cycles from a single add.
        let config = BoardPlanConfig {
            cycle_commission: 500.0,
            re_entry_enabled: true,
            re_entry_position: ReEntryPosition::Bottom,
            max_cycles_per_period: 4,
            max_cascade_depth: 1,
            stall_threshold_periods: 3,
            inactive_compression: false,
        };

        let mut engine = BoardPlanEngine::new(2, 1, config, 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add 20 members. With 3-position boards and re-entry, boards
        // fill and cycle frequently. The cascade bound should prevent
        // any single add_member from producing more than 1 level of
        // chained cycling.
        let mut all_cycle_events = Vec::new();
        for i in 0..20u8 {
            let member = test_uuid(100 + i);
            let s = if i == 0 { sponsor } else { test_uuid(100) };
            let result = engine.add_member(member, s, 2000 + i as i64).unwrap();
            // Each add_member's cycle events should have at most 1
            // depth level due to max_cascade_depth=1.
            assert!(
                result.cycle_events.len() <= 1,
                "max_cascade_depth=1 should limit cycle events to at most 1 per add, got {}",
                result.cycle_events.len()
            );
            all_cycle_events.extend(result.cycle_events);
        }

        // Cycles did fire (the system is working, just bounded).
        assert!(
            !all_cycle_events.is_empty(),
            "cycles should still fire, just bounded per add"
        );
    }

    #[test]
    fn displaced_members_placed_before_new_members() {
        // re_entry_enabled=false: cycled members go to displaced pool.
        // Use 2x2 (7 positions) so there is plenty of room after cycling.
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();

        // Fill the 2x2 board to trigger cycling.
        let (members, result) = fill_2x2_board(&mut engine);
        let root = members[0];

        // Root should be displaced after cycling.
        assert!(
            !result.cycle_events.is_empty(),
            "filling the board should trigger cycling"
        );
        assert!(
            engine.displaced_members().contains(&root),
            "root should be in displaced pool after cycling with re-entry disabled"
        );

        // Add a new member. The displaced root should be placed first.
        let new_member = test_uuid(30);
        engine.add_member(new_member, root, 5000).unwrap();

        // Displaced pool should now be empty.
        assert!(
            engine.displaced_members().is_empty(),
            "displaced pool should be empty after placing displaced members"
        );

        // The displaced root should be on a board.
        let root_board = engine.get_member_board(root);
        assert!(
            root_board.is_some(),
            "displaced root should have been placed on a board"
        );

        // Root was placed before the new member. Since place_displaced_members
        // runs before placement, root should have gotten an earlier position
        // or an earlier board than the new member.
        let new_member_board = engine.get_member_board(new_member);
        assert!(
            new_member_board.is_some(),
            "new member should also be on a board"
        );

        // Both should be placed. The key guarantee is that displaced
        // members are drained before the new member is placed, so
        // the root won't remain in the displaced pool.
        assert_ne!(
            engine.get_member_board(root),
            None,
            "root should not remain displaced"
        );
    }

    #[test]
    fn lineage_tracks_parent_board() {
        // Use no re-entry so we can inspect boards cleanly.
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();

        // Capture the original board ID.
        let original_board_id = engine.list_boards()[0].id;

        // Fill the 2x2 board (7 positions) to trigger cycling.
        fill_2x2_board(&mut engine);

        // After cycling, the original board is removed and replaced
        // by 2 new boards. Both should reference the original.
        let boards = engine.list_boards();
        assert_eq!(boards.len(), 2);

        for board_summary in &boards {
            assert_eq!(
                board_summary.parent_board_id,
                Some(original_board_id),
                "new board {} should have parent_board_id pointing to the original board",
                board_summary.id
            );
        }
    }

    // --- stall detection and dissolution tests ---

    #[test]
    fn detect_stalled_boards_returns_boards_before_cutoff() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add a member to update last_activity_at to 2000.
        engine.add_member(test_uuid(10), sponsor, 2000).unwrap();

        // Cutoff at 3000: the board's last_activity_at is 2000, which
        // is before the cutoff.
        let stalled = engine.detect_stalled_boards(3000);
        assert_eq!(stalled.len(), 1);
        assert_eq!(stalled[0].last_activity_at, 2000);
        assert_eq!(stalled[0].filled_positions, 1);
        assert_eq!(stalled[0].total_positions, 7);
        assert_eq!(stalled[0].members, vec![test_uuid(10)]);
    }

    #[test]
    fn detect_stalled_boards_excludes_active_boards() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add a member at timestamp 5000.
        engine.add_member(test_uuid(10), sponsor, 5000).unwrap();

        // Cutoff at 3000: the board's last_activity_at is 5000, which
        // is after the cutoff. No stalled boards.
        let stalled = engine.detect_stalled_boards(3000);
        assert!(
            stalled.is_empty(),
            "boards with activity after cutoff should not be stalled"
        );
    }

    #[test]
    fn dissolve_board_returns_displaced_members() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add 3 members.
        engine.add_member(test_uuid(10), sponsor, 2000).unwrap();
        engine
            .add_member(test_uuid(11), test_uuid(10), 2001)
            .unwrap();
        engine
            .add_member(test_uuid(12), test_uuid(10), 2002)
            .unwrap();

        let board_id = engine.get_member_board(test_uuid(10)).unwrap();

        // Dissolve the board.
        let result = engine.dissolve_board(board_id, 3000).unwrap();

        assert_eq!(result.dissolved_board_id, board_id);
        assert_eq!(result.displaced_members.len(), 3);
        assert!(result.displaced_members.contains(&test_uuid(10)));
        assert!(result.displaced_members.contains(&test_uuid(11)));
        assert!(result.displaced_members.contains(&test_uuid(12)));

        // Members are no longer on any board.
        assert_eq!(engine.get_member_board(test_uuid(10)), None);
        assert_eq!(engine.get_member_board(test_uuid(11)), None);
        assert_eq!(engine.get_member_board(test_uuid(12)), None);

        // Members are in the displaced pool.
        assert!(engine.displaced_members().contains(&test_uuid(10)));
        assert!(engine.displaced_members().contains(&test_uuid(11)));
        assert!(engine.displaced_members().contains(&test_uuid(12)));

        // Board no longer exists.
        assert!(engine.get_board(board_id).is_none());
    }

    #[test]
    fn dissolve_unknown_board_errors() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let unknown = Uuid::new_v4();

        let result = engine.dissolve_board(unknown, 3000);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            BoardPlanError::BoardNotFound(id) if id == unknown
        ));
    }

    // --- inactive compression tests ---

    #[test]
    fn compress_inactive_removes_and_compacts() {
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

        // Compress member at position 1 (test_uuid(11)).
        let result = engine.compress_inactive(vec![test_uuid(11)], 3000).unwrap();

        assert_eq!(result.compressed.len(), 1);
        assert_eq!(result.compressed[0].user_id, test_uuid(11));
        assert_eq!(result.compressed[0].board_id, board_id);

        // Removed member is no longer on any board.
        assert_eq!(engine.get_member_board(test_uuid(11)), None);

        // Remaining members are compacted: positions 0, 1, 2 filled.
        let board = engine.get_board(board_id).unwrap();
        assert_eq!(board.filled_count(), 3);
        assert_eq!(board.positions[0], Some(test_uuid(10)));
        assert_eq!(board.positions[1], Some(test_uuid(12)));
        assert_eq!(board.positions[2], Some(test_uuid(13)));
        assert_eq!(board.positions[3], None);
    }

    #[test]
    fn compress_inactive_skips_unknown_members() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add one member so the engine is not empty.
        engine.add_member(test_uuid(10), sponsor, 2000).unwrap();

        // Compress with members that are not on any board.
        let result = engine
            .compress_inactive(vec![test_uuid(99), test_uuid(98)], 3000)
            .unwrap();

        // Nothing was compressed.
        assert!(result.compressed.is_empty());
        assert!(result.cycle_events.is_empty());

        // The existing member is unaffected.
        assert!(engine.get_member_board(test_uuid(10)).is_some());
    }

    // --- commission run integration test ---

    #[test]
    fn full_commission_flow_with_cycling() {
        use crate::commission::board_plan::calculate_board_commissions;
        use std::collections::HashMap;

        // Create a 2x2 board plan engine.
        // test_config() gives cycle_commission=500, max_cycles_per_period=4.
        let config = test_config();
        let mut engine = BoardPlanEngine::new(2, 2, config.clone(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // Add 7 members to fill the board and trigger cycling.
        let mut all_cycle_events = Vec::new();
        for i in 0..7u8 {
            let user = test_uuid(10 + i);
            let s = if i == 0 { sponsor } else { test_uuid(10) };
            let result = engine.add_member(user, s, 2000 + i as i64).unwrap();
            all_cycle_events.extend(result.cycle_events);
        }

        // A full 2x2 board triggers at least one cycle event.
        assert!(
            !all_cycle_events.is_empty(),
            "filling a board should produce at least one cycle event"
        );

        // Calculate commissions from the cycle events.
        let period_counts = HashMap::new();
        let result = calculate_board_commissions(&all_cycle_events, &period_counts, &config);

        // Each cycle event should produce an earning.
        assert_eq!(
            result.earnings.len(),
            all_cycle_events.len(),
            "one earning per cycle event"
        );

        for earning in &result.earnings {
            assert_eq!(
                earning.dollar_amount, 500.0,
                "each earning should match cycle_commission"
            );
            assert!(
                !earning.capped,
                "earnings should not be capped within max_cycles_per_period"
            );
        }

        // Cycle counts should be updated for each member who cycled.
        assert!(
            !result.updated_cycle_counts.is_empty(),
            "cycle counts should track cycled members"
        );
    }

    #[test]
    fn engine_snapshot_round_trip() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        let sponsor = test_uuid(1);

        // First member bootstraps the sponsor relationship.
        engine.add_member(test_uuid(10), sponsor, 2000).unwrap();
        // Subsequent members use the first member as sponsor.
        engine
            .add_member(test_uuid(11), test_uuid(10), 3000)
            .unwrap();
        engine
            .add_member(test_uuid(12), test_uuid(10), 4000)
            .unwrap();

        let json = serde_json::to_string(&engine).unwrap();
        let restored: BoardPlanEngine = serde_json::from_str(&json).unwrap();

        // Verify dimensions are preserved.
        assert_eq!(restored.dimensions(), (2, 2));

        // Verify board count is preserved.
        assert_eq!(restored.board_count(), engine.board_count());

        // Verify member boards are preserved.
        assert!(restored.get_member_board(test_uuid(10)).is_some());
        assert!(restored.get_member_board(test_uuid(11)).is_some());
        assert!(restored.get_member_board(test_uuid(12)).is_some());
        assert!(restored.get_member_board(test_uuid(99)).is_none());

        // Verify board content is the same.
        let original_boards = engine.list_boards();
        let restored_boards = restored.list_boards();
        assert_eq!(original_boards.len(), restored_boards.len());
        for (orig, rest) in original_boards.iter().zip(restored_boards.iter()) {
            assert_eq!(orig.id, rest.id);
            assert_eq!(orig.filled_count, rest.filled_count);
            assert_eq!(orig.total_positions, rest.total_positions);
        }
    }
}
