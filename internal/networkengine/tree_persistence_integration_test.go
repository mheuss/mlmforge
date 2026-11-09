package networkengine

import (
	"context"
	"encoding/json"
	"errors"
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
	require.NoError(t, deliver(ctx, consumer, event))

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
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	// Place child.
	childEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: childID, ParentID: rootID, SponsorID: rootID,
		TreeType:   treeTypeUnilevel,
		EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, deliver(ctx, consumer, childEvent))

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
	require.NoError(t, deliver(ctx, consumer, removeEvent))

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

func TestTreePersistence_StreamLoadMatchesTheWritingWorker(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree := testTreeUUID(1)
	rootID, childID, grandchildID := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	it.addUnilevelRoot(t, tree, rootID)
	placed, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: childID, ParentID: rootID,
		SponsorID: rootID, EnrolledAt: writeTime.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)

	// Kept so its depths can be compared with the load worker's.
	writingEngine := it.engine(t)
	placed, err = NewTreeWriter(it.events, it.store, it.snapshots, writingEngine, NewPostgresTreeLocker(it.dsn)).
		Place(ctx, PlaceRequest{TreeID: tree, UserID: grandchildID, ParentID: childID, SponsorID: childID,
			EnrolledAt: writeTime.Add(2 * time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)

	loadEngine := it.engine(t)
	loaded, err := NewTreeWriter(it.events, it.store, it.snapshots, loadEngine, NewPostgresTreeLocker(it.dsn)).
		Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})
	require.NoError(t, err)
	assert.Equal(t, 3, loaded.Nodes)

	for _, user := range []string{rootID, childID, grandchildID} {
		want, err := writingEngine.GetPosition(ctx, tree, user)
		require.NoError(t, err)
		got, err := loadEngine.GetPosition(ctx, tree, user)
		require.NoError(t, err)
		assert.Equal(t, want.Depth, got.Depth, "depth of %s", user)
	}
}

func TestTreePersistence_EngineFailureRetry(t *testing.T) {
	_, treeStore, _, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootUserID := testUserUUID(1)

	// Use a failN transport instead of a real engine to test retry behavior.
	transport := newFailNTransport(1)
	mockEngine := newEngineClientWithTransport(transport)
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

	err = deliver(ctx, consumer, event)
	require.NoError(t, err)

	// Verify store projection succeeded.
	node, err := treeStore.GetNode(ctx, treeID, rootUserID)
	require.NoError(t, err)
	require.NotNil(t, node, "store should have the node despite engine failure on first try")

	// Verify engine received 2 calls (initial fail + 1 retry success).
	assert.Len(t, transport.calls, 2)
}

