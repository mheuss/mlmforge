package networkengine_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// derefRenderer reads a field, so calling it on a nil pointer panics. A
// receiver that ignores its pointer would let the test below pass without ever
// meeting the failure it is named for.
type derefRenderer struct{ text string }

func (d *derefRenderer) Error() string { return d.text }

// panickingCause stands for any cause whose own Error method fails.
type panickingCause struct{}

func (panickingCause) Error() string { panic("cause blew up") }

// textCause renders whatever it is given, including nothing visible.
type textCause struct{ text string }

func (c textCause) Error() string { return c.text }

// sliceRenderer is nil-able but perfectly callable when nil. An earlier guard
// treated it the same as a nil pointer and dropped its text.
type sliceRenderer []string

func (s sliceRenderer) Error() string { return "slice cause text" }

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
		Err:       errors.New("worker said no"),
	}

	assert.Equal(t,
		"tree load incomplete: tree t2: stage nodes: placement 3 of 4, 2 acknowledged: nodes u9: worker said no",
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
			name: "incomplete, first placement with nothing acknowledged",
			err: &networkengine.TreeLoadIncompleteError{
				TreeID: "t2", Stage: networkengine.TreeLoadStageNodes, Attempted: 1, Total: 4,
			},
			want: "tree load incomplete: tree t2: stage nodes: placement 1 of 4, 0 acknowledged",
		},
		{
			name: "incomplete, acknowledged count with no placement index",
			err:  &networkengine.TreeLoadIncompleteError{TreeID: "t2", Confirmed: 7},
			want: "tree load incomplete: tree t2: 7 acknowledged",
		},
		{
			name: "rejected, node ids that print as nothing",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t1", NodeIDs: []string{"", ""}},
			want: `tree load rejected: tree t1: nodes "", ""`,
		},
		{
			name: "rejected, one empty node id among real ones",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t1", NodeIDs: []string{"", "u2"}},
			want: `tree load rejected: tree t1: nodes "", u2`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

// A cause whose Error method fails must not take the render with it. Both
// shapes below crashed an earlier version of this renderer.
func TestTreeLoadErrors_AFailingCauseIsContained(t *testing.T) {
	var nilPtr *derefRenderer

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "a nil pointer cause, whose Error dereferences it",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t4", Kind: networkengine.TreeLoadDataInvalid, Err: nilPtr},
			want: "tree load rejected: tree t4: kind data_invalid: cause could not be rendered",
		},
		{
			name: "a cause that renders the empty string",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t6", Kind: networkengine.TreeLoadDataInvalid, Err: textCause{""}},
			want: "tree load rejected: tree t6: kind data_invalid: cause rendered no text",
		},
		{
			name: "a cause that renders only a space",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t6", Kind: networkengine.TreeLoadDataInvalid, Err: textCause{" "}},
			want: "tree load rejected: tree t6: kind data_invalid: cause rendered no text",
		},
		{
			// errors.Join separates with a newline, so two causes that render
			// nothing reach this naturally rather than adversarially.
			name: "two joined causes that render nothing",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t6", Err: errors.Join(textCause{""}, textCause{""})},
			want: "tree load rejected: tree t6: cause rendered no text",
		},
		{
			name: "a cause whose text has its own surrounding space",
			err:  &networkengine.TreeLoadRejectedError{TreeID: "t6", Err: textCause{"  boom  "}},
			want: "tree load rejected: tree t6:   boom  ",
		},
		{
			name: "a cause whose Error panics outright",
			err:  &networkengine.TreeLoadIncompleteError{TreeID: "t5", Stage: networkengine.TreeLoadStageCreate, Err: panickingCause{}},
			want: "tree load incomplete: tree t5: stage create: cause could not be rendered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() { _ = tt.err.Error() })
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

// A cause that is one of these types with no message would re-enter the
// renderer. That recursion ends in a stack overflow, which is a runtime fatal
// error rather than a panic, so no caller can recover from it.
func TestTreeLoadErrors_ACyclicCauseDoesNotRecurse(t *testing.T) {
	self := &networkengine.TreeLoadRejectedError{TreeID: "t1", Kind: networkengine.TreeLoadDataInvalid}
	self.Err = self

	first := &networkengine.TreeLoadRejectedError{TreeID: "a", Kind: networkengine.TreeLoadDataInvalid}
	second := &networkengine.TreeLoadIncompleteError{TreeID: "b", Stage: networkengine.TreeLoadStageNodes}
	first.Err = second
	second.Err = first

	assert.Equal(t, "tree load rejected: tree t1: kind data_invalid: tree load rejected", self.Error())
	assert.Equal(t, "tree load rejected: tree a: kind data_invalid: tree load incomplete", first.Error())
}

// A cause is named whenever one is present, because Unwrap returns it either
// way. Dropping it would make the message disagree with errors.Is.
func TestTreeLoadErrors_ANilValuedCauseStillRenders(t *testing.T) {
	var nilSlice sliceRenderer

	err := &networkengine.TreeLoadRejectedError{TreeID: "t", Err: nilSlice}

	assert.Equal(t, "tree load rejected: tree t: slice cause text", err.Error())
	// errors.As, not errors.Is: a slice type is not comparable, so errors.Is
	// skips the equality path and can never match this target. assert.NotNil
	// reflects and reads a nil slice as nil, so it cannot say this either.
	var got sliceRenderer
	assert.True(t, errors.As(err, &got))
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
