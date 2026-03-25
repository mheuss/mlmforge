//! Result and event types for board plan operations.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use uuid::Uuid;

/// Result of adding a member to a board.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AddMemberResult {
    /// The board the member was placed on.
    pub board_id: Uuid,

    /// Position index within the board.
    pub position: usize,

    /// Cycle events triggered by the placement (if the board filled).
    pub cycle_events: Vec<CycleEvent>,
}

/// A single cycle event. Produced when a board fills and the top
/// position cycles out.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CycleEvent {
    /// The board that cycled.
    pub board_id: Uuid,

    /// The member who cycled out of the top position.
    pub cycled_member: Uuid,

    /// Whether the member earned commission for this cycle.
    pub earned_commission: bool,

    /// New boards created from the split.
    pub new_boards: Vec<Uuid>,

    /// The board the cycled member re-entered, if re-entry is enabled.
    pub re_entry_board: Option<Uuid>,
}

/// Result of removing a member from a board.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RemoveMemberResult {
    /// Members who were compacted (moved up) to fill the gap.
    pub compacted: Vec<Uuid>,

    /// Cycle events triggered by compaction.
    pub cycle_events: Vec<CycleEvent>,
}

/// A board that has not had activity for longer than the stall
/// threshold.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StalledBoard {
    /// The stalled board's identifier.
    pub board_id: Uuid,

    /// Unix timestamp (seconds) of the last activity on this board.
    pub last_activity_at: i64,

    /// Number of occupied positions.
    pub filled_positions: usize,

    /// Total positions in the board.
    pub total_positions: usize,

    /// Members currently on the board, in BFS order.
    pub members: Vec<Uuid>,
}

/// Result of dissolving a stalled board.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DissolutionResult {
    /// The board that was dissolved.
    pub dissolved_board_id: Uuid,

    /// Members displaced by the dissolution.
    pub displaced_members: Vec<Uuid>,
}

/// A member who was compressed out of a board due to inactivity.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CompressedMember {
    /// The member who was compressed.
    pub user_id: Uuid,

    /// The board they were removed from.
    pub board_id: Uuid,
}

/// Result of running inactive compression across all boards.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CompressionResult {
    /// Members who were compressed out of their boards.
    pub compressed: Vec<CompressedMember>,

    /// Cycle events triggered by compaction after compression.
    pub cycle_events: Vec<CycleEvent>,
}

/// Summary view of a board. Used for listing and inspection.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoardSummary {
    /// Board identifier.
    pub id: Uuid,

    /// Number of occupied positions.
    pub filled_count: usize,

    /// Total positions in the board.
    pub total_positions: usize,

    /// Unix timestamp (seconds) when the board was created.
    pub created_at: i64,

    /// Unix timestamp (seconds) of the most recent activity.
    pub last_activity_at: i64,

    /// The board this one was split from, if any.
    pub parent_board_id: Option<Uuid>,
}

/// A single cycle commission earning.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoardCycleEarning {
    /// The member who earned the commission.
    pub earner_id: Uuid,

    /// The board that cycled to produce this earning.
    pub board_id: Uuid,

    /// Dollar amount earned.
    pub dollar_amount: f64,

    /// Which cycle number this is for the member in the current period.
    pub cycle_number: u32,

    /// Whether the earning was capped by max_cycles_per_period.
    pub capped: bool,
}

/// Result of computing board cycle commissions for a period.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoardCommissionResult {
    /// All cycle earnings for the period.
    pub earnings: Vec<BoardCycleEarning>,

    /// Updated cycle counts per member. Keyed by user_id.
    pub updated_cycle_counts: HashMap<Uuid, u32>,
}
