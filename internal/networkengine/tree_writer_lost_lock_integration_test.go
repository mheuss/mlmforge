package networkengine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockKillingEvents ends the tree lock's backend after its first successful
// append, then runs meanwhile before returning.
type lockKillingEvents struct {
	platform.EventStore
	t         *testing.T
	pool      *pgxpool.Pool
	meanwhile func()
	once      sync.Once
}

func (e *lockKillingEvents) Append(ctx context.Context, stream string, expected int64, events []platform.NewEvent) error {
	if err := e.EventStore.Append(ctx, stream, expected, events); err != nil {
		return err
	}
	e.once.Do(func() {
		terminateTreeLockHolder(e.t, e.pool)
		e.meanwhile()
	})
	return nil
}

// terminateTreeLockHolder ends the one backend holding a tree lock.
func terminateTreeLockHolder(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT pg_terminate_backend(a.pid, 5000)
		   FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		  WHERE l.locktype = 'advisory' AND l.granted AND a.application_name = $1`,
		treeLockApplicationName)
	require.NoError(t, err)
	ended, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	require.NoError(t, err)
	require.Equal(t, []bool{true}, ended,
		"pg_terminate_backend(pid, 5000) results for backends named %q holding an advisory lock", treeLockApplicationName)
}

// buildSponsorChain adds root as the tree's root, then places a, b and c under
// it, sponsored by root, a and b.
func (it *writerIntegration) buildSponsorChain(t *testing.T, tree, root, a, b, c string) {
	t.Helper()
	it.addUnilevelRoot(t, tree, root)
	for i, p := range []struct{ user, sponsor string }{{a, root}, {b, a}, {c, b}} {
		res, err := it.writer(t).Place(context.Background(), PlaceRequest{
			TreeID: tree, UserID: p.user, ParentID: root, SponsorID: p.sponsor,
			EnrolledAt: writeTime.Add(time.Duration(i+1) * time.Hour),
		})
		require.NoError(t, err)
		require.NoError(t, res.ProjectionErr)
	}
}

// activeSponsors maps each active user in tree to the sponsor its row names.
func activeSponsors(t *testing.T, store TreeStore, tree string) map[string]string {
	t.Helper()
	rows, err := store.GetByTree(context.Background(), tree)
	require.NoError(t, err)
	sponsors := make(map[string]string, len(rows))
	for _, r := range rows {
		require.NotNil(t, r.SponsorID, "the active row for %s has no sponsor", r.UserID)
		sponsors[r.UserID] = *r.SponsorID
	}
	return sponsors
}

func TestTreeWriter_ARemovalProjectedAfterItsLockWasLostLeavesNoStaleSponsor(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c := testTreeUUID(309), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4)
	it.buildSponsorChain(t, tree, root, a, b, c)

	var second WriteResult
	var secondErr error
	events := &lockKillingEvents{EventStore: it.events, t: t, pool: it.pool, meanwhile: func() {
		second, secondErr = it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: a, RemovedAt: writeTime.Add(5 * time.Hour)})
	}}
	first, firstErr := NewTreeWriter(events, it.store, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree, UserID: b, RemovedAt: writeTime.Add(4 * time.Hour)})

	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	assert.Equal(t, map[string]string{root: root, c: root}, activeSponsors(t, it.store, tree))
	assert.Equal(t, int64(5), first.Version)
	assert.Equal(t, int64(6), second.Version)
	require.NotNil(t, second.CaughtUp, "the second writer reported no caught-up event")
	assert.Equal(t, first.EventID, second.CaughtUp.EventID)
	assert.NoError(t, second.ProjectionErr)
	assert.NoError(t, second.ReleaseErr)
	require.Error(t, first.ProjectionErr)
	assert.Contains(t, first.ProjectionErr.Error(), "soft delete for user "+b+" in tree "+tree+" matched 0 active rows")
	require.Error(t, first.ReleaseErr)
	assert.Contains(t, first.ReleaseErr.Error(), "the pg_advisory_unlock query for tree "+tree+" failed")
}

// parkedRemovalEngine delays the return of its first RemoveNode until open is
// called, and closes parked when the delay begins.
type parkedRemovalEngine struct {
	TreeEngineChecker
	parked   chan struct{}
	release  chan struct{}
	once     sync.Once
	openOnce sync.Once
}

func newParkedRemovalEngine(engine TreeEngineChecker) *parkedRemovalEngine {
	return &parkedRemovalEngine{TreeEngineChecker: engine, parked: make(chan struct{}), release: make(chan struct{})}
}

// open lets the delayed call return. It is safe to call more than once.
func (e *parkedRemovalEngine) open() {
	e.openOnce.Do(func() { close(e.release) })
}

func (e *parkedRemovalEngine) RemoveNode(ctx context.Context, structure, userID string) ([]Responsored, error) {
	moved, err := e.TreeEngineChecker.RemoveNode(ctx, structure, userID)
	e.once.Do(func() {
		close(e.parked)
		<-e.release
	})
	return moved, err
}

func TestTreeWriter_ACatchUpBehindALateProjectionAppendsNothing(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c := testTreeUUID(310), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4)
	it.buildSponsorChain(t, tree, root, a, b, c)

	parked := newParkedRemovalEngine(it.engine(t))
	t.Cleanup(parked.open)
	type outcome struct {
		res WriteResult
		err error
	}
	second := make(chan outcome, 1)
	events := &lockKillingEvents{EventStore: it.events, t: t, pool: it.pool, meanwhile: func() {
		w2 := NewTreeWriter(it.events, it.store, parked, NewPostgresTreeLocker(it.dsn))
		go func() {
			res, err := w2.Remove(ctx, RemoveRequest{TreeID: tree, UserID: a, RemovedAt: writeTime.Add(5 * time.Hour)})
			second <- outcome{res, err}
		}()
		select {
		case <-parked.parked:
		case got := <-second:
			t.Fatalf("the second writer returned before the parked engine began its delay: %v", got.err)
		case <-time.After(30 * time.Second):
			t.Fatal("within 30s the second writer had not returned and the parked engine had not begun its delay")
		}
	}}
	first, firstErr := NewTreeWriter(events, it.store, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree, UserID: b, RemovedAt: writeTime.Add(4 * time.Hour)})
	require.NoError(t, firstErr)
	parked.open()
	got := receive(t, second, 30*time.Second)

	assert.Equal(t, map[string]string{root: root, a: root, c: a}, activeSponsors(t, it.store, tree))
	assert.NotEmpty(t, first.EventID, "the first writer reported no appended event")
	assert.Equal(t, int64(5), first.Version)
	assert.NoError(t, first.ProjectionErr)
	assert.ErrorContains(t, first.ReleaseErr, "the pg_advisory_unlock query for tree "+tree+" failed")
	assert.Empty(t, got.res.EventID, "the second writer reported an appended event")
	assert.Nil(t, got.res.CaughtUp, "the second writer reported a caught-up event")
	assert.NoError(t, got.res.ReleaseErr)
	stored, err := it.events.ReadStream(ctx, TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	if assert.Len(t, stored, 5) {
		assert.Equal(t, first.EventID, stored[4].ID)
	}
	var catchUp *CatchUpFailedError
	if assert.ErrorAs(t, got.err, &catchUp) {
		assert.Equal(t, first.EventID, catchUp.EventID)
	}
	assert.ErrorContains(t, got.err, "soft delete for user "+b+" in tree "+tree+" matched 0 active rows")
}

// TestTreeWriter_APlacementProjectedAfterItsLockWasLostIsRefused pins the
// late writer's refusal, and that the late writer's engine does not take the
// placement.
func TestTreeWriter_APlacementProjectedAfterItsLockWasLostIsRefused(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, x := testTreeUUID(311), testUserUUID(1), testUserUUID(2)
	it.addUnilevelRoot(t, tree, root)

	var second WriteResult
	var secondErr error
	events := &lockKillingEvents{EventStore: it.events, t: t, pool: it.pool, meanwhile: func() {
		second, secondErr = it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: x, RemovedAt: writeTime.Add(2 * time.Hour)})
	}}
	lateEngine := it.engine(t)
	first, firstErr := NewTreeWriter(events, it.store, lateEngine, NewPostgresTreeLocker(it.dsn)).
		Place(ctx, PlaceRequest{TreeID: tree, UserID: x, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(time.Hour)})

	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	assert.Equal(t, map[string]string{root: root}, activeSponsors(t, it.store, tree))
	assert.Equal(t, int64(2), first.Version)
	assert.Equal(t, int64(3), second.Version)
	require.NotNil(t, second.CaughtUp, "the second writer reported no caught-up event")
	assert.Equal(t, first.EventID, second.CaughtUp.EventID)
	assert.NoError(t, second.ProjectionErr)
	assert.NoError(t, second.ReleaseErr)
	assert.ErrorIs(t, first.ProjectionErr, ErrReplayedPlacement)
	pos, posErr := lateEngine.GetPosition(ctx, tree, x)
	assert.True(t, isEngineCode(posErr, engineCodeUserNotFound),
		"GetPosition for %s on the first writer's engine returned position %+v and error %v", x, pos, posErr)
	require.Error(t, first.ReleaseErr)
	assert.Contains(t, first.ReleaseErr.Error(), "the pg_advisory_unlock query for tree "+tree+" failed")
	row, err := it.store.GetNodeIncludingRemoved(ctx, tree, x)
	require.NoError(t, err)
	require.NotNil(t, row, "no row at all for %s", x)
	assert.Equal(t, first.EventID, row.ID)
	assert.NotNil(t, row.RemovedAt, "the row for %s is active", x)
}

func TestTreeWriter_ALateRemovalLeavesAUserPlacedAgainActive(t *testing.T) {
	t.Skip("skipped until HEU-857: a late removal tombstoned B, placed again, and left C sponsored by removed A; " +
		"every writer's error and projection error was nil, and writer 1's release error was set because its lock connection was terminated")
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c := testTreeUUID(312), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4)
	it.buildSponsorChain(t, tree, root, a, b, c)

	var replaced, removedA WriteResult
	var replacedErr, removedAErr error
	events := &lockKillingEvents{EventStore: it.events, t: t, pool: it.pool, meanwhile: func() {
		replaced, replacedErr = it.writer(t).Place(ctx, PlaceRequest{
			TreeID: tree, UserID: b, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(5 * time.Hour)})
		removedA, removedAErr = it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: a, RemovedAt: writeTime.Add(6 * time.Hour)})
	}}
	first, firstErr := NewTreeWriter(events, it.store, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree, UserID: b, RemovedAt: writeTime.Add(4 * time.Hour)})

	require.NoError(t, firstErr)
	require.NoError(t, replacedErr)
	require.NoError(t, removedAErr)
	assert.NoError(t, replaced.ProjectionErr)
	assert.NoError(t, removedA.ProjectionErr)
	assert.Equal(t, int64(5), first.Version)
	assert.Equal(t, int64(6), replaced.Version)
	assert.Equal(t, int64(7), removedA.Version)
	assert.Equal(t, map[string]string{root: root, b: root, c: root}, activeSponsors(t, it.store, tree))
	// first.ProjectionErr is left unasserted: how a refused late write is reported is HEU-857's to define.
	assert.ErrorContains(t, first.ReleaseErr, "the pg_advisory_unlock query for tree "+tree+" failed")
}

// lockKillingLoadStore ends the tree lock's backend after its first full-tree
// load, then runs meanwhile before returning.
type lockKillingLoadStore struct {
	TreeStore
	t         *testing.T
	pool      *pgxpool.Pool
	meanwhile func()
	once      sync.Once
}

func (s *lockKillingLoadStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	rows, err := s.TreeStore.GetByTreeDepthOrdered(ctx, treeID)
	s.once.Do(func() {
		terminateTreeLockHolder(s.t, s.pool)
		s.meanwhile()
	})
	return rows, err
}

func TestTreeWriter_ARemovalAfterALockLostBeforeItsAppendMovesEveryRecruit(t *testing.T) {
	t.Skip("skipped until HEU-859: writer 1 lost its lock after its load, D was placed sponsored by B, and writer 1's removal of B left D sponsored by removed B; " +
		"every writer's error and projection error was nil, writer 1's release error was set because its lock connection was terminated, " +
		"and the next write failed to load the tree because D names a sponsor not in it")
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c, d, e, y := testTreeUUID(313), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4),
		testUserUUID(5), testUserUUID(6), testUserUUID(8)
	it.buildSponsorChain(t, tree, root, a, b, c)

	var placedD, placedE WriteResult
	var placedDErr, placedEErr error
	store := &lockKillingLoadStore{TreeStore: it.store, t: t, pool: it.pool, meanwhile: func() {
		placedD, placedDErr = it.writer(t).Place(ctx, PlaceRequest{
			TreeID: tree, UserID: d, ParentID: root, SponsorID: b, EnrolledAt: writeTime.Add(5 * time.Hour)})
		placedE, placedEErr = it.writer(t).Place(ctx, PlaceRequest{
			TreeID: tree, UserID: e, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(6 * time.Hour)})
	}}
	first, firstErr := NewTreeWriter(it.events, store, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree, UserID: b, RemovedAt: writeTime.Add(7 * time.Hour)})

	require.NoError(t, placedDErr)
	require.NoError(t, placedEErr)
	assert.NoError(t, placedD.ProjectionErr)
	assert.NoError(t, placedE.ProjectionErr)
	assert.Equal(t, int64(5), placedD.Version)
	assert.Equal(t, int64(6), placedE.Version)
	assert.ErrorContains(t, first.ReleaseErr, "the pg_advisory_unlock query for tree "+tree+" failed")
	stored, err := it.events.ReadStream(ctx, TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	removalAppended := false
	for _, ev := range stored {
		if ev.Version <= placedE.Version || ev.Type != EventTypeNodeRemoved {
			continue
		}
		var p NodeRemovedPayload
		require.NoError(t, json.Unmarshal(ev.Payload, &p))
		removalAppended = removalAppended || p.UserID == b
	}
	// The next write redelivers the stream's last event, so the store is
	// judged after it rather than straight after the first writer returns.
	next, nextErr := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: y, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(8 * time.Hour)})
	assert.NoError(t, nextErr, "a placement under the root after the first writer's attempt")
	assert.NoError(t, next.ProjectionErr)
	want := map[string]string{root: root, a: root, b: a, c: b, d: b, e: root, y: root}
	if removalAppended {
		want = map[string]string{root: root, a: root, c: a, d: a, e: root, y: root}
	}
	assert.Equal(t, want, activeSponsors(t, it.store, tree),
		"the first writer's removal returned %v; a removal of B after version %d is in the stream: %t", firstErr, placedE.Version, removalAppended)
}

func TestTreeWriter_APlacementAfterALockLostBeforeItsAppendLeavesTheTreeWritable(t *testing.T) {
	t.Skip("skipped until HEU-859: writer 1 lost its lock after its load, B was removed, and writer 1 appended a placement under B; " +
		"its projection failed with parent not found, and the next writer's catch-up failed the same way and appended nothing")
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c, e, x, y := testTreeUUID(314), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4),
		testUserUUID(6), testUserUUID(7), testUserUUID(8)
	it.buildSponsorChain(t, tree, root, a, b, c)

	var removedB, placedE WriteResult
	var removedBErr, placedEErr error
	store := &lockKillingLoadStore{TreeStore: it.store, t: t, pool: it.pool, meanwhile: func() {
		removedB, removedBErr = it.writer(t).Remove(ctx, RemoveRequest{TreeID: tree, UserID: b, RemovedAt: writeTime.Add(5 * time.Hour)})
		placedE, placedEErr = it.writer(t).Place(ctx, PlaceRequest{
			TreeID: tree, UserID: e, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(6 * time.Hour)})
	}}
	first, firstErr := NewTreeWriter(it.events, store, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Place(ctx, PlaceRequest{TreeID: tree, UserID: x, ParentID: b, SponsorID: b, EnrolledAt: writeTime.Add(7 * time.Hour)})

	require.NoError(t, removedBErr)
	require.NoError(t, placedEErr)
	assert.NoError(t, removedB.ProjectionErr)
	assert.NoError(t, placedE.ProjectionErr)
	t.Logf("the first writer's placement under removed B returned %v, with projection error %v", firstErr, first.ProjectionErr)
	assert.ErrorContains(t, first.ReleaseErr, "the pg_advisory_unlock query for tree "+tree+" failed")
	stored, err := it.events.ReadStream(ctx, TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	for _, ev := range stored {
		if ev.Type != EventTypeNodePlaced {
			continue
		}
		var p NodePlacedPayload
		require.NoError(t, json.Unmarshal(ev.Payload, &p))
		assert.False(t, p.UserID == x && p.ParentID == b,
			"the stream holds event %s at version %d placing %s under removed %s", ev.ID, ev.Version, x, b)
	}
	next, nextErr := it.writer(t).Place(ctx, PlaceRequest{
		TreeID: tree, UserID: y, ParentID: root, SponsorID: root, EnrolledAt: writeTime.Add(8 * time.Hour)})
	assert.NoError(t, nextErr, "a placement under the root after the first writer's attempt")
	assert.NoError(t, next.ProjectionErr)
}
