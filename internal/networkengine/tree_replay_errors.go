package networkengine

import (
	"fmt"
	"strings"
)

// TreeReplayStage names the step of building a tree from its stream that failed.
type TreeReplayStage string

const (
	TreeReplayStageRestore    TreeReplayStage = "restore"
	TreeReplayStageReplay     TreeReplayStage = "replay"
	TreeReplayStageCreate     TreeReplayStage = "create"
	TreeReplayStageDrop       TreeReplayStage = "drop"
	TreeReplayStageMembership TreeReplayStage = "membership"
)

// TreeReplayError reports a load that could not build a tree in the engine from
// its event stream.
type TreeReplayError struct {
	TreeID           string
	Stage            TreeReplayStage
	ReachedVersion   int64
	ProjectedVersion int64
	FailedVersion    int64
	FailedType       string
	Expected         string
	Observed         string
	Mismatch         string
	Err              error
}

func (e *TreeReplayError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "build tree %s at stage %s: reached version %d of projected version %d",
		e.TreeID, e.Stage, e.ReachedVersion, e.ProjectedVersion)
	if e.FailedVersion > 0 {
		fmt.Fprintf(&b, "; event at version %d of type %s failed", e.FailedVersion, e.FailedType)
	}
	if e.Expected != "" {
		fmt.Fprintf(&b, "; expected %s", e.Expected)
	}
	if e.Observed != "" {
		fmt.Fprintf(&b, "; observed %s", e.Observed)
	}
	if e.Mismatch != "" {
		fmt.Fprintf(&b, "; %s", e.Mismatch)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %s", e.Err)
	}
	return b.String()
}

func (e *TreeReplayError) Unwrap() error { return e.Err }