// TestTreePersistence_MatrixMultiNodeRoundTrip proves a multi-node matrix tree
// with admin-override placements rebuilds from its stream with identical
// topology on a real worker.
//
// The fixture is deliberately awkward and must not be simplified. The rows
// cover a placement spillover would not choose, differing parent and sponsor, a
// sponsor deeper than the node it sponsors, and a slot at width-1. Simplify the
// shape and a build that re-derives placement or transposes parent and sponsor
// starts passing.
func TestTreePersistence_MatrixMultiNodeRoundTrip(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()

	const width = 3
	treeID := testTreeUUID(1)
	u1, u2, u3, u4, u5 := testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4), testUserUUID(5)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// The rows the writes below must reproduce.
	rows := []TreeNodeRow{
		// root
		makeUUIDNode(testNodeUUID(1), treeID, u1, 0, nil, ptr(u1), nil),
		// u2 under the root at slot 0
		makeUUIDNode(testNodeUUID(2), treeID, u2, 1, ptr(u1), ptr(u1), intPtr(0)),
		// u3 pushed a level down under u2 while the root still has free slots.
		// Spillover would never choose this. Parent and sponsor also differ.
		makeUUIDNode(testNodeUUID(3), treeID, u3, 2, ptr(u2), ptr(u1), intPtr(2)),
		// u4 sits at depth 1 but is sponsored by u3 at depth 2. Also at width-1.
		makeUUIDNode(testNodeUUID(4), treeID, u4, 1, ptr(u1), ptr(u3), intPtr(2)),
		// u5 fills the root's last free slot.
		makeUUIDNode(testNodeUUID(5), treeID, u5, 1, ptr(u1), ptr(u1), intPtr(1)),
	}
	for i := range rows {
		rows[i].EnrolledAt = base.Add(time.Duration(i) * time.Hour)
	}

	spillover := "breadth_first"
	w := width
	added, err := it.writer(t).AddRoot(ctx, AddRootRequest{TreeID: treeID, UserID: u1, SponsorID: u1,
		TreeType: treeTypeMatrix, MatrixWidth: &w, MatrixSpillover: &spillover, EnrolledAt: rows[0].EnrolledAt})
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	for _, r := range rows[1:] {
		res, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: treeID, UserID: r.UserID, ParentID: *r.ParentID,
			SponsorID: *r.SponsorID, Position: r.Position, EnrolledAt: r.EnrolledAt})
		require.NoError(t, err, "placing %s", r.UserID)
		require.NoError(t, res.ProjectionErr)
	}

	engine := it.engine(t)
	_, err = NewTreeWriter(it.events, it.store, it.snapshots, engine, NewPostgresTreeLocker(it.dsn)).
		Load(ctx, LoadRequest{TreeID: treeID, TreeType: treeTypeMatrix, MatrixWidth: &w, MatrixSpillover: &spillover})
	require.NoError(t, err)

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
	// phantom the build invented.
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
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	place := func(version int64, userID, parentID, sponsorID string, pos int, at time.Time) {
		t.Helper()
		ev := appendTreeEvent(t, eventStore, stream, version, EventTypeNodePlaced, NodePlacedPayload{
			TreeID: treeID, UserID: userID, ParentID: parentID, SponsorID: sponsorID,
			Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: at,
		})
		require.NoError(t, deliver(ctx, consumer, ev))
	}

	// u2 under the root at slot 0.
	place(1, u2, u1, u1, 0, base.Add(1*time.Hour))

	// Malformed event mid-stream: matrix with no position. It must reject
	// before either projection and leave the consumer fully usable.
	badEvent := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: testUserUUID(9), ParentID: u1, SponsorID: u1,
		TreeType: treeTypeMatrix, EnrolledAt: base.Add(90 * time.Minute),
	})
	err := deliver(ctx, consumer, badEvent)
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
// position) dies at the store insert, before the engine, so the stored rows
// stay clean. The conflicting event stays at the head of the stream, so a load
// fails until reject-event clears it. Then the tree reloads. Without the index
// both rows land and load preflight refuses the whole tree (HEU-553 triage,
// codex C3).
func TestTreePersistence_SlotConflictFailsCleanAndTreeReloads(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	width, spillover := 3, "breadth_first"
	tree := testTreeUUID(1)
	u1, u2, u3 := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	load := LoadRequest{TreeID: tree, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}

	added, err := it.writer(t).AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: u1, SponsorID: u1,
		TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: base})
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	placed, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: u2, ParentID: u1, SponsorID: u1,
		Position: intPtr(0), EnrolledAt: base.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)

	engine := it.engine(t)
	_, err = NewTreeWriter(it.events, it.store, it.snapshots, engine, NewPostgresTreeLocker(it.dsn)).Load(ctx, load)
	require.NoError(t, err)

	// u3 claims the slot u2 holds. The writer would refuse it before the
	// append, so it is appended directly and delivered through the consumer.
	conflict := appendForeign(t, it, tree, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: tree, UserID: u3, ParentID: u1, SponsorID: u1,
		Position: intPtr(0), TreeType: treeTypeMatrix, EnrolledAt: base.Add(2 * time.Hour)})
	err = deliver(ctx, NewTreeEventConsumer(it.store, engine), conflict)
	assert.ErrorIs(t, err, ErrSlotConflict)

	rows, err := it.store.GetByTree(ctx, tree)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "conflicting claim left no row")

	// The insert failed before the engine call (store-before-engine, ADR-021).
	_, posErr := engine.GetPosition(ctx, tree, u3)
	require.True(t, isEngineCode(posErr, engineCodeUserNotFound),
		"the code lives in a field, not in the message text: %v", posErr)

	_, err = it.writer(t).Load(ctx, load)
	require.Error(t, err, "a load redelivers the stuck event and must fail on it")

	rejected, err := it.writer(t).Reject(ctx, RejectRequest{TreeID: tree, EventID: conflict.ID, Reason: "claims the slot u2 holds"})
	require.NoError(t, err)
	require.Equal(t, RejectOutcomeRejected, rejected.Outcome, "the retry returned %v", rejected.RetryErr)
	assert.ErrorIs(t, rejected.RetryErr, ErrSlotConflict)
	require.NoError(t, rejected.ProjectionErr)

	reload := it.engine(t)
	_, err = NewTreeWriter(it.events, it.store, it.snapshots, reload, NewPostgresTreeLocker(it.dsn)).Load(ctx, load)
	require.NoError(t, err)

	active, err := it.store.GetByTree(ctx, tree)
	require.NoError(t, err)
	users := make([]string, 0, len(active))
	for _, r := range active {
		users = append(users, r.UserID)
	}
	assert.ElementsMatch(t, []string{u1, u2}, users)
	got, err := reload.GetPosition(ctx, tree, u2)
	require.NoError(t, err)
	require.NotNil(t, got.ParentUserID)
	assert.Equal(t, u1, *got.ParentUserID)
	assert.Equal(t, 0, got.Position)
}

