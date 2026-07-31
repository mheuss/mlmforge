package networkengine

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Compile-time check.
var _ TreeStore = (*MemoryTreeStore)(nil)

// MemoryTreeStore is an in-memory TreeStore for testing.
type MemoryTreeStore struct {
	nodes []TreeNodeRow
}

func NewMemoryTreeStore() *MemoryTreeStore {
	return &MemoryTreeStore{}
}

func (s *MemoryTreeStore) InsertNode(_ context.Context, node TreeNodeRow) error {
	// Enforce same uniqueness as the Postgres partial unique indexes
	// (only active nodes are constrained).
	if node.RemovedAt == nil {
		for _, n := range s.nodes {
			if n.RemovedAt != nil || n.TreeID != node.TreeID {
				continue
			}
			if n.UserID == node.UserID {
				return fmt.Errorf("duplicate active node: tree=%s user=%s", node.TreeID, node.UserID)
			}
			// Mirror idx_tree_nodes_tree_parent_position_active (migration
			// 000004): one active claim per (tree, parent, position). Rows
			// without a parent or position are exempt, matching the index's
			// WHERE clause and Postgres NULL semantics.
			if node.ParentID != nil && node.Position != nil &&
				n.ParentID != nil && n.Position != nil &&
				*n.ParentID == *node.ParentID && *n.Position == *node.Position {
				return fmt.Errorf("duplicate active slot: tree=%s parent=%s position=%d",
					node.TreeID, *node.ParentID, *node.Position)
			}
		}
	}
	s.nodes = append(s.nodes, node)
	return nil
}

func (s *MemoryTreeStore) DeleteNode(_ context.Context, treeID, userID string) error {
	now := time.Now()
	for i := range s.nodes {
		if s.nodes[i].TreeID == treeID && s.nodes[i].UserID == userID && s.nodes[i].RemovedAt == nil {
			s.nodes[i].RemovedAt = &now
			s.nodes[i].UpdatedAt = now
			return nil
		}
	}
	return nil
}

func (s *MemoryTreeStore) GetNode(_ context.Context, treeID, userID string) (*TreeNodeRow, error) {
	for i := range s.nodes {
		if s.nodes[i].TreeID == treeID && s.nodes[i].UserID == userID && s.nodes[i].RemovedAt == nil {
			node := s.nodes[i] // detached copy to match Postgres semantics
			return &node, nil
		}
	}
	return nil, nil
}

func (s *MemoryTreeStore) GetChildren(_ context.Context, treeID, parentUserID string) ([]TreeNodeRow, error) {
	var result []TreeNodeRow
	for _, n := range s.nodes {
		if n.TreeID == treeID && n.ParentID != nil && *n.ParentID == parentUserID && n.RemovedAt == nil {
			result = append(result, n)
		}
	}
	return result, nil
}

func (s *MemoryTreeStore) GetByTree(_ context.Context, treeID string) ([]TreeNodeRow, error) {
	var result []TreeNodeRow
	for _, n := range s.nodes {
		if n.TreeID == treeID && n.RemovedAt == nil {
			result = append(result, n)
		}
	}
	return result, nil
}

func (s *MemoryTreeStore) GetByTreeDepthOrdered(_ context.Context, treeID string) ([]TreeNodeRow, error) {
	var result []TreeNodeRow
	for _, n := range s.nodes {
		if n.TreeID == treeID && n.RemovedAt == nil {
			result = append(result, n)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Depth != result[j].Depth {
			return result[i].Depth < result[j].Depth
		}
		return result[i].EnrolledAt.Before(result[j].EnrolledAt)
	})
	return result, nil
}

func (s *MemoryTreeStore) BulkInsert(ctx context.Context, nodes []TreeNodeRow) error {
	for _, n := range nodes {
		if err := s.InsertNode(ctx, n); err != nil {
			return err
		}
	}
	return nil
}
