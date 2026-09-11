//! Consistency checks for a deserialized engine.
//!
//! `restore_snapshot` accepts an engine built by a caller rather than by this
//! crate, so nothing has enforced the invariants the constructors maintain.

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

    #[error("node slot {slot} holds {user_id}; the index has no entry for it")]
    NodeNotIndexed { slot: usize, user_id: Uuid },

    #[error("{field} on node slot {slot} names node slot {target}; the arena holds {node_count} slots")]
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

    #[error("root names node slot {slot}; the arena holds {node_count} slots")]
    RootOutOfRange { slot: usize, node_count: usize },

    #[error("root names node slot {slot}; that slot is a tombstone")]
    RootTombstoned { slot: usize },

    #[error("the child slot map names node slot {slot}; the arena holds {node_count} slots")]
    ChildSlotOutOfRange { slot: usize, node_count: usize },

    #[error("the child slot map names node slot {slot}; that slot is a tombstone")]
    ChildSlotTombstoned { slot: usize },

    #[error("node slot {slot} holds {user_id}; the child slot map has no entry for it")]
    SlotEntryMissing { slot: usize, user_id: Uuid },

    #[error("the child slot map gives node slot {slot} {found} child slots; the tree width is {width}")]
    ChildSlotWidthMismatch {
        slot: usize,
        found: usize,
        width: u8,
    },

    #[error("the holding tank names {user_id}; that user is also placed at node slot {slot}")]
    HoldingTankUserPlaced { user_id: Uuid, slot: usize },

    #[error("member_boards puts {user_id} on board {board_id}; the engine holds no such board")]
    BoardAbsent { user_id: Uuid, board_id: Uuid },

    #[error("member_boards puts {user_id} on board {board_id}; that board's positions do not hold them")]
    MemberNotOnBoard { user_id: Uuid, board_id: Uuid },

    #[error("user_streams lists stream {stream_id} for {user_id}; the engine holds no such stream")]
    StreamAbsent { user_id: Uuid, stream_id: u32 },

    #[error("user_streams lists stream {stream_id} for {user_id}; that stream's tree does not hold them")]
    UserNotInStreamTree { user_id: Uuid, stream_id: u32 },

    #[error("stream_owners lists stream {stream_id} for {user_id}; the engine holds no such stream")]
    OwnerStreamAbsent { user_id: Uuid, stream_id: u32 },

    #[error("stream_owners lists stream {stream_id} for {user_id}; that stream's owner is {owner_id}")]
    StreamOwnerMismatch {
        user_id: Uuid,
        stream_id: u32,
        owner_id: Uuid,
    },

    #[error("stream {stream_id} is inconsistent: {source}")]
    Stream {
        stream_id: u32,
        #[source]
        source: Box<SnapshotConsistencyError>,
    },
}
