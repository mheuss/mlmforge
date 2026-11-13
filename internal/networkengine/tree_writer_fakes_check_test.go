package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRootedFakeEngine(t *testing.T, treeType string) *fakeWriterEngine {
	t.Helper()
	engine := newFakeWriterEngine()
	if treeType == treeTypeMatrix {
		require.NoError(t, engine.CreateMatrixTree(context.Background(), writerTree, 3, "breadth_first"))
	} else {
		require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeType))
	}
	require.NoError(t, engine.AddRoot(context.Background(), writerTree, writerRoot, writeTime.Unix()))
	return engine
}

// requireEngineRefusal requires err to be an engine error with code and message.
func requireEngineRefusal(t *testing.T, err error, code, message string) {
	t.Helper()
	var engineErr *EngineError
	require.ErrorAs(t, err, &engineErr)
	assert.Equal(t, code, engineErr.Code)
	assert.Equal(t, message, engineErr.Message)
}

func TestFakeWriterEngine_RefusesAPlacementWithAnAbsentSponsor(t *testing.T) {
	engine := newRootedFakeEngine(t, treeTypeUnilevel)

	err := engine.AddNode(context.Background(), writerTree, writerChild, writerRoot, writerAbsentSponsor, writeTime.Unix())

	requireEngineRefusal(t, err, "SPONSOR_NOT_FOUND", "sponsor "+writerAbsentSponsor+" not found in tree")
	assert.NotContains(t, engine.trees[writerTree].nodes, writerChild)
}

func TestFakeWriterEngine_RefusesAPlacementAtAPositionWithAnAbsentSponsor(t *testing.T) {
	engine := newRootedFakeEngine(t, treeTypeMatrix)

	err := engine.AddNodeAt(context.Background(), writerTree, writerChild, writerRoot, writerAbsentSponsor, 0, writeTime.Unix())

	requireEngineRefusal(t, err, "SPONSOR_NOT_FOUND", "sponsor "+writerAbsentSponsor+" not found in tree")
	assert.NotContains(t, engine.trees[writerTree].nodes, writerChild)
}

func TestFakeWriterEngine_RefusesAPlacementCheckWithAnAbsentSponsor(t *testing.T) {
	for name, tc := range map[string]struct {
		treeType string
		check    Mutation
	}{
		"add_node":    {treeTypeUnilevel, CheckAddNode(writerChild, writerRoot, writerAbsentSponsor, writeTime.Unix())},
		"add_node_at": {treeTypeMatrix, CheckAddNodeAt(writerChild, writerRoot, writerAbsentSponsor, 0, writeTime.Unix())},
	} {
		t.Run(name, func(t *testing.T) {
			engine := newRootedFakeEngine(t, tc.treeType)

			err := engine.CheckMutation(context.Background(), writerTree, tc.check)

			requireEngineRefusal(t, err, "SPONSOR_NOT_FOUND", "sponsor "+writerAbsentSponsor+" not found in tree")
		})
	}
}

func TestFakeWriterEngine_RefusesAnAbsentParentBeforeAnAbsentSponsor(t *testing.T) {
	engine := newRootedFakeEngine(t, treeTypeUnilevel)

	applyErr := engine.AddNode(context.Background(), writerTree, writerChild, writerOther, writerAbsentSponsor, writeTime.Unix())
	checkErr := engine.CheckMutation(context.Background(), writerTree,
		CheckAddNode(writerChild, writerOther, writerAbsentSponsor, writeTime.Unix()))

	requireEngineRefusal(t, applyErr, engineCodeUserNotFound, "user "+writerOther+" not found in tree")
	requireEngineRefusal(t, checkErr, engineCodeUserNotFound, "user "+writerOther+" not found in tree")
}

func TestFakeWriterEngine_RefusesAHeldUserBeforeAnAbsentParentOrSponsor(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		treeType string
		check    Mutation
		apply    func(*fakeWriterEngine) error
	}{
		"add_node": {treeTypeUnilevel, CheckAddNode(writerRoot, writerOther, writerAbsentSponsor, writeTime.Unix()),
			func(e *fakeWriterEngine) error {
				return e.AddNode(ctx, writerTree, writerRoot, writerOther, writerAbsentSponsor, writeTime.Unix())
			}},
		"add_node_at": {treeTypeMatrix, CheckAddNodeAt(writerRoot, writerOther, writerAbsentSponsor, 0, writeTime.Unix()),
			func(e *fakeWriterEngine) error {
				return e.AddNodeAt(ctx, writerTree, writerRoot, writerOther, writerAbsentSponsor, 0, writeTime.Unix())
			}},
	} {
		t.Run(name, func(t *testing.T) {
			engine := newRootedFakeEngine(t, tc.treeType)

			applyErr := tc.apply(engine)
			checkErr := engine.CheckMutation(ctx, writerTree, tc.check)

			requireEngineRefusal(t, applyErr, engineCodeUserAlreadyExists, "user "+writerRoot+" already exists in tree")
			requireEngineRefusal(t, checkErr, engineCodeUserAlreadyExists, "user "+writerRoot+" already exists in tree")
		})
	}
}

func TestFakeWriterEngine_AcceptsAPlacementWhoseSponsorIsHeld(t *testing.T) {
	engine := newRootedFakeEngine(t, treeTypeUnilevel)

	require.NoError(t, engine.CheckMutation(context.Background(), writerTree,
		CheckAddNode(writerChild, writerRoot, writerRoot, writeTime.Unix())))
	require.NoError(t, engine.AddNode(context.Background(), writerTree, writerChild, writerRoot, writerRoot, writeTime.Unix()))
	assert.Contains(t, engine.trees[writerTree].nodes, writerChild)
}
