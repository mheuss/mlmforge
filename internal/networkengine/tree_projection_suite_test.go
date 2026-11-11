package networkengine

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projectionState is what a projection test compares before and after a
// write: the tree's projected version, and the newest row for each user.
type projectionState struct {
	version int64
	found   bool
	rows    map[string]*TreeNodeRow
}

func readProjectionState(t *testing.T, s TreeStore, tree string, users ...string) projectionState {
	t.Helper()
	ctx := context.Background()
	version, found, err := s.ProjectedVersion(ctx, tree)
	require.NoError(t, err)
	rows := make(map[string]*TreeNodeRow, len(users))
	for _, u := range users {
		row, err := s.GetNodeIncludingRemoved(ctx, tree, u)
		require.NoError(t, err)
		rows[u] = row
	}
	return projectionState{version: version, found: found, rows: rows}
}

// notAUUID is an id that Postgres's uuid input refuses.
const notAUUID = "not-a-uuid"

// badIDWrite is one write carrying a single id Postgres's uuid input refuses.
type badIDWrite struct {
	name  string
	write func(ctx context.Context, s TreeStore) error
}

// seedBadIDTree writes a root and two rows under it, at versions 1 to 3.
func seedBadIDTree(t *testing.T, s TreeStore, tree, root, child, recruit string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0)), 2))
	require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(3), tree, recruit, 1, ptr(root), ptr(child), intPtr(1)), 3))
}

// badIDWrites returns writes that each carry one id Postgres's uuid input
// refuses, with every other value valid. controls holds one unedited write of
// each kind.
func badIDWrites(tree, root, child, recruit string) (bad, controls []badIDWrite) {
	insert := func(field string, edit func(*TreeNodeRow)) badIDWrite {
		return badIDWrite{"insert " + field, func(ctx context.Context, s TreeStore) error {
			row := makeUUIDNode(testNodeUUID(4), tree, testUserUUID(4), 2, ptr(child), ptr(child), intPtr(0))
			edit(&row)
			return s.ProjectInsert(ctx, row, 4)
		}}
	}
	remove := func(name, treeID string, r RemovalProjection, event string) badIDWrite {
		return badIDWrite{"removal " + name, func(ctx context.Context, s TreeStore) error {
			return s.ProjectRemoval(ctx, treeID, r, event, 4)
		}}
	}
	onlyChild := RemovalProjection{Users: []string{child}}
	urn := "urn:uuid:" + testUserUUID(4)
	wrapped := "x" + testUserUUID(4) + "x"
	controls = []badIDWrite{
		insert("with every id valid", func(*TreeNodeRow) {}),
		remove("with every id valid", tree, RemovalProjection{Users: []string{child},
			Responsored: []Responsored{{UserID: recruit, NewSponsorID: root}},
			Relocated:   []Relocation{{UserID: recruit, ParentID: root, Position: 5, Depth: 1}}}, testNodeUUID(9)),
		{"rejection with every id valid", func(ctx context.Context, s TreeStore) error {
			return s.ProjectRejection(ctx, tree, testNodeUUID(2), 4)
		}},
		{"undo root with every id valid", func(ctx context.Context, s TreeStore) error {
			return s.UndoRootProjection(ctx, tree, root, 3)
		}},
	}
	return []badIDWrite{
		insert("tree_id", func(r *TreeNodeRow) { r.TreeID = notAUUID }),
		insert("id", func(r *TreeNodeRow) { r.ID = notAUUID }),
		insert("user_id", func(r *TreeNodeRow) { r.UserID = notAUUID }),
		insert("parent_id", func(r *TreeNodeRow) { r.ParentID = ptr(notAUUID) }),
		insert("sponsor_id", func(r *TreeNodeRow) { r.SponsorID = ptr(notAUUID) }),
		insert("tree_id with a urn:uuid: prefix", func(r *TreeNodeRow) { r.TreeID = urn }),
		insert("tree_id wrapped in non-braces", func(r *TreeNodeRow) { r.TreeID = wrapped }),
		insert("user_id with a urn:uuid: prefix", func(r *TreeNodeRow) { r.UserID = urn }),
		insert("user_id wrapped in non-braces", func(r *TreeNodeRow) { r.UserID = wrapped }),
		remove("tree_id", notAUUID, onlyChild, testNodeUUID(9)),
		remove("removal_event_id", tree, onlyChild, notAUUID),
		remove("removed user_id", tree, RemovalProjection{Users: []string{child, notAUUID}}, testNodeUUID(9)),
		remove("re-sponsored user_id", tree, RemovalProjection{Users: []string{child},
			Responsored: []Responsored{{UserID: notAUUID, NewSponsorID: root}}}, testNodeUUID(9)),
		remove("new_sponsor_id", tree, RemovalProjection{Users: []string{child},
			Responsored: []Responsored{{UserID: recruit, NewSponsorID: notAUUID}}}, testNodeUUID(9)),
		remove("relocated user_id", tree, RemovalProjection{Users: []string{child},
			Relocated: []Relocation{{UserID: notAUUID, ParentID: root, Position: 5, Depth: 1}}}, testNodeUUID(9)),
		remove("relocated parent_id", tree, RemovalProjection{Users: []string{child},
			Relocated: []Relocation{{UserID: recruit, ParentID: notAUUID, Position: 5, Depth: 1}}}, testNodeUUID(9)),
		{"rejection tree_id", func(ctx context.Context, s TreeStore) error {
			return s.ProjectRejection(ctx, notAUUID, testNodeUUID(2), 4)
		}},
		{"rejection rejected_event_id", func(ctx context.Context, s TreeStore) error {
			return s.ProjectRejection(ctx, tree, notAUUID, 4)
		}},
		{"undo root tree_id", func(ctx context.Context, s TreeStore) error {
			return s.UndoRootProjection(ctx, notAUUID, root, 3)
		}},
		{"undo root user_id", func(ctx context.Context, s TreeStore) error {
			return s.UndoRootProjection(ctx, tree, notAUUID, 3)
		}},
	}, controls
}

// int4Write is one write carrying a position and a depth, with the user whose
// row it writes.
type int4Write struct {
	name            string
	user            string
	position, depth int
	write           func(ctx context.Context, s TreeStore) error
}

