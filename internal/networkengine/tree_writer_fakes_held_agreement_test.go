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
	DropStructure(ctx context.Context, structure string) (bool, error)
}

// buildHeldTree builds writerTree in e and returns its snapshot and a snapshot
// of the same tree holding only its root.
func buildHeldTree(t *testing.T, e heldAgreementEngine, treeType string) (held, rootOnly *SnapshotResultDTO) {
	t.Helper()
	ctx := context.Background()
	agreementRooted(treeType)(t, e)
	rootOnly, err := e.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	dropped, err := e.DropStructure(ctx, writerTree)
	require.NoError(t, err)
	require.True(t, dropped)
	restoreSetup(t, e, treeType)
	held, err = e.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	require.NotEqual(t, string(rootOnly.Data), string(held.Data), "the root-only snapshot against the held one")
	return held, rootOnly
}

// createTreeOfType creates writerTree as a tree of treeType.
func createTreeOfType(e heldAgreementEngine, treeType string) error {
	if treeType == treeTypeMatrix {
		return e.CreateMatrixTree(context.Background(), writerTree, 3, "breadth_first")
	}
	return e.CreateTree(context.Background(), writerTree, treeType)
}

// assertHeldUnchanged asserts that writerTree in e still snapshots as before.
func assertHeldUnchanged(t *testing.T, e heldAgreementEngine, before *SnapshotResultDTO, side, call string) {
	t.Helper()
	after, err := e.TakeSnapshot(context.Background(), writerTree)
	require.NoError(t, err, "%s: take_snapshot after %s", side, call)
	assert.Equal(t, before.TreeType, after.TreeType, "%s: the tree type of %s after %s", side, writerTree, call)
	assert.JSONEq(t, string(before.Data), string(after.Data), "%s: the snapshot of %s after %s", side, writerTree, call)
}

func TestFakeWriterEngine_RefusesAHeldStructureAsTheWorkerDoes(t *testing.T) {
	ctx := context.Background()
	for _, treeType := range []string{treeTypeUnilevel, treeTypeBinary, treeTypeMatrix} {
		t.Run(treeType, func(t *testing.T) {
			client := startWorker(t)
			fake := newFakeWriterEngine()
			workerBefore, workerRootOnly := buildHeldTree(t, client, treeType)
			fakeBefore, fakeRootOnly := buildHeldTree(t, fake, treeType)

			workerErr := createTreeOfType(client, treeType)
			fakeErr := createTreeOfType(fake, treeType)

			assert.Equal(t, engineCodeTreeExists, engineCodeOf(workerErr), "the worker's answer to create on a held structure")
			assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr), "create on a held structure")
			assertHeldUnchanged(t, client, workerBefore, "worker", "create")
			assertHeldUnchanged(t, fake, fakeBefore, "fake", "create")

			workerErr = client.RestoreSnapshot(ctx, writerTree, workerRootOnly.TreeType, workerRootOnly.Data)
			fakeErr = fake.RestoreSnapshot(ctx, writerTree, fakeRootOnly.TreeType, fakeRootOnly.Data)

			assert.Equal(t, engineCodeTreeExists, engineCodeOf(workerErr), "the worker's answer to restore_snapshot on a held structure")
			assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr), "restore_snapshot on a held structure")
			assertHeldUnchanged(t, client, workerBefore, "worker", "restore_snapshot")
			assertHeldUnchanged(t, fake, fakeBefore, "fake", "restore_snapshot")

			workerErr = client.RestoreSnapshot(ctx, writerTree, workerBefore.TreeType, workerBefore.Data)
			fakeErr = fake.RestoreSnapshot(ctx, writerTree, fakeBefore.TreeType, fakeBefore.Data)

			assert.Equal(t, engineCodeTreeExists, engineCodeOf(workerErr),
				"the worker's answer to restore_snapshot of the held structure's own snapshot")
			assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr),
				"restore_snapshot of the held structure's own snapshot")

			payload := json.RawMessage(`{"nodes":1}`)
			workerErr = client.RestoreSnapshot(ctx, writerTree, treeType, payload)
			fakeErr = fake.RestoreSnapshot(ctx, writerTree, treeType, payload)

			assert.Equal(t, engineCodeTreeExists, engineCodeOf(workerErr),
				"the worker's answer to restore_snapshot of %s on a held structure", payload)
			assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr),
				"restore_snapshot of %s on a held structure", payload)
			assertHeldUnchanged(t, client, workerBefore, "worker", "restore_snapshot of "+string(payload))
			assertHeldUnchanged(t, fake, fakeBefore, "fake", "restore_snapshot of "+string(payload))

			workerErr = client.RestoreSnapshot(ctx, restoredTree, treeType, payload)
			fakeErr = fake.RestoreSnapshot(ctx, restoredTree, treeType, payload)

			assert.NotEmpty(t, engineCodeOf(workerErr),
				"the worker's answer to restore_snapshot of %s on an unheld structure", payload)
			assert.Equal(t, engineCodeOf(workerErr), engineCodeOf(fakeErr),
				"the codes for restore_snapshot of %s on an unheld structure", payload)
			_, workerTakeErr := client.TakeSnapshot(ctx, restoredTree)
			_, fakeTakeErr := fake.TakeSnapshot(ctx, restoredTree)
			assert.Equal(t, "STRUCTURE_NOT_FOUND", engineCodeOf(workerTakeErr),
				"the worker's answer to take_snapshot of %s after restore_snapshot of %s", restoredTree, payload)
			assert.Equal(t, engineCodeOf(workerTakeErr), engineCodeOf(fakeTakeErr),
				"the codes for take_snapshot of %s after restore_snapshot of %s", restoredTree, payload)
		})
	}
}
