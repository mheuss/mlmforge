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
	// Primary-key mirror: the row id is the event ID, so a duplicate id is
	// rejected whatever its tree or removed state. Checked in its own pass
	// before the partial-index mirrors below, so a caller can tell an id
	// collision from the others. Which one wins when several are violated at
	// once is unsettled, HEU-794.
	for _, n := range s.nodes {
		if n.ID == node.ID {
			return fmt.Errorf("%w: id=%s", ErrNodeAlreadyProjected, node.ID)
		}
	}
	// Enforce same uniqueness as the Postgres partial unique indexes
	// (only active nodes are constrained).
	if node.RemovedAt == nil {
		for _, n := range s.nodes {
			if n.RemovedAt != nil || n.TreeID != node.TreeID {
				continue
			}
			if n.UserID == node.UserID {
				return fmt.Errorf("%w: tree=%s user=%s", ErrActiveUserConflict, node.TreeID, node.UserID)
			}
			// Mirror idx_tree_nodes_tree_parent_position_active (migration
			// 000004): one active claim per (tree, parent, position). Rows
			// without a position are outside the index (WHERE position IS
			// NOT NULL). Rows without a parent are indexed but never
			// conflict, because unique indexes treat NULLs as distinct.
			if node.ParentID != nil && node.Position != nil &&
				n.ParentID != nil && n.Position != nil &&
				*n.ParentID == *node.ParentID && *n.Position == *node.Position {
				return fmt.Errorf("%w: tree=%s parent=%s position=%d (held by %s)",
					ErrSlotConflict, node.TreeID, *node.ParentID, *node.Position, n.UserID)
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

func (s *MemoryTreeStore) DeleteNodeAndResponsor(
	ctx context.Context,
	treeID, userID string,
	moved []Responsored,
) error {
	// Mirrors the Postgres transaction by staging: nothing is written until
	// every re-sponsor target has been found.
	now := time.Now()
	targets := make([]int, 0, len(moved))
	for _, m := range moved {
		found := -1
		for i := range s.nodes {
			if s.nodes[i].TreeID == treeID && s.nodes[i].UserID == m.UserID && s.nodes[i].RemovedAt == nil {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf(
				"re-sponsoring %s in tree %s found no active row to update",
				m.UserID, treeID)
		}
		targets = append(targets, found)
	}

	if err := s.DeleteNode(ctx, treeID, userID); err != nil {
		return err
	}
	for i, m := range moved {
		sponsor := m.NewSponsorID
		s.nodes[targets[i]].SponsorID = &sponsor
		s.nodes[targets[i]].UpdatedAt = now
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

func (s *MemoryTreeStore) GetNodeIncludingRemoved(_ context.Context, treeID, userID string) (*TreeNodeRow, error) {
	var best *TreeNodeRow
	for i := range s.nodes {
		if s.nodes[i].TreeID != treeID || s.nodes[i].UserID != userID {
			continue
		}
		n := s.nodes[i]
		if n.RemovedAt == nil {
			return &n, nil
		}
		if best == nil || best.RemovedAt.Before(*n.RemovedAt) {
			best = &n
		}
	}
	return best, nil
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
	// InsertNode validates against s.nodes, so pointing it at a copy is what
	// makes the batch all-or-none: a conflict anywhere, including between two
	// rows of this batch, leaves the original slice untouched.
	staged := make([]TreeNodeRow, len(s.nodes), len(s.nodes)+len(nodes))
	copy(staged, s.nodes)

	original := s.nodes
	s.nodes = staged
	for _, n := range nodes {
		if err := s.InsertNode(ctx, n); err != nil {
			s.nodes = original
			return err
		}
	}
	return nil
}
