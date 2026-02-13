//! Matrix structure configuration types.
//!
//! Matrix plans use a fixed-width, fixed-depth tree. Every position has
//! exactly `width` child slots. The tree cannot grow deeper than `height`
//! levels. New enrollees are placed algorithmically via spillover when
//! their sponsor's direct positions are full.

use serde::{Deserialize, Serialize};

/// Fixed-width, fixed-depth matrix structure parameters.
///
/// Defines the shape of a forced matrix tree. Width controls the maximum
/// children per node. Height controls the maximum depth. Together they
/// determine the theoretical maximum positions: `width ^ height`.
///
/// Spillover fills positions algorithmically when a sponsor's direct
/// slots are full. New recruits "spill" into positions under other
/// distributors, creating excitement from organic growth.
///
/// Width must be >= 2. A width of 1 is a streamline, not a matrix.
/// Warn when `width ^ height` exceeds 1,000,000 theoretical positions.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MatrixStructureParams {
    /// Maximum children per node.
    ///
    /// Every position has exactly this many child slots. Must be >= 2.
    /// A width of 1 is a streamline, not a matrix.
    pub width: u8,

    /// Maximum depth of the matrix tree.
    ///
    /// The tree cannot grow deeper than this. Total theoretical
    /// positions = `width ^ height`. Warn when this exceeds 1,000,000.
    pub height: u8,

    /// How new enrollees are placed when their sponsor's direct
    /// positions are full.
    pub spillover: SpilloverDirection,
}

/// Direction used to fill positions during spillover placement.
///
/// When a sponsor's direct positions are full, new recruits spill into
/// positions deeper in the tree. The direction determines which open
/// position is chosen.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SpilloverDirection {
    /// Fill each level completely before moving deeper.
    ///
    /// Creates wide, balanced trees. Industry standard for matrix plans.
    /// Most common spillover direction.
    BreadthFirst,

    /// Fill one branch completely before starting the next.
    ///
    /// Creates deep, narrow paths. Less common. Used when plans want
    /// to maximize depth for specific distributors.
    DepthFirst,
}

/// Configuration for node removal (pruning) in a matrix tree.
///
/// When a node is removed from the matrix, its children must be
/// repositioned. The mode determines whether this happens automatically
/// or requires admin intervention. Both modes emit domain events for
/// audit.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PruningConfig {
    /// What happens when a node is removed from the matrix.
    pub mode: PruningMode,
}

/// What happens to child nodes when their parent is removed from the
/// matrix.
///
/// Two strategies for handling orphaned nodes after removal. Both emit
/// domain events for audit.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PruningMode {
    /// Promote the earliest-enrolled child to fill the gap.
    ///
    /// Automatic. No admin intervention needed. The removed node's
    /// earliest child (by enrollment date) inherits the position.
    /// Remaining children are repositioned under the inheritor. May
    /// disrupt established subtrees.
    PromoteEarliest,

    /// Move orphaned nodes to a holding tank for admin re-placement.
    ///
    /// Gives admin full control over where children are repositioned.
    /// Safest option but requires manual intervention. Children lose
    /// commission eligibility while in the holding tank.
    HoldingTank,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_matrix_params() {
        let json = r#"{
            "width": 3,
            "height": 9,
            "spillover": "breadth_first"
        }"#;
        let params: MatrixStructureParams = serde_json::from_str(json).unwrap();
        assert_eq!(params.width, 3);
        assert_eq!(params.height, 9);
        assert!(matches!(params.spillover, SpilloverDirection::BreadthFirst));
    }

    #[test]
    fn deserialize_spillover_directions() {
        let json_breadth = r#""breadth_first""#;
        let dir: SpilloverDirection = serde_json::from_str(json_breadth).unwrap();
        assert!(matches!(dir, SpilloverDirection::BreadthFirst));

        let json_depth = r#""depth_first""#;
        let dir: SpilloverDirection = serde_json::from_str(json_depth).unwrap();
        assert!(matches!(dir, SpilloverDirection::DepthFirst));
    }

    #[test]
    fn deserialize_pruning_config() {
        let json_promote = r#"{
            "mode": "promote_earliest"
        }"#;
        let config: PruningConfig = serde_json::from_str(json_promote).unwrap();
        assert!(matches!(config.mode, PruningMode::PromoteEarliest));

        let json_tank = r#"{
            "mode": "holding_tank"
        }"#;
        let config: PruningConfig = serde_json::from_str(json_tank).unwrap();
        assert!(matches!(config.mode, PruningMode::HoldingTank));
    }

    #[test]
    fn deserialize_pruning_modes() {
        let json_promote = r#""promote_earliest""#;
        let mode: PruningMode = serde_json::from_str(json_promote).unwrap();
        assert!(matches!(mode, PruningMode::PromoteEarliest));

        let json_tank = r#""holding_tank""#;
        let mode: PruningMode = serde_json::from_str(json_tank).unwrap();
        assert!(matches!(mode, PruningMode::HoldingTank));
    }
}
