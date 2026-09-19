package networkengine_test

import (
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/require"
)

// A root placement that fails strands a structure holding every node the load
// read. The counts are what a caller reports to whoever decides on a restart.
func TestLoadTree_RootStageReportsTheStrandedSize(t *testing.T) {
	store := networkengine.NewMemoryTreeStore()
	rows := []networkengine.TreeNodeRow{
		{ID: "u0", TreeID: "t", UserID: "u0", Depth: 0, EnrolledAt: time.Unix(1, 0)},
		{ID: "u1", TreeID: "t", UserID: "u1", Depth: 1, ParentID: ptr("u0"), SponsorID: ptr("u0"), EnrolledAt: time.Unix(2, 0)},
		{ID: "u2", TreeID: "t", UserID: "u2", Depth: 1, ParentID: ptr("u0"), SponsorID: ptr("u0"), EnrolledAt: time.Unix(3, 0)},
	}
	for _, r := range rows {
		require.NoError(t, store.InsertNode(t.Context(), r))
	}

	err := networkengine.NewTreeLoader(store, failingMutator{failOn: "AddRoot"}).
		LoadTree(t.Context(), "t", "unilevel")

	var incomplete *networkengine.TreeLoadIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, networkengine.TreeLoadStageRoot, incomplete.Stage)
	require.Equal(t, 2, incomplete.Total, "Total is the non-root count the load set out to place")
	require.Equal(t, 0, incomplete.Attempted, "no placement was attempted")
	require.Equal(t, 0, incomplete.Confirmed, "no placement was acknowledged")
}

// The fallback renders only for a value this package did not construct. With
// Total set and Attempted zero it emits a placement index of zero. Pinned
// rather than changed.
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

func ptr(s string) *string { return &s }
