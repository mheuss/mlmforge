package networkengine_test

import (
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/require"
)

// Drives a root-stage failure from outside the package and reads the three
// counts a caller reports.
func TestLoadTree_RootStageReportsTheNonRootCount(t *testing.T) {
	root := func(userID string) networkengine.TreeNodeRow {
		return networkengine.TreeNodeRow{
			ID: userID, TreeID: extTree, UserID: userID, Depth: 0,
			EnrolledAt: time.Unix(1, 0),
		}
	}
	child := func(userID, parentID string, enrolled int64) networkengine.TreeNodeRow {
		return networkengine.TreeNodeRow{
			ID: userID, TreeID: extTree, UserID: userID, Depth: 1,
			ParentID: &parentID, SponsorID: &parentID,
			EnrolledAt: time.Unix(enrolled, 0),
		}
	}

	store := networkengine.NewMemoryTreeStore()
	for i, r := range []networkengine.TreeNodeRow{
		root(extUser0), child(extUser1, extUser0, 2), child(extUser2, extUser0, 3),
	} {
		require.NoError(t, store.ProjectInsert(t.Context(), r, int64(i+1)))
	}

	_, err := networkengine.NewTreeLoader(store, failingMutator{failOn: "AddRoot"}).
		LoadTree(t.Context(), extTree, "unilevel")

	var incomplete *networkengine.TreeLoadIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, networkengine.TreeLoadStageRoot, incomplete.Stage)
	require.Equal(t, 2, incomplete.Total, "two non-root rows were read")
	require.Equal(t, 0, incomplete.Attempted, "no non-root placement was attempted")
	require.Equal(t, 0, incomplete.Confirmed, "no non-root placement was acknowledged")
}

// Pins what a keyed literal renders at the root stage. The placement index of
// zero is deliberate, so do not correct it into something that reads better.
func TestTreeLoadIncomplete_RootStageFallbackString(t *testing.T) {
	e := &networkengine.TreeLoadIncompleteError{
		TreeID: "t7",
		Stage:  networkengine.TreeLoadStageRoot,
		Total:  47,
	}

	require.Equal(t,
		"tree load incomplete: tree t7: stage root: non-root placement 0 of 47, 0 acknowledged",
		e.Error())
}
