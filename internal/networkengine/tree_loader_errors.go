package networkengine

import (
	"fmt"
	"slices"
	"strings"
)

// TreeLoadRejectionKind names why a load was refused before the engine was
// called.
type TreeLoadRejectionKind string

const (
	// TreeLoadDataInvalid means the stored node set cannot be replayed. Other
	// trees are unaffected.
	TreeLoadDataInvalid TreeLoadRejectionKind = "data_invalid"
	// TreeLoadConfigInvalid means the load's arguments describe a structure
	// this loader cannot replay.
	TreeLoadConfigInvalid TreeLoadRejectionKind = "config_invalid"
	// TreeLoadStoreReadFailed means the node set could not be read. The cause
	// may be the store, a cancelled context, or a decode failure.
	TreeLoadStoreReadFailed TreeLoadRejectionKind = "store_read_failed"
)

// TreeLoadRejectedError reports a load that failed before any engine call, so
// the engine is unchanged and the load can be retried.
//
// Typed rather than a bare fmt.Errorf.
// A caller has to tell this apart from TreeLoadIncompleteError with errors.As,
// because the two need opposite handling: this one leaves the engine usable,
// the other may not.
type TreeLoadRejectedError struct {
	TreeID string
	// NodeIDs holds every user the message names, in the order the message
	// names them. Empty when the message names none.
	//
	// The order reproduces the message and means nothing else. It does not rank
	// blame, and where two users are named it does not say which came first. An
	// entry may name a user with no row in this tree, because the message names
	// references as well as rows, and the same user may appear twice.
	NodeIDs []string
	// Kind categorises why the load was refused.
	Kind TreeLoadRejectionKind
	// Err is the store's error for TreeLoadStoreReadFailed, nil otherwise.
	Err error
	msg string
}

// Error returns the stored message. A value built outside this package has
// none, so it renders from the exported fields instead.
func (e *TreeLoadRejectedError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return renderFallback("tree load rejected", e.TreeID, labelled("kind", string(e.Kind)), e.NodeIDs, e.Err, nil)
}
func (e *TreeLoadRejectedError) Unwrap() error { return e.Err }

// TreeLoadStage names how far a load reached before it failed.
//
// Every stage means the load reached that point and did not complete it. None
// of them means an operation did not take effect. Whether a call was sent at
// all is what Err distinguishes.
type TreeLoadStage string

const (
	// TreeLoadStageCreate means structure creation was attempted and did not
	// report success.
	TreeLoadStageCreate TreeLoadStage = "create"
	// TreeLoadStageRoot means the structure was created and root placement was
	// attempted and did not report success.
	TreeLoadStageRoot TreeLoadStage = "root"
	// TreeLoadStageNodes means the structure was created, the root was
	// acknowledged, and a later placement did not complete.
	TreeLoadStageNodes TreeLoadStage = "nodes"
)

// TreeLoadIncompleteError reports a load that failed after the engine was
// called. The structure may be partly built, and the worker has no operation
// to drop it (HEU-557), so a process restart is the only remedy.
//
// Confirmed and Total count non-root placements. Attempted is the one-based
// index the message carries, so within TreeLoadStageNodes Confirmed is
// Attempted minus one. All three are 0 at the other stages.
type TreeLoadIncompleteError struct {
	TreeID string
	// NodeIDs holds every user the message names, in the order the message
	// names them. The order reproduces the message and means nothing else. An
	// entry may name a user with no row in this tree, and the same user may
	// appear twice. Empty at TreeLoadStageCreate.
	NodeIDs []string
	// Stage names how far the load reached. It is for the operator reading the
	// log, not for selecting a recovery action.
	Stage TreeLoadStage
	// Confirmed is how many non-root placements the engine acknowledged.
	Confirmed int
	// Attempted is the one-based index of the placement that did not report
	// success. When it is non-zero that placement may still have taken effect,
	// unless Err is nil, which means the load stopped before the call was sent
	// and no request was made for it.
	Attempted int
	// Total is how many non-root placements the load set out to make.
	Total int
	// Err is the TreeMutator operation's error, or nil when a nil guard fired
	// instead. It may be a transport or context error rather than an
	// *EngineError.
	Err error
	msg string
}

func (e *TreeLoadIncompleteError) Error() string { return e.msg }
func (e *TreeLoadIncompleteError) Unwrap() error { return e.Err }

// newTreeLoadRejected builds a TreeLoadRejectedError.
func newTreeLoadRejected(kind TreeLoadRejectionKind, treeID string, err error, msg string, nodeIDs ...string) *TreeLoadRejectedError {
	return &TreeLoadRejectedError{
		TreeID:  treeID,
		NodeIDs: slices.Clone(nodeIDs),
		Kind:    kind,
		Err:     err,
		msg:     msg,
	}
}

// newTreeLoadIncomplete builds a TreeLoadIncompleteError.
// attempted is the one-based index the message carries and total is the
// non-root count. Both are 0 outside TreeLoadStageNodes. Confirmed is derived
// here rather than passed, so the count an operator reads cannot disagree with
// the index the message names.
func newTreeLoadIncomplete(stage TreeLoadStage, treeID string, err error, attempted, total int, msg string, nodeIDs ...string) *TreeLoadIncompleteError {
	return &TreeLoadIncompleteError{
		TreeID:    treeID,
		NodeIDs:   slices.Clone(nodeIDs),
		Stage:     stage,
		Confirmed: max(attempted-1, 0),
		Attempted: attempted,
		Total:     total,
		Err:       err,
		msg:       msg,
	}
}

// renderFallback builds a message for a value this package did not construct.
// Each part is omitted when its field is unset, so a zero value still renders
// the leading label rather than a run of separators.
func renderFallback(label, treeID, kindOrStage string, nodeIDs []string, err error, counts []string) string {
	parts := []string{label}
	if treeID != "" {
		parts = append(parts, fmt.Sprintf("tree %s", treeID))
	}
	if kindOrStage != "" {
		parts = append(parts, kindOrStage)
	}
	parts = append(parts, counts...)
	if len(nodeIDs) > 0 {
		parts = append(parts, fmt.Sprintf("nodes %s", strings.Join(nodeIDs, ", ")))
	}
	if err != nil {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, ": ")
}

// labelled prefixes a value with what it names, and returns empty for an unset
// value so the caller omits the segment. The stage value "nodes" would
// otherwise read as the node list, which uses the same word.
func labelled(label, value string) string {
	if value == "" {
		return ""
	}
	return label + " " + value
}
