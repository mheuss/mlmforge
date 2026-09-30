package networkengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readCountingStore counts the store reads a load makes, and fails the ones a
// test names.
type readCountingStore struct {
	TreeStore
	versionReads   int
	depthReads     int
	treeReads      int
	versionErr     error
	versionErrFrom int
	treeErr        error
}

func (s *readCountingStore) ProjectedVersion(ctx context.Context, treeID string) (int64, bool, error) {
	s.versionReads++
	if s.versionErr != nil && s.versionReads >= s.versionErrFrom {
		return 0, false, s.versionErr
	}
	return s.TreeStore.ProjectedVersion(ctx, treeID)
}

func (s *readCountingStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	s.depthReads++
	return s.TreeStore.GetByTreeDepthOrdered(ctx, treeID)
}

func (s *readCountingStore) GetByTree(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	s.treeReads++
	if s.treeErr != nil {
		return nil, s.treeErr
	}
	return s.TreeStore.GetByTree(ctx, treeID)
}

func TestTreeWriter_AWriteKeepsItsProjectedVersionReadError(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	store := &readCountingStore{TreeStore: env.store, versionErr: errors.New("connection reset"), versionErrFrom: 1}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.EqualError(t, err, "read the projected version of tree "+writerTree+
		"; nothing was appended: connection reset")
	var rejected *TreeLoadRejectedError
	assert.False(t, errors.As(err, &rejected), "a write's read failure was typed as a load rejection")
}
