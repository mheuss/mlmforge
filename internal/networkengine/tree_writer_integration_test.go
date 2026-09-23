package networkengine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writerIntegration holds what the writer integration tests share.
type writerIntegration struct {
	events *platform.PostgresEventStore
	store  *PostgresTreeStore
	dsn    string
	worker string
}

func newWriterIntegration(t *testing.T) *writerIntegration {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := findWorkerBinary(t)
	pool := pgContainer.NewPool(t)
	return &writerIntegration{
		events: platform.NewPostgresEventStore(pool),
		store:  NewPostgresTreeStore(pool),
		dsn:    pgContainer.DSN,
		worker: worker,
	}
}

// engine starts a fresh worker.
func (it *writerIntegration) engine(t *testing.T) *EngineClient {
	t.Helper()
	engine, err := NewEngineClient(context.Background(), it.worker)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })
	return engine
}

// writer builds a TreeWriter over a fresh worker and a Postgres lock.
func (it *writerIntegration) writer(t *testing.T) *TreeWriter {
	t.Helper()
	return NewTreeWriter(it.events, it.store, it.engine(t), NewPostgresTreeLocker(it.dsn))
}

func (it *writerIntegration) addUnilevelRoot(t *testing.T, tree, root string) {
	t.Helper()
	res, err := it.writer(t).AddRoot(context.Background(), AddRootRequest{
		TreeID: tree, UserID: root, SponsorID: root, TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
}

// receive waits for one value, and fails the test after timeout.
func receive[T any](t *testing.T, ch chan T, timeout time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("nothing arrived within %s", timeout)
		var zero T
		return zero
	}
}

// heldEngine holds its first AddNode until release is closed, and logs when
// that call returns.
type heldEngine struct {
	TreeEngineChecker
	log         *orderLog
	name        string
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

// open lets the held AddNode continue. It is safe to call more than once.
func (g *heldEngine) open() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func newHeldEngine(engine TreeEngineChecker, log *orderLog, name string) *heldEngine {
	return &heldEngine{
		TreeEngineChecker: engine, log: log, name: name,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
}

func (g *heldEngine) AddNode(ctx context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error {
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	err := g.TreeEngineChecker.AddNode(ctx, structure, userID, parentID, sponsorID, enrolledAt, opts...)
	g.log.add(g.name + " AddNode returned")
	return err
}

// loggingLocker records each lock request before passing it on.
type loggingLocker struct {
	TreeLocker
	log  *orderLog
	name string
}

func (l *loggingLocker) Lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	l.log.add(l.name + " lock requested")
	return l.TreeLocker.Lock(ctx, treeID)
}

// assertTwoWritersSerialise runs two placements under root. The first is held
// inside its projection. The second must not load until that projection
// returns.
func assertTwoWritersSerialise(t *testing.T, it *writerIntegration, firstTreeID, secondTreeID, root string) {
	t.Helper()
	ctx := context.Background()
	log := &orderLog{}
	gate := newHeldEngine(it.engine(t), log, "w1")
	w1 := NewTreeWriter(it.events, &loggingStore{TreeStore: it.store, log: log, name: "w1"},
		gate, NewPostgresTreeLocker(it.dsn))
	w2 := NewTreeWriter(it.events, &loggingStore{TreeStore: it.store, log: log, name: "w2"},
		it.engine(t), &loggingLocker{TreeLocker: NewPostgresTreeLocker(it.dsn), log: log, name: "w2"})
	place := func(w *TreeWriter, tree, user string, out chan error) {
		res, err := w.Place(ctx, PlaceRequest{TreeID: tree, UserID: user, ParentID: root, SponsorID: root, EnrolledAt: writeTime})
		if err == nil {
			err = errors.Join(res.ProjectionErr, res.ReleaseErr)
		}
		out <- err
	}
	t.Cleanup(gate.open)

	first := make(chan error, 1)
	go place(w1, firstTreeID, testUserUUID(2), first)
	select {
	case <-gate.entered:
	case err := <-first:
		t.Fatalf("the first writer returned before its projection was held: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatalf("the first writer's projection did not start within 30s: %v", log.snapshot())
	}

	second := make(chan error, 1)
	go place(w2, secondTreeID, testUserUUID(3), second)
	require.True(t, log.waitFor("w2 lock requested", 10*time.Second),
		"no \"w2 lock requested\" entry within 10s: %v", log.snapshot())
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, -1, log.indexOf("w2 load"),
		"the second writer loaded while the first held the tree: %v", log.snapshot())

	gate.open()
	require.NoError(t, receive(t, first, 30*time.Second))
	require.NoError(t, receive(t, second, 30*time.Second))

	entries := log.snapshot()
	projected, loaded := slices.Index(entries, "w1 AddNode returned"), slices.Index(entries, "w2 load")
	require.GreaterOrEqual(t, projected, 0, "%v", entries)
	assert.Less(t, projected, loaded, "\"w2 load\" came before \"w1 AddNode returned\": %v", entries)
}

func TestTreeWriter_RootPlacementAndRemoval(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, child := testTreeUUID(301), testUserUUID(1), testUserUUID(2)

	added, err := it.writer(t).AddRoot(ctx, AddRootRequest{
		TreeID: tree, UserID: root, SponsorID: root, TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	assert.Equal(t, int64(1), added.Version)

	placed, err := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)
	assert.Equal(t, int64(2), placed.Version)
	require.NotNil(t, placed.CaughtUp)
	assert.Equal(t, added.EventID, placed.CaughtUp.EventID)
	row, err := it.store.GetNode(ctx, tree, child)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.Depth)

	removed, err := it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: child, RemovedAt: writeTime.Add(2 * time.Hour)})
	require.NoError(t, err)
	require.NoError(t, removed.ProjectionErr)
	assert.Equal(t, int64(3), removed.Version)
	active, err := it.store.GetNode(ctx, tree, child)
	require.NoError(t, err)
	assert.Nil(t, active)
	stamped, err := it.store.GetNodeByRemovalEvent(ctx, tree, removed.EventID)
	require.NoError(t, err)
	assert.NotNil(t, stamped)

	stored, err := it.events.ReadStream(ctx, TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	types := make([]string, 0, len(stored))
	for _, e := range stored {
		types = append(types, e.Type)
	}
	assert.Equal(t, []string{EventTypeRootAdded, EventTypeNodePlaced, EventTypeNodeRemoved}, types)

	loaded, err := NewTreeLoader(it.store, it.engine(t)).LoadTree(ctx, tree, treeTypeUnilevel)
	require.NoError(t, err)
	assert.Equal(t, 1, loaded, "the store must reload with the root alone")
}

func TestTreeWriter_MatrixPlacementAtAnExplicitSlot(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, child, other := testTreeUUID(302), testUserUUID(1), testUserUUID(2), testUserUUID(3)
	width, spillover := 3, "breadth_first"
	_, err := it.writer(t).AddRoot(ctx, AddRootRequest{
		TreeID: tree, UserID: root, SponsorID: root, TreeType: treeTypeMatrix,
		MatrixWidth: &width, MatrixSpillover: &spillover, EnrolledAt: writeTime,
	})
	require.NoError(t, err)

	slot := 2
	placed, err := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: root, Position: &slot, EnrolledAt: writeTime,
	})
	require.NoError(t, err)
	require.NoError(t, placed.ProjectionErr)
	row, err := it.store.GetNode(ctx, tree, child)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.Position)
	assert.Equal(t, 2, *row.Position)

	first, third := 1, testUserUUID(4)
	next, err := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: third, ParentID: root, SponsorID: root, Position: &first, EnrolledAt: writeTime,
	})
	require.NoError(t, err)
	require.NoError(t, next.ProjectionErr)
	require.NotNil(t, next.CaughtUp, "CaughtUp is nil after the second matrix placement")
	assert.Equal(t, placed.EventID, next.CaughtUp.EventID)

	past := 3
	_, err = it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: other, ParentID: root, SponsorID: root, Position: &past, EnrolledAt: writeTime,
	})
	var engineErr *EngineError
	require.ErrorAs(t, err, &engineErr)
	assert.Equal(t, "INVALID_POSITION", engineErr.Code)
	stored, err := it.events.ReadStream(ctx, TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	assert.Len(t, stored, 3)
}

