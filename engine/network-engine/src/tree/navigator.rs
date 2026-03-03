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
    /// Returns true if the tree contains a node with this user_id.
    fn contains(&self, user_id: Uuid) -> bool;
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tree::binary::BinaryTree;
    use crate::tree::test_helpers::test_uuid;
    use crate::tree::unilevel::UnilevelTree;

    #[test]
    fn unilevel_tree_is_object_safe() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let nav: Box<dyn TreeNavigator> = Box::new(tree);
        assert!(nav.contains(test_uuid(1)));
        assert!(nav.contains(test_uuid(2)));
        assert!(!nav.contains(test_uuid(99)));

        let children = nav.get_children(test_uuid(1)).unwrap();
        assert_eq!(children.len(), 1);
        assert_eq!(children[0].user_id, test_uuid(2));
    }

    #[test]
    fn binary_tree_is_object_safe() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 1)
            .unwrap();

        let nav: Box<dyn TreeNavigator> = Box::new(tree);
        assert!(nav.contains(test_uuid(1)));
        assert!(nav.contains(test_uuid(2)));
        assert!(!nav.contains(test_uuid(99)));

        let parent = nav.get_parent(test_uuid(2)).unwrap();
        assert_eq!(parent.unwrap().user_id, test_uuid(1));
    }

    #[test]
    fn matrix_tree_is_object_safe() {
        use crate::config::matrix::SpilloverDirection;
        use crate::tree::matrix::MatrixTree;

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 1).unwrap();

        let nav: Box<dyn TreeNavigator> = Box::new(tree);
        assert!(nav.contains(test_uuid(1)));
        assert!(nav.contains(test_uuid(2)));
        assert!(!nav.contains(test_uuid(99)));

        let children = nav.get_children(test_uuid(1)).unwrap();
        assert_eq!(children.len(), 1);
        assert_eq!(children[0].user_id, test_uuid(2));
    }
}
