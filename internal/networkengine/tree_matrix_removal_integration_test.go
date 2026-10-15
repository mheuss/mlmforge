package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// versionedEvent builds an event of tree's stream at version.
func versionedEvent(t *testing.T, tree string, version int64, eventType string, payload any) platform.Event {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return platform.Event{ID: uuid.NewString(), Stream: TreeStreamName(tree), Type: eventType, Version: version, Payload: data}
}

// orNone renders an optional identifier for a failure message.
func orNone(id *string) string {
	if id == nil {
		return "none"
	}
	return *id
}

// requireRowsMatchEngine fails unless every active row in tree agrees with the
// engine's position for that user on parent and depth, and every non-root row
// on position and sponsor.
func requireRowsMatchEngine(t *testing.T, store TreeStore, engine TreeInspector, tree string) {
	t.Helper()
	ctx := context.Background()
	rows, err := store.GetByTree(ctx, tree)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		pos, err := engine.GetPosition(ctx, tree, row.UserID)
		require.NoError(t, err, "engine position of %s", row.UserID)
		assert.True(t, samePtrUUID(pos.ParentUserID, row.ParentID),
			"parent of %s: engine %s, row %s", row.UserID, orNone(pos.ParentUserID), orNone(row.ParentID))
		assert.Equal(t, int(pos.Depth), row.Depth, "depth of %s", row.UserID)
		if row.ParentID == nil {
			continue
		}
		if assert.NotNil(t, row.Position, "position of %s", row.UserID) {
			assert.Equal(t, *row.Position, pos.Position, "position of %s", row.UserID)
		}
		assert.True(t, samePtrUUID(pos.SponsorUserID, row.SponsorID),
			"sponsor of %s: engine %s, row %s", row.UserID, orNone(pos.SponsorUserID), orNone(row.SponsorID))
	}
}

// matrixConsumerEnv drives one matrix tree's events through a consumer over
// Postgres and a real worker.
type matrixConsumerEnv struct {
	store    *PostgresTreeStore
	engine   *EngineClient
	consumer *TreeEventConsumer
	tree     string
	version  int64
}

func newMatrixConsumerEnv(t *testing.T, tree string, width int) *matrixConsumerEnv {
	t.Helper()
	store := newTestPostgresTreeStore(t)
	engine, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })
	require.NoError(t, engine.CreateMatrixTree(context.Background(), tree, width, "breadth_first"))
	env := &matrixConsumerEnv{store: store, engine: engine, consumer: NewTreeEventConsumer(store, engine), tree: tree}
	spillover := "breadth_first"
	env.handle(t, EventTypeRootAdded, RootAddedPayload{TreeID: tree, UserID: testUserUUID(1), SponsorID: testUserUUID(1),
		EnrolledAt: writeTime, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover})
	return env
}

func (e *matrixConsumerEnv) handle(t *testing.T, eventType string, payload any) {
	t.Helper()
	e.version++
	require.NoError(t, e.consumer.HandleEvent(context.Background(), versionedEvent(t, e.tree, e.version, eventType, payload)),
		"event at version %d", e.version)
}

func (e *matrixConsumerEnv) place(t *testing.T, user, parent string, position int, after time.Duration) {
	t.Helper()
	e.handle(t, EventTypeNodePlaced, NodePlacedPayload{TreeID: e.tree, UserID: user, ParentID: parent, SponsorID: parent,
		Position: &position, TreeType: treeTypeMatrix, EnrolledAt: writeTime.Add(after)})
}

func (e *matrixConsumerEnv) remove(t *testing.T, user string, mode PruningMode) {
	t.Helper()
	e.handle(t, EventTypeNodeRemoved, NodeRemovedPayload{TreeID: e.tree, UserID: user, RemovedAt: writeTime,
		TreeType: treeTypeMatrix, PruningMode: mode})
}

func TestTreeConsumer_ARedeliveredHoldingTankRemovalConverges(t *testing.T) {
	env := newMatrixConsumerEnv(t, testTreeUUID(5825), 2)
	root, x, a := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	env.place(t, x, root, 0, time.Hour)
	env.place(t, a, x, 0, 2*time.Hour)
	removal := versionedEvent(t, env.tree, 4, EventTypeNodeRemoved, NodeRemovedPayload{TreeID: env.tree, UserID: x,
		RemovedAt: writeTime, TreeType: treeTypeMatrix, PruningMode: PruningHoldingTank})
	require.NoError(t, env.consumer.HandleEvent(context.Background(), removal))
	before, err := env.store.GetByTree(context.Background(), env.tree)
	require.NoError(t, err)

	err = env.consumer.HandleEvent(context.Background(), removal)

	require.NoError(t, err)
	after, err := env.store.GetByTree(context.Background(), env.tree)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	requireRowsMatchEngine(t, env.store, env.engine, env.tree)
}

