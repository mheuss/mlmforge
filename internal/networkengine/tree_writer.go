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

// TreeLockWaitError reports a tree lock the writer waited for and did not
// acquire.
type TreeLockWaitError struct {
	TreeID string
	Waited time.Duration
}

func (e *TreeLockWaitError) Error() string {
	return fmt.Sprintf("waited %s for the lock on tree %s and did not acquire it", e.Waited, e.TreeID)
}

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

// WriteResult describes one write. EventID and Version are set once the
// append is confirmed.
type WriteResult struct {
	Stream        string
	EventID       string
	Version       int64
	ProjectionErr error // any failure after the append was confirmed
	ReleaseErr    error // the unlock failed
}

// TreeWriter appends tree events and projects them, one tree at a time.
type TreeWriter struct {
	events   platform.EventStore
	engine   TreeEngineChecker
	locker   TreeLocker
	loader   *TreeLoader
	consumer *TreeEventConsumer
	lockWait time.Duration
}

// TreeWriterOption configures a TreeWriter.
type TreeWriterOption func(*TreeWriter)

// WithLockWait sets how long each write waits for its tree's lock.
func WithLockWait(d time.Duration) TreeWriterOption {
	return func(w *TreeWriter) { w.lockWait = d }
}

// NewTreeWriter creates a writer over one event store, tree store, engine and
// locker.
func NewTreeWriter(events platform.EventStore, store TreeStore, engine TreeEngineChecker,
	locker TreeLocker, opts ...TreeWriterOption) *TreeWriter {
	w := &TreeWriter{
		events:   events,
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
		if shape.treeType != r.TreeType {
			return WriteResult{}, rootTypeConflict(tree, stream, shape.treeType, r.TreeType)
		}
	} else {
		if requested, err = shapeFromRequest(tree, r.TreeType, r.MatrixWidth, r.MatrixSpillover); err != nil {
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
	return w.write(ctx, spec)
}

// rootTypeConflict refuses a root whose requested type differs from the one
// version 1 records.
func rootTypeConflict(tree, stream, recorded, requested string) error {
	return fmt.Errorf("add root to tree %s: stream %s records tree type %s at version 1, and the request names %s",
		tree, stream, recorded, requested)
}

// readShape reads version 1 of a stream. found is false for an empty stream.
func (w *TreeWriter) readShape(ctx context.Context, stream string) (shape treeShape, found bool, err error) {
	first, err := w.events.ReadStream(ctx, stream, 1, 1)
	if err != nil {
		return treeShape{}, false, fmt.Errorf("read version 1 of stream %s: %w", stream, err)
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
}

// write runs the locked part of every operation.
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
	event, check, err := spec.build(shape)
	if err != nil {
		return result, err
	}
	if err := w.load(ctx, tree, shape); err != nil {
		return result, err
	}
	last, err := w.events.ReadLastEvent(ctx, stream)
	if err != nil {
		return result, fmt.Errorf("read the last event of stream %s; nothing was appended: %w", stream, err)
	}
	var expected int64
	if last != nil {
		expected = last.Version
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
	return result, nil
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
		return nil, &TreeLockWaitError{TreeID: treeID.String(), Waited: w.lockWait}
	}
	return nil, fmt.Errorf("lock tree %s: %w", treeID, err)
}

// load rebuilds the tree in the engine from the store, or creates it empty
// when the store holds no rows.
func (w *TreeWriter) load(ctx context.Context, tree string, shape treeShape) error {
	n, err := w.loader.LoadTree(ctx, tree, shape.treeType, shape.loadOptions()...)
	if err != nil {
		return fmt.Errorf("load tree %s; nothing was appended: %w", tree, err)
	}
	if n > 0 {
		return nil
	}
	if shape.treeType == treeTypeMatrix {
		err = w.engine.CreateMatrixTree(ctx, tree, shape.width, shape.spillover)
	} else {
		err = w.engine.CreateTree(ctx, tree, shape.treeType)
	}
	if err != nil {
		return fmt.Errorf("create tree %s in the engine; nothing was appended: %w", tree, err)
	}
	return nil
}

// append appends one event at the expected version and returns the version it
// landed at.
func (w *TreeWriter) append(ctx context.Context, stream string, expected int64, event platform.NewEvent) (int64, error) {
	err := w.events.Append(ctx, stream, expected, []platform.NewEvent{event})
	if err == nil {
		return expected + 1, nil
	}
	var conflict *platform.ConcurrencyError
	if errors.As(err, &conflict) {
		return 0, &appendConflictError{stream: stream, expected: expected, err: err}
	}
	return 0, fmt.Errorf("append event %s to stream %s at version %d: %w", event.ID, stream, expected+1, err)
}

// project reads the event stored at version and projects that copy. Its error
// is the write's ProjectionErr.
func (w *TreeWriter) project(ctx context.Context, stream, eventID string, version int64) error {
	stored, err := w.events.ReadStream(ctx, stream, version, 1)
	if err != nil {
		return fmt.Errorf("read back event %s at version %d in stream %s: %w", eventID, version, stream, err)
	}
	if len(stored) == 0 || stored[0].Version != version {
		return fmt.Errorf("read back at version %d in stream %s found no event; event %s was not projected",
			version, stream, eventID)
	}
	if !sameUUID(stored[0].ID, eventID) {
		return fmt.Errorf("stream %s holds event %s at version %d, where event %s was appended; nothing was projected",
			stream, stored[0].ID, version, eventID)
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
