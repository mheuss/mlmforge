package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeWriter_LoadRebuildsEachTreeTypeFromItsStream(t *testing.T) {
	zero, one, two := 0, 1, 2
	width, spillover := 3, "breadth_first"
	cases := []struct {
		name      string
		root      AddRootRequest
		positions []*int
	}{
		{"unilevel", AddRootRequest{TreeType: treeTypeUnilevel}, []*int{nil, nil}},
		{"binary", AddRootRequest{TreeType: treeTypeBinary}, []*int{&zero, &one}},
		{"matrix", AddRootRequest{TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}, []*int{&two, &zero}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := newWriterIntegration(t)
			ctx := context.Background()
			tree, root := testTreeUUID(2970+i), testUserUUID(1)
			req := tc.root
			req.TreeID, req.UserID, req.SponsorID, req.EnrolledAt = tree, root, root, writeTime
			res, err := it.writer(t).AddRoot(ctx, req)
			require.NoError(t, err)
			require.NoError(t, res.ProjectionErr)
			for j, pos := range tc.positions {
				res, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: testUserUUID(2 + j),
					ParentID: root, SponsorID: root, Position: pos, EnrolledAt: writeTime})
				require.NoError(t, err)
				require.NoError(t, res.ProjectionErr)
			}

			engine := it.engine(t)
			loaded, err := NewTreeWriter(it.events, it.store, it.snapshots, engine, NewPostgresTreeLocker(it.dsn)).
				Load(ctx, LoadRequest{TreeID: tree, TreeType: tc.root.TreeType})

			require.NoError(t, err)
			require.NoError(t, loaded.ReleaseErr)
			assert.Empty(t, loaded.Warnings)
			assert.Equal(t, 3, loaded.Nodes)
			down, err := engine.GetDownline(ctx, tree, root, 0)
			require.NoError(t, err)
			assert.Len(t, down, 2)
			for j, pos := range tc.positions {
				if pos == nil {
					continue
				}
				got, err := engine.GetPosition(ctx, tree, testUserUUID(2+j))
				require.NoError(t, err)
				assert.Equal(t, *pos, int(got.Position), "child %d", j)
			}
		})
	}
}

func TestTreeWriter_LoadRefusesAStoreMissingARowTheStreamPlaced(t *testing.T) {
	it := newWriterIntegration(t)
	ctx := context.Background()
	tree, root := testTreeUUID(2980), testUserUUID(1)
	it.addUnilevelRoot(t, tree, root)
	for j := range 2 {
		res, err := it.writer(t).Place(ctx, PlaceRequest{TreeID: tree, UserID: testUserUUID(2 + j),
			ParentID: root, SponsorID: root, EnrolledAt: writeTime})
		require.NoError(t, err)
		require.NoError(t, res.ProjectionErr)
	}
	_, err := it.pool.Exec(ctx, "DELETE FROM tree_nodes WHERE tree_id = $1 AND user_id = $2", tree, testUserUUID(3))
	require.NoError(t, err)

	loaded, err := NewTreeWriter(it.events, it.store, it.snapshots, it.engine(t), NewPostgresTreeLocker(it.dsn)).
		Load(ctx, LoadRequest{TreeID: tree, TreeType: treeTypeUnilevel})

	var replay *TreeReplayError
	require.ErrorAs(t, err, &replay)
	assert.Equal(t, TreeReplayStageMembership, replay.Stage)
	require.NoError(t, loaded.ReleaseErr)
}
