package networkengine

import (
	"context"
	"strings"
	"testing"
)

// treeNodeIndexDef reads an index definition back out of the database.
func treeNodeIndexDef(t *testing.T, name string) string {
	t.Helper()
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	pool := pgContainer.NewPool(t)

	var def string
	err := pool.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE tablename = 'tree_nodes' AND indexname = $1`,
		name,
	).Scan(&def)
	if err != nil {
		t.Fatalf("read index definition for %s: %v", name, err)
	}
	return def
}

// The primary key is the ON CONFLICT arbiter, so its shape decides what a
// skipped insert means. Behavioral tests pin the conflicts an index raises.
// They cannot see a predicate being added, because a narrower index still
// raises on the rows the tests use.
func TestTreeNodesPrimaryKeyIsNotPartial(t *testing.T) {
	def := treeNodeIndexDef(t, "tree_nodes_pkey")

	if !strings.Contains(def, "UNIQUE") {
		t.Errorf("the ON CONFLICT arbiter must be unique, got: %s", def)
	}
	if !strings.Contains(def, "(id)") {
		t.Errorf("the arbiter must key on id alone, or ON CONFLICT (id) stops matching it, got: %s", def)
	}
	// A WHERE clause here would free a soft-deleted row's id for reuse, and a
	// redelivery of the original event would insert a second row instead of
	// being skipped.
	if strings.Contains(def, "WHERE") {
		t.Errorf("the primary key must not be partial, got: %s", def)
	}
}

// conflictError matches this index by name. The name is covered by the
// behavioral tests; the predicate is not.
func TestTreeNodesActiveUserIndex(t *testing.T) {
	def := treeNodeIndexDef(t, activeUserIndex)

	if !strings.Contains(def, "UNIQUE") {
		t.Errorf("index must be UNIQUE to raise a conflict at all, got: %s", def)
	}
	if !strings.Contains(def, "(tree_id, user_id)") {
		t.Errorf("index must key on tree and user, got: %s", def)
	}
	// Without the predicate a removed user could never be re-placed, which is
	// the soft-delete contract in ADR-023.
	if !strings.Contains(def, "removed_at IS NULL") {
		t.Errorf("index must cover active rows only, got: %s", def)
	}
}

func TestTreeNodesActiveSlotIndex(t *testing.T) {
	def := treeNodeIndexDef(t, activeSlotIndex)

	if !strings.Contains(def, "UNIQUE") {
		t.Errorf("index must be UNIQUE to raise a conflict at all, got: %s", def)
	}
	if !strings.Contains(def, "tree_id") || !strings.Contains(def, "parent_id") ||
		!strings.Contains(def, "position") {
		t.Errorf("index must key on tree, parent and position, got: %s", def)
	}
	if !strings.Contains(def, "removed_at IS NULL") {
		t.Errorf("index must cover active rows only, got: %s", def)
	}
	// Rows without a position sit outside the index. Dropping this would make
	// every parent's unpositioned children collide with each other.
	if !strings.Contains(def, "position\" IS NOT NULL") {
		t.Errorf("index must exclude rows with no position, got: %s", def)
	}
}
