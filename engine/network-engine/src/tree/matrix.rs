use std::collections::HashMap;
use uuid::Uuid;

use super::arena::Arena;
use super::node::NodeIndex;
use crate::config::matrix::SpilloverDirection;

/// Entry in the holding tank for nodes removed via HoldingTank pruning
/// or awaiting manual placement.
#[derive(Debug, Clone)]
#[allow(dead_code)] // Used by later implementation tasks.
pub struct HoldingTankEntry {
    pub user_id: Uuid,
    pub sponsor: Option<NodeIndex>,
    pub enrolled_at: i64,
}

/// Result of a remove_node operation, describing what changed.
#[derive(Debug)]
#[allow(dead_code)] // Used by later implementation tasks.
pub struct RemovalResult {
    pub removed: Uuid,
    pub promoted: Option<Uuid>,
    pub repositioned: Vec<Uuid>,
    pub moved_to_tank: Vec<Uuid>,
}

/// Pruning mode for matrix node removal.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[allow(dead_code)] // Used by later implementation tasks.
pub enum PruningMode {
    PromoteEarliest,
    HoldingTank,
}

/// Arena-backed matrix tree with fixed-width positional placement.
///
/// Each node has exactly `width` child slots (0..width-1).
/// Placement is either automatic (breadth-first spillover within
/// the sponsor's subtree) or explicit (admin override).
/// Depth is unlimited. Width is immutable after construction.
#[allow(dead_code)] // Fields used by later implementation tasks.
pub struct MatrixTree {
    arena: Arena,
    width: u8,
    spillover: SpilloverDirection,
    slots: HashMap<NodeIndex, Vec<Option<NodeIndex>>>,
    holding_tank: Vec<HoldingTankEntry>,
}
