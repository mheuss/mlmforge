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

		for name, bad := range map[string]Snapshot{
			"version 0":         snap(0, `"x"`),
			"unknown tree type": {TreeID: tree, Version: 1, TreeType: "board_plan", Data: []byte(`"x"`)},
			"nil data":          {TreeID: tree, Version: 1, TreeType: treeTypeUnilevel},
		} {
			stored, err := s.Put(ctx, bad)
			assert.Error(t, err, name)
			assert.False(t, stored, name)
		}

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

	attempt := func(version int64, stored bool, observed string) SnapshotAttempt {
		return SnapshotAttempt{TreeID: tree, Version: version, Stored: stored, Observed: observed}
	}
	requireAttempt := func(t *testing.T, s SnapshotStore, want SnapshotAttempt) {
		t.Helper()
		got, found, err := s.LastAttempt(context.Background(), tree)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, want, got)
	}

	t.Run("a tree with no attempt reports none", func(t *testing.T) {
		_, found, err := newStore(t).LastAttempt(context.Background(), tree)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("LastAttempt returns what RecordAttempt recorded", func(t *testing.T) {
		for name, a := range map[string]SnapshotAttempt{
			"failed": attempt(1000, false, "storing the snapshot returned: disk full"),
			"stored": attempt(1000, true, ""),
		} {
			t.Run(name, func(t *testing.T) {
				s := newStore(t)
				require.NoError(t, s.RecordAttempt(context.Background(), a))

				requireAttempt(t, s, a)
			})
		}
	})

	t.Run("a higher attempt replaces the record", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.RecordAttempt(ctx, attempt(1000, false, "first")))
		require.NoError(t, s.RecordAttempt(ctx, attempt(2000, true, "")))

		requireAttempt(t, s, attempt(2000, true, ""))
	})

	t.Run("a lower attempt over the same outcome is ignored", func(t *testing.T) {
		cases := map[string]struct{ kept, lower SnapshotAttempt }{
			"stored over stored": {attempt(2000, true, ""), attempt(1000, true, "")},
			"failed over failed": {attempt(2000, false, "kept"), attempt(1000, false, "lower")},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				s := newStore(t)
				ctx := context.Background()
				require.NoError(t, s.RecordAttempt(ctx, tc.kept))
				require.NoError(t, s.RecordAttempt(ctx, tc.lower))

				requireAttempt(t, s, tc.kept)
			})
		}
	})

	t.Run("a lower stored attempt over a failed one is ignored", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.RecordAttempt(ctx, attempt(2000, false, "kept")))
		require.NoError(t, s.RecordAttempt(ctx, attempt(1000, true, "")))

		requireAttempt(t, s, attempt(2000, false, "kept"))
	})

	t.Run("a lower failed attempt over a stored one replaces it", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.RecordAttempt(ctx, attempt(2000, true, "")))
		require.NoError(t, s.RecordAttempt(ctx, attempt(1000, false, "stored nothing")))

		requireAttempt(t, s, attempt(1000, false, "stored nothing"))
	})

	t.Run("an attempt at an equal version replaces the record", func(t *testing.T) {
		cases := map[string]struct{ first, second SnapshotAttempt }{
			"failed over stored": {attempt(1000, true, ""), attempt(1000, false, "second")},
			"stored over failed": {attempt(1000, false, "first"), attempt(1000, true, "")},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				s := newStore(t)
				ctx := context.Background()
				require.NoError(t, s.RecordAttempt(ctx, tc.first))
				require.NoError(t, s.RecordAttempt(ctx, tc.second))

				requireAttempt(t, s, tc.second)
			})
		}
	})

	t.Run("an invalid attempt is refused and records nothing", func(t *testing.T) {
		cases := map[string]SnapshotAttempt{
			"version 0":            attempt(0, false, "x"),
			"stored with text":     attempt(1000, true, "x"),
			"failed without text":  attempt(1000, false, ""),
			"text with a NUL byte": attempt(1000, false, "a\x00b"),
			"text not valid UTF-8": attempt(1000, false, "\xff"),
		}
		for name, a := range cases {
			t.Run(name, func(t *testing.T) {
				s := newStore(t)

				assert.Error(t, s.RecordAttempt(context.Background(), a))

				_, found, err := s.LastAttempt(context.Background(), tree)
				require.NoError(t, err)
				assert.False(t, found)
			})
		}
	})

	t.Run("a valid attempt of each kind is accepted", func(t *testing.T) {
		for name, a := range map[string]SnapshotAttempt{
			"stored": attempt(1, true, ""),
			"failed": attempt(1, false, "x"),
		} {
			t.Run(name, func(t *testing.T) {
				s := newStore(t)
				require.NoError(t, s.RecordAttempt(context.Background(), a))

				requireAttempt(t, s, a)
			})
		}
	})

	t.Run("trees keep separate attempts", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		other := testTreeUUID(2)
		require.NoError(t, s.RecordAttempt(ctx, attempt(1000, true, "")))
		require.NoError(t, s.RecordAttempt(ctx, SnapshotAttempt{TreeID: other, Version: 3000, Observed: "other"}))

		requireAttempt(t, s, attempt(1000, true, ""))
		got, found, err := s.LastAttempt(ctx, other)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, SnapshotAttempt{TreeID: other, Version: 3000, Observed: "other"}, got)
	})

	t.Run("Delete leaves the attempt", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		requireStored(t, ctx, s, snap(1000, `"x"`))
		require.NoError(t, s.RecordAttempt(ctx, attempt(1000, true, "")))

		require.NoError(t, s.Delete(ctx, tree, 1000))

		requireAttempt(t, s, attempt(1000, true, ""))
	})

	t.Run("every method refuses a cancelled context", func(t *testing.T) {
		s := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, _, getErr := s.Get(ctx, tree)
		assert.ErrorIs(t, getErr, context.Canceled)
		stored, putErr := s.Put(ctx, snap(1000, `"x"`))
		assert.ErrorIs(t, putErr, context.Canceled)
		assert.False(t, stored, "a refused put")
		assert.ErrorIs(t, s.Delete(ctx, tree, 1000), context.Canceled)
		_, _, lastErr := s.LastAttempt(ctx, tree)
		assert.ErrorIs(t, lastErr, context.Canceled)
		assert.ErrorIs(t, s.RecordAttempt(ctx, attempt(1000, true, "")), context.Canceled)
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