// int4Writes returns writes that each carry one position or depth outside the
// range of Postgres's int4, with every other value valid. controls holds the
// same writes with the out-of-range value moved to the edge of that range.
func int4Writes(tree, root, child, recruit string) (bad, controls []int4Write) {
	insert := func(name string, position, depth int) int4Write {
		user := testUserUUID(4)
		return int4Write{"an insert with a " + name, user, position, depth, func(ctx context.Context, s TreeStore) error {
			return s.ProjectInsert(ctx,
				makeUUIDNode(testNodeUUID(4), tree, user, depth, ptr(child), ptr(child), intPtr(position)), 4)
		}}
	}
	relocate := func(name string, position, depth int) int4Write {
		return int4Write{"a relocation with a " + name, recruit, position, depth, func(ctx context.Context, s TreeStore) error {
			return s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child},
				Relocated: []Relocation{{UserID: recruit, ParentID: root, Position: position, Depth: depth}}},
				testNodeUUID(9), 4)
		}}
	}
	const above, below = math.MaxInt32 + 1, math.MinInt32 - 1
	controls = []int4Write{
		insert("position at the int4 maximum", math.MaxInt32, 2),
		insert("position at the int4 minimum", math.MinInt32, 2),
		insert("depth at the int4 maximum", 0, math.MaxInt32),
		insert("depth at the int4 minimum", 0, math.MinInt32),
		relocate("position at the int4 maximum", math.MaxInt32, 1),
		relocate("position at the int4 minimum", math.MinInt32, 1),
		relocate("depth at the int4 maximum", 5, math.MaxInt32),
		relocate("depth at the int4 minimum", 5, math.MinInt32),
	}
	return []int4Write{
		insert("position above the int4 maximum", above, 2),
		insert("position below the int4 minimum", below, 2),
		insert("depth above the int4 maximum", 0, above),
		insert("depth below the int4 minimum", 0, below),
		relocate("position above the int4 maximum", above, 1),
		relocate("position below the int4 minimum", below, 1),
		relocate("depth above the int4 maximum", 5, above),
		relocate("depth below the int4 minimum", 5, below),
	}, controls
}

