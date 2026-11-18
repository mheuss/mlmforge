package networkengine

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const (
	wedgeNodes = 16383
	wedgeChunk = 128
)

// removalOnlyStore sends ProjectRemoval to removals and every other call to the embedded store.
type removalOnlyStore struct {
	TreeStore
	removals TreeStore
}

func (s removalOnlyStore) ProjectRemoval(ctx context.Context, treeID string, r RemovalProjection, eventID string, v int64) error {
	return s.removals.ProjectRemoval(ctx, treeID, r, eventID, v)
}

// timeoutStore returns a PostgresTreeStore on a pool whose sessions run with statement_timeout set to timeout.
func timeoutStore(t *testing.T, timeout time.Duration, chunk int) *PostgresTreeStore {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(pgContainer.DSN)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = timeout.String()
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := NewPostgresTreeStore(pool)
	store.removalChunk = chunk
	return store
}

// wedgeFixture builds the matrix and computes its near-root promotion on a scratch engine.
func wedgeFixture(t *testing.T, it *writerIntegration) (measuredTree, engineOutcome) {
	t.Helper()
	ctx := context.Background()
	scratch := it.engine(t)
	tree := generateMatrixTree(t, it.events, scratch, it.pool, matrixShape{width: 2}, wedgeNodes)
	call, err := nodeRemovedCall(NodeRemovedPayload{TreeID: tree.id, UserID: measureUser(1), PruningMode: PruningPromoteEarliest}, treeTypeMatrix)
	require.NoError(t, err)
	outcome, err := call.apply(ctx, scratch)
	require.NoError(t, err)
	return tree, outcome
}

// timeRelocation runs the tombstone step and then the relocation of moves in one rolled-back transaction, and returns how long the relocation took.
func timeRelocation(t *testing.T, it *writerIntegration, treeID string, removed []string, moves []Relocation) time.Duration {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tx, err := it.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, tombstoneRemovedSQL, treeID, removed, testNodeUUID(999_998), at)
	require.NoError(t, err)
	args := relocateArgs(treeID, moves, at)
	start := time.Now()
	_, err = collectUserIDs(ctx, tx, relocateSurvivorsSQL, args...)
	elapsed := time.Since(start)
	require.NoError(t, err)
	return elapsed
}

// relocateArgs returns the relocation UPDATE's arguments for moves, stamped at at.
func relocateArgs(treeID string, moves []Relocation, at time.Time) []any {
	users := make([]string, len(moves))
	parents := make([]string, len(moves))
	positions := make([]int, len(moves))
	depths := make([]int, len(moves))
	for i, m := range moves {
		users[i], parents[i], positions[i], depths[i] = m.UserID, m.ParentID, m.Position, m.Depth
	}
	return []any{treeID, users, parents, positions, depths, at}
}

// leafUnderNodeTwo returns the largest node index below n whose parent chain reaches node 2.
func leafUnderNodeTwo(t *testing.T, m matrixShape, n int) int {
	t.Helper()
	for i := n - 1; i > 2; i-- {
		j := i
		for j > 2 {
			j = m.parent(j)
		}
		if j == 2 {
			return i
		}
	}
	t.Fatalf("no node below %d has node 2 as an ancestor", n)
	return 0
}

func TestTreeWriter_ALargeRemovalProjectsUnderAStatementTimeoutInChunks(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, outcome := wedgeFixture(t, it)

	unsplit := timeRelocation(t, it, tree.id, outcome.removed, outcome.relocated)
	var slowest time.Duration
	for lo := 0; lo < len(outcome.relocated); lo += wedgeChunk {
		hi := min(lo+wedgeChunk, len(outcome.relocated))
		slowest = max(slowest, timeRelocation(t, it, tree.id, outcome.removed, outcome.relocated[lo:hi]))
	}
	ratio := float64(unsplit) / float64(slowest)
	timeout := max(time.Duration(math.Sqrt(float64(unsplit)*float64(slowest))).Round(time.Millisecond), time.Millisecond)
	t.Logf("calibration: n=%d timeout=%s chunk=%d unsplit=%s slowest chunk=%s ratio=%.1f relocated=%d",
		wedgeNodes, timeout, wedgeChunk, unsplit, slowest, ratio, len(outcome.relocated))
	require.GreaterOrEqual(t, ratio, 16.0, "unsplit %s over slowest chunk %s", unsplit, slowest)
	require.GreaterOrEqual(t, unsplit, 4*timeout, "unsplit relocation time, against 4 × statement_timeout")
	require.LessOrEqual(t, slowest, timeout/4, "slowest chunk's relocation time, against statement_timeout / 4")

	removals := removalOnlyStore{TreeStore: it.store, removals: timeoutStore(t, timeout, wedgeChunk)}
	res, err := NewTreeWriter(it.events, removals, it.snapshots, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree.id, UserID: measureUser(1), RemovedAt: writeTime, PruningMode: PruningPromoteEarliest})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	version, _, err := it.store.ProjectedVersion(ctx, tree.id)
	require.NoError(t, err)
	require.Equal(t, res.Version, version, "projected version after the chunked removal")

	next := it.engine(t)
	leaf := leafUnderNodeTwo(t, matrixShape{width: 2}, wedgeNodes)
	res, err = NewTreeWriter(it.events, it.store, it.snapshots, next, NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree.id, UserID: measureUser(leaf), RemovedAt: writeTime, PruningMode: PruningPromoteEarliest})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	requireRowsMatchEngine(t, it.store, next, tree.id)

	t.Run("unsplit control", func(t *testing.T) {
		it := newWriterIntegration(t)
		tree, outcome := wedgeFixture(t, it)
		removals := removalOnlyStore{TreeStore: it.store, removals: timeoutStore(t, timeout, len(outcome.relocated)+1)}

		res, err := NewTreeWriter(it.events, removals, it.snapshots, it.engine(t), NewPostgresTreeLocker(it.dsn)).
			Remove(context.Background(), RemoveRequest{TreeID: tree.id, UserID: measureUser(1), RemovedAt: writeTime, PruningMode: PruningPromoteEarliest})

		require.NoError(t, err)
		var pgErr *pgconn.PgError
		require.True(t, errors.As(res.ProjectionErr, &pgErr), "projection error %v carries no *pgconn.PgError", res.ProjectionErr)
		require.Equal(t, "57014", pgErr.Code, "expected SQLSTATE 57014")
	})
}
