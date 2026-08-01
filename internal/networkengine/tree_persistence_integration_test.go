package networkengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventCounter generates unique, deterministic event UUIDs.
var eventCounter uint64

func nextEventUUID() string {
	n := atomic.AddUint64(&eventCounter, 1)
	return fmt.Sprintf("cccccccc-cccc-cccc-cccc-%012d", n)
}

// newIntegrationDeps creates all dependencies for integration tests.
// Skips if either the database or the engine binary is unavailable.
func newIntegrationDeps(t *testing.T) (*platform.PostgresEventStore, *PostgresTreeStore, *EngineClient, *pgxpool.Pool) {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}

	engineBinary := findWorkerBinary(t)

	pool := pgContainer.NewPool(t)
	eventStore := platform.NewPostgresEventStore(pool)
	treeStore := NewPostgresTreeStore(pool)

	engine, err := NewEngineClient(context.Background(), engineBinary)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })

	return eventStore, treeStore, engine, pool
}

func appendTreeEvent(t *testing.T, es platform.EventStore, stream string, version int64, eventType string, payload any) platform.Event {
	t.Helper()

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	eventID := nextEventUUID()

	ne := []platform.NewEvent{{
		ID:      eventID,
		Type:    eventType,
		Payload: data,
	}}

	err = es.Append(context.Background(), stream, version, ne)
	require.NoError(t, err)

	events, err := es.ReadStream(context.Background(), stream, version+1, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	return events[0]
}

func TestTreePersistence_FullWritePath(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootUserID := testUserUUID(1)

	// Create tree in engine using the UUID as structure name.
	require.NoError(t, engine.CreateTree(ctx, treeID, "unilevel"))

	consumer := NewTreeEventConsumer(treeStore, engine)

	rootPayload := RootAddedPayload{
		TreeID:     treeID,
		UserID:     rootUserID,
		SponsorID:  rootUserID,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	event := appendTreeEvent(t, eventStore, TreeStreamName(treeID), 0, EventTypeRootAdded, rootPayload)
	require.NoError(t, consumer.HandleEvent(ctx, event))

	// Verify node exists in table.
	node, err := treeStore.GetNode(ctx, treeID, rootUserID)
	require.NoError(t, err)
	require.NotNil(t, node, "root should exist in adjacency table")
	assert.Equal(t, 0, node.Depth)

	// Verify node exists in engine.
	pos, err := engine.GetPosition(ctx, treeID, rootUserID)
	require.NoError(t, err)
	require.NotNil(t, pos)
	assert.Equal(t, uint32(0), pos.Depth)
}

func TestTreePersistence_PlaceAndRemove(t *testing.T) {
	eventStore, treeStore, engine, pool := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootID := testUserUUID(1)
	childID := testUserUUID(2)
	stream := TreeStreamName(treeID)

	require.NoError(t, engine.CreateTree(ctx, treeID, "unilevel"))
	consumer := NewTreeEventConsumer(treeStore, engine)

	// Place root.
	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: rootID, SponsorID: rootID,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, rootEvent))

	// Place child.
	childEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: childID, ParentID: rootID, SponsorID: rootID,
		TreeType:   treeTypeUnilevel,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, childEvent))

	// Verify child in table and engine.
	childNode, err := treeStore.GetNode(ctx, treeID, childID)
	require.NoError(t, err)
	require.NotNil(t, childNode)
	assert.Equal(t, 1, childNode.Depth)

	childPos, err := engine.GetPosition(ctx, treeID, childID)
	require.NoError(t, err)
	require.NotNil(t, childPos)

	// Remove child.
	removeEvent := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: treeID, UserID: childID, RemovedAt: time.Now(),
	})
	require.NoError(t, consumer.HandleEvent(ctx, removeEvent))

	// Verify child is soft-deleted in table (has removed_at).
	activeNode, err := treeStore.GetNode(ctx, treeID, childID)
	require.NoError(t, err)
	assert.Nil(t, activeNode, "child should not appear in active query after removal")

	var removedCount int
	err = pool.QueryRow(ctx,
		"SELECT count(*) FROM tree_nodes WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NOT NULL",
		treeID, childID,
	).Scan(&removedCount)
	require.NoError(t, err)
	assert.Equal(t, 1, removedCount, "child should have removed_at set")
}

