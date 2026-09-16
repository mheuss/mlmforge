package networkengine

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
// Typed rather than a bare fmt.Errorf, matching ProtocolVersionMismatchError.
// A caller has to tell this apart from TreeLoadIncompleteError with errors.As,
// because the two need opposite handling: this one leaves the engine usable,
// the other may not.
type TreeLoadRejectedError struct {
	TreeID string
	// NodeIDs holds every user the message names, in the order the message
	// names them. Empty when the message names none.
	NodeIDs []string
	Kind    TreeLoadRejectionKind
	// Err is the store's error for TreeLoadStoreReadFailed, nil otherwise.
	Err error
	msg string
}

func (e *TreeLoadRejectedError) Error() string { return e.msg }
func (e *TreeLoadRejectedError) Unwrap() error { return e.Err }

// TreeLoadStage names how far a load reached before it failed.
//
// Every stage means an operation was attempted and did not report success.
// None of them means the operation did not take effect. See section 4.
type TreeLoadStage string

const (
	// TreeLoadStageCreate means structure creation was attempted and did not
	// report success.
	TreeLoadStageCreate TreeLoadStage = "create"
	// TreeLoadStageRoot means the structure was created and root placement was
	// attempted and did not report success.
	TreeLoadStageRoot TreeLoadStage = "root"
	// TreeLoadStageNodes means the structure was created, the root was
	// acknowledged, and a later placement did not report success.
	TreeLoadStageNodes TreeLoadStage = "nodes"
)

// TreeLoadIncompleteError reports a load that failed after the engine was
// called. The structure may be partly built, and the worker has no operation
// to drop it (HEU-557), so a process restart is the only remedy.
//
// Confirmed and Total count non-root placements. Attempted is the one-based
// index the message carries, so Confirmed is always Attempted minus one. Both
// are 0 outside TreeLoadStageNodes.
type TreeLoadIncompleteError struct {
	TreeID string
	// NodeIDs holds every user the message names, in the order the message
	// names them. Empty at TreeLoadStageCreate.
	NodeIDs []string
	Stage   TreeLoadStage
	// Confirmed is how many non-root placements the engine acknowledged.
	Confirmed int
	// Attempted is the one-based index of the placement that did not report
	// success. That placement may still have taken effect.
	Attempted int
	Total     int
	// Err is the TreeMutator operation's error, or nil when a nil guard fired
	// instead. It may be a transport or context error rather than an
	// *EngineError.
	Err error
	msg string
}

func (e *TreeLoadIncompleteError) Error() string { return e.msg }
func (e *TreeLoadIncompleteError) Unwrap() error { return e.Err }

// newTreeLoadRejected builds the error every pre-engine exit returns. The
// message is passed in rather than derived, because the wording of each exit
// is a settled decision and this change does not reopen it.
func newTreeLoadRejected(kind TreeLoadRejectionKind, treeID string, err error, msg string, nodeIDs ...string) *TreeLoadRejectedError {
	return &TreeLoadRejectedError{
		TreeID:  treeID,
		NodeIDs: nodeIDs,
		Kind:    kind,
		Err:     err,
		msg:     msg,
	}
}

// newTreeLoadIncomplete builds the error every post-engine exit returns.
// confirmed is how many non-root placements the engine acknowledged, attempted
// is the one-based index the message carries, and total is the non-root count.
// All three are 0 outside TreeLoadStageNodes.
func newTreeLoadIncomplete(stage TreeLoadStage, treeID string, err error, confirmed, attempted, total int, msg string, nodeIDs ...string) *TreeLoadIncompleteError {
	return &TreeLoadIncompleteError{
		TreeID:    treeID,
		NodeIDs:   nodeIDs,
		Stage:     stage,
		Confirmed: confirmed,
		Attempted: attempted,
		Total:     total,
		Err:       err,
		msg:       msg,
	}
}