func TestTreeConsumer_ProjectsAMatrixRemovalTheEngineAgreesWith(t *testing.T) {
	for i, tc := range []struct {
		mode   PruningMode
		active int
	}{
		{PruningPromoteEarliest, 5},
		{PruningHoldingTank, 1},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			env := newMatrixConsumerEnv(t, testTreeUUID(5821+i), 2)
			root, x, a, b, c, d := testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4), testUserUUID(5), testUserUUID(6)
			env.place(t, x, root, 0, time.Hour)
			env.place(t, a, x, 0, 2*time.Hour)
			env.place(t, b, x, 1, 3*time.Hour)
			env.place(t, c, a, 0, 4*time.Hour)
			env.place(t, d, a, 1, 5*time.Hour)

			env.remove(t, x, tc.mode)

			requireRowsMatchEngine(t, env.store, env.engine, env.tree)
			rows, err := env.store.GetByTree(context.Background(), env.tree)
			require.NoError(t, err)
			assert.Len(t, rows, tc.active)
		})
	}
}

// The removed parent is placed again before the redelivery, so the parent the
// redelivered event names has an active row. The redelivered event is at the
// projected version.
func TestTreeConsumer_ARedeliveredPlacementConvergesAfterAPromotionMovedItsNode_NotReachableThroughCatchUpToday(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryTreeStore()
	engine, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })
	tree, root, a, b := testTreeUUID(5823), testUserUUID(1), testUserUUID(2), testUserUUID(3)
	require.NoError(t, engine.CreateMatrixTree(ctx, tree, 2, "breadth_first"))
	consumer := NewTreeEventConsumer(store, engine)
	width, spillover, zero, one := 2, "breadth_first", 0, 1
	events := []platform.Event{
		versionedEvent(t, tree, 1, EventTypeRootAdded, RootAddedPayload{TreeID: tree, UserID: root, SponsorID: root,
			EnrolledAt: writeTime, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}),
		versionedEvent(t, tree, 2, EventTypeNodePlaced, NodePlacedPayload{TreeID: tree, UserID: a, ParentID: root,
			SponsorID: root, Position: &zero, TreeType: treeTypeMatrix, EnrolledAt: writeTime}),
		versionedEvent(t, tree, 3, EventTypeNodePlaced, NodePlacedPayload{TreeID: tree, UserID: b, ParentID: a,
			SponsorID: a, Position: &one, TreeType: treeTypeMatrix, EnrolledAt: writeTime}),
	}
	for _, ev := range events {
		require.NoError(t, consumer.HandleEvent(ctx, ev))
	}
	// Promote b into a's slot, then place a again at the root's slot 1, in the
	// engine and the store alike.
	moved, err := engine.RemoveMatrixNode(ctx, tree, a, PruningPromoteEarliest)
	require.NoError(t, err)
	require.NoError(t, store.ProjectRemoval(ctx, tree, RemovalProjection{Users: []string{a},
		Responsored: moved.Responsored, Relocated: moved.Relocated}, uuid.NewString(), 3))
	require.NoError(t, engine.AddNodeAt(ctx, tree, a, root, root, 1, writeTime.Unix()))
	require.NoError(t, store.InsertNode(ctx, TreeNodeRow{ID: uuid.NewString(), TreeID: tree, UserID: a,
		ParentID: ptr(root), SponsorID: ptr(root), Position: intPtr(1), Depth: 1, EnrolledAt: writeTime}))

	err = consumer.HandleEvent(ctx, events[2])

	require.NoError(t, err)
	requireRowsMatchEngine(t, store, engine, tree)
}

func TestTreeWriter_ReplaysAHoldingTankRemovalIntoAFreshWorker(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c := testTreeUUID(5824), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4)
	width, spillover := 3, "breadth_first"
	// Each write gets its own writer over a fresh worker.
	writer := func(engine TreeRebuilder) *TreeWriter {
		return NewTreeWriter(it.events, it.store, NewMemorySnapshotStore(), engine, NewPostgresTreeLocker(it.dsn))
	}
	res, err := writer(it.engine(t)).AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: root, SponsorID: root,
		TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: writeTime})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	for i, p := range []struct{ user, parent string }{{a, root}, {b, a}, {c, a}} {
		res, err := writer(it.engine(t)).Place(ctx, PlaceRequest{TreeID: tree, UserID: p.user, ParentID: p.parent,
			SponsorID: p.parent, Position: intPtr(i % 2), EnrolledAt: writeTime.Add(time.Duration(i+1) * time.Hour)})
		require.NoError(t, err)
		require.NoError(t, res.ProjectionErr)
	}
	first := it.engine(t)
	res, err = writer(first).Remove(ctx, RemoveRequest{TreeID: tree, UserID: a, RemovedAt: writeTime, PruningMode: PruningHoldingTank})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	before, err := first.GetHoldingTank(ctx, tree, "")
	require.NoError(t, err)
	require.Len(t, before, 3, "holding tank entries after the holding_tank removal of %s", a)

	fresh := it.engine(t)
	_, err = writer(fresh).Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover})
	require.NoError(t, err)
	after, err := fresh.GetHoldingTank(ctx, tree, "")
	require.NoError(t, err)

	assert.Equal(t, before, after)
}

// failOnceRemovalStore fails its first ProjectRemoval and passes every other
// call through.
type failOnceRemovalStore struct {
	TreeStore
	removals int
}

