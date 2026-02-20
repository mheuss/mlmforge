use uuid::Uuid;

use super::error::TreeError;
use super::node::Node;
use crate::types::TreePosition;

/// Shared read-only interface for all tree types.
///
/// Covers placement traversals, sponsor traversals, and position queries.
/// Each tree type implements this trait. The worker uses `dyn TreeNavigator`
/// to dispatch query operations without matching on tree type.
///
/// Mutation methods (`add_root`, `add_node`, `remove_node`) are NOT part
/// of this trait because their signatures differ per tree type (binary
/// requires position, unilevel does not).
pub trait TreeNavigator {
    fn get_parent(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError>;
    fn get_children(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError>;
    fn get_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError>;
    fn get_downline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError>;
    fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError>;
    fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError>;
    fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError>;
    fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError>;
    fn is_descendant_of(&self, user_id: Uuid, ancestor_id: Uuid) -> Result<bool, TreeError>;
    fn get_sponsor(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError>;
    fn get_sponsor_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError>;
    fn get_sponsored(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError>;
}
