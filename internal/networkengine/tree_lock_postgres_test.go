package networkengine

import (
	"context"
	"runtime/debug"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The expected values were computed at planning time with FNV-1a over the
// UUID's 16 bytes.
func TestTreeLockKey_IsFixed(t *testing.T) {
	assert.Equal(t, int32(1953654117), treeLockNamespace)
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

func TestPostgresTreeLocker_ExcludesASecondHolderUntilRelease(t *testing.T) {
	dsn := requireLockDatabase(t)
	tree := uuid.MustParse(testTreeUUID(1))
	first, second := NewPostgresTreeLocker(dsn), NewPostgresTreeLocker(dsn)

	unlock, err := first.Lock(context.Background(), tree)
	require.NoError(t, err)

	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = second.Lock(short, tree)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	require.NoError(t, unlock())
	wait, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	again, err := second.Lock(wait, tree)
	require.NoError(t, err)
	require.NoError(t, again())
}

func TestPostgresTreeLocker_DifferentTreesDoNotExclude(t *testing.T) {
	dsn := requireLockDatabase(t)
	locker := NewPostgresTreeLocker(dsn)

	unlock, err := locker.Lock(context.Background(), uuid.MustParse(testTreeUUID(1)))
	require.NoError(t, err)
	defer func() { _ = unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	other, err := locker.Lock(ctx, uuid.MustParse(testTreeUUID(2)))
	require.NoError(t, err)
	require.NoError(t, other())
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
	_, err = holder.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", treeLockNamespace, treeLockKey(tree))
	require.NoError(t, err)

	locker := NewPostgresTreeLocker(dsn)
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = locker.Lock(short, tree)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the raw holder and the locker must use one key")

	require.NoError(t, holder.Close(ctx))

	wait, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()
	unlock, err := locker.Lock(wait, tree)
	require.NoError(t, err, "closing the holder's connection must release its lock")
	require.NoError(t, unlock())
}

func TestPostgresTreeLocker_AStaleUnlockDoesNotFreeTheNextHolder(t *testing.T) {
	dsn := requireLockDatabase(t)
	tree := uuid.MustParse(testTreeUUID(5))
	locker := NewPostgresTreeLocker(dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, err := locker.Lock(ctx, tree)
	require.NoError(t, err)
	require.NoError(t, first())
	second, err := locker.Lock(ctx, tree)
	require.NoError(t, err)
	defer func() { _ = second() }()

	require.Error(t, first(), "the first holder's second unlock returned nil")

	short, cancelShort := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShort()
	_, err = locker.Lock(short, tree)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a third Lock succeeded while the second holder held the tree")
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

	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = waiter.Lock(short, tree)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Eventually(t, func() bool { return lockSessions(t, dsn) == 1 }, 5*time.Second, 50*time.Millisecond,
		"a Lock that gave up left its connection open")

	require.NoError(t, unlock())
	require.Eventually(t, gone, 5*time.Second, 50*time.Millisecond, "unlock left its connection open")
}
