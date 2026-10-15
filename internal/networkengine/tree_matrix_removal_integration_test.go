package networkengine

import (
	"context"
	"encoding/json"
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
	width, spillover, zero := 2, "breadth_first", 0
	events := []platform.Event{
		versionedEvent(t, tree, 1, EventTypeRootAdded, RootAddedPayload{TreeID: tree, UserID: root, SponsorID: root,
			EnrolledAt: writeTime, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}),
		versionedEvent(t, tree, 2, EventTypeNodePlaced, NodePlacedPayload{TreeID: tree, UserID: a, ParentID: root,
			SponsorID: root, Position: &zero, TreeType: treeTypeMatrix, EnrolledAt: writeTime}),
		versionedEvent(t, tree, 3, EventTypeNodePlaced, NodePlacedPayload{TreeID: tree, UserID: b, ParentID: a,
			SponsorID: a, Position: &zero, TreeType: treeTypeMatrix, EnrolledAt: writeTime}),
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
