package networkengine

import (
	"context"
	"fmt"
	"runtime/debug"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The expected values were computed at planning time with FNV-1a over the
// UUID's 16 bytes.
func TestTreeLockKey_IsFixed(t *testing.T) {
	assert.Equal(t, int32(856866490), treeLockKey(uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-000000000001")))
	assert.Equal(t, int32(840088871), treeLockKey(uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-000000000002")))
	assert.Equal(t, int32(-1288558304), treeLockKey(uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-000000000001")),
		"a key whose hash has the high bit set")
}

func requireLockDatabase(t *testing.T) string {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	return pgContainer.DSN
}

// refusedPollQuery counts idle lock sessions outside $2 whose last statement
// was a pg_try_advisory_lock call and that hold no advisory lock.
const refusedPollQuery = `SELECT count(*) FROM pg_stat_activity a
WHERE a.datname = current_database()
  AND a.application_name = $1
  AND a.pid <> ALL($2::int[])
  AND a.state = 'idle'
  AND a.query LIKE 'SELECT pg_try_advisory_lock(%'
  AND NOT EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid = a.pid AND l.locktype = 'advisory')`

// watchLockPolls watches for refused attempts from lock sessions that open
// after the watcher is made.
func watchLockPolls(dsn string) watchPolls {
	return func(t *testing.T) pollWatcher {
		t.Helper()
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		var before []int32
		require.NoError(t, conn.QueryRow(ctx,
			"SELECT coalesce(array_agg(pid), '{}') FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1",
			treeLockApplicationName).Scan(&before))
		// Each check runs as its own statement outside a transaction, so
		// pg_stat_activity is read fresh rather than from a cached snapshot.
		return func() bool {
			check, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			var n int
			require.NoError(t, conn.QueryRow(check, refusedPollQuery, treeLockApplicationName, before).Scan(&n))
			return n > 0
		}
	}
}

func TestPostgresTreeLocker_SharedBehaviour(t *testing.T) {
	dsn := requireLockDatabase(t)
	runTreeLockerSuite(t, func(t *testing.T) TreeLocker {
		return NewPostgresTreeLocker(dsn)
	}, watchLockPolls(dsn))
}

func TestPostgresTreeLocker_SeparateLockersExcludeEachOther(t *testing.T) {
	dsn := requireLockDatabase(t)
	tree := uuid.MustParse(testTreeUUID(1))
	first, second := NewPostgresTreeLocker(dsn), NewPostgresTreeLocker(dsn)

	unlock, err := first.Lock(context.Background(), tree)
	require.NoError(t, err)

	requireLockTimesOut(t, second, tree, watchLockPolls(dsn),
		"a Lock from a second locker on a tree the first locker holds did not time out")

	require.NoError(t, unlock())
	wait, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	again, err := second.Lock(wait, tree)
	require.NoError(t, err)
	require.NoError(t, again())
}

func TestPostgresTreeLocker_TreesWhoseKeysCollideExcludeEachOther(t *testing.T) {
	dsn := requireLockDatabase(t)
	held := uuid.MustParse("cccccccc-cccc-cccc-cccc-40042ca32b29")
	colliding := uuid.MustParse("cccccccc-cccc-cccc-cccc-a81943728855")
	require.NotEqual(t, held, colliding)
	require.Equal(t, treeLockKey(held), treeLockKey(colliding),
		"treeLockKey returned different keys for the two trees in the collision fixture")
	locker := NewPostgresTreeLocker(dsn)

	unlock := holdTree(t, locker, held)
	requireLockTimesOut(t, locker, colliding, watchLockPolls(dsn),
		"a Lock on a tree whose key equals the held tree's key did not time out")

	require.NoError(t, unlock())
	wait, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	again, err := locker.Lock(wait, colliding)
	require.NoError(t, err)
	require.NoError(t, again())
}

func TestPostgresTreeLocker_AcceptsAURLCarryingPoolSettings(t *testing.T) {
	dsn := requireLockDatabase(t)
	require.Contains(t, dsn, "?", "the DSN must already carry a query string")
	locker := NewPostgresTreeLocker(dsn + "&pool_max_conns=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := locker.Lock(ctx, uuid.MustParse(testTreeUUID(4)))

	require.NoError(t, err, "the CLI hands the locker the pool's URL")
	require.NoError(t, unlock())
}

func TestPostgresTreeLocker_ALostSessionReleasesTheLock(t *testing.T) {
	dsn := requireLockDatabase(t)
	ctx := context.Background()
	tree := uuid.MustParse(testTreeUUID(3))

	holder, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, err = holder.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", platform.TreeLockNamespace, treeLockKey(tree))
	require.NoError(t, err)

	locker := NewPostgresTreeLocker(dsn)
	requireLockTimesOut(t, locker, tree, watchLockPolls(dsn), "a Lock on a tree a raw session holds did not time out")

	require.NoError(t, holder.Close(ctx))

	wait, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()
	unlock, err := locker.Lock(wait, tree)
	require.NoError(t, err, "closing the holder's connection must release its lock")
	require.NoError(t, unlock())
}

// lockSessions counts the open lock connections Postgres reports.
func lockSessions(t *testing.T, dsn string) int {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(context.Background()) }()
	var n int
	require.NoError(t, conn.QueryRow(context.Background(),
		"SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", treeLockApplicationName).Scan(&n))
	return n
}

func TestPostgresTreeLocker_ClosesItsConnections(t *testing.T) {
	dsn := requireLockDatabase(t)
	// A garbage-collected socket closes through its finalizer, which would pass
	// this test with the explicit close deleted.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	tree := uuid.MustParse(testTreeUUID(6))
	holder, waiter := NewPostgresTreeLocker(dsn), NewPostgresTreeLocker(dsn)
	gone := func() bool { return lockSessions(t, dsn) == 0 }
	require.Eventually(t, gone, 5*time.Second, 50*time.Millisecond, "lock connections left open by an earlier test")

	unlock, err := holder.Lock(context.Background(), tree)
	require.NoError(t, err)
	require.Equal(t, 1, lockSessions(t, dsn), "a held lock keeps one connection open")

	requireLockTimesOut(t, waiter, tree, watchLockPolls(dsn), "a Lock on a held tree did not time out")
	require.Eventually(t, func() bool { return lockSessions(t, dsn) == 1 }, 5*time.Second, 50*time.Millisecond,
		"a Lock that gave up left its connection open")

	require.NoError(t, unlock())
	require.Eventually(t, gone, 5*time.Second, 50*time.Millisecond, "unlock left its connection open")
}

func TestPostgresTreeLocker_ReportsAConnectTimeout(t *testing.T) {
	testutil.ClearTimeoutEnv(t)
	addr := testutil.SilentListener(t)
	locker := NewPostgresTreeLocker("postgres://app:s3cret@" + addr + "/app?sslmode=disable&connect_timeout=1")
	tree := uuid.MustParse(testTreeUUID(1))

	unlock, err := locker.Lock(context.Background(), tree)

	require.Nil(t, unlock)
	var cte *platform.ConnectTimeoutError
	require.ErrorAs(t, err, &cte)
	require.Equal(t, addr, cte.Hosts)
	require.GreaterOrEqual(t, cte.Waited, time.Second)
	require.Less(t, cte.Waited, 5*time.Second)
	require.ErrorContains(t, err, "open the lock connection for tree "+tree.String())
	require.NotContains(t, err.Error(), "s3cret")
}

func TestPostgresTreeLocker_ARefusedConnStringHoldsNoPassword(t *testing.T) {
	tree := uuid.MustParse(testTreeUUID(1))
	for _, tc := range testutil.ConnStringCases() {
		if tc.PgxStage == "" {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			testutil.IsolatePgxEnv(t)
			want := "parse the database URL for the lock on tree " + tree.String() + ": " +
				testutil.RefusalText(t, "pgx", tc.PgxStage)

			unlock, err := NewPostgresTreeLocker(tc.ConnString).Lock(context.Background(), tree)

			require.Nil(t, unlock)
			require.Error(t, err)
			testutil.RequireNoPasswordWindow(t, fmt.Sprintf("%v\n%+v", err, err), tc.Password, want, tc.WithoutPassword())
			testutil.RequireNoDriverParseError(t, err)
			require.EqualError(t, err, want)
			var cse *platform.ConnStringError
			require.ErrorAs(t, err, &cse)
			require.Equal(t, "pgx", cse.Driver())
			require.Equal(t, tc.PgxStage, cse.Stage())
		})
	}
}
