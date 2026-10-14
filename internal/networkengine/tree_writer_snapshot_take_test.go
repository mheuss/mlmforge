package networkengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// placeUnderRoot places n new users under writerRoot, one write each.
func placeUnderRoot(t *testing.T, env *writerEnv, first, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		mustPlace(t, env, testUserUUID(first+i), nil)
	}
}

func TestTreeWriterSnapshot_TakesTheFirstSnapshotAtVersion1000(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placeUnderRoot(t, env, 10, snapshotInterval-2)
	_, found, err := env.snapshots.Get(context.Background(), writerTree)
	require.NoError(t, err)
	require.False(t, found, "a snapshot was stored below version %d", snapshotInterval)

	res := mustPlace(t, env, testUserUUID(9000), nil)

	require.Equal(t, int64(snapshotInterval), res.Version)
	assert.Empty(t, res.Warnings)
	snap, found, err := env.snapshots.Get(context.Background(), writerTree)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(snapshotInterval), snap.Version)
	assert.Equal(t, treeTypeUnilevel, snap.TreeType)
}

// failingPutStore fails every Put.
type failingPutStore struct {
	SnapshotStore
	err error
}

func (s failingPutStore) Put(context.Context, Snapshot) error { return s.err }

func TestTreeWriterSnapshot_AFailedPutIsAWarning(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placeUnderRoot(t, env, 10, snapshotInterval-2)
	w := NewTreeWriter(env.events, env.store, failingPutStore{SnapshotStore: env.snapshots, err: errors.New("disk full")},
		newFakeWriterEngine(), env.locker)

	res, err := w.Place(context.Background(), placeRequest(testUserUUID(9000), nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, []string{"storing the snapshot of tree " + writerTree + " at version 1000 returned: disk full"}, res.Warnings)
}

// versionStore answers ProjectedVersion with a fixed result.
type versionStore struct {
	TreeStore
	version int64
	err     error
}

func (s versionStore) ProjectedVersion(context.Context, string) (int64, bool, error) {
	return s.version, true, s.err
}

func TestTreeWriterSnapshot_StoresNothingWhenTheProjectedVersionDiffers(t *testing.T) {
	cases := map[string]struct {
		store versionStore
		want  string
	}{
		"a failed read": {versionStore{err: errors.New("connection reset")},
			"reading the projected version of tree " + writerTree + " before a snapshot returned: connection reset; no snapshot was stored"},
		"another version": {versionStore{version: 999},
			"tree " + writerTree + " has projected version 999 (projection row found: true) after the write at version 1000; no snapshot was stored"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newWriterEnv()
			engine := newFakeWriterEngine()
			tc.store.TreeStore = env.store
			w := NewTreeWriter(env.events, tc.store, env.snapshots, engine, env.locker)

			warning := w.takeSnapshot(context.Background(), writerTree, treeShape{treeType: treeTypeUnilevel}, 1000)

			assert.Equal(t, tc.want, warning)
			assert.NotContains(t, engine.calls, "take_snapshot")
		})
	}
}

func TestTreeWriterSnapshot_AFailedTakeIsAWarning(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placeUnderRoot(t, env, 10, snapshotInterval-2)
	w, engine := env.writer()
	engine.takeErr = errors.New("worker gone")

	res, err := w.Place(context.Background(), placeRequest(testUserUUID(9000), nil))

	require.NoError(t, err)
	assert.Equal(t, []string{"taking a snapshot of tree " + writerTree + " at version 1000 returned: worker gone"}, res.Warnings)
}

func TestTreeWriterSnapshot_StoresNothingWhenTheSnapshotTypeDiffers(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placeUnderRoot(t, env, 10, snapshotInterval-2)
	w, engine := env.writer()
	engine.snapshotType = treeTypeBinary

	res, err := w.Place(context.Background(), placeRequest(testUserUUID(9000), nil))

	require.NoError(t, err)
	assert.Equal(t, []string{"take_snapshot for tree " + writerTree + " at version 1000 returned tree type binary, " +
		"and version 1 records unilevel; no snapshot was stored"}, res.Warnings)
	_, found, err := env.snapshots.Get(context.Background(), writerTree)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestNewTreeWriter_RefusesANilSnapshotStore(t *testing.T) {
	env := newWriterEnv()

	assert.PanicsWithValue(t, "NewTreeWriter was given a nil SnapshotStore", func() {
		NewTreeWriter(env.events, env.store, nil, newFakeWriterEngine(), env.locker)
	})
}