// TestTreePersistence_DuplicateDeliveryConverges pins that redelivering an
// already-projected event succeeds against the real worker and Postgres.
func TestTreePersistence_DuplicateDeliveryConverges(t *testing.T) {
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
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	pos := 0
	okEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: u2, ParentID: u1, SponsorID: u1,
		Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: base.Add(time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, okEvent))

	require.NoError(t, deliver(ctx, consumer, okEvent))

	rows, err := treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "redelivery left no duplicate row")

	downline, err := engine.GetDownline(ctx, treeID, u1, 0)
	require.NoError(t, err)
	assert.Len(t, downline, 1, "redelivery added no second engine node")
}

// TestTreePersistence_PreflightRefusalLeavesTheEngineLoadable proves that a load
// preflight refuses leaves no structure, and that a corrected load succeeds in
// the same worker.
func TestTreePersistence_PreflightRefusalLeavesTheEngineLoadable(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	width, spillover := 3, "breadth_first"
	tree := testTreeUUID(1)
	u1, u2, u3 := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	load := LoadRequest{TreeID: tree, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}

	added, err := it.writer(t).AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: u1, SponsorID: u1,
		TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: base})
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	placed, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: u2, ParentID: u1, SponsorID: u1,
		Position: intPtr(0), EnrolledAt: base.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)

	// Depth 3 under a depth-0 parent, with no event behind it.
	bad := makeUUIDNode(testNodeUUID(3), tree, u3, 3, ptr(u1), ptr(u1), intPtr(1))
	bad.EnrolledAt = base.Add(2 * time.Hour)
	require.NoError(t, insertNode(ctx, it.pool, bad))

	engine := it.engine(t)
	w := NewTreeWriter(it.events, it.store, it.snapshots, engine, NewPostgresTreeLocker(it.dsn))

	_, err = w.Load(ctx, load)
	require.Error(t, err)
	_, posErr := engine.GetPosition(ctx, tree, u1)
	require.True(t, isEngineCode(posErr, "STRUCTURE_NOT_FOUND"),
		"a refused load must leave no structure behind: %v", posErr)
	assert.ErrorContains(t, err, "has depth 3 but parent")

	require.NoError(t, tombstoneUser(ctx, it.store, tree, u3))
	_, err = w.Load(ctx, load)
	require.NoError(t, err)

	got, err := engine.GetPosition(ctx, tree, u2)
	require.NoError(t, err)
	require.NotNil(t, got.ParentUserID)
	assert.Equal(t, u1, *got.ParentUserID)
	assert.Equal(t, 0, got.Position)
}

