package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runSnapshotStoreSuite is the shared contract both SnapshotStore twins pass.
func runSnapshotStoreSuite(t *testing.T, newStore func(t *testing.T) SnapshotStore) {
	t.Helper()
	tree := testTreeUUID(1)
	// A duplicate key and odd spacing prove the bytes come back exactly.
	data := []byte(`{"a":1,  "a":2}`)
	snap := func(version int64, payload string) Snapshot {
		return Snapshot{TreeID: tree, Version: version, TreeType: treeTypeUnilevel, Data: []byte(payload)}
	}

	t.Run("a tree with no snapshot reports none", func(t *testing.T) {
		_, found, err := newStore(t).Get(context.Background(), tree)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("Get returns exactly what Put stored", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		stored, err := s.Put(ctx, Snapshot{TreeID: tree, Version: 1000, TreeType: treeTypeMatrix, Data: data})
		require.NoError(t, err)
		assert.True(t, stored, "the first put")

		got, found, err := s.Get(ctx, tree)

		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, Snapshot{TreeID: tree, Version: 1000, TreeType: treeTypeMatrix, Data: data}, got)
	})

	t.Run("a put at a higher version replaces the snapshot", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		requireStored(t, ctx, s, snap(1000, `"old"`))
		stored, err := s.Put(ctx, snap(2000, `"new"`))
		require.NoError(t, err)
		assert.True(t, stored, "the higher put")

		got, _, err := s.Get(ctx, tree)

		require.NoError(t, err)
		assert.Equal(t, snap(2000, `"new"`), got)
	})

	t.Run("a put at an equal or lower version is ignored", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		requireStored(t, ctx, s, snap(2000, `"kept"`))
		equal, err := s.Put(ctx, snap(2000, `"equal"`))
		require.NoError(t, err)
		lower, err := s.Put(ctx, snap(1000, `"lower"`))
		require.NoError(t, err)

		assert.False(t, equal, "the equal put")
		assert.False(t, lower, "the lower put")
		got, _, err := s.Get(ctx, tree)
		require.NoError(t, err)
		assert.Equal(t, snap(2000, `"kept"`), got)
	})

	t.Run("Delete at the stored version removes the snapshot", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		requireStored(t, ctx, s, snap(1000, `"x"`))

		require.NoError(t, s.Delete(ctx, tree, 1000))

		_, found, err := s.Get(ctx, tree)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("Delete at another version removes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		requireStored(t, ctx, s, snap(2000, `"x"`))

		require.NoError(t, s.Delete(ctx, tree, 1000))
		require.NoError(t, s.Delete(ctx, tree, 3000))

		_, found, err := s.Get(ctx, tree)
		require.NoError(t, err)
		assert.True(t, found)
	})

	t.Run("Delete removes only the named tree's snapshot", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		other := testTreeUUID(2)
		requireStored(t, ctx, s, snap(1000, `"one"`))
		requireStored(t, ctx, s, Snapshot{TreeID: other, Version: 1000, TreeType: treeTypeUnilevel, Data: []byte(`"two"`)})

		require.NoError(t, s.Delete(ctx, tree, 1000))

		got, found, err := s.Get(ctx, other)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, []byte(`"two"`), got.Data)
	})

	t.Run("an invalid put is refused and stores nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		_, err := s.Put(ctx, snap(0, `"x"`))
		assert.Error(t, err)
		_, err = s.Put(ctx, Snapshot{TreeID: tree, Version: 1, TreeType: "board_plan", Data: []byte(`"x"`)})
		assert.Error(t, err)
		_, err = s.Put(ctx, Snapshot{TreeID: tree, Version: 1, TreeType: treeTypeUnilevel})
		assert.Error(t, err)

		_, found, err := s.Get(ctx, tree)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("bytes are copied on the way in and out", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		in := []byte(`"abc"`)
		requireStored(t, ctx, s, Snapshot{TreeID: tree, Version: 1000, TreeType: treeTypeUnilevel, Data: in})
		in[1] = 'z'
		first, found, err := s.Get(ctx, tree)
		require.NoError(t, err)
		require.True(t, found)
		first.Data[1] = 'y'

		got, _, err := s.Get(ctx, tree)

		require.NoError(t, err)
		assert.Equal(t, []byte(`"abc"`), got.Data)
	})

	t.Run("trees keep separate snapshots", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		other := testTreeUUID(2)
		requireStored(t, ctx, s, snap(1000, `"one"`))
		requireStored(t, ctx, s, Snapshot{TreeID: other, Version: 3000, TreeType: treeTypeBinary, Data: []byte(`"two"`)})

		got, _, err := s.Get(ctx, tree)
		require.NoError(t, err)
		assert.Equal(t, snap(1000, `"one"`), got)
		got, _, err = s.Get(ctx, other)
		require.NoError(t, err)
		assert.Equal(t, Snapshot{TreeID: other, Version: 3000, TreeType: treeTypeBinary, Data: []byte(`"two"`)}, got)
	})

	t.Run("every method refuses a cancelled context", func(t *testing.T) {
		s := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, _, getErr := s.Get(ctx, tree)
		assert.ErrorIs(t, getErr, context.Canceled)
		_, putErr := s.Put(ctx, snap(1000, `"x"`))
		assert.ErrorIs(t, putErr, context.Canceled)
		assert.ErrorIs(t, s.Delete(ctx, tree, 1000), context.Canceled)
	})
}

func TestMemorySnapshotStore_Suite(t *testing.T) {
	runSnapshotStoreSuite(t, func(t *testing.T) SnapshotStore { return NewMemorySnapshotStore() })
}

func TestPostgresSnapshotStore_Suite(t *testing.T) {
	runSnapshotStoreSuite(t, func(t *testing.T) SnapshotStore {
		if pgContainer == nil {
			t.Skip("Postgres container not available")
		}
		return NewPostgresSnapshotStore(pgContainer.NewPool(t))
	})
}

// requireStored puts snap and fails the test unless the store reports it stored.
func requireStored(t *testing.T, ctx context.Context, s SnapshotStore, snap Snapshot) {
	t.Helper()
	stored, err := s.Put(ctx, snap)
	require.NoError(t, err)
	require.True(t, stored, "Put of tree %s at version %d reported nothing stored", snap.TreeID, snap.Version)
}
