package networkengine

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newIntegrationDeps creates all dependencies for integration tests.
// Skips if either the database or the engine binary is unavailable.
func newIntegrationDeps(t *testing.T) (*platform.PostgresEventStore, *PostgresTreeStore, *EngineClient, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("EVENTSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("EVENTSTORE_TEST_DSN not set, skipping integration tests")
	}

	engineBinary := findWorkerBinary(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	// Clean slate.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")
	cleanup := platform.RunMigrationsForTest(t, dsn)
	t.Cleanup(cleanup)

	_, err = pool.Exec(ctx, "TRUNCATE events RESTART IDENTITY")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "TRUNCATE tree_nodes RESTART IDENTITY")
	require.NoError(t, err)

	eventStore := platform.NewPostgresEventStore(pool)
	treeStore := NewPostgresTreeStore(pool)

	engine, err := NewEngineClient(ctx, engineBinary)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })

	return eventStore, treeStore, engine, pool
}

func appendTreeEvent(t *testing.T, es *platform.EventStore, stream string, version int64, eventType string, payload any) platform.Event {
	t.Helper()

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	eventID := "00000000-0000-0000-0000-" + time.Now().Format("150405.000000")

	ne := []platform.NewEvent{{
		ID:      eventID,
		Type:    eventType,
		Payload: data,
	}}

	err = (*es).Append(context.Background(), stream, version, ne)
	require.NoError(t, err)

	events, err := (*es).ReadStream(context.Background(), stream, version+1, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	return events[0]
}

func TestTreePersistence_FullWritePath(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	// Create tree in engine.
	require.NoError(t, engine.CreateTree(ctx, "tree-1", "unilevel"))

	consumer := NewTreeEventConsumer(treeStore, engine)

	// Append and consume a root_added event.
	rootPayload := RootAddedPayload{
		TreeID:     "tree-1",
		UserID:     "00000000-0000-0000-0000-000000000001",
		SponsorID:  "00000000-0000-0000-0000-000000000001",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	es := platform.EventStore(eventStore)
	event := appendTreeEvent(t, &es, "tree-tree-1", 0, EventTypeRootAdded, rootPayload)
	require.NoError(t, consumer.HandleEvent(ctx, event))

	// Verify node exists in table.
	node, err := treeStore.GetNode(ctx, "tree-1", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	require.NotNil(t, node, "root should exist in adjacency table")
	assert.Equal(t, 0, node.Depth)

	// Verify node exists in engine.
	pos, err := engine.GetPosition(ctx, "tree-1", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	require.NotNil(t, pos)
	assert.Equal(t, 0, pos.Depth)
}

func TestTreePersistence_PlaceAndRemove(t *testing.T) {
	eventStore, treeStore, engine, pool := newIntegrationDeps(t)
	ctx := context.Background()

	require.NoError(t, engine.CreateTree(ctx, "tree-1", "unilevel"))
	consumer := NewTreeEventConsumer(treeStore, engine)
	es := platform.EventStore(eventStore)

	rootID := "00000000-0000-0000-0000-000000000001"
	childID := "00000000-0000-0000-0000-000000000002"

	// Place root.
	rootEvent := appendTreeEvent(t, &es, "tree-tree-1", 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: "tree-1", UserID: rootID, SponsorID: rootID,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, rootEvent))

	// Place child.
	childEvent := appendTreeEvent(t, &es, "tree-tree-1", 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: "tree-1", UserID: childID, ParentID: rootID, SponsorID: rootID,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, childEvent))

	// Verify child in table and engine.
	childNode, err := treeStore.GetNode(ctx, "tree-1", childID)
	require.NoError(t, err)
	require.NotNil(t, childNode)
	assert.Equal(t, 1, childNode.Depth)

	childPos, err := engine.GetPosition(ctx, "tree-1", childID)
	require.NoError(t, err)
	require.NotNil(t, childPos)

	// Remove child.
	removeEvent := appendTreeEvent(t, &es, "tree-tree-1", 2, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: "tree-1", UserID: childID, RemovedAt: time.Now(),
	})
	require.NoError(t, consumer.HandleEvent(ctx, removeEvent))

	// Verify child is soft-deleted in table (has removed_at).
	activeNode, err := treeStore.GetNode(ctx, "tree-1", childID)
	require.NoError(t, err)
	assert.Nil(t, activeNode, "child should not appear in active query after removal")

	var removedCount int
	err = pool.QueryRow(ctx,
		"SELECT count(*) FROM tree_nodes WHERE user_id = $1 AND removed_at IS NOT NULL",
		childID,
	).Scan(&removedCount)
	require.NoError(t, err)
	assert.Equal(t, 1, removedCount, "child should have removed_at set")
}

func TestTreePersistence_BulkLoadMatchesEventPath(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	require.NoError(t, engine.CreateTree(ctx, "tree-1", "unilevel"))
	consumer := NewTreeEventConsumer(treeStore, engine)
	es := platform.EventStore(eventStore)

	rootID := "00000000-0000-0000-0000-000000000001"
	childID := "00000000-0000-0000-0000-000000000002"
	grandchildID := "00000000-0000-0000-0000-000000000003"

	// Build tree via events.
	rootEvent := appendTreeEvent(t, &es, "tree-tree-1", 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: "tree-1", UserID: rootID, SponsorID: rootID,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, rootEvent))

	childEvent := appendTreeEvent(t, &es, "tree-tree-1", 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: "tree-1", UserID: childID, ParentID: rootID, SponsorID: rootID,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, childEvent))

	grandchildEvent := appendTreeEvent(t, &es, "tree-tree-1", 2, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: "tree-1", UserID: grandchildID, ParentID: childID, SponsorID: childID,
		EnrolledAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, grandchildEvent))

	// Capture positions from the event-built engine.
	rootPos, err := engine.GetPosition(ctx, "tree-1", rootID)
	require.NoError(t, err)
	childPos, err := engine.GetPosition(ctx, "tree-1", childID)
	require.NoError(t, err)
	grandchildPos, err := engine.GetPosition(ctx, "tree-1", grandchildID)
	require.NoError(t, err)

	// Stop engine and start a fresh one.
	require.NoError(t, engine.Stop())
	freshEngine, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = freshEngine.Stop() }()

	// Bulk-load from the adjacency table.
	loader := NewTreeLoader(treeStore, freshEngine)
	require.NoError(t, loader.LoadTree(ctx, "tree-1", "unilevel"))

	// Compare positions. The fresh engine should match the original.
	freshRootPos, err := freshEngine.GetPosition(ctx, "tree-1", rootID)
	require.NoError(t, err)
	assert.Equal(t, rootPos.Depth, freshRootPos.Depth)

	freshChildPos, err := freshEngine.GetPosition(ctx, "tree-1", childID)
	require.NoError(t, err)
	assert.Equal(t, childPos.Depth, freshChildPos.Depth)

	freshGrandchildPos, err := freshEngine.GetPosition(ctx, "tree-1", grandchildID)
	require.NoError(t, err)
	assert.Equal(t, grandchildPos.Depth, freshGrandchildPos.Depth)
}

func TestTreePersistence_EngineFailureRetry(t *testing.T) {
	_, treeStore, _, _ := newIntegrationDeps(t)
	ctx := context.Background()

	// Use a failN transport instead of a real engine to test retry behavior.
	transport := newFailNTransport(1)
	mockEngine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(treeStore, mockEngine)

	rootPayload := RootAddedPayload{
		TreeID:     "tree-1",
		UserID:     "00000000-0000-0000-0000-000000000001",
		SponsorID:  "00000000-0000-0000-0000-000000000001",
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	data, _ := json.Marshal(rootPayload)
	event := platform.Event{
		ID:      "00000000-0000-0000-0000-000000000099",
		Stream:  "tree-tree-1",
		Type:    EventTypeRootAdded,
		Version: 1,
		Payload: data,
	}

	err := consumer.HandleEvent(ctx, event)
	require.NoError(t, err)

	// Verify store projection succeeded.
	node, err := treeStore.GetNode(ctx, "tree-1", "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	require.NotNil(t, node, "store should have the node despite engine failure on first try")

	// Verify engine received 2 calls (initial fail + 1 retry success).
	assert.Len(t, transport.calls, 2)
}