func TestTreePersistence_BulkLoadMatchesEventPath(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootID := testUserUUID(1)
	childID := testUserUUID(2)
	grandchildID := testUserUUID(3)
	stream := TreeStreamName(treeID)

	require.NoError(t, engine.CreateTree(ctx, treeID, "unilevel"))
	consumer := NewTreeEventConsumer(treeStore, engine)

	// Build tree via events.
	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: rootID, SponsorID: rootID,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, rootEvent))

	childEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: childID, ParentID: rootID, SponsorID: rootID,
		TreeType:   treeTypeUnilevel,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, childEvent))

	grandchildEvent := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: grandchildID, ParentID: childID, SponsorID: childID,
		TreeType:   treeTypeUnilevel,
		EnrolledAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, consumer.HandleEvent(ctx, grandchildEvent))

	// Capture positions from the event-built engine.
	rootPos, err := engine.GetPosition(ctx, treeID, rootID)
	require.NoError(t, err)
	childPos, err := engine.GetPosition(ctx, treeID, childID)
	require.NoError(t, err)
	grandchildPos, err := engine.GetPosition(ctx, treeID, grandchildID)
	require.NoError(t, err)

	// Stop engine and start a fresh one.
	require.NoError(t, engine.Stop())
	freshEngine, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = freshEngine.Stop() }()

	// Bulk-load from the adjacency table.
	loader := NewTreeLoader(treeStore, freshEngine)
	require.NoError(t, loader.LoadTree(ctx, treeID, "unilevel"))

	// Compare positions. The fresh engine should match the original.
	freshRootPos, err := freshEngine.GetPosition(ctx, treeID, rootID)
	require.NoError(t, err)
	assert.Equal(t, rootPos.Depth, freshRootPos.Depth)

	freshChildPos, err := freshEngine.GetPosition(ctx, treeID, childID)
	require.NoError(t, err)
	assert.Equal(t, childPos.Depth, freshChildPos.Depth)

	freshGrandchildPos, err := freshEngine.GetPosition(ctx, treeID, grandchildID)
	require.NoError(t, err)
	assert.Equal(t, grandchildPos.Depth, freshGrandchildPos.Depth)
}

