package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeWriter_ASecondWriterOverOneEngineStopsAtTreeExists(t *testing.T) {
	ops := []struct {
		name    string
		pending bool
		run     func(w *TreeWriter, pendingID string) error
	}{
		{name: "remove", run: func(w *TreeWriter, _ string) error {
			_, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})
			return err
		}},
		{name: "load", pending: true, run: func(w *TreeWriter, _ string) error {
			_, err := w.Load(context.Background(), loadRequest())
			return err
		}},
		{name: "reject", pending: true, run: func(w *TreeWriter, pendingID string) error {
			_, err := w.Reject(context.Background(), RejectRequest{TreeID: writerTree, EventID: pendingID, Reason: "a second writer"})
			return err
		}},
	}
	builds := []struct {
		name     string
		snapshot bool
		call     string
	}{
		{name: "built from the stream", call: "create_tree " + treeTypeUnilevel},
		{name: "built from a snapshot", snapshot: true, call: "restore_snapshot"},
	}
	for _, op := range ops {
		for _, build := range builds {
			t.Run(op.name+" "+build.name, func(t *testing.T) {
				ctx := context.Background()
				env := newWriterEnv()
				mustAddRoot(t, env, treeTypeUnilevel)
				mustPlace(t, env, writerChild, nil)
				if build.snapshot {
					seedSnapshot(t, env, 2)
				}
				engine := newFakeWriterEngine()
				first := NewTreeWriter(env.events, env.store, env.snapshots, engine, env.locker)
				_, err := first.Load(ctx, loadRequest())
				require.NoError(t, err)
				var pendingID string
				if op.pending {
					pendingID = appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
						TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime, TreeType: treeTypeUnilevel,
					}).ID
				}
				before, err := engine.TakeSnapshot(ctx, writerTree)
				require.NoError(t, err)
				stream := streamEvents(t, env.events, TreeStreamName(writerTree))
				calls := len(engine.calls)
				second := NewTreeWriter(env.events, env.store, env.snapshots, engine, env.locker)

				err = op.run(second, pendingID)

				assert.Equal(t, []string{build.call}, engine.calls[calls:],
					"the engine calls the second writer's %s made after the first writer built tree %s", op.name, writerTree)
				after, takeErr := engine.TakeSnapshot(ctx, writerTree)
				require.NoError(t, takeErr)
				assert.JSONEq(t, string(before.Data), string(after.Data),
					"the engine's copy of tree %s after the second writer's %s", writerTree, op.name)
				assert.Equal(t, stream, streamEvents(t, env.events, TreeStreamName(writerTree)),
					"the stream of tree %s after the second writer's %s", writerTree, op.name)
				assert.Equal(t, engineCodeTreeExists, engineCodeOf(err),
					"the second writer's %s over an engine already holding tree %s returned: %v", op.name, writerTree, err)
			})
		}
	}
}