func TestTreeWriter_RefusesWhatTheEngineRefuses(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root := testTreeUUID(303), testUserUUID(1)
	it.addUnilevelRoot(t, tree, root)

	_, err := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: root, ParentID: root, SponsorID: root, EnrolledAt: writeTime,
	})

	var engineErr *EngineError
	require.ErrorAs(t, err, &engineErr)
	assert.Equal(t, "USER_ALREADY_EXISTS", engineErr.Code)
	stored, err := it.events.ReadStream(ctx, TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	assert.Len(t, stored, 1)
}

func TestTreeWriter_SerialisesTwoWritersOnOneTree(t *testing.T) {
	it := newWriterIntegration(t)
	tree, root := testTreeUUID(304), testUserUUID(1)
	it.addUnilevelRoot(t, tree, root)

	assertTwoWritersSerialise(t, it, tree, tree, root)

	stored, err := it.events.ReadStream(context.Background(), TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	assert.Len(t, stored, 3)
}

func TestTreeWriter_EquivalentSpellingsShareOneStreamAndOneLock(t *testing.T) {
	it := newWriterIntegration(t)
	tree, root := testTreeUUID(305), testUserUUID(1)
	it.addUnilevelRoot(t, tree, root)

	assertTwoWritersSerialise(t, it, strings.ToUpper(tree), tree, root)

	ctx := context.Background()
	stored, err := it.events.ReadStream(ctx, TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	assert.Len(t, stored, 3, "lower-case stream length")
	upper, err := it.events.ReadStream(ctx, TreeStreamName(strings.ToUpper(tree)), 1, 0)
	require.NoError(t, err)
	assert.Empty(t, upper)
}

func TestTreeWriter_ProjectsAnUnprojectedEventBeforeAppending(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, child, other := testTreeUUID(306), testUserUUID(1), testUserUUID(2), testUserUUID(3)
	it.addUnilevelRoot(t, tree, root)
	unprojected := appendTreeEvent(t, it.events, TreeStreamName(tree), 1, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: root, TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	log := &orderLog{}
	w := NewTreeWriter(&loggingEvents{EventStore: it.events, log: log},
		&loggingStore{TreeStore: it.store, log: log, name: "w"}, it.engine(t), NewPostgresTreeLocker(it.dsn))

	res, err := w.Place(ctx, PlaceRequest{TreeID: tree, UserID: other, ParentID: root, SponsorID: root, EnrolledAt: writeTime})

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	require.NotNil(t, res.CaughtUp)
	assert.Equal(t, unprojected.ID, res.CaughtUp.EventID)
	assert.Equal(t, int64(3), res.Version)
	insert, appendNew := log.indexOf("w insert "+child), log.indexOf("append at 3")
	require.GreaterOrEqual(t, insert, 0, "the unprojected placement was never inserted: %v", log.snapshot())
	assert.Less(t, insert, appendNew, "the earlier event must project before the new one is appended: %v", log.snapshot())
}

func TestTreeWriter_CompletesOnAOneConnectionPool(t *testing.T) {
	it := newWriterIntegration(t)
	require.Contains(t, it.dsn, "?", "the DSN must already carry a query string")
	url := it.dsn + "&pool_max_conns=1"
	pool, err := pgxpool.New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.EqualValues(t, 1, pool.Config().MaxConns)
	events, store := platform.NewPostgresEventStore(pool), NewPostgresTreeStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tree, root := testTreeUUID(307), testUserUUID(1)

	writes := []func(*TreeWriter) (WriteResult, error){
		func(w *TreeWriter) (WriteResult, error) {
			return w.AddRoot(ctx, AddRootRequest{TreeID: tree, UserID: root, SponsorID: root, TreeType: treeTypeUnilevel, EnrolledAt: writeTime})
		},
		func(w *TreeWriter) (WriteResult, error) {
			return w.Place(ctx, PlaceRequest{TreeID: tree, UserID: testUserUUID(2), ParentID: root, SponsorID: root, EnrolledAt: writeTime})
		},
	}
	for i, write := range writes {
		res, err := write(NewTreeWriter(events, store, it.engine(t), NewPostgresTreeLocker(url)))
		require.NoError(t, err, "write %d", i)
		require.NoError(t, res.ProjectionErr, "write %d", i)
		require.NoError(t, res.ReleaseErr, "write %d", i)
	}
}

func TestTreeWriter_ReRootsATreeWhoseOnlyRootWasRemoved(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, first, second := testTreeUUID(308), testUserUUID(1), testUserUUID(2)
	it.addUnilevelRoot(t, tree, first)

	removed, err := it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: first, RemovedAt: writeTime.Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, removed.ProjectionErr)
	rerooted, err := it.writer(t).AddRoot(ctx, AddRootRequest{
		TreeID: tree, UserID: second, SponsorID: second, TreeType: treeTypeUnilevel, EnrolledAt: writeTime.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, rerooted.ProjectionErr)
	require.NoError(t, rerooted.ReleaseErr)
	assert.Equal(t, int64(3), rerooted.Version)
	require.NotNil(t, rerooted.CaughtUp)
	assert.Equal(t, removed.EventID, rerooted.CaughtUp.EventID)

	engine := it.engine(t)
	loaded, err := NewTreeLoader(it.store, engine).LoadTree(ctx, tree, treeTypeUnilevel)
	require.NoError(t, err)
	assert.Equal(t, 1, loaded)
	pos, err := engine.GetPosition(ctx, tree, second)
	require.NoError(t, err)
	assert.EqualValues(t, 0, pos.Depth)
	gone, err := it.store.GetNode(ctx, tree, first)
	require.NoError(t, err)
	assert.Nil(t, gone)
}
