package networkengine_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nilRenderer is never called. It exists so a test can put a nil pointer of a
// concrete error type into a non-nil error interface.
type nilRenderer struct{}

func (n *nilRenderer) Error() string { return "this method must not be reached" }

func TestTreeLoadRejectedError_ExternalLiteralRendersEveryField(t *testing.T) {
	err := &networkengine.TreeLoadRejectedError{
		TreeID:  "t1",
		Kind:    networkengine.TreeLoadStoreReadFailed,
		NodeIDs: []string{"u1", "u2"},
		Err:     errors.New("connection refused"),
	}

	assert.Equal(t,
		"tree load rejected: tree t1: kind store_read_failed: nodes u1, u2: connection refused",
		err.Error())
}

func TestTreeLoadIncompleteError_ExternalLiteralRendersEveryField(t *testing.T) {
	err := &networkengine.TreeLoadIncompleteError{
		TreeID:    "t2",
		Stage:     networkengine.TreeLoadStageNodes,
		Confirmed: 2,
		Attempted: 3,
		Total:     4,
		NodeIDs:   []string{"u9"},
	}

	assert.Equal(t,
		"tree load incomplete: tree t2: stage nodes: placement 3 of 4, 2 acknowledged: nodes u9",
		err.Error())
}

// The stage value and the node list both print the word "nodes". This asserts
// the stage keeps its own name so one message cannot use the word twice
// meaning two things.
func TestTreeLoadIncompleteError_StageIsNamedApartFromTheNodeList(t *testing.T) {
	err := &networkengine.TreeLoadIncompleteError{
		TreeID:  "t2",
		Stage:   networkengine.TreeLoadStageNodes,
		NodeIDs: []string{"u9"},
	}

	assert.Equal(t, "tree load incomplete: tree t2: stage nodes: nodes u9", err.Error())
}

func TestTreeLoadErrors_UnsetFieldsAreOmitted(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "rejected, nothing set",
			err:  &networkengine.TreeLoadRejectedError{},
			want: "tree load rejected",
		},
		{
			name: "incomplete, nothing set",
			err:  &networkengine.TreeLoadIncompleteError{},
			want: "tree load incomplete",
		},
		{
			name: "rejected, tree only",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t3"},
			want: "tree load rejected: tree t3",
		},
		{
			name: "rejected, kind only",
			err:  &networkengine.TreeLoadRejectedError{Kind: networkengine.TreeLoadConfigInvalid},
			want: "tree load rejected: kind config_invalid",
		},
		{
			name: "incomplete, stage only",
			err:  &networkengine.TreeLoadIncompleteError{TreeID: "t6", Stage: networkengine.TreeLoadStageRoot},
			want: "tree load incomplete: tree t6: stage root",
		},
		{
			name: "incomplete, acknowledged count with no placement index",
			err:  &networkengine.TreeLoadIncompleteError{TreeID: "t2", Confirmed: 7},
			want: "tree load incomplete: tree t2: 7 acknowledged",
		},
		{
			name: "rejected, node ids that print as nothing",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t1", NodeIDs: []string{"", ""}},
			want: "tree load rejected: tree t1",
		},
		{
			name: "rejected, one empty node id among real ones",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t1", NodeIDs: []string{"", "u2"}},
			want: "tree load rejected: tree t1: nodes u2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

// A nil pointer inside a non-nil error interface passes an != nil check. The
// renderer must not call through it.
func TestTreeLoadErrors_TypedNilCauseDoesNotPanic(t *testing.T) {
	var typed *nilRenderer
	// cause is not nil as the language sees it, so an != nil guard lets it
	// through. staticcheck says the comparison is always true, which is the
	// property being relied on, so asserting it would be asserting nothing.
	var cause error = typed

	rejected := &networkengine.TreeLoadRejectedError{TreeID: "t4", Kind: networkengine.TreeLoadDataInvalid, Err: cause}
	incomplete := &networkengine.TreeLoadIncompleteError{TreeID: "t5", Stage: networkengine.TreeLoadStageCreate, Err: cause}

	assert.Equal(t, "tree load rejected: tree t4: kind data_invalid", rejected.Error())
	assert.Equal(t, "tree load incomplete: tree t5: stage create", incomplete.Error())
}

func TestTreeLoadErrors_WrappedExternalLiteralCarriesText(t *testing.T) {
	rejected := &networkengine.TreeLoadRejectedError{TreeID: "t3", Kind: networkengine.TreeLoadConfigInvalid}

	wrapped := fmt.Errorf("load failed: %w", rejected)

	assert.Equal(t, "load failed: tree load rejected: tree t3: kind config_invalid", wrapped.Error())
}

func TestTreeLoadErrors_ExternalLiteralStillMatches(t *testing.T) {
	cause := errors.New("connection refused")
	rejected := &networkengine.TreeLoadRejectedError{TreeID: "t4", Kind: networkengine.TreeLoadStoreReadFailed, Err: cause}

	wrapped := fmt.Errorf("load failed: %w", rejected)

	assert.ErrorIs(t, wrapped, cause)

	var got *networkengine.TreeLoadRejectedError
	require.ErrorAs(t, wrapped, &got)
	assert.Equal(t, networkengine.TreeLoadStoreReadFailed, got.Kind)
}

func TestTreeLoadIncompleteError_ExternalLiteralIsMatchableByType(t *testing.T) {
	incomplete := &networkengine.TreeLoadIncompleteError{TreeID: "t2", Stage: networkengine.TreeLoadStageNodes}

	wrapped := fmt.Errorf("restart required: %w", incomplete)

	var got *networkengine.TreeLoadIncompleteError
	require.ErrorAs(t, wrapped, &got)
	assert.Equal(t, networkengine.TreeLoadStageNodes, got.Stage)

	var wrong *networkengine.TreeLoadRejectedError
	assert.False(t, errors.As(wrapped, &wrong))
}