// runTreeProjectionSuite checks the projected-version behavior of a TreeStore.
// newStore returns a fresh, empty store on each call.
func runTreeProjectionSuite(t *testing.T, newStore func(t *testing.T) TreeStore) {
	t.Helper()

	tree := testTreeUUID(1)
	root, child, recruit := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	rootRow := makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil)
	childRow := makeUUIDNode(testNodeUUID(2), tree, child, 1, ptr(root), ptr(root), intPtr(0))
	recruitRow := makeUUIDNode(testNodeUUID(3), tree, recruit, 1, ptr(root), ptr(child), intPtr(1))
	grand := testUserUUID(4)
	grandRow := makeUUIDNode(testNodeUUID(4), tree, grand, 2, ptr(child), ptr(child), intPtr(0))
	onlyChild := RemovalProjection{Users: []string{child}}

	t.Run("a tree with no projection has no projection row", func(t *testing.T) {
		s := newStore(t)

		got := readProjectionState(t, s, tree)

		assert.False(t, got.found)
		assert.Equal(t, int64(0), got.version)
	})

	t.Run("every projection method refuses a cancelled context", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.ProjectInsert(context.Background(), rootRow, 3))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		calls := []struct {
			name string
			call func() error
		}{
			{"ProjectedVersion", func() error {
				_, _, err := s.ProjectedVersion(ctx, tree)
				return err
			}},
			{"ProjectedRows", func() error {
				_, _, _, err := s.ProjectedRows(ctx, tree)
				return err
			}},
			{"ProjectInsert", func() error { return s.ProjectInsert(ctx, childRow, 2) }},
			{"ProjectRemoval", func() error {
				return s.ProjectRemoval(ctx, tree, onlyChild, testNodeUUID(9), 2)
			}},
			{"UndoRootProjection", func() error { return s.UndoRootProjection(ctx, tree, root, 1) }},
			{"ProjectRejection", func() error { return s.ProjectRejection(ctx, tree, childRow.ID, 4) }},
		}
		for _, c := range calls {
			assert.ErrorIs(t, c.call(), context.Canceled, c.name)
		}
	})

	t.Run("a projection above the version records its version", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		got := readProjectionState(t, s, tree, child)
		assert.True(t, got.found)
		assert.Equal(t, int64(2), got.version)
		require.NotNil(t, got.rows[child])
		assert.Nil(t, got.rows[child].RemovedAt)
	})

	t.Run("an insert below the version is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 3))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectInsert(ctx, recruitRow, 2)

		var refused *ProjectionRefusedError
		require.ErrorAs(t, err, &refused)
		assert.Equal(t, ProjectionRefusedError{TreeID: tree, EventVersion: 2, ProjectedVersion: 3}, *refused)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a removal below the version is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 4))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree,
			RemovalProjection{Users: []string{child}, Responsored: []Responsored{{UserID: recruit, NewSponsorID: root}}},
			testNodeUUID(9), 3)

		var refused *ProjectionRefusedError
		require.ErrorAs(t, err, &refused)
		assert.Equal(t, ProjectionRefusedError{TreeID: tree, EventVersion: 3, ProjectedVersion: 4}, *refused)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a removal above the version tombstones the row and records its version", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, testNodeUUID(9), 3))

		got := readProjectionState(t, s, tree, child)
		assert.Equal(t, int64(3), got.version)
		require.NotNil(t, got.rows[child])
		assert.NotNil(t, got.rows[child].RemovedAt)
	})

	t.Run("a removal above the version repoints the moved recruits", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))

		require.NoError(t, s.ProjectRemoval(ctx, tree,
			RemovalProjection{Users: []string{child}, Responsored: []Responsored{{UserID: recruit, NewSponsorID: root}}},
			testNodeUUID(9), 4))

		got := readProjectionState(t, s, tree, recruit)
		assert.Equal(t, int64(4), got.version)
		require.NotNil(t, got.rows[recruit])
		require.NotNil(t, got.rows[recruit].SponsorID)
		assert.Equal(t, root, *got.rows[recruit].SponsorID)
	})

	t.Run("a removal above the version that fails part way writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		absent := testUserUUID(7)
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child}, Responsored: []Responsored{
			{UserID: recruit, NewSponsorID: root},
			{UserID: absent, NewSponsorID: root},
		}}, testNodeUUID(9), 4)

		require.Error(t, err)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a root_added event at the version leaves the store unchanged", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		before := readProjectionState(t, s, tree, root)

		err := s.ProjectInsert(ctx, rootRow, 1)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		assert.Equal(t, before, readProjectionState(t, s, tree, root))
	})

	t.Run("a node_placed event at the version leaves the store unchanged", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		before := readProjectionState(t, s, tree, root, child)

		err := s.ProjectInsert(ctx, childRow, 2)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a node_removed event at the version leaves the store unchanged", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		moved := []Responsored{{UserID: recruit, NewSponsorID: root}}
		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child}, Responsored: moved}, removal, 4))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child}, Responsored: moved}, removal, 4)

		require.NoError(t, err)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a node_removed event at the version given uppercase ids converges", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		hr, hc := hexUser(1), hexUser(2)
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, hr, 0, nil, ptr(hr), nil), 1))
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(2), tree, hc, 1, ptr(hr), ptr(hr), intPtr(0)), 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{hc}}, removal, 3))
		before := readProjectionState(t, s, tree, hr, hc)
		up := strings.ToUpper

		err := s.ProjectRemoval(ctx, up(tree), RemovalProjection{Users: []string{up(hc)}}, up(removal), 3)

		require.NoError(t, err)
		assert.Equal(t, before, readProjectionState(t, s, tree, hr, hc))
	})

	t.Run("a removal at the version whose stamp covers only some of its users fails", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		absent := testUserUUID(5)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, removal, 3))
		before := readProjectionState(t, s, tree, root, child, absent)

		err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child, absent}}, removal, 3)

		require.ErrorContains(t, err,
			"tombstoning removed users ["+child+" "+absent+"] in tree "+tree+" matched 0 of 2 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, absent))
	})

	t.Run("a removal at the version whose other user is still active fails", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, removal, 4))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child, recruit}}, removal, 4)

		require.ErrorContains(t, err,
			"tombstoning removed users ["+child+" "+recruit+"] in tree "+tree+" matched 1 of 2 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a removal at the version whose event stamped another user fails", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child, recruit}}, removal, 4))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, onlyChild, removal, 4)

		require.ErrorContains(t, err, "tombstoning removed users ["+child+"] in tree "+tree+" matched 0 of 1 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a removal at the version does not converge on its stamp in another tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		other := testTreeUUID(2)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, removal, 3))
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(11), other, root, 0, nil, ptr(root), nil), 1))
		before := readProjectionState(t, s, other, root, child)

		err := s.ProjectRemoval(ctx, other, onlyChild, removal, 1)

		require.ErrorContains(t, err, "tombstoning removed users ["+child+"] in tree "+other+" matched 0 of 1 active rows")
		assert.Equal(t, before, readProjectionState(t, s, other, root, child))
	})

	t.Run("a removal at the version does not converge on the stamp of another event", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, testNodeUUID(8), 3))
		before := readProjectionState(t, s, tree, root, child)

		err := s.ProjectRemoval(ctx, tree, onlyChild, testNodeUUID(9), 3)

		require.ErrorContains(t, err, "tombstoning removed users ["+child+"] in tree "+tree+" matched 0 of 1 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a removal at the version naming its user twice fails", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, removal, 3))
		before := readProjectionState(t, s, tree, root, child)

		err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child, child}}, removal, 3)

		require.ErrorContains(t, err,
			"tombstoning removed users ["+child+" "+child+"] in tree "+tree+" matched 0 of 2 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a removal of two users at the version converges with its users in another order", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		removal := testNodeUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{child, recruit}}, removal, 4))
		before := readProjectionState(t, s, tree, root, child, recruit)

		err := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{recruit, child}}, removal, 4)

		require.NoError(t, err)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
	})

	t.Run("a write refused for another reason leaves the version where it was", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		second := makeUUIDNode(testNodeUUID(4), tree, child, 1, ptr(root), ptr(root), intPtr(5))

		err := s.ProjectInsert(ctx, second, 3)

		require.ErrorIs(t, err, ErrActiveUserConflict)
		got := readProjectionState(t, s, tree)
		assert.Equal(t, int64(2), got.version)
	})

	t.Run("a refused first projection leaves no projection row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		other := testTreeUUID(2)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		sameID := makeUUIDNode(testNodeUUID(1), other, root, 0, nil, ptr(root), nil)

		err := s.ProjectInsert(ctx, sameID, 1)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		got := readProjectionState(t, s, other)
		assert.False(t, got.found)
	})

	t.Run("undoing a root at version 1 restores version 0", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))

		require.NoError(t, s.UndoRootProjection(ctx, tree, root, 1))

		got := readProjectionState(t, s, tree, root)
		assert.True(t, got.found)
		assert.Equal(t, int64(0), got.version)
		require.NotNil(t, got.rows[root])
		assert.NotNil(t, got.rows[root].RemovedAt)
	})

	t.Run("undoing a root at a later version restores the version before it", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 4))

		require.NoError(t, s.UndoRootProjection(ctx, tree, root, 4))

		got := readProjectionState(t, s, tree, root)
		assert.Equal(t, int64(3), got.version)
		require.NotNil(t, got.rows[root])
		assert.NotNil(t, got.rows[root].RemovedAt)
	})

	t.Run("undoing a root with no active row leaves the version where it was", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		before := readProjectionState(t, s, tree, root, child)

		err := s.UndoRootProjection(ctx, tree, child, 1)

		require.ErrorContains(t, err, "soft delete for root "+child+" in tree "+tree+" matched 0 active rows")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("undoing a root in a tree with no projection row deletes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, insertRow(ctx, s, rootRow))
		before := readProjectionState(t, s, tree, root)

		err := s.UndoRootProjection(ctx, tree, root, 1)

		require.EqualError(t, err, "tree "+tree+" has no projection row; the root row for "+root+" at version 1 was not deleted")
		assert.Equal(t, before, readProjectionState(t, s, tree, root))
	})

	t.Run("an event version below 1 is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		insertErr := s.ProjectInsert(ctx, rootRow, 0)
		removeErr := s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{root}}, testNodeUUID(9), 0)
		undoErr := s.UndoRootProjection(ctx, tree, root, 0)

		want := "tree " + tree + " was given event version 0, below 1; nothing was written"
		require.EqualError(t, insertErr, want)
		require.EqualError(t, removeErr, want)
		require.EqualError(t, undoErr, want)
		got := readProjectionState(t, s, tree, root)
		assert.False(t, got.found)
		assert.Nil(t, got.rows[root])
	})

	t.Run("undoing a root at a version the tree is not at deletes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		before := readProjectionState(t, s, tree, root)

		err := s.UndoRootProjection(ctx, tree, root, 1)

		require.EqualError(t, err, "tree "+tree+" has projected version 2, not 1; the root row for "+root+" was not deleted")
		assert.Equal(t, before, readProjectionState(t, s, tree, root))
	})

	for _, tc := range []struct {
		name       string
		undoSecond bool
	}{
		{"the tree whose root went in second", true},
		{"the tree whose root went in first", false},
	} {
		t.Run("undoing a root does not cross tree boundaries, undoing "+tc.name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			other := testTreeUUID(2)
			require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
			require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(5), other, root, 0, nil, ptr(root), nil), 1))
			undone, kept := other, tree
			if !tc.undoSecond {
				undone, kept = tree, other
			}

			require.NoError(t, s.UndoRootProjection(ctx, undone, root, 1))

			inKept, err := s.GetNode(ctx, kept, root)
			require.NoError(t, err)
			assert.NotNil(t, inKept, "no active root read back in the kept tree after an undo in the other")
			inUndone, err := s.GetNode(ctx, undone, root)
			require.NoError(t, err)
			assert.Nil(t, inUndone, "active root read back in the tree whose root was undone")
			keptVersion, keptFound, err := s.ProjectedVersion(ctx, kept)
			require.NoError(t, err)
			assert.True(t, keptFound, "no projection row read back for the kept tree")
			assert.Equal(t, int64(1), keptVersion, "projected version read back for the kept tree")
		})
	}

	t.Run("undoing a root leaves the user's row and the version of another tree", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		other := testTreeUUID(2)
		otherRoot := testUserUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(5), other, otherRoot, 0, nil, ptr(otherRoot), nil), 1))
		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(6), other, root, 1, ptr(otherRoot), ptr(otherRoot), intPtr(0)), 2))

		require.NoError(t, s.UndoRootProjection(ctx, tree, root, 1))

		member, err := s.GetNode(ctx, other, root)
		require.NoError(t, err)
		assert.NotNil(t, member, "no active member row read back in the other tree after the undo")
		version, found, err := s.ProjectedVersion(ctx, other)
		require.NoError(t, err)
		assert.True(t, found, "no projection row read back for the other tree")
		assert.Equal(t, int64(2), version, "projected version read back for the other tree")
	})

	t.Run("a rejection soft-deletes the rejected event's row and records its version", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		require.NoError(t, s.ProjectRejection(ctx, tree, childRow.ID, 3))

		got := readProjectionState(t, s, tree, root, child)
		assert.Equal(t, int64(3), got.version)
		require.NotNil(t, got.rows[child])
		assert.NotNil(t, got.rows[child].RemovedAt, "the rejected event's row is still active")
		assert.Nil(t, got.rows[child].RemovedByEventID, "the rejection stamped the row")
		require.NotNil(t, got.rows[root])
		assert.Nil(t, got.rows[root].RemovedAt, "the root's row was soft-deleted")
	})

	t.Run("a rejection applied twice leaves the store as the first left it", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRejection(ctx, tree, childRow.ID, 3))
		before := readProjectionState(t, s, tree, root, child)

		require.NoError(t, s.ProjectRejection(ctx, tree, childRow.ID, 3))

		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a rejection that matches no row still records its version", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))

		require.NoError(t, s.ProjectRejection(ctx, tree, testNodeUUID(8), 3))

		got := readProjectionState(t, s, tree, root)
		assert.Equal(t, int64(3), got.version, "ProjectRejection returned nil; the projected version is not 3")
		require.NotNil(t, got.rows[root])
		assert.Nil(t, got.rows[root].RemovedAt)
	})

	t.Run("a rejection on a tree with no projection row creates it", func(t *testing.T) {
		s := newStore(t)

		require.NoError(t, s.ProjectRejection(context.Background(), tree, testNodeUUID(8), 2))

		got := readProjectionState(t, s, tree)
		assert.True(t, got.found)
		assert.Equal(t, int64(2), got.version)
	})

	t.Run("a rejection below the version is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 3))
		before := readProjectionState(t, s, tree, root, child)

		err := s.ProjectRejection(ctx, tree, childRow.ID, 2)

		var refused *ProjectionRefusedError
		require.ErrorAs(t, err, &refused)
		assert.Equal(t, ProjectionRefusedError{TreeID: tree, EventVersion: 2, ProjectedVersion: 3}, *refused)
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a rejection leaves an older tombstone of the row alone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, testNodeUUID(9), 3))
		before := readProjectionState(t, s, tree, child)

		require.NoError(t, s.ProjectRejection(ctx, tree, childRow.ID, 4))

		got := readProjectionState(t, s, tree, child)
		assert.Equal(t, int64(4), got.version)
		assert.Equal(t, before.rows[child], got.rows[child], "the rejection rewrote a tombstone")
	})

	t.Run("a cancelled insert or removal writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		before := readProjectionState(t, s, tree, root, child, recruit, grand)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		assert.ErrorIs(t, s.ProjectInsert(cancelled, grandRow, 4), context.Canceled, "ProjectInsert")
		assert.ErrorIs(t, s.ProjectRemoval(cancelled, tree, RemovalProjection{
			Users:       []string{child},
			Responsored: []Responsored{{UserID: recruit, NewSponsorID: root}},
		}, testNodeUUID(9), 4), context.Canceled, "ProjectRemoval")

		assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit, grand))
	})

	t.Run("a rejection with a cancelled context writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		before := readProjectionState(t, s, tree, root, child)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		require.ErrorIs(t, s.ProjectRejection(cancelled, tree, childRow.ID, 3), context.Canceled)

		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a rejection at event version 0 is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)

		err := s.ProjectRejection(context.Background(), tree, childRow.ID, 0)

		require.EqualError(t, err, "tree "+tree+" was given event version 0, below 1; nothing was written")
		assert.False(t, readProjectionState(t, s, tree).found)
	})

	t.Run("a redelivered row reports its own id, never an active-row index", func(t *testing.T) {
		slotted := makeUUIDNode(testNodeUUID(4), tree, testUserUUID(4), 1, ptr(root), ptr(root), intPtr(2))
		for _, row := range []TreeNodeRow{rootRow, slotted} {
			s := newStore(t)
			ctx := context.Background()
			if row.Depth > 0 {
				require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
			}
			require.NoError(t, s.ProjectInsert(ctx, row, 2))

			err := s.ProjectInsert(ctx, row, 2)

			require.ErrorIs(t, err, ErrNodeAlreadyProjected, "row %s", row.ID)
			for _, conflict := range []error{ErrActiveUserConflict, ErrSlotConflict, ErrRootConflict} {
				assert.NotErrorIs(t, err, conflict, "row %s", row.ID)
			}
		}
	})

	t.Run("a redelivered root whose own row is a tombstone reports its own id when another root is active", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.UndoRootProjection(ctx, tree, root, 1))
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(6), tree, testUserUUID(6), 0, nil, ptr(testUserUUID(6)), nil), 2))

		err := s.ProjectInsert(ctx, rootRow, 2)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		for _, conflict := range []error{ErrActiveUserConflict, ErrSlotConflict, ErrRootConflict} {
			assert.NotErrorIs(t, err, conflict)
		}
	})

	t.Run("a redelivered row whose own row is a tombstone reports its own id when another event holds the user and slot", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, testNodeUUID(9), 3))
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(5), tree, child, 1, ptr(root), ptr(root), intPtr(0)), 4))

		err := s.ProjectInsert(ctx, childRow, 4)

		require.ErrorIs(t, err, ErrNodeAlreadyProjected)
		for _, conflict := range []error{ErrActiveUserConflict, ErrSlotConflict, ErrRootConflict} {
			assert.NotErrorIs(t, err, conflict)
		}
	})

	t.Run("ProjectedRows returns the version and the active rows in depth order", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		version, found, rows, err := s.ProjectedRows(ctx, tree)

		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(2), version)
		assert.Equal(t, []string{root, child}, nodeUserIDs(rows))
	})

	t.Run("ProjectedRows leaves out a removed row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, testNodeUUID(9), 3))

		version, _, rows, err := s.ProjectedRows(ctx, tree)

		require.NoError(t, err)
		assert.Equal(t, int64(3), version)
		assert.Equal(t, []string{root}, nodeUserIDs(rows))
	})

	t.Run("ProjectedRows on a tree with no projection row reports none", func(t *testing.T) {
		s := newStore(t)

		version, found, rows, err := s.ProjectedRows(context.Background(), tree)

		require.NoError(t, err)
		assert.False(t, found)
		assert.Zero(t, version)
		assert.Empty(t, rows)
	})

	t.Run("a projection after ProjectedRows returns does not show in what it returned", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		version, _, rows, err := s.ProjectedRows(ctx, tree)
		require.NoError(t, err)
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))

		assert.Equal(t, int64(2), version)
		assert.Equal(t, []string{root, child}, nodeUserIDs(rows))
	})
	t.Run("a removal that names no users is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		before := readProjectionState(t, s, tree, root, child)

		err := s.ProjectRemoval(ctx, tree, RemovalProjection{}, testNodeUUID(9), 3)

		require.EqualError(t, err, "project removal in tree "+tree+": the removal names no users")
		assert.Equal(t, before, readProjectionState(t, s, tree, root, child))
	})

	t.Run("a removal tombstones and stamps every removed user", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, grandRow, 3))

		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{grand, child}}, testNodeUUID(9), 4))

		got := readProjectionState(t, s, tree, child, grand)
		assert.Equal(t, int64(4), got.version)
		for _, u := range []string{child, grand} {
			require.NotNil(t, got.rows[u])
			require.NotNil(t, got.rows[u].RemovedAt, "%s is still active", u)
			require.NotNil(t, got.rows[u].RemovedByEventID)
			assert.Equal(t, testNodeUUID(9), *got.rows[u].RemovedByEventID)
		}
		assert.True(t, got.rows[child].RemovedAt.Equal(*got.rows[grand].RemovedAt),
			"one removal's tombstones carry one removed_at: %s and %s", got.rows[child].RemovedAt, got.rows[grand].RemovedAt)
	})

	t.Run("a removal moves every relocated row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, grandRow, 3))

		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{
			Users:     []string{child},
			Relocated: []Relocation{{UserID: grand, ParentID: root, Position: 0, Depth: 1}},
		}, testNodeUUID(9), 4))

		got := readProjectionState(t, s, tree, grand)
		require.NotNil(t, got.rows[grand])
		require.NotNil(t, got.rows[grand].ParentID)
		require.NotNil(t, got.rows[grand].Position)
		assert.Equal(t, root, *got.rows[grand].ParentID)
		assert.Equal(t, 0, *got.rows[grand].Position)
		assert.Equal(t, 1, got.rows[grand].Depth)
	})

	t.Run("a removal runs its tombstones, re-sponsors and relocations together", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		second := testUserUUID(5)
		secondRow := makeUUIDNode(testNodeUUID(5), tree, second, 2, ptr(child), ptr(child), intPtr(1))
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
		require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
		require.NoError(t, s.ProjectInsert(ctx, grandRow, 4))
		require.NoError(t, s.ProjectInsert(ctx, secondRow, 5))

		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{
			Users: []string{child},
			Responsored: []Responsored{
				{UserID: recruit, NewSponsorID: root},
				{UserID: grand, NewSponsorID: root},
				{UserID: second, NewSponsorID: root},
			},
			Relocated: []Relocation{
				{UserID: grand, ParentID: root, Position: 0, Depth: 1},
				{UserID: second, ParentID: root, Position: 2, Depth: 1},
			},
		}, testNodeUUID(9), 6))

		got := readProjectionState(t, s, tree, child, recruit, grand, second)
		assert.Equal(t, int64(6), got.version)
		require.NotNil(t, got.rows[child].RemovedAt)
		require.NotNil(t, got.rows[recruit].SponsorID)
		assert.Equal(t, root, *got.rows[recruit].SponsorID)
		for user, position := range map[string]int{grand: 0, second: 2} {
			require.NotNil(t, got.rows[user].ParentID)
			require.NotNil(t, got.rows[user].Position)
			assert.Equal(t, root, *got.rows[user].ParentID, user)
			assert.Equal(t, position, *got.rows[user].Position, user)
			assert.Equal(t, 1, got.rows[user].Depth, user)
			require.NotNil(t, got.rows[user].SponsorID)
			assert.Equal(t, root, *got.rows[user].SponsorID, user)
		}
	})

	for _, tc := range []struct {
		name   string
		r      RemovalProjection
		wantIs error
	}{
		{"a relocation names no active row", RemovalProjection{Users: []string{child},
			Relocated: []Relocation{{UserID: testUserUUID(7), ParentID: root, Position: 1, Depth: 1}}}, nil},
		{"a re-sponsored user has no active row", RemovalProjection{Users: []string{child},
			Responsored: []Responsored{{UserID: testUserUUID(7), NewSponsorID: root}}}, nil},
		{"a relocation targets an occupied slot", RemovalProjection{Users: []string{child},
			Relocated: []Relocation{{UserID: grand, ParentID: root, Position: 1, Depth: 1}}}, ErrSlotConflict},
		{"a removed user has no active row", RemovalProjection{Users: []string{child, testUserUUID(7)}}, nil},
		{"a re-sponsored user is the removed user", RemovalProjection{Users: []string{child},
			Responsored: []Responsored{{UserID: child, NewSponsorID: root}}}, nil},
		{"a relocated user is the removed user", RemovalProjection{Users: []string{child},
			Relocated: []Relocation{{UserID: child, ParentID: root, Position: 5, Depth: 1}}}, nil},
		{"two relocations swap their slots", RemovalProjection{Users: []string{grand},
			Relocated: []Relocation{
				{UserID: child, ParentID: root, Position: 1, Depth: 1},
				{UserID: recruit, ParentID: root, Position: 0, Depth: 1},
			}}, ErrSlotConflict},
	} {
		t.Run("a removal fails after the tombstone step and writes nothing when "+tc.name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
			require.NoError(t, s.ProjectInsert(ctx, childRow, 2))
			require.NoError(t, s.ProjectInsert(ctx, recruitRow, 3))
			require.NoError(t, s.ProjectInsert(ctx, grandRow, 4))
			before := readProjectionState(t, s, tree, root, child, recruit, grand)

			err := s.ProjectRemoval(ctx, tree, tc.r, testNodeUUID(9), 5)

			require.Error(t, err)
			if tc.wantIs != nil {
				assert.ErrorIs(t, err, tc.wantIs)
			} else {
				assert.NotErrorIs(t, err, ErrSlotConflict)
			}
			assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit, grand))
		})
	}

	t.Run("an uppercase tree id reaches the same projection", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		upper := strings.ToUpper(tree)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		version, found, err := s.ProjectedVersion(ctx, upper)
		require.NoError(t, err)
		assert.True(t, found, "ProjectedVersion found no projection row under tree %s", upper)
		assert.Equal(t, int64(2), version)

		version, found, rows, err := s.ProjectedRows(ctx, upper)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(2), version)
		assert.Equal(t, []string{root, child}, nodeUserIDs(rows))

		require.NoError(t, s.ProjectRemoval(ctx, upper, onlyChild, testNodeUUID(9), 3))

		got := readProjectionState(t, s, tree, child)
		assert.Equal(t, int64(3), got.version)
		require.NotNil(t, got.rows[child])
		assert.NotNil(t, got.rows[child].RemovedAt)
	})

	t.Run("a refusal under an uppercase tree id reports the tree id as given", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		upper := strings.ToUpper(tree)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 3))
		low := childRow
		low.TreeID = upper

		calls := []struct {
			name string
			call func() error
		}{
			{"ProjectInsert", func() error { return s.ProjectInsert(ctx, low, 2) }},
			{"ProjectRemoval", func() error { return s.ProjectRemoval(ctx, upper, onlyChild, testNodeUUID(9), 2) }},
			{"ProjectRejection", func() error { return s.ProjectRejection(ctx, upper, childRow.ID, 2) }},
		}
		for _, c := range calls {
			var refused *ProjectionRefusedError
			require.ErrorAs(t, c.call(), &refused, c.name)
			assert.Equal(t, upper, refused.TreeID, "%s reported tree %s, want %s as given", c.name, refused.TreeID, upper)
		}
	})

	t.Run("removal and undo messages report uppercase ids as given", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		up := strings.ToUpper
		r := hexUser(1)
		upTree, upRoot, upAbsent := up(tree), up(r), up(hexUser(5))

		err := s.ProjectRemoval(ctx, upTree, RemovalProjection{}, testNodeUUID(9), 1)
		assert.EqualError(t, err, fmt.Sprintf("project removal in tree %s: the removal names no users", upTree))

		err = s.ProjectRemoval(ctx, upTree, onlyChild, testNodeUUID(9), 0)
		assert.EqualError(t, err, fmt.Sprintf(
			"tree %s was given event version 0, below 1; nothing was written", upTree))

		err = s.UndoRootProjection(ctx, upTree, upRoot, 1)
		assert.EqualError(t, err, fmt.Sprintf(
			"tree %s has no projection row; the root row for %s at version 1 was not deleted", upTree, upRoot))

		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(11), tree, r, 0, nil, ptr(r), nil), 1))

		err = s.UndoRootProjection(ctx, upTree, upRoot, 2)
		assert.EqualError(t, err, fmt.Sprintf(
			"tree %s has projected version 1, not 2; the root row for %s was not deleted", upTree, upRoot))

		err = s.UndoRootProjection(ctx, upTree, upAbsent, 1)
		assert.EqualError(t, err, fmt.Sprintf(
			"soft delete for root %s in tree %s matched 0 active rows; the projected version was not changed",
			upAbsent, upTree))

		err = s.ProjectRemoval(ctx, upTree, RemovalProjection{Users: []string{upAbsent}}, testNodeUUID(9), 2)
		assert.EqualError(t, err, fmt.Sprintf(
			"tombstoning removed users [%s] in tree %s matched 0 of 1 active rows", upAbsent, upTree))

		below := fmt.Sprintf("tree %s was given event version 0, below 1; nothing was written", upTree)
		upRow := makeUUIDNode(testNodeUUID(12), upTree, hexUser(2), 1, ptr(r), ptr(r), intPtr(0))
		assert.EqualError(t, s.ProjectInsert(ctx, upRow, 0), below, "ProjectInsert")
		assert.EqualError(t, s.ProjectRejection(ctx, upTree, testNodeUUID(12), 0), below, "ProjectRejection")
		assert.EqualError(t, s.UndoRootProjection(ctx, upTree, upRoot, 0), below, "UndoRootProjection")
	})

	t.Run("a row projected under an uppercase tree id advances the canonical version", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		row := rootRow
		row.TreeID = strings.ToUpper(tree)

		require.NoError(t, s.ProjectInsert(ctx, row, 1))

		got := readProjectionState(t, s, tree)
		assert.True(t, got.found, "ProjectInsert under tree %s left no projection row for %s", row.TreeID, tree)
		assert.Equal(t, int64(1), got.version)
	})

	t.Run("a removal writes uppercase sponsor and parent ids in canonical form", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		r, c, rec, g := hexUser(1), hexUser(2), hexUser(3), hexUser(4)
		up := strings.ToUpper
		require.NoError(t, s.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(11), tree, r, 0, nil, ptr(r), nil), 1))
		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(12), tree, c, 1, ptr(r), ptr(r), intPtr(0)), 2))
		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(13), tree, rec, 1, ptr(r), ptr(c), intPtr(1)), 3))
		require.NoError(t, s.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(14), tree, g, 2, ptr(c), ptr(c), intPtr(0)), 4))

		require.NoError(t, s.ProjectRemoval(ctx, tree, RemovalProjection{
			Users:       []string{c},
			Responsored: []Responsored{{UserID: rec, NewSponsorID: up(r)}},
			Relocated:   []Relocation{{UserID: g, ParentID: up(r), Position: 0, Depth: 1}},
		}, testNodeUUID(19), 5))

		got := readProjectionState(t, s, tree, rec, g)
		require.NotNil(t, got.rows[rec])
		require.NotNil(t, got.rows[rec].SponsorID)
		assert.Equal(t, r, *got.rows[rec].SponsorID)
		require.NotNil(t, got.rows[g])
		require.NotNil(t, got.rows[g].ParentID)
		assert.Equal(t, r, *got.rows[g].ParentID)
	})

	t.Run("an uppercase rejected event id soft-deletes that event's row", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		require.NoError(t, s.ProjectRejection(ctx, tree, strings.ToUpper(childRow.ID), 3))

		got := readProjectionState(t, s, tree, child)
		require.NotNil(t, got.rows[child])
		assert.NotNil(t, got.rows[child].RemovedAt, "the rejection of %s left the row active", strings.ToUpper(childRow.ID))
	})

	t.Run("a removal stamps an uppercase removal event id in canonical form", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		event := testNodeUUID(9)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		require.NoError(t, s.ProjectRemoval(ctx, tree, onlyChild, strings.ToUpper(event), 3))

		got := readProjectionState(t, s, tree, child)
		require.NotNil(t, got.rows[child])
		require.NotNil(t, got.rows[child].RemovedByEventID)
		assert.Equal(t, event, *got.rows[child].RemovedByEventID)
	})

	t.Run("an uppercase tree id reaches the same row for a rejection and an undo", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		upper := strings.ToUpper(tree)
		require.NoError(t, s.ProjectInsert(ctx, rootRow, 1))
		require.NoError(t, s.ProjectInsert(ctx, childRow, 2))

		require.NoError(t, s.ProjectRejection(ctx, upper, childRow.ID, 3))
		require.NoError(t, s.UndoRootProjection(ctx, upper, root, 3))

		got := readProjectionState(t, s, tree, root, child)
		assert.Equal(t, int64(2), got.version)
		require.NotNil(t, got.rows[child])
		assert.NotNil(t, got.rows[child].RemovedAt, "the rejection under tree %s left the row active", upper)
		require.NotNil(t, got.rows[root])
		assert.NotNil(t, got.rows[root].RemovedAt, "the undo under tree %s left the root active", upper)
	})

	activeIDs := func(t *testing.T, s TreeStore) []string {
		t.Helper()
		rows, err := s.GetByTree(context.Background(), tree)
		require.NoError(t, err)
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		slices.Sort(ids)
		return ids
	}

	bad, controls := badIDWrites(tree, root, child, recruit)
	for _, w := range controls {
		t.Run("the control "+w.name+" is accepted and writes", func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			seedBadIDTree(t, s, tree, root, child, recruit)
			before := readProjectionState(t, s, tree, root, child, recruit)
			beforeIDs := activeIDs(t, s)

			require.NoError(t, w.write(ctx, s))
			assert.NotEqual(t, before, readProjectionState(t, s, tree, root, child, recruit),
				"projection state of tree %s after an accepted write", tree)
			assert.NotEqual(t, beforeIDs, activeIDs(t, s), "active rows in tree %s after an accepted write", tree)
		})
	}

	for _, w := range bad {
		t.Run("a write with a non-UUID "+w.name+" is refused and writes nothing", func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			seedBadIDTree(t, s, tree, root, child, recruit)
			before := readProjectionState(t, s, tree, root, child, recruit)
			beforeIDs := activeIDs(t, s)

			require.Error(t, w.write(ctx, s))
			assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
			assert.Equal(t, beforeIDs, activeIDs(t, s), "active rows in tree %s", tree)
		})
	}

	outOfRange, inRange := int4Writes(tree, root, child, recruit)
	for _, w := range inRange {
		t.Run(w.name+" is accepted and writes", func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			seedBadIDTree(t, s, tree, root, child, recruit)
			before := readProjectionState(t, s, tree, root, child, recruit)
			beforeIDs := activeIDs(t, s)

			require.NoError(t, w.write(ctx, s))
			assert.NotEqual(t, before, readProjectionState(t, s, tree, root, child, recruit),
				"projection state of tree %s after an accepted write", tree)
			assert.NotEqual(t, beforeIDs, activeIDs(t, s), "active rows in tree %s after an accepted write", tree)
			got, err := s.GetNode(ctx, tree, w.user)
			require.NoError(t, err)
			require.NotNil(t, got, "active row for %s", w.user)
			require.NotNil(t, got.Position, "position of %s", w.user)
			assert.Equal(t, w.position, *got.Position, "position of %s", w.user)
			assert.Equal(t, w.depth, got.Depth, "depth of %s", w.user)
		})
	}

	for _, w := range outOfRange {
		t.Run(w.name+" is refused and writes nothing", func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			seedBadIDTree(t, s, tree, root, child, recruit)
			before := readProjectionState(t, s, tree, root, child, recruit)
			beforeIDs := activeIDs(t, s)

			require.Error(t, w.write(ctx, s))
			assert.Equal(t, before, readProjectionState(t, s, tree, root, child, recruit))
			assert.Equal(t, beforeIDs, activeIDs(t, s), "active rows in tree %s", tree)
		})
	}

	t.Run("an insert below version 1 with a non-UUID tree id reports the version", func(t *testing.T) {
		s := newStore(t)
		err := s.ProjectInsert(context.Background(), makeUUIDNode(testNodeUUID(1), notAUUID, root, 0, nil, ptr(root), nil), 0)
		require.EqualError(t, err, "tree "+notAUUID+" was given event version 0, below 1; nothing was written")
	})

	t.Run("a removal with a non-UUID removal event id leaves the user active and unstamped", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		seedBadIDTree(t, s, tree, root, child, recruit)

		require.Error(t, s.ProjectRemoval(ctx, tree, onlyChild, notAUUID, 4))

		got, err := s.GetNodeIncludingRemoved(ctx, tree, child)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Nil(t, got.RemovedAt, "the refused removal tombstoned the user")
		assert.Nil(t, got.RemovedByEventID, "the refused removal stamped the user")
	})

	t.Run("a removal below version 1 with a non-UUID tree id reports the version", func(t *testing.T) {
		s := newStore(t)
		err := s.ProjectRemoval(context.Background(), notAUUID, onlyChild, testNodeUUID(9), 0)
		require.EqualError(t, err, "tree "+notAUUID+" was given event version 0, below 1; nothing was written")
	})

	t.Run("a rejection below version 1 with a non-UUID tree id reports the version", func(t *testing.T) {
		s := newStore(t)
		err := s.ProjectRejection(context.Background(), notAUUID, testNodeUUID(1), 0)
		require.EqualError(t, err, "tree "+notAUUID+" was given event version 0, below 1; nothing was written")
	})

	t.Run("an undo below version 1 with a non-UUID tree id reports the version", func(t *testing.T) {
		s := newStore(t)
		err := s.UndoRootProjection(context.Background(), notAUUID, root, 0)
		require.EqualError(t, err, "tree "+notAUUID+" was given event version 0, below 1; nothing was written")
	})
}