// A recruit whose recruiter is removed must not keep a sponsor_id naming an
// absent user after the tree reloads.
func TestTreePersistence_RemovedSponsorStillReloads(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree := testTreeUUID(1)
	rootID, recruiterID, recruitID := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	it.addUnilevelRoot(t, tree, rootID)
	place := func(user, sponsor string, after time.Duration) {
		res, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: user, ParentID: rootID,
			SponsorID: sponsor, EnrolledAt: writeTime.Add(after)})
		require.NoError(t, err)
		require.NoError(t, res.ProjectionErr)
	}
	// Placed under the root so the recruiter stays childless and removable,
	// sponsored by the recruiter so the stored edge outlives its target.
	place(recruiterID, rootID, time.Hour)
	place(recruitID, recruiterID, 2*time.Hour)

	stored, err := it.store.GetNode(ctx, tree, recruitID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.SponsorID)
	require.Equal(t, recruiterID, *stored.SponsorID,
		"the stored edge under test must exist before the removal")

	removed, err := it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: recruiterID,
		RemovedAt: writeTime.Add(3 * time.Hour)})
	require.NoError(t, err)
	require.NoError(t, removed.ProjectionErr)

	after, err := it.store.GetNode(ctx, tree, recruitID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.NotNil(t, after.SponsorID)
	assert.Equal(t, rootID, *after.SponsorID,
		"the store must record the sponsor the engine moved the recruit to")

	loadEngine := it.engine(t)
	_, err = NewTreeWriter(it.events, it.store, it.snapshots, loadEngine, NewPostgresTreeLocker(it.dsn)).
		Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})
	require.NoError(t, err, "a tree that has lost a recruiter must still rebuild")

	reloaded, err := loadEngine.GetSponsor(ctx, tree, recruitID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, rootID, reloaded.UserID,
		"the rebuilt engine must agree with the store about who sponsors the recruit")
}

// A redelivered placement whose user has since been removed and placed again
// violates the primary key and both active unique indexes, against different
// rows. Which one Postgres reports is not pinned anywhere (HEU-794), so this
// asserts the outcome every answer must produce rather than the error identity
// one of them produces.
func TestTreePersistence_SupersededRedeliveryIsRefused(t *testing.T) {
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
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	pos := 0
	okEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: u2, ParentID: u1, SponsorID: u1,
		Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: base.Add(time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, okEvent))

	require.NoError(t, tombstoneUser(ctx, treeStore, treeID, u2))
	require.NoError(t, insertNode(ctx, treeStore.pool, TreeNodeRow{
		ID:         testNodeUUID(9),
		TreeID:     treeID,
		UserID:     u2,
		ParentID:   &u1,
		SponsorID:  &u1,
		Position:   &pos,
		Depth:      1,
		EnrolledAt: base.Add(2 * time.Hour),
	}))

	before, err := treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)

	rerr := deliver(ctx, consumer, okEvent)
	require.Error(t, rerr, "a superseded redelivery is refused")
	assert.True(t,
		errors.Is(rerr, ErrReplayedPlacement) ||
			errors.Is(rerr, ErrActiveUserConflict) ||
			errors.Is(rerr, ErrSlotConflict),
		"refused for a conflict rather than for some other reason: %v", rerr)

	after, err := treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "the refusal wrote no row")

	downline, err := engine.GetDownline(ctx, treeID, u1, 0)
	require.NoError(t, err)
	assert.Len(t, downline, 1, "the refusal added no engine node")
}

// gatedTransport refuses one op while gated and forwards everything else to
// the worker underneath.
//
// Call may run on a goroutine that did not set the gate.
type gatedTransport struct {
	EngineTransport
	op     string
	gated  atomic.Bool
	forced atomic.Int64
}

func (g *gatedTransport) Call(ctx context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	if g.gated.Load() && op == g.op {
		g.forced.Add(1)
		return nil, fmt.Errorf("simulated worker failure on %s", op)
	}
	return g.EngineTransport.Call(ctx, op, params)
}

