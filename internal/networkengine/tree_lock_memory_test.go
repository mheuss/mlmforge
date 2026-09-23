package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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

func TestMemoryTreeLocker_ExcludesASecondHolderUntilRelease(t *testing.T) {
	locker := NewMemoryTreeLocker()
	tree := uuid.MustParse(testTreeUUID(1))

	unlock, err := locker.Lock(context.Background(), tree)
	require.NoError(t, err)
	second := lockInBackground(context.Background(), locker, tree)

	select {
	case <-second:
		t.Fatal("a second Lock on a held tree returned before the release")
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, unlock())
	select {
	case got := <-second:
		require.NoError(t, got.err)
		require.NoError(t, got.unlock())
	case <-time.After(2 * time.Second):
		t.Fatal("the second Lock did not return within 2s of the release")
	}
}

func TestMemoryTreeLocker_DifferentTreesDoNotExclude(t *testing.T) {
	locker := NewMemoryTreeLocker()
	unlock, err := locker.Lock(context.Background(), uuid.MustParse(testTreeUUID(1)))
	require.NoError(t, err)
	defer func() { _ = unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	other, err := locker.Lock(ctx, uuid.MustParse(testTreeUUID(2)))

	require.NoError(t, err)
	require.NoError(t, other())
}

func TestMemoryTreeLocker_GivesUpWhenTheContextEnds(t *testing.T) {
	locker := NewMemoryTreeLocker()
	tree := uuid.MustParse(testTreeUUID(1))
	unlock, err := locker.Lock(context.Background(), tree)
	require.NoError(t, err)
	defer func() { _ = unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = locker.Lock(ctx, tree)

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMemoryTreeLocker_RefusesAnEndedContextOnAFreeTree(t *testing.T) {
	locker := NewMemoryTreeLocker()
	tree := uuid.MustParse(testTreeUUID(1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := locker.Lock(ctx, tree)

	require.ErrorIs(t, err, context.Canceled)
	unlock, err := locker.Lock(context.Background(), tree)
	require.NoError(t, err, "the refused call must not have taken the slot")
	require.NoError(t, unlock())
}

func TestMemoryTreeLocker_AStaleUnlockDoesNotFreeTheNextHolder(t *testing.T) {
	locker := NewMemoryTreeLocker()
	tree := uuid.MustParse(testTreeUUID(1))
	first, err := locker.Lock(context.Background(), tree)
	require.NoError(t, err)
	require.NoError(t, first())
	second, err := locker.Lock(context.Background(), tree)
	require.NoError(t, err)
	defer func() { _ = second() }()

	require.Error(t, first(), "the first holder's unlock ran twice")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = locker.Lock(ctx, tree)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the second holder must still hold the tree")
}

func TestMemoryTreeLocker_ASecondUnlockReportsItself(t *testing.T) {
	locker := NewMemoryTreeLocker()
	tree := uuid.MustParse(testTreeUUID(1))
	unlock, err := locker.Lock(context.Background(), tree)
	require.NoError(t, err)

	require.NoError(t, unlock())
	assert.EqualError(t, unlock(),
		"unlock of tree "+tree.String()+" was called after the lock was released")
}
