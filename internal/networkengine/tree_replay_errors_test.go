package networkengine

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTreeReplayError_StatesWhatWasObserved(t *testing.T) {
	cases := map[string]struct {
		err  *TreeReplayError
		want string
	}{
		"an event that failed": {
			&TreeReplayError{TreeID: "t", Stage: TreeReplayStageReplay, ReachedVersion: 4, ProjectedVersion: 9,
				FailedVersion: 5, FailedType: EventTypeNodePlaced, Err: errors.New("boom")},
			"build tree t at stage replay: reached version 4 of projected version 9; event at version 5 of type tree.node_placed failed: boom",
		},
		"an invariant": {
			&TreeReplayError{TreeID: "t", Stage: TreeReplayStageReplay, ReachedVersion: 2, ProjectedVersion: 9,
				Expected: "version 3", Observed: "version 4"},
			"build tree t at stage replay: reached version 2 of projected version 9; expected version 3; observed version 4",
		},
		"an observation alone": {
			&TreeReplayError{TreeID: "t", Stage: TreeReplayStageDrop, ProjectedVersion: 9,
				Observed: "the restore succeeded, and drop_structure reported no structure named t"},
			"build tree t at stage drop: reached version 0 of projected version 9; observed the restore succeeded, and drop_structure reported no structure named t",
		},
		"a mismatch": {
			&TreeReplayError{TreeID: "t", Stage: TreeReplayStageMembership, ReachedVersion: 9, ProjectedVersion: 9,
				Mismatch: "the engine holds 1 nodes"},
			"build tree t at stage membership: reached version 9 of projected version 9; the engine holds 1 nodes",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

func TestTreeReplayError_UnwrapsItsCause(t *testing.T) {
	cause := &EngineError{Code: "SPONSOR_NOT_FOUND", Message: "x"}
	var engineErr *EngineError

	assert.True(t, errors.As(&TreeReplayError{Err: cause}, &engineErr))
	assert.Equal(t, cause, engineErr)
}