// newGatedEngine spawns a real worker behind a gate on one op.
func newGatedEngine(t *testing.T, op string) (*EngineClient, *gatedTransport) {
	t.Helper()
	stdio, err := NewStdioTransport(findWorkerBinary(t))
	require.NoError(t, err)
	gate := &gatedTransport{EngineTransport: stdio, op: op}
	gate.gated.Store(true)
	engine, err := newCheckedClient(context.Background(), gate)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })
	return engine, gate
}

// The store write lands and the engine call does not. The redelivery must
// finish the engine side without writing a second row.
func TestTreePersistence_RedeliveryAfterPartialProjection(t *testing.T) {
	eventStore, treeStore, _, _ := newIntegrationDeps(t)
	ctx := context.Background()
	engine, gate := newGatedEngine(t, "add_node_at")

	treeID := testTreeUUID(1)
	u1, u2 := testUserUUID(1), testUserUUID(2)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateMatrixTree(ctx, treeID, 3, "breadth_first"))
	consumer := NewTreeEventConsumer(treeStore, engine)

	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: u1, SponsorID: u1, EnrolledAt: base,
	})
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	// Slot 1 rather than 0: EnginePosition.Position's zero value is 0, so the
	// closing assertion cannot fail on a consumer that drops the event's
	// position.
	pos := 1
	placed := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: u2, ParentID: u1, SponsorID: u1,
		Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: base.Add(time.Hour),
	})

	require.Error(t, deliver(ctx, consumer, placed), "the gated engine call fails")
	require.Positive(t, gate.forced.Load(), "the gate returned at least one refusal for add_node_at")

	stored, err := treeStore.GetNode(ctx, treeID, u2)
	require.NoError(t, err)
	require.NotNil(t, stored, "a row for the user exists after the failed delivery")
	_, perr := engine.GetPosition(ctx, treeID, u2)
	require.True(t, isEngineCode(perr, engineCodeUserNotFound),
		"the code lives in a field, not in the message text: %v", perr)

	gate.gated.Store(false)
	require.NoError(t, deliver(ctx, consumer, placed), "the redelivery converges")

	rows, err := treeStore.GetByTree(ctx, treeID)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "the redelivery wrote no second row")

	reread, err := treeStore.GetNode(ctx, treeID, u2)
	require.NoError(t, err)
	require.NotNil(t, reread)
	assert.Equal(t, placed.ID, reread.ID, "the active row carries this event's id")

	got, err := engine.GetPosition(ctx, treeID, u2)
	require.NoError(t, err)
	require.NotNil(t, got.ParentUserID)
	assert.Equal(t, u1, *got.ParentUserID, "the engine holds the event's parent")
	assert.Equal(t, pos, got.Position, "and the event's position")
}

// A placement redelivered after its node was removed must not resurrect it.
func TestTreePersistence_ReplayedPlacementRefused(t *testing.T) {
	eventStore, treeStore, engine, pool := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	u1, u2 := testUserUUID(1), testUserUUID(2)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Unilevel rather than matrix: a matrix removal fails before the engine
	// call, HEU-582.
	require.NoError(t, engine.CreateTree(ctx, treeID, treeTypeUnilevel))
	consumer := NewTreeEventConsumer(treeStore, engine)

	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: u1, SponsorID: u1, EnrolledAt: base,
	})
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	placed := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: u2, ParentID: u1, SponsorID: u1,
		TreeType: treeTypeUnilevel, EnrolledAt: base.Add(time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, placed))

	removed := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: treeID, UserID: u2, RemovedAt: base.Add(2 * time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, removed))

	err := deliver(ctx, consumer, placed)

	var refused *ProjectionRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, ProjectionRefusedError{TreeID: treeID, EventVersion: 2, ProjectedVersion: 3}, *refused)

	active, err := treeStore.GetNode(ctx, treeID, u2)
	require.NoError(t, err)
	assert.Nil(t, active, "GetNode returned a row for the user: %+v", active)

	_, perr := engine.GetPosition(ctx, treeID, u2)
	require.True(t, isEngineCode(perr, engineCodeUserNotFound),
		"the code lives in a field, not in the message text: %v", perr)

	var tombstones int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM tree_nodes WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NOT NULL",
		treeID, u2).Scan(&tombstones))
	assert.Equal(t, 1, tombstones, "tree_nodes holds %d removed rows for the user in this tree", tombstones)
}