func TestTreePersistence_EngineFailureRetry(t *testing.T) {
	_, treeStore, _, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootUserID := testUserUUID(1)

	// Use a failN transport instead of a real engine to test retry behavior.
	transport := newFailNTransport(1)
	mockEngine := NewEngineClientWithTransport(transport)
	consumer := NewTreeEventConsumer(treeStore, mockEngine)

	rootPayload := RootAddedPayload{
		TreeID:     treeID,
		UserID:     rootUserID,
		SponsorID:  rootUserID,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(rootPayload)
	require.NoError(t, err)
	event := platform.Event{
		ID:      nextEventUUID(),
		Stream:  TreeStreamName(treeID),
		Type:    EventTypeRootAdded,
		Version: 1,
		Payload: data,
	}

	err = consumer.HandleEvent(ctx, event)
	require.NoError(t, err)

	// Verify store projection succeeded.
	node, err := treeStore.GetNode(ctx, treeID, rootUserID)
	require.NoError(t, err)
	require.NotNil(t, node, "store should have the node despite engine failure on first try")

	// Verify engine received 2 calls (initial fail + 1 retry success).
	assert.Len(t, transport.calls, 2)
}

// TestTreePersistence_LoadMatrixTreeCreatesInEngine proves the HEU-533 fix
// end-to-end against the real worker: reconstructing a matrix tree from the
// adjacency store now routes through CreateMatrixTree (width + spillover)
// instead of plain CreateTree, which the worker rejected with MISSING_PARAM.
// Scope is the create step only. Multi-node replay placement is covered by
// TestTreePersistence_MatrixMultiNodeRoundTrip.
func TestTreePersistence_LoadMatrixTreeCreatesInEngine(t *testing.T) {
	_, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootID := testUserUUID(1)

	// Seed a matrix root directly, as if projected from a prior run's events.
	root := makeUUIDNode(testNodeUUID(1), treeID, rootID, 0, nil, ptr(rootID), nil)
	require.NoError(t, treeStore.InsertNode(ctx, root))

	// Before HEU-533 this errored at the create step. The engine has never
	// seen treeID, so this mirrors startup reconstruction.
	loader := NewTreeLoader(treeStore, engine)
	require.NoError(t, loader.LoadTree(ctx, treeID, "matrix", WithMatrixParams(3, "breadth_first")))

	// The root exists in the freshly reconstructed matrix tree.
	rootPos, err := engine.GetPosition(ctx, treeID, rootID)
	require.NoError(t, err)
	require.NotNil(t, rootPos)
	assert.Equal(t, uint32(0), rootPos.Depth)
}

// TestTreePersistence_MatrixMultiNodeRoundTrip proves HEU-534 end-to-end
// against the real worker: a multi-node matrix tree with admin-override
// placements reloads from the adjacency store with identical topology.
//
// The fixture is deliberately awkward and must not be simplified. Between them
// the rows cover a placement spillover would not choose, differing parent and
// sponsor, a sponsor deeper than the node it sponsors, and a slot at width-1 —
// see the inline notes on u3 and u4, which carry most of that load. Simplify
// the shape and a loader that re-derives placement, transposes parent and
// sponsor, or replays in depth order starts passing (HEU-534).
func TestTreePersistence_MatrixMultiNodeRoundTrip(t *testing.T) {
	_, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	const width = 3
	treeID := testTreeUUID(1)
	u1, u2, u3, u4, u5 := testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4), testUserUUID(5)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Rows as a prior run's events would have projected them.
	rows := []TreeNodeRow{
		// root
		makeUUIDNode(testNodeUUID(1), treeID, u1, 0, nil, ptr(u1), nil),
		// u2 under the root at slot 0
		makeUUIDNode(testNodeUUID(2), treeID, u2, 1, ptr(u1), ptr(u1), intPtr(0)),
		// u3 pushed a level down under u2 while the root still has free slots.
		// Spillover would never choose this. Parent and sponsor also differ.
		makeUUIDNode(testNodeUUID(3), treeID, u3, 2, ptr(u2), ptr(u1), intPtr(2)),
		// u4 sits at depth 1 but is sponsored by u3 at depth 2, so depth-ordered
		// replay would reach it before its sponsor exists. Also at width-1.
		makeUUIDNode(testNodeUUID(4), treeID, u4, 1, ptr(u1), ptr(u3), intPtr(2)),
		// u5 fills the root's last free slot.
		makeUUIDNode(testNodeUUID(5), treeID, u5, 1, ptr(u1), ptr(u1), intPtr(1)),
	}
	for i := range rows {
		rows[i].EnrolledAt = base.Add(time.Duration(i) * time.Hour)
		require.NoError(t, treeStore.InsertNode(ctx, rows[i]))
	}

	loader := NewTreeLoader(treeStore, engine)
	require.NoError(t, loader.LoadTree(ctx, treeID, "matrix", WithMatrixParams(width, "breadth_first")))

	// The root carries no sponsor in the engine by construction: AddRoot takes
	// no sponsor and the Rust root is sponsor: None. Assert it separately.
	rootPos, err := engine.GetPosition(ctx, treeID, u1)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), rootPos.Depth)
	assert.Nil(t, rootPos.ParentUserID, "root has no parent")
	assert.Nil(t, rootPos.SponsorUserID, "root has no sponsor")
	assert.Equal(t, rows[0].EnrolledAt.Unix(), rootPos.EnrolledAt, "root enrolled_at")

	// Every non-root node must match its stored row exactly. Named u2..u5 so a
	// failure points at the fixture comment rather than a bare UUID.
	for i, want := range rows[1:] {
		t.Run(fmt.Sprintf("u%d", i+2), func(t *testing.T) {
			got, err := engine.GetPosition(ctx, treeID, want.UserID)
			require.NoError(t, err)
			require.NotNil(t, got.ParentUserID)
			require.NotNil(t, got.SponsorUserID)

			assert.Equal(t, *want.ParentID, *got.ParentUserID, "parent")
			assert.Equal(t, *want.SponsorID, *got.SponsorUserID, "sponsor")
			assert.Equal(t, *want.Position, got.Position, "position")
			assert.Equal(t, uint32(want.Depth), got.Depth, "depth")

			// enrolled_at decides who gets promoted when a node is removed
			// (matrix.rs picks the minimum), so a reload that loses it builds
			// a tree that looks right and pays the wrong distributor later.
			assert.Equal(t, want.EnrolledAt.Unix(), got.EnrolledAt, "enrolled_at")
		})
	}

	// The engine must hold these five and nothing else. Every assertion above
	// asks about a node the fixture already knows; none of them would notice a
	// phantom the loader invented.
	downline, err := engine.GetDownline(ctx, treeID, u1, 0)
	require.NoError(t, err)
	assert.Len(t, downline, len(rows)-1, "engine holds exactly the stored nodes")
}

