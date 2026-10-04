package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
)

// defaultTreeLockWait bounds how long a TreeWriter waits for a tree's lock.
const defaultTreeLockWait = 30 * time.Second

// AddRootRequest asks for a tree's root. The type and matrix parameters shape
// the tree when its stream is empty.
type AddRootRequest struct {
	TreeID          string
	UserID          string
	SponsorID       string
	TreeType        string
	MatrixWidth     *int
	MatrixSpillover *string
	EnrolledAt      time.Time
}

// PlaceRequest asks for a node at an explicit parent and position. Position is
// nil for a unilevel tree.
type PlaceRequest struct {
	TreeID     string
	UserID     string
	ParentID   string
	SponsorID  string
	Position   *int
	EnrolledAt time.Time
}

// RemoveRequest asks for a leaf's removal.
type RemoveRequest struct {
	TreeID    string
	UserID    string
	RemovedAt time.Time
}

// CaughtUpEvent names a redelivered last event.
type CaughtUpEvent struct {
	EventID string
	Version int64
	Type    string
}

// WriteResult describes one write. EventID and Version are set once the
// append is confirmed.
type WriteResult struct {
	Stream        string
	EventID       string
	Version       int64
	CaughtUp      *CaughtUpEvent         // the stream's last event, redelivered and handled without error
	ProjectionErr error                  // any failure after the append was confirmed
	Observed      *ProjectionObservation // the projected version, re-read after a projection error
	ReleaseErr    error                  // the unlock failed
}

// ProjectionObservation is the tree's projected version, read after a
// projection error.
type ProjectionObservation struct {
	Version int64
	Found   bool
	Err     error
}

// LoadRequest names a tree to load and the shape the caller expects. A nil
// matrix field was not given.
type LoadRequest struct {
	TreeID          string
	TreeType        string
	MatrixWidth     *int
	MatrixSpillover *string
}

// LoadResult describes one load.
type LoadResult struct {
	CaughtUp       *CaughtUpEvent         // the stream's last event, redelivered and handled without error
	ProjectedAfter *ProjectionObservation // the projected version read after a redelivery
	Nodes          int                    // the active rows the load leaves, set only when Load returns no error
	ReleaseErr     error                  // the unlock failed
}

// TreeWriter appends tree events and projects them, one tree at a time.
type TreeWriter struct {
	events   platform.EventStore
	store    TreeStore
	engine   TreeEngineChecker
	locker   TreeLocker
	loader   *TreeLoader
	consumer *TreeEventConsumer
	lockWait time.Duration
}

// TreeWriterOption configures a TreeWriter.
type TreeWriterOption func(*TreeWriter)

// WithLockWait sets how long each write waits for its tree's lock. A wait of
// zero or less is ignored.
func WithLockWait(d time.Duration) TreeWriterOption {
	return func(w *TreeWriter) {
		if d > 0 {
			w.lockWait = d
		}
	}
}