// seedRecruiterAndRecruit builds root, a childless recruiter under it, and a
// recruit placed under the root but sponsored by the recruiter, so removing
// the recruiter moves a sponsor edge.
func seedRecruiterAndRecruit(
	t *testing.T,
	eventStore platform.EventStore,
	consumer *TreeEventConsumer,
	treeID, rootID, recruiterID, recruitID string,
	base time.Time,
) int64 {
	t.Helper()
	ctx := context.Background()
	stream := TreeStreamName(treeID)

	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: rootID, SponsorID: rootID, EnrolledAt: base,
	})
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	recruiterEvent := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: recruiterID, ParentID: rootID, SponsorID: rootID,
		TreeType: treeTypeUnilevel, EnrolledAt: base.Add(24 * time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, recruiterEvent))

	recruitEvent := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: recruitID, ParentID: rootID, SponsorID: recruiterID,
		TreeType: treeTypeUnilevel, EnrolledAt: base.Add(48 * time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, recruitEvent))
	return 3
}

// A removal that projected into both sides must survive being delivered again
// without moving the sponsor edge a second time.
func TestTreePersistence_RedeliveredRemovalConverges(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootID, recruiterID, recruitID := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateTree(ctx, treeID, treeTypeUnilevel))
	consumer := NewTreeEventConsumer(treeStore, engine)
	next := seedRecruiterAndRecruit(t, eventStore, consumer, treeID, rootID, recruiterID, recruitID, base)

	removeEvent := appendTreeEvent(t, eventStore, stream, next, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: treeID, UserID: recruiterID, RemovedAt: base.Add(72 * time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, removeEvent))

	// Without this, a converged redelivery and a recruiter the store never
	// held are indistinguishable from here.
	removedRow, err := treeStore.GetNodeIncludingRemoved(ctx, treeID, recruiterID)
	require.NoError(t, err)
	require.NotNil(t, removedRow, "GetNodeIncludingRemoved returned no row for the recruiter")
	require.NotNil(t, removedRow.RemovedAt, "recruiter row removed_at is nil")

	first, err := treeStore.GetNode(ctx, treeID, recruitID)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NotNil(t, first.SponsorID)
	require.Equal(t, rootID, *first.SponsorID, "recruit sponsor_id after the first delivery")

	require.NoError(t, deliver(ctx, consumer, removeEvent),
		"second HandleEvent of the same node_removed event returned an error")

	after, err := treeStore.GetNode(ctx, treeID, recruitID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.NotNil(t, after.SponsorID)
	assert.Equal(t, *first.SponsorID, *after.SponsorID, "recruit sponsor_id after the redelivery")
}

// The engine applied the removal and the store write never landed.
func TestTreePersistence_RedeliveredRemovalFailsWhenTheStoreNeverLanded(t *testing.T) {
	eventStore, treeStore, engine, _ := newIntegrationDeps(t)
	ctx := context.Background()

	treeID := testTreeUUID(1)
	rootID, recruiterID, recruitID := testUserUUID(1), testUserUUID(2), testUserUUID(3)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateTree(ctx, treeID, treeTypeUnilevel))
	consumer := NewTreeEventConsumer(treeStore, engine)
	next := seedRecruiterAndRecruit(t, eventStore, consumer, treeID, rootID, recruiterID, recruitID, base)

	// Straight to the engine, so the store keeps an active row for a user the
	// engine has already dropped.
	_, err := engine.RemoveNode(ctx, treeID, recruiterID)
	require.NoError(t, err)

	removeEvent := appendTreeEvent(t, eventStore, stream, next, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: treeID, UserID: recruiterID, RemovedAt: base.Add(72 * time.Hour),
	})

	err = deliver(ctx, consumer, removeEvent)

	var typed *RemovalNotProjectedError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, treeID, typed.TreeID)
	assert.Equal(t, recruiterID, typed.UserID)
	assert.Equal(t, removeEvent.ID, typed.EventID)

	still, err := treeStore.GetNode(ctx, treeID, recruiterID)
	require.NoError(t, err)
	assert.NotNil(t, still, "GetNode returned no row for the recruiter")

	orphaned, err := treeStore.GetNode(ctx, treeID, recruitID)
	require.NoError(t, err)
	require.NotNil(t, orphaned)
	require.NotNil(t, orphaned.SponsorID)
	assert.Equal(t, recruiterID, *orphaned.SponsorID, "recruit sponsor_id after the failed removal")
}

