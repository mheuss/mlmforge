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
			ID: userID, TreeID: "t", UserID: userID, Depth: 0,
			EnrolledAt: time.Unix(1, 0),
		}
	}
	child := func(userID, parentID string, enrolled int64) networkengine.TreeNodeRow {
		return networkengine.TreeNodeRow{
			ID: userID, TreeID: "t", UserID: userID, Depth: 1,
			ParentID: &parentID, SponsorID: &parentID,
			EnrolledAt: time.Unix(enrolled, 0),
		}
	}

	store := networkengine.NewMemoryTreeStore()
	for _, r := range []networkengine.TreeNodeRow{
		root("u0"), child("u1", "u0", 2), child("u2", "u0", 3),
	} {
		require.NoError(t, store.InsertNode(t.Context(), r))
	}

	_, err := networkengine.NewTreeLoader(store, failingMutator{failOn: "AddRoot"}).
		LoadTree(t.Context(), "t", "unilevel")

	var incomplete *networkengine.TreeLoadIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, networkengine.TreeLoadStageRoot, incomplete.Stage)
	require.Equal(t, 2, incomplete.Total, "two non-root rows were read")
	require.Equal(t, 0, incomplete.Attempted, "no placement was attempted")
	require.Equal(t, 0, incomplete.Confirmed, "no placement was acknowledged")
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
		"tree load incomplete: tree t7: stage root: placement 0 of 47, 0 acknowledged",
		e.Error())
}