// TestTreePersistence_MatrixConsumerRoundTrip proves HEU-553 end-to-end
// against the real worker: live matrix node_placed events project the SAME
// topology into tree_nodes and the engine.
//
// The fixture is deliberately awkward and must not be simplified. It covers:
// a placement BFS spillover would not choose (u3 goes under u2 while the
// root still has a free slot — spillover from sponsor u1 would take that),
// a node whose sponsor differs from its parent (u3), a sponsor deeper than
// the node it sponsors (u4, sponsored by u3), a slot at width-1 (u4), and a
// malformed event mid-stream that must reach neither projection. Simplify
// the shape and a consumer that falls back to spillover, transposes parent
// and sponsor, or projects rejected events starts passing (HEU-553).
func TestTreePersistence_MatrixConsumerRoundTrip(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	const width = 3
	treeID := testTreeUUID(1)
	u1, u2, u3, u4, u5 := testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4), testUserUUID(5)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateMatrixTree(ctx, treeID, width, "breadth_first"))
	consumer := NewTreeEventConsumer(treeStore, engine)

	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: u1, SponsorID: u1, EnrolledAt: base,
	})
	require.NoError(t, consumer.HandleEvent(ctx, rootEvent))

	place := func(version int64, userID, parentID, sponsorID string, pos int, at time.Time) {
		t.Helper()
		ev := appendTreeEvent(t, eventStore, stream, version, EventTypeNodePlaced, NodePlacedPayload{
			TreeID: treeID, UserID: userID, ParentID: parentID, SponsorID: sponsorID,
			Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: at,
		})
		require.NoError(t, consumer.HandleEvent(ctx, ev))
	}

	// u2 under the root at slot 0.
	place(1, u2, u1, u1, 0, base.Add(1*time.Hour))

	// Malformed event mid-stream: matrix with no position. It must reject
	// before either projection and leave the consumer fully usable.
	badEvent := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: testUserUUID(9), ParentID: u1, SponsorID: u1,
		TreeType: treeTypeMatrix, EnrolledAt: base.Add(90 * time.Minute),
	})
	err := consumer.HandleEvent(ctx, badEvent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no position")
	assert.Contains(t, err.Error(), testUserUUID(9), "rejection names the poison event's user")
	rows, err := treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "rejected event left no row")

	// The event itself survives in the EventStore for diagnosis — rejection
	// happens at projection time, not append time.
	persisted, err := eventStore.ReadStream(ctx, stream, 3, 1)
	require.NoError(t, err)
	require.Len(t, persisted, 1, "rejected event stays persisted")
	assert.Equal(t, badEvent.ID, persisted[0].ID, "the persisted event is the rejected one")

	// u5 fills root slot 1. u3 goes a level down under u2 at slot 2 while
	// the root still has slot 2 free. u4 sits at depth 1, sponsored by the
	// deeper u3, at width-1.
	place(3, u5, u1, u1, 1, base.Add(2*time.Hour))
	place(4, u3, u2, u1, 2, base.Add(3*time.Hour))
	place(5, u4, u1, u3, 2, base.Add(4*time.Hour))

	// Store and engine agree, field by field, for every non-root node.
	// enrolled_at is load-bearing: matrix removal promotes by min enrolled_at,
	// so a shifted value pays the wrong distributor later with no error
	// (docs/development/network-engine.md "Timestamps").
	rows, err = treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)
	require.Len(t, rows, 5)
	// Subtests are named u2..u5 so a failure points at the fixture comment
	// rather than a bare UUID (GetByTree order is undefined, so a map, not
	// the index, supplies the name).
	names := map[string]string{u2: "u2", u3: "u3", u4: "u4", u5: "u5"}
	for _, want := range rows {
		if want.UserID == u1 {
			continue
		}
		name, ok := names[want.UserID]
		if !ok {
			// A user the fixture never placed is itself the divergence
			// case — fail under the raw UUID rather than anonymously.
			name = want.UserID
		}
		t.Run(name, func(t *testing.T) {
			got, err := engine.GetPosition(ctx, treeID, want.UserID)
			require.NoError(t, err)
			require.NotNil(t, got.ParentUserID)
			require.NotNil(t, got.SponsorUserID)
			assert.Equal(t, *want.ParentID, *got.ParentUserID, "parent")
			assert.Equal(t, *want.SponsorID, *got.SponsorUserID, "sponsor")
			assert.Equal(t, *want.Position, got.Position, "position")
			assert.Equal(t, uint32(want.Depth), got.Depth, "depth")
			assert.Equal(t, want.EnrolledAt.Unix(), got.EnrolledAt, "enrolled_at")
		})
	}

	// Root: the engine carries no sponsor by construction (HEU-534 §5).
	// Depth and enrolled_at are asserted here, on BOTH projections, because
	// the per-node loop skips the root — a zeroed root enrolled_at once
	// passed silently, and the store row is a separate write that can rot
	// independently of the engine call.
	rootPos, err := engine.GetPosition(ctx, treeID, u1)
	require.NoError(t, err)
	assert.Nil(t, rootPos.ParentUserID)
	assert.Nil(t, rootPos.SponsorUserID)
	assert.Equal(t, uint32(0), rootPos.Depth)
	assert.Equal(t, base.Unix(), rootPos.EnrolledAt, "root enrolled_at")

	var rootRow TreeNodeRow
	for _, r := range rows {
		if r.UserID == u1 {
			rootRow = r
		}
	}
	assert.Equal(t, base.Unix(), rootRow.EnrolledAt.Unix(), "stored root enrolled_at")
	assert.Equal(t, 0, rootRow.Depth, "stored root depth")

	// Exactly the stored nodes, no phantoms.
	downline, err := engine.GetDownline(ctx, treeID, u1, 0)
	require.NoError(t, err)
	assert.Len(t, downline, 4)
}