func (s *failOnceRemovalStore) ProjectRemoval(ctx context.Context, treeID string, r RemovalProjection,
	removalEventID string, eventVersion int64) error {
	s.removals++
	if s.removals == 1 {
		return errors.New("failOnceRemovalStore: first ProjectRemoval refused")
	}
	return s.TreeStore.ProjectRemoval(ctx, treeID, r, removalEventID, eventVersion)
}

// matrixRestoreCounter counts RestoreSnapshot calls and passes every call
// through.
type matrixRestoreCounter struct {
	TreeRebuilder
	restores int
}

func (e *matrixRestoreCounter) RestoreSnapshot(ctx context.Context, structure, treeType string, data json.RawMessage) error {
	e.restores++
	return e.TreeRebuilder.RestoreSnapshot(ctx, structure, treeType, data)
}

// seedRealSnapshot loads tree into a fresh worker, snapshots it, and stores the
// snapshot at version.
func seedRealSnapshot(t *testing.T, it *writerIntegration, tree string, width int, spillover string, version int64) {
	t.Helper()
	ctx := context.Background()
	engine := it.engine(t)
	_, err := NewTreeWriter(it.events, it.store, NewMemorySnapshotStore(), engine, NewPostgresTreeLocker(it.dsn)).
		Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover})
	require.NoError(t, err)
	taken, err := engine.TakeSnapshot(ctx, tree)
	require.NoError(t, err)
	require.NoError(t, it.snapshots.Put(ctx, Snapshot{TreeID: tree, Version: version, TreeType: taken.TreeType, Data: taken.Data}))
}

func TestTreeWriter_AMatrixRemovalWhoseProjectionFailedConvergesOnTheNextWrite(t *testing.T) {
	cases := []struct {
		mode     PruningMode
		snapshot bool
		active   int
	}{
		{PruningPromoteEarliest, false, 4},
		{PruningPromoteEarliest, true, 4},
		{PruningHoldingTank, false, 2},
		{PruningHoldingTank, true, 2},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s, snapshot %t", tc.mode, tc.snapshot), func(t *testing.T) {
			it := newWriterIntegration(t)
			ctx := context.Background()
			tree := testTreeUUID(5831 + i)
			root, a, b, c, d := testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4), testUserUUID(5)
			width, spillover := 2, "breadth_first"
			res, err := it.writer(t).AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: root, SponsorID: root,
				TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: writeTime})
			require.NoError(t, err)
			require.NoError(t, res.ProjectionErr)
			for _, p := range []struct {
				user, parent string
				position     int
				after        time.Duration
			}{{a, root, 0, time.Hour}, {b, a, 0, 2 * time.Hour}, {c, a, 1, 3 * time.Hour}} {
				res, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: p.user, ParentID: p.parent,
					SponsorID: p.parent, Position: intPtr(p.position), EnrolledAt: writeTime.Add(p.after)})
				require.NoError(t, err)
				require.NoError(t, res.ProjectionErr)
			}
			if tc.snapshot {
				seedRealSnapshot(t, it, tree, width, spillover, 4)
			}

			failing := &failOnceRemovalStore{TreeStore: it.store}
			removal, err := NewTreeWriter(it.events, failing, it.snapshots, it.engine(t), NewPostgresTreeLocker(it.dsn)).
				Remove(ctx, RemoveRequest{TreeID: tree, UserID: a, RemovedAt: writeTime, PruningMode: tc.mode})
			require.NoError(t, err)
			require.ErrorContains(t, removal.ProjectionErr, "failOnceRemovalStore: first ProjectRemoval refused")
			version, _, err := it.store.ProjectedVersion(ctx, tree)
			require.NoError(t, err)
			require.Equal(t, int64(4), version)

			engine := &matrixRestoreCounter{TreeRebuilder: it.engine(t)}
			next, err := NewTreeWriter(it.events, it.store, it.snapshots, engine, NewPostgresTreeLocker(it.dsn)).
				Place(ctx, PlaceRequest{TreeID: tree, UserID: d, ParentID: root, SponsorID: root,
					Position: intPtr(1), EnrolledAt: writeTime.Add(4 * time.Hour)})

			require.NoError(t, err)
			require.NoError(t, next.ProjectionErr)
			require.NotNil(t, next.CaughtUp)
			assert.Equal(t, removal.EventID, next.CaughtUp.EventID)
			wantRestores := 0
			if tc.snapshot {
				wantRestores = 1
			}
			assert.Equal(t, wantRestores, engine.restores, "load path")
			if tc.snapshot {
				assert.Empty(t, next.Warnings, "warnings from the next write")
				stored, found, err := it.snapshots.Get(ctx, tree)
				require.NoError(t, err)
				require.True(t, found, "a stored snapshot of %s after the next write", tree)
				assert.Equal(t, int64(4), stored.Version)
			}
			requireRowsMatchEngine(t, it.store, engine, tree)
			rows, err := it.store.GetByTree(ctx, tree)
			require.NoError(t, err)
			assert.Len(t, rows, tc.active)
		})
	}
}
