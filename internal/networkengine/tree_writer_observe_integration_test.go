package networkengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingAddEngine refuses every placement and passes every other call through.
type failingAddEngine struct {
	TreeEngineChecker
}

func (failingAddEngine) AddNode(context.Context, string, string, string, string, int64, ...AddNodeOption) error {
	return errors.New("failingAddEngine: add_node refused")
}

// A placement's row and version commit before its engine call. An engine that
// then fails leaves a store that is current, and the write has to say so.
func TestTreeWriter_ObservesTheStoreCurrentWhenTheEngineFailsAfterTheInsert(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, child := testTreeUUID(320), testUserUUID(1), testUserUUID(2)
	it.addUnilevelRoot(t, tree, root)
	w := NewTreeWriter(it.events, it.store, failingAddEngine{TreeEngineChecker: it.engine(t)}, NewPostgresTreeLocker(it.dsn))

	res, err := w.Place(ctx, PlaceRequest{TreeID: tree, UserID: child, ParentID: root, SponsorID: root, EnrolledAt: writeTime})

	require.NoError(t, err)
	require.ErrorContains(t, res.ProjectionErr, "failingAddEngine: add_node refused")
	assert.Equal(t, &ProjectionObservation{Version: 2, Found: true}, res.Observed)
	row, err := it.store.GetNode(ctx, tree, child)
	require.NoError(t, err)
	assert.NotNil(t, row, "the placement's row did not commit before the engine call")
}
