package networkengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldAgreementEngine is the engine surface the held-structure agreement uses.
type heldAgreementEngine interface {
	agreementEngine
	TakeSnapshot(ctx context.Context, structure string) (*SnapshotResultDTO, error)
	RestoreSnapshot(ctx context.Context, structure, treeType string, data json.RawMessage) error
}

// createLikeWriter makes the create call the writer's build makes for treeType.
func createLikeWriter(e heldAgreementEngine, treeType string) error {
	if treeType == treeTypeMatrix {
		return e.CreateMatrixTree(context.Background(), writerTree, 3, "breadth_first")
	}
	return e.CreateTree(context.Background(), writerTree, treeType)
}

// assertHeldUnchanged asserts that writerTree in e still snapshots as before.
func assertHeldUnchanged(t *testing.T, e heldAgreementEngine, before *SnapshotResultDTO, side, call string) {
	t.Helper()
	after, err := e.TakeSnapshot(context.Background(), writerTree)
	require.NoError(t, err, "%s: take_snapshot after the refused %s", side, call)
	assert.Equal(t, before.TreeType, after.TreeType, "%s: the tree type of %s after the refused %s", side, writerTree, call)
	assert.JSONEq(t, string(before.Data), string(after.Data), "%s: the snapshot of %s after the refused %s", side, writerTree, call)
}

func TestFakeWriterEngine_RefusesAHeldStructureAsTheWorkerDoes(t *testing.T) {
	ctx := context.Background()
	for _, treeType := range []string{treeTypeUnilevel, treeTypeBinary, treeTypeMatrix} {
		t.Run(treeType, func(t *testing.T) {
			client := startWorker(t)
			fake := newFakeWriterEngine()
			restoreSetup(t, client, treeType)
			restoreSetup(t, fake, treeType)
			workerBefore, err := client.TakeSnapshot(ctx, writerTree)
			require.NoError(t, err)
			fakeBefore, err := fake.TakeSnapshot(ctx, writerTree)
			require.NoError(t, err)

			workerErr := createLikeWriter(client, treeType)
			fakeErr := createLikeWriter(fake, treeType)

			assert.Equal(t, engineCodeTreeExists, engineCodeOf(workerErr), "the worker's answer to create on a held structure")
			assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr), "create on a held structure")
			assertHeldUnchanged(t, client, workerBefore, "worker", "create")
			assertHeldUnchanged(t, fake, fakeBefore, "fake", "create")

			workerErr = client.RestoreSnapshot(ctx, writerTree, workerBefore.TreeType, workerBefore.Data)
			fakeErr = fake.RestoreSnapshot(ctx, writerTree, fakeBefore.TreeType, fakeBefore.Data)

			assert.Equal(t, engineCodeTreeExists, engineCodeOf(workerErr), "the worker's answer to restore_snapshot on a held structure")
			assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr), "restore_snapshot on a held structure")
			assertHeldUnchanged(t, client, workerBefore, "worker", "restore_snapshot")
			assertHeldUnchanged(t, fake, fakeBefore, "fake", "restore_snapshot")
		})
	}
}
