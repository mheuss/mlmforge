package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backoffEnv returns a writer env whose stream stands at version 999.
func backoffEnv(t *testing.T) *writerEnv {
	t.Helper()
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placeUnderRoot(t, env, 10, snapshotInterval-2)
	return env
}

// backoffWrites places one user per version from through to, each through a
// new writer over snapshots and an engine from newEngine. It returns the
// take_snapshot calls across those engines and the last write's result.
func backoffWrites(t *testing.T, env *writerEnv, snapshots SnapshotStore, newEngine func() *fakeWriterEngine,
	from, to int64) (int, WriteResult) {
	t.Helper()
	takes := 0
	var last WriteResult
	for v := from; v <= to; v++ {
		engine := newEngine()
		res, err := NewTreeWriter(env.events, env.store, snapshots, engine, env.locker).
			Place(context.Background(), placeRequest(testUserUUID(int(20000+v)), nil))
		require.NoError(t, err)
		require.NoError(t, res.ProjectionErr)
		require.Equal(t, v, res.Version)
		for _, call := range engine.calls {
			if call == "take_snapshot" {
				takes++
			}
		}
		last = res
	}
	return takes, last
}

func requireLastAttempt(t *testing.T, env *writerEnv, want SnapshotAttempt) {
	t.Helper()
	got, found, err := env.snapshots.LastAttempt(context.Background(), writerTree)
	require.NoError(t, err)
	require.True(t, found, "no attempt recorded")
	assert.Equal(t, want, got)
}

// lastAttemptFailingStore fails every LastAttempt.
type lastAttemptFailingStore struct {
	SnapshotStore
	err error
}

func (s lastAttemptFailingStore) LastAttempt(context.Context, string) (SnapshotAttempt, bool, error) {
	return SnapshotAttempt{}, false, s.err
}

// recordFailingStore fails every RecordAttempt.
type recordFailingStore struct {
	SnapshotStore
	err error
}

func (s recordFailingStore) RecordAttempt(context.Context, SnapshotAttempt) error { return s.err }

// attemptCountingStore counts LastAttempt and RecordAttempt calls.
type attemptCountingStore struct {
	SnapshotStore
	lastAttempts, records int
}

func (s *attemptCountingStore) LastAttempt(ctx context.Context, treeID string) (SnapshotAttempt, bool, error) {
	s.lastAttempts++
	return s.SnapshotStore.LastAttempt(ctx, treeID)
}

func (s *attemptCountingStore) RecordAttempt(ctx context.Context, a SnapshotAttempt) error {
	s.records++
	return s.SnapshotStore.RecordAttempt(ctx, a)
}

// cancellingPutStore cancels the caller's context inside Put and fails with its error.
type cancellingPutStore struct {
	SnapshotStore
	cancel context.CancelFunc
}

func (s cancellingPutStore) Put(ctx context.Context, _ Snapshot) (bool, error) {
	s.cancel()
	return false, ctx.Err()
}

// cancellingTakeEngine cancels the caller's context inside TakeSnapshot and fails with its error.
type cancellingTakeEngine struct {
	*fakeWriterEngine
	cancel context.CancelFunc
}

func (e cancellingTakeEngine) TakeSnapshot(ctx context.Context, _ string) (*SnapshotResultDTO, error) {
	e.calls = append(e.calls, "take_snapshot")
	e.cancel()
	return nil, ctx.Err()
}

// deadlineTakeEngine waits inside TakeSnapshot until the caller's deadline passes.
type deadlineTakeEngine struct{ *fakeWriterEngine }

