package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type lockResult struct {
	unlock func() error
	err    error
}

func lockInBackground(ctx context.Context, locker TreeLocker, tree uuid.UUID) <-chan lockResult {
	out := make(chan lockResult, 1)
	go func() {
		unlock, err := locker.Lock(ctx, tree)
		out <- lockResult{unlock, err}
	}()
	return out
}

// lockTimeout is the wait given to a Lock that must time out.
const lockTimeout = 300 * time.Millisecond

// requireLockTimesOut requires a Lock on tree to end with context.DeadlineExceeded.
func requireLockTimesOut(t *testing.T, locker TreeLocker, tree uuid.UUID, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	unlock, err := locker.Lock(ctx, tree)
	if err == nil {
		_ = unlock()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded, msg)
}

// runTreeLockerSuite is the behavioural contract both tree lockers share.
// newLocker must return a fresh locker on each call.
func runTreeLockerSuite(t *testing.T, newLocker func(t *testing.T) TreeLocker) {
	t.Helper()

	t.Run("a second Lock waits until the holder releases", func(t *testing.T) {
		locker := newLocker(t)
		tree := uuid.MustParse(testTreeUUID(11))
		unlock, err := locker.Lock(context.Background(), tree)
		require.NoError(t, err)

		requireLockTimesOut(t, locker, tree, "a second Lock on a held tree did not time out")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		waiting := lockInBackground(ctx, locker, tree)
		select {
		case <-waiting:
			t.Fatal("a waiting Lock on a held tree returned before the release")
		case <-time.After(lockTimeout):
		}

		require.NoError(t, unlock())
		var got lockResult
		select {
		case got = <-waiting:
		case <-time.After(2 * time.Second):
			t.Fatal("the waiting Lock did not return within 2s of the release")
		}
		require.NoError(t, got.err)
		requireLockTimesOut(t, locker, tree, "a Lock took the tree while the waiting Lock held it")
		require.NoError(t, got.unlock())
	})

	t.Run("different trees do not exclude", func(t *testing.T) {
		locker := newLocker(t)
		held, other := uuid.MustParse(testTreeUUID(12)), uuid.MustParse(testTreeUUID(13))
		require.NotEqual(t, treeLockKey(held), treeLockKey(other),
			"treeLockKey returned one key for both trees in the different-trees fixture")

		unlock, err := locker.Lock(context.Background(), held)
		require.NoError(t, err)
		defer func() { _ = unlock() }()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		otherUnlock, err := locker.Lock(ctx, other)
		require.NoError(t, err, "a Lock on a second tree did not return within 2s while the first tree was held")
		require.NoError(t, otherUnlock())
	})

	t.Run("a stale unlock does not free the next holder", func(t *testing.T) {
		locker := newLocker(t)
		tree := uuid.MustParse(testTreeUUID(14))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		first, err := locker.Lock(ctx, tree)
		require.NoError(t, err)
		require.NoError(t, first())
		second, err := locker.Lock(ctx, tree)
		require.NoError(t, err)
		defer func() { _ = second() }()

		staleErr := first()
		requireLockTimesOut(t, locker, tree, "a third Lock took the tree while the second holder held it")
		require.Error(t, staleErr, "the first holder's second unlock returned nil")
	})

	t.Run("a waiting Lock gives up when its context ends", func(t *testing.T) {
		locker := newLocker(t)
		tree := uuid.MustParse(testTreeUUID(15))
		unlock, err := locker.Lock(context.Background(), tree)
		require.NoError(t, err)
		defer func() { _ = unlock() }()

		ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
		defer cancel()
		waiting := lockInBackground(ctx, locker, tree)
		var got lockResult
		select {
		case got = <-waiting:
		case <-time.After(lockTimeout + 2*time.Second):
			t.Fatal("a waiting Lock did not return within 2s of its context ending")
		}
		if got.err == nil {
			_ = got.unlock()
		}
		require.ErrorIs(t, got.err, context.DeadlineExceeded)
	})

	t.Run("an ended context is refused on a free tree and takes no slot", func(t *testing.T) {
		locker := newLocker(t)
		tree := uuid.MustParse(testTreeUUID(16))
		ended, cancelEnded := context.WithCancel(context.Background())
		cancelEnded()

		_, err := locker.Lock(ended, tree)
		require.ErrorIs(t, err, context.Canceled)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		next, err := locker.Lock(ctx, tree)
		require.NoError(t, err, "a Lock after the refused call did not take the free tree within 2s")
		require.NoError(t, next())
	})

	t.Run("a second unlock reports itself", func(t *testing.T) {
		locker := newLocker(t)
		tree := uuid.MustParse(testTreeUUID(17))
		unlock, err := locker.Lock(context.Background(), tree)
		require.NoError(t, err)

		require.NoError(t, unlock())
		require.EqualError(t, unlock(),
			"unlock of tree "+tree.String()+" was called after the lock was released")
	})
}
