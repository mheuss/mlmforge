package networkengine

import (
	"context"
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

// buildSponsorChain places a, b and c under root, sponsored by root, a and b.
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
			t.Fatalf("the second writer returned before any RemoveNode on the parked engine returned: %v", got.err)
		case <-time.After(30 * time.Second):
			t.Fatal("within 30s the second writer had not returned and no RemoveNode on the parked engine had returned")
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