func TestMemoryTreeStore_ProjectionSuite(t *testing.T) {
	runTreeProjectionSuite(t, func(t *testing.T) TreeStore {
		return NewMemoryTreeStore()
	})
}

func TestPostgresTreeStore_ProjectionSuite(t *testing.T) {
	runTreeProjectionSuite(t, func(t *testing.T) TreeStore {
		return newTestPostgresTreeStore(t)
	})
}

func TestPostgresTreeStore_AFailedRejectionLeavesTheVersion(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()
	tree, root := testTreeUUID(1), testUserUUID(1)
	require.NoError(t, store.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))

	err := store.ProjectRejection(ctx, tree, "not-a-uuid", 2)

	require.Error(t, err, "Postgres accepted a rejected event ID that is not a UUID")
	assert.Equal(t, int64(1), readProjectionState(t, store, tree).version)
}

func TestPostgresTreeStore_ConcurrentFirstProjectionsQueue(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()

	for i := range 20 {
		tree := testTreeUUID(100 + i)
		root := testUserUUID(1)
		row := makeUUIDNode(testNodeUUID(100+i), tree, root, 0, nil, ptr(root), nil)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				errs <- store.ProjectInsert(ctx, row, 1)
			}()
		}
		close(start)
		first, second := <-errs, <-errs

		var failed error
		switch {
		case first == nil && second != nil:
			failed = second
		case second == nil && first != nil:
			failed = first
		default:
			t.Fatalf("round %d: the two projections returned %v and %v; want one nil and one error", i, first, second)
		}
		require.ErrorIs(t, failed, ErrNodeAlreadyProjected, "round %d", i)
		version, found, err := store.ProjectedVersion(ctx, tree)
		require.NoError(t, err)
		assert.True(t, found, "round %d", i)
		assert.Equal(t, int64(1), version, "round %d", i)
	}
}