// NewTreeWriter creates a writer over one event store, tree store, engine and
// locker.
func NewTreeWriter(events platform.EventStore, store TreeStore, engine TreeEngineChecker,
	locker TreeLocker, opts ...TreeWriterOption) *TreeWriter {
	w := &TreeWriter{
		events:   events,
		store:    store,
		engine:   engine,
		locker:   locker,
		loader:   NewTreeLoader(store, engine),
		consumer: NewTreeEventConsumer(store, engine),
		lockWait: defaultTreeLockWait,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// AddRoot appends a tree.root_added event and projects it.
func (w *TreeWriter) AddRoot(ctx context.Context, r AddRootRequest) (WriteResult, error) {
	treeID, err := canonicalID("tree_id", r.TreeID)
	if err != nil {
		return WriteResult{}, err
	}
	userID, err := canonicalID("user_id", r.UserID)
	if err != nil {
		return WriteResult{}, err
	}
	sponsorID, err := canonicalID("sponsor_id", r.SponsorID)
	if err != nil {
		return WriteResult{}, err
	}
	tree := treeID.String()
	stream := TreeStreamName(tree)

	shape, found, err := w.readShape(ctx, stream)
	if err != nil {
		return WriteResult{}, err
	}
	var requested treeShape
	if found {
		if err := shapeConflict("add root to", tree, stream, shape, r.TreeType, r.MatrixWidth, r.MatrixSpillover); err != nil {
			return WriteResult{}, err
		}
	} else {
		if requested, err = shapeFromRequest("add root to", tree, r.TreeType, r.MatrixWidth, r.MatrixSpillover); err != nil {
			return WriteResult{}, err
		}
		shape = requested
	}

	spec := writeSpec{
		treeID: treeID,
		shape:  shape,
		build: func(shape treeShape) (platform.NewEvent, Mutation, error) {
			payload := RootAddedPayload{
				TreeID:     tree,
				UserID:     userID.String(),
				SponsorID:  sponsorID.String(),
				EnrolledAt: r.EnrolledAt,
				TreeType:   shape.treeType,
			}
			if shape.treeType == treeTypeMatrix {
				width, spillover := shape.width, shape.spillover
				payload.MatrixWidth, payload.MatrixSpillover = &width, &spillover
			}
			event, err := newTreeEvent(EventTypeRootAdded, payload)
			return event, CheckAddRoot(userID.String(), r.EnrolledAt.Unix()), err
		},
	}
	if !found {
		spec.underLock = func(ctx context.Context) (treeShape, error) {
			recorded, found, err := w.readShape(ctx, stream)
			if err != nil {
				return treeShape{}, err
			}
			if !found {
				return requested, nil
			}
			if err := shapeConflict("add root to", tree, stream, recorded, r.TreeType, r.MatrixWidth, r.MatrixSpillover); err != nil {
				return treeShape{}, err
			}
			return recorded, nil
		}
	}
	return w.write(ctx, spec)
}

// Place appends a tree.node_placed event at the parent and position the
// request names, and projects it.
func (w *TreeWriter) Place(ctx context.Context, r PlaceRequest) (WriteResult, error) {
	treeID, err := canonicalID("tree_id", r.TreeID)
	if err != nil {
		return WriteResult{}, err
	}
	userID, err := canonicalID("user_id", r.UserID)
	if err != nil {
		return WriteResult{}, err
	}
	parentID, err := canonicalID("parent_id", r.ParentID)
	if err != nil {
		return WriteResult{}, err
	}
	sponsorID, err := canonicalID("sponsor_id", r.SponsorID)
	if err != nil {
		return WriteResult{}, err
	}
	tree := treeID.String()
	stream := TreeStreamName(tree)

	shape, found, err := w.readShape(ctx, stream)
	if err != nil {
		return WriteResult{}, err
	}
	if !found {
		return WriteResult{}, fmt.Errorf("place %s in tree %s: stream %s has no events", userID, tree, stream)
	}
	payload := NodePlacedPayload{
		TreeID:     tree,
		UserID:     userID.String(),
		ParentID:   parentID.String(),
		SponsorID:  sponsorID.String(),
		Position:   r.Position,
		TreeType:   shape.treeType,
		EnrolledAt: r.EnrolledAt,
	}
	if err := checkNodePlacedShape(payload); err != nil {
		return WriteResult{}, err
	}

	return w.write(ctx, writeSpec{
		treeID: treeID,
		shape:  shape,
		build: func(treeShape) (platform.NewEvent, Mutation, error) {
			event, err := newTreeEvent(EventTypeNodePlaced, payload)
			return event, placementCheck(payload), err
		},
	})
}

// placementCheck builds the check for placing p.
func placementCheck(p NodePlacedPayload) Mutation {
	switch p.TreeType {
	case treeTypeMatrix:
		return CheckAddNodeAt(p.UserID, p.ParentID, p.SponsorID, *p.Position, p.EnrolledAt.Unix())
	case treeTypeBinary:
		return CheckAddNode(p.UserID, p.ParentID, p.SponsorID, p.EnrolledAt.Unix(), WithPosition(*p.Position))
	default:
		return CheckAddNode(p.UserID, p.ParentID, p.SponsorID, p.EnrolledAt.Unix())
	}
}

// Remove appends a tree.node_removed event and projects it.
func (w *TreeWriter) Remove(ctx context.Context, r RemoveRequest) (WriteResult, error) {
	treeID, err := canonicalID("tree_id", r.TreeID)
	if err != nil {
		return WriteResult{}, err
	}
	userID, err := canonicalID("user_id", r.UserID)
	if err != nil {
		return WriteResult{}, err
	}
	tree := treeID.String()
	stream := TreeStreamName(tree)

	shape, found, err := w.readShape(ctx, stream)
	if err != nil {
		return WriteResult{}, err
	}
	if !found {
		return WriteResult{}, fmt.Errorf("remove %s from tree %s: stream %s has no events", userID, tree, stream)
	}
	if shape.treeType == treeTypeMatrix {
		return WriteResult{}, refuseMatrixRemoval(userID.String(), tree, stream)
	}
	payload := NodeRemovedPayload{TreeID: tree, UserID: userID.String(), RemovedAt: r.RemovedAt}

	return w.write(ctx, writeSpec{
		treeID: treeID,
		shape:  shape,
		build: func(treeShape) (platform.NewEvent, Mutation, error) {
			event, err := newTreeEvent(EventTypeNodeRemoved, payload)
			return event, CheckRemoveNode(payload.UserID), err
		},
	})
}

// refuseMatrixRemoval refuses a removal from a matrix tree.
func refuseMatrixRemoval(user, tree, stream string) error {
	return fmt.Errorf("remove %s from tree %s: stream %s records tree type matrix at version 1, "+
		"and this writer does not remove from matrix trees", user, tree, stream)
}

// shapeConflict refuses a requested type or matrix parameter that differs from
// the shape version 1 records. Absent matrix parameters match.
func shapeConflict(action, tree, stream string, recorded treeShape, treeType string, width *int, spillover *string) error {
	prefix := fmt.Sprintf("%s tree %s: stream %s records", action, tree, stream)
	if recorded.treeType != treeType {
		return fmt.Errorf("%s tree type %s at version 1, and the request names %s", prefix, recorded.treeType, treeType)
	}
	if recorded.treeType != treeTypeMatrix {
		if width != nil {
			return fmt.Errorf("%s no matrix width at version 1, and the request names %d", prefix, *width)
		}
		if spillover != nil {
			return fmt.Errorf("%s no matrix spillover at version 1, and the request names %q", prefix, *spillover)
		}
		return nil
	}
	if width != nil && *width != recorded.width {
		return fmt.Errorf("%s matrix width %d at version 1, and the request names %d", prefix, recorded.width, *width)
	}
	if spillover != nil && *spillover != recorded.spillover {
		return fmt.Errorf("%s matrix spillover %q at version 1, and the request names %q",
			prefix, recorded.spillover, *spillover)
	}
	return nil
}

// readShape reads version 1 of a stream. found is false for an empty stream.
func (w *TreeWriter) readShape(ctx context.Context, stream string) (shape treeShape, found bool, err error) {
	first, err := w.events.ReadStream(ctx, stream, 1, 1)
	if err != nil {
		return treeShape{}, false, &storeReadError{what: fmt.Sprintf("read version 1 of stream %s", stream), err: err}
	}
	if len(first) == 0 {
		return treeShape{}, false, nil
	}
	shape, err = readTreeShape(stream, first[0])
	if err != nil {
		return treeShape{}, false, err
	}
	return shape, true, nil
}

// writeSpec is one operation, ready for the locked part of the write.
type writeSpec struct {
	treeID uuid.UUID
	shape  treeShape
	// build returns the event to append and the mutation to check first, for
	// the shape the write goes ahead with.
	build func(shape treeShape) (platform.NewEvent, Mutation, error)
	// underLock, when set, returns the shape the write goes ahead with in place
	// of shape.
	underLock func(ctx context.Context) (treeShape, error)
}

// write runs the locked part of a write.
func (w *TreeWriter) write(ctx context.Context, spec writeSpec) (result WriteResult, err error) {
	tree := spec.treeID.String()
	stream := TreeStreamName(tree)
	result.Stream = stream

	unlock, err := w.lock(ctx, spec.treeID)
	if err != nil {
		return result, err
	}
	defer func() {
		result.ReleaseErr = unlock()
	}()

	shape := spec.shape
	if spec.underLock != nil {
		if shape, err = spec.underLock(ctx); err != nil {
			return result, err
		}
	}
	event, check, err := spec.build(shape)
	if err != nil {
		return result, err
	}
	loaded, found, last, _, err := w.prepare(ctx, tree, shape)
	if err != nil {
		return result, err
	}
	var expected int64
	if last != nil {
		expected = last.Version
		if result.CaughtUp, err = w.catchUp(ctx, tree, stream, *last, loaded, found); err != nil {
			return result, err
		}
	}
	if err := w.engine.CheckMutation(ctx, tree, check); err != nil {
		return result, fmt.Errorf("check_mutation for %s in tree %s returned: %w; nothing was appended",
			check.op, tree, err)
	}
	version, err := w.append(ctx, stream, expected, event)
	if err != nil {
		return result, err
	}
	result.EventID, result.Version = event.ID, version
	result.ProjectionErr = w.project(ctx, stream, event.ID, version)
	if result.ProjectionErr != nil {
		result.Observed = w.observe(ctx, tree)
	}
	return result, nil
}

// Load loads the tree, and brings its store level with its stream when the
// store is one event behind.
func (w *TreeWriter) Load(ctx context.Context, r LoadRequest) (result LoadResult, err error) {
	treeID, err := canonicalID("tree_id", r.TreeID)
	if err != nil {
		return result, err
	}
	tree := treeID.String()
	stream := TreeStreamName(tree)

	shape, found, err := w.loadShape(ctx, tree, stream, r)
	if err != nil {
		return result, err
	}
	unlock, err := w.lock(ctx, treeID)
	if err != nil {
		return result, err
	}
	defer func() {
		result.ReleaseErr = unlock()
	}()
	// Version 1's absence is not final until the lock is held.
	if !found {
		if shape, _, err = w.loadShape(ctx, tree, stream, r); err != nil {
			return result, err
		}
	}

	loaded, found, last, nodes, err := w.prepare(ctx, tree, shape)
	if err != nil {
		return result, rejectEarlyRead(tree, err)
	}
	if last == nil || last.Version != loaded+1 {
		result.Nodes = nodes
		return result, nil
	}
	if result.CaughtUp, err = w.catchUp(ctx, tree, stream, *last, loaded, found); err != nil {
		return result, err
	}
	after, afterFound, err := w.store.ProjectedVersion(ctx, tree)
	if err != nil {
		return result, fmt.Errorf("read the projected version of tree %s after redelivering version %d: %w",
			tree, last.Version, err)
	}
	result.ProjectedAfter = &ProjectionObservation{Version: after, Found: afterFound}
	rows, err := w.store.GetByTree(ctx, tree)
	if err != nil {
		return result, fmt.Errorf("read the active rows of tree %s after redelivering version %d: %w",
			tree, last.Version, err)
	}
	result.Nodes = len(rows)
	return result, nil
}

// loadShape returns the shape a load goes ahead with: the one version 1
// records, or the requested one for an empty stream.
func (w *TreeWriter) loadShape(ctx context.Context, tree, stream string, r LoadRequest) (treeShape, bool, error) {
	recorded, found, err := w.readShape(ctx, stream)
	if err != nil {
		return treeShape{}, false, rejectEarlyRead(tree, err)
	}
	if found {
		return recorded, true, shapeConflict("load", tree, stream, recorded, r.TreeType, r.MatrixWidth, r.MatrixSpillover)
	}
	shape, err := shapeFromRequest("load", tree, r.TreeType, r.MatrixWidth, r.MatrixSpillover)
	return shape, false, err
}

// rejectEarlyRead types a failed read made before any engine call as a load
// rejection, and returns any other error unchanged.
func rejectEarlyRead(tree string, err error) error {
	var read *storeReadError
	if !errors.As(err, &read) {
		return err
	}
	return newTreeLoadRejected(TreeLoadStoreReadFailed, tree, read.err, err.Error())
}

// storeReadError is a failed store read, carrying what was being read.
type storeReadError struct {
	what string
	err  error
}

func (e *storeReadError) Error() string { return e.what + ": " + e.err.Error() }
func (e *storeReadError) Unwrap() error { return e.err }

// prepare loads the tree and refuses a stream the store cannot be brought
// level with by one redelivery. nodes is the loader's row count.
func (w *TreeWriter) prepare(ctx context.Context, tree string, shape treeShape) (
	loaded int64, found bool, last *platform.Event, nodes int, err error) {
	// Read before the rows. A projection landing between the two reads then
	// leaves the rows newer than loaded.
	loaded, found, err = w.store.ProjectedVersion(ctx, tree)
	if err != nil {
		return 0, false, nil, 0, &storeReadError{
			what: fmt.Sprintf("read the projected version of tree %s; nothing was appended", tree), err: err,
		}
	}
	stream := TreeStreamName(tree)
	nodes, loadErr := w.load(ctx, tree, shape)
	if loadErr != nil {
		// Read only to name a pending rejection. A failed read leaves the load
		// error as the report.
		if last, readErr := w.events.ReadLastEvent(ctx, stream); readErr == nil {
			if pending := pendingRejection(tree, last, loaded, found, loadErr); pending != nil {
				return 0, false, nil, 0, pending
			}
		}
		return 0, false, nil, 0, loadErr
	}
	last, err = w.events.ReadLastEvent(ctx, stream)
	if err != nil {
		return 0, false, nil, 0, fmt.Errorf("read the last event of stream %s; nothing was appended: %w", stream, err)
	}
	if pending := pendingRejection(tree, last, loaded, found, nil); pending != nil {
		return 0, false, nil, 0, pending
	}
	if last != nil && last.Type == EventTypeEventRejected && last.Version == loaded {
		if err := w.checkRejectionTarget(ctx, stream, *last); err != nil {
			return 0, false, nil, 0, fmt.Errorf("%w; nothing was appended", err)
		}
	}
	var lastVersion int64
	if last != nil {
		lastVersion = last.Version
	}
	if err := checkLoadedVersion(tree, loaded, found, lastVersion); err != nil {
		return 0, false, nil, 0, err
	}
	return loaded, found, last, nodes, nil
}

// observe reads the tree's projected version with a context the caller's
// cancellation does not reach.
func (w *TreeWriter) observe(ctx context.Context, tree string) *ProjectionObservation {
	readCtx, cancel := detachedRead(ctx)
	defer cancel()
	version, found, err := w.store.ProjectedVersion(readCtx, tree)
	return &ProjectionObservation{Version: version, Found: found, Err: err}
}

// lock takes the tree's lock, waiting at most w.lockWait.
func (w *TreeWriter) lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	start := time.Now()
	lockCtx, cancel := context.WithTimeout(ctx, w.lockWait)
	defer cancel()

	unlock, err := w.locker.Lock(lockCtx, treeID)
	if err == nil {
		return unlock, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("the lock on tree %s was not acquired; the caller's context ended after %s: %w",
			treeID, time.Since(start).Round(time.Millisecond), ctxErr)
	}
	if errors.Is(lockCtx.Err(), context.DeadlineExceeded) {
		return nil, &TreeLockWaitError{TreeID: treeID.String(), Waited: w.lockWait, Err: err}
	}
	return nil, fmt.Errorf("lock tree %s: %w", treeID, err)
}

// load rebuilds the tree in the engine from the store, or creates it empty
// when the store holds no rows. It returns the rows loaded.
func (w *TreeWriter) load(ctx context.Context, tree string, shape treeShape) (int, error) {
	n, err := w.loader.LoadTree(ctx, tree, shape.treeType, shape.loadOptions()...)
	if err != nil {
		return 0, fmt.Errorf("load tree %s; nothing was appended: %w", tree, err)
	}
	if n > 0 {
		return n, nil
	}
	if shape.treeType == treeTypeMatrix {
		err = w.engine.CreateMatrixTree(ctx, tree, shape.width, shape.spillover)
	} else {
		err = w.engine.CreateTree(ctx, tree, shape.treeType)
	}
	if err != nil {
		return 0, fmt.Errorf("create tree %s in the engine; nothing was appended: %w", tree, err)
	}
	return 0, nil
}

// checkLoadedVersion refuses a stream whose last version is neither the
// version the tree was loaded at nor one past it.
func checkLoadedVersion(tree string, loaded int64, found bool, last int64) error {
	if last == loaded || last == loaded+1 {
		return nil
	}
	if !found {
		return &ProjectionMissingError{TreeID: tree, LastVersion: last}
	}
	return &StreamMovedError{TreeID: tree, LoadedVersion: loaded, LastVersion: last}
}

// treeEventTypes are the event types catch-up redelivers.
var treeEventTypes = map[string]bool{
	EventTypeRootAdded:     true,
	EventTypeNodePlaced:    true,
	EventTypeNodeRemoved:   true,
	EventTypeEventRejected: true,
}

// catchUp redelivers the stream's last event through the consumer.
func (w *TreeWriter) catchUp(ctx context.Context, tree, stream string, last platform.Event, loaded int64, found bool) (*CaughtUpEvent, error) {
	// An unlisted type is refused rather than reported as caught up.
	if !treeEventTypes[last.Type] {
		return nil, fmt.Errorf("stream %s ends with event %s at version %d of type %q, which catch-up does not redeliver; nothing was appended",
			stream, last.ID, last.Version, last.Type)
	}
	if last.Type == EventTypeEventRejected {
		if pending := pendingRejection(tree, &last, loaded, found, nil); pending != nil {
			return nil, pending
		}
		if err := w.checkRejectionTarget(ctx, stream, last); err != nil {
			return nil, fmt.Errorf("%w; nothing was appended", err)
		}
	}
	if err := w.consumer.HandleEvent(ctx, last); err != nil {
		return nil, &CatchUpFailedError{TreeID: tree, EventID: last.ID, Version: last.Version, Type: last.Type, Err: err}
	}
	return &CaughtUpEvent{EventID: last.ID, Version: last.Version, Type: last.Type}, nil
}

// append appends one event at the expected version and returns the version it
// landed at. An error means the event is not known to have landed.
func (w *TreeWriter) append(ctx context.Context, stream string, expected int64, event platform.NewEvent) (int64, error) {
	version := expected + 1
	appendErr := w.events.Append(ctx, stream, expected, []platform.NewEvent{event})
	if appendErr == nil {
		return version, nil
	}
	var conflict *platform.ConcurrencyError
	if errors.As(appendErr, &conflict) {
		return 0, &appendConflictError{stream: stream, expected: expected, err: appendErr}
	}
	if errors.Is(appendErr, platform.ErrInvalidStreamName) || errors.Is(appendErr, platform.ErrEmptyAppend) {
		return 0, fmt.Errorf("append event %s to stream %s: %w", event.ID, stream, appendErr)
	}

	// The commit may have landed and its reply been lost. The read that decides
	// it must not end with the caller's context.
	readCtx, cancel := detachedRead(ctx)
	defer cancel()
	stored, readErr := w.events.ReadStream(readCtx, stream, version, 1)
	if readErr != nil {
		return 0, &AppendOutcomeUnknownError{
			Stream: stream, Version: version, EventID: event.ID, AppendErr: appendErr, ReadErr: readErr,
		}
	}
	if len(stored) == 1 && stored[0].Version == version {
		if sameUUID(stored[0].ID, event.ID) {
			return version, nil
		}
		return 0, fmt.Errorf("append event %s to stream %s at version %d returned: %w; a read of that version found event %s, so event %s was not appended",
			event.ID, stream, version, appendErr, stored[0].ID, event.ID)
	}
	return 0, fmt.Errorf("append event %s to stream %s at version %d returned: %w; a read of that version found no event",
		event.ID, stream, version, appendErr)
}

// project reads the event stored at version and projects that copy.
func (w *TreeWriter) project(ctx context.Context, stream, eventID string, version int64) error {
	stored, err := w.events.ReadStream(ctx, stream, version, 1)
	if err != nil {
		return fmt.Errorf("read back event %s at version %d in stream %s: %w", eventID, version, stream, err)
	}
	if len(stored) == 0 {
		return fmt.Errorf("read back at version %d in stream %s found no event; event %s was not projected",
			version, stream, eventID)
	}
	if stored[0].Version != version {
		return fmt.Errorf("read back at version %d in stream %s returned event %s at version %d; event %s was not projected",
			version, stream, stored[0].ID, stored[0].Version, eventID)
	}
	if !sameUUID(stored[0].ID, eventID) {
		return fmt.Errorf("stream %s holds event %s at version %d, where event %s was appended; nothing was projected",
			stream, stored[0].ID, version, eventID)
	}
	if stored[0].Type == EventTypeEventRejected {
		if err := w.checkRejectionTarget(ctx, stream, stored[0]); err != nil {
			return fmt.Errorf("project event %s at version %d in stream %s: %w", eventID, version, stream, err)
		}
	}
	if err := w.consumer.HandleEvent(ctx, stored[0]); err != nil {
		return fmt.Errorf("project event %s at version %d in stream %s: %w", eventID, version, stream, err)
	}
	return nil
}

// newTreeEvent builds an event with a new ID.
func newTreeEvent(eventType string, payload any) (platform.NewEvent, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return platform.NewEvent{}, fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	return platform.NewEvent{ID: uuid.NewString(), Type: eventType, Payload: data}, nil
}
