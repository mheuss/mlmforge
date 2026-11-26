package networkengine

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
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

// refusedPollQuery lists idle lock sessions outside $2 whose last query text
// was a pg_try_advisory_lock call and that hold no advisory lock.
const refusedPollQuery = `SELECT a.pid, a.query_start FROM pg_stat_activity a
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
		// Count a session only once it shows two query_start values at least
		// half a poll interval apart.
		firstStart := map[int32]time.Time{}
		// Each check runs as its own statement outside an explicit transaction,
		// so pg_stat_activity is read fresh rather than from a cached snapshot.
		return func() bool {
			check, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			rows, err := conn.Query(check, refusedPollQuery, treeLockApplicationName, before)
			require.NoError(t, err, "the refused-poll query on pg_stat_activity returned an error")
			type sighting struct {
				pid   int32
				start time.Time
			}
			seen, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sighting, error) {
				var s sighting
				return s, r.Scan(&s.pid, &s.start)
			})
			require.NoError(t, err, "reading the refused-poll rows from pg_stat_activity returned an error")
			for _, s := range seen {
				first, ok := firstStart[s.pid]
				if !ok {
					firstStart[s.pid] = s.start
					continue
				}
				if s.start.Sub(first) >= treeLockPollInterval/2 {
					return true
				}
			}
			return false
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

// lockSessionPIDs lists the pids of this database's sessions named for the tree locker.
func lockSessionPIDs(ctx context.Context, conn *pgx.Conn) ([]int32, error) {
	var pids []int32
	err := conn.QueryRow(ctx,
		`SELECT coalesce(array_agg(pid ORDER BY pid), '{}') FROM pg_stat_activity
		 WHERE datname = current_database() AND application_name = $1`, treeLockApplicationName).Scan(&pids)
	return pids, err
}

// waitForNewLockSession polls until exactly one locker session outside before exists, and returns its pid.
func waitForNewLockSession(t *testing.T, conn *pgx.Conn, before []int32) int32 {
	t.Helper()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var after []int32
	reads := 0
	for {
		seen, err := lockSessionPIDs(ctx, conn)
		if err != nil && reads == 0 {
			t.Fatalf("after %s, no pg_stat_activity read of the %s sessions succeeded; before the Lock they were %v. The read returned: %v",
				time.Since(start).Round(time.Millisecond), treeLockApplicationName, before, err)
		}
		if err != nil {
			t.Fatalf("after %s, the %s sessions were %v; before the Lock they were %v; want exactly one new one. The last pg_stat_activity read returned: %v",
				time.Since(start).Round(time.Millisecond), treeLockApplicationName, after, before, err)
		}
		reads++
		after = seen
		var added []int32
		for _, pid := range after {
			if !slices.Contains(before, pid) {
				added = append(added, pid)
			}
		}
		if len(added) == 1 {
			return added[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForRefusedPoll polls until pid sits idle after a pg_try_advisory_lock call while holding no advisory lock.
func waitForRefusedPoll(t *testing.T, conn *pgx.Conn, pid int32) {
	t.Helper()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var state, query string
	reads := 0
	for {
		var seenState, seenQuery string
		var refused bool
		err := conn.QueryRow(ctx,
			`SELECT coalesce(a.state, ''), a.query,
			        coalesce(a.state = 'idle' AND a.query LIKE 'SELECT pg_try_advisory_lock(%'
			          AND NOT EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid = a.pid AND l.locktype = 'advisory'), false)
			   FROM pg_stat_activity a WHERE a.pid = $1`, pid).Scan(&seenState, &seenQuery, &refused)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("after %s, pg_stat_activity had no row for lock session pid %d. Before that, %d read(s) succeeded, and the last showed state %q with last query %q",
				time.Since(start).Round(time.Millisecond), pid, reads, state, query)
		}
		if err != nil && reads == 0 {
			t.Fatalf("after %s, no pg_stat_activity read of lock session pid %d succeeded. The read returned: %v",
				time.Since(start).Round(time.Millisecond), pid, err)
		}
		if err != nil {
			t.Fatalf("after %s, lock session pid %d was in state %q with last query %q; want idle after a refused pg_try_advisory_lock. The last pg_stat_activity read returned: %v",
				time.Since(start).Round(time.Millisecond), pid, state, query, err)
		}
		reads++
		state, query = seenState, seenQuery
		if refused {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPostgresTreeLocker_AWaiterWhoseSessionEndsMidPollFails(t *testing.T) {
	dsn := requireLockDatabase(t)
	tree := uuid.MustParse(testTreeUUID(7))
	key := treeLockKey(tree)

	admin, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	holderCfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	holderCfg.RuntimeParams["application_name"] = "heu1113-holder"
	holder, err := pgx.ConnectConfig(context.Background(), holderCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close(context.Background()) })
	q, cancelQ := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelQ)
	acquire, cancelAcquire := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancelAcquire)
	_, err = holder.Exec(acquire, "SELECT pg_advisory_lock($1, $2)", platform.TreeLockNamespace, key)
	require.NoError(t, err, "the holder's pg_advisory_lock on tree %s, run under a 10s ctx, returned an error", tree)
	var holderPID int32
	require.NoError(t, holder.QueryRow(q, "SELECT pg_backend_pid()").Scan(&holderPID))

	before, err := lockSessionPIDs(q, admin)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	type pollResult struct {
		unlock func() error
		err    error
	}
	results := make(chan pollResult, 1)
	go func() {
		unlock, err := NewPostgresTreeLocker(dsn).Lock(ctx, tree)
		results <- pollResult{unlock: unlock, err: err}
	}()
	received := false
	t.Cleanup(func() {
		if received {
			return
		}
		// Cancel before draining, or the receive below can wait out its bound.
		cancel()
		select {
		case r := <-results:
			if r.unlock != nil {
				_ = r.unlock()
			}
		case <-time.After(10 * time.Second):
			t.Errorf("no result from the Lock arrived within 10s of cancelling its ctx")
		}
	})
	waiter := waitForNewLockSession(t, admin, before)
	waitForRefusedPoll(t, admin, waiter)

	var ended bool
	require.NoError(t, admin.QueryRow(q, "SELECT pg_terminate_backend($1, 5000)", waiter).Scan(&ended))
	require.True(t, ended, "pg_terminate_backend(%d, 5000) returned false", waiter)

	r := receiveWithin(t, results, 10*time.Second, "the Lock whose session was terminated")
	received = true
	if r.unlock != nil {
		_ = r.unlock()
		t.Fatalf("the Lock whose session was terminated returned an unlock func and error %v", r.err)
	}
	require.ErrorContains(t, r.err, "the pg_try_advisory_lock query for tree "+tree.String()+" failed")
	require.NoError(t, ctx.Err(), "the Lock's ctx had ended when checked after its result was received")

	var holders []int32
	require.NoError(t, admin.QueryRow(q,
		`SELECT coalesce(array_agg(l.pid ORDER BY l.pid), '{}') FROM pg_locks l
		 WHERE l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		   AND l.locktype = 'advisory' AND l.granted
		   AND l.classid::bigint = ($1::bigint & 4294967295)
		   AND l.objid::bigint = ($2::bigint & 4294967295)
		   AND l.objsubid = 2`, platform.TreeLockNamespace, key).Scan(&holders))
	assert.Equal(t, []int32{holderPID}, holders, "pids holding the advisory lock on tree %s", tree)
	require.NoError(t, holder.Close(context.Background()))
}
