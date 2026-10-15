package networkengine

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Compile-time check.
var _ TreeStore = (*MemoryTreeStore)(nil)

// MemoryTreeStore is an in-memory TreeStore for testing.
type MemoryTreeStore struct {
	nodes     []TreeNodeRow
	projected map[string]int64
}

func NewMemoryTreeStore() *MemoryTreeStore {
	return &MemoryTreeStore{}
}

func (s *MemoryTreeStore) InsertNode(ctx context.Context, node TreeNodeRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.insertNodeAt(node, time.Now())
}

// insertNodeAt stores one row, stamped with now.
func (s *MemoryTreeStore) insertNodeAt(node TreeNodeRow, now time.Time) error {
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
			// One active depth-0 row per tree. Kept after the user check:
			// a row breaking both rules must report the same sentinel the
			// database would, and that order is not ours to set (HEU-794).
			if node.Depth == 0 && n.Depth == 0 {
				return fmt.Errorf("%w: tree=%s rooted by %s",
					ErrRootConflict, node.TreeID, n.UserID)
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
	node.CreatedAt = now
	node.UpdatedAt = now
	s.nodes = append(s.nodes, node)
	return nil
}

// appendUnchecked stores a row without any of InsertNode's checks, for a test
// that needs a row the constraints refuse.
func (s *MemoryTreeStore) appendUnchecked(node TreeNodeRow) error {
	s.nodes = append(s.nodes, node)
	return nil
}

func (s *MemoryTreeStore) DeleteNode(ctx context.Context, treeID, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	treeID, userID, removalEventID string,
	moved []Responsored,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

	// Captured before anything is written. Searching for a removed row after
	// the delete can find an earlier placement's tombstone.
	removing := -1
	for i := range s.nodes {
		if s.nodes[i].TreeID == treeID && s.nodes[i].UserID == userID && s.nodes[i].RemovedAt == nil {
			removing = i
			break
		}
	}
	if removing < 0 {
		return fmt.Errorf(
			"soft delete for user %s in tree %s matched 0 active rows; %d re-sponsor writes not applied",
			userID, treeID, len(moved))
	}

	if err := s.DeleteNode(ctx, treeID, userID); err != nil {
		return err
	}
	event := removalEventID
	s.nodes[removing].RemovedByEventID = &event
	for i, m := range moved {
		sponsor := m.NewSponsorID
		s.nodes[targets[i]].SponsorID = &sponsor
		s.nodes[targets[i]].UpdatedAt = now
	}
	return nil
}

func (s *MemoryTreeStore) GetNode(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i := range s.nodes {
		if s.nodes[i].TreeID == treeID && s.nodes[i].UserID == userID && s.nodes[i].RemovedAt == nil {
			node := s.nodes[i] // detached copy to match Postgres semantics
			return &node, nil
		}
	}
	return nil, nil
}

func (s *MemoryTreeStore) GetNodeIncludingRemoved(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var best *TreeNodeRow
	for i := range s.nodes {
		if s.nodes[i].TreeID != treeID || s.nodes[i].UserID != userID {
			continue
		}
		n := s.nodes[i]
		if n.RemovedAt == nil {
			return &n, nil
		}
		if best == nil || ranksAhead(n, *best) {
			best = &n
		}
	}
	return best, nil
}

func (s *MemoryTreeStore) GetNodeByRemovalEvent(ctx context.Context, treeID, removalEventID string) (*TreeNodeRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var best *TreeNodeRow
	for i := range s.nodes {
		stamp := s.nodes[i].RemovedByEventID
		if s.nodes[i].TreeID != treeID || stamp == nil || *stamp != removalEventID {
			continue
		}
		n := s.nodes[i]
		if best == nil || ranksAhead(n, *best) {
			best = &n
		}
	}
	return best, nil
}

// ranksAhead reports whether tombstone a comes before tombstone b: removed
// later, or removed at the same instant with the higher id.
func ranksAhead(a, b TreeNodeRow) bool {
	if !a.RemovedAt.Equal(*b.RemovedAt) {
		return a.RemovedAt.After(*b.RemovedAt)
	}
	// Lowercased so the order does not depend on the id's letter case.
	return strings.ToLower(a.ID) > strings.ToLower(b.ID)
}

func (s *MemoryTreeStore) GetChildren(ctx context.Context, treeID, parentUserID string) ([]TreeNodeRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var result []TreeNodeRow
	for _, n := range s.nodes {
		if n.TreeID == treeID && n.ParentID != nil && *n.ParentID == parentUserID && n.RemovedAt == nil {
			result = append(result, n)
		}
	}
	return result, nil
}

func (s *MemoryTreeStore) GetByTree(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var result []TreeNodeRow
	for _, n := range s.nodes {
		if n.TreeID == treeID && n.RemovedAt == nil {
			result = append(result, n)
		}
	}
	return result, nil
}

func (s *MemoryTreeStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	// The batch goes into a copy, and the original is restored on the first
	// error. That makes the batch all-or-none, including a conflict between
	// two rows of this batch.
	staged := make([]TreeNodeRow, len(s.nodes), len(s.nodes)+len(nodes))
	copy(staged, s.nodes)

	original := s.nodes
	s.nodes = staged
	now := time.Now()
	for _, n := range nodes {
		if err := s.insertNodeAt(n, now); err != nil {
			s.nodes = original
			return err
		}
	}
	return nil
}

func (s *MemoryTreeStore) ProjectedVersion(ctx context.Context, treeID string) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	version, found := s.projected[treeID]
	return version, found, nil
}