func (e deadlineTakeEngine) TakeSnapshot(ctx context.Context, _ string) (*SnapshotResultDTO, error) {
	e.calls = append(e.calls, "take_snapshot")
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTreeWriterSnapshotBackoff_AFailingPutTakesOncePerThousandEvents(t *testing.T) {
	env := backoffEnv(t)
	failing := failingPutStore{SnapshotStore: env.snapshots, err: errors.New("disk full")}

	takes, last := backoffWrites(t, env, failing, newFakeWriterEngine, snapshotInterval, 2*snapshotInterval-1)

	assert.Equal(t, 1, takes, "writes 1000 to 1999")
	assert.Equal(t, []string{"skipped the snapshot of tree " + writerTree + " at version 1999; the last attempt, at version 1000, " +
		"stored nothing: storing the snapshot of tree " + writerTree + " at version 1000 returned: disk full"}, last.Warnings)

	takes, _ = backoffWrites(t, env, failing, newFakeWriterEngine, 2*snapshotInterval, 2*snapshotInterval)

	assert.Equal(t, 1, takes, "write 2000")
	requireLastAttempt(t, env, SnapshotAttempt{TreeID: writerTree, Version: 2 * snapshotInterval,
		Observed: "storing the snapshot of tree " + writerTree + " at version 2000 returned: disk full"})
}

func TestTreeWriterSnapshotBackoff_RecordsEachAttemptThatReachedTheEngine(t *testing.T) {
	cases := map[string]struct {
		snapshots func(env *writerEnv) SnapshotStore
		takeErr   error
		want      SnapshotAttempt
	}{
		"a failed put": {func(env *writerEnv) SnapshotStore {
			return failingPutStore{SnapshotStore: env.snapshots, err: errors.New("disk full")}
		}, nil, SnapshotAttempt{TreeID: writerTree, Version: 1000,
			Observed: "storing the snapshot of tree " + writerTree + " at version 1000 returned: disk full"}},
		"a stored put": {func(env *writerEnv) SnapshotStore { return env.snapshots }, nil,
			SnapshotAttempt{TreeID: writerTree, Version: 1000, Stored: true}},
		"a failed take": {func(env *writerEnv) SnapshotStore { return env.snapshots }, errors.New("worker gone"),
			SnapshotAttempt{TreeID: writerTree, Version: 1000,
				Observed: "taking a snapshot of tree " + writerTree + " at version 1000 returned: worker gone"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := backoffEnv(t)
			newEngine := func() *fakeWriterEngine {
				e := newFakeWriterEngine()
				e.takeErr = tc.takeErr
				return e
			}

			backoffWrites(t, env, tc.snapshots(env), newEngine, snapshotInterval, snapshotInterval)

			requireLastAttempt(t, env, tc.want)
		})
	}
}

func TestTreeWriterSnapshotBackoff_AProjectedVersionMismatchRecordsNothing(t *testing.T) {
	env := backoffEnv(t)
	w := NewTreeWriter(env.events, versionStore{TreeStore: env.store, version: 999}, env.snapshots, newFakeWriterEngine(), env.locker)

	res, err := w.Place(context.Background(), placeRequest(testUserUUID(9000), nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	_, found, err := env.snapshots.LastAttempt(context.Background(), writerTree)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestTreeWriterSnapshotBackoff_AFailingGetBacksOffWithoutASkipWarning(t *testing.T) {
	env := backoffEnv(t)
	failing := getFailingSnapshots{SnapshotStore: env.snapshots, err: errors.New("connection reset")}

	takes, last := backoffWrites(t, env, failing, newFakeWriterEngine, snapshotInterval, 2*snapshotInterval-1)

	assert.Equal(t, 1, takes, "writes 1000 to 1999")
	assert.Equal(t, []string{"reading the snapshot of tree " + writerTree + " returned: connection reset"}, last.Warnings)
	requireLastAttempt(t, env, SnapshotAttempt{TreeID: writerTree, Version: 1000, Stored: true})

	takes, _ = backoffWrites(t, env, failing, newFakeWriterEngine, 2*snapshotInterval, 2*snapshotInterval)

	assert.Equal(t, 1, takes, "write 2000")
	requireLastAttempt(t, env, SnapshotAttempt{TreeID: writerTree, Version: 2 * snapshotInterval, Stored: true})
}

func TestTreeWriterSnapshotBackoff_AFailedLastAttemptSkipsTheSnapshot(t *testing.T) {
	env := backoffEnv(t)
	failing := lastAttemptFailingStore{SnapshotStore: env.snapshots, err: errors.New("connection reset")}

	takes, last := backoffWrites(t, env, failing, newFakeWriterEngine, snapshotInterval, snapshotInterval)

	assert.Equal(t, 0, takes)
	assert.Equal(t, []string{"reading the last snapshot attempt of tree " + writerTree +
		" at version 1000 returned: connection reset; no snapshot was taken"}, last.Warnings)
}

func TestTreeWriterSnapshotBackoff_AFailedRecordIsOnlyAWarning(t *testing.T) {
	env := backoffEnv(t)
	failing := recordFailingStore{SnapshotStore: env.snapshots, err: errors.New("connection reset")}

	takes, last := backoffWrites(t, env, failing, newFakeWriterEngine, snapshotInterval, snapshotInterval)

	assert.Equal(t, 1, takes)
	assert.Equal(t, int64(snapshotInterval), last.Version)
	assert.NotEmpty(t, last.EventID)
	assert.Equal(t, []string{"recording the snapshot attempt of tree " + writerTree +
		" at version 1000 returned: connection reset"}, last.Warnings)
	snap, found, err := env.snapshots.Get(context.Background(), writerTree)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(snapshotInterval), snap.Version)
}

func TestTreeWriterSnapshotBackoff_KeepsTheTakeWarningBeforeTheRecordWarning(t *testing.T) {
	env := backoffEnv(t)
	failing := recordFailingStore{
		SnapshotStore: failingPutStore{SnapshotStore: env.snapshots, err: errors.New("disk full")},
		err:           errors.New("connection reset"),
	}

	_, last := backoffWrites(t, env, failing, newFakeWriterEngine, snapshotInterval, snapshotInterval)

	assert.Equal(t, []string{
		"storing the snapshot of tree " + writerTree + " at version 1000 returned: disk full",
		"recording the snapshot attempt of tree " + writerTree + " at version 1000 returned: connection reset",
	}, last.Warnings)
}

func TestTreeWriterSnapshotBackoff_RecordsAnAttemptTheCallerCancelled(t *testing.T) {
	t.Run("the take cancels", func(t *testing.T) {
		env := backoffEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		engine := cancellingTakeEngine{fakeWriterEngine: newFakeWriterEngine(), cancel: cancel}

		_, err := NewTreeWriter(env.events, env.store, env.snapshots, engine, env.locker).
			Place(ctx, placeRequest(testUserUUID(9000), nil))

		require.NoError(t, err)
		requireLastAttempt(t, env, SnapshotAttempt{TreeID: writerTree, Version: 1000,
			Observed: "taking a snapshot of tree " + writerTree + " at version 1000 returned: context canceled"})
	})
	t.Run("the take passes its deadline", func(t *testing.T) {
		env := backoffEnv(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		engine := deadlineTakeEngine{fakeWriterEngine: newFakeWriterEngine()}

		_, err := NewTreeWriter(env.events, env.store, env.snapshots, engine, env.locker).
			Place(ctx, placeRequest(testUserUUID(9000), nil))

		require.NoError(t, err)
		requireLastAttempt(t, env, SnapshotAttempt{TreeID: writerTree, Version: 1000,
			Observed: "taking a snapshot of tree " + writerTree + " at version 1000 returned: context deadline exceeded"})
	})
	t.Run("the put cancels", func(t *testing.T) {
		env := backoffEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := cancellingPutStore{SnapshotStore: env.snapshots, cancel: cancel}

		_, err := NewTreeWriter(env.events, env.store, store, newFakeWriterEngine(), env.locker).
			Place(ctx, placeRequest(testUserUUID(9000), nil))

		require.NoError(t, err)
		requireLastAttempt(t, env, SnapshotAttempt{TreeID: writerTree, Version: 1000,
			Observed: "storing the snapshot of tree " + writerTree + " at version 1000 returned: context canceled"})
	})
}

func TestStorableObserved_ReplacesWhatATextColumnRefuses(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"plain text":   {"storing returned: disk full", "storing returned: disk full"},
		"a NUL byte":   {"worker said \x00 here", "worker said � here"},
		"invalid UTF8": {"bad \xff byte", "bad � byte"},
		"both":         {"\x00\xff", "��"},
		"empty":        {"", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, storableObserved(tc.in))
		})
	}
}

func TestTreeWriterSnapshotBackoff_ReadsAndRecordsOnlyPastTheThreshold(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placeUnderRoot(t, env, 10, snapshotInterval-3)
	spy := &attemptCountingStore{SnapshotStore: env.snapshots}

	backoffWrites(t, env, spy, newFakeWriterEngine, snapshotInterval-1, snapshotInterval-1)
	assert.Equal(t, [2]int{0, 0}, [2]int{spy.lastAttempts, spy.records}, "write 999")

	backoffWrites(t, env, spy, newFakeWriterEngine, snapshotInterval, snapshotInterval)
	assert.Equal(t, [2]int{1, 1}, [2]int{spy.lastAttempts, spy.records}, "write 1000")

	backoffWrites(t, env, spy, newFakeWriterEngine, snapshotInterval+1, snapshotInterval+1)
	assert.Equal(t, [2]int{1, 1}, [2]int{spy.lastAttempts, spy.records}, "write 1001")
}
