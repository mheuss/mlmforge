//! Board plan cycling configuration types.
//!
//! Board plans use small fixed-size matrices that cycle. When a board
//! fills, the top position cycles out and earns a fixed commission.
//! Re-entry rules control whether cycled-out members rejoin new boards.
//! Cascade limits prevent runaway chain reactions from a single add.

use serde::{Deserialize, Serialize};

/// Board plan cycling configuration.
///
/// Controls cycling behavior for board plan structures: commission amount,
/// re-entry rules, cascade limits, and stall detection thresholds.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoardPlanConfig {
    /// Fixed dollar amount earned when cycling out of a board.
    pub cycle_commission: f64,

    /// Whether cycled-out members automatically re-enter a new board.
    pub re_entry_enabled: bool,

    /// Where re-entered members are placed.
    pub re_entry_position: ReEntryPosition,

    /// Maximum times a member can earn cycle commission per period.
    pub max_cycles_per_period: u32,

    /// Maximum chained cycles from a single add_member operation.
    #[serde(default = "default_max_cascade_depth")]
    pub max_cascade_depth: u32,

    /// Stall threshold in periods. Go converts to timestamp at detection time.
    pub stall_threshold_periods: u32,

    /// Whether inactive members are compressed out of boards.
    pub inactive_compression: bool,
}

/// Where a cycled-out member re-enters.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ReEntryPosition {
    /// Placed in the oldest board with an open slot.
    Bottom,
    /// Placed in the sponsor's current board. Falls back to Bottom if full.
    SponsorBoard,
}

fn default_max_cascade_depth() -> u32 {
    10
}
