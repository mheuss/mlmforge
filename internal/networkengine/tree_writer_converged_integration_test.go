package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lostReplyEngine applies its first RemoveNode and then returns an error, the
// way a reply lost after the engine acted looks to the caller.
type lostReplyEngine struct {
	TreeRebuilder
	removes int
}

func (e *lostReplyEngine) RemoveNode(ctx context.Context, structure, userID string) ([]Responsored, error) {
	e.removes++
	moved, err := e.TreeRebuilder.RemoveNode(ctx, structure, userID)
	if e.removes == 1 && err == nil {
		return nil, errors.New("lostReplyEngine: the reply to the first RemoveNode was dropped")
	}
	return moved, err
}

func TestTreeWriter_ARemovalWhoseReplyWasLostConvergesOnTheStampAnotherWriterLeft(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root, a, b, c := testTreeUUID(330), testUserUUID(1), testUserUUID(2), testUserUUID(3), testUserUUID(4)
	it.buildSponsorChain(t, tree, root, a, b, c)

	var load LoadResult
	var loadErr error
	events := &lockKillingEvents{EventStore: it.events, t: t, pool: it.pool, meanwhile: func() {
		load, loadErr = it.writer(t).Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})
	}}
	engine := &lostReplyEngine{TreeRebuilder: it.engine(t)}
	first, firstErr := NewTreeWriter(events, it.store, NewMemorySnapshotStore(), engine, NewPostgresTreeLocker(it.dsn)).
		Remove(ctx, RemoveRequest{TreeID: tree, UserID: b, RemovedAt: writeTime.Add(4 * time.Hour)})

	require.NoError(t, firstErr)
	require.NoError(t, loadErr)
	require.NotNil(t, load.CaughtUp, "the load reported no caught-up event")
	assert.Equal(t, first.EventID, load.CaughtUp.EventID)
	assert.Equal(t, 2, engine.removes, "RemoveNode calls on the first writer's engine")
	assert.NoError(t, first.ProjectionErr)
	assert.Equal(t, map[string]string{root: root, a: root, c: a}, activeSponsors(t, it.store, tree))
	stamped, err := it.store.GetNodeByRemovalEvent(ctx, tree, first.EventID)
	require.NoError(t, err)
	require.NotNil(t, stamped, "no row carries the removal's stamp")
	assert.Equal(t, b, stamped.UserID)
}