func (s *MemoryTreeStore) ProjectedRows(ctx context.Context, treeID string) (int64, bool, []TreeNodeRow, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, nil, err
	}
	version, found := s.projected[treeID]
	rows, err := s.GetByTreeDepthOrdered(ctx, treeID)
	if err != nil {
		return 0, false, nil, err
	}
	return version, found, rows, nil
}

func (s *MemoryTreeStore) ProjectInsert(ctx context.Context, node TreeNodeRow, eventVersion int64) error {
	if err := checkEventVersion(node.TreeID, eventVersion); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.refuseBelow(node.TreeID, eventVersion); err != nil {
		return err
	}
	if err := s.InsertNode(ctx, node); err != nil {
		return err
	}
	s.advance(node.TreeID, eventVersion)
	return nil
}

func (s *MemoryTreeStore) ProjectRemoval(
	ctx context.Context,
	treeID string,
	r RemovalProjection,
	removalEventID string,
	eventVersion int64,
) error {
	if len(r.Users) == 0 {
		return fmt.Errorf("project removal in tree %s: the removal names no users", treeID)
	}
	if err := checkEventVersion(treeID, eventVersion); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.refuseBelow(treeID, eventVersion); err != nil {
		return err
	}
	staged, err := s.stageRemoval(treeID, r, removalEventID)
	if err != nil {
		return err
	}
	s.nodes = staged
	s.advance(treeID, eventVersion)
	return nil
}

// stageRemoval returns the rows as the removal leaves them, or the first fault
// it finds. It changes nothing in s.
func (s *MemoryTreeStore) stageRemoval(treeID string, r RemovalProjection, removalEventID string) ([]TreeNodeRow, error) {
	rows := slices.Clone(s.nodes)
	now := time.Now()
	active := func(user string) int {
		for i := range rows {
			if rows[i].TreeID == treeID && rows[i].UserID == user && rows[i].RemovedAt == nil {
				return i
			}
		}
		return -1
	}

	tombstoned := map[int]bool{}
	for _, u := range r.Users {
		if i := active(u); i >= 0 {
			event := removalEventID
			rows[i].RemovedAt, rows[i].UpdatedAt, rows[i].RemovedByEventID = &now, now, &event
			tombstoned[i] = true
		}
	}
	if len(tombstoned) != len(r.Users) {
		return nil, fmt.Errorf("tombstoning removed users %v in tree %s matched %d of %d active rows",
			r.Users, treeID, len(tombstoned), len(r.Users))
	}

	responsored := map[int]bool{}
	for _, m := range r.Responsored {
		if i := active(m.UserID); i >= 0 {
			sponsor := m.NewSponsorID
			rows[i].SponsorID, rows[i].UpdatedAt = &sponsor, now
			responsored[i] = true
		}
	}
	if len(responsored) != len(r.Responsored) {
		return nil, fmt.Errorf("re-sponsoring %d recruits in tree %s updated %d active rows",
			len(r.Responsored), treeID, len(responsored))
	}

	relocated := map[int]bool{}
	for _, m := range r.Relocated {
		if i := active(m.UserID); i >= 0 {
			parent, position := m.ParentID, m.Position
			rows[i].ParentID, rows[i].Position, rows[i].Depth, rows[i].UpdatedAt = &parent, &position, m.Depth, now
			relocated[i] = true
		}
	}
	if len(relocated) != len(r.Relocated) {
		return nil, fmt.Errorf("relocating %d survivors in tree %s updated %d active rows",
			len(r.Relocated), treeID, len(relocated))
	}

	if err := checkActiveSlots(rows, treeID); err != nil {
		return nil, err
	}
	return rows, nil
}