// waitForLockWaiters polls until at least n backends in this database wait on
// a lock in a query that mentions tree_projections.
func waitForLockWaiters(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			 WHERE wait_event_type = 'Lock' AND datname = current_database() AND query LIKE '%tree_projections%'`,
		).Scan(&waiting))
		if waiting >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 10s, %d backends were waiting on a lock with a tree_projections query; want %d", waiting, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPostgresTreeStore_ALowerVersionQueuedBehindAHigherOneIsRefused(t *testing.T) {
	store := newTestPostgresTreeStore(t)
	ctx := context.Background()
	tree, root := testTreeUUID(1), testUserUUID(1)
	require.NoError(t, store.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), tree, root, 0, nil, ptr(root), nil), 1))
	require.NoError(t, store.ProjectInsert(ctx,
		makeUUIDNode(testNodeUUID(2), tree, testUserUUID(2), 1, ptr(root), ptr(root), intPtr(0)), 2))

	blocker, err := store.pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
	_, err = blocker.Exec(ctx, `SELECT 1 FROM tree_projections WHERE tree_id = $1 FOR UPDATE`, tree)
	require.NoError(t, err)

	higher, lower := make(chan error, 1), make(chan error, 1)
	go func() {
		higher <- store.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(4), tree, testUserUUID(4), 1, ptr(root), ptr(root), intPtr(2)), 4)
	}()
	waitForLockWaiters(t, store.pool, 1)
	go func() {
		lower <- store.ProjectInsert(ctx,
			makeUUIDNode(testNodeUUID(3), tree, testUserUUID(3), 1, ptr(root), ptr(root), intPtr(1)), 3)
	}()
	waitForLockWaiters(t, store.pool, 2)
	require.NoError(t, blocker.Commit(ctx))

	require.NoError(t, receive(t, higher, 10*time.Second))
	var refused *ProjectionRefusedError
	require.ErrorAs(t, receive(t, lower, 10*time.Second), &refused)
	assert.Equal(t, ProjectionRefusedError{TreeID: tree, EventVersion: 3, ProjectedVersion: 4}, *refused)
	version, _, err := store.ProjectedVersion(ctx, tree)
	require.NoError(t, err)
	assert.Equal(t, int64(4), version)
	row, err := store.GetNode(ctx, tree, testUserUUID(3))
	require.NoError(t, err)
	assert.Nil(t, row, "the lower version's row landed")
}
