//! Board plan engine — manages all boards for one board plan structure.

use std::collections::{BTreeMap, HashMap, HashSet};

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::config::board_plan::{BoardPlanConfig, ReEntryPosition};
use crate::snapshot::{MAX_RESTORED_BOARD_SEQ, SnapshotConsistencyError};

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

    /// The `seq` the next new board receives.
    next_board_seq: u64,

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
        let initial_board = Board::new(total_positions, timestamp, None, 0);

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
            next_board_seq: 1,
            config,
        })
    }

    /// Prove a restored engine's stored values are in range, its boards
    /// agree with its membership index, and every board fills from position 0.
    ///
    /// Membership is checked in both directions.
    ///
    /// `sponsor_map` is deliberately not checked. It is permanent enrollment
    /// data that keeps removed members so re-entry can still route them, so it
    /// holds users who are on no board by design. Checking it against `boards`
    /// would reject engines this crate itself produces.
    pub fn validate_restored(&self) -> Result<(), SnapshotConsistencyError> {
        // The walks run in order, each depending on the ones before it having returned.
        // Two of those dependencies are load-bearing rather than cosmetic: the
        // key check must precede anything that treats a key as an identity,
        // and the duplicate check must precede the reverse membership walk,
        // which reads a map that holds one board per occupant only because the
        // duplicate check already returned.
        // A restore does not run the constructor, so the range it enforces has
        // to be re-established here.
        if !(2..=5).contains(&self.width) || !(1..=4).contains(&self.height) {
            return Err(SnapshotConsistencyError::BoardDimensionsOutOfRange {
                width: self.width,
                height: self.height,
            });
        }
        // Recomputed, not read. total_positions is cached, so comparing the
        // boards against it would prove only that they agree with the cache.
        let expected = board::total_positions(self.width, self.height);
        if self.total_positions != expected {
            return Err(SnapshotConsistencyError::BoardTotalPositionsMismatch {
                found: self.total_positions,
                width: self.width,
                height: self.height,
                expected,
            });
        }
        // Runs before the walk below, which uses the map key as the board's
        // identity. A fault reported against a key that names no board gives
        // an operator an id they cannot look up.
        let mut key_fault: Option<(Uuid, SnapshotConsistencyError)> = None;
        for (key, board) in &self.boards {
            if *key != board.id && key_fault.as_ref().is_none_or(|(held, _)| key < held) {
                key_fault = Some((
                    *key,
                    SnapshotConsistencyError::BoardIdMismatch {
                        key: *key,
                        board_id: board.id,
                    },
                ));
            }
        }
        if let Some((_, err)) = key_fault {
            return Err(err);
        }

        // Ties break on the lowest board id, since self.boards iterates in
        // random hash order and the offender must not vary between runs.
        let mut placed: HashSet<(Uuid, Uuid)> = HashSet::new();
        let mut sizing: Option<(Uuid, SnapshotConsistencyError)> = None;
        let mut seats: HashMap<Uuid, Uuid> = HashMap::new();
        let mut duplicate: Option<(Uuid, Uuid)> = None;
        for (board_id, board) in &self.boards {
            if board.positions.len() != self.total_positions {
                let err = SnapshotConsistencyError::BoardPositionCountMismatch {
                    board_id: *board_id,
                    found: board.positions.len(),
                    expected: self.total_positions,
                };
                if sizing.as_ref().is_none_or(|(held, _)| board_id < held) {
                    sizing = Some((*board_id, err));
                }
                continue;
            }
            for occupant in board.positions.iter().flatten() {
                placed.insert((*board_id, *occupant));
                if seats.insert(*occupant, *board_id).is_some()
                    && duplicate.is_none_or(|(held, _)| *occupant < held)
                {
                    // The board is carried so the rejection below always has
                    // one to name, even when every repeat is on this board.
                    duplicate = Some((*occupant, *board_id));
                }
            }
        }
        if let Some((_, err)) = sizing {
            return Err(err);
        }
        if let Some((user_id, seen_on)) = duplicate {
            // Names the two lowest boards holding this occupant, not
            // whichever two hash order reached first. Must return; falling
            // through would accept the payload that reached it.
            let mut held: Vec<Uuid> = self
                .boards
                .iter()
                .filter(|(_, board)| board.positions.iter().flatten().any(|o| *o == user_id))
                .map(|(board_id, _)| *board_id)
                .collect();
            held.sort_unstable();
            return match (held.first().copied(), held.get(1).copied()) {
                (Some(first_board), Some(second_board)) => {
                    Err(SnapshotConsistencyError::MemberOnTwoBoards {
                        user_id,
                        first_board,
                        second_board,
                    })
                }
                // Fewer than two boards hold them, so the repeat the walk saw
                // is two positions on the board it saw it on.
                _ => Err(SnapshotConsistencyError::MemberSeatedTwiceOnBoard {
                    user_id,
                    board_id: seen_on,
                }),
            };
        }

        // Lowest user_id wins, since member_boards also iterates in hash order.
        let mut fault: Option<(Uuid, SnapshotConsistencyError)> = None;
        for (user_id, board_id) in &self.member_boards {
            let found = if !self.boards.contains_key(board_id) {
                Some(SnapshotConsistencyError::BoardAbsent {
                    user_id: *user_id,
                    board_id: *board_id,
                })
            } else if !placed.contains(&(*board_id, *user_id)) {
                Some(SnapshotConsistencyError::MemberNotOnBoard {
                    user_id: *user_id,
                    board_id: *board_id,
                })
            } else {
                None
            };
            if let Some(err) = found {
                if fault.as_ref().is_none_or(|(held, _)| user_id < held) {
                    fault = Some((*user_id, err));
                }
            }
        }
        if let Some((_, err)) = fault {
            return Err(err);
        }

        // The reverse of the walk above: every seat must have an index entry
        // pointing back to it.
        let mut unindexed: Option<(Uuid, SnapshotConsistencyError)> = None;
        for (user_id, board_id) in &seats {
            if self.member_boards.get(user_id) != Some(board_id)
                && unindexed.as_ref().is_none_or(|(held, _)| user_id < held)
            {
                unindexed = Some((
                    *user_id,
                    SnapshotConsistencyError::OccupantNotIndexed {
                        user_id: *user_id,
                        board_id: *board_id,
                    },
                ));
            }
        }
        if let Some((_, err)) = unindexed {
            return Err(err);
        }

        // A listed user must hold no seat, and the list must have no repeats.
        // Either one gets that user a second seat on the next enrollment.
        //
        // Returns on the first fault rather than holding the lowest: this is a
        // Vec, so its fixed order always names the same offender first.
        let mut listed: HashSet<Uuid> = HashSet::new();
        for user_id in &self.displaced_members {
            if !listed.insert(*user_id) {
                return Err(SnapshotConsistencyError::MemberDisplacedTwice { user_id: *user_id });
            }
            if let Some(board_id) = self.member_boards.get(user_id) {
                return Err(SnapshotConsistencyError::MemberDisplacedAndSeated {
                    user_id: *user_id,
                    board_id: *board_id,
                });
            }
        }
        // Lowest board id wins: boards iterates in hash order.
        let mut gap: Option<(Uuid, SnapshotConsistencyError)> = None;
        for (board_id, board) in &self.boards {
            let Some(open) = board.first_open_position() else {
                continue;
            };
            let Some(offset) = board.positions[open..].iter().position(Option::is_some) else {
                continue;
            };
            if gap.as_ref().is_none_or(|(held, _)| board_id < held) {
                gap = Some((
                    *board_id,
                    SnapshotConsistencyError::BoardOccupantAboveOpening {
                        board_id: *board_id,
                        open_position: open,
                        occupied_position: open + offset,
                    },
                ));
            }
        }
        if let Some((_, err)) = gap {
            return Err(err);
        }
        if self.next_board_seq > MAX_RESTORED_BOARD_SEQ {
            return Err(SnapshotConsistencyError::BoardSeqCounterExhausted {
                next_board_seq: self.next_board_seq,
            });
        }
        // Lowest board id wins: boards iterates in hash order.
        let mut above: Option<(Uuid, SnapshotConsistencyError)> = None;
        let mut holders: BTreeMap<u64, Vec<Uuid>> = BTreeMap::new();
        for (board_id, board) in &self.boards {
            if board.seq >= self.next_board_seq
                && above.as_ref().is_none_or(|(held, _)| board_id < held)
            {
                above = Some((
                    *board_id,
                    SnapshotConsistencyError::BoardSeqNotBelowCounter {
                        board_id: *board_id,
                        seq: board.seq,
                        next_board_seq: self.next_board_seq,
                    },
                ));
            }
            holders.entry(board.seq).or_default().push(*board_id);
        }
        if let Some((_, err)) = above {
            return Err(err);
        }
        // The lowest repeated seq, named by its two lowest boards.
        if let Some((seq, mut held)) = holders.into_iter().find(|(_, held)| held.len() > 1) {
            held.sort_unstable();
            return Err(SnapshotConsistencyError::BoardSeqRepeated {
                seq,
                first_board: held[0],
                second_board: held[1],
            });
        }
        Ok(())
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

    /// Returns summaries of all boards, ordered by `created_at`, then `seq`.
    pub fn list_boards(&self) -> Vec<BoardSummary> {
        let mut boards: Vec<&Board> = self.boards.values().collect();
        boards.sort_by_key(|b| (b.created_at, b.seq));
        boards
            .into_iter()
            .map(|b| BoardSummary {
                id: b.id,
                filled_count: b.filled_count(),
                total_positions: self.total_positions,
                created_at: b.created_at,
                last_activity_at: b.last_activity_at,
                parent_board_id: b.parent_board_id,
            })
            .collect()
    }

    /// Returns the list of displaced members awaiting reassignment.
    pub fn displaced_members(&self) -> &[Uuid] {
        &self.displaced_members
    }

    /// Adds a member to a board.
    ///
    /// Validates the member is neither placed nor awaiting reassignment, and
    /// that the sponsor exists.
    /// First member is bootstrapped automatically. Displaced members are
    /// placed before the new member. Placement board depends on the
    /// configured `re_entry_position`.
    pub fn add_member(
        &mut self,
        user_id: Uuid,
        sponsor_id: Uuid,
        timestamp: i64,
    ) -> Result<AddMemberResult, BoardPlanError> {
        // Validate: member not already on a board, and not waiting for one.
        // A displaced member is absent from member_boards by design, so
        // checking that map alone let them be seated twice.
        if self.member_boards.contains_key(&user_id) {
            return Err(BoardPlanError::MemberAlreadyExists(user_id));
        }
        if self.displaced_members.contains(&user_id) {
            return Err(BoardPlanError::MemberAwaitingReassignment(user_id));
        }

        // The first member's sponsor is not enrolled, so only later adds
        // check it.
        let bootstrap = self.member_boards.is_empty() && self.sponsor_map.is_empty();
        if !bootstrap
            && !self.member_boards.contains_key(&sponsor_id)
            && !self.displaced_members.contains(&sponsor_id)
        {
            return Err(BoardPlanError::SponsorNotFound(sponsor_id));
        }
        if self.boards.is_empty() {
            return Err(BoardPlanError::NoBoardsAvailable);
        }
        self.sponsor_map.insert(user_id, sponsor_id);

        let mut cycle_events = Vec::new();
        self.drain_full_boards(timestamp, &mut cycle_events);

        // Place displaced members first.
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
    /// position. Boards are compared by `created_at`, then `seq`.
    fn oldest_board_with_opening(&self) -> Result<Uuid, BoardPlanError> {
        self.boards
            .values()
            .filter(|b| b.first_open_position().is_some())
            .min_by_key(|b| (b.created_at, b.seq))
            .map(|b| b.id)
            .ok_or(BoardPlanError::NoBoardsAvailable)
    }

    /// Cycles every board that is full when called, oldest first, each with
    /// a fresh cascade budget.
    fn drain_full_boards(&mut self, timestamp: i64, cycle_events: &mut Vec<CycleEvent>) {
        let mut full: Vec<&Board> = self.boards.values().filter(|b| b.is_full()).collect();
        full.sort_by_key(|b| (b.created_at, b.seq));
        let full: Vec<Uuid> = full.into_iter().map(|b| b.id).collect();
        for board_id in full {
            self.process_cycles(board_id, timestamp, cycle_events, 0);
        }
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

    /// Removes a member from their board and compacts the remaining members
    /// to fill the gap, or takes them out of the displaced pool.
    ///
    /// Cycles every full board first. Returns the members compaction moved
    /// and every cycle event.
    pub fn remove_member(
        &mut self,
        user_id: Uuid,
        timestamp: i64,
    ) -> Result<RemoveMemberResult, BoardPlanError> {
        if !self.member_boards.contains_key(&user_id) && !self.displaced_members.contains(&user_id)
        {
            return Err(BoardPlanError::MemberNotFound(user_id));
        }

        let mut cycle_events = Vec::new();
        self.drain_full_boards(timestamp, &mut cycle_events);
        let compacted = self.take_member_out(user_id, timestamp, &mut cycle_events)?;

        Ok(RemoveMemberResult {
            compacted,
            cycle_events,
        })
    }

    /// Takes a member off their board, or out of the displaced pool, and
    /// returns the members compaction moved.
    ///
    /// Cycles the member's board first if it is full.
    fn take_member_out(
        &mut self,
        user_id: Uuid,
        timestamp: i64,
        cycle_events: &mut Vec<CycleEvent>,
    ) -> Result<Vec<Uuid>, BoardPlanError> {
        let full_board = self
            .member_boards
            .get(&user_id)
            .copied()
            .filter(|board_id| self.boards.get(board_id).is_some_and(Board::is_full));
        if let Some(board_id) = full_board {
            self.process_cycles(board_id, timestamp, cycle_events, 0);
        }

        // Cycling can move the member, so their place is read again.
        let Some(board_id) = self.member_boards.get(&user_id).copied() else {
            self.displaced_members.retain(|m| *m != user_id);
            return Ok(Vec::new());
        };

        let board = self
            .boards
            .get_mut(&board_id)
            .ok_or(BoardPlanError::BoardNotFound(board_id))?;
        if let Some(pos) = board.positions.iter().position(|p| *p == Some(user_id)) {
            board.positions[pos] = None;
        }
        board.last_activity_at = timestamp;
        self.member_boards.remove(&user_id);

        Ok(self.compact_board(board_id))
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
        if depth >= self.config.max_cascade_depth.get() {
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

    /// Creates an empty board carrying the next `seq`.
    fn new_board(&mut self, timestamp: i64, parent_board_id: Option<Uuid>) -> Board {
        let seq = self.next_board_seq;
        self.next_board_seq += 1;
        Board::new(self.total_positions, timestamp, parent_board_id, seq)
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
            let mut new_board = self.new_board(timestamp, Some(board.id));

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

    /// Dissolves a board, moving all its members to the displaced pool, then
    /// cycles every board left full.
    ///
    /// Refuses a board that is full.
    pub fn dissolve_board(
        &mut self,
        board_id: Uuid,
        timestamp: i64,
    ) -> Result<super::types::DissolutionResult, BoardPlanError> {
        if let Some(board) = self.boards.get(&board_id).filter(|b| b.is_full()) {
            return Err(BoardPlanError::BoardFull {
                board_id,
                filled: board.filled_count(),
                total: board.positions.len(),
            });
        }
        let board = self
            .boards
            .remove(&board_id)
            .ok_or(BoardPlanError::BoardNotFound(board_id))?;

        let members = board.members();
        for &member in &members {
            self.member_boards.remove(&member);
            self.displaced_members.push(member);
        }

        let mut cycle_events = Vec::new();
        self.drain_full_boards(timestamp, &mut cycle_events);

        Ok(super::types::DissolutionResult {
            dissolved_board_id: board_id,
            displaced_members: members,
            cycle_events,
        })
    }

    /// Removes inactive members from their boards and compacts the
    /// remaining members.
    ///
    /// Cycles every full board first. Members not on a board when the call
    /// starts are skipped.
    pub fn compress_inactive(
        &mut self,
        inactive_members: Vec<Uuid>,
        timestamp: i64,
    ) -> Result<super::types::CompressionResult, BoardPlanError> {
        let seated: Vec<(Uuid, Uuid)> = inactive_members
            .into_iter()
            .filter_map(|member_id| {
                self.member_boards
                    .get(&member_id)
                    .map(|&board_id| (member_id, board_id))
            })
            .collect();

        let mut cycle_events = Vec::new();
        self.drain_full_boards(timestamp, &mut cycle_events);

        let mut compressed = Vec::new();
        for (member_id, board_id) in seated {
            // A repeated id is already out after its first appearance.
            if !self.member_boards.contains_key(&member_id)
                && !self.displaced_members.contains(&member_id)
            {
                continue;
            }
            self.take_member_out(member_id, timestamp, &mut cycle_events)?;
            compressed.push(super::types::CompressedMember {
                user_id: member_id,
                board_id,
            });
        }

        Ok(super::types::CompressionResult {
            compressed,
            cycle_events,
        })
    }
}

#[cfg(test)]
mod tests {
    use std::num::NonZeroU32;

    use super::*;
    use crate::config::board_plan::ReEntryPosition;

    fn seeded_engine() -> (BoardPlanEngine, Uuid) {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 0).unwrap();
        let member = Uuid::from_bytes([9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.add_member(member, member, 0).unwrap();
        (engine, member)
    }

    #[test]
    fn validate_restored_accepts_a_healthy_engine() {
        let (engine, _) = seeded_engine();

        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn engine_output_holds_the_three_board_invariants_through_a_cycle() {
        // Twelve enrollments on a 2x1 board should split it into two, each
        // seeded with one member and needing two more.
        let mut engine = BoardPlanEngine::new(2, 1, test_config(), 0).unwrap();
        let first = Uuid::from_bytes([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.add_member(first, first, 1).unwrap();
        for n in 2..=12u8 {
            let m = Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            engine.add_member(m, first, n as i64).unwrap();
        }
        assert!(
            engine.boards.len() > 1,
            "twelve enrollments left {} board(s); a split was expected",
            engine.boards.len()
        );

        assert_board_invariants(&engine);
    }

    /// Asserts the three invariants HEU-750's board criteria depend on.
    fn assert_board_invariants(engine: &BoardPlanEngine) {
        // The validator itself, not just the three checks spelled out below.
        // Without this the guards are never run against engine output, which
        // is the one thing they must never reject.
        assert_eq!(engine.validate_restored(), Ok(()));

        for (key, board) in &engine.boards {
            assert_eq!(*key, board.id, "map key disagrees with board id");
        }

        let mut seen: HashSet<Uuid> = HashSet::new();
        for board in engine.boards.values() {
            for occupant in board.positions.iter().flatten() {
                assert!(seen.insert(*occupant), "{occupant} occupies two boards");
            }
        }

        for (board_id, board) in &engine.boards {
            for occupant in board.positions.iter().flatten() {
                assert_eq!(
                    engine.member_boards.get(occupant),
                    Some(board_id),
                    "{occupant} sits on {board_id} with no member_boards entry pointing back"
                );
            }
        }
    }

    #[test]
    fn engine_output_holds_the_three_board_invariants_through_displacement() {
        // Re-entry is off here, so enrollments leave someone displaced. That
        // is the path that hid a double-seating bug.
        let mut engine = BoardPlanEngine::new(2, 1, test_config_no_reentry(), 0).unwrap();
        let first = Uuid::from_bytes([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.add_member(first, first, 1).unwrap();
        assert_board_invariants(&engine);
        for n in 2..=12u8 {
            let m = Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            engine.add_member(m, first, n as i64).unwrap();
            assert_board_invariants(&engine);
        }
        assert!(
            !engine.displaced_members.is_empty(),
            "re-entry is off, so twelve enrollments should leave someone displaced"
        );

        let board_id = *engine.boards.keys().next().unwrap();
        engine.dissolve_board(board_id, 200).unwrap();
        assert_board_invariants(&engine);

        let displaced = *engine.displaced_members.first().unwrap();
        let sponsor = *engine.member_boards.keys().next().unwrap();
        let _ = engine.add_member(displaced, sponsor, 300);
        assert_board_invariants(&engine);
    }

    #[test]
    fn validate_restored_rejects_a_board_filed_under_the_wrong_key() {
        let (mut engine, member) = seeded_engine();
        let seated = *engine.member_boards.get(&member).unwrap();
        let board = engine.boards.remove(&seated).unwrap();
        let board_id = board.id;
        let wrong_key = Uuid::from_bytes([7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.boards.insert(wrong_key, board);
        engine.member_boards.insert(member, wrong_key);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardIdMismatch {
                key: wrong_key,
                board_id,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_key_offender_every_run() {
        // A fresh engine per iteration. One HashMap repeats its own iteration
        // order, so looping over a single engine proves nothing about hash
        // order and stays green with the lowest-key hold deleted.
        for _ in 0..64 {
            let (mut engine, member) = seeded_engine();
            let seated = *engine.member_boards.get(&member).unwrap();
            let board = engine.boards.remove(&seated).unwrap();
            let low_board_id = board.id;
            let low = Uuid::from_bytes([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            let high = Uuid::from_bytes([2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            let mut second = board.clone();
            second.id = Uuid::from_bytes([3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            engine.boards.insert(low, board);
            engine.boards.insert(high, second);
            engine.member_boards.insert(member, low);

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::BoardIdMismatch {
                    key: low,
                    board_id: low_board_id,
                }),
                "the lowest key should win regardless of hash order"
            );
        }
    }

    /// Enrolls until one member is left awaiting reassignment.
    fn engine_with_a_displaced_member() -> (BoardPlanEngine, Uuid) {
        let mut engine = BoardPlanEngine::new(2, 1, test_config_no_reentry(), 0).unwrap();
        let first = Uuid::from_bytes([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.add_member(first, first, 1).unwrap();
        for n in 2..=8u8 {
            let m = Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            engine.add_member(m, first, n as i64).unwrap();
        }
        let displaced = *engine
            .displaced_members
            .first()
            .expect("eight enrollments with re-entry off leave a displaced member");
        (engine, displaced)
    }

    #[test]
    fn add_member_rejects_a_member_awaiting_reassignment() {
        // A displaced member is absent from member_boards by design, so the
        // duplicate check alone missed them, letting them be seated twice.
        let (mut engine, displaced) = engine_with_a_displaced_member();
        let sponsor = *engine.member_boards.keys().next().unwrap();

        assert_eq!(
            engine.add_member(displaced, sponsor, 100).unwrap_err(),
            BoardPlanError::MemberAwaitingReassignment(displaced)
        );
    }

    #[test]
    fn reassigning_a_displaced_member_leaves_the_engine_restorable() {
        // The supported path. A new enrollment drains the displaced pool
        // first, so the waiting member is seated by someone else's join.
        let (mut engine, displaced) = engine_with_a_displaced_member();
        assert_eq!(engine.validate_restored(), Ok(()));
        let sponsor = *engine.member_boards.keys().next().unwrap();
        let newcomer = Uuid::from_bytes([0xF1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);

        engine.add_member(newcomer, sponsor, 100).unwrap();

        assert!(
            !engine.displaced_members.contains(&displaced),
            "the enrollment should have drained {displaced} from the pool"
        );
        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_rejects_a_displaced_user_who_is_also_seated() {
        // A displaced user who is also seated is seated again on the next
        // enrollment, leaving the original seat an occupant nothing names.
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        engine.displaced_members.push(member);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::MemberDisplacedAndSeated {
                user_id: member,
                board_id,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_user_displaced_twice() {
        // A repeat must be rejected before it can seat the same user twice.
        let (mut engine, _) = seeded_engine();
        let waiting = Uuid::from_bytes([0xEE, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.displaced_members.push(waiting);
        engine.displaced_members.push(waiting);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::MemberDisplacedTwice { user_id: waiting })
        );
    }

    #[test]
    fn validate_restored_accepts_a_displaced_user_who_holds_no_seat() {
        // A displaced user holding no seat is valid engine output. Rejecting
        // it would reject snapshots this crate produces.
        let (mut engine, _) = seeded_engine();
        let waiting = Uuid::from_bytes([0xEE, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.displaced_members.push(waiting);

        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_rejects_a_user_seated_twice_on_one_board() {
        // Two positions, one enrollment: the same harm as two boards, reached
        // without a second board. The index maps back correctly from both.
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        engine.boards.get_mut(&board_id).unwrap().positions[1] = Some(member);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::MemberSeatedTwiceOnBoard {
                user_id: member,
                board_id,
            })
        );
    }

    #[test]
    fn validate_restored_does_not_let_a_same_board_repeat_mask_a_two_board_one() {
        // A repeat on one board and a second occupant on two must both be
        // caught. Holding only the lowest id must not drop either fault.
        // Fresh engine per iteration, since self.boards' hash order decides
        // which board is recorded last.
        for _ in 0..64 {
            let (mut engine, member) = seeded_engine();
            let board_id = *engine.member_boards.get(&member).unwrap();
            engine.boards.get_mut(&board_id).unwrap().positions[1] = Some(member);

            let higher = Uuid::from_bytes([0xCC, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            assert!(member < higher, "the seeded member must be the lower id");
            engine.boards.get_mut(&board_id).unwrap().positions[2] = Some(higher);
            engine.member_boards.insert(higher, board_id);
            let mut spare = engine.boards.get(&board_id).unwrap().clone();
            let spare_id = Uuid::from_bytes([0xDD, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            spare.id = spare_id;
            spare.positions.iter_mut().for_each(|p| *p = None);
            spare.positions[0] = Some(higher);
            engine.boards.insert(spare_id, spare);

            // Whichever fault is named, the engine must never be accepted.
            assert!(
                engine.validate_restored().is_err(),
                "a double seat and a two-board occupant must not both be dropped"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_a_user_seated_on_two_boards() {
        let (mut engine, member) = seeded_engine();
        let first = *engine.member_boards.get(&member).unwrap();
        let mut clone = engine.boards.get(&first).unwrap().clone();
        let second = Uuid::from_bytes([4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        clone.id = second;
        engine.boards.insert(second, clone);
        let (low, high) = if first < second {
            (first, second)
        } else {
            (second, first)
        };

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::MemberOnTwoBoards {
                user_id: member,
                first_board: low,
                second_board: high,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_two_board_offender_every_run() {
        // A fresh engine per iteration, for the reason in the key test above.
        // Two duplicated occupants, so the lowest-user_id hold has something
        // to choose between. The higher id sits at the later position, so a
        // hold that simply overwrote would name it and lose.
        for _ in 0..64 {
            let (mut engine, member) = seeded_engine();
            let first = *engine.member_boards.get(&member).unwrap();
            let other = Uuid::from_bytes([0xAA, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            assert!(member < other, "the seeded member must be the lower id");
            engine.boards.get_mut(&first).unwrap().positions[1] = Some(other);
            engine.member_boards.insert(other, first);

            let mut clone = engine.boards.get(&first).unwrap().clone();
            let second = Uuid::from_bytes([4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            clone.id = second;
            engine.boards.insert(second, clone);
            let (low, high) = if first < second {
                (first, second)
            } else {
                (second, first)
            };

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::MemberOnTwoBoards {
                    user_id: member,
                    first_board: low,
                    second_board: high,
                }),
                "the lowest user_id should win, with its board ids sorted"
            );
        }
    }

    #[test]
    fn validate_restored_names_the_same_pair_when_a_user_holds_three_boards() {
        // seats overwrote, so the pair named was whichever two boards hash
        // order reached first. The lowest two must win instead.
        for _ in 0..64 {
            let (mut engine, member) = seeded_engine();
            let seated = *engine.member_boards.get(&member).unwrap();
            let board = engine.boards.remove(&seated).unwrap();
            let ids = [
                Uuid::from_bytes([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]),
                Uuid::from_bytes([2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]),
                Uuid::from_bytes([3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]),
            ];
            for id in ids {
                let mut copy = board.clone();
                copy.id = id;
                engine.boards.insert(id, copy);
            }
            engine.member_boards.insert(member, ids[0]);

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::MemberOnTwoBoards {
                    user_id: member,
                    first_board: ids[0],
                    second_board: ids[1],
                }),
                "the two lowest board ids should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_a_board_whose_position_count_disagrees() {
        // The forged shape that reaches split_board's indexing: the board
        // agrees with member_boards, so the membership checks pass, and only
        // the vector length is wrong.
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        engine
            .boards
            .get_mut(&board_id)
            .unwrap()
            .positions
            .truncate(2);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardPositionCountMismatch {
                board_id,
                found: 2,
                expected: 7,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_dimensions_outside_the_constructor_range() {
        let (mut engine, _) = seeded_engine();
        engine.width = 9;
        engine.height = 9;

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardDimensionsOutOfRange {
                width: 9,
                height: 9,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_cached_board_size_the_geometry_denies() {
        // The boards agree with the cached value, so the per-board check
        // passes. Only recomputing from width and height catches it. A board
        // restored this way cycles early and pays early.
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        engine.total_positions = 3;
        engine
            .boards
            .get_mut(&board_id)
            .unwrap()
            .positions
            .truncate(3);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardTotalPositionsMismatch {
                found: 3,
                width: 2,
                height: 2,
                expected: 7,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_board_offender_every_run() {
        // self.boards is a HashMap too. A corrupted payload rarely damages
        // exactly one board, so this walk needs the same lowest-key hold as
        // the member walk below.
        for _ in 0..64 {
            let (mut engine, member) = seeded_engine();
            let seated = *engine.member_boards.get(&member).unwrap();
            let low = Uuid::from_bytes([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            let high = Uuid::from_bytes([4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            for id in [low, high] {
                let mut board = engine.boards.get(&seated).unwrap().clone();
                board.id = id;
                board.positions.truncate(3);
                engine.boards.insert(id, board);
            }

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::BoardPositionCountMismatch {
                    board_id: low,
                    found: 3,
                    expected: 7,
                }),
                "the lowest board id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_names_the_same_member_offender_every_run() {
        // member_boards is a HashMap with a randomized hasher. Fresh engine
        // each iteration, so each gets its own seed.
        for _ in 0..64 {
            let (mut engine, _) = seeded_engine();
            let low = Uuid::from_bytes([2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            let high = Uuid::from_bytes([3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            let ghost = Uuid::from_bytes([7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            engine.member_boards.insert(high, ghost);
            engine.member_boards.insert(low, ghost);

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::BoardAbsent {
                    user_id: low,
                    board_id: ghost,
                }),
                "the lowest user_id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_a_member_board_that_is_absent() {
        let (mut engine, member) = seeded_engine();
        let ghost = Uuid::from_bytes([7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.member_boards.insert(member, ghost);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardAbsent {
                user_id: member,
                board_id: ghost,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_member_the_board_does_not_hold() {
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        let board = engine.boards.get_mut(&board_id).unwrap();
        for position in board.positions.iter_mut() {
            *position = None;
        }

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::MemberNotOnBoard {
                user_id: member,
                board_id,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_an_occupant_the_index_does_not_name() {
        // add_member treats a seat with no index entry as a new enrollment and
        // seats them again, so both positions cycle off one enrollment.
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        engine.member_boards.remove(&member);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::OccupantNotIndexed {
                user_id: member,
                board_id,
            })
        );
    }

    #[test]
    fn validate_restored_still_reports_the_forward_direction_first() {
        // The index names a board the member does not sit on. The forward walk
        // owns that case, so the reverse walk must not take it over.
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        let elsewhere = Uuid::from_bytes([5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        let mut spare = engine.boards.get(&board_id).unwrap().clone();
        spare.id = elsewhere;
        spare.positions.iter_mut().for_each(|p| *p = None);
        engine.boards.insert(elsewhere, spare);
        engine.member_boards.insert(member, elsewhere);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::MemberNotOnBoard {
                user_id: member,
                board_id: elsewhere,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_unindexed_occupant_every_run() {
        // Two unindexed occupants, so the hold has something to choose
        // between. The walk order varies per call, so a hold that overwrote
        // would name the higher id on some of these runs.
        for _ in 0..64 {
            let (mut engine, member) = seeded_engine();
            let board_id = *engine.member_boards.get(&member).unwrap();
            let higher = Uuid::from_bytes([0xBB, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
            assert!(member < higher, "the seeded member must be the lower id");
            engine.boards.get_mut(&board_id).unwrap().positions[1] = Some(higher);
            engine.member_boards.remove(&member);

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::OccupantNotIndexed {
                    user_id: member,
                    board_id,
                }),
                "the lowest user_id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_a_member_placed_on_a_different_board() {
        // The membership key is (board, user), not user alone. A member who
        // sits on some board but not the one member_boards names is still a
        // contradiction, and a user-only check would accept it.
        let (mut engine, member) = seeded_engine();
        let their_board = *engine.member_boards.get(&member).unwrap();
        let other_id = Uuid::from_bytes([6, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        let mut other = engine.boards.get(&their_board).unwrap().clone();
        other.id = other_id;
        engine.boards.insert(other_id, other);
        for position in engine
            .boards
            .get_mut(&their_board)
            .unwrap()
            .positions
            .iter_mut()
        {
            *position = None;
        }

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::MemberNotOnBoard {
                user_id: member,
                board_id: their_board,
            })
        );
    }

    #[test]
    fn validate_restored_accepts_a_sponsor_map_entry_for_an_unplaced_user() {
        // sponsor_map keeps removed members for re-entry routing, so an entry
        // naming nobody on any board is correct state, not a fault. A check
        // over sponsor_map would reject engines this crate itself produces.
        let (mut engine, member) = seeded_engine();
        let departed = Uuid::from_bytes([8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF]);
        engine.sponsor_map.insert(departed, member);

        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_rejects_an_occupant_above_an_open_position() {
        let mut engine = BoardPlanEngine::new(2, 1, test_config(), 0).unwrap();
        engine.add_member(test_uuid(1), test_uuid(1), 1).unwrap();
        engine.add_member(test_uuid(2), test_uuid(1), 2).unwrap();
        let board_id = *engine.member_boards.get(&test_uuid(1)).unwrap();
        engine.boards.get_mut(&board_id).unwrap().positions =
            vec![None, Some(test_uuid(1)), Some(test_uuid(2))];

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardOccupantAboveOpening {
                board_id,
                open_position: 0,
                occupied_position: 1,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_gapped_board_every_run() {
        for _ in 0..64 {
            let (mut engine, _) = seeded_engine();
            let template = engine.boards.values().next().unwrap().clone();
            for (id, occupant) in [(test_uuid(1), test_uuid(11)), (test_uuid(4), test_uuid(14))] {
                let mut board = template.clone();
                board.id = id;
                board.positions = vec![None; 7];
                board.positions[2] = Some(occupant);
                engine.boards.insert(id, board);
                engine.member_boards.insert(occupant, id);
            }

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::BoardOccupantAboveOpening {
                    board_id: test_uuid(1),
                    open_position: 0,
                    occupied_position: 2,
                }),
                "the lowest board id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_accepts_a_full_board() {
        let mut engine = BoardPlanEngine::new(2, 1, test_config(), 0).unwrap();
        engine.add_member(test_uuid(1), test_uuid(1), 1).unwrap();
        engine.add_member(test_uuid(2), test_uuid(1), 2).unwrap();
        let board_id = *engine.member_boards.get(&test_uuid(1)).unwrap();
        engine.boards.get_mut(&board_id).unwrap().positions[2] = Some(test_uuid(3));
        engine.member_boards.insert(test_uuid(3), board_id);

        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_rejects_a_repeated_board_seq() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();
        let (_, last) = fill_2x2_board(&mut engine);
        let first = last.cycle_events[0].new_boards[0];
        let second = last.cycle_events[0].new_boards[1];
        let seq = engine.boards[&first].seq;
        engine.boards.get_mut(&second).unwrap().seq = seq;

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardSeqRepeated {
                seq,
                first_board: first.min(second),
                second_board: first.max(second),
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_board_seq_at_the_counter() {
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        let counter = engine.next_board_seq;
        engine.boards.get_mut(&board_id).unwrap().seq = counter;

        let err = engine.validate_restored().unwrap_err();

        assert_eq!(
            err,
            SnapshotConsistencyError::BoardSeqNotBelowCounter {
                board_id,
                seq: counter,
                next_board_seq: counter,
            }
        );
        assert_eq!(
            err.to_string(),
            format!(
                "board {board_id} carries seq {counter}; the engine will assign seq {counter} next"
            )
        );
    }

    #[test]
    fn validate_restored_rejects_a_board_seq_above_the_counter() {
        let (mut engine, member) = seeded_engine();
        let board_id = *engine.member_boards.get(&member).unwrap();
        let counter = engine.next_board_seq;
        engine.boards.get_mut(&board_id).unwrap().seq = counter + 5;

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::BoardSeqNotBelowCounter {
                board_id,
                seq: counter + 5,
                next_board_seq: counter,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_board_seq_counter_above_half_the_range() {
        let (mut engine, _) = seeded_engine();
        engine.next_board_seq = u64::MAX / 2 + 1;

        let err = engine.validate_restored().unwrap_err();

        assert_eq!(
            err,
            SnapshotConsistencyError::BoardSeqCounterExhausted {
                next_board_seq: u64::MAX / 2 + 1,
            }
        );
        assert_eq!(
            err.to_string(),
            "the engine will assign board seq 9223372036854775808 next; \
             a restored counter above 9223372036854775807 is refused"
        );
    }

    #[test]
    fn validate_restored_accepts_a_board_seq_counter_at_half_the_range() {
        let (mut engine, _) = seeded_engine();
        engine.next_board_seq = u64::MAX / 2;

        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_names_the_same_board_above_the_counter_every_run() {
        for _ in 0..64 {
            let (mut engine, _) = seeded_engine();
            let template = engine.boards.values().next().unwrap().clone();
            let counter = engine.next_board_seq;
            for (id, seq) in [(test_uuid(4), counter), (test_uuid(1), counter + 1)] {
                let mut board = template.clone();
                board.id = id;
                board.positions = vec![None; 7];
                board.seq = seq;
                engine.boards.insert(id, board);
            }

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::BoardSeqNotBelowCounter {
                    board_id: test_uuid(1),
                    seq: counter + 1,
                    next_board_seq: counter,
                }),
                "the lowest board id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_names_the_same_repeated_seq_every_run() {
        for _ in 0..64 {
            let (mut engine, _) = seeded_engine();
            let template = engine.boards.values().next().unwrap().clone();
            engine.next_board_seq = 100;
            for (id, seq) in [
                (test_uuid(4), 7),
                (test_uuid(5), 7),
                (test_uuid(3), 6),
                (test_uuid(2), 6),
                (test_uuid(1), 6),
            ] {
                let mut board = template.clone();
                board.id = id;
                board.positions = vec![None; 7];
                board.seq = seq;
                engine.boards.insert(id, board);
            }

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::BoardSeqRepeated {
                    seq: 6,
                    first_board: test_uuid(1),
                    second_board: test_uuid(2),
                }),
                "the lowest repeated seq and its two lowest boards should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_reports_a_repeated_seq_by_the_boards_it_read() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();
        let (_, last) = fill_2x2_board(&mut engine);
        let first = last.cycle_events[0].new_boards[0];
        let second = last.cycle_events[0].new_boards[1];
        let seq = engine.boards[&first].seq;
        engine.boards.get_mut(&second).unwrap().seq = seq;

        let message = engine.validate_restored().unwrap_err().to_string();

        assert_eq!(
            message,
            format!(
                "boards {} and {} both carry seq {}",
                first.min(second),
                first.max(second),
                seq
            )
        );
    }

    /// Creates a standard BoardPlanConfig for testing.
    fn test_config() -> BoardPlanConfig {
        BoardPlanConfig {
            cycle_commission: 500.0,
            re_entry_enabled: true,
            re_entry_position: ReEntryPosition::Bottom,
            max_cycles_per_period: 4,
            max_cascade_depth: NonZeroU32::new(10).unwrap(),
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

        let board_b = engine.new_board(3000, None);
        let board_a = engine.new_board(2000, None);

        engine.boards.insert(board_b.id, board_b);
        engine.boards.insert(board_a.id, board_a);

        let summaries = engine.list_boards();
        assert_eq!(summaries.len(), 3);
        assert_eq!(summaries[0].created_at, 1000);
        assert_eq!(summaries[1].created_at, 2000);
        assert_eq!(summaries[2].created_at, 3000);
    }

    #[test]
    fn a_split_assigns_seq_in_child_order() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();

        let (_, last) = fill_2x2_board(&mut engine);

        let seqs: Vec<u64> = last.cycle_events[0]
            .new_boards
            .iter()
            .map(|id| engine.get_board(*id).unwrap().seq)
            .collect();
        assert_eq!(
            seqs,
            vec![1, 2],
            "split children's seqs, in new_boards order"
        );
    }

    #[test]
    fn placement_orders_by_created_at_before_seq() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        engine.boards.clear();
        let later = engine.new_board(3000, None);
        let earlier = engine.new_board(2000, None);
        let earlier_id = earlier.id;
        engine.boards.insert(later.id, later);
        engine.boards.insert(earlier_id, earlier);

        assert_eq!(engine.oldest_board_with_opening(), Ok(earlier_id));
    }

    #[test]
    fn the_first_child_board_takes_the_next_placement_after_a_split() {
        // A fresh engine per iteration.
        for _ in 0..64 {
            let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();
            let (_, last) = fill_2x2_board(&mut engine);
            let first_child = last.cycle_events[0].new_boards[0];

            let next = engine
                .add_member(test_uuid(30), test_uuid(11), 5000)
                .unwrap();

            assert_eq!(
                next.board_id, first_child,
                "placement chose a board other than new_boards[0]"
            );
        }
    }

    #[test]
    fn list_boards_orders_boards_from_one_split_by_seq() {
        for _ in 0..64 {
            let mut engine = BoardPlanEngine::new(2, 2, test_config_no_reentry(), 1000).unwrap();
            let (_, last) = fill_2x2_board(&mut engine);

            let listed: Vec<Uuid> = engine.list_boards().iter().map(|s| s.id).collect();

            assert_eq!(listed, last.cycle_events[0].new_boards);
        }
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
            max_cascade_depth: NonZeroU32::new(10).unwrap(),
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
        let fallback_board = engine.new_board(3000, None);
        let fallback_id = fallback_board.id;
        engine.boards.insert(fallback_id, fallback_board);

        // SponsorBoard mode tries sponsor's board first, but it's
        // full, so it falls back to the oldest board with an opening.
        assert_eq!(engine.find_placement_board(sponsor), Ok(fallback_id));
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
    fn remove_member_removes_a_displaced_member() {
        let (mut engine, displaced) = engine_with_a_displaced_member();

        let result = engine.remove_member(displaced, 100).unwrap();

        assert!(result.compacted.is_empty());
        assert!(result.cycle_events.is_empty());
        assert!(!engine.displaced_members().contains(&displaced));
        assert_eq!(engine.get_member_board(displaced), None);
        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn remove_member_cycles_a_pending_board_first() {
        let (mut engine, pending) = engine_with_a_pending_full_board();

        let result = engine.remove_member(test_uuid(5), 6).unwrap();

        assert_eq!(
            result.cycle_events.first().map(|e| e.board_id),
            Some(pending)
        );
        assert_eq!(engine.get_member_board(test_uuid(5)), None);
    }

    #[test]
    fn a_removal_pays_the_members_board_when_the_drain_filled_it() {
        let config = BoardPlanConfig {
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
            ..test_config_sponsor_board()
        };
        let (mut engine, full, open) = engine_with_two_boards(config);

        let result = engine.remove_member(test_uuid(5), 10).unwrap();

        let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
        assert_eq!(cycled, vec![full, open]);
        assert_eq!(engine.get_member_board(test_uuid(5)), None);
        assert!(!engine.displaced_members().contains(&test_uuid(5)));
    }

    #[test]
    fn a_removal_takes_a_member_the_drain_displaced_out_of_the_pool() {
        let (mut engine, full, _) = engine_with_two_boards(test_config_no_reentry());

        let result = engine.remove_member(test_uuid(1), 10).unwrap();

        let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
        assert_eq!(cycled, vec![full]);
        assert_eq!(engine.get_member_board(test_uuid(1)), None);
        assert!(!engine.displaced_members().contains(&test_uuid(1)));
    }

    #[test]
    fn a_drain_cycles_full_boards_oldest_first() {
        // A fresh engine per iteration. One engine repeats its own hash order.
        for _ in 0..64 {
            let (mut engine, ids) = engine_from_rows(
                test_config_no_reentry(),
                &[
                    [Some(1), Some(2), Some(3)],
                    [Some(4), Some(5), Some(6)],
                    [Some(7), None, None],
                ],
                &[(1, 1), (2, 1), (3, 1), (4, 4), (5, 4), (6, 4), (7, 7)],
            );
            // The newer seq gets the older timestamp, so the keys disagree.
            engine.boards.get_mut(&ids[1]).unwrap().created_at = 0;

            let result = engine.remove_member(test_uuid(7), 10).unwrap();

            let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
            assert_eq!(cycled, vec![ids[1], ids[0]]);
        }
    }

    #[test]
    fn each_drained_board_gets_its_own_cascade_budget() {
        let config = BoardPlanConfig {
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
            ..test_config_sponsor_board()
        };
        let (mut engine, ids) = engine_from_rows(
            config,
            &[
                [Some(1), Some(2), Some(3)],
                [Some(4), Some(5), Some(6)],
                [Some(7), Some(8), None],
                [Some(9), Some(10), None],
                [Some(11), None, None],
            ],
            &[
                (1, 7),
                (2, 1),
                (3, 1),
                (4, 9),
                (5, 4),
                (6, 4),
                (7, 7),
                (8, 7),
                (9, 9),
                (10, 9),
                (11, 11),
            ],
        );

        let result = engine.remove_member(test_uuid(11), 10).unwrap();

        let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
        assert_eq!(
            cycled,
            vec![ids[0], ids[1]],
            "both drained boards should cycle at bound 1"
        );
        assert!(engine.get_board(ids[2]).unwrap().is_full());
        assert!(engine.get_board(ids[3]).unwrap().is_full());
    }

    #[test]
    fn a_removal_empties_a_board_the_leaving_members_re_entry_filled() {
        let config = BoardPlanConfig {
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
            ..test_config_sponsor_board()
        };
        let (mut engine, ids) = engine_from_rows(
            config,
            &[
                [Some(1), Some(2), Some(3)],
                [Some(4), Some(5), None],
                [Some(6), Some(7), None],
            ],
            &[(1, 4), (2, 1), (3, 1), (4, 6), (5, 4), (6, 6), (7, 6)],
        );

        let result = engine.remove_member(test_uuid(4), 10).unwrap();

        let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
        assert_eq!(cycled, vec![ids[0], ids[1]]);
        assert_eq!(
            engine.get_board(ids[2]).unwrap().positions,
            vec![Some(test_uuid(6)), Some(test_uuid(7)), None]
        );
    }

    #[test]
    fn a_refused_removal_leaves_a_pending_board_full() {
        let (mut engine, _) = engine_with_a_pending_full_board();
        let before = serde_json::to_value(&engine).unwrap();

        let err = engine.remove_member(test_uuid(99), 6).unwrap_err();

        assert_eq!(err, BoardPlanError::MemberNotFound(test_uuid(99)));
        assert_eq!(serde_json::to_value(&engine).unwrap(), before);
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
            max_cascade_depth: NonZeroU32::new(10).unwrap(),
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
        let sponsor_board = engine.new_board(1500, None);
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
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
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

    /// A 2x1 SponsorBoard engine at cascade bound 1 whose last add left one
    /// board full. Returns the engine and that board's id.
    fn engine_with_a_pending_full_board() -> (BoardPlanEngine, Uuid) {
        let config = BoardPlanConfig {
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
            ..test_config_sponsor_board()
        };
        let mut engine = BoardPlanEngine::new(2, 1, config, 0).unwrap();
        engine.add_member(test_uuid(1), test_uuid(1), 1).unwrap();
        engine.add_member(test_uuid(2), test_uuid(1), 2).unwrap();
        engine.add_member(test_uuid(3), test_uuid(1), 3).unwrap();
        engine.add_member(test_uuid(4), test_uuid(3), 4).unwrap();
        engine.add_member(test_uuid(5), test_uuid(3), 5).unwrap();
        let pending = engine.get_member_board(test_uuid(2)).unwrap();
        assert!(
            engine.get_board(pending).unwrap().is_full(),
            "the cascade bound should have left {pending} full"
        );
        (engine, pending)
    }

    /// A 2x1 engine holding one board per row, oldest first, and `sponsors`
    /// as (member, sponsor) pairs. Returns the engine and the board ids in
    /// row order.
    fn engine_from_rows(
        config: BoardPlanConfig,
        rows: &[[Option<u8>; 3]],
        sponsors: &[(u8, u8)],
    ) -> (BoardPlanEngine, Vec<Uuid>) {
        let mut engine = BoardPlanEngine::new(2, 1, config, 0).unwrap();
        engine.boards.clear();
        let mut ids = Vec::new();
        for (i, row) in rows.iter().enumerate() {
            let mut board = engine.new_board(i as i64 + 1, None);
            board.positions = row.iter().map(|m| m.map(test_uuid)).collect();
            for member in row.iter().flatten() {
                engine.member_boards.insert(test_uuid(*member), board.id);
            }
            ids.push(board.id);
            engine.boards.insert(board.id, board);
        }
        for (member, sponsor) in sponsors {
            engine
                .sponsor_map
                .insert(test_uuid(*member), test_uuid(*sponsor));
        }
        assert_eq!(engine.validate_restored(), Ok(()));
        (engine, ids)
    }

    /// A 2x1 engine holding a full board, `[1, 2, 3]`, and a board with one
    /// opening, `[4, 5, _]`. Member 4 sponsors member 1. Returns the engine,
    /// the full board's id and the open board's id.
    fn engine_with_two_boards(config: BoardPlanConfig) -> (BoardPlanEngine, Uuid, Uuid) {
        let (engine, ids) = engine_from_rows(
            config,
            &[[Some(1), Some(2), Some(3)], [Some(4), Some(5), None]],
            &[(1, 4), (2, 1), (3, 1), (4, 4), (5, 4)],
        );
        (engine, ids[0], ids[1])
    }

    #[test]
    fn an_add_drains_full_boards_oldest_first() {
        for _ in 0..64 {
            let (mut engine, ids) = engine_from_rows(
                test_config_no_reentry(),
                &[
                    [Some(1), Some(2), Some(3)],
                    [Some(4), Some(5), Some(6)],
                    [Some(7), None, None],
                ],
                &[(1, 1), (2, 1), (3, 1), (4, 4), (5, 4), (6, 4), (7, 7)],
            );

            let result = engine.add_member(test_uuid(8), test_uuid(7), 10).unwrap();

            let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
            assert_eq!(cycled[..2], [ids[0], ids[1]]);
        }
    }

    #[test]
    fn a_drain_breaks_a_created_at_tie_by_seq() {
        for _ in 0..64 {
            let (mut engine, ids) = engine_from_rows(
                test_config_no_reentry(),
                &[
                    [Some(1), Some(2), Some(3)],
                    [Some(4), Some(5), Some(6)],
                    [Some(7), None, None],
                ],
                &[(1, 1), (2, 1), (3, 1), (4, 4), (5, 4), (6, 4), (7, 7)],
            );
            engine.boards.get_mut(&ids[1]).unwrap().created_at = 1;

            let result = engine.add_member(test_uuid(8), test_uuid(7), 10).unwrap();

            let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
            assert_eq!(cycled[..2], [ids[0], ids[1]]);
        }
    }

    #[test]
    fn an_add_gives_each_drained_board_its_own_cascade_budget() {
        let config = BoardPlanConfig {
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
            ..test_config_sponsor_board()
        };
        let (mut engine, ids) = engine_from_rows(
            config,
            &[
                [Some(1), Some(2), Some(3)],
                [Some(4), Some(5), Some(6)],
                [Some(7), Some(8), None],
                [Some(9), Some(10), None],
                [Some(11), None, None],
            ],
            &[
                (1, 7),
                (2, 1),
                (3, 1),
                (4, 9),
                (5, 4),
                (6, 4),
                (7, 7),
                (8, 7),
                (9, 9),
                (10, 9),
                (11, 11),
            ],
        );

        let result = engine.add_member(test_uuid(12), test_uuid(11), 10).unwrap();

        let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
        assert_eq!(cycled, vec![ids[0], ids[1]]);
        assert!(
            engine.get_board(ids[1]).is_none(),
            "the second drained board should have cycled"
        );
    }

    #[test]
    fn a_board_left_full_at_the_bound_cycles_on_the_next_add() {
        let (mut engine, pending) = engine_with_a_pending_full_board();

        let result = engine.add_member(test_uuid(6), test_uuid(4), 6).unwrap();

        assert_eq!(
            result
                .cycle_events
                .first()
                .map(|e| (e.board_id, e.cycled_member)),
            Some((pending, test_uuid(2)))
        );
        assert!(
            engine.get_board(pending).is_none(),
            "the pending board should have split"
        );
    }

    #[test]
    fn a_refused_add_leaves_a_pending_board_full() {
        let (mut engine, _) = engine_with_a_pending_full_board();
        let before = serde_json::to_value(&engine).unwrap();

        let err = engine
            .add_member(test_uuid(6), test_uuid(99), 6)
            .unwrap_err();

        assert_eq!(err, BoardPlanError::SponsorNotFound(test_uuid(99)));
        assert_eq!(serde_json::to_value(&engine).unwrap(), before);
    }

    #[test]
    fn an_add_with_no_board_left_is_refused_before_it_changes_anything() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 0).unwrap();
        engine.add_member(test_uuid(1), test_uuid(1), 1).unwrap();
        let board_id = engine.get_member_board(test_uuid(1)).unwrap();
        engine.dissolve_board(board_id, 2).unwrap();
        let before = serde_json::to_value(&engine).unwrap();

        let err = engine
            .add_member(test_uuid(2), test_uuid(1), 3)
            .unwrap_err();

        assert_eq!(err, BoardPlanError::NoBoardsAvailable);
        assert_eq!(serde_json::to_value(&engine).unwrap(), before);
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

    #[test]
    fn dissolve_refuses_a_full_board_and_changes_nothing() {
        let (mut engine, pending) = engine_with_a_pending_full_board();
        let before = serde_json::to_value(&engine).unwrap();

        let err = engine.dissolve_board(pending, 6).unwrap_err();

        assert_eq!(
            err,
            BoardPlanError::BoardFull {
                board_id: pending,
                filled: 3,
                total: 3,
            }
        );
        assert_eq!(
            err.to_string(),
            format!("board {pending} has 3 of 3 positions filled")
        );
        assert_eq!(serde_json::to_value(&engine).unwrap(), before);
    }

    #[test]
    fn a_refused_dissolve_leaves_a_pending_board_full() {
        let (mut engine, _) = engine_with_a_pending_full_board();
        let before = serde_json::to_value(&engine).unwrap();

        let err = engine.dissolve_board(test_uuid(99), 6).unwrap_err();

        assert_eq!(err, BoardPlanError::BoardNotFound(test_uuid(99)));
        assert_eq!(serde_json::to_value(&engine).unwrap(), before);
    }

    #[test]
    fn dissolve_reports_the_dissolution_and_the_drain_after_it() {
        let config = BoardPlanConfig {
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
            ..test_config_sponsor_board()
        };
        let (mut engine, full, open) = engine_with_two_boards(config);

        let result = engine.dissolve_board(open, 10).unwrap();

        assert_eq!(result.dissolved_board_id, open);
        assert_eq!(result.displaced_members, vec![test_uuid(4), test_uuid(5)]);
        let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
        assert_eq!(cycled, vec![full]);
        assert!(
            engine.get_member_board(test_uuid(1)).is_some(),
            "member 1 is on no board after the dissolve"
        );
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

    #[test]
    fn compress_inactive_cycles_a_pending_board_first() {
        let (mut engine, pending) = engine_with_a_pending_full_board();

        let result = engine.compress_inactive(vec![test_uuid(5)], 6).unwrap();

        assert_eq!(
            result.cycle_events.first().map(|e| e.board_id),
            Some(pending)
        );
    }

    #[test]
    fn compress_with_only_unknown_members_still_cycles_a_pending_board() {
        let (mut engine, pending) = engine_with_a_pending_full_board();

        let result = engine.compress_inactive(vec![test_uuid(99)], 6).unwrap();

        assert!(result.compressed.is_empty());
        assert_eq!(
            result.cycle_events.first().map(|e| e.board_id),
            Some(pending)
        );
    }

    #[test]
    fn compress_names_the_starting_board_of_a_member_the_drain_displaced() {
        let (mut engine, full, _) = engine_with_two_boards(test_config_no_reentry());

        let result = engine.compress_inactive(vec![test_uuid(1)], 10).unwrap();

        assert_eq!(result.compressed.len(), 1);
        assert_eq!(result.compressed[0].user_id, test_uuid(1));
        assert_eq!(result.compressed[0].board_id, full);
        assert_eq!(engine.get_member_board(test_uuid(1)), None);
        assert!(!engine.displaced_members().contains(&test_uuid(1)));
    }

    #[test]
    fn compress_drains_once_for_all_its_members() {
        let config = BoardPlanConfig {
            max_cascade_depth: NonZeroU32::new(1).unwrap(),
            ..test_config_sponsor_board()
        };
        let (mut engine, ids) = engine_from_rows(
            config,
            &[
                [Some(1), Some(2), Some(3)],
                [Some(4), Some(5), None],
                [Some(6), Some(7), None],
                [Some(8), None, None],
            ],
            &[
                (1, 4),
                (2, 1),
                (3, 1),
                (4, 6),
                (5, 4),
                (6, 6),
                (7, 6),
                (8, 8),
            ],
        );

        let result = engine
            .compress_inactive(vec![test_uuid(5), test_uuid(8)], 10)
            .unwrap();

        let cycled: Vec<Uuid> = result.cycle_events.iter().map(|e| e.board_id).collect();
        assert_eq!(cycled, vec![ids[0], ids[1]]);
        assert!(
            engine.get_board(ids[2]).unwrap().is_full(),
            "a board this call filled should wait for the next operation"
        );
    }

    #[test]
    fn compress_inactive_takes_a_repeated_member_out_once() {
        let mut engine = BoardPlanEngine::new(2, 2, test_config(), 1000).unwrap();
        engine
            .add_member(test_uuid(10), test_uuid(1), 2000)
            .unwrap();
        engine
            .add_member(test_uuid(11), test_uuid(10), 2001)
            .unwrap();

        let result = engine
            .compress_inactive(vec![test_uuid(11), test_uuid(11)], 3000)
            .unwrap();

        assert_eq!(result.compressed.len(), 1);
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