// E1 places U, R1 removes U, E2 places U again with the engine call failing,
// then R1 is redelivered. R1 projected in full before E2 arrived.
func TestTreePersistence_RedeliveredRemovalAfterAFailedReplacement(t *testing.T) {
	eventStore, treeStore, _, _ := newIntegrationDeps(t)
	ctx := context.Background()
	engine, gate := newGatedEngine(t, "add_node")
	gate.gated.Store(false)

	treeID := testTreeUUID(1)
	rootID, userID := testUserUUID(1), testUserUUID(2)
	stream := TreeStreamName(treeID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateTree(ctx, treeID, treeTypeUnilevel))
	consumer := NewTreeEventConsumer(treeStore, engine)

	rootEvent := appendTreeEvent(t, eventStore, stream, 0, EventTypeRootAdded, RootAddedPayload{
		TreeID: treeID, UserID: rootID, SponsorID: rootID, EnrolledAt: base,
	})
	require.NoError(t, deliver(ctx, consumer, rootEvent))

	e1 := appendTreeEvent(t, eventStore, stream, 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: userID, ParentID: rootID, SponsorID: rootID,
		TreeType: treeTypeUnilevel, EnrolledAt: base.Add(time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, e1))

	r1 := appendTreeEvent(t, eventStore, stream, 2, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: treeID, UserID: userID, RemovedAt: base.Add(2 * time.Hour),
	})
	require.NoError(t, deliver(ctx, consumer, r1))

	tomb, err := treeStore.GetNodeIncludingRemoved(ctx, treeID, userID)
	require.NoError(t, err)
	require.NotNil(t, tomb, "no row for the user after R1's first delivery")
	require.Equal(t, e1.ID, tomb.ID, "the newest row for the user after R1's first delivery")
	require.NotNil(t, tomb.RemovedAt, "E1's row is still active after R1's first delivery")
	require.NotNil(t, tomb.RemovedByEventID, "E1's tombstone carries no removal stamp")
	require.Equal(t, r1.ID, *tomb.RemovedByEventID, "E1's tombstone stamp")

	gate.gated.Store(true)
	e2 := appendTreeEvent(t, eventStore, stream, 3, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: treeID, UserID: userID, ParentID: rootID, SponsorID: rootID,
		TreeType: treeTypeUnilevel, EnrolledAt: base.Add(3 * time.Hour),
	})
	require.Error(t, deliver(ctx, consumer, e2), "the gated engine call fails")
	require.Positive(t, gate.forced.Load(), "the gate returned at least one refusal for add_node")
	gate.gated.Store(false)

	active, err := treeStore.GetNode(ctx, treeID, userID)
	require.NoError(t, err)
	require.NotNil(t, active, "no active row for the user after E2's failed delivery")
	require.Equal(t, e2.ID, active.ID, "the active row after E2's failed delivery")
	_, perr := engine.GetPosition(ctx, treeID, userID)
	require.True(t, isEngineCode(perr, engineCodeUserNotFound),
		"engine GetPosition for the user after E2's failed delivery returned %v", perr)

	require.NoError(t, deliver(ctx, consumer, r1), "the redelivered removal")

	survivor, err := treeStore.GetNode(ctx, treeID, userID)
	require.NoError(t, err)
	require.NotNil(t, survivor, "no active row for the user after R1's redelivery")
	assert.Equal(t, e2.ID, survivor.ID, "the active row after R1's redelivery")
}