// TestTreePersistence_SlotConflictFailsCleanAndTreeReloads proves migration
// 000004's consequence: a second active claim on an occupied (tree, parent,
// position) dies at the store insert — before the engine — and the tree it
// protects is still loadable afterward. Without the index both rows land and
// reload preflight refuses the whole tree (HEU-553 triage, codex C3).
func TestTreePersistence_SlotConflictFailsCleanAndTreeReloads(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	const width = 3
	treeID := testTreeUUID(1)
	u1, u2, u3 := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateMatrixTree(ctx, treeID, width, "breadth_first"))
	consumer := NewTreeEventConsumer(treeStore, engine)

	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: u1, SponsorID: u1, EnrolledAt: base,
	})
	require.NoError(t, consumer.HandleEvent(ctx, rootEvent))

	pos := 0
	okEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: u2, ParentID: u1, SponsorID: u1,
		Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: base.Add(time.Hour),
	})
	require.NoError(t, consumer.HandleEvent(ctx, okEvent))

	// u3 claims the slot u2 holds. The store insert dies on the partial
	// unique index; the engine is never called for it.
	conflict := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: u3, ParentID: u1, SponsorID: u1,
		Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: base.Add(2 * time.Hour),
	})
	err := consumer.HandleEvent(ctx, conflict)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "idx_tree_nodes_tree_parent_position_active")

	rows, err := treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "conflicting claim left no row")

	// The conflicting event never reached the engine: the insert failed
	// first (store-before-engine, ADR-021). Checked before the restart,
	// which would otherwise erase the evidence.
	_, posErr := engine.GetPosition(ctx, treeID, u3)
	require.Error(t, posErr)
	assert.Contains(t, posErr.Error(), "USER_NOT_FOUND")

	// The store stayed clean, so a fresh engine reloads it. This is the
	// consequence the index exists for.
	require.NoError(t, engine.Stop())
	freshEngine, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = freshEngine.Stop() }()

	loader := NewTreeLoader(treeStore, freshEngine)
	require.NoError(t, loader.LoadTree(ctx, treeID, "matrix", WithMatrixParams(width, "breadth_first")))

	got, err := freshEngine.GetPosition(ctx, treeID, u2)
	require.NoError(t, err)
	require.NotNil(t, got.ParentUserID)
	assert.Equal(t, u1, *got.ParentUserID)
	assert.Equal(t, 0, got.Position)
}