// checkActiveSlots reports two active rows of treeID that claim one slot.
func checkActiveSlots(rows []TreeNodeRow, treeID string) error {
	type slot struct {
		parent   string
		position int
	}
	held := map[slot]string{}
	for _, n := range rows {
		if n.TreeID != treeID || n.RemovedAt != nil || n.ParentID == nil || n.Position == nil {
			continue
		}
		key := slot{*n.ParentID, *n.Position}
		if other, ok := held[key]; ok {
			return fmt.Errorf("%w: tree=%s parent=%s position=%d (held by %s and %s)",
				ErrSlotConflict, treeID, *n.ParentID, *n.Position, other, n.UserID)
		}
		held[key] = n.UserID
	}
	return nil
}

func (s *MemoryTreeStore) ProjectRejection(ctx context.Context, treeID, rejectedEventID string, eventVersion int64) error {
	if err := checkEventVersion(treeID, eventVersion); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.refuseBelow(treeID, eventVersion); err != nil {
		return err
	}
	now := time.Now()
	for i := range s.nodes {
		if s.nodes[i].TreeID == treeID && s.nodes[i].ID == rejectedEventID && s.nodes[i].RemovedAt == nil {
			s.nodes[i].RemovedAt = &now
			s.nodes[i].UpdatedAt = now
		}
	}
	s.advance(treeID, eventVersion)
	return nil
}

func (s *MemoryTreeStore) UndoRootProjection(ctx context.Context, treeID, userID string, eventVersion int64) error {
	if err := checkEventVersion(treeID, eventVersion); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	projected, found := s.projected[treeID]
	if !found {
		return fmt.Errorf("tree %s has no projection row; the root row for %s at version %d was not deleted",
			treeID, userID, eventVersion)
	}
	if projected != eventVersion {
		return fmt.Errorf("tree %s has projected version %d, not %d; the root row for %s was not deleted",
			treeID, projected, eventVersion, userID)
	}
	row, err := s.GetNode(ctx, treeID, userID)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("soft delete for root %s in tree %s matched 0 active rows; the projected version was not changed",
			userID, treeID)
	}
	if err := s.DeleteNode(ctx, treeID, userID); err != nil {
		return err
	}
	s.projected[treeID] = eventVersion - 1
	return nil
}

// refuseBelow refuses an event version below 1, or below the tree's projected
// version.
func (s *MemoryTreeStore) refuseBelow(treeID string, eventVersion int64) error {
	if err := checkEventVersion(treeID, eventVersion); err != nil {
		return err
	}
	if projected := s.projected[treeID]; eventVersion < projected {
		return &ProjectionRefusedError{TreeID: treeID, EventVersion: eventVersion, ProjectedVersion: projected}
	}
	return nil
}

// advance records eventVersion as the tree's projected version when it is
// higher than the one recorded.
func (s *MemoryTreeStore) advance(treeID string, eventVersion int64) {
	if s.projected == nil {
		s.projected = map[string]int64{}
	}
	if eventVersion > s.projected[treeID] {
		s.projected[treeID] = eventVersion
	}
}
