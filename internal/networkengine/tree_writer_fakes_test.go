package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// writeTime is the enrolment and removal time the writer tests use.
var writeTime = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// Identifiers the writer unit tests share.
var (
	writerTree  = testTreeUUID(1)
	writerRoot  = testUserUUID(1)
	writerChild = testUserUUID(2)
	writerOther = testUserUUID(3)
)

// fakeWriterEngine stands in for the engine in writer tests.
type fakeWriterEngine struct {
	trees    map[string]*fakeEngineTree
	checkErr error
	checks   []Mutation
	// calls logs the requests made, in order.
	calls []string
	// placements holds each placement call, in the form of its check.
	placements []Mutation
	// failAdd fails every add of the keyed user.
	failAdd map[string]error
}

type fakeEngineTree struct {
	root  string
	nodes map[string]EnginePosition
}

var _ TreeEngineChecker = (*fakeWriterEngine)(nil)

func newFakeWriterEngine() *fakeWriterEngine {
	return &fakeWriterEngine{trees: map[string]*fakeEngineTree{}, failAdd: map[string]error{}}
}

func fakeEngineError(code, format string, args ...any) error {
	return &EngineError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (f *fakeWriterEngine) tree(structure string) (*fakeEngineTree, error) {
	t, ok := f.trees[structure]
	if !ok {
		return nil, fakeEngineError("STRUCTURE_NOT_FOUND", "no tree named '%s'", structure)
	}
	return t, nil
}

func (f *fakeWriterEngine) CreateTree(_ context.Context, structure, treeType string) error {
	f.calls = append(f.calls, "create_tree "+treeType)
	return f.createTree(structure)
}

func (f *fakeWriterEngine) createTree(structure string) error {
	if _, ok := f.trees[structure]; ok {
		return fakeEngineError("TREE_EXISTS", "tree '%s' already exists", structure)
	}
	f.trees[structure] = &fakeEngineTree{nodes: map[string]EnginePosition{}}
	return nil
}

func (f *fakeWriterEngine) CreateMatrixTree(_ context.Context, structure string, width int, spillover string) error {
	f.calls = append(f.calls, fmt.Sprintf("create_matrix_tree %d %s", width, spillover))
	return f.createTree(structure)
}

func (f *fakeWriterEngine) AddRoot(_ context.Context, structure, userID string, enrolledAt int64) error {
	if err, ok := f.failAdd[userID]; ok {
		return err
	}
	t, err := f.tree(structure)
	if err != nil {
		return err
	}
	if t.root != "" {
		return fakeEngineError(engineCodeRootAlreadyExists, "tree already has a root node")
	}
	if _, ok := t.nodes[userID]; ok {
		return fakeEngineError(engineCodeUserAlreadyExists, "user %s already exists in tree", userID)
	}
	t.root = userID
	t.nodes[userID] = EnginePosition{UserID: userID, EnrolledAt: enrolledAt}
	return nil
}

func (f *fakeWriterEngine) place(structure, userID, parentID, sponsorID string, position int, enrolledAt int64) error {
	if err, ok := f.failAdd[userID]; ok {
		return err
	}
	t, err := f.tree(structure)
	if err != nil {
		return err
	}
	if _, ok := t.nodes[userID]; ok {
		return fakeEngineError(engineCodeUserAlreadyExists, "user %s already exists in tree", userID)
	}
	parent, ok := t.nodes[parentID]
	if !ok {
		return fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", parentID)
	}
	p, s := parentID, sponsorID
	t.nodes[userID] = EnginePosition{
		UserID: userID, ParentUserID: &p, SponsorUserID: &s,
		Position: position, Depth: parent.Depth + 1, EnrolledAt: enrolledAt,
	}
	return nil
}

func (f *fakeWriterEngine) AddNode(_ context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error {
	params := map[string]any{}
	for _, opt := range opts {
		opt(params)
	}
	position, _ := params["position"].(int)
	f.placements = append(f.placements, CheckAddNode(userID, parentID, sponsorID, enrolledAt, opts...))
	return f.place(structure, userID, parentID, sponsorID, position, enrolledAt)
}

func (f *fakeWriterEngine) AddNodeAt(_ context.Context, structure, userID, parentID, sponsorID string, position int, enrolledAt int64) error {
	f.placements = append(f.placements, CheckAddNodeAt(userID, parentID, sponsorID, position, enrolledAt))
	return f.place(structure, userID, parentID, sponsorID, position, enrolledAt)
}

func (f *fakeWriterEngine) RemoveNode(_ context.Context, structure, userID string) ([]Responsored, error) {
	t, err := f.tree(structure)
	if err != nil {
		return nil, err
	}
	if _, ok := t.nodes[userID]; !ok {
		return nil, fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", userID)
	}
	delete(t.nodes, userID)
	if t.root == userID {
		t.root = ""
	}
	return []Responsored{}, nil
}

func (f *fakeWriterEngine) GetPosition(_ context.Context, structure, userID string) (*EnginePosition, error) {
	t, err := f.tree(structure)
	if err != nil {
		return nil, err
	}
	pos, ok := t.nodes[userID]
	if !ok {
		return nil, fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", userID)
	}
	return &pos, nil
}

func (f *fakeWriterEngine) CheckMutation(_ context.Context, structure string, m Mutation) error {
	f.calls = append(f.calls, "check "+m.op)
	f.checks = append(f.checks, m)
	t, err := f.tree(structure)
	if err != nil {
		return err
	}
	if parent, ok := m.params["parent_id"].(string); ok {
		if _, found := t.nodes[parent]; !found {
			return fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", parent)
		}
	}
	return f.checkErr
}

// writerEnv holds the stores one test's writes share.
type writerEnv struct {
	events platform.EventStore
	store  *MemoryTreeStore
	locker *MemoryTreeLocker
}

func newWriterEnv() *writerEnv {
	return &writerEnv{
		events: platform.NewMemoryEventStore(),
		store:  NewMemoryTreeStore(),
		locker: NewMemoryTreeLocker(),
	}
}

// writer builds a TreeWriter over a fresh engine.
func (e *writerEnv) writer(opts ...TreeWriterOption) (*TreeWriter, *fakeWriterEngine) {
	engine := newFakeWriterEngine()
	return NewTreeWriter(e.events, e.store, engine, e.locker, opts...), engine
}

// mustAddRoot adds writerRoot as the root of writerTree through a writer.
func mustAddRoot(t *testing.T, env *writerEnv, treeType string) {
	t.Helper()
	req := AddRootRequest{
		TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot,
		TreeType: treeType, EnrolledAt: writeTime,
	}
	if treeType == treeTypeMatrix {
		width, spillover := 3, "breadth_first"
		req.MatrixWidth, req.MatrixSpillover = &width, &spillover
	}
	w, _ := env.writer()
	res, err := w.AddRoot(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
}

// streamEvents reads a whole stream.
func streamEvents(t *testing.T, events platform.EventStore, stream string) []platform.Event {
	t.Helper()
	got, err := events.ReadStream(context.Background(), stream, 1, 0)
	require.NoError(t, err)
	return got
}

// refusingLocker fails the test if the writer asks it for a lock.
type refusingLocker struct{ t *testing.T }

func (l refusingLocker) Lock(_ context.Context, treeID uuid.UUID) (func() error, error) {
	l.t.Errorf("Lock was called for tree %s", treeID)
	return nil, errors.New("refusingLocker: Lock was called")
}

// recordingLocker records each lock request in the engine's call log.
type recordingLocker struct {
	inner  TreeLocker
	engine *fakeWriterEngine
}

func (l recordingLocker) Lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	l.engine.calls = append(l.engine.calls, "lock")
	return l.inner.Lock(ctx, treeID)
}

// releaseFailingLocker grants every lock and fails every release.
type releaseFailingLocker struct{ err error }

func (l releaseFailingLocker) Lock(context.Context, uuid.UUID) (func() error, error) {
	return func() error { return l.err }, nil
}

// scriptedEvents is a MemoryEventStore whose Append and ReadStream a test can
// script once its setup is done.
type scriptedEvents struct {
	*platform.MemoryEventStore
	// afterAppend runs once, after the next Append, failed or not.
	afterAppend func()
	// readAs replaces what ReadStream returns when read from readAsVersion.
	readAs        *platform.Event
	readAsVersion int64
	// appendErr is what the next Append returns. commitFirst writes its events
	// to the inner store before returning it.
	appendErr   error
	commitFirst bool
	// readErr is what every ReadStream returns after a scripted Append failure.
	readErr error
	failed  bool
}

func (s *scriptedEvents) Append(ctx context.Context, stream string, expected int64, events []platform.NewEvent) error {
	if s.appendErr != nil {
		err := s.appendErr
		s.appendErr, s.failed = nil, true
		if s.commitFirst {
			if innerErr := s.MemoryEventStore.Append(ctx, stream, expected, events); innerErr != nil {
				return innerErr
			}
		}
		s.runAfterAppend()
		return err
	}
	if err := s.MemoryEventStore.Append(ctx, stream, expected, events); err != nil {
		return err
	}
	s.runAfterAppend()
	return nil
}

func (s *scriptedEvents) runAfterAppend() {
	if s.afterAppend != nil {
		hook := s.afterAppend
		s.afterAppend = nil
		hook()
	}
}

func (s *scriptedEvents) ReadStream(ctx context.Context, stream string, from, limit int64) ([]platform.Event, error) {
	if s.failed && s.readErr != nil {
		return nil, s.readErr
	}
	if s.readAs != nil && from == s.readAsVersion {
		return []platform.Event{*s.readAs}, nil
	}
	return s.MemoryEventStore.ReadStream(ctx, stream, from, limit)
}

// mustPlace places user under writerRoot through a writer.
func mustPlace(t *testing.T, env *writerEnv, user string, position *int) WriteResult {
	t.Helper()
	w, _ := env.writer()
	res, err := w.Place(context.Background(), PlaceRequest{
		TreeID: writerTree, UserID: user, ParentID: writerRoot, SponsorID: writerRoot,
		Position: position, EnrolledAt: writeTime,
	})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	return res
}

// appendDirect appends one event to writerTree's stream without projecting it.
func appendDirect(t *testing.T, events platform.EventStore, eventType string, payload any) platform.Event {
	t.Helper()
	ctx := context.Background()
	stream := TreeStreamName(writerTree)
	last, err := events.ReadLastEvent(ctx, stream)
	require.NoError(t, err)
	var expected int64
	if last != nil {
		expected = last.Version
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, events.Append(ctx, stream, expected, []platform.NewEvent{{
		ID: uuid.NewString(), Type: eventType, Payload: data,
	}}))
	stored, err := events.ReadStream(ctx, stream, expected+1, 1)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	return stored[0]
}

// orderLog records calls across wrapped stores in the order they happen.
type orderLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *orderLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *orderLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

// indexOf returns the position of the first matching entry, or -1.
func (l *orderLog) indexOf(entry string) int {
	return slices.Index(l.snapshot(), entry)
}

// loggingStore records each insert and each full-tree load.
type loggingStore struct {
	TreeStore
	log  *orderLog
	name string
}

func (s *loggingStore) InsertNode(ctx context.Context, node TreeNodeRow) error {
	s.log.add(s.name + " insert " + node.UserID)
	return s.TreeStore.InsertNode(ctx, node)
}

func (s *loggingStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	s.log.add(s.name + " load")
	return s.TreeStore.GetByTreeDepthOrdered(ctx, treeID)
}

// loggingEvents records each append by the version it asks for.
type loggingEvents struct {
	platform.EventStore
	log *orderLog
}

func (e *loggingEvents) Append(ctx context.Context, stream string, expected int64, events []platform.NewEvent) error {
	e.log.add(fmt.Sprintf("append at %d", expected+1))
	return e.EventStore.Append(ctx, stream, expected, events)
}