// TestTreePersistence_DuplicateDeliveryPinsNonIdempotence documents, on
// purpose, that redelivering an already-projected event FAILS today: the
// row's id is the event ID, so the insert dies on tree_nodes_pkey and the
// engine is never reached. HEU-576 owns making redelivery converge; when
// it lands, this test's expectation flips from error to clean success.
func TestTreePersistence_DuplicateDeliveryPinsNonIdempotence(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	u1, u2 := testUserUUID(1), testUserUUID(2)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateMatrixTree(ctx, treeID, 3, "breadth_first"))
	consumer := NewTreeEventConsumer(treeStore, engine)

	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: u1, SponsorID: u1, EnrolledAt: base,
	})
	require.NoError(t, consumer.HandleEvent(ctx, rootEvent))

	pos := 0
	okEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: u2, ParentID: u1, SponsorID: u1,
		Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: base.Add(time.Hour),
	})
	require.NoError(t, consumer.HandleEvent(ctx, okEvent))

	// Redeliver the exact same event. The row's id IS the event ID, so an
	// identical redelivery dies on the primary key — before the
	// (tree_id, user_id) index is ever consulted. That distinction is the
	// discriminator HEU-576's idempotency needs: pkey collision means "this
	// exact event was already projected"; idx_tree_nodes_tree_user means "a
	// different event claims the same user", which is real corruption.
	// (Pkey-first depends on migration 000002 declaring the PK inline in
	// CREATE TABLE, ahead of both named unique indexes — Postgres checks
	// indexes in OID order, which tracks creation order in a fresh
	// database. A redelivery would violate the slot index too; the pkey
	// simply fires first.)
	err := consumer.HandleEvent(ctx, okEvent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tree_nodes_pkey")

	rows, err := treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "redelivery left no duplicate row")

	// The engine never saw the redelivery either: u1's downline is still
	// just u2.
	downline, err := engine.GetDownline(ctx, treeID, u1, 0)
	require.NoError(t, err)
	assert.Len(t, downline, 1, "engine untouched by the redelivery")
}

// TestTreePersistence_RejectedTreeLeavesEngineLoadable proves the operational
// claim preflight exists for, which no stub can: after a tree is refused, the
// worker is clean enough that a corrected load succeeds.
//
// The unit tests assert "preflight failure must make no engine calls" against
// a fake mutator. That pins the Go-side mechanism but not the consequence. If
// CreateMatrixTree ever leaked ahead of validation, the retry would hit
// TREE_EXISTS and the tree would be unloadable until the process restarted —
// the worker has no operation to drop a structure (HEU-557).
func TestTreePersistence_RejectedTreeLeavesEngineLoadable(t *testing.T) {
	_, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	u1, u2, u3 := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	rows := []TreeNodeRow{
		makeUUIDNode(testNodeUUID(1), treeID, u1, 0, nil, ptr(u1), nil),
		makeUUIDNode(testNodeUUID(2), treeID, u2, 1, ptr(u1), ptr(u1), intPtr(0)),
		// Depth 3 under a depth-0 parent. Preflight must refuse the tree.
		// (The old fixture double-claimed a slot; migration 000004 now stops
		// that at the insert, so the corruption moved to a field the
		// database cannot check.)
		makeUUIDNode(testNodeUUID(3), treeID, u3, 3, ptr(u1), ptr(u1), intPtr(1)),
	}
	for i := range rows {
		rows[i].EnrolledAt = base.Add(time.Duration(i) * time.Hour)
		require.NoError(t, treeStore.InsertNode(ctx, rows[i]))
	}

	loader := NewTreeLoader(treeStore, engine)
	err := loader.LoadTree(ctx, treeID, "matrix", WithMatrixParams(3, "breadth_first"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has depth 3 but parent")

	// The structure was never created. Assert on the specific code: GetPosition
	// also errors for a known structure missing that user, so a bare Error
	// check would pass even if the create had leaked.
	_, err = engine.GetPosition(ctx, treeID, u1)
	require.Error(t, err, "a refused tree must leave no structure behind")
	assert.Contains(t, err.Error(), "STRUCTURE_NOT_FOUND")

	// Correct the data the way an operator would, then retry. This is the step
	// that proves the engine is still usable: a leaked create would fail here.
	require.NoError(t, treeStore.DeleteNode(ctx, treeID, u3))
	require.NoError(t, loader.LoadTree(ctx, treeID, "matrix", WithMatrixParams(3, "breadth_first")))

	got, err := engine.GetPosition(ctx, treeID, u2)
	require.NoError(t, err)
	require.NotNil(t, got.ParentUserID)
	assert.Equal(t, u1, *got.ParentUserID)
	assert.Equal(t, 0, got.Position)
}
