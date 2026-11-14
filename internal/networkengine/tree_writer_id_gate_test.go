package networkengine

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeWriter_RefusesANilIDBeforeTheLock(t *testing.T) {
	nilID := uuid.Nil.String()
	cases := []struct {
		name    string
		setup   bool
		write   func(context.Context, *TreeWriter) error
		wantErr string
	}{
		{"a root whose user is nil", false, func(ctx context.Context, w *TreeWriter) error {
			req := unilevelRootRequest()
			req.UserID = nilID
			_, err := w.AddRoot(ctx, req)
			return err
		}, "root_added in tree " + writerTree + " has the nil UUID as user_id"},
		{"a placement whose user is nil", true, func(ctx context.Context, w *TreeWriter) error {
			_, err := w.Place(ctx, placeRequest(nilID, nil))
			return err
		}, "node_placed in tree " + writerTree + " has the nil UUID as user_id"},
		{"a placement whose parent is nil", true, func(ctx context.Context, w *TreeWriter) error {
			req := placeRequest(writerChild, nil)
			req.ParentID = nilID
			_, err := w.Place(ctx, req)
			return err
		}, "node_placed in tree " + writerTree + " has the nil UUID as parent_id"},
		{"a placement whose sponsor is nil", true, func(ctx context.Context, w *TreeWriter) error {
			req := placeRequest(writerChild, nil)
			req.SponsorID = nilID
			_, err := w.Place(ctx, req)
			return err
		}, "node_placed in tree " + writerTree + " has the nil UUID as sponsor_id"},
		{"a removal whose user is nil", true, func(ctx context.Context, w *TreeWriter) error {
			_, err := w.Remove(ctx, RemoveRequest{TreeID: writerTree, UserID: nilID, RemovedAt: writeTime})
			return err
		}, "node_removed in tree " + writerTree + " has the nil UUID as user_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newWriterEnv()
			if tc.setup {
				mustAddRoot(t, env, treeTypeUnilevel)
			}
			before := len(streamEvents(t, env.events, TreeStreamName(writerTree)))
			engine := newFakeWriterEngine()
			w := NewTreeWriter(env.events, env.store, env.snapshots, engine, refusingLocker{t})

			err := tc.write(context.Background(), w)

			require.EqualError(t, err, tc.wantErr)
			assert.Empty(t, engine.calls)
			assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), before)
		})
	}
}

func TestTreeWriterAddRoot_AppendsARootWhoseSponsorIsNil(t *testing.T) {
	env := newWriterEnv()
	w, _ := env.writer()
	req := unilevelRootRequest()
	req.SponsorID = uuid.Nil.String()

	res, err := w.AddRoot(context.Background(), req)

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
}
