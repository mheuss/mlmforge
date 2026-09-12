//! Consistency checks for a deserialized engine.
//!
//! A restored engine is built by a caller, not by this crate's own
//! constructors, so nothing here has enforced the invariants they maintain.

use thiserror::Error;
use uuid::Uuid;

/// A restored structure whose stored references disagree with what they name.
#[derive(Debug, PartialEq, Error)]
pub enum SnapshotConsistencyError {
    #[error("the index maps {user_id} to node slot {slot}; the arena holds {node_count} slots")]
    IndexSlotOutOfRange {
        user_id: Uuid,
        slot: usize,
        node_count: usize,
    },

    #[error("the index maps {user_id} to node slot {slot}; that slot holds {found}")]
    IndexSlotMismatch {
        user_id: Uuid,
        slot: usize,
        found: Uuid,
    },

    #[error("the index maps {user_id} to node slot {slot}; that slot is a tombstone")]
    IndexSlotTombstoned { user_id: Uuid, slot: usize },

    #[error("node slot {slot} holds {user_id}; the index does not map that user to this slot")]
    NodeNotIndexed { slot: usize, user_id: Uuid },

    #[error(
        "{field} on node slot {slot} names node slot {target}; the arena holds {node_count} slots"
    )]
    EdgeOutOfRange {
        field: &'static str,
        slot: usize,
        target: usize,
        node_count: usize,
    },

    #[error("{field} on node slot {slot} names node slot {target}; that slot is a tombstone")]
    EdgeTombstoned {
        field: &'static str,
        slot: usize,
        target: usize,
    },

    #[error("the free list names node slot {slot}; the arena holds {node_count} slots")]
    FreeSlotOutOfRange { slot: usize, node_count: usize },

    #[error("the free list names node slot {slot}; that slot holds {found}")]
    FreeSlotNotTombstoned { slot: usize, found: Uuid },

    #[error("the free list names node slot {slot} more than once")]
    FreeSlotRepeated { slot: usize },

    #[error("node slot {slot} is a tombstone and still holds a {field}")]
    TombstoneNotCleared { slot: usize, field: &'static str },

    #[error(
        "the engine will allocate stream {next_stream_id} next; it already holds stream {highest_stream_id}"
    )]
    StreamIdCursorNotPastEnd {
        next_stream_id: u32,
        highest_stream_id: u32,
    },

    #[error(
        "the engine will allocate stream {next_stream_id} next; allocating it leaves no id for the stream after it"
    )]
    StreamIdCursorExhausted { next_stream_id: u32 },

    #[error("node slot {slot} holds {user_id} at depth {depth} and names no parent")]
    NodeDepthNotZero {
        slot: usize,
        user_id: Uuid,
        depth: u32,
    },

    #[error(
        "node slot {slot} holds {user_id} at depth {depth}; its parent at node slot {parent_slot} is at depth {parent_depth}"
    )]
    NodeDepthMismatch {
        slot: usize,
        user_id: Uuid,
        depth: u32,
        parent_slot: usize,
        parent_depth: u32,
    },

    #[error("root names node slot {slot}; the arena holds {node_count} slots")]
    RootOutOfRange { slot: usize, node_count: usize },

    #[error("root names node slot {slot}; that slot is a tombstone")]
    RootTombstoned { slot: usize },

    #[error("the child slot map names node slot {slot}; the arena holds {node_count} slots")]
    ChildSlotOutOfRange { slot: usize, node_count: usize },

    #[error("the child slot map names node slot {slot}; that slot is a tombstone")]
    ChildSlotTombstoned { slot: usize },

    #[error("node slot {slot} holds {user_id}; no parent's child slots name it")]
    LiveNodeNotSlotted { slot: usize, user_id: Uuid },

    #[error("node slot {parent} names node slot {slot} in more than one child slot")]
    ChildSlottedTwiceUnderOneParent { slot: usize, parent: usize },

    #[error(
        "node slots {first_parent} and {second_parent} both name node slot {slot} in a child slot; \
         {parent_count} node slots name it"
    )]
    ChildSlotRepeated {
        slot: usize,
        first_parent: usize,
        second_parent: usize,
        parent_count: usize,
    },

    #[error("node slot {parent} names the root in one of its child slots")]
    RootSlottedAsChild { parent: usize },

    #[error("node slot {slot} names itself in one of its own child slots")]
    NodeSlottedUnderItself { slot: usize },

    #[error("node slot {slot} holds {user_id}; the child slot map has no entry for it")]
    SlotEntryMissing { slot: usize, user_id: Uuid },

    #[error(
        "the child slot map gives node slot {slot} {found} child slots; the tree width is {width}"
    )]
    ChildSlotWidthMismatch {
        slot: usize,
        found: usize,
        width: u8,
    },

    #[error("the tree declares a width of {width}; a matrix width is at least 2")]
    MatrixWidthTooSmall { width: u8 },

    #[error("the tree declares depth-first spillover; this engine places breadth-first")]
    MatrixSpilloverUnsupported,

    #[error(
        "the engine declares a width of {width} and a height of {height}; a board plan width is 2 to 5 and a height is 1 to 4"
    )]
    BoardDimensionsOutOfRange { width: u8, height: u8 },

    #[error(
        "the engine's board size is {found}; a width of {width} and a height of {height} give {expected}"
    )]
    BoardTotalPositionsMismatch {
        found: usize,
        width: u8,
        height: u8,
        expected: usize,
    },

    #[error("the holding tank lists {user_id} more than once")]
    HoldingTankUserRepeated { user_id: Uuid },

    #[error("the holding tank names {user_id}; that user is also placed at node slot {slot}")]
    HoldingTankUserPlaced { user_id: Uuid, slot: usize },

    #[error("board {board_id} holds {found} positions; the engine's board size is {expected}")]
    BoardPositionCountMismatch {
        board_id: Uuid,
        found: usize,
        expected: usize,
    },

    #[error("the boards map files board {board_id} under key {key}")]
    BoardIdMismatch { key: Uuid, board_id: Uuid },

    #[error("board {board_id} seats {user_id}; member_boards does not put them on that board")]
    OccupantNotIndexed { user_id: Uuid, board_id: Uuid },

    #[error("board {board_id} seats {user_id} in more than one position")]
    MemberSeatedTwiceOnBoard { user_id: Uuid, board_id: Uuid },

    #[error("displaced_members lists {user_id}; member_boards puts them on board {board_id}")]
    MemberDisplacedAndSeated { user_id: Uuid, board_id: Uuid },

    #[error("displaced_members lists {user_id} more than once")]
    MemberDisplacedTwice { user_id: Uuid },

    #[error("{user_id} occupies a position on board {first_board} and on board {second_board}")]
    MemberOnTwoBoards {
        user_id: Uuid,
        first_board: Uuid,
        second_board: Uuid,
    },

    #[error("member_boards puts {user_id} on board {board_id}; the engine holds no such board")]
    BoardAbsent { user_id: Uuid, board_id: Uuid },

    #[error(
        "member_boards puts {user_id} on board {board_id}; that board's positions do not hold them"
    )]
    MemberNotOnBoard { user_id: Uuid, board_id: Uuid },

    #[error("user_streams lists no streams for {user_id}")]
    UserStreamsEntryEmpty { user_id: Uuid },

    #[error("user_streams lists stream {stream_id} for {user_id}; the engine holds no such stream")]
    StreamAbsent { user_id: Uuid, stream_id: u32 },

    #[error(
        "user_streams lists stream {stream_id} for {user_id}; that stream's tree does not hold them"
    )]
    UserNotInStreamTree { user_id: Uuid, stream_id: u32 },

    #[error(
        "stream_owners lists stream {stream_id} for {user_id}; the engine holds no such stream"
    )]
    OwnerStreamAbsent { user_id: Uuid, stream_id: u32 },

    #[error(
        "stream_owners lists stream {stream_id} for {user_id}; that stream's owner is {owner_id}"
    )]
    StreamOwnerMismatch {
        user_id: Uuid,
        stream_id: u32,
        owner_id: Uuid,
    },

    #[error("the streams map files stream {stream_id} under key {key}")]
    StreamIdMismatch { key: u32, stream_id: u32 },

    #[error(
        "stream {stream_id} names {user_id} as its bottom; that stream's tree does not hold them"
    )]
    StreamBottomNotInTree { stream_id: u32, user_id: Uuid },

    #[error("stream {stream_id} names no bottom; that stream's tree holds {node_count} users")]
    StreamBottomMissing { stream_id: u32, node_count: usize },

    #[error("stream {stream_id}'s tree holds {user_id}; user_streams has no entry for that stream")]
    TreeUserNotIndexed { stream_id: u32, user_id: Uuid },

    #[error("stream {stream_id} is inconsistent: {source}")]
    Stream {
        stream_id: u32,
        #[source]
        source: Box<SnapshotConsistencyError>,
    },
}
