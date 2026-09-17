package networkengine

import (
	"fmt"
	"slices"
	"strings"
)

// The leading segment of a message this package renders from a value it did
// not construct. A message the loader produced never begins with one.
const (
	rejectedFallbackLabel   = "tree load rejected"
	incompleteFallbackLabel = "tree load incomplete"
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
	return treeLoadFallback(rejectedFallbackLabel, e.TreeID, namedSegment("kind", string(e.Kind)), e.NodeIDs, e.Err, nil)
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

// Error returns the stored message. A value built outside this package has
// none, so it renders from the exported fields instead.
func (e *TreeLoadIncompleteError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	var seen []string
	named := e.Attempted != 0 || e.Total != 0
	if named {
		seen = append(seen, fmt.Sprintf("placement %d of %d", e.Attempted, e.Total))
	}
	if named || e.Confirmed != 0 {
		seen = append(seen, fmt.Sprintf("%d acknowledged", e.Confirmed))
	}
	var counts []string
	if len(seen) > 0 {
		counts = append(counts, strings.Join(seen, ", "))
	}
	return treeLoadFallback(incompleteFallbackLabel, e.TreeID, namedSegment("stage", string(e.Stage)), e.NodeIDs, e.Err, counts)
}
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

// treeLoadFallback builds a message for a value this package did not
// construct. Each segment is omitted when its field is unset, so a zero value
// still renders the leading label rather than a run of separators.
func treeLoadFallback(label, treeID, kindOrStage string, nodeIDs []string, err error, counts []string) string {
	parts := []string{label}
	if treeID != "" {
		parts = append(parts, fmt.Sprintf("tree %s", treeID))
	}
	if kindOrStage != "" {
		parts = append(parts, kindOrStage)
	}
	parts = append(parts, counts...)
	if list := namedNodes(nodeIDs); list != "" {
		parts = append(parts, list)
	}
	if text, ok := causeText(err); ok {
		parts = append(parts, text)
	}
	return strings.Join(parts, ": ")
}

// namedSegment prefixes a value with what it names, and returns empty for an
// unset value so the caller omits the segment.
func namedSegment(name, value string) string {
	if value == "" {
		return ""
	}
	return name + " " + value
}

// namedNodes renders the node list. An entry that would print as nothing is
// shown as an empty pair of quotes, so the count a reader sees matches the
// count the field holds.
func namedNodes(nodeIDs []string) string {
	if len(nodeIDs) == 0 {
		return ""
	}
	shown := make([]string, len(nodeIDs))
	for i, id := range nodeIDs {
		shown[i] = id
		if id == "" {
			shown[i] = `""`
		}
	}
	return fmt.Sprintf("nodes %s", strings.Join(shown, ", "))
}

// causeText renders a cause, reporting false when there is none.
//
// The cause on a value this path exists for came from a caller who did not use
// the constructors, so nothing constrains what its Error method does. Three
// shapes are handled here rather than allowed out of a method callers treat as
// total: a cause that panics, a cause that renders nothing, and a cause that is
// one of this package's own types carrying no message.
//
// That third one is named rather than called, because calling it would re-enter
// this path and recurse until the stack is exhausted. A stack overflow is a
// runtime fatal error that recover cannot contain.
//
// **The cycle guard reaches this package's own types and stops there.** A cause
// of any other type is called, so a caller whose own Error method renders the
// value holding it still recurses without bound. That cannot be guarded from
// inside this method: Error takes no parameter to carry a depth, the recursion
// leaves through a foreign method and returns, Go exposes no goroutine identity
// to key a re-entry set on, and a flag on the receiver would race two
// goroutines rendering one value.
//
// errors.Is is not a precedent for this input. It walks Unwrap and never calls
// Error, so on a cycle built this way it returns at once rather than recursing.
// It does hang on a cyclic Unwrap chain, which is a different input.
//
// TestLoadTree_ExternalCycleStillOverflows pins where the guard ends, by
// crashing a subprocess on purpose.
//
// A cause that cannot be rendered is still reported as present, because Unwrap
// returns it and a message that omitted it would disagree with errors.Is.
func causeText(err error) (text string, ok bool) {
	if err == nil {
		return "", false
	}
	defer func() {
		if r := recover(); r != nil {
			text, ok = "cause could not be rendered", true
		}
	}()
	switch cause := err.(type) {
	case *TreeLoadRejectedError:
		if cause.msg == "" {
			return rejectedFallbackLabel, true
		}
	case *TreeLoadIncompleteError:
		if cause.msg == "" {
			return incompleteFallbackLabel, true
		}
	}
	text = err.Error()
	if strings.TrimSpace(text) == "" {
		return "cause rendered no text", true
	}
	return text, true
}
