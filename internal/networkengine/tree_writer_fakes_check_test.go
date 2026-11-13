package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRootedFakeEngine(t *testing.T) *fakeWriterEngine {
	t.Helper()
	engine := newFakeWriterEngine()
	require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeTypeUnilevel))
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
	engine := newRootedFakeEngine(t)

	err := engine.AddNode(context.Background(), writerTree, writerChild, writerRoot, writerAbsentSponsor, writeTime.Unix())

	requireEngineRefusal(t, err, "SPONSOR_NOT_FOUND", "sponsor "+writerAbsentSponsor+" not found in tree")
	assert.NotContains(t, engine.trees[writerTree].nodes, writerChild)
}

func TestFakeWriterEngine_RefusesAPlacementAtAPositionWithAnAbsentSponsor(t *testing.T) {
	engine := newRootedFakeEngine(t)

	err := engine.AddNodeAt(context.Background(), writerTree, writerChild, writerRoot, writerAbsentSponsor, 0, writeTime.Unix())

	requireEngineRefusal(t, err, "SPONSOR_NOT_FOUND", "sponsor "+writerAbsentSponsor+" not found in tree")
	assert.NotContains(t, engine.trees[writerTree].nodes, writerChild)
}

func TestFakeWriterEngine_RefusesAPlacementCheckWithAnAbsentSponsor(t *testing.T) {
	for name, check := range map[string]Mutation{
		"add_node":    CheckAddNode(writerChild, writerRoot, writerAbsentSponsor, writeTime.Unix()),
		"add_node_at": CheckAddNodeAt(writerChild, writerRoot, writerAbsentSponsor, 0, writeTime.Unix()),
	} {
		t.Run(name, func(t *testing.T) {
			engine := newRootedFakeEngine(t)

			err := engine.CheckMutation(context.Background(), writerTree, check)

			requireEngineRefusal(t, err, "SPONSOR_NOT_FOUND", "sponsor "+writerAbsentSponsor+" not found in tree")
		})
	}
}

func TestFakeWriterEngine_RefusesAnAbsentParentBeforeAnAbsentSponsor(t *testing.T) {
	engine := newRootedFakeEngine(t)

	applyErr := engine.AddNode(context.Background(), writerTree, writerChild, writerOther, writerAbsentSponsor, writeTime.Unix())
	checkErr := engine.CheckMutation(context.Background(), writerTree,
		CheckAddNode(writerChild, writerOther, writerAbsentSponsor, writeTime.Unix()))

	requireEngineRefusal(t, applyErr, engineCodeUserNotFound, "user "+writerOther+" not found in tree")
	requireEngineRefusal(t, checkErr, engineCodeUserNotFound, "user "+writerOther+" not found in tree")
}

func TestFakeWriterEngine_RefusesAHeldUserBeforeAnAbsentParentOrSponsor(t *testing.T) {
	for name, check := range map[string]Mutation{
		"add_node":    CheckAddNode(writerRoot, writerOther, writerAbsentSponsor, writeTime.Unix()),
		"add_node_at": CheckAddNodeAt(writerRoot, writerOther, writerAbsentSponsor, 0, writeTime.Unix()),
	} {
		t.Run(name, func(t *testing.T) {
			engine := newRootedFakeEngine(t)

			applyErr := engine.AddNode(context.Background(), writerTree, writerRoot, writerOther, writerAbsentSponsor, writeTime.Unix())
			checkErr := engine.CheckMutation(context.Background(), writerTree, check)

			requireEngineRefusal(t, applyErr, engineCodeUserAlreadyExists, "user "+writerRoot+" already exists in tree")
			requireEngineRefusal(t, checkErr, engineCodeUserAlreadyExists, "user "+writerRoot+" already exists in tree")
		})
	}
}

func TestFakeWriterEngine_AcceptsAPlacementWhoseSponsorIsHeld(t *testing.T) {
	engine := newRootedFakeEngine(t)

	require.NoError(t, engine.CheckMutation(context.Background(), writerTree,
		CheckAddNode(writerChild, writerRoot, writerRoot, writeTime.Unix())))
	require.NoError(t, engine.AddNode(context.Background(), writerTree, writerChild, writerRoot, writerRoot, writeTime.Unix()))
	assert.Contains(t, engine.trees[writerTree].nodes, writerChild)
}
